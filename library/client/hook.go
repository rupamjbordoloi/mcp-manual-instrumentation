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

// sessionParentCtx maps *mcp.ClientSession → sessionEntry.
// Populated in AfterConnect once Connect succeeds.
// Read by clientProtocolMiddleware for all post-Connect operations.
// Deleted in BeforeClose.
var sessionParentCtx sync.Map // map[*mcp.ClientSession]sessionEntry

type sessionEntry struct {
	span        trace.Span
	spanContext trace.SpanContext
}

func finishSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		fmt.Println("error", err)
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
	fmt.Println("ending", span.SpanContext().SpanID())
}

// ---- NewClient hook ----

func AfterNewClient(ictx hook.HookContext, c *mcp.Client) {
	if !clientEnabler.Enable() {
		return
	}
	initInstrumentation()
	logger.Debug("AfterNewClient: installing protocol middleware")
	c.AddSendingMiddleware(clientProtocolMiddleware)
}

// clientProtocolMiddleware creates one client span per outbound MCP method.
//
// During Connect (initialize, discover, notifications/initialized):
// sessionParentCtx has no entry yet — ctx flows from BeforeConnect's SetParam
// which already carries mcp.session as active span, so these spans are
// correctly parented under mcp.session.
//
// After Connect (tools/call, tools/list, etc.):
// The SDK internally reassigns ctx before handleSend fires, potentially
// losing the session span. We inject mcp.session's SpanContext as a remote
// parent via trace.ContextWithRemoteSpanContext. This makes every post-Connect
// operation a direct child of mcp.session regardless of SDK ctx manipulation,
// and ensures the W3C traceparent header carries the correct parent context
// for the server to extract.
func clientProtocolMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if !clientEnabler.Enable() {
			return next(ctx, method, req)
		}

		// Post-Connect: inject mcp.session as remote parent so operations
		// are siblings under mcp.session, not nested under prior operations.
		if cs, ok := req.GetSession().(*mcp.ClientSession); ok {
			if val, ok := sessionParentCtx.Load(cs); ok {
				if entry, ok := val.(sessionEntry); ok {
					ctx = trace.ContextWithRemoteSpanContext(ctx, entry.spanContext)
				}
			}
		}

		logger.Info("clientProtocolMiddleware", "clientProtocolMiddleware", method)
		spanName := clientMethodToSpanName(method)
		ctx, methodSpan := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("rpc.system", "jsonrpc"),
				attribute.String("rpc.method", method),
			),
		)
		logger.Info("span creating method", method, methodSpan.SpanContext().SpanID())

		if method == "tools/call" {
			if p, ok := req.GetParams().(*mcp.CallToolParams); ok && p != nil {
				methodSpan.SetAttributes(attribute.String("mcp.tool.name", p.Name))
			}
		}

		result, err := next(ctx, method, req)

		if err != nil {
			methodSpan.RecordError(err)
			methodSpan.SetStatus(codes.Error, err.Error())
		} else if method == "tools/call" {
			if r, ok := result.(*mcp.CallToolResult); ok && r != nil && r.IsError {
				methodSpan.SetStatus(codes.Error, "tool returned an error result")
			} else {
				methodSpan.SetStatus(codes.Ok, "")
			}
		} else {
			methodSpan.SetStatus(codes.Ok, "")
		}
		methodSpan.End()
		fmt.Println("ending", method)

		return result, err
	}
}

func clientMethodToSpanName(method string) string {
	logger.Info("debug", "method", method)
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
	case "notifications/initialized":
		return "mcp.notifications/initialized"
	default:
		return "mcp." + method
	}
}

// ---- Connect hooks ----
//
// BeforeConnect creates mcp.session only.
// mcp.initialize is created by clientProtocolMiddleware when the SDK's
// internal initialize call fires — exactly one span per operation, no duplicate.
//
// sessionCtx (mcp.session active) is threaded into Connect via SetParam so
// all SDK internal operations during Connect (discover, initialize,
// notifications/initialized) inherit the correct parent automatically.
//
// Param indices: recv=0, ctx=1, t=2, opts=3

func BeforeConnect(ictx hook.HookContext, recv *mcp.Client, ctx context.Context, t mcp.Transport, opts *mcp.ClientSessionOptions) {
	if !clientEnabler.Enable() {
		return
	}
	initInstrumentation()
	logger.Debug("BeforeConnect called")

	sessionCtx, sessionSpan := tracer.Start(ctx, "mcp.session",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	logger.Info("span creating method", "mcp.session", sessionSpan.SpanContext().SpanID())

	ictx.SetData(map[string]interface{}{
		"sessionSpan": sessionSpan,
		"sessionCtx":  sessionCtx,
	})

	ictx.SetParam(1, sessionCtx)
}

func AfterConnect(ictx hook.HookContext, session *mcp.ClientSession, err error) {
	sessionSpan, ok := ictx.GetKeyData("sessionSpan").(trace.Span)
	if !ok || sessionSpan == nil {
		logger.Debug("AfterConnect: no sessionSpan from before hook")
		return
	}

	if err != nil {
		finishSpan(sessionSpan, err)
		logger.Debug("AfterConnect called with error", "error", err)
		return
	}

	if session != nil {
		sessionParentCtx.Store(session, sessionEntry{
			span:        sessionSpan,
			spanContext: sessionSpan.SpanContext(),
		})
	}
	logger.Debug("AfterConnect completed")
}

// ---- Close hooks ----

func BeforeClose(ictx hook.HookContext, recv *mcp.ClientSession) {
	if !clientEnabler.Enable() {
		return
	}
	initInstrumentation()

	val, ok := sessionParentCtx.Load(recv)
	if !ok {
		return
	}
	sessionParentCtx.Delete(recv)

	entry, ok := val.(sessionEntry)
	if !ok {
		return
	}

	shutdownCtx := trace.ContextWithRemoteSpanContext(context.Background(), entry.spanContext)
	_, shutdownSpan := tracer.Start(shutdownCtx, "mcp.shutdown",
		trace.WithSpanKind(trace.SpanKindClient))

	logger.Info("span creating method", "mcp.shutdown", shutdownSpan.SpanContext().SpanID())

	ictx.SetData(map[string]interface{}{
		"shutdownSpan": shutdownSpan,
		"sessionSpan":  entry.span,
	})
}

func AfterClose(ictx hook.HookContext, err error) {
	shutdownSpan, ok := ictx.GetKeyData("shutdownSpan").(trace.Span)
	if !ok || shutdownSpan == nil {
		return
	}
	finishSpan(shutdownSpan, err)

	if sessionSpan, ok := ictx.GetKeyData("sessionSpan").(trace.Span); ok && sessionSpan != nil {
		finishSpan(sessionSpan, err)
	}
}

// ---- Process exit flush ----

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
