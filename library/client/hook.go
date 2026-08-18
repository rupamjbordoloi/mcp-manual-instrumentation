// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook"
	"go.opentelemetry.io/otelc/pkg/runtime"
)

const (
	instrumentationName = "go.opentelemetry.io/otelc/instrumentation/mcp/client"
	instrumentationKey  = "MCP_CLIENT"
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
		logger.Info("MCP client instrumentation initialized")
	})
}

// mcpClientEnabler controls whether MCP client instrumentation is enabled,
// respecting OTEL_GO_ENABLED_INSTRUMENTATIONS / OTEL_GO_DISABLED_INSTRUMENTATIONS
// the same way otelc's own net/http instrumentation does.
type mcpClientEnabler struct{}

func (mcpClientEnabler) Enable() bool {
	return runtime.Instrumented(instrumentationKey)
}

var clientEnabler = mcpClientEnabler{}

// sessionParentCtx maps a live *mcp.ClientSession to the context its
// mcp.session root span lives in. It exists because (*ClientSession).Close
// takes no context.Context parameter — there is nothing for a compile-time
// hook to read a parent span from directly. Connect's after-hook stashes
// the parent context here, keyed by the session pointer it just received;
// Close's before-hook looks it up via its receiver and removes the entry.
var sessionParentCtx sync.Map // map[*mcp.ClientSession]context.Context

func finishSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// BeforeConnect / AfterConnect instrument (*mcp.Client).Connect as mcp.initialize.
func BeforeConnect(ictx hook.HookContext, recv *mcp.Client, ctx context.Context, t mcp.Transport, opts *mcp.ClientSessionOptions) {
	if !clientEnabler.Enable() {
		logger.Info("MCP client instrumentation disabled")
		return
	}
	initInstrumentation()

	logger.Info("BeforeConnect called")

	// spanCtx, span := tracer.Start(ctx, "mcp.initialize",
	// 	trace.WithSpanKind(trace.SpanKindClient),
	// )

	// ictx.SetData(map[string]interface{}{
	// 	"span":      span,
	// 	"callerCtx": ctx,
	// })

	// return spanCtx

	spanCtx, span := tracer.Start(ctx, "mcp.initialize", trace.WithSpanKind(trace.SpanKindClient))
	ictx.SetData(map[string]interface{}{"span": span, "callerCtx": ctx})
	ictx.SetParam(1, spanCtx) // index 1: recv=0, ctx=1
	// return spanCtx
}

func AfterConnect(ictx hook.HookContext, session *mcp.ClientSession, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		logger.Info("AfterConnect: no span from before hook")
		return
	}
	finishSpan(span, err)

	if err != nil {
		logger.Info("AfterConnect called with error", "error", err)
		return
	}

	if session != nil {
		if callerCtx, ok := ictx.GetKeyData("callerCtx").(context.Context); ok {
			sessionParentCtx.Store(session, callerCtx)
		}
	}
	logger.Info("AfterConnect completed")
}

// BeforeCallTool / AfterCallTool instrument (*mcp.ClientSession).CallTool as
// mcp.tools.call.
func BeforeCallTool(ictx hook.HookContext, recv *mcp.ClientSession, ctx context.Context, params *mcp.CallToolParams) {
	if !clientEnabler.Enable() {
		return
	}
	initInstrumentation()

	logger.Info("BeforeCallTool called", "tool", params.Name)

	// spanCtx, span := tracer.Start(ctx, "mcp.tools.call",
	// 	trace.WithSpanKind(trace.SpanKindClient),
	// 	trace.WithAttributes(
	// 		attribute.String("rpc.system", "jsonrpc"),
	// 		attribute.String("rpc.method", "tools/call"),
	// 		attribute.String("mcp.tool.name", params.Name),
	// 	),
	// )

	// ictx.SetData(map[string]interface{}{
	// 	"span": span,
	// 	"ctx":  spanCtx,
	// })

	// return spanCtx

	spanCtx, span := tracer.Start(ctx, "mcp.tools.call",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", "tools/call"),
			attribute.String("mcp.tool.name", params.Name),
		),
	)
	ictx.SetData(map[string]interface{}{
		"span": span,
		"ctx":  spanCtx,
	})
	ictx.SetParam(1, spanCtx)
}

func AfterCallTool(ictx hook.HookContext, res *mcp.CallToolResult, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		logger.Info("AfterCallTool: no span from before hook")
		return
	}
	spanCtx, _ := ictx.GetKeyData("ctx").(context.Context)

	if err != nil {
		finishSpan(span, err)
		logger.Info("AfterCallTool called with error", "error", err)
		return
	}
	if res != nil && res.IsError {
		finishSpan(span, fmt.Errorf("tool call returned an error result"))
		return
	}

	if spanCtx != nil {
		_, deserSpan := tracer.Start(spanCtx, "mcp.deserialize_response", trace.WithSpanKind(trace.SpanKindInternal))
		finishSpan(deserSpan, nil)
	}
	finishSpan(span, nil)
	logger.Info("AfterCallTool completed")
}

// BeforeClose / AfterClose instrument (*mcp.ClientSession).Close as
// mcp.shutdown, recovering the session's parent context via sessionParentCtx
// since Close has no context.Context parameter of its own.
func BeforeClose(ictx hook.HookContext, recv *mcp.ClientSession) hook.HookContext {
	if !clientEnabler.Enable() {
		return ictx
	}
	initInstrumentation()

	parentCtx, ok := sessionParentCtx.Load(recv)
	if !ok {
		return ictx
	}
	sessionParentCtx.Delete(recv)

	_, span := tracer.Start(parentCtx.(context.Context), "mcp.shutdown", trace.WithSpanKind(trace.SpanKindClient))
	ictx.SetData(map[string]interface{}{"span": span})
	return ictx
}

func AfterClose(ictx hook.HookContext, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		return
	}
	finishSpan(span, err)
}

// AfterMain flushes the active TracerProvider on process exit. It reads
// back whatever provider otelc's own auto-bootstrap installed via
// otel.GetTracerProvider() rather than constructing or owning one — a
// second, competing bootstrap here previously caused mcp.session spans to
// be created against a different provider than the rest of the hooks and
// silently dropped.
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
		logger.Info("MCP client instrumentation flushed")
	}
}

// BeforeRun / AfterRun instrument main.run as the mcp.session root span —
// the top-level unit of work for one client invocation. Everything else
// (mcp.initialize, mcp.tools.call, mcp.shutdown) nests under this because
// run's ctx, once rewritten here, is the same ctx threaded through every
// subsequent call inside run's body.
// func BeforeRun(ictx hook.HookContext, ctx context.Context) context.Context {
// 	if !clientEnabler.Enable() {
// 		return ctx
// 	}
// 	initInstrumentation()

// 	logger.Info("BeforeRun called")

// 	spanCtx, span := tracer.Start(ctx, "mcp.session",
// 		trace.WithSpanKind(trace.SpanKindClient),
// 	)
// 	ictx.SetData(map[string]interface{}{"span": span})
// 	return spanCtx
// }

func BeforeRun(ictx hook.HookContext, ctx context.Context) context.Context {
	if !clientEnabler.Enable() {
		return ctx
	}
	initInstrumentation()

	logger.Info("BeforeRun called")

	spanCtx, span := tracer.Start(ctx, "mcp.session",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	ictx.SetData(map[string]interface{}{"span": span})
	ictx.SetParam(0, spanCtx) // <- explicit rewrite, matching official BeforeRoundTrip
	return spanCtx
}

func AfterRun(ictx hook.HookContext, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		logger.Info("AfterRun: no span from before hook")
		return
	}
	finishSpan(span, err)
	logger.Info("AfterRun completed")
}
