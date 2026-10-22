package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

func TestTransportWriter(t *testing.T) {
	t.Run("an untouched response reports 200", func(t *testing.T) {
		w := &transportWriter{ResponseWriter: httptest.NewRecorder()}

		assert.Equal(t, http.StatusOK, w.statusCode())
	})

	t.Run("writing a body implies 200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := &transportWriter{ResponseWriter: rec}

		n, err := w.Write([]byte("hello"))

		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, http.StatusOK, w.statusCode())
		assert.Equal(t, int64(5), w.written.Load())
		assert.Equal(t, "hello", rec.Body.String(), "the body still reaches the client")
	})

	t.Run("the first final status wins", func(t *testing.T) {
		w := &transportWriter{ResponseWriter: httptest.NewRecorder()}

		w.WriteHeader(http.StatusNotFound)
		w.WriteHeader(http.StatusInternalServerError)

		assert.Equal(t, http.StatusNotFound, w.statusCode())
	})

	t.Run("informational 1xx statuses do not count as the result", func(t *testing.T) {
		w := &transportWriter{ResponseWriter: httptest.NewRecorder()}

		w.WriteHeader(http.StatusEarlyHints)
		assert.Equal(t, http.StatusOK, w.statusCode(), "still nothing final")

		w.WriteHeader(http.StatusAccepted)
		assert.Equal(t, http.StatusAccepted, w.statusCode())
	})

	t.Run("a status set before a body is kept", func(t *testing.T) {
		w := &transportWriter{ResponseWriter: httptest.NewRecorder()}

		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad"))

		assert.Equal(t, http.StatusBadRequest, w.statusCode())
	})

	t.Run("bytes are summed across writes", func(t *testing.T) {
		w := &transportWriter{ResponseWriter: httptest.NewRecorder()}

		_, _ = w.Write([]byte("abc"))
		_, _ = w.Write([]byte("de"))

		assert.Equal(t, int64(5), w.written.Load())
	})

	t.Run("passes the status and headers through", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := &transportWriter{ResponseWriter: rec}

		w.Header().Set("X-Test", "1")
		w.WriteHeader(http.StatusTeapot)

		assert.Equal(t, http.StatusTeapot, rec.Code)
		assert.Equal(t, "1", rec.Header().Get("X-Test"))
	})

	t.Run("Unwrap lets http.ResponseController reach the real writer, so SSE can flush", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := &transportWriter{ResponseWriter: rec}

		assert.Same(t, rec, w.Unwrap())
		require.NoError(t, http.NewResponseController(w).Flush())
		assert.True(t, rec.Flushed)
	})
}

func TestCountingBody(t *testing.T) {
	b := &countingBody{ReadCloser: io.NopCloser(strings.NewReader("hello world"))}

	data, err := io.ReadAll(b)

	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data), "the body is passed through unchanged")
	assert.Equal(t, int64(11), b.read.Load())
	assert.NoError(t, b.Close())
}

func TestNormalizeHTTPMethod(t *testing.T) {
	for _, m := range []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace,
	} {
		assert.Equal(t, m, normalizeHTTPMethod(m))
	}

	for _, m := range []string{"BREW", "get", "", "PROPFIND"} {
		assert.Equal(t, otherAttrValue, normalizeHTTPMethod(m), "%q is client-chosen, so it is bucketed", m)
	}
}

func TestBeginTransportObservation(t *testing.T) {
	t.Run("wraps the writer and the request body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("body"))

		obs, wrapped := beginTransportObservation(rec, req)

		assert.IsType(t, &transportWriter{}, wrapped)
		assert.NotNil(t, obs.body)
		assert.True(t, obs.isPost)
		assert.Equal(t, http.MethodPost, obs.method)

		_, err := io.ReadAll(req.Body) // req.Body was replaced in place
		require.NoError(t, err)
		assert.Equal(t, int64(4), obs.body.read.Load())
	})

	t.Run("a request without a body is not wrapped", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)

		obs, _ := beginTransportObservation(httptest.NewRecorder(), req)

		assert.Nil(t, obs.body)
		assert.Equal(t, http.NoBody, req.Body)
	})

	t.Run("a nil body is tolerated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Body = nil

		assert.NotPanics(t, func() { beginTransportObservation(httptest.NewRecorder(), req) })
	})

	t.Run("an unusual verb is bucketed", func(t *testing.T) {
		req := httptest.NewRequest("BREW", "/", nil)

		obs, _ := beginTransportObservation(httptest.NewRecorder(), req)

		assert.Equal(t, otherAttrValue, obs.method)
		assert.False(t, obs.isPost)
	})

	t.Run("keeps the request's context for exemplars", func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(context.Background(), key{}, "v")
		req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)

		obs, _ := beginTransportObservation(httptest.NewRecorder(), req)

		assert.Equal(t, "v", obs.ctx.Value(key{}))
	})
}

func TestTransportObservation_End(t *testing.T) {
	// run sends a request through a handler and ends the observation, as the
	// Before/AfterTransportRequest hooks do around (*StreamableHTTPHandler).ServeHTTP.
	run := func(t *testing.T, method, body string, handler http.HandlerFunc) *telemetry {
		t.Helper()

		tel := useTestTelemetry(t)
		req := httptest.NewRequest(method, "/", strings.NewReader(body))

		obs, w := beginTransportObservation(httptest.NewRecorder(), req)
		handler(w, req)
		obs.end()

		return tel
	}

	reply := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}

	received := kv(attrMCPMessageDirection, directionReceived)
	sent := kv(attrMCPMessageDirection, directionSent)

	t.Run("a successful POST records both message sizes and no error", func(t *testing.T) {
		tel := run(t, http.MethodPost, `{"jsonrpc":"2.0"}`, reply(200, "response!"))

		count, sum := tel.hist(t, "mcp.message.size", received)
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, float64(len(`{"jsonrpc":"2.0"}`)), sum)

		count, sum = tel.hist(t, "mcp.message.size", sent)
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, float64(len("response!")), sum)

		assert.Zero(t, tel.sum(t, "mcp.transport.errors.total"))
	})

	t.Run("a 4xx or 5xx counts a transport error with method, status and type", func(t *testing.T) {
		for _, status := range []int{400, 404, 415, 500, 503} {
			tel := run(t, http.MethodPost, "x", reply(status, "no"))

			assert.Equal(t, int64(1), tel.sum(t, "mcp.transport.errors.total",
				kv(attrHTTPRequestMethod, http.MethodPost),
				attribute.Int(attrHTTPResponseStatusCode, status),
				kv(attrErrorType, strconv.Itoa(status))), "status %d", status)
		}
	})

	t.Run("399 and below are not errors", func(t *testing.T) {
		for _, status := range []int{200, 202, 301, 304} {
			tel := run(t, http.MethodPost, "x", reply(status, ""))

			assert.Zero(t, tel.sum(t, "mcp.transport.errors.total"), "status %d", status)
		}
	})

	t.Run("a request rejected before any body is written still counts", func(t *testing.T) {
		tel := run(t, http.MethodPost, "", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })

		assert.Equal(t, int64(1), tel.sum(t, "mcp.transport.errors.total", attribute.Int(attrHTTPResponseStatusCode, 403)))
	})

	t.Run("an empty request body or response body records no size for that direction", func(t *testing.T) {
		tel := run(t, http.MethodPost, "", reply(202, ""))

		count, _ := tel.hist(t, "mcp.message.size")
		assert.Zero(t, count)
	})

	t.Run("a handler that writes nothing reports 200", func(t *testing.T) {
		tel := run(t, http.MethodPost, "", func(http.ResponseWriter, *http.Request) {})

		assert.Zero(t, tel.sum(t, "mcp.transport.errors.total"))
	})

	t.Run("GET (the SSE stream) and DELETE record no message size", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			tel := run(t, method, "body", reply(200, "stream bytes"))

			count, _ := tel.hist(t, "mcp.message.size")
			assert.Zero(t, count, method)
		}
	})

	t.Run("GET and DELETE failures are still transport errors", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			tel := run(t, method, "", reply(404, ""))

			assert.Equal(t, int64(1), tel.sum(t, "mcp.transport.errors.total", kv(attrHTTPRequestMethod, method)), method)
		}
	})

	t.Run("an unusual verb is labelled _OTHER", func(t *testing.T) {
		tel := run(t, "BREW", "", reply(405, ""))

		assert.Equal(t, int64(1), tel.sum(t, "mcp.transport.errors.total", kv(attrHTTPRequestMethod, otherAttrValue)))
	})
}
