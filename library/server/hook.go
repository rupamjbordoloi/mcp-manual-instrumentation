// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook"
	"go.opentelemetry.io/otelc/pkg/runtime"
)

var (
	logger   = runtime.Logger()
	tracer   trace.Tracer
	initOnce sync.Once
)

func initInstrumentation() {
	initOnce.Do(func() {
		tracer = otel.GetTracerProvider().Tracer(
			instrumentationName,
			trace.WithInstrumentationVersion(runtime.ModuleVersion()),
		)
		logger.Info("MCP server instrumentation initialized")
	})
}

type mcpServerEnabler struct{}

func (mcpServerEnabler) Enable() bool { return runtime.Instrumented(instrumentationKey) }

var serverEnabler = mcpServerEnabler{}

func finishSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
	fmt.Println("ending span", span.SpanContext().SpanID())
}

// ---- Tool dispatch hook ----

func BeforeCallTool(ictx hook.HookContext, recv *mcp.Server, ctx context.Context, req *mcp.CallToolRequest) {
	if !serverEnabler.Enable() {
		return
	}
	initInstrumentation()

	toolName := req.Params.Name
	logger.Info("------------------->>>BeforeCallTool called", "tool", toolName)
	PrintParentSpan(ctx)
	newCtx, span := tracer.Start(ctx, "tool."+toolName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("tool.name", toolName),
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", "tools/call"),
		),
	)
	ictx.SetData(map[string]any{"span": span})
	ictx.SetParam(1, newCtx)
	fmt.Println("creating span", "tool"+toolName, "id", span.SpanContext().SpanID())
}

func AfterCallTool(ictx hook.HookContext, res *mcp.CallToolResult, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		logger.Info("AfterCallTool: no span from before hook")
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
	logger.Info("AfterCallTool completed")
}

// ---- Protocol middleware ----

func AfterNewServer(ictx hook.HookContext, s *mcp.Server) {
	if !serverEnabler.Enable() {
		return
	}
	initInstrumentation()
	logger.Info("AfterNewServer: installing protocol middleware")
	s.AddReceivingMiddleware(serverProtocolMiddleware)
}

// serverProtocolMiddleware creates one span per meaningful inbound MCP method.
//
// Skipped methods (no span created):
//
//   - notifications/initialized: this is a client acknowledgment notification.
//     It is 0µs on the server (no server-side work), and the client-side
//     mcp.notifications/initialized span already records the operation with
//     full context. Creating a server-side span adds visual noise with no value.
//
//   - notifications/* in general: notifications are fire-and-forget; the server
//     does no meaningful work that needs its own span.
//
// The MCP SDK (StreamableHTTPHandler with stateless per-request getServer) may
// process multiple JSON-RPC messages within one HTTP request — for example,
// initialize + initialized + tools/call can arrive in the same HTTP connection.
// When this happens, each method still gets its own span correctly parented to
// the HTTP server span (extracted from traceparent by net/http/server auto-
// instrumentation). The visual grouping under one HTTP POST in Tempo reflects
// the actual protocol behavior, not a bug in this instrumentation.
func serverProtocolMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if !serverEnabler.Enable() {
			return next(ctx, method, req)
		}

		initInstrumentation()

		if isNotification(method) {
			return next(ctx, method, req)
		}

		// IMPORTANT:
		// In stateful StreamableHTTP, ctx can belong to the persistent
		// MCP session rather than the current HTTP POST.
		//
		// Recover the current HTTP server span from the request metadata
		// injected by BeforeStreamableHTTP.
		if extra := req.GetExtra(); extra != nil && extra.Header != nil {
			if traceparent := extra.Header.Get(httpSpanContextHeader); traceparent != "" {
				carrier := propagation.MapCarrier{
					"traceparent": traceparent,
				}

				ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)

				// The span was created locally by net/http instrumentation,
				// so don't mark it as a remote parent.
				if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
					ctx = trace.ContextWithSpanContext(
						ctx,
						sc.WithRemote(false),
					)
				}
			}
		}

		spanName := methodToSpanName(method)

		sc := trace.SpanContextFromContext(ctx)

		fmt.Printf(
			"\n=== MCP METHOD ===\n"+
				"method: %s\n"+
				"trace_id: %s\n"+
				"parent_span_id: %s\n"+
				"remote: %v\n"+
				"valid: %v\n",
			method,
			sc.TraceID(),
			sc.SpanID(),
			sc.IsRemote(),
			sc.IsValid(),
		)

		ctx, methodSpan := tracer.Start(
			ctx,
			spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("rpc.system", "jsonrpc"),
				attribute.String("rpc.method", method),
			),
		)

		fmt.Printf(
			"created span: %s\nspan_id: %s\n",
			spanName,
			methodSpan.SpanContext().SpanID(),
		)

		switch method {
		case "initialize":
			methodSpan.AddEvent("protocol.validate")
			methodSpan.AddEvent("capability.negotiation")

		case "server/discover":
			methodSpan.AddEvent("capability.enumeration")
		}

		result, err := next(ctx, method, req)

		if err != nil {
			methodSpan.RecordError(err)
			methodSpan.SetStatus(codes.Error, err.Error())
		} else {
			methodSpan.SetStatus(codes.Ok, "")
		}

		methodSpan.End()

		return result, err
	}
}

// isNotification reports whether the given JSON-RPC method is a one-way
// notification (no response expected, no server-side work to measure).
func isNotification(method string) bool {
	switch method {
	case "notifications/initialized",
		"notifications/roots/list_changed",
		"notifications/progress",
		"notifications/cancelled":
		return true
	}
	return false
}

func methodToSpanName(method string) string {
	logger.Info("debug", "method", method)
	switch method {
	case "initialize":
		return "mcp.initialize"
	case "server/discover":
		return "mcp.discovery"
	case "tools/list":
		return "mcp.tools.list"
	case "tools/call":
		return "mcp.tools.execute"
	case "prompts/get":
		return "mcp.prompts.get"
	case "resources/read":
		return "mcp.resources.read"
	default:
		return "mcp." + method
	}
}

// AfterMain flushes the active TracerProvider on process exit.
type flushable interface {
	ForceFlush(ctx context.Context) error
}

func AfterMain(ictx hook.HookContext) {
	tp := otel.GetTracerProvider()
	f, ok := tp.(flushable)
	if !ok {
		logger.Info("active TracerProvider does not support ForceFlush")
		return
	}
	if err := f.ForceFlush(context.Background()); err != nil {
		logger.Info("otel shutdown error", "error", err)
	} else {
		logger.Info("MCP server instrumentation flushed")
	}
}

func PrintParentSpan(ctx context.Context) {
	spanContext := trace.SpanContextFromContext(ctx)

	// 2. Validate and read the SpanID (which acts as the Parent Span ID)
	if spanContext.IsValid() {
		parentSpanID := spanContext.SpanID().String()
		traceID := spanContext.TraceID().String()

		fmt.Printf("Parent Span ID: %s\n", parentSpanID)
		fmt.Printf("Trace ID: %s\n", traceID)
	} else {
		fmt.Println("No valid parent span found in the context (this will be a root span).")
	}
}

func SpanMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, methodSpan := tracer.Start(r.Context(), "method",
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("rpc.system", "jsonrpc"),
					attribute.String("rpc.method", "method"),
				),
			)
			next.ServeHTTP(w, r)

			methodSpan.End()
			fmt.Println("-->>>ending ", "method")
		} else {

			next.ServeHTTP(w, r)
		}
	})
}

func BeforeStreamableHTTP(
	ictx hook.HookContext,
	recv *mcp.StreamableServerTransport,
	w http.ResponseWriter,
	req *http.Request,
) {
	if !serverEnabler.Enable() {
		return
	}

	initInstrumentation()

	// Only bridge POST request context.
	if req.Method != http.MethodPost {
		return
	}

	// This is the context created by net/http server auto-instrumentation.
	httpCtx := req.Context()

	sc := trace.SpanContextFromContext(httpCtx)
	if !sc.IsValid() {
		logger.Info("BeforeStreamableHTTP: no valid HTTP server span")
		return
	}

	// Serialize the CURRENT HTTP server span context into a private
	// request header. This header is later copied by the MCP SDK into
	// RequestExtra.Header for every JSON-RPC request.
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(httpCtx, carrier)

	traceparent := carrier.Get("traceparent")
	if traceparent == "" {
		return
	}

	req.Header.Set(httpSpanContextHeader, traceparent)

	// Keep the request context unchanged. The important part is the
	// per-HTTP-request span context stored in RequestExtra.Header.
	ictx.SetParam(1, req)
}
