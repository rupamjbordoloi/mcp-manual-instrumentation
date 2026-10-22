package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otelc/pkg/hook/hooktest"
)

// connect and closeSession run the four session hooks the way otelc does around
// (*Server).Connect and (*ServerSession).Close.
func connect(session *mcp.ServerSession) {
	ictx := hooktest.NewMockHookContext()
	BeforeServerConnect(ictx, nil, nil, nil, nil)
	AfterServerConnect(ictx, session, nil)
}

func closeSession(session *mcp.ServerSession, err error) {
	ictx := hooktest.NewMockHookContext(session)
	BeforeServerSessionClose(ictx, session)
	AfterServerSessionClose(ictx, err)
}

func TestSessionLifecycle(t *testing.T) {
	t.Run("a session counts as created and active, then closed with a duration", func(t *testing.T) {
		tel := useTestTelemetry(t)
		session := &mcp.ServerSession{}

		connect(session)

		assert.Equal(t, int64(1), tel.sum(t, "mcp.session.created"))
		assert.Equal(t, int64(1), tel.sum(t, "mcp.session.active"))
		assert.Zero(t, tel.sum(t, "mcp.session.closed"))

		closeSession(session, nil)

		assert.Equal(t, int64(1), tel.sum(t, "mcp.session.created"))
		assert.Equal(t, int64(1), tel.sum(t, "mcp.session.closed"))
		assert.Zero(t, tel.sum(t, "mcp.session.active"), "active returns to zero")

		count, sum := tel.hist(t, "mcp.session.duration")
		assert.Equal(t, uint64(1), count)
		assert.GreaterOrEqual(t, sum, 0.0)

		_, tracked := serverSessionStart.Load(session)
		assert.False(t, tracked, "the start time is released, so the map does not grow")
	})

	t.Run("concurrent sessions are tracked independently", func(t *testing.T) {
		tel := useTestTelemetry(t)
		a, b, c := &mcp.ServerSession{}, &mcp.ServerSession{}, &mcp.ServerSession{}

		connect(a)
		connect(b)
		connect(c)
		closeSession(b, nil)

		assert.Equal(t, int64(3), tel.sum(t, "mcp.session.created"))
		assert.Equal(t, int64(2), tel.sum(t, "mcp.session.active"))
		assert.Equal(t, int64(1), tel.sum(t, "mcp.session.closed"))
	})

	t.Run("Close is idempotent: a second close is not counted again", func(t *testing.T) {
		tel := useTestTelemetry(t)
		session := &mcp.ServerSession{}

		connect(session)
		closeSession(session, nil)
		closeSession(session, nil)

		assert.Equal(t, int64(1), tel.sum(t, "mcp.session.closed"))
		assert.Zero(t, tel.sum(t, "mcp.session.active"), "never goes negative")
		count, _ := tel.hist(t, "mcp.session.duration")
		assert.Equal(t, uint64(1), count)
	})

	t.Run("closing a session that was never tracked records nothing", func(t *testing.T) {
		tel := useTestTelemetry(t)

		closeSession(&mcp.ServerSession{}, nil)

		assert.Zero(t, tel.sum(t, "mcp.session.closed"))
		assert.Zero(t, tel.sum(t, "mcp.session.active"))
	})

	t.Run("a close error is recorded as error.type but the session is still closed", func(t *testing.T) {
		tel := useTestTelemetry(t)
		session := &mcp.ServerSession{}

		connect(session)
		closeSession(session, errors.New("transport broke"))

		count, _ := tel.hist(t, "mcp.session.duration", kv(attrErrorType, "*errors.errorString"))
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, int64(1), tel.sum(t, "mcp.session.closed"))
		assert.Zero(t, tel.sum(t, "mcp.session.active"))
	})
}

func TestAfterServerConnect_Ignored(t *testing.T) {
	t.Run("a failed Connect creates no session", func(t *testing.T) {
		tel := useTestTelemetry(t)
		ictx := hooktest.NewMockHookContext()
		BeforeServerConnect(ictx, nil, nil, nil, nil)

		AfterServerConnect(ictx, &mcp.ServerSession{}, errors.New("connect failed"))

		assert.Zero(t, tel.sum(t, "mcp.session.created"))
		assert.Zero(t, tel.sum(t, "mcp.session.active"))
	})

	t.Run("a nil session is ignored", func(t *testing.T) {
		tel := useTestTelemetry(t)
		ictx := hooktest.NewMockHookContext()
		BeforeServerConnect(ictx, nil, nil, nil, nil)

		assert.NotPanics(t, func() { AfterServerConnect(ictx, nil, nil) })
		assert.Zero(t, tel.sum(t, "mcp.session.created"))
	})

	t.Run("without the before hook's start time nothing is recorded", func(t *testing.T) {
		tel := useTestTelemetry(t)

		AfterServerConnect(hooktest.NewMockHookContext(), &mcp.ServerSession{}, nil)

		assert.Zero(t, tel.sum(t, "mcp.session.created"))
	})
}

func TestSessionHooks_Disabled(t *testing.T) {
	tel := useTestTelemetry(t)
	t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)
	session := &mcp.ServerSession{}

	connect(session)
	closeSession(session, nil)

	assert.Zero(t, tel.sum(t, "mcp.session.created"))
	assert.Zero(t, tel.sum(t, "mcp.session.closed"))
	count, _ := tel.hist(t, "mcp.session.duration")
	assert.Zero(t, count)
}

func TestToolRegistryHooks(t *testing.T) {
	add := func(name string) {
		ictx := hooktest.NewMockHookContext()
		BeforeServerAddTool(ictx, nil, &mcp.Tool{Name: name}, nil)
		AfterServerAddTool(ictx)
	}

	t.Run("a registered tool keeps its name as a label", func(t *testing.T) {
		useTestTelemetry(t)

		add("greet")

		assert.Equal(t, "greet", metricToolName("greet"))
		assert.Equal(t, otherAttrValue, metricToolName("other"))
	})

	t.Run("several tools", func(t *testing.T) {
		useTestTelemetry(t)

		add("a")
		add("b")

		assert.Equal(t, "a", metricToolName("a"))
		assert.Equal(t, "b", metricToolName("b"))
	})

	t.Run("a nil tool is ignored", func(t *testing.T) {
		useTestTelemetry(t)
		ictx := hooktest.NewMockHookContext()

		assert.NotPanics(t, func() {
			BeforeServerAddTool(ictx, nil, nil, nil)
			AfterServerAddTool(ictx)
		})
	})

	t.Run("a name is registered only after AddTool returned (After without Before does nothing)", func(t *testing.T) {
		useTestTelemetry(t)
		ictx := hooktest.NewMockHookContext()
		BeforeServerAddTool(ictx, nil, &mcp.Tool{Name: "greet"}, nil)

		assert.Equal(t, otherAttrValue, metricToolName("greet"), "AddTool has not returned yet")

		AfterServerAddTool(hooktest.NewMockHookContext())
		assert.Equal(t, otherAttrValue, metricToolName("greet"), "an After with no matching Before registers nothing")

		AfterServerAddTool(ictx)
		assert.Equal(t, "greet", metricToolName("greet"))
	})

	t.Run("disabled: nothing is registered", func(t *testing.T) {
		useTestTelemetry(t)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)

		add("greet")

		assert.Equal(t, otherAttrValue, metricToolName("greet"))
	})
}

// serve runs a request through the transport hooks and a handler, as otelc
// does around (*StreamableHTTPHandler).ServeHTTP.
func serve(method, body string, handler http.HandlerFunc) (*httptest.ResponseRecorder, http.ResponseWriter) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))

	ictx := hooktest.NewMockHookContext(nil, rec, req)
	BeforeTransportRequest(ictx, nil, rec, req)

	w, _ := ictx.GetParam(1).(http.ResponseWriter) // what the handler receives
	handler(w, req)

	AfterTransportRequest(ictx)

	return rec, w
}

func TestTransportRequestHooks(t *testing.T) {
	t.Run("the handler gets a counting writer and the client still gets the response", func(t *testing.T) {
		tel := useTestTelemetry(t)

		rec, w := serve(http.MethodPost, `{"a":1}`, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("response"))
		})

		assert.IsType(t, &transportWriter{}, w)
		assert.Equal(t, "response", rec.Body.String())

		_, received := tel.hist(t, "mcp.message.size", kv(attrMCPMessageDirection, directionReceived))
		_, sent := tel.hist(t, "mcp.message.size", kv(attrMCPMessageDirection, directionSent))
		assert.Equal(t, float64(len(`{"a":1}`)), received)
		assert.Equal(t, float64(len("response")), sent)
	})

	t.Run("a rejected request is a transport error", func(t *testing.T) {
		tel := useTestTelemetry(t)

		serve(http.MethodPost, "", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusBadRequest) })

		assert.Equal(t, int64(1), tel.sum(t, "mcp.transport.errors.total"))
	})

	t.Run("an SSE-style response can still be flushed through the wrapper", func(t *testing.T) {
		useTestTelemetry(t)

		rec, _ := serve(http.MethodGet, "", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: x\n\n"))
			assert.NoError(t, http.NewResponseController(w).Flush())
		})

		assert.True(t, rec.Flushed)
	})

	t.Run("disabled: the writer is left alone and nothing is recorded", func(t *testing.T) {
		tel := useTestTelemetry(t)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)

		rec, w := serve(http.MethodPost, "x", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })

		assert.Same(t, rec, w)
		assert.Zero(t, tel.sum(t, "mcp.transport.errors.total"))
	})

	t.Run("AfterTransportRequest without a before hook does nothing", func(t *testing.T) {
		tel := useTestTelemetry(t)

		require.NotPanics(t, func() { AfterTransportRequest(hooktest.NewMockHookContext()) })
		assert.Zero(t, tel.sum(t, "mcp.transport.errors.total"))
	})
}
