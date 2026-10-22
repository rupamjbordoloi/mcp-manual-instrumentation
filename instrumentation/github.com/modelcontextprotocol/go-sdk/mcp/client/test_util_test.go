package client

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook/hooktest"
)

// telemetry records the spans the hooks produce.
type telemetry struct {
	spans *tracetest.SpanRecorder
	tp    *sdktrace.TracerProvider
}

// useTestTelemetry swaps the package's lazy tracers for ones backed by an
// in-memory recorder, and restores them (and the session registry) when the
// test ends. The session and shutdown spans normally go to a dedicated
// provider; here they land in the same recorder so their relationships can
// be checked.
func useTestTelemetry(t *testing.T) *telemetry {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	originalTracer, originalShutdownTracer := tracer, shutdownTracer
	tracer = func() trace.Tracer { return tp.Tracer("test") }
	shutdownTracer = func() trace.Tracer { return tp.Tracer("test-shutdown") }

	t.Cleanup(func() {
		tracer, shutdownTracer = originalTracer, originalShutdownTracer
		sessionParentCtx.Range(func(k, _ any) bool { sessionParentCtx.Delete(k); return true })
		require.NoError(t, tp.Shutdown(context.Background()))
	})

	return &telemetry{spans: recorder, tp: tp}
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

func timeout(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	return ctx
}

func text(s string) []mcp.Content { return []mcp.Content{&mcp.TextContent{Text: s}} }

// harness is a client connected to a real in-memory server. The client side is
// not otelc-instrumented, so the hooks are called by hand where otelc would
// call them: around NewClient, Connect and Close.
type harness struct {
	tel     *telemetry
	client  *mcp.Client
	session *mcp.ClientSession
	server  *mcp.ServerSession
}

func connect(t *testing.T, setup func(*mcp.Server)) *harness {
	t.Helper()

	h := &harness{tel: useTestTelemetry(t)}

	srv := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v0"}, nil)
	if setup != nil {
		setup(srv)
	}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	var err error

	h.server, err = srv.Connect(timeout(t), serverTransport, nil)
	require.NoError(t, err)

	h.client = mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	AfterNewClient(hooktest.NewMockHookContext(), h.client) // installs clientProtocolMiddleware

	h.session = h.connectWith(t, clientTransport, nil)

	t.Cleanup(func() {
		_ = h.session.Close()
		_ = h.server.Close()
	})

	return h
}

// connectWith runs Connect with the hooks around it. ctx is what the
// application passes to Connect (nil means context.Background()).
func (h *harness) connectWith(t *testing.T, transport mcp.Transport, ctx context.Context) *mcp.ClientSession {
	t.Helper()

	if ctx == nil {
		ctx = timeout(t)
	}

	ictx := hooktest.NewMockHookContext(h.client, ctx, transport, nil)
	BeforeConnect(ictx, h.client, ctx, transport, nil)

	if c, ok := ictx.GetParam(1).(context.Context); ok && c != nil {
		ctx = c // the session context otelc hands to Connect
	}

	session, err := h.client.Connect(ctx, transport, nil)
	AfterConnect(ictx, session, err)
	require.NoError(t, err)

	return session
}

// closeSession runs Close with the hooks around it.
func (h *harness) closeSession(t *testing.T) error {
	t.Helper()

	ictx := hooktest.NewMockHookContext(h.session)
	BeforeClose(ictx, h.session)
	err := h.session.Close()
	AfterClose(ictx, err)

	return err
}

// request starts a root server-kind span, like the HTTP request span in the
// application's handler, and returns a context carrying it.
func (h *harness) request(t *testing.T, name string) (context.Context, trace.Span) {
	t.Helper()

	ctx, span := h.tel.tp.Tracer("app").Start(timeout(t), name, trace.WithSpanKind(trace.SpanKindServer))
	t.Cleanup(func() { span.End() })

	return ctx, span
}

// greetTool is a plain tool for the in-memory server.
func greetTool(srv *mcp.Server) {
	srv.AddTool(&mcp.Tool{Name: "greet", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: text("hi")}, nil
		})
}
