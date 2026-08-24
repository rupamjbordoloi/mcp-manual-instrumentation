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

type mcpClientEnabler struct{}

func (mcpClientEnabler) Enable() bool { return runtime.Instrumented(instrumentationKey) }

var clientEnabler = mcpClientEnabler{}

// sessionParentCtx maps a live *mcp.ClientSession to the context its
// mcp.session root span lives in. Close takes no context.Context so we
// stash the caller's ctx at Connect time and recover it at Close time.
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

// ---- NewClient hook ----
//
// Installs client-side protocol middleware into the SDK's sending chain
// immediately after the client is constructed — one call, covers every
// outbound request for the lifetime of this client.
// NewClient returns *Client as its only return value:
// after params: ictx + c = 2 total.

func AfterNewClient(ictx hook.HookContext, c *mcp.Client) {
	if !clientEnabler.Enable() {
		return
	}
	initInstrumentation()
	logger.Debug("AfterNewClient: installing protocol middleware")
	c.AddSendingMiddleware(clientProtocolMiddleware)
}

// clientProtocolMiddleware adds a span for every outbound MCP method.
// MethodHandler signature confirmed from urlElicitationMiddleware in client.go:
//
//	func(ctx context.Context, method string, req Request) (Result, error)
//
// Sending middleware wraps outbound requests — initialize, tools/call,
// tools/list, ping, etc. — giving us one span per protocol operation
// without hooking each method individually.
func clientProtocolMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if !clientEnabler.Enable() {
			return next(ctx, method, req)
		}

		spanName := clientMethodToSpanName(method)
		ctx, methodSpan := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("rpc.system", "jsonrpc"),
				attribute.String("rpc.method", method),
			),
		)
		logger.Info("traceid", "traceid", methodSpan.SpanContext().TraceID())

		// Extract tool name for tools/call spans.
		if method == "tools/call" {
			if p, ok := req.GetParams().(*mcp.CallToolParams); ok && p != nil {
				methodSpan.SetAttributes(attribute.String("mcp.tool.name", p.Name))
			}
		}

		result, err := next(ctx, method, req)

		if err != nil {
			methodSpan.SetStatus(codes.Error, err.Error())
		} else {
			methodSpan.SetStatus(codes.Ok, "")
		}
		methodSpan.End()

		return result, err
	}
}

func clientMethodToSpanName(method string) string {
	switch method {
	case "initialize":
		return "mcp.initialize"
	case "server/discover":
		return "mcp.discovery"
	case "tools/call":
		return "mcp.tools.call"
	case "tools/list":
		return "mcp.tools.list"
	case "prompts/get":
		return "mcp.prompts.get"
	case "resources/read":
		return "mcp.resources.read"
	case "ping":
		return "mcp.ping"
	default:
		return "mcp." + method
	}
}

// ---- Connect hooks ----
//
// (*Client).Connect — instruments the full session establishment as
// mcp.initialize and stashes the caller's ctx for Close's mcp.shutdown span.
// Param indices (recv=0, ctx=1, t=2, opts=3):
// ctx rewritten via SetParam(1, spanCtx).

func BeforeConnect(ictx hook.HookContext, recv *mcp.Client, ctx context.Context, t mcp.Transport, opts *mcp.ClientSessionOptions) {
	if !clientEnabler.Enable() {
		return
	}
	initInstrumentation()
	logger.Debug("BeforeConnect called")

	// mcp.initialize wraps the entire Connect call — server/discover
	// negotiation and legacy initialize handshake both happen inside it.
	spanCtx, span := tracer.Start(ctx, "mcp.initialize",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	logger.Info("traceid", "traceid", span.SpanContext().TraceID())
	ictx.SetData(map[string]any{
		"span":      span,
		"callerCtx": ctx, // original ctx, not spanCtx — used to parent mcp.shutdown later
	})
	ictx.SetParam(1, spanCtx)
}

func AfterConnect(ictx hook.HookContext, session *mcp.ClientSession, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		logger.Debug("AfterConnect: no span from before hook")
		return
	}
	finishSpan(span, err)

	if err != nil {
		logger.Debug("AfterConnect called with error", "error", err)
		return
	}
	if session != nil {
		if callerCtx, ok := ictx.GetKeyData("callerCtx").(context.Context); ok {
			sessionParentCtx.Store(session, callerCtx)
		}
	}
	logger.Debug("AfterConnect completed")
}

// ---- CallTool hooks ----
//
// (*ClientSession).CallTool — wraps the tool call as mcp.tools.call.
// The sending middleware (clientProtocolMiddleware) already creates a span
// for the outbound tools/call request; this hook creates the higher-level
// *client-visible* mcp.tools.call span that includes deserialization and
// error interpretation, nesting the middleware's transport span inside it.
// Param indices: recv=0, ctx=1, params=2.

func BeforeCallTool(ictx hook.HookContext, recv *mcp.ClientSession, ctx context.Context, params *mcp.CallToolParams) {
	if !clientEnabler.Enable() {
		return
	}
	initInstrumentation()
	logger.Debug("BeforeCallTool called", "tool", params.Name)

	spanCtx, span := tracer.Start(ctx, "mcp.tools.call",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", "tools/call"),
			attribute.String("mcp.tool.name", params.Name),
		),
	)
	logger.Info("traceid", "traceid", span.SpanContext().TraceID())
	ictx.SetData(map[string]any{
		"span": span,
		"ctx":  spanCtx,
	})
	ictx.SetParam(1, spanCtx)
}

func AfterCallTool(ictx hook.HookContext, res *mcp.CallToolResult, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		logger.Debug("AfterCallTool: no span from before hook")
		return
	}
	spanCtx, _ := ictx.GetKeyData("ctx").(context.Context)

	if err != nil {
		finishSpan(span, err)
		logger.Debug("AfterCallTool called with error", "error", err)
		return
	}
	if res != nil && res.IsError {
		finishSpan(span, fmt.Errorf("tool call returned an error result"))
		return
	}

	// Deserialize response span — represents client-side unmarshaling.
	if spanCtx != nil {
		_, deserSpan := tracer.Start(spanCtx, "mcp.deserialize_response",
			trace.WithSpanKind(trace.SpanKindInternal))
		finishSpan(deserSpan, nil)
	}
	finishSpan(span, nil)
	logger.Debug("AfterCallTool completed")
}

// ---- Close hooks ----
//
// (*ClientSession).Close — wraps session teardown as mcp.shutdown.
// Close() has signature (recv *ClientSession) error — no context.Context.
// Before params: ictx + recv = 2 total.
// After params: ictx + err = 2 total.
// Parent context recovered from sessionParentCtx stashed at AfterConnect.

func BeforeClose(ictx hook.HookContext, recv *mcp.ClientSession) {
	if !clientEnabler.Enable() {
		return
	}
	initInstrumentation()

	parentCtx, ok := sessionParentCtx.Load(recv)
	if !ok {
		return
	}
	sessionParentCtx.Delete(recv)

	_, span := tracer.Start(parentCtx.(context.Context), "mcp.shutdown",
		trace.WithSpanKind(trace.SpanKindClient))
	ictx.SetData(map[string]any{"span": span})
}

func AfterClose(ictx hook.HookContext, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		return
	}
	finishSpan(span, err)
}

// ---- run() root span hooks ----
//
// Wraps main.run as the mcp.session root span. Every other client span
// (mcp.initialize, mcp.tools.call, mcp.shutdown) nests under this because
// run's ctx is rewritten via SetParam(0, spanCtx) before run's body executes.
// run is a plain func (no receiver), so ctx is at index 0.

func BeforeRun(ictx hook.HookContext, ctx context.Context) {
	if !clientEnabler.Enable() {
		return
	}
	initInstrumentation()
	logger.Debug("BeforeRun called")

	spanCtx, span := tracer.Start(ctx, "mcp.session",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	logger.Info("traceid", "traceid", span.SpanContext().TraceID())
	ictx.SetData(map[string]any{"span": span})
	ictx.SetParam(0, spanCtx)
}

func AfterRun(ictx hook.HookContext, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		logger.Debug("AfterRun: no span from before hook")
		return
	}
	finishSpan(span, err)
	logger.Debug("AfterRun completed")
}

// ---- Process exit flush ----
//
// Hooks main() after-exit to ForceFlush otelc's auto-installed
// TracerProvider. Short-lived processes drop buffered spans without this.

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
		logger.Info("MCP client instrumentation flushed")
	}
}
