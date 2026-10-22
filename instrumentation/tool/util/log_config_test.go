package logconfig

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/runtime"

	"github.hdfcbank.com/HDFCBANK/ps-otel/instrumentation/tool/logtest"
)

func TestEnabled(t *testing.T) {
	assert.True(t, Enabled("logs/test"), "enabled by default")

	t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", "logs/test")
	assert.False(t, Enabled("logs/test"))
	assert.True(t, Enabled("logs/other"), "only the named instrumentation is disabled")
}

func TestWithActiveSpan(t *testing.T) {
	clientSC := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: logtest.TraceID, SpanID: logtest.ClientSpan, TraceFlags: trace.FlagsSampled, Remote: true,
	})

	tests := []struct {
		name  string
		apply func(testing.TB)
		ctx   func() context.Context
		want  trace.SpanContext // zero value: no span expected
	}{
		{"goroutine span", logtest.ActiveSpan, context.Background, logtest.SpanContext()},
		{"goroutine IDs only (older instrumentation)", logtest.ActiveSpanIDsOnly, context.Background, logtest.SpanContext()},
		{"nothing active", logtest.NoActiveSpan, context.Background, trace.SpanContext{}},
		{"nil context", logtest.ActiveSpan, func() context.Context { return nil }, logtest.SpanContext()},
		{
			name:  "a span already on ctx wins over the goroutine's",
			apply: logtest.ActiveSpan,
			ctx:   func() context.Context { return trace.ContextWithSpanContext(context.Background(), clientSC) },
			want:  clientSC,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.apply(t)

			got := trace.SpanContextFromContext(WithActiveSpan(tt.ctx()))
			assert.Equal(t, tt.want.TraceID(), got.TraceID())
			assert.Equal(t, tt.want.SpanID(), got.SpanID())
		})
	}

	t.Run("malformed IDs from the runtime leave ctx without a span", func(t *testing.T) {
		runtime.RegisterSpanFromGLSFunc(func() trace.Span { return nil })
		runtime.RegisterTraceAndSpanIDFunc(func() (string, string) { return "not-hex", "also-not" })
		t.Cleanup(func() { runtime.RegisterTraceAndSpanIDFunc(func() (string, string) { return "", "" }) })

		assert.False(t, trace.SpanContextFromContext(WithActiveSpan(context.Background())).IsValid())
	})
}

func TestWithSpanIDs(t *testing.T) {
	tid, sid := logtest.TraceID.String(), logtest.SpanID.String()

	tests := []struct {
		name        string
		trace, span string
		wantOK      bool
	}{
		{"valid", tid, sid, true},
		{"empty trace ID", "", sid, false},
		{"empty span ID", tid, "", false},
		{"malformed trace ID", "zz" + tid[2:], sid, false},
		{"malformed span ID", tid, "zz" + sid[2:], false},
		{"trace ID too short", tid[:30], sid, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, ok := WithSpanIDs(context.Background(), tt.trace, tt.span)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantOK, trace.SpanContextFromContext(ctx).IsValid())

			if tt.wantOK {
				sc := trace.SpanContextFromContext(ctx)
				assert.Equal(t, logtest.TraceID, sc.TraceID())
				assert.Equal(t, logtest.SpanID, sc.SpanID())
				assert.True(t, sc.IsSampled(), "IDs carry no sampling decision, so the context is marked sampled")
				assert.False(t, sc.IsRemote())
			}
		})
	}

	t.Run("all-zero IDs never yield a valid span", func(t *testing.T) {
		ctx, _ := WithSpanIDs(context.Background(), "00000000000000000000000000000000", "0000000000000000")

		assert.False(t, trace.SpanContextFromContext(ctx).IsValid())
	})
}

func TestSpanIDs(t *testing.T) {
	traceID, spanID := SpanIDs(trace.ContextWithSpanContext(context.Background(), logtest.SpanContext()))
	assert.Equal(t, logtest.TraceID.String(), traceID)
	assert.Equal(t, logtest.SpanID.String(), spanID)

	traceID, spanID = SpanIDs(context.Background())
	assert.Empty(t, traceID)
	assert.Empty(t, spanID)
}

func TestAttributeKeys(t *testing.T) {
	// These match the keys otelc's own slog and zap instrumentations use.
	assert.Equal(t, "trace_id", TraceIDKey)
	assert.Equal(t, "span_id", SpanIDKey)
}

func TestBridge(t *testing.T) {
	t.Run("builds once while the provider is unchanged", func(t *testing.T) {
		logtest.Install(t)

		var b Bridge[*int]
		builds := 0
		build := func(log.LoggerProvider) *int { builds++; n := builds; return &n }

		first := b.Get(build)
		assert.Same(t, first, b.Get(build))
		assert.Equal(t, 1, builds)
	})

	t.Run("rebuilds when the global provider is replaced", func(t *testing.T) {
		logtest.Install(t)

		var b Bridge[*int]
		builds := 0
		build := func(log.LoggerProvider) *int { builds++; n := builds; return &n }

		first := b.Get(build)
		logtest.Install(t) // a new provider, as when the runtime installs the real one
		second := b.Get(build)

		assert.NotSame(t, first, second)
		assert.Equal(t, 2, builds)
	})

	t.Run("a provider that cannot be compared only costs a rebuild", func(t *testing.T) {
		previous := otel.GetLoggerProvider()
		otel.SetLoggerProvider(uncomparableProvider{tags: []string{"x"}})
		t.Cleanup(func() { otel.SetLoggerProvider(previous) })

		var b Bridge[int]
		builds := 0
		build := func(log.LoggerProvider) int { builds++; return builds }

		require.NotPanics(t, func() { b.Get(build); b.Get(build) })
		assert.Equal(t, 2, builds)
	})

	t.Run("is safe for concurrent use", func(t *testing.T) {
		logtest.Install(t)

		var (
			b  Bridge[int]
			wg sync.WaitGroup
		)

		for i := 0; i < 50; i++ {
			wg.Add(1)

			go func() {
				defer wg.Done()
				b.Get(func(log.LoggerProvider) int { return 7 })
			}()
		}

		wg.Wait()
		assert.Equal(t, 7, b.Get(func(log.LoggerProvider) int { return 0 }))
	})
}

type uncomparableProvider struct {
	embedded.LoggerProvider
	tags []string // makes the type non-comparable
}

func (uncomparableProvider) Logger(string, ...log.LoggerOption) log.Logger { return nil }
