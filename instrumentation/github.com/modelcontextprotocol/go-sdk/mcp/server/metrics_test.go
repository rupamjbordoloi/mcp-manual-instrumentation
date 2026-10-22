package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestNewServerMetrics_InstrumentNamesAndUnits(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	m := newServerMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"))
	ctx := context.Background()

	// Record once on every instrument so each one shows up in a collection.
	m.operationDuration.Record(ctx, 1)
	m.toolInvocationTotal.Add(ctx, 1)
	m.toolInvocationSuccess.Add(ctx, 1)
	m.toolInvocationFailure.Add(ctx, 1)
	m.toolInvocationDuration.Record(ctx, 1)
	m.toolRequestSize.Record(ctx, 1)
	m.toolResponseSize.Record(ctx, 1)
	m.sessionCreated.Add(ctx, 1)
	m.sessionClosed.Add(ctx, 1)
	m.sessionActive.Add(ctx, 1)
	m.sessionRequest.Add(ctx, 1)
	m.sessionDuration.Record(ctx, 1)
	m.transportErrors.Add(ctx, 1)
	m.serverError.Add(ctx, 1)
	m.messageSize.Record(ctx, 1)
	m.resourceReads.Add(ctx, 1)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	got := map[string]string{}
	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			got[metric.Name] = metric.Unit
		}
	}

	want := map[string]string{
		"mcp.server.operation.duration":  "s",
		"mcp.tool.invocation.total":      "{invocation}",
		"mcp.tool.invocation.success":    "{invocation}",
		"mcp.tool.invocation.failure":    "{invocation}",
		"mcp.tool.invocation.duration":   "s",
		"mcp.tool.request.message.size":  "By",
		"mcp.tool.response.message.size": "By",
		"mcp.session.created":            "{session}",
		"mcp.session.closed":             "{session}",
		"mcp.session.active":             "{session}",
		"mcp.session.request":            "{request}",
		"mcp.session.duration":           "s",
		"mcp.transport.errors.total":     "{error}",
		"mcp.server.error":               "{error}",
		"mcp.message.size":               "By",
		"mcp.resource.reads.total":       "{read}",
	}

	assert.Equal(t, want, got, "instrument names and units are a public contract: dashboards and alerts depend on them")
}

func TestNewServerMetrics_HistogramBuckets(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	m := newServerMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"))
	ctx := context.Background()

	m.operationDuration.Record(ctx, 1)
	m.messageSize.Record(ctx, 1)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	bounds := map[string][]float64{}

	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			switch d := metric.Data.(type) {
			case metricdata.Histogram[float64]:
				bounds[metric.Name] = d.DataPoints[0].Bounds
			case metricdata.Histogram[int64]:
				bounds[metric.Name] = d.DataPoints[0].Bounds
			}
		}
	}

	assert.Equal(t, durationBuckets, bounds["mcp.server.operation.duration"])
	assert.Equal(t, messageSizeBuckets, bounds["mcp.message.size"])
}

func TestInstrumentConstructorsFallBackToNoops(t *testing.T) {
	meter := sdkmetric.NewMeterProvider().Meter("test")

	// An empty name is rejected by the SDK. Recording code must still work.
	assert.IsType(t, noop.Int64Counter{}, counter(meter, "", "1", "d"))
	assert.IsType(t, noop.Int64UpDownCounter{}, upDownCounter(meter, "", "1", "d"))
	assert.IsType(t, noop.Float64Histogram{}, floatHistogram(meter, "", "s", "d", nil))
	assert.IsType(t, noop.Int64Histogram{}, intHistogram(meter, "", "By", "d", nil))

	assert.NotPanics(t, func() {
		counter(meter, "", "1", "d").Add(context.Background(), 1)
	})
}

func TestWithErrorType(t *testing.T) {
	base := make([]attribute.KeyValue, 1, 8) // spare capacity: a plain append would be shared
	base[0] = kv("a", "1")

	t.Run("no error leaves the attributes as they are", func(t *testing.T) {
		assert.Equal(t, base, withErrorType(base, ""))
	})

	t.Run("adds error.type", func(t *testing.T) {
		got := withErrorType(base, "boom")

		require.Len(t, got, 2)
		assert.Equal(t, attrErrorType, string(got[1].Key))
		assert.Equal(t, "boom", got[1].Value.Emit())
	})

	t.Run("never writes into the caller's slice, so results do not overwrite each other", func(t *testing.T) {
		first := withErrorType(base, "one")
		second := withErrorType(base, "two")

		assert.Equal(t, "one", first[1].Value.Emit())
		assert.Equal(t, "two", second[1].Value.Emit())
		assert.Len(t, base, 1)
		assert.Equal(t, attribute.KeyValue{}, base[:2][1], "spare capacity was not written to")
	})

	t.Run("works on a nil slice", func(t *testing.T) {
		assert.Empty(t, withErrorType(nil, ""))
		assert.Len(t, withErrorType(nil, "x"), 1)
	})
}

func TestMetricToolName(t *testing.T) {
	useTestTelemetry(t)

	assert.Equal(t, otherAttrValue, metricToolName("greet"), "unregistered names are bucketed")

	registerTool("greet")

	assert.Equal(t, "greet", metricToolName("greet"))
	assert.Equal(t, otherAttrValue, metricToolName("attacker-chosen-"+"12345"), "a client cannot create new label values")
	assert.Equal(t, otherAttrValue, metricToolName(""))
}

func TestMetricProtocolVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"2024-11-05", "2024-11-05"},
		{"2025-03-26", "2025-03-26"},
		{"2025-06-18", "2025-06-18"},
		{"2025-11-25", "2025-11-25"},
		{statelessMCPProtocolVersion, statelessMCPProtocolVersion},
		{"1999-01-01", otherAttrValue},
		{"garbage", otherAttrValue},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, metricProtocolVersion(tt.in), tt.in)
	}
}

func attrMap(attrs []attribute.KeyValue) map[string]string {
	out := map[string]string{}
	for _, a := range attrs {
		out[string(a.Key)] = a.Value.Emit()
	}

	return out
}

func TestBeginOperation(t *testing.T) {
	useTestTelemetry(t)
	registerTool("greet")

	t.Run("tools/call over HTTP", func(t *testing.T) {
		o := beginOperation("tools/call", "greet", "2025-06-18", true)

		assert.Equal(t, map[string]string{
			attrMCPMethodName:       "tools/call",
			attrMCPProtocolVersion:  "2025-06-18",
			attrGenAIToolName:       "greet",
			attrGenAIOperationName:  genAIOperationExecuteTool,
			attrNetworkTransport:    "tcp",
			attrNetworkProtocolName: "http",
		}, attrMap(o.attrs))
		assert.Equal(t, "greet", o.toolName)
	})

	t.Run("not over HTTP: no network attributes", func(t *testing.T) {
		o := beginOperation("tools/call", "greet", "2025-06-18", false)

		assert.NotContains(t, attrMap(o.attrs), attrNetworkTransport)
		assert.NotContains(t, attrMap(o.attrs), attrNetworkProtocolName)
	})

	t.Run("a method other than tools/call carries no tool", func(t *testing.T) {
		o := beginOperation("tools/list", "greet", "", false)

		assert.Equal(t, map[string]string{attrMCPMethodName: "tools/list"}, attrMap(o.attrs))
		assert.Empty(t, o.toolName)
	})

	t.Run("a client-chosen tool name or protocol version is bucketed, not used as a label", func(t *testing.T) {
		o := beginOperation("tools/call", "not-registered", "9999-99-99", false)

		assert.Equal(t, otherAttrValue, attrMap(o.attrs)[attrGenAIToolName])
		assert.Equal(t, otherAttrValue, attrMap(o.attrs)[attrMCPProtocolVersion])
	})

	t.Run("a session ID is never a metric attribute", func(t *testing.T) {
		assert.NotContains(t, attrMap(beginOperation("tools/call", "greet", "2025-06-18", true).attrs), attrMCPSessionID)
	})
}

func TestOperationObservation_End(t *testing.T) {
	ctx := context.Background()

	t.Run("success records duration and the request count, and no error", func(t *testing.T) {
		tel := useTestTelemetry(t)

		beginOperation("tools/list", "", "2025-06-18", false).end(ctx, "")

		count, _ := tel.hist(t, "mcp.server.operation.duration", kv(attrMCPMethodName, "tools/list"))
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, int64(1), tel.sum(t, "mcp.session.request", kv(attrMCPMethodName, "tools/list")))
		assert.Zero(t, tel.sum(t, "mcp.server.error"))
		assert.Zero(t, tel.sum(t, "mcp.resource.reads.total"))
	})

	t.Run("failure adds error.type to the duration and counts a server error", func(t *testing.T) {
		tel := useTestTelemetry(t)

		beginOperation("initialize", "", "", false).end(ctx, "*errors.errorString")

		count, _ := tel.hist(t, "mcp.server.operation.duration", kv(attrErrorType, "*errors.errorString"))
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, int64(1), tel.sum(t, "mcp.server.error",
			kv(attrMCPMethodName, "initialize"), kv(attrErrorType, "*errors.errorString")))
		assert.Equal(t, int64(1), tel.sum(t, "mcp.session.request", kv(attrMCPMethodName, "initialize")),
			"a failed request is still a request")
	})

	t.Run("mcp.session.request is labelled by method only", func(t *testing.T) {
		tel := useTestTelemetry(t)
		registerTool("greet")

		beginOperation("tools/call", "greet", "2025-06-18", true).end(ctx, "")

		m, ok := find(tel.collect(t), "mcp.session.request")
		require.True(t, ok)

		dp := m.Data.(metricdata.Sum[int64]).DataPoints[0]
		assert.Equal(t, 1, dp.Attributes.Len())
	})

	t.Run("resources/read counts a read, with error.type on failure", func(t *testing.T) {
		tel := useTestTelemetry(t)

		beginOperation(methodResourcesRead, "", "", false).end(ctx, "")
		beginOperation(methodResourcesRead, "", "", false).end(ctx, "resource_not_found")

		assert.Equal(t, int64(2), tel.sum(t, "mcp.resource.reads.total"))
		assert.Equal(t, int64(1), tel.sum(t, "mcp.resource.reads.total", kv(attrErrorType, "resource_not_found")))
	})

	t.Run("other methods do not count resource reads", func(t *testing.T) {
		tel := useTestTelemetry(t)

		beginOperation("prompts/get", "", "", false).end(ctx, "")

		assert.Zero(t, tel.sum(t, "mcp.resource.reads.total"))
	})

	t.Run("the measured duration is not negative and includes time spent before end", func(t *testing.T) {
		tel := useTestTelemetry(t)

		o := beginOperation("ping", "", "", false)
		o.end(ctx, "")

		_, sum := tel.hist(t, "mcp.server.operation.duration")
		assert.GreaterOrEqual(t, sum, 0.0)
	})
}

func TestToolObservation_End(t *testing.T) {
	ctx := context.Background()
	greet := kv(attrGenAIToolName, "greet")

	t.Run("success", func(t *testing.T) {
		tel := useTestTelemetry(t)
		registerTool("greet")
		res := &mcp.CallToolResult{Content: text("hi")}

		beginToolObservation("greet", 42).end(ctx, res, nil)

		assert.Equal(t, int64(1), tel.sum(t, "mcp.tool.invocation.total", greet))
		assert.Equal(t, int64(1), tel.sum(t, "mcp.tool.invocation.success", greet))
		assert.Zero(t, tel.sum(t, "mcp.tool.invocation.failure"))

		count, _ := tel.hist(t, "mcp.tool.invocation.duration", greet)
		assert.Equal(t, uint64(1), count)

		count, sum := tel.hist(t, "mcp.tool.request.message.size", greet)
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, 42.0, sum, "the request size is what BeforeCallTool measured")

		count, sum = tel.hist(t, "mcp.tool.response.message.size", greet)
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, float64(jsonSize(res)), sum)
	})

	t.Run("a returned error is a failure with error.type", func(t *testing.T) {
		tel := useTestTelemetry(t)
		registerTool("greet")

		beginToolObservation("greet", 1).end(ctx, nil, errors.New("boom"))

		assert.Equal(t, int64(1), tel.sum(t, "mcp.tool.invocation.total", greet))
		assert.Equal(t, int64(1), tel.sum(t, "mcp.tool.invocation.failure", greet, kv(attrErrorType, "*errors.errorString")))
		assert.Zero(t, tel.sum(t, "mcp.tool.invocation.success"))

		count, _ := tel.hist(t, "mcp.tool.response.message.size")
		assert.Zero(t, count, "no result, so no response to measure")

		count, _ = tel.hist(t, "mcp.tool.request.message.size", greet)
		assert.Equal(t, uint64(1), count, "the request was still received")
	})

	t.Run("an isError result is a failure with the tool_error type", func(t *testing.T) {
		tel := useTestTelemetry(t)
		registerTool("greet")

		beginToolObservation("greet", 1).end(ctx, &mcp.CallToolResult{IsError: true, Content: text("nope")}, nil)

		assert.Equal(t, int64(1), tel.sum(t, "mcp.tool.invocation.failure", greet, kv(attrErrorType, toolErrorType)))
		assert.Zero(t, tel.sum(t, "mcp.tool.invocation.success"))

		count, _ := tel.hist(t, "mcp.tool.response.message.size", greet)
		assert.Equal(t, uint64(1), count, "an isError result is still a response")
	})

	t.Run("an unregistered tool name is bucketed", func(t *testing.T) {
		tel := useTestTelemetry(t)

		beginToolObservation("whatever-the-client-sent", 1).end(ctx, nil, errors.New("unknown tool"))

		assert.Equal(t, int64(1), tel.sum(t, "mcp.tool.invocation.total", kv(attrGenAIToolName, otherAttrValue)))
		assert.Zero(t, tel.sum(t, "mcp.tool.invocation.total", kv(attrGenAIToolName, "whatever-the-client-sent")))
	})

	t.Run("total equals success plus failure across calls", func(t *testing.T) {
		tel := useTestTelemetry(t)
		registerTool("greet")

		beginToolObservation("greet", 1).end(ctx, &mcp.CallToolResult{}, nil)
		beginToolObservation("greet", 1).end(ctx, &mcp.CallToolResult{}, nil)
		beginToolObservation("greet", 1).end(ctx, nil, errors.New("x"))

		assert.Equal(t, int64(3), tel.sum(t, "mcp.tool.invocation.total"))
		assert.Equal(t, int64(2), tel.sum(t, "mcp.tool.invocation.success"))
		assert.Equal(t, int64(1), tel.sum(t, "mcp.tool.invocation.failure"))
	})
}

func TestJSONSize(t *testing.T) {
	res := &mcp.CallToolResult{Content: text("hi")}
	encoded, err := json.Marshal(res)
	require.NoError(t, err)

	assert.Equal(t, int64(len(encoded)), jsonSize(res))
	assert.Equal(t, int64(len("null")), jsonSize((*mcp.CallToolResult)(nil)))
	assert.Zero(t, jsonSize(make(chan int)), "a value that cannot be marshaled measures 0 instead of failing")
}
