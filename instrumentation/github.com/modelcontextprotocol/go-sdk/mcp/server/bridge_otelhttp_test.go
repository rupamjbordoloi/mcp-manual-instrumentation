package server

// These tests need go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp
// as a test dependency. They run the real handler that http_metrics_wrap puts
// around the MCP handler (see mcp.otelc.yaml), because what it does to the
// request context is exactly what the HTTP span bridge has to cope with.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.opentelemetry.io/otelc/pkg/hook/hooktest"
)

const clientSpanHex = "c1c1c1c1c1c1c1c1"

// throughOtelhttp sends a request into BeforeStreamableHTTP, optionally behind
// a real otelhttp handler as http_metrics_wrap builds it.
//
// A recording SDK span stands in for the HTTP server span that otelc's net/http
// instrumentation puts in the request context, and the request carries the
// client's traceparent. With goroutine set, the otelc runtime also reports
// that span as the goroutine's active span, as it does when the sdk/trace
// instrumentation is applied.
//
// It returns the server span's ID and the span ID found in the bridge header.
func throughOtelhttp(t *testing.T, wrap, goroutine bool, opts ...otelhttp.Option) (serverSpan, bridged string) {
	t.Helper()
	useTraceContextPropagator(t)

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("otelc").Start(context.Background(), "POST", trace.WithSpanKind(trace.SpanKindServer))
	t.Cleanup(func() { span.End() })

	if goroutine {
		goroutineSpan(t, span.SpanContext(), true, true)
	} else {
		goroutineSpan(t, trace.SpanContext{}, false, false)
	}

	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		BeforeStreamableHTTP(hooktest.NewMockHookContext(nil, nil, r), nil, w, r)
		bridged = r.Header.Get(httpSpanContextHeader)
	})

	if wrap {
		// http_metrics_wrap: no spans of its own, metrics only.
		all := append([]otelhttp.Option{otelhttp.WithTracerProvider(noop.NewTracerProvider())}, opts...)
		handler = otelhttp.NewHandler(handler, "mcp", all...)
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	req.Header.Set("traceparent", "00-"+span.SpanContext().TraceID().String()+"-"+clientSpanHex+"-01")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	return span.SpanContext().SpanID().String(), bridged
}

// noPropagators is the option mcp.otelc.yaml gives the wrapper: it stops
// otelhttp extracting the incoming traceparent over the server span.
func noPropagators() otelhttp.Option {
	return otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator())
}

func TestBridgeBehindOtelhttp(t *testing.T) {
	t.Run("control: with otelhttp's default propagators the context holds the client's span, and that is what gets bridged", func(t *testing.T) {
		server, bridged := throughOtelhttp(t, true, false)

		assert.Contains(t, bridged, clientSpanHex, "this is the failure mcp.otelc.yaml's empty propagator prevents")
		assert.NotContains(t, bridged, server)
	})

	tests := []struct {
		name      string
		wrap      bool
		goroutine bool
		opts      []otelhttp.Option
	}{
		{"no wrapper, no goroutine span", false, false, nil},
		{"no wrapper, goroutine span", false, true, nil},
		{"the rule's empty propagator, no goroutine span", true, false, []otelhttp.Option{noPropagators()}},
		{"the rule's empty propagator, goroutine span", true, true, []otelhttp.Option{noPropagators()}},
		{"an older rule with default propagators is rescued by the goroutine span", true, true, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, bridged := throughOtelhttp(t, tt.wrap, tt.goroutine, tt.opts...)

			assert.Contains(t, bridged, server, "the HTTP server span is bridged")
			assert.NotContains(t, bridged, clientSpanHex, "not the client's span")
		})
	}
}
