package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook/hooktest"
	"go.opentelemetry.io/otelc/pkg/runtime"
)

var (
	testTraceID    = trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	testServerSpan = trace.SpanID{0x5e, 0x5e, 0x5e, 0x5e, 0x5e, 0x5e, 0x5e, 0x5e}
	testClientSpan = trace.SpanID{0xc1, 0xc1, 0xc1, 0xc1, 0xc1, 0xc1, 0xc1, 0xc1}
)

func spanContext(span trace.SpanID, remote bool) trace.SpanContext {
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: testTraceID, SpanID: span, TraceFlags: trace.FlagsSampled, Remote: remote,
	})
}

// goroutineSpan sets what the otelc runtime reports as the goroutine's active
// span: through GetSpanFromGLS (current instrumentation) and/or
// GetTraceAndSpanID (older instrumentation). sc.IsValid() == false reports none.
func goroutineSpan(t *testing.T, sc trace.SpanContext, viaSpan, viaIDs bool) {
	t.Helper()

	runtime.RegisterSpanFromGLSFunc(func() trace.Span {
		if !viaSpan || !sc.IsValid() {
			return nil
		}

		return trace.SpanFromContext(trace.ContextWithSpanContext(context.Background(), sc))
	})
	runtime.RegisterTraceAndSpanIDFunc(func() (string, string) {
		if !viaIDs || !sc.IsValid() {
			return "", ""
		}

		return sc.TraceID().String(), sc.SpanID().String()
	})

	t.Cleanup(func() {
		runtime.RegisterSpanFromGLSFunc(func() trace.Span { return nil })
		runtime.RegisterTraceAndSpanIDFunc(func() (string, string) { return "", "" })
	})
}

func useTraceContextPropagator(t *testing.T) {
	t.Helper()

	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
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

		s := tel.span(t, "op")
		assert.Equal(t, codes.Ok, s.Status().Code)
		assert.NotContains(t, spanAttrs(s), attrErrorType)
	})

	t.Run("an error is recorded with its type and message", func(t *testing.T) {
		tel, span := start(t)

		finishSpan(span, errors.New("boom"), "")

		s := tel.span(t, "op")
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, "boom", s.Status().Description)
		assert.Equal(t, "*errors.errorString", spanAttrs(s)[attrErrorType])
		require.Len(t, s.Events(), 1)
		assert.Equal(t, "exception", s.Events()[0].Name)
	})

	t.Run("a semantic error type marks failure without an exception event", func(t *testing.T) {
		tel, span := start(t)

		finishSpan(span, nil, toolErrorType)

		s := tel.span(t, "op")
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, toolErrorType, s.Status().Description)
		assert.Equal(t, toolErrorType, spanAttrs(s)[attrErrorType])
		assert.Empty(t, s.Events())
	})

	t.Run("an error takes precedence over a semantic type", func(t *testing.T) {
		tel, span := start(t)

		finishSpan(span, errors.New("boom"), toolErrorType)

		assert.Equal(t, "*errors.errorString", spanAttrs(tel.span(t, "op"))[attrErrorType])
	})
}

func TestPrintParentSpan(t *testing.T) {
	assert.NotPanics(t, func() { PrintParentSpan(context.Background()) })
	assert.NotPanics(t, func() {
		PrintParentSpan(trace.ContextWithSpanContext(context.Background(), spanContext(testServerSpan, false)))
	})
}

func TestToolHooks(t *testing.T) {
	args := map[string]any{"name": "HDFC"}
	const argsJSON = `{"name":"HDFC"}`

	call := func(t *testing.T, h *harness, tool string) (*mcp.CallToolResult, error) {
		t.Helper()

		return h.client.CallTool(timeout(t), &mcp.CallToolParams{Name: tool, Arguments: args})
	}

	greet := kv(attrGenAIToolName, "greet")

	t.Run("a successful call: tool span, nesting, attributes, context and metrics", func(t *testing.T) {
		var handlerSC trace.SpanContext

		h := newHarness(t, func(s *mcp.Server) {
			addTool(s, "greet", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				handlerSC = trace.SpanContextFromContext(ctx)

				return &mcp.CallToolResult{Content: text("hi")}, nil
			})
		})

		res, err := call(t, h, "greet")
		require.NoError(t, err)
		assert.False(t, res.IsError)

		toolSpan := h.tel.span(t, "tool.greet")
		requestSpan := h.tel.span(t, "tools/call greet")

		assert.Equal(t, trace.SpanKindInternal, toolSpan.SpanKind())
		assert.Equal(t, requestSpan.SpanContext().SpanID(), toolSpan.Parent().SpanID(), "the tool span is a child of the MCP request span")
		assert.Equal(t, requestSpan.SpanContext().TraceID(), toolSpan.SpanContext().TraceID())
		assert.Equal(t, toolSpan.SpanContext(), handlerSC, "the handler runs with the tool span in its context")
		assert.Equal(t, codes.Ok, toolSpan.Status().Code)

		attrs := spanAttrs(toolSpan)
		assert.Equal(t, "tools/call", attrs[attrMCPMethodName])
		assert.Equal(t, "greet", attrs[attrGenAIToolName])
		assert.Equal(t, genAIOperationExecuteTool, attrs[attrGenAIOperationName])
		assert.Equal(t, "greet", attrs[attrToolNameLegacy])
		assert.Equal(t, "jsonrpc", attrs[attrRPCSystemName])
		assert.Equal(t, "tools/call", attrs[attrRPCMethod])
		assert.NotEmpty(t, attrs[attrMCPProtocolVersion])
		assert.Equal(t, "true", attrs[attrToolSuccess])

		assert.Equal(t, int64(1), h.tel.sum(t, "mcp.tool.invocation.total", greet))
		assert.Equal(t, int64(1), h.tel.sum(t, "mcp.tool.invocation.success", greet))
		assert.Zero(t, h.tel.sum(t, "mcp.tool.invocation.failure"))

		_, requestSize := h.tel.hist(t, "mcp.tool.request.message.size", greet)
		assert.Equal(t, float64(len(argsJSON)), requestSize, "the raw arguments as they arrived")

		count, _ := h.tel.hist(t, "mcp.tool.response.message.size", greet)
		assert.Equal(t, uint64(1), count)
	})

	t.Run("a handler that returns an error", func(t *testing.T) {
		h := newHarness(t, func(s *mcp.Server) {
			addTool(s, "greet", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return nil, errors.New("boom")
			})
		})

		res, err := call(t, h, "greet")
		assert.True(t, err != nil || (res != nil && res.IsError), "the client sees a failure")

		toolSpan := h.tel.span(t, "tool.greet")
		assert.Equal(t, codes.Error, toolSpan.Status().Code)
		assert.Equal(t, "false", spanAttrs(toolSpan)[attrToolSuccess])
		assert.Equal(t, "*errors.errorString", spanAttrs(toolSpan)[attrErrorType])
		assert.Equal(t, codes.Error, h.tel.span(t, "tools/call greet").Status().Code)

		assert.Equal(t, int64(1), h.tel.sum(t, "mcp.tool.invocation.failure", greet, kv(attrErrorType, "*errors.errorString")))
		assert.Zero(t, h.tel.sum(t, "mcp.tool.invocation.success"))
		assert.Equal(t, int64(1), h.tel.sum(t, "mcp.server.error", kv(attrMCPMethodName, "tools/call")))
	})

	t.Run("a result with isError is a tool_error failure, not a protocol error", func(t *testing.T) {
		h := newHarness(t, func(s *mcp.Server) {
			addTool(s, "greet", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{IsError: true, Content: text("nope")}, nil
			})
		})

		res, err := call(t, h, "greet")
		require.NoError(t, err)
		assert.True(t, res.IsError)

		toolSpan := h.tel.span(t, "tool.greet")
		assert.Equal(t, codes.Error, toolSpan.Status().Code)
		assert.Equal(t, "false", spanAttrs(toolSpan)[attrToolSuccess])
		assert.Equal(t, toolErrorType, spanAttrs(toolSpan)[attrErrorType])
		assert.Equal(t, toolErrorType, spanAttrs(h.tel.span(t, "tools/call greet"))[attrErrorType])

		assert.Equal(t, int64(1), h.tel.sum(t, "mcp.tool.invocation.failure", greet, kv(attrErrorType, toolErrorType)))
		assert.Equal(t, int64(1), h.tel.sum(t, "mcp.server.error", kv(attrErrorType, toolErrorType)))
	})

	t.Run("an unknown tool never reaches the handler, so only the protocol layer sees it", func(t *testing.T) {
		h := newHarness(t, func(s *mcp.Server) {
			addTool(s, "greet", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
		})

		_, err := call(t, h, "nope")
		require.Error(t, err)

		assert.Empty(t, h.tel.named("tool.nope"))
		assert.Equal(t, codes.Error, h.tel.span(t, "tools/call nope").Status().Code)
		assert.Equal(t, int64(1), h.tel.sum(t, "mcp.server.error", kv(attrMCPMethodName, "tools/call")))
		assert.Zero(t, h.tel.sum(t, "mcp.tool.invocation.total"))
	})

	t.Run("several calls are counted separately", func(t *testing.T) {
		h := newHarness(t, func(s *mcp.Server) {
			addTool(s, "greet", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: text("hi")}, nil
			})
		})

		for i := 0; i < 3; i++ {
			_, err := call(t, h, "greet")
			require.NoError(t, err)
		}

		assert.Len(t, h.tel.named("tool.greet"), 3)
		assert.Equal(t, int64(3), h.tel.sum(t, "mcp.tool.invocation.total", greet))
		assert.Equal(t, int64(3), h.tel.sum(t, "mcp.tool.invocation.success", greet))
	})

	t.Run("two tools keep their own labels", func(t *testing.T) {
		ok := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		}
		h := newHarness(t, func(s *mcp.Server) { addTool(s, "greet", ok); addTool(s, "bye", ok) })

		_, _ = call(t, h, "greet")
		_, _ = call(t, h, "bye")
		_, _ = call(t, h, "bye")

		assert.Equal(t, int64(1), h.tel.sum(t, "mcp.tool.invocation.total", greet))
		assert.Equal(t, int64(2), h.tel.sum(t, "mcp.tool.invocation.total", kv(attrGenAIToolName, "bye")))
	})

	t.Run("disabled after the server was set up: no tool span and no tool metrics", func(t *testing.T) {
		h := newHarness(t, func(s *mcp.Server) {
			addTool(s, "greet", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
		})
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)

		_, err := call(t, h, "greet")
		require.NoError(t, err)

		assert.Empty(t, h.tel.named("tool.greet"))
		assert.Empty(t, h.tel.named("tools/call greet"), "the protocol middleware stands down too")
		assert.Zero(t, h.tel.sum(t, "mcp.tool.invocation.total"))
	})

	t.Run("AfterCallTool without BeforeCallTool does nothing", func(t *testing.T) {
		tel := useTestTelemetry(t)

		require.NotPanics(t, func() { AfterCallTool(hooktest.NewMockHookContext(), &mcp.CallToolResult{}, nil) })
		assert.Empty(t, tel.spans.Ended())
		assert.Zero(t, tel.sum(t, "mcp.tool.invocation.total"))
	})
}

func TestAfterNewServer(t *testing.T) {
	t.Run("installs the protocol middleware", func(t *testing.T) {
		h := newHarness(t, nil)

		_, err := h.client.ListTools(timeout(t), nil)
		require.NoError(t, err)

		assert.Len(t, h.tel.named("tools/list"), 1)
	})

	t.Run("disabled: no middleware is installed", func(t *testing.T) {
		tel := useTestTelemetry(t)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)
		srv := mcp.NewServer(&mcp.Implementation{Name: "s", Version: "v0"}, nil)

		AfterNewServer(hooktest.NewMockHookContext(), srv)

		a, b := mcp.NewInMemoryTransports()
		ss, err := srv.Connect(timeout(t), a, nil)
		require.NoError(t, err)
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil).Connect(timeout(t), b, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })

		_, err = cs.ListTools(timeout(t), nil)
		require.NoError(t, err)

		assert.Empty(t, tel.spans.Ended())
	})
}

func TestProtocolMiddleware_Spans(t *testing.T) {
	t.Run("the connect handshake is spanned and the initialized notification is not", func(t *testing.T) {
		h := newHarness(t, nil)

		// Which method opens the session depends on the SDK version:
		// initialize before the stateless protocol, server/discover after it.
		handshake := append(h.tel.named("initialize"), h.tel.named("server/discover")...)

		require.NotEmpty(t, handshake)

		for _, s := range handshake {
			assert.Equal(t, trace.SpanKindServer, s.SpanKind(), s.Name())
			assert.Equal(t, codes.Ok, s.Status().Code, s.Name())
		}

		assert.Empty(t, h.tel.named("notifications/initialized"), "notifications have no response to measure")
	})

	t.Run("a plain request: attributes, kind and no tool or network attributes", func(t *testing.T) {
		h := newHarness(t, nil)

		_, err := h.client.ListTools(timeout(t), nil)
		require.NoError(t, err)

		s := h.tel.span(t, "tools/list")
		attrs := spanAttrs(s)

		assert.Equal(t, trace.SpanKindServer, s.SpanKind())
		assert.Equal(t, codes.Ok, s.Status().Code)
		assert.Equal(t, "tools/list", attrs[attrMCPMethodName])
		assert.Equal(t, "jsonrpc", attrs[attrRPCSystemName])
		assert.Equal(t, "tools/list", attrs[attrRPCMethod])
		assert.NotEmpty(t, attrs[attrMCPProtocolVersion])
		assert.NotContains(t, attrs, attrGenAIToolName)
		assert.NotContains(t, attrs, attrNetworkTransport, "an in-memory transport is not HTTP")
		assert.NotContains(t, attrs, attrNetworkProtocolName)
	})

	t.Run("tools/call carries the tool name and operation", func(t *testing.T) {
		h := newHarness(t, func(s *mcp.Server) {
			addTool(s, "greet", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: text("hi")}, nil
			})
		})

		_, err := h.client.CallTool(timeout(t), &mcp.CallToolParams{Name: "greet"})
		require.NoError(t, err)

		attrs := spanAttrs(h.tel.span(t, "tools/call greet"))
		assert.Equal(t, "greet", attrs[attrGenAIToolName])
		assert.Equal(t, genAIOperationExecuteTool, attrs[attrGenAIOperationName])
	})

	t.Run("ping", func(t *testing.T) {
		h := newHarness(t, nil)

		require.NoError(t, h.client.Ping(timeout(t), nil))

		assert.Equal(t, codes.Ok, h.tel.span(t, "ping").Status().Code)
	})

	t.Run("resources/read: spans, the read counter, and not-found as an error", func(t *testing.T) {
		h := newHarness(t, func(s *mcp.Server) {
			s.AddResource(&mcp.Resource{URI: "test://doc", Name: "doc"},
				func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
					return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "hello"}}}, nil
				})
		})

		_, err := h.client.ReadResource(timeout(t), &mcp.ReadResourceParams{URI: "test://doc"})
		require.NoError(t, err)
		_, err = h.client.ReadResource(timeout(t), &mcp.ReadResourceParams{URI: "test://missing"})
		require.Error(t, err)

		spans := h.tel.named("resources/read")
		require.Len(t, spans, 2)

		var failed int

		for _, s := range spans {
			if s.Status().Code == codes.Error {
				failed++
			}
		}

		assert.Equal(t, 1, failed)

		assert.Equal(t, int64(2), h.tel.sum(t, "mcp.resource.reads.total"))
		assert.Equal(t, int64(1), h.tel.sum(t, "mcp.server.error", kv(attrMCPMethodName, "resources/read")))
		m, ok := find(h.tel.collect(t), "mcp.resource.reads.total")
		require.True(t, ok)

		var withErrorType int

		for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
			if _, has := dp.Attributes.Value(attrErrorType); has {
				withErrorType++
			}
		}

		assert.Equal(t, 1, withErrorType, "exactly one failed read carries error.type")
	})
}

func TestProtocolMiddleware_Metrics(t *testing.T) {
	h := newHarness(t, func(s *mcp.Server) {
		addTool(s, "greet", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: text("hi")}, nil
		})
	})

	for i := 0; i < 2; i++ {
		_, err := h.client.ListTools(timeout(t), nil)
		require.NoError(t, err)
	}

	_, err := h.client.CallTool(timeout(t), &mcp.CallToolParams{Name: "greet"})
	require.NoError(t, err)

	assert.Equal(t, int64(2), h.tel.sum(t, "mcp.session.request", kv(attrMCPMethodName, "tools/list")))
	assert.Equal(t, int64(1), h.tel.sum(t, "mcp.session.request", kv(attrMCPMethodName, "tools/call")))

	count, _ := h.tel.hist(t, "mcp.server.operation.duration", kv(attrMCPMethodName, "tools/list"))
	assert.Equal(t, uint64(2), count)

	assert.Zero(t, h.tel.sum(t, "mcp.server.error"), "nothing failed")
	assert.Zero(t, h.tel.sum(t, "mcp.session.request", kv(attrMCPMethodName, "notifications/initialized")),
		"notifications are not counted as requests")
}

// ---- the middleware called directly, for what an in-memory transport cannot supply ----

func TestProtocolMiddleware_Direct(t *testing.T) {
	h := newHarness(t, nil)
	listRequest := func(header http.Header) *mcp.ListToolsRequest {
		req := &mcp.ListToolsRequest{Session: h.session, Params: &mcp.ListToolsParams{}}
		if header != nil {
			req.Extra = &mcp.RequestExtra{Header: header}
		}

		return req
	}

	var seen context.Context

	next := func(result mcp.Result, err error) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			seen = ctx

			return result, err
		}
	}

	bridgeHeader := func(sc trace.SpanContext) http.Header {
		header := http.Header{}
		header.Set(httpSpanContextHeader, "00-"+sc.TraceID().String()+"-"+sc.SpanID().String()+"-01")

		return header
	}

	t.Run("a bridged HTTP span becomes the parent, as a local span, and the request is marked HTTP", func(t *testing.T) {
		useTraceContextPropagator(t)

		_, err := serverProtocolMiddleware(next(&mcp.ListToolsResult{}, nil))(
			context.Background(), "tools/list", listRequest(bridgeHeader(spanContext(testServerSpan, false))))
		require.NoError(t, err)

		s := h.tel.span(t, "tools/list")
		assert.Equal(t, testServerSpan, s.Parent().SpanID())
		assert.Equal(t, testTraceID, s.SpanContext().TraceID(), "it joins the bridged span's trace")
		assert.False(t, s.Parent().IsRemote(), "the HTTP server span was created in this process")
		assert.Equal(t, "tcp", spanAttrs(s)[attrNetworkTransport])
		assert.Equal(t, "http", spanAttrs(s)[attrNetworkProtocolName])
		assert.Equal(t, s.SpanContext().SpanID(), trace.SpanContextFromContext(seen).SpanID(), "next runs inside the span")
	})

	t.Run("no bridge header: the span is not parented to anything bridged", func(t *testing.T) {
		useTraceContextPropagator(t)
		h := newHarness(t, nil)
		req := &mcp.ListToolsRequest{Session: h.session, Params: &mcp.ListToolsParams{}, Extra: &mcp.RequestExtra{Header: http.Header{}}}

		_, err := serverProtocolMiddleware(next(&mcp.ListToolsResult{}, nil))(context.Background(), "tools/list", req)
		require.NoError(t, err)

		assert.False(t, h.tel.span(t, "tools/list").Parent().IsValid())
	})

	t.Run("a malformed bridge header is ignored", func(t *testing.T) {
		useTraceContextPropagator(t)
		h := newHarness(t, nil)
		malformed := http.Header{}
		malformed.Set(httpSpanContextHeader, "not-a-traceparent")
		req := &mcp.ListToolsRequest{Session: h.session, Params: &mcp.ListToolsParams{}, Extra: &mcp.RequestExtra{Header: malformed}}

		_, err := serverProtocolMiddleware(next(&mcp.ListToolsResult{}, nil))(context.Background(), "tools/list", req)
		require.NoError(t, err)

		assert.False(t, h.tel.span(t, "tools/list").Parent().IsValid())
	})

	t.Run("initialize has its own events", func(t *testing.T) {
		h := newHarness(t, nil)
		h.tel.spans.Reset()

		_, err := serverProtocolMiddleware(next(nil, nil))(context.Background(), "initialize", listRequest(nil))
		require.NoError(t, err)

		var events []string
		for _, e := range h.tel.span(t, "initialize").Events() {
			events = append(events, e.Name)
		}

		assert.Equal(t, []string{"protocol.validate", "capability.negotiation"}, events)
	})

	t.Run("the handler's result and error pass through unchanged", func(t *testing.T) {
		h := newHarness(t, nil)
		want := &mcp.ListToolsResult{}
		boom := errors.New("boom")

		got, err := serverProtocolMiddleware(next(want, nil))(context.Background(), "tools/list", listRequest(nil))
		require.NoError(t, err)
		assert.Same(t, want, got)

		got, err = serverProtocolMiddleware(next(nil, boom))(context.Background(), "tools/list", listRequest(nil))
		assert.Nil(t, got)
		assert.Same(t, boom, err)

		spans := h.tel.named("tools/list")
		require.Len(t, spans, 2)
		assert.Equal(t, codes.Ok, spans[0].Status().Code)
		assert.Equal(t, codes.Error, spans[1].Status().Code)
	})

	t.Run("an isError tool result is a failure on the span and in the metrics", func(t *testing.T) {
		h := newHarness(t, nil)
		req := &mcp.CallToolRequest{Session: h.session, Params: &mcp.CallToolParamsRaw{Name: "greet"}}

		_, err := serverProtocolMiddleware(next(&mcp.CallToolResult{IsError: true}, nil))(context.Background(), "tools/call", req)
		require.NoError(t, err)

		s := h.tel.span(t, "tools/call greet")
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, toolErrorType, spanAttrs(s)[attrErrorType])
		assert.Equal(t, int64(1), h.tel.sum(t, "mcp.server.error", kv(attrErrorType, toolErrorType)))
	})

	t.Run("notifications pass straight through without a span or a metric", func(t *testing.T) {
		h := newHarness(t, nil)
		called := false

		_, err := serverProtocolMiddleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
			called = true

			return nil, nil
		})(context.Background(), "notifications/progress", listRequest(nil))

		require.NoError(t, err)
		assert.True(t, called)
		assert.Empty(t, h.tel.named("notifications/progress"))
		assert.Zero(t, h.tel.sum(t, "mcp.session.request", kv(attrMCPMethodName, "notifications/progress")))
	})

	t.Run("server/discover has its own event", func(t *testing.T) {
		h := newHarness(t, nil)
		h.tel.spans.Reset() // drop the handshake's own spans (an SDK version may use this method for it)

		_, err := serverProtocolMiddleware(next(nil, nil))(context.Background(), "server/discover", listRequest(nil))
		require.NoError(t, err)

		events := h.tel.span(t, "server/discover").Events()
		require.Len(t, events, 1)
		assert.Equal(t, "capability.enumeration", events[0].Name)
	})

	t.Run("disabled: next is called and nothing is recorded", func(t *testing.T) {
		h := newHarness(t, nil)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)
		called := false

		_, err := serverProtocolMiddleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
			called = true

			return nil, nil
		})(context.Background(), "tools/list", listRequest(nil))

		require.NoError(t, err)
		assert.True(t, called)
		assert.Empty(t, h.tel.named("tools/list"))
	})
}

// ---- the HTTP span bridge ----

func TestBeforeStreamableHTTP(t *testing.T) {
	useTraceContextPropagator(t)

	// bridge runs the hook for a request whose context carries ctxSC.
	bridge := func(t *testing.T, method string, ctxSC trace.SpanContext) (header string, ictx *hooktest.MockHookContext, req *http.Request) {
		t.Helper()

		ctx := context.Background()
		if ctxSC.IsValid() {
			ctx = trace.ContextWithSpanContext(ctx, ctxSC)
		}

		req = httptest.NewRequest(method, "/", nil).WithContext(ctx)
		// Parameter 2 (the request) starts empty so a SetParam from the hook is visible.
		ictx = hooktest.NewMockHookContext(nil, nil, nil)

		BeforeStreamableHTTP(ictx, nil, httptest.NewRecorder(), req)

		return req.Header.Get(httpSpanContextHeader), ictx, req
	}

	t.Run("a POST carries the HTTP server span in a traceparent", func(t *testing.T) {
		goroutineSpan(t, trace.SpanContext{}, false, false)

		header, ictx, req := bridge(t, http.MethodPost, spanContext(testServerSpan, false))

		assert.Equal(t, "00-"+testTraceID.String()+"-"+testServerSpan.String()+"-01", header)
		assert.Same(t, req, ictx.GetParam(2), "the request is handed back to the transport")
	})

	t.Run("a GET is never bridged: it is the long-lived stream", func(t *testing.T) {
		goroutineSpan(t, trace.SpanContext{}, false, false)

		header, _, _ := bridge(t, http.MethodGet, spanContext(testServerSpan, false))

		assert.Empty(t, header)
	})

	t.Run("no span anywhere: nothing is bridged", func(t *testing.T) {
		goroutineSpan(t, trace.SpanContext{}, false, false)

		header, _, _ := bridge(t, http.MethodPost, trace.SpanContext{})

		assert.Empty(t, header)
	})

	t.Run("a client-supplied bridge header is overwritten, not trusted", func(t *testing.T) {
		goroutineSpan(t, trace.SpanContext{}, false, false)
		req := httptest.NewRequest(http.MethodPost, "/", nil).
			WithContext(trace.ContextWithSpanContext(context.Background(), spanContext(testServerSpan, false)))
		req.Header.Set(httpSpanContextHeader, "00-"+testTraceID.String()+"-"+testClientSpan.String()+"-01")

		BeforeStreamableHTTP(hooktest.NewMockHookContext(nil, nil, req), nil, httptest.NewRecorder(), req)

		assert.Contains(t, req.Header.Get(httpSpanContextHeader), testServerSpan.String())
		assert.NotContains(t, req.Header.Get(httpSpanContextHeader), testClientSpan.String())
	})

	t.Run("the context's extracted client span is not bridged when the goroutine's server span is available", func(t *testing.T) {
		goroutineSpan(t, spanContext(testServerSpan, false), true, true)

		header, _, _ := bridge(t, http.MethodPost, spanContext(testClientSpan, true))

		assert.Contains(t, header, testServerSpan.String())
		assert.NotContains(t, header, testClientSpan.String())
	})

	t.Run("a propagator that injects nothing bridges nothing and leaves the request alone", func(t *testing.T) {
		goroutineSpan(t, trace.SpanContext{}, false, false)
		previous := otel.GetTextMapPropagator()
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
		t.Cleanup(func() { otel.SetTextMapPropagator(previous) })

		header, ictx, _ := bridge(t, http.MethodPost, spanContext(testServerSpan, false))

		assert.Empty(t, header)
		assert.Nil(t, ictx.GetParam(2), "the request is not handed back when there is nothing to carry")
	})

	t.Run("disabled: nothing is bridged", func(t *testing.T) {
		goroutineSpan(t, trace.SpanContext{}, false, false)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)

		header, _, _ := bridge(t, http.MethodPost, spanContext(testServerSpan, false))

		assert.Empty(t, header)
	})

	t.Run("a context span and the GLS fallback agree on the header format", func(t *testing.T) {
		goroutineSpan(t, spanContext(testServerSpan, false), false, true)

		header, _, _ := bridge(t, http.MethodPost, spanContext(testClientSpan, true))

		assert.True(t, strings.HasPrefix(header, "00-"+testTraceID.String()+"-"+testServerSpan.String()))
	})
}

func TestHTTPServerSpanContext(t *testing.T) {
	local := spanContext(testServerSpan, false)
	remote := spanContext(testClientSpan, true)
	withSpan := func(sc trace.SpanContext) context.Context {
		return trace.ContextWithSpanContext(context.Background(), sc)
	}

	tests := []struct {
		name         string
		ctx          context.Context
		goroutine    trace.SpanContext
		viaSpan      bool
		viaIDs       bool
		wantSpan     trace.SpanID
		wantNoResult bool
	}{
		{"a local span in the context is the server span", withSpan(local), spanContext(testClientSpan, false), true, true, testServerSpan, false},
		{"a local span in the context wins even without goroutine storage", withSpan(local), trace.SpanContext{}, false, false, testServerSpan, false},
		{"a remote span in the context means the parent was extracted: use the goroutine's span", withSpan(remote), local, true, false, testServerSpan, false},
		{"...through GetTraceAndSpanID when only that is registered", withSpan(remote), local, false, true, testServerSpan, false},
		{"...preferring the span accessor over the IDs", withSpan(remote), local, true, true, testServerSpan, false},
		{"a remote span with no goroutine span is better than nothing", withSpan(remote), trace.SpanContext{}, false, false, testClientSpan, false},
		{"nothing at all", context.Background(), trace.SpanContext{}, false, false, trace.SpanID{}, true},
		{"no context span, goroutine span available", context.Background(), local, true, false, testServerSpan, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goroutineSpan(t, tt.goroutine, tt.viaSpan, tt.viaIDs)

			got := httpServerSpanContext(tt.ctx)

			if tt.wantNoResult {
				assert.False(t, got.IsValid())

				return
			}

			assert.Equal(t, tt.wantSpan, got.SpanID())
			assert.Equal(t, testTraceID, got.TraceID())
		})
	}

	t.Run("malformed goroutine IDs fall back to the context", func(t *testing.T) {
		runtime.RegisterSpanFromGLSFunc(func() trace.Span { return nil })
		runtime.RegisterTraceAndSpanIDFunc(func() (string, string) { return "zz", "zz" })
		t.Cleanup(func() { runtime.RegisterTraceAndSpanIDFunc(func() (string, string) { return "", "" }) })

		assert.Equal(t, testClientSpan, httpServerSpanContext(withSpan(remote)).SpanID())
	})
}
