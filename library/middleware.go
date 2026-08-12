package mcptrace

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// rpcEnvelope is the minimal shape needed to identify a JSON-RPC message,
// without depending on MCP SDK-internal types.
type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
}

// ProtocolMiddleware wraps an MCP HTTP handler (mcp.NewStreamableHTTPHandler)
// and adds protocol-level spans.
//
// For routed methods (tools/call, tools/list, resources/read, ...):
//
//	jsonrpc.decode -> mcp.dispatch -> mcp.<method> -> [tool.<name> nested inside]
//
// For "initialize", which is a handshake step rather than a routed call,
// mcp.dispatch is skipped entirely in favor of a direct mcp.initialize span
// carrying span events for the handshake's observable milestones.
//
// Place this INSIDE otelhttp.NewHandler so the existing net/http server
// span is always the parent of everything here — this middleware never
// creates a root span itself.
func ProtocolMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		switch r.Method {
		case http.MethodDelete:
			ctx, span := tracer.Start(ctx, "mcp.shutdown", trace.WithSpanKind(trace.SpanKindServer))
			serveSafely(w, r.WithContext(ctx), next, span, nil)
			return

		case http.MethodGet:
			ctx, span := tracer.Start(ctx, "mcp.stream", trace.WithSpanKind(trace.SpanKindServer))
			serveSafely(w, r.WithContext(ctx), next, span, nil)
			return
		}

		// POST: a JSON-RPC message. Buffer the body so it can be decoded
		// for span naming/attributes AND still handed intact to the real
		// MCP handler.
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var env rpcEnvelope
		ctx, decodeSpan := tracer.Start(ctx, "jsonrpc.decode", trace.WithSpanKind(trace.SpanKindInternal))
		decodeErr := json.Unmarshal(body, &env)
		if decodeErr == nil {
			decodeSpan.SetAttributes(
				attribute.String("rpc.system", "jsonrpc"),
				attribute.String("rpc.jsonrpc.version", env.JSONRPC),
				attribute.String("rpc.method", env.Method),
			)
			if len(env.ID) > 0 {
				decodeSpan.SetAttributes(attribute.String("mcp.request.id", string(env.ID)))
			}
		}
		finishSpan(decodeSpan, decodeErr)

		r.Body = io.NopCloser(bytes.NewReader(body))

		if env.Method == "initialize" {
			serveInitialize(w, r.WithContext(ctx), next, env)
			return
		}

		serveDispatched(w, r.WithContext(ctx), next, env, body)
	})
}

// serveInitialize handles the MCP handshake directly, without a generic
// mcp.dispatch layer — initialize doesn't route through the same
// tool/resource dispatch mechanism the SDK uses for ordinary calls.
// protocol.validate and capability.negotiation are recorded as span events,
// not child spans: there is no independent hook into the SDK's internal
// handshake logic to measure their real duration, so a child span here
// would carry a fabricated boundary rather than an observed one.
func serveInitialize(w http.ResponseWriter, r *http.Request, next http.Handler, env rpcEnvelope) {
	ctx, span := tracer.Start(r.Context(), "mcp.initialize",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", env.Method),
			attribute.String("mcp.protocol.version", ProtocolVersion),
		),
	)

	span.AddEvent("protocol.validate")
	span.AddEvent("capability.negotiation")

	serveSafely(w, r.WithContext(ctx), next, span, nil)
}

// serveDispatched handles every routed JSON-RPC method other than
// initialize: jsonrpc.decode (already finished by the caller) -> mcp.dispatch
// -> mcp.<method> -> whatever the handler nests inside (e.g. tool.<name>).
func serveDispatched(w http.ResponseWriter, r *http.Request, next http.Handler, env rpcEnvelope, body []byte) {
	ctx, dispatchSpan := tracer.Start(r.Context(), "mcp.dispatch",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("rpc.method", env.Method)),
	)

	spanName, known := methodToSpanName[env.Method]
	if !known {
		spanName = "mcp." + env.Method
	}

	ctx, methodSpan := tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", env.Method),
		),
	)

	if env.Method == "tools/call" {
		var full struct {
			Params json.RawMessage `json:"params"`
		}
		var params struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(body, &full) == nil && json.Unmarshal(full.Params, &params) == nil && params.Name != "" {
			methodSpan.SetAttributes(attribute.String("mcp.tool.name", params.Name))
		}
	}

	serveSafely(w, r.WithContext(ctx), next, methodSpan, dispatchSpan)
}

// serveSafely runs next.ServeHTTP with panic protection so open spans are
// always closed — including dispatchSpan when present — before the panic is
// re-raised for the standard library's own recovery/logging to handle.
//
// rec wraps w only to observe the final status code; it explicitly forwards
// Flush (and Hijack, for completeness) to the underlying ResponseWriter so
// streaming responses — critically, the MCP SDK's standalone SSE GET stream
// — continue to flush incrementally instead of silently buffering forever.
// Without this, rec satisfies http.ResponseWriter but not http.Flusher, the
// SSE handler's flush type-assertion fails, and the stream never sends
// anything until the client times out.
func serveSafely(w http.ResponseWriter, r *http.Request, next http.Handler, primary trace.Span, dispatch trace.Span) {
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	defer func() {
		if p := recover(); p != nil {
			primary.RecordError(errFromPanic(p))
			primary.SetStatus(codes.Error, "panic in handler")
			primary.End()
			if dispatch != nil {
				dispatch.SetStatus(codes.Error, "panic in handler")
				dispatch.End()
			}
			panic(p)
		}
	}()

	next.ServeHTTP(rec, r)

	var handlerErr error
	if rec.status >= 400 {
		handlerErr = httpStatusError(rec.status)
	}

	if dispatch != nil {
		finishSpan(dispatch, handlerErr)
	}

	if handlerErr != nil {
		primary.SetStatus(codes.Error, handlerErr.Error())
	} else {
		primary.SetStatus(codes.Ok, "")
	}
	primary.SetAttributes(attribute.Int("http.status_code", rec.status))
	primary.End()
}

// statusRecorder wraps http.ResponseWriter to capture the final status code
// while transparently forwarding the streaming-related interfaces the
// underlying writer supports, so wrapping it never silently disables
// flushing or connection hijacking for handlers that need them.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying ResponseWriter's Flush if it supports
// http.Flusher — required for SSE and any other streaming response.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type httpStatusErr struct{ status int }

func (e httpStatusErr) Error() string { return http.StatusText(e.status) }

func httpStatusError(status int) error { return httpStatusErr{status: status} }

type panicErr struct{ v any }

func (e panicErr) Error() string { return "panic: " + errString(e.v) }

func errFromPanic(v any) error { return panicErr{v: v} }

func errString(v any) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return "unknown panic value"
}
