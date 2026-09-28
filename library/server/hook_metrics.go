// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package server

import (
	"context"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otelc/pkg/hook"
	"go.opentelemetry.io/otelc/pkg/runtime"
)

// durationBuckets are the ExplicitBucketBoundaries the MCP semantic
// conventions recommend for both duration histograms below. See
// https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/mcp.md#metrics
var durationBuckets = []float64{0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300}

var (
	metricsOnce sync.Once
	meter       metric.Meter

	mcpServerOperationDuration metric.Float64Histogram
	mcpServerSessionDuration   metric.Float64Histogram

	// serverSessionStart records when each *mcp.ServerSession was created
	// (AfterServerConnect), so BeforeServerSessionClose/AfterServerSessionClose
	// can compute mcp.server.session.duration when the session ends.
	serverSessionStart sync.Map // map[*mcp.ServerSession]time.Time
)

func initMetrics() {
	metricsOnce.Do(func() {
		meter = otel.GetMeterProvider().Meter(instrumentationName, metric.WithInstrumentationVersion(runtime.ModuleVersion()))

		var err error

		mcpServerOperationDuration, err = meter.Float64Histogram(
			"mcp.server.operation.duration",
			metric.WithUnit("s"),
			metric.WithDescription("MCP request or notification duration as observed on the receiver from the time it was received until the result or ack is sent."),
			metric.WithExplicitBucketBoundaries(durationBuckets...),
		)
		if err != nil {
			logger.Error("failed to create mcp.server.operation.duration histogram", "error", err)
		}

		mcpServerSessionDuration, err = meter.Float64Histogram(
			"mcp.server.session.duration",
			metric.WithUnit("s"),
			metric.WithDescription("The duration of the MCP session as observed on the MCP server."),
			metric.WithExplicitBucketBoundaries(durationBuckets...),
		)
		if err != nil {
			logger.Error("failed to create mcp.server.session.duration histogram", "error", err)
		}

		logger.Info("MCP server metric instrumentation initialized")
	})
}

// ---- mcp.server.operation.duration ----
//
// recordOperationStart/recordOperationDuration are the seam
// serverProtocolMiddleware (hook_traces.go) calls into, so that file
// doesn't need to import "time" or "go.opentelemetry.io/otel/metric" itself.

func recordOperationStart() time.Time {
	initMetrics()
	return time.Now()
}

func recordOperationDuration(ctx context.Context, start time.Time, attrs []attribute.KeyValue) {
	if mcpServerOperationDuration == nil {
		return
	}

	mcpServerOperationDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
}

// ---- mcp.server.session.duration / server session hooks ----

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

	initMetrics()

	ictx.SetData(map[string]any{"start": time.Now()})
}

// AfterServerConnect stores the start time against the new session so
// BeforeServerSessionClose/AfterServerSessionClose can compute
// mcp.server.session.duration once the session ends.
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
}

// BeforeServerSessionClose looks up the start time AfterServerConnect
// recorded for this session. Close() is idempotent (see its own doc
// comment in go-sdk), so a repeat call simply finds nothing here and
// AfterServerSessionClose skips recording a second time.
func BeforeServerSessionClose(ictx hook.HookContext, recv *mcp.ServerSession) {
	if !serverEnabler.Enable() {
		return
	}

	initMetrics()

	start, ok := serverSessionStart.Load(recv)
	if !ok {
		logger.Debug("BeforeServerSessionClose: no recorded start time")
		return
	}

	serverSessionStart.Delete(recv)

	ictx.SetData(map[string]any{"start": start})
}

// AfterServerSessionClose records mcp.server.session.duration.
//
// The MCP semantic conventions also list jsonrpc.protocol.version,
// mcp.protocol.version, network.protocol.name/version, and
// network.transport as recommended attributes here; none are currently
// available from *mcp.ServerSession without further SDK exploration, so
// only error.type (the sole conditionally-required one) is recorded today.
func AfterServerSessionClose(ictx hook.HookContext, err error) {
	start, ok := ictx.GetKeyData("start").(time.Time)
	if !ok {
		logger.Debug("AfterServerSessionClose: no start time from before hook")
		return
	}

	if mcpServerSessionDuration == nil {
		return
	}

	var attrs []attribute.KeyValue
	if err != nil {
		attrs = append(attrs, attribute.String(attrErrorType, errorType(err)))
	}

	mcpServerSessionDuration.Record(context.Background(), time.Since(start).Seconds(), metric.WithAttributes(attrs...))

	logger.Debug("AfterServerSessionClose: recorded mcp.server.session.duration")
}
