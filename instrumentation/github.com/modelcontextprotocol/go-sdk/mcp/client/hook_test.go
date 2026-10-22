package client

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook/hooktest"
)

// ---- small helpers ----

func TestIsSessionLifecycleMethod(t *testing.T) {
	for _, m := range []string{"server/discover", "initialize", "notifications/initialized"} {
		assert.True(t, isSessionLifecycleMethod(m), m)
	}

	for _, m := range []string{"tools/call", "tools/list", "resources/read", "prompts/get", "ping", "", "notifications/cancelled"} {
		assert.False(t, isSessionLifecycleMethod(m), "%q must keep the caller's own context, not be pinned to mcp.session", m)
	}
}

func TestClientMethodToSpanName(t *testing.T) {
	tests := []struct{ method, tool, want string }{
		{"tools/call", "greet", "tools/call greet"},
		{"tools/call", "", "tools/call"},
		{"tools/list", "", "tools/list"},
		{"tools/list", "ignored", "tools/list"},
		{"initialize", "", "initialize"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, clientMethodToSpanName(tt.method, tt.tool), tt.method+"/"+tt.tool)
	}
}

func TestIsToolResultError(t *testing.T) {
	var typedNil *mcp.CallToolResult

	assert.False(t, isToolResultError(nil))
	assert.False(t, isToolResultError(typedNil))
	assert.False(t, isToolResultError(&mcp.ListToolsResult{}))
	assert.False(t, isToolResultError(&mcp.CallToolResult{}))
	assert.True(t, isToolResultError(&mcp.CallToolResult{IsError: true}))
}

func TestErrorType(t *testing.T) {
	assert.Empty(t, errorType(nil))
	assert.Equal(t, "mcp_error", errorType(errors.New("secret detail")), "a constant: error text can hold user data")
}

func TestShouldRecordSessionID(t *testing.T) {
	assert.True(t, shouldRecordSessionID(""))
	assert.True(t, shouldRecordSessionID("2025-06-18"))
	assert.True(t, shouldRecordSessionID("2025-11-25"))
	assert.False(t, shouldRecordSessionID(statelessMCPProtocolVersion))
}

func TestAppendMCPContextAttributes(t *testing.T) {
	asMap := func(attrs []attribute.KeyValue) map[string]string {
		out := map[string]string{}
		for _, a := range attrs {
			out[string(a.Key)] = a.Value.String()
		}

		return out
	}

	tests := []struct {
		name            string
		version, sessID string
		want            map[string]string
	}{
		{"both", "2025-06-18", "s1", map[string]string{attrMCPProtocolVersion: "2025-06-18", attrMCPSessionID: "s1"}},
		{"version only", "2025-06-18", "", map[string]string{attrMCPProtocolVersion: "2025-06-18"}},
		{"stateless drops the session", statelessMCPProtocolVersion, "s1", map[string]string{attrMCPProtocolVersion: statelessMCPProtocolVersion}},
		{"nothing known", "", "", map[string]string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attrs []attribute.KeyValue

			appendMCPContextAttributes(&attrs, tt.version, tt.sessID)

			assert.Equal(t, tt.want, asMap(attrs))
		})
	}

	assert.NotPanics(t, func() { appendMCPContextAttributes(nil, "2025-06-18", "s") })
}

func TestAppendMCPContextToSpan(t *testing.T) {
	tel := useTestTelemetry(t)

	t.Run("sets what is known on the span", func(t *testing.T) {
		_, span := tracer().Start(context.Background(), "a")
		appendMCPContextToSpan(span, "2025-06-18", "s1")
		span.End()

		attrs := spanAttrs(tel.span(t, "a"))
		assert.Equal(t, "2025-06-18", attrs[attrMCPProtocolVersion])
		assert.Equal(t, "s1", attrs[attrMCPSessionID])
	})

	t.Run("a stateless protocol version carries no session ID", func(t *testing.T) {
		_, span := tracer().Start(context.Background(), "b")
		appendMCPContextToSpan(span, statelessMCPProtocolVersion, "s1")
		span.End()

		assert.NotContains(t, spanAttrs(tel.span(t, "b")), attrMCPSessionID)
	})

	t.Run("a nil span is tolerated", func(t *testing.T) {
		assert.NotPanics(t, func() { appendMCPContextToSpan(nil, "2025-06-18", "s") })
	})
}

func TestRequestToolName(t *testing.T) {
	tests := []struct {
		name   string
		method string
		req    mcp.Request
		want   string
	}{
		{"a client tools/call request", "tools/call", &mcp.ClientRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Name: "greet"}}, "greet"},
		{"a server-side style request", "tools/call", &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "greet"}}, "greet"},
		{"only tools/call has a tool", "tools/list", &mcp.ClientRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Name: "greet"}}, ""},
		{"nil request", "tools/call", nil, ""},
		{"params of another type", "tools/call", &mcp.ClientRequest[*mcp.ListToolsParams]{Params: &mcp.ListToolsParams{}}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, requestToolName(tt.method, tt.req))
		})
	}
}

func TestRequestProtocolVersion(t *testing.T) {
	entry := sessionEntry{protocolVersion: "2025-06-18"}

	tests := []struct {
		name     string
		method   string
		req      mcp.Request
		hasEntry bool
		want     string
	}{
		{
			"initialize carries the version it proposes", "initialize",
			&mcp.ClientRequest[*mcp.InitializeParams]{Params: &mcp.InitializeParams{ProtocolVersion: "2025-11-25"}}, false, "2025-11-25",
		},
		{
			"a version in the request _meta wins", "tools/list",
			&mcp.ClientRequest[*mcp.ListToolsParams]{Params: &mcp.ListToolsParams{Meta: mcp.Meta{mcpProtocolVersionMetaKey: "2026-07-28"}}}, true, "2026-07-28",
		},
		{
			"otherwise the session's negotiated version", "tools/list",
			&mcp.ClientRequest[*mcp.ListToolsParams]{Params: &mcp.ListToolsParams{}}, true, "2025-06-18",
		},
		{"nothing known", "tools/list", &mcp.ClientRequest[*mcp.ListToolsParams]{Params: &mcp.ListToolsParams{}}, false, ""},
		{"nil request falls back to the session", "tools/list", nil, true, "2025-06-18"},
		{"nil request and no session", "tools/list", nil, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, requestProtocolVersion(tt.method, tt.req, entry, tt.hasEntry))
		})
	}
}

func TestRequestSessionID(t *testing.T) {
	assert.Equal(t, "s1", requestSessionID(nil, sessionEntry{protocolVersion: "2025-06-18", sessionID: "s1"}, true))
	assert.Empty(t, requestSessionID(nil, sessionEntry{protocolVersion: statelessMCPProtocolVersion, sessionID: "s1"}, true),
		"the stateless protocol has no sessions")
	assert.Empty(t, requestSessionID(nil, sessionEntry{}, false))
}

func TestFinishSpan(t *testing.T) {
	start := func(t *testing.T) (*telemetry, trace.Span) {
		t.Helper()

		tel := useTestTelemetry(t)
		_, span := tracer().Start(context.Background(), "op")

		return tel, span
	}

	t.Run("success ends the span with OK", func(t *testing.T) {
		tel, span := start(t)

		finishSpan(span, nil, "")

		assert.Equal(t, codes.Ok, tel.span(t, "op").Status().Code)
	})

	t.Run("an error is recorded as a constant type, with the message on the status", func(t *testing.T) {
		tel, span := start(t)

		finishSpan(span, errors.New("boom"), "")

		s := tel.span(t, "op")
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, "boom", s.Status().Description)
		assert.Equal(t, "mcp_error", spanAttrs(s)[attrErrorType])
		require.Len(t, s.Events(), 1)
		assert.Equal(t, "exception", s.Events()[0].Name)
	})

	t.Run("a semantic error type marks failure without an exception event", func(t *testing.T) {
		tel, span := start(t)

		finishSpan(span, nil, toolErrorType)

		s := tel.span(t, "op")
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, toolErrorType, spanAttrs(s)[attrErrorType])
		assert.Empty(t, s.Events())
	})
}

// ---- lazy accessors ----

func TestLazyAccessors(t *testing.T) {
	t.Run("tracer is created once", func(t *testing.T) {
		require.NotNil(t, tracer())
		assert.Equal(t, tracer(), tracer())
	})

	t.Run("the shutdown tracer is created on first use and its provider is kept for AfterMain", func(t *testing.T) {
		t.Setenv("OTEL_TRACES_EXPORTER", "none")
		shutdownProvider.Store(nil)
		t.Cleanup(func() {
			if p := shutdownProvider.Swap(nil); p != nil {
				_ = p.Shutdown(context.Background())
			}
		})

		got := newShutdownTracer()

		require.NotNil(t, got)
		assert.NotNil(t, shutdownProvider.Load())
	})

	t.Run("an exporter that cannot be built falls back to the shared tracer", func(t *testing.T) {
		t.Setenv("OTEL_TRACES_EXPORTER", "no-such-exporter")
		shutdownProvider.Store(nil)

		got := newShutdownTracer()

		require.NotNil(t, got, "the shutdown spans are still created, even if export cannot be guaranteed")
		assert.Nil(t, shutdownProvider.Load(), "there is nothing for AfterMain to release")
	})
}

// shutdownSpy records that the provider was shut down.
type shutdownSpy struct {
	mu   sync.Mutex
	down bool
}

func (*shutdownSpy) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (*shutdownSpy) OnEnd(sdktrace.ReadOnlySpan)                     {}
func (*shutdownSpy) ForceFlush(context.Context) error                { return nil }
func (s *shutdownSpy) Shutdown(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = true

	return nil
}

func TestAfterMain(t *testing.T) {
	t.Run("releases the dedicated provider", func(t *testing.T) {
		spy := &shutdownSpy{}
		shutdownProvider.Store(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spy)))
		t.Cleanup(func() { shutdownProvider.Store(nil) })

		AfterMain(hooktest.NewMockHookContext())

		assert.True(t, spy.down)
	})

	t.Run("without a dedicated provider there is nothing to release", func(t *testing.T) {
		shutdownProvider.Store(nil)

		assert.NotPanics(t, func() { AfterMain(hooktest.NewMockHookContext()) })
	})
}

// ---- hooks ----

func TestAfterNewClient(t *testing.T) {
	t.Run("installs the protocol middleware", func(t *testing.T) {
		h := connect(t, nil)
		h.tel.spans.Reset()

		_, err := h.session.ListTools(timeout(t), nil)
		require.NoError(t, err)

		assert.Len(t, h.tel.named("tools/list"), 1)
	})

	t.Run("disabled: no middleware is installed", func(t *testing.T) {
		tel := useTestTelemetry(t)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)

		srv := mcp.NewServer(&mcp.Implementation{Name: "s", Version: "v0"}, nil)
		a, b := mcp.NewInMemoryTransports()
		ss, err := srv.Connect(timeout(t), a, nil)
		require.NoError(t, err)

		client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
		AfterNewClient(hooktest.NewMockHookContext(), client)

		cs, err := client.Connect(timeout(t), b, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })

		_, err = cs.ListTools(timeout(t), nil)
		require.NoError(t, err)

		assert.Empty(t, tel.spans.Ended())
	})
}

func TestSessionTrace(t *testing.T) {
	t.Run("mcp.session is a root span, and the handshake nests under it", func(t *testing.T) {
		h := connect(t, nil)
		require.NoError(t, h.closeSession(t))

		session := h.tel.span(t, "mcp.session")
		assert.False(t, session.Parent().IsValid(), "the session starts its own trace")
		assert.Equal(t, trace.SpanKindClient, session.SpanKind())
		assert.Equal(t, codes.Ok, session.Status().Code)

		var handshake []sdktrace.ReadOnlySpan
		for _, name := range []string{"initialize", "server/discover", "notifications/initialized"} {
			handshake = append(handshake, h.tel.named(name)...)
		}

		// Which methods open a session depends on the SDK version.
		require.NotEmpty(t, handshake)

		for _, s := range handshake {
			assert.Equal(t, session.SpanContext().SpanID(), s.Parent().SpanID(), "%s is a child of mcp.session", s.Name())
			assert.Equal(t, session.SpanContext().TraceID(), s.SpanContext().TraceID(), s.Name())
			assert.Equal(t, trace.SpanKindClient, s.SpanKind(), s.Name())
		}
	})

	t.Run("mcp.session carries what the handshake negotiated", func(t *testing.T) {
		h := connect(t, nil)
		require.NoError(t, h.closeSession(t))

		attrs := spanAttrs(h.tel.span(t, "mcp.session"))

		if result := h.session.InitializeResult(); result != nil && result.ProtocolVersion != "" {
			assert.Equal(t, result.ProtocolVersion, attrs[attrMCPProtocolVersion])
		} else {
			assert.NotContains(t, attrs, attrMCPProtocolVersion)
		}
	})

	t.Run("shutdown is a child of the session and the session ends last", func(t *testing.T) {
		h := connect(t, nil)
		require.NoError(t, h.closeSession(t))

		session := h.tel.span(t, "mcp.session")
		shutdown := h.tel.span(t, "mcp.shutdown")

		assert.Equal(t, session.SpanContext().SpanID(), shutdown.Parent().SpanID())
		assert.Equal(t, session.SpanContext().TraceID(), shutdown.SpanContext().TraceID())
		assert.Equal(t, trace.SpanKindClient, shutdown.SpanKind())
		assert.Equal(t, codes.Ok, shutdown.Status().Code)

		attrs := spanAttrs(shutdown)
		assert.Equal(t, "shutdown", attrs[attrMCPMethodName])
		assert.Equal(t, "jsonrpc", attrs[attrRPCSystemName])
		assert.Equal(t, "shutdown", attrs[attrRPCMethod])

		assert.False(t, shutdown.EndTime().After(session.EndTime()), "mcp.shutdown ends before mcp.session")

		for _, s := range h.tel.spans.Ended() {
			assert.False(t, s.EndTime().After(session.EndTime()), "%s ended after the session span", s.Name())
		}
	})

	t.Run("the session span stays open until the session is closed", func(t *testing.T) {
		h := connect(t, nil)

		assert.Empty(t, h.tel.named("mcp.session"), "nothing ends the session span at Connect time")
		assert.Empty(t, h.tel.named("mcp.shutdown"))

		_, tracked := sessionParentCtx.Load(h.session)
		assert.True(t, tracked)
	})

	t.Run("closing releases the session, and a second close adds nothing", func(t *testing.T) {
		h := connect(t, nil)
		require.NoError(t, h.closeSession(t))

		_, tracked := sessionParentCtx.Load(h.session)
		assert.False(t, tracked, "the registry does not grow")

		before := len(h.tel.spans.Ended())
		_ = h.closeSession(t)

		assert.Len(t, h.tel.spans.Ended(), before, "a repeat close creates no spans")
		assert.Len(t, h.tel.named("mcp.shutdown"), 1)
		assert.Len(t, h.tel.named("mcp.session"), 1)
	})
}

func TestToolCallsAreSeparateTraces(t *testing.T) {
	t.Run("a call made inside a request joins the request's trace, not the session's", func(t *testing.T) {
		h := connect(t, greetTool)
		ctx, request := h.request(t, "POST /call")

		res, err := h.session.CallTool(ctx, &mcp.CallToolParams{Name: "greet"})
		require.NoError(t, err)
		assert.False(t, res.IsError)

		require.NoError(t, h.closeSession(t))

		call := h.tel.span(t, "tools/call greet")
		session := h.tel.span(t, "mcp.session")

		assert.Equal(t, request.SpanContext().SpanID(), call.Parent().SpanID(), "a child of the request span")
		assert.Equal(t, request.SpanContext().TraceID(), call.SpanContext().TraceID())
		assert.NotEqual(t, session.SpanContext().TraceID(), call.SpanContext().TraceID(), "never in the session trace")
		assert.Equal(t, trace.SpanKindClient, call.SpanKind())
	})

	t.Run("a call with no request span starts its own trace", func(t *testing.T) {
		h := connect(t, greetTool)

		_, err := h.session.CallTool(timeout(t), &mcp.CallToolParams{Name: "greet"})
		require.NoError(t, err)
		require.NoError(t, h.closeSession(t))

		call := h.tel.span(t, "tools/call greet")
		assert.False(t, call.Parent().IsValid())
		assert.NotEqual(t, h.tel.span(t, "mcp.session").SpanContext().TraceID(), call.SpanContext().TraceID())
	})

	t.Run("two requests give two traces", func(t *testing.T) {
		h := connect(t, greetTool)

		for _, name := range []string{"POST /a", "POST /b"} {
			ctx, _ := h.request(t, name)
			_, err := h.session.CallTool(ctx, &mcp.CallToolParams{Name: "greet"})
			require.NoError(t, err)
		}

		calls := h.tel.named("tools/call greet")
		require.Len(t, calls, 2)
		assert.NotEqual(t, calls[0].SpanContext().TraceID(), calls[1].SpanContext().TraceID())
	})

	t.Run("several tools in one request are siblings in one trace", func(t *testing.T) {
		h := connect(t, greetTool)
		ctx, request := h.request(t, "POST /call")

		var wg sync.WaitGroup

		for i := 0; i < 3; i++ {
			wg.Add(1)

			go func() {
				defer wg.Done()

				_, err := h.session.CallTool(ctx, &mcp.CallToolParams{Name: "greet"})
				assert.NoError(t, err)
			}()
		}

		wg.Wait()

		calls := h.tel.named("tools/call greet")
		require.Len(t, calls, 3)

		ids := map[trace.SpanID]bool{}

		for _, c := range calls {
			assert.Equal(t, request.SpanContext().SpanID(), c.Parent().SpanID(), "siblings under the request span")
			assert.Equal(t, request.SpanContext().TraceID(), c.SpanContext().TraceID())

			ids[c.SpanContext().SpanID()] = true
		}

		assert.Len(t, ids, 3, "each call has its own span")
	})

	t.Run("other requests after the handshake follow the same rule", func(t *testing.T) {
		h := connect(t, greetTool)
		ctx, request := h.request(t, "GET /tools")

		_, err := h.session.ListTools(ctx, nil)
		require.NoError(t, err)
		require.NoError(t, h.session.Ping(ctx, nil))

		for _, name := range []string{"tools/list", "ping"} {
			s := h.tel.span(t, name)
			assert.Equal(t, request.SpanContext().SpanID(), s.Parent().SpanID(), name)
		}
	})

	t.Run("a session started inside a request nests under that request (the caller's context is honoured)", func(t *testing.T) {
		tel := useTestTelemetry(t)
		h := &harness{tel: tel}

		srv := mcp.NewServer(&mcp.Implementation{Name: "s", Version: "v0"}, nil)
		a, b := mcp.NewInMemoryTransports()
		var err error
		h.server, err = srv.Connect(timeout(t), a, nil)
		require.NoError(t, err)
		h.client = mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
		AfterNewClient(hooktest.NewMockHookContext(), h.client)

		ctx, request := h.request(t, "POST /first")
		h.session = h.connectWith(t, b, ctx)
		require.NoError(t, h.closeSession(t))

		assert.Equal(t, request.SpanContext().SpanID(), tel.span(t, "mcp.session").Parent().SpanID(),
			"start the session from a context without a span to keep it a separate trace, as the proxy does")
	})
}

func TestToolCallSpan(t *testing.T) {
	t.Run("attributes", func(t *testing.T) {
		h := connect(t, greetTool)

		_, err := h.session.CallTool(timeout(t), &mcp.CallToolParams{Name: "greet"})
		require.NoError(t, err)

		s := h.tel.span(t, "tools/call greet")
		attrs := spanAttrs(s)

		assert.Equal(t, "tools/call", attrs[attrMCPMethodName])
		assert.Equal(t, "jsonrpc", attrs[attrRPCSystemName])
		assert.Equal(t, "tools/call", attrs[attrRPCMethod])
		assert.Equal(t, "greet", attrs[attrGenAIToolName])
		assert.Equal(t, genAIOperationExecuteTool, attrs[attrGenAIOperationName])
		assert.Equal(t, "greet", attrs[attrToolNameLegacy])
		assert.Equal(t, codes.Ok, s.Status().Code)

		if result := h.session.InitializeResult(); result != nil && result.ProtocolVersion != "" {
			assert.Equal(t, result.ProtocolVersion, attrs[attrMCPProtocolVersion])
		}
	})

	t.Run("other methods carry no tool attributes", func(t *testing.T) {
		h := connect(t, greetTool)

		_, err := h.session.ListTools(timeout(t), nil)
		require.NoError(t, err)

		attrs := spanAttrs(h.tel.span(t, "tools/list"))
		assert.NotContains(t, attrs, attrGenAIToolName)
		assert.NotContains(t, attrs, attrGenAIOperationName)
		assert.NotContains(t, attrs, attrToolNameLegacy)
	})

	t.Run("an isError result fails the span with tool_error", func(t *testing.T) {
		h := connect(t, func(srv *mcp.Server) {
			srv.AddTool(&mcp.Tool{Name: "greet", InputSchema: map[string]any{"type": "object"}},
				func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{IsError: true, Content: text("nope")}, nil
				})
		})

		res, err := h.session.CallTool(timeout(t), &mcp.CallToolParams{Name: "greet"})
		require.NoError(t, err)
		assert.True(t, res.IsError)

		s := h.tel.span(t, "tools/call greet")
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, toolErrorType, spanAttrs(s)[attrErrorType])
		assert.Empty(t, s.Events(), "a tool-level failure is not an exception")
	})

	t.Run("a protocol error fails the span with the constant error type", func(t *testing.T) {
		h := connect(t, greetTool)

		_, err := h.session.CallTool(timeout(t), &mcp.CallToolParams{Name: "no-such-tool"})
		require.Error(t, err)

		s := h.tel.span(t, "tools/call no-such-tool")
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, "mcp_error", spanAttrs(s)[attrErrorType])
	})
}

func TestConnectFailure(t *testing.T) {
	t.Run("a failed Connect ends the session span with the error and registers nothing", func(t *testing.T) {
		tel := useTestTelemetry(t)
		client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
		AfterNewClient(hooktest.NewMockHookContext(), client)

		ctx := timeout(t)
		ictx := hooktest.NewMockHookContext(client, ctx, failingTransport{}, nil)
		BeforeConnect(ictx, client, ctx, failingTransport{}, nil)

		connectCtx, _ := ictx.GetParam(1).(context.Context)
		session, err := client.Connect(connectCtx, failingTransport{}, nil)
		require.Error(t, err)
		AfterConnect(ictx, session, err)

		s := tel.span(t, "mcp.session")
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, "mcp_error", spanAttrs(s)[attrErrorType])

		count := 0
		sessionParentCtx.Range(func(_, _ any) bool { count++; return true })
		assert.Zero(t, count)
	})

	t.Run("a nil session with no error ends the span OK and registers nothing", func(t *testing.T) {
		tel := useTestTelemetry(t)
		ictx := hooktest.NewMockHookContext(nil, context.Background(), nil, nil)
		BeforeConnect(ictx, nil, context.Background(), nil, nil)

		AfterConnect(ictx, nil, nil)

		assert.Equal(t, codes.Ok, tel.span(t, "mcp.session").Status().Code)
	})

	t.Run("AfterConnect without BeforeConnect does nothing", func(t *testing.T) {
		tel := useTestTelemetry(t)

		assert.NotPanics(t, func() { AfterConnect(hooktest.NewMockHookContext(), nil, nil) })
		assert.Empty(t, tel.spans.Ended())
	})

	t.Run("BeforeConnect hands Connect a context carrying the session span", func(t *testing.T) {
		useTestTelemetry(t)
		ictx := hooktest.NewMockHookContext(nil, context.Background(), nil, nil)

		BeforeConnect(ictx, nil, context.Background(), nil, nil)

		connectCtx, ok := ictx.GetParam(1).(context.Context)
		require.True(t, ok)
		assert.True(t, trace.SpanContextFromContext(connectCtx).IsValid())
	})
}

type failingTransport struct{}

func (failingTransport) Connect(context.Context) (mcp.Connection, error) {
	return nil, errors.New("dial failed")
}

func TestClose_Edges(t *testing.T) {
	t.Run("BeforeClose for a session that was never registered does nothing", func(t *testing.T) {
		tel := useTestTelemetry(t)
		session := &mcp.ClientSession{}
		ictx := hooktest.NewMockHookContext(session)

		assert.NotPanics(t, func() {
			BeforeClose(ictx, session)
			AfterClose(ictx, nil)
		})
		assert.Empty(t, tel.spans.Ended())
	})

	t.Run("a registry entry of an unexpected type is ignored", func(t *testing.T) {
		tel := useTestTelemetry(t)
		session := &mcp.ClientSession{}
		sessionParentCtx.Store(session, "not a sessionEntry")
		ictx := hooktest.NewMockHookContext(session)

		assert.NotPanics(t, func() {
			BeforeClose(ictx, session)
			AfterClose(ictx, nil)
		})
		assert.Empty(t, tel.spans.Ended())
	})

	t.Run("AfterClose without BeforeClose does nothing", func(t *testing.T) {
		tel := useTestTelemetry(t)

		assert.NotPanics(t, func() { AfterClose(hooktest.NewMockHookContext(), errors.New("x")) })
		assert.Empty(t, tel.spans.Ended())
	})

	t.Run("a close error fails both the shutdown and the session spans", func(t *testing.T) {
		h := connect(t, nil)
		ictx := hooktest.NewMockHookContext(h.session)
		BeforeClose(ictx, h.session)

		AfterClose(ictx, errors.New("close failed"))

		for _, name := range []string{"mcp.shutdown", "mcp.session"} {
			s := h.tel.span(t, name)
			assert.Equal(t, codes.Error, s.Status().Code, name)
			assert.Equal(t, "mcp_error", spanAttrs(s)[attrErrorType], name)
		}
	})
}

func TestHooks_Disabled(t *testing.T) {
	t.Run("nothing is created and the connect context is left alone", func(t *testing.T) {
		tel := useTestTelemetry(t)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)

		ctx := context.Background()
		ictx := hooktest.NewMockHookContext(nil, ctx, nil, nil)

		BeforeConnect(ictx, nil, ctx, nil, nil)
		AfterConnect(ictx, nil, nil)

		assert.Equal(t, ctx, ictx.GetParam(1), "Connect gets the caller's own context")
		assert.Empty(t, tel.spans.Ended())
	})

	t.Run("a session started while disabled is not shut down with spans", func(t *testing.T) {
		h := connect(t, nil)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)

		before := len(h.tel.spans.Ended())
		_ = h.closeSession(t)

		assert.Len(t, h.tel.spans.Ended(), before)
	})

	t.Run("requests are not spanned", func(t *testing.T) {
		h := connect(t, greetTool)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)
		h.tel.spans.Reset()

		_, err := h.session.CallTool(timeout(t), &mcp.CallToolParams{Name: "greet"})
		require.NoError(t, err)

		assert.Empty(t, h.tel.spans.Ended())
	})
}

// The rule the whole client design rests on: after Connect has returned, only
// the handshake methods are pinned to mcp.session. Everything else keeps the
// context the caller passed in.
func TestOnlyHandshakeMethodsArePinnedToTheSession(t *testing.T) {
	h := connect(t, greetTool)
	ctx, request := h.request(t, "POST /call")

	value, ok := sessionParentCtx.Load(h.session)
	require.True(t, ok)

	entry := value.(sessionEntry)

	next := clientProtocolMiddleware(func(context.Context, string, mcp.Request) (mcp.Result, error) { return nil, nil })

	h.tel.spans.Reset() // drop the connect handshake's own spans

	_, err := next(ctx, "notifications/initialized",
		&mcp.ClientRequest[*mcp.InitializedParams]{Session: h.session, Params: &mcp.InitializedParams{}})
	require.NoError(t, err)

	_, err = next(ctx, "tools/list",
		&mcp.ClientRequest[*mcp.ListToolsParams]{Session: h.session, Params: &mcp.ListToolsParams{}})
	require.NoError(t, err)

	lifecycle := h.tel.span(t, "notifications/initialized")
	assert.Equal(t, entry.spanContext.SpanID(), lifecycle.Parent().SpanID(), "a handshake method goes back under mcp.session")
	assert.Equal(t, entry.spanContext.TraceID(), lifecycle.SpanContext().TraceID())
	assert.False(t, lifecycle.Parent().IsRemote(), "the session span is local to this process")

	other := h.tel.span(t, "tools/list")
	assert.Equal(t, request.SpanContext().SpanID(), other.Parent().SpanID(), "anything else keeps the caller's context")
	assert.NotEqual(t, entry.spanContext.TraceID(), other.SpanContext().TraceID())
}
