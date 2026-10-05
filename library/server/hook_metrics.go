// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otelc/pkg/hook"
)

// This file holds the hook entry points that mcp.otelc.yaml wires to SDK
// functions. Instruments and the recording logic behind them live in
// metrics.go and metrics_transport.go; hook_traces.go calls into the
// operation and tool observations for the two hooks it shares with tracing.

// serverSessionStart records when each *mcp.ServerSession was created
// (AfterServerConnect), so the close hooks can compute
// mcp.server.session.duration and balance mcp.session.active.
var serverSessionStart sync.Map // map[*mcp.ServerSession]time.Time

// ---- Server session hooks ----

// BeforeServerConnect captures the moment a new inbound MCP session begins.
// (*Server).Connect returns a new *mcp.ServerSession per client connection
// (unlike NewServer, which runs once for the whole process), so this is the
// server-side symmetric counterpart to the client's BeforeConnect.
func BeforeServerConnect(
	ictx hook.HookContext,
	recv *mcp.Server,
	ctx context.Context,
	t mcp.Transport,
	opts *mcp.ServerSessionOptions,
) {
	if !serverEnabler.Enable() {
		return
	}

	ictx.SetData(map[string]any{"start": time.Now()})
}

// AfterServerConnect stores the start time against the new session and
// counts it as active. Both are undone in AfterServerSessionClose.
func AfterServerConnect(ictx hook.HookContext, session *mcp.ServerSession, err error) {
	if err != nil || session == nil {
		return
	}

	start, ok := ictx.GetKeyData("start").(time.Time)
	if !ok {
		logger.Debug("AfterServerConnect: no start time from before hook")
		return
	}

	serverSessionStart.Store(session, start)
	metrics().sessionActive.Add(context.Background(), 1)
}

// BeforeServerSessionClose looks up the start time AfterServerConnect
// recorded for this session. Close() is idempotent (see its own doc
// comment in go-sdk), so a repeat call finds nothing here and
// AfterServerSessionClose skips recording a second time. That also keeps
// mcp.session.active from being decremented twice for one session.
func BeforeServerSessionClose(ictx hook.HookContext, recv *mcp.ServerSession) {
	if !serverEnabler.Enable() {
		return
	}

	start, ok := serverSessionStart.LoadAndDelete(recv)
	if !ok {
		logger.Debug("BeforeServerSessionClose: no recorded start time")
		return
	}

	ictx.SetData(map[string]any{"start": start})
}

// AfterServerSessionClose records mcp.server.session.duration and takes the
// session out of mcp.session.active.
//
// The MCP semantic conventions also list jsonrpc.protocol.version,
// mcp.protocol.version, network.protocol.name/version, and
// network.transport as recommended attributes on the duration; none are
// available from *mcp.ServerSession, so only error.type (the sole
// conditionally-required one) is recorded.
func AfterServerSessionClose(ictx hook.HookContext, err error) {
	start, ok := ictx.GetKeyData("start").(time.Time)
	if !ok {
		logger.Debug("AfterServerSessionClose: no start time from before hook")
		return
	}

	m := metrics()

	var attrs []attribute.KeyValue
	if err != nil {
		attrs = append(attrs, attribute.String(attrErrorType, errorType(err)))
	}

	ctx := context.Background()
	m.sessionDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
	m.sessionActive.Add(ctx, -1)

	logger.Debug("AfterServerSessionClose: recorded session metrics")
}

// ---- Tool registry hooks ----

// BeforeServerAddTool remembers the name of the tool being added. Both
// Server.AddTool and the generic mcp.AddTool (which calls it) pass through
// here, so every registered tool is seen.
func BeforeServerAddTool(ictx hook.HookContext, recv *mcp.Server, t *mcp.Tool, h mcp.ToolHandler) {
	if !serverEnabler.Enable() || t == nil {
		return
	}

	ictx.SetKeyData("tool.name", t.Name)
}

// AfterServerAddTool registers the name only once AddTool has returned, so a
// tool rejected with a panic (missing or invalid schema) is not recorded.
//
// The registry exists because tool names arrive from clients: metrics label
// only registered tools by name and bucket the rest as _OTHER (see
// metricToolName).
func AfterServerAddTool(ictx hook.HookContext) {
	if name, ok := ictx.GetKeyData("tool.name").(string); ok {
		registerTool(name)
	}
}

// ---- Transport request hooks ----

// BeforeTransportRequest runs once per inbound HTTP request, on the outer
// (*StreamableHTTPHandler).ServeHTTP. This layer sees every request,
// including those the handler rejects (bad headers, unknown session,
// oversized body) before they reach a session. Those rejections are the
// requests mcp.transport.errors.total exists to count.
//
// It swaps in a status/byte-counting ResponseWriter (parameter 1) and wraps
// the request body, which is replaced in place through the shared *Request.
// BeforeStreamableHTTP (hook_traces.go) hooks the inner per-session
// transport instead.
func BeforeTransportRequest(
	ictx hook.HookContext,
	recv *mcp.StreamableHTTPHandler,
	w http.ResponseWriter,
	req *http.Request,
) {
	if !serverEnabler.Enable() {
		return
	}

	obs, wrapped := beginTransportObservation(w, req)

	ictx.SetKeyData("transport", obs)
	ictx.SetParam(1, wrapped)
}

// AfterTransportRequest records the transport metrics once the request has
// been fully handled, including the end of any SSE response stream.
func AfterTransportRequest(ictx hook.HookContext) {
	if obs, ok := ictx.GetKeyData("transport").(*transportObservation); ok {
		obs.end()
	}
}
