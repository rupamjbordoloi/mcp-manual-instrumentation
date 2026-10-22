package server

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook/hooktest"
)

// telemetry records the spans and metrics the hooks produce.
type telemetry struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

// useTestTelemetry swaps the package's lazy tracer and metrics accessors for
// ones backed by in-memory recorders, and restores them (and the tool and
// session registries) when the test ends.
func useTestTelemetry(t *testing.T) *telemetry {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	instruments := newServerMetrics(mp.Meter("test"))

	originalTracer, originalMetrics := tracer, metrics
	tracer = func() trace.Tracer { return tp.Tracer("test") }
	metrics = func() *serverMetrics { return instruments }

	t.Cleanup(func() {
		tracer, metrics = originalTracer, originalMetrics
		registeredTools.Range(func(k, _ any) bool { registeredTools.Delete(k); return true })
		serverSessionStart.Range(func(k, _ any) bool { serverSessionStart.Delete(k); return true })
		require.NoError(t, tp.Shutdown(context.Background()))
	})

	return &telemetry{spans: recorder, reader: reader}
}

func (tel *telemetry) collect(t *testing.T) []metricdata.Metrics {
	t.Helper()

	var rm metricdata.ResourceMetrics
	require.NoError(t, tel.reader.Collect(context.Background(), &rm))

	var out []metricdata.Metrics
	for _, sm := range rm.ScopeMetrics {
		out = append(out, sm.Metrics...)
	}

	return out
}

func find(ms []metricdata.Metrics, name string) (metricdata.Metrics, bool) {
	for _, m := range ms {
		if m.Name == name {
			return m, true
		}
	}

	return metricdata.Metrics{}, false
}

// matches reports whether set holds every attribute in want.
func matches(set attribute.Set, want []attribute.KeyValue) bool {
	for _, kv := range want {
		v, ok := set.Value(kv.Key)
		if !ok || v.String() != kv.Value.String() {
			return false
		}
	}

	return true
}

// sum totals the counter (or up-down counter) data points that carry every
// attribute in want; 0 when the metric was never recorded.
func (tel *telemetry) sum(t *testing.T, name string, want ...attribute.KeyValue) int64 {
	t.Helper()

	m, ok := find(tel.collect(t), name)
	if !ok {
		return 0
	}

	d, isSum := m.Data.(metricdata.Sum[int64])
	require.True(t, isSum, "%s is not an int64 sum", name)

	var total int64

	for _, dp := range d.DataPoints {
		if matches(dp.Attributes, want) {
			total += dp.Value
		}
	}

	return total
}

// hist returns the observation count and the sum of values of the histogram
// data points that carry every attribute in want.
func (tel *telemetry) hist(t *testing.T, name string, want ...attribute.KeyValue) (uint64, float64) {
	t.Helper()

	m, ok := find(tel.collect(t), name)
	if !ok {
		return 0, 0
	}

	var (
		count uint64
		total float64
	)

	switch d := m.Data.(type) {
	case metricdata.Histogram[float64]:
		for _, dp := range d.DataPoints {
			if matches(dp.Attributes, want) {
				count += dp.Count
				total += dp.Sum
			}
		}

	case metricdata.Histogram[int64]:
		for _, dp := range d.DataPoints {
			if matches(dp.Attributes, want) {
				count += dp.Count
				total += float64(dp.Sum)
			}
		}

	default:
		require.Failf(t, "not a histogram", "%s is %T", name, m.Data)
	}

	return count, total
}

func (tel *telemetry) named(name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range tel.spans.Ended() {
		if s.Name() == name {
			out = append(out, s)
		}
	}

	return out
}

// span returns the one ended span with that name.
func (tel *telemetry) span(t *testing.T, name string) sdktrace.ReadOnlySpan {
	t.Helper()

	got := tel.named(name)
	require.Len(t, got, 1, "spans named %q", name)

	return got[0]
}

func spanAttrs(s sdktrace.ReadOnlySpan) map[string]string {
	out := map[string]string{}
	for _, kv := range s.Attributes() {
		out[string(kv.Key)] = kv.Value.Emit()
	}

	return out
}

func kv(key, value string) attribute.KeyValue { return attribute.String(key, value) }

// ---- a real in-memory MCP server and client ----

// harness is a server with the instrumentation's middleware installed, and a
// client connected to it over an in-memory transport. Neither side is
// otelc-instrumented, so the hooks are called by hand where otelc would.
type harness struct {
	tel     *telemetry
	server  *mcp.Server
	session *mcp.ServerSession
	client  *mcp.ClientSession
}

func newHarness(t *testing.T, setup func(*mcp.Server)) *harness {
	t.Helper()

	h := &harness{tel: useTestTelemetry(t)}
	h.server = mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v0"}, nil)
	AfterNewServer(hooktest.NewMockHookContext(), h.server) // installs serverProtocolMiddleware

	if setup != nil {
		setup(h.server)
	}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	var err error

	h.session, err = h.server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	h.client, err = mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil).Connect(ctx, clientTransport, nil)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = h.client.Close()
		_ = h.session.Close()
	})

	return h
}

func timeout(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	return ctx
}

// addTool registers a tool the way otelc's hooks see it: BeforeServerAddTool
// and AfterServerAddTool around the registration, and the handler wrapped by
// BeforeCallTool and AfterCallTool the way they wrap (*Server).callTool.
func addTool(srv *mcp.Server, name string, handler mcp.ToolHandler) {
	tool := &mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}

	register := hooktest.NewMockHookContext(srv, tool, nil)
	BeforeServerAddTool(register, srv, tool, nil)

	srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ictx := hooktest.NewMockHookContext(srv, ctx, req)
		BeforeCallTool(ictx, srv, ctx, req)

		if c, ok := ictx.GetParam(1).(context.Context); ok && c != nil {
			ctx = c // the span-carrying context otelc passes on to the handler
		}

		res, err := handler(ctx, req)
		AfterCallTool(ictx, res, err)

		return res, err
	})

	AfterServerAddTool(register)
}

func text(s string) []mcp.Content { return []mcp.Content{&mcp.TextContent{Text: s}} }
