// Package logconfig holds what the log-bridge hook packages under
// instrumentation for logs share: trace-context recovery and a per-provider cache for
// the bridge objects.
//
// Hooks on the standard library can run while other packages are still
// initialising, so nothing here relies on package-level initialisers; every
// type is usable as its zero value.
package logconfig

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/runtime"
)

// Attribute keys under which the trace and span IDs are added to exported
// records. They match the keys otelc's own slog and zap instrumentations use,
// so a record that already carries them from those is left alone.
const (
	TraceIDKey = "trace_id"
	SpanIDKey  = "span_id"
)

// Enabled reports whether the instrumentation named key is switched on. Use
// OTEL_GO_DISABLED_INSTRUMENTATIONS (or OTEL_GO_ENABLED_INSTRUMENTATIONS) to
// control it; names are compared case-insensitively.
func Enabled(key string) bool {
	return runtime.Instrumented(key)
}

// WithActiveSpan returns ctx carrying the span that is active on the calling
// goroutine, unless ctx already carries a valid span.
//
// Logging calls such as slog.Info take no context, so without this the bridge
// could not attach trace_id and span_id to the record. The span comes from
// the goroutine-local storage that otelc's sdk/trace instrumentation
// maintains.
func WithActiveSpan(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	if trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}

	// Preferred: the live span itself, which keeps its trace flags.
	if span := runtime.GetSpanFromGLS(); span != nil && span.SpanContext().IsValid() {
		return trace.ContextWithSpan(ctx, span)
	}

	// Fallback: rebuild the span context from the IDs. The instrumentation
	// modules are versioned separately from the runtime, and an older one
	// registers GetTraceAndSpanID but not GetSpanFromGLS, in which case the
	// call above always returns nil.
	return withSpanIDs(ctx)
}

// SpanIDs returns the hex trace and span IDs of the span carried by ctx, or
// two empty strings when ctx has no valid span.
func SpanIDs(ctx context.Context) (traceID, spanID string) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}

	return sc.TraceID().String(), sc.SpanID().String()
}

// withSpanIDs adds the goroutine's active trace and span IDs to ctx. The IDs
// carry no sampling decision, so WithSpanIDs marks the context sampled: a span
// that is active on the goroutine was started by the SDK.
func withSpanIDs(ctx context.Context) context.Context {
	traceHex, spanHex := runtime.GetTraceAndSpanID()
	ctx, _ = WithSpanIDs(ctx, traceHex, spanHex)

	return ctx
}

// WithSpanIDs returns ctx carrying a span context built from hex trace and
// span IDs, for callers that only have the IDs, such as a log line that
// already holds them as fields. It returns ctx and false when either ID is
// empty or malformed. The context is marked sampled, since the IDs carry no
// sampling decision.
func WithSpanIDs(ctx context.Context, traceHex, spanHex string) (context.Context, bool) {
	if traceHex == "" || spanHex == "" {
		return ctx, false
	}

	traceID, err := trace.TraceIDFromHex(traceHex)
	if err != nil {
		return ctx, false
	}

	spanID, err := trace.SpanIDFromHex(spanHex)
	if err != nil {
		return ctx, false
	}

	return trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})), true
}

// Bridge lazily builds one bridge value (an slog handler, a zap core, ...)
// and rebuilds it if the global LoggerProvider is replaced.
//
// The runtime installs its provider during start-up. A log call made before
// that sees the placeholder provider, and a bridge built against it would
// drop records forever, so the cache is keyed on the provider it was built
// for. The zero value is ready to use.
type Bridge[T any] struct {
	state atomic.Pointer[bridgeState[T]]
}

type bridgeState[T any] struct {
	provider log.LoggerProvider
	value    T
}

// Get returns the cached value, calling build when there is none yet or the
// global LoggerProvider has changed since it was built. Concurrent callers
// may each build once; the results are equivalent and the last one wins.
func (b *Bridge[T]) Get(build func(log.LoggerProvider) T) T {
	provider := otel.GetLoggerProvider()

	if s := b.state.Load(); s != nil && sameProvider(s.provider, provider) {
		return s.value
	}

	s := &bridgeState[T]{provider: provider, value: build(provider)}
	b.state.Store(s)

	return s.value
}

// sameProvider compares two providers, treating a provider type that cannot
// be compared as "different" (which only costs a rebuild).
func sameProvider(a, b log.LoggerProvider) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()

	return a == b
}
