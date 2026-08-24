// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package server

import (
	"context"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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
}

// ---- Tool dispatch hook ----
//
// Targets (*Server).callTool — the single internal dispatch point every
// registered tool passes through, regardless of tool name. One rule covers
// every current and future tool: the span name is built from
// req.Params.Name at runtime, never hardcoded in YAML or Go.
//
// Param indices: recv=0, ctx=1, req=2
// ctx is rewritten via SetParam(1, newCtx) — return value is discarded
// by the trampoline, as confirmed from generated code earlier.

func BeforeCallTool(ictx hook.HookContext, recv *mcp.Server, ctx context.Context, req *mcp.CallToolRequest) {
	if !serverEnabler.Enable() {
		return
	}
	initInstrumentation()

	toolName := req.Params.Name
	logger.Debug("BeforeCallTool called", "tool", toolName)

	newCtx, span := tracer.Start(ctx, "tool."+toolName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("tool.name", toolName),
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", "tools/call"),
		),
	)
	ictx.SetData(map[string]any{"span": span})
	ictx.SetParam(1, newCtx) // recv=0, ctx=1, req=2
}

func AfterCallTool(ictx hook.HookContext, res *mcp.CallToolResult, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		logger.Debug("AfterCallTool: no span from before hook")
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
	logger.Debug("AfterCallTool completed")
}

// ---- Protocol middleware hook ----
//
// Targets (*Server).AddReceivingMiddleware. Fires once during NewServer
// setup, installing our protocol-level spans into the SDK's own receiving
// middleware chain — eliminates the ProtocolMiddleware wrapper in main.go.
//
// The middleware uses the concrete type mcp.MethodHandler[*mcp.ServerSession]
// directly, since AddReceivingMiddleware on *Server takes exactly that type.
// Using the generic form avoids the "cannot use func literal as mcp.MethodHandler"
// error caused by trying to assign a plain func to a named/generic type.
func AfterNewServer(ictx hook.HookContext, s *mcp.Server) {
	if !serverEnabler.Enable() {
		return
	}
	initInstrumentation()
	logger.Debug("AfterNewServer: installing protocol middleware")
	s.AddReceivingMiddleware(serverProtocolMiddleware)
}

// serverProtocolMiddleware is the concrete middleware function with the exact
// type signature AddReceivingMiddleware[*ServerSession] expects.
// Declared as a named func rather than a closure so its type is
// func(mcp.MethodHandler[*mcp.ServerSession]) mcp.MethodHandler[*mcp.ServerSession]
// which the compiler can verify matches without a conversion.
func serverProtocolMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		// ... same body as before ...
		if !serverEnabler.Enable() {
			return next(ctx, method, req)
		}

		// jsonrpc.decode already happened inside the SDK before middleware
		// runs — record it as a zero-duration event span rather than a span
		// with a fabricated duration.
		ctx, decodeSpan := tracer.Start(ctx, "jsonrpc.decode",
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithAttributes(
				attribute.String("rpc.system", "jsonrpc"),
				attribute.String("rpc.method", method),
			),
		)
		decodeSpan.AddEvent("decoded")
		finishSpan(decodeSpan, nil)

		logger.Info("traceparent header:", "traceparent", req.GetExtra().Header.Get("traceparent"))

		spanName := methodToSpanName(method)
		ctx, dispatchSpan := tracer.Start(ctx, "mcp.dispatch",
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithAttributes(attribute.String("rpc.method", method)),
		)

		ctx, methodSpan := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("rpc.system", "jsonrpc"),
				attribute.String("rpc.method", method),
			),
		)

		switch method {
		case "initialize":
			methodSpan.AddEvent("protocol.validate")
			methodSpan.AddEvent("capability.negotiation")
		case "server/discover":
			methodSpan.AddEvent("capability.enumeration")
		}

		result, err := next(ctx, method, req)

		finishSpan(dispatchSpan, err)

		if err != nil {
			// SetStatus only — BeforeCallTool/AfterCallTool already called
			// RecordError on the inner tool span; recording it again here
			// would duplicate the error event in Tempo without adding signal.
			methodSpan.SetStatus(codes.Error, err.Error())
		} else {
			methodSpan.SetStatus(codes.Ok, "")
		}
		methodSpan.End()

		_, encodeSpan := tracer.Start(ctx, "jsonrpc.encode",
			trace.WithSpanKind(trace.SpanKindInternal),
		)
		finishSpan(encodeSpan, nil)

		return result, err
	}
}

func methodToSpanName(method string) string {
	switch method {
	case "initialize":
		return "mcp.initialize"
	case "server/discover":
		return "mcp.discovery"
	case "notifications/initialized":
		return "mcp.initialized"
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
		logger.Debug("active TracerProvider does not support ForceFlush")
		return
	}
	if err := f.ForceFlush(context.Background()); err != nil {
		logger.Debug("otel shutdown error", "error", err)
	} else {
		logger.Info("MCP server instrumentation flushed")
	}
}
