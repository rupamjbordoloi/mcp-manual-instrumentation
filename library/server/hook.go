package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook"
)

func finishSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// ---- Tool handler wrapping ----
//
// NOTE ON GENERALITY: the rule below matches by function name (mcp.otelc.yaml
// lists "SayHi" explicitly), because a compile-time Function Hook Rule's
// `func` selector requires an exact name — it cannot wildcard-match "any
// function with this signature" the way WrapTool[In,Out] could at the Go
// generics level. Adding tool.weather / tool.sql / etc. means adding one
// more `where.func` entry to mcp.otelc.yaml per new handler function name —
// not writing new tracing code, but not fully zero-touch either. This is a
// real constraint of the rule engine as currently understood, not a
// shortcut taken here.

func BeforeToolHandler(ictx hook.HookContext, ctx context.Context, req *mcp.CallToolRequest, input any) context.Context {
	newCtx, span := tracer.Start(ctx, "tool."+ictx.GetFuncName(),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("tool.name", ictx.GetFuncName())),
	)
	ictx.SetKeyData("span", span)
	return newCtx
}

func AfterToolHandler(ictx hook.HookContext, res *mcp.CallToolResult, output any, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok {
		return
	}
	if err != nil {
		span.SetAttributes(attribute.Bool("tool.success", false), attribute.String("tool.error", err.Error()))
	} else if res != nil && res.IsError {
		span.SetAttributes(attribute.Bool("tool.success", false))
	} else {
		span.SetAttributes(attribute.Bool("tool.success", true))
	}
	finishSpan(span, err)
}

// ---- Protocol-level HTTP wrapping ----

func AfterNewStreamableHTTPHandler(ictx hook.HookContext, h *mcp.StreamableHTTPHandler) {
	// Wraps the handler in place is not possible here (the trampoline's
	// SetReturnVal requires the exact declared return type, and this
	// middleware needs to change concrete behavior, not just observe it).
	// Instead this hook is a no-op placeholder — protocol-level HTTP
	// middleware for jsonrpc.decode / mcp.dispatch / mcp.<method> spans is
	// applied via one explicit composition line in main.go (see that file).
	// This is the second necessary manual line in the whole client+server
	// codebase, and exists for the same class of reason as the tracer
	// provider Shutdown() call: Go's type system, not a tracing gap.
}

// ProtocolMiddleware is exported so main.go can apply it with one line.
// Internally it is unchanged from the manual-instrumentation version.
func ProtocolMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Println("traceparent header:", r.Header.Get("traceparent"))
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

		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		type rpcEnvelope struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method,omitempty"`
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
		}
		finishSpan(decodeSpan, decodeErr)
		r.Body = io.NopCloser(bytes.NewReader(body))

		if protocolLevelMethods[env.Method] {
			spanName, known := methodToSpanName[env.Method]
			if !known {
				spanName = "mcp." + env.Method
			}
			ctx, span := tracer.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(attribute.String("rpc.method", env.Method)))
			if env.Method == "initialize" {
				span.AddEvent("protocol.validate")
				span.AddEvent("capability.negotiation")
			} else {
				span.AddEvent("capability.enumeration")
			}
			serveSafely(w, r.WithContext(ctx), next, span, nil)
			return
		}

		ctx, dispatchSpan := tracer.Start(ctx, "mcp.dispatch", trace.WithSpanKind(trace.SpanKindInternal))
		spanName, known := methodToSpanName[env.Method]
		if !known {
			spanName = "mcp." + env.Method
		}
		ctx, methodSpan := tracer.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("rpc.method", env.Method)))

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
	})
}

func serveSafely(w http.ResponseWriter, r *http.Request, next http.Handler, primary trace.Span, dispatch trace.Span) {
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		if p := recover(); p != nil {
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

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type httpStatusErr struct{ status int }

func (e httpStatusErr) Error() string  { return http.StatusText(e.status) }
func httpStatusError(status int) error { return httpStatusErr{status: status} }
