// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package server

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// transportWriter records the status code and body bytes of a response.
//
// The MCP SDK flushes SSE streams through http.NewResponseController, which
// finds the real writer via Unwrap, so this wrapper does not need to
// implement http.Flusher itself. Counters are atomic because SSE events can
// be written from a goroutine other than the one running ServeHTTP.
type transportWriter struct {
	http.ResponseWriter
	status  atomic.Int32 // first final (>= 200) status code; 0 until written
	written atomic.Int64
}

func (w *transportWriter) WriteHeader(code int) {
	// Informational (1xx) headers may be sent before the final status.
	if code >= http.StatusOK {
		w.status.CompareAndSwap(0, int32(code))
	}

	w.ResponseWriter.WriteHeader(code)
}

func (w *transportWriter) Write(b []byte) (int, error) {
	// Writing a body without WriteHeader implies 200 OK.
	w.status.CompareAndSwap(0, http.StatusOK)

	n, err := w.ResponseWriter.Write(b)
	w.written.Add(int64(n))

	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *transportWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// statusCode returns the recorded status, or 200 if the handler wrote nothing
// (net/http then sends 200 OK).
func (w *transportWriter) statusCode() int {
	if s := w.status.Load(); s != 0 {
		return int(s)
	}

	return http.StatusOK
}

// countingBody counts the bytes read from a request body.
type countingBody struct {
	io.ReadCloser
	read atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read.Add(int64(n))

	return n, err
}

// transportObservation measures one request handled by the Streamable HTTP
// handler, from BeforeTransportRequest to AfterTransportRequest.
type transportObservation struct {
	ctx    context.Context
	method string // normalized for use as an attribute
	isPost bool
	writer *transportWriter
	body   *countingBody // nil when the request had no body
}

// beginTransportObservation wraps the response writer and request body so
// their sizes and the final status can be read afterwards. It returns the
// writer the handler must use in place of w; req.Body is replaced in place.
func beginTransportObservation(w http.ResponseWriter, req *http.Request) (*transportObservation, http.ResponseWriter) {
	tw := &transportWriter{ResponseWriter: w}
	obs := &transportObservation{
		ctx:    req.Context(),
		method: normalizeHTTPMethod(req.Method),
		isPost: req.Method == http.MethodPost,
		writer: tw,
	}

	if req.Body != nil && req.Body != http.NoBody {
		obs.body = &countingBody{ReadCloser: req.Body}
		req.Body = obs.body
	}

	return obs, tw
}

// end records mcp.transport.errors.total for responses with a status of 400
// or above, and mcp.message.size for POST requests. Only POST carries a
// discrete MCP message in each direction: GET is a long-lived SSE stream and
// DELETE has no body, so measuring them would not describe a message.
func (o *transportObservation) end() {
	m := metrics()

	if status := o.writer.statusCode(); status >= http.StatusBadRequest {
		m.transportErrors.Add(o.ctx, 1, metric.WithAttributes(
			attribute.String(attrHTTPRequestMethod, o.method),
			attribute.Int(attrHTTPResponseStatusCode, status),
			attribute.String(attrErrorType, strconv.Itoa(status)),
		))
	}

	if !o.isPost {
		return
	}

	if o.body != nil {
		if n := o.body.read.Load(); n > 0 {
			m.messageSize.Record(o.ctx, n, metric.WithAttributes(
				attribute.String(attrMCPMessageDirection, directionReceived)))
		}
	}

	if n := o.writer.written.Load(); n > 0 {
		m.messageSize.Record(o.ctx, n, metric.WithAttributes(
			attribute.String(attrMCPMessageDirection, directionSent)))
	}
}

// normalizeHTTPMethod maps unknown verbs to _OTHER, as the HTTP semantic
// conventions require, because the verb is client-controlled.
func normalizeHTTPMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect,
		http.MethodOptions, http.MethodTrace:
		return method
	}

	return otherAttrValue
}
