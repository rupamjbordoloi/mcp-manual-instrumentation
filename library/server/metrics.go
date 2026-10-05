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
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otelc/pkg/runtime"
)

const (
	// Attribute names and values used only by metrics.
	attrMCPMessageDirection    = "mcp.message.direction"
	attrHTTPRequestMethod      = "http.request.method"
	attrHTTPResponseStatusCode = "http.response.status_code"
	attrGenAITokenType         = "gen_ai.token.type"

	directionReceived = "received"
	directionSent     = "sent"

	// tokenTypeInput is the GenAI token type for content the model ingests.
	// Everything this server returns to an agent (tool results, resource
	// contents, prompt messages) is input from the model's point of view.
	tokenTypeInput = "input"

	// otherAttrValue replaces any client-controlled attribute value that is
	// not on a known, bounded list, so a client cannot inflate metric
	// cardinality by sending arbitrary strings.
	otherAttrValue = "_OTHER"

	methodResourcesRead = "resources/read"

	// bytesPerToken is the rule-of-thumb ratio used to estimate LLM tokens
	// from text, because MCP results carry no native token counts.
	bytesPerToken = 4
)

var (
	// durationBuckets are the bucket boundaries the MCP semantic conventions
	// recommend for duration histograms. See
	// https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/mcp.md#metrics
	durationBuckets = []float64{0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300}

	// messageSizeBuckets cover 64 B to 4 MiB.
	messageSizeBuckets = []float64{
		64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768,
		65536, 131072, 262144, 524288, 1048576, 4194304,
	}
)

// serverMetrics groups every instrument this package records. Instruments
// that fail to build are replaced by no-ops, so recording code never needs
// nil checks.
type serverMetrics struct {
	// MCP semantic-convention metrics.
	operationDuration metric.Float64Histogram // mcp.server.operation.duration
	sessionDuration   metric.Float64Histogram // mcp.server.session.duration

	// Tool execution.
	toolInvocations metric.Int64Counter     // mcp.tool.invocations.total
	toolDuration    metric.Float64Histogram // mcp.tool.duration
	toolErrors      metric.Int64Counter     // mcp.tool.errors.total

	// Transport and protocol layer.
	transportErrors metric.Int64Counter       // mcp.transport.errors.total
	sessionActive   metric.Int64UpDownCounter // mcp.session.active
	messageSize     metric.Int64Histogram     // mcp.message.size

	// Agentic accounting.
	resourceReads metric.Int64Counter // mcp.resource.reads.total
}

var (
	metricsOnce sync.Once
	instruments *serverMetrics
)

// metrics returns the lazily created instruments. The meter is resolved on
// first use rather than at init so the runtime has a chance to install its
// MeterProvider first.
func metrics() *serverMetrics {
	metricsOnce.Do(func() {
		meter := otel.GetMeterProvider().Meter(
			instrumentationName,
			metric.WithInstrumentationVersion(runtime.ModuleVersion()),
		)
		instruments = newServerMetrics(meter)
		logger.Info("MCP server metric instrumentation initialized")
	})

	return instruments
}

func newServerMetrics(meter metric.Meter) *serverMetrics {
	return &serverMetrics{
		operationDuration: floatHistogram(meter, "mcp.server.operation.duration", "s",
			"MCP request or notification duration as observed on the receiver from the time it was received until the result or ack is sent.",
			durationBuckets),
		sessionDuration: floatHistogram(meter, "mcp.server.session.duration", "s",
			"The duration of the MCP session as observed on the MCP server.",
			durationBuckets),

		toolInvocations: counter(meter, "mcp.tool.invocations.total", "{invocation}",
			"Number of MCP tool invocations, successful or not."),
		toolDuration: floatHistogram(meter, "mcp.tool.duration", "s",
			"Duration of MCP tool execution on the server.",
			durationBuckets),
		toolErrors: counter(meter, "mcp.tool.errors.total", "{error}",
			"Number of MCP tool invocations that failed, either with a protocol error or an isError result."),

		transportErrors: counter(meter, "mcp.transport.errors.total", "{error}",
			"Number of MCP transport requests that completed with an HTTP status of 400 or above."),
		sessionActive: upDownCounter(meter, "mcp.session.active", "{session}",
			"Number of MCP sessions currently active on the server."),
		messageSize: intHistogram(meter, "mcp.message.size", "By",
			"Size in bytes of the MCP message bodies exchanged over the Streamable HTTP transport, by direction. Sent sizes include SSE framing.",
			messageSizeBuckets),

		resourceReads: counter(meter, "mcp.resource.reads.total", "{read}",
			"Number of MCP resources/read requests."),
	}
}

func counter(meter metric.Meter, name, unit, description string) metric.Int64Counter {
	c, err := meter.Int64Counter(name, metric.WithUnit(unit), metric.WithDescription(description))
	if err != nil {
		logger.Error("failed to create metric instrument", "name", name, "error", err)
		return noop.Int64Counter{}
	}

	return c
}

func upDownCounter(meter metric.Meter, name, unit, description string) metric.Int64UpDownCounter {
	c, err := meter.Int64UpDownCounter(name, metric.WithUnit(unit), metric.WithDescription(description))
	if err != nil {
		logger.Error("failed to create metric instrument", "name", name, "error", err)
		return noop.Int64UpDownCounter{}
	}

	return c
}

func floatHistogram(meter metric.Meter, name, unit, description string, buckets []float64) metric.Float64Histogram {
	h, err := meter.Float64Histogram(
		name,
		metric.WithUnit(unit),
		metric.WithDescription(description),
		metric.WithExplicitBucketBoundaries(buckets...),
	)
	if err != nil {
		logger.Error("failed to create metric instrument", "name", name, "error", err)
		return noop.Float64Histogram{}
	}

	return h
}

func intHistogram(meter metric.Meter, name, unit, description string, buckets []float64) metric.Int64Histogram {
	h, err := meter.Int64Histogram(
		name,
		metric.WithUnit(unit),
		metric.WithDescription(description),
		metric.WithExplicitBucketBoundaries(buckets...),
	)
	if err != nil {
		logger.Error("failed to create metric instrument", "name", name, "error", err)
		return noop.Int64Histogram{}
	}

	return h
}

// ---- Bounded attribute values ----
//
// Tool names and protocol versions are chosen by the client. Using them
// verbatim as metric attributes would let any client create unbounded time
// series, so both are checked against a known set first. (Traces are not
// affected; span attributes are not aggregated.)

var (
	// registeredTools holds every tool name ever added to a server in this
	// process; see BeforeServerAddTool/AfterServerAddTool.
	registeredTools sync.Map // map[string]struct{}

	// knownProtocolVersions guards mcp.protocol.version against an
	// unbounded value from the client.
	//
	// This is a static list rather than one derived from the SDK: older
	// SDK versions don't export an accessor for it (mcp.SupportedProtocolVersions
	// was added later), and the set of MCP protocol revisions changes rarely
	// enough that maintaining it here is low cost. Values are the ones
	// go-sdk's mcp/shared.go defines as of this writing; update this list
	// when a new MCP protocol revision ships.
	knownProtocolVersions = map[string]struct{}{
		"2026-07-28": {},
		"2025-11-25": {},
		"2025-06-18": {},
		"2025-03-26": {},
		"2024-11-05": {},
	}
)

func registerTool(name string) {
	registeredTools.Store(name, struct{}{})
}

// metricToolName returns name if it is a registered tool, otherwise _OTHER.
func metricToolName(name string) string {
	if _, ok := registeredTools.Load(name); ok {
		return name
	}

	return otherAttrValue
}

// metricProtocolVersion returns version if the SDK supports it, otherwise
// _OTHER. An empty version stays empty so the attribute is omitted.
func metricProtocolVersion(version string) string {
	if version == "" {
		return ""
	}

	if _, ok := knownProtocolVersions[version]; ok {
		return version
	}

	return otherAttrValue
}

// withErrorType returns attrs plus error.type when errType is set. It never
// mutates attrs' backing array, so callers can reuse the input slice.
func withErrorType(attrs []attribute.KeyValue, errType string) []attribute.KeyValue {
	if errType == "" {
		return attrs
	}

	return append(attrs[:len(attrs):len(attrs)], attribute.String(attrErrorType, errType))
}

// ---- Protocol operations: duration, resource reads, tokens ----

// operationObservation measures one inbound MCP request handled by
// serverProtocolMiddleware. It is created just before the handler runs and
// ended right after, so tracing and metrics observe the same window.
type operationObservation struct {
	start    time.Time
	method   string
	toolName string // already bounded via metricToolName; empty unless tools/call
	attrs    []attribute.KeyValue
}

func beginOperation(method, toolName, protocolVersion string, overHTTP bool) operationObservation {
	attrs := make([]attribute.KeyValue, 0, 6)
	attrs = append(attrs, attribute.String(attrMCPMethodName, method))

	if v := metricProtocolVersion(protocolVersion); v != "" {
		attrs = append(attrs, attribute.String(attrMCPProtocolVersion, v))
	}

	tool := ""
	if method == "tools/call" && toolName != "" {
		tool = metricToolName(toolName)
		attrs = append(attrs,
			attribute.String(attrGenAIToolName, tool),
			attribute.String(attrGenAIOperationName, genAIOperationExecuteTool),
		)
	}

	if overHTTP {
		attrs = append(attrs,
			attribute.String(attrNetworkTransport, "tcp"),
			attribute.String(attrNetworkProtocolName, "http"),
		)
	}

	return operationObservation{start: time.Now(), method: method, toolName: tool, attrs: attrs}
}

// end records mcp.server.operation.duration, and for the methods that
// have one, mcp.resource.reads.total and mcp.tokens.consumed. errType is ""
// on success (see resultErrorType).
func (o operationObservation) end(ctx context.Context, errType string) {
	m := metrics()

	m.operationDuration.Record(ctx, time.Since(o.start).Seconds(),
		metric.WithAttributes(withErrorType(o.attrs, errType)...))

	if o.method == methodResourcesRead {
		m.resourceReads.Add(ctx, 1, metric.WithAttributes(withErrorType(nil, errType)...))
	}
}

func (o operationObservation) tokenAttributes() []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(attrMCPMethodName, o.method),
		attribute.String(attrGenAITokenType, tokenTypeInput),
	}
	if o.toolName != "" {
		attrs = append(attrs, attribute.String(attrGenAIToolName, o.toolName))
	}

	return attrs
}

// ---- Tool execution: invocations, duration, errors ----

// toolObservation measures one (*Server).callTool invocation, from
// BeforeCallTool to AfterCallTool.
type toolObservation struct {
	start time.Time
	name  string // already bounded via metricToolName
}

func beginToolObservation(name string) toolObservation {
	return toolObservation{start: time.Now(), name: metricToolName(name)}
}

// end records mcp.tool.invocations.total, mcp.tool.duration and, on
// failure, mcp.tool.errors.total. A tool fails either by returning a Go /
// JSON-RPC error or by returning a result with isError=true.
func (o toolObservation) end(ctx context.Context, res *mcp.CallToolResult, err error) {
	m := metrics()

	errType := resultErrorType(res, err)
	opts := metric.WithAttributes(withErrorType(
		[]attribute.KeyValue{attribute.String(attrGenAIToolName, o.name)},
		errType,
	)...)

	m.toolInvocations.Add(ctx, 1, opts)
	m.toolDuration.Record(ctx, time.Since(o.start).Seconds(), opts)

	if errType != "" {
		m.toolErrors.Add(ctx, 1, opts)
	}
}
