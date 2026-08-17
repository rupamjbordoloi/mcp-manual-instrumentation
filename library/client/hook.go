package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook"
)

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
func BeforeConnect(ictx hook.HookContext, recv *mcp.Client, ctx context.Context, t mcp.Transport, opts *mcp.ClientSessionOptions) context.Context {
	ictx.SetData(ctx) // stash the caller's ctx for AfterConnect to key the session map with
	spanCtx, span := tracer.Start(ctx, "mcp.initialize",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	ictx.SetKeyData("span", span)
	return spanCtx
}

func AfterConnect(ictx hook.HookContext, session *mcp.ClientSession, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok {
		return
	}
	finishSpan(span, err)

	if err == nil && session != nil {
		if callerCtx, ok := ictx.GetData().(context.Context); ok {
			sessionParentCtx.Store(session, callerCtx)
		}
	}
}

// BeforeCallTool / AfterCallTool instrument (*mcp.ClientSession).CallTool as
// mcp.tools.call — a sibling of mcp.initialize under the same root, since it
// uses the caller's ctx directly rather than mcp.initialize's span context.
func BeforeCallTool(ictx hook.HookContext, recv *mcp.ClientSession, ctx context.Context, params *mcp.CallToolParams) context.Context {
	spanCtx, span := tracer.Start(ctx, "mcp.tools.call",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", "tools/call"),
			attribute.String("mcp.tool.name", params.Name),
		),
	)
	ictx.SetKeyData("span", span)
	ictx.SetKeyData("ctx", spanCtx)
	return spanCtx
}

func AfterCallTool(ictx hook.HookContext, res *mcp.CallToolResult, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok {
		return
	}
	spanCtx, _ := ictx.GetKeyData("ctx").(context.Context)

	if err != nil {
		finishSpan(span, err)
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
}

// BeforeClose / AfterClose instrument (*mcp.ClientSession).Close as
// mcp.shutdown, recovering the session's parent context via sessionParentCtx
// since Close has no context.Context parameter of its own.
func BeforeClose(ictx hook.HookContext, recv *mcp.ClientSession) hook.HookContext {
	parentCtx, ok := sessionParentCtx.Load(recv)
	if !ok {
		return ictx
	}
	sessionParentCtx.Delete(recv)

	_, span := tracer.Start(parentCtx.(context.Context), "mcp.shutdown", trace.WithSpanKind(trace.SpanKindClient))
	ictx.SetKeyData("span", span)
	return ictx
}

func AfterClose(ictx hook.HookContext, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok {
		return
	}
	finishSpan(span, err)
}
