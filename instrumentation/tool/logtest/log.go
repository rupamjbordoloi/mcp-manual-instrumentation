package logtest

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/runtime"
)

// The span the helpers report as active.
var (
	TraceID    = trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	SpanID     = trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	ClientSpan = trace.SpanID{0xc1, 0xc1, 0xc1, 0xc1, 0xc1, 0xc1, 0xc1, 0xc1}
)

// SpanContext returns the sampled, local span context the helpers use.
func SpanContext() trace.SpanContext {
	return trace.NewSpanContext(trace.SpanContextConfig{TraceID: TraceID, SpanID: SpanID, TraceFlags: trace.FlagsSampled})
}

// Emitted is one record that reached the provider, with the context it was
// emitted under.
type Emitted struct {
	Ctx    context.Context
	Record log.Record
}

func (e Emitted) Body() string                   { return e.Record.Body().AsString() }
func (e Emitted) SpanContext() trace.SpanContext { return trace.SpanContextFromContext(e.Ctx) }

// Attrs returns the record's attributes as key -> every value seen, so a
// duplicated attribute is visible.
func (e Emitted) Attrs() map[string][]string {
	out := map[string][]string{}

	e.Record.WalkAttributes(func(kv attribute.KeyValue) bool {
		out[string(kv.Key)] = append(out[string(kv.Key)], kv.Value.Emit())

		return true
	})

	return out
}

// Recorder is a LoggerProvider that keeps every record emitted to it.
type Recorder struct {
	mu      sync.Mutex
	got     []Emitted
	enabled bool
}

// Install makes a new Recorder the global LoggerProvider until the test ends.
func Install(t testing.TB) *Recorder {
	t.Helper()

	r := &Recorder{enabled: true}
	previous := otel.GetLoggerProvider()

	otel.SetLoggerProvider(&provider{r: r})
	t.Cleanup(func() { otel.SetLoggerProvider(previous) })

	return r
}

// Records returns what has been emitted so far.
func (r *Recorder) Records() []Emitted {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]Emitted(nil), r.got...)
}

// SetEnabled controls what the Logger reports from Enabled, as an SDK does
// when a processor filters records out.
func (r *Recorder) SetEnabled(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.enabled = v
}

type provider struct {
	embedded.LoggerProvider
	r *Recorder
}

func (p *provider) Logger(string, ...log.LoggerOption) log.Logger { return &logger{r: p.r} }

type logger struct {
	embedded.Logger
	r *Recorder
}

func (l *logger) Emit(ctx context.Context, rec log.Record) {
	l.r.mu.Lock()
	defer l.r.mu.Unlock()

	l.r.got = append(l.r.got, Emitted{Ctx: ctx, Record: rec.Clone()})
}

func (l *logger) Enabled(context.Context, log.EnabledParameters) bool {
	l.r.mu.Lock()
	defer l.r.mu.Unlock()

	return l.r.enabled
}

// ---- the goroutine's active span, as the otelc runtime reports it ----

// Scenario is one way the runtime can report (or not report) an active span.
type Scenario struct {
	Name      string
	Apply     func(testing.TB)
	WantTrace bool
}

// Scenarios covers a current instrumentation module, an older one that
// registers only GetTraceAndSpanID, and no active span.
var Scenarios = []Scenario{
	{"goroutine span (GetSpanFromGLS)", ActiveSpan, true},
	{"goroutine IDs only (older instrumentation)", ActiveSpanIDsOnly, true},
	{"no active span", NoActiveSpan, false},
}

// ActiveSpan reports SpanContext() as active through both accessors.
func ActiveSpan(t testing.TB) {
	t.Helper()
	register(t, SpanContext(), true, true)
}

// ActiveSpanIDsOnly reports it through GetTraceAndSpanID only, as an older
// instrumentation module does.
func ActiveSpanIDsOnly(t testing.TB) {
	t.Helper()
	register(t, SpanContext(), false, true)
}

// NoActiveSpan reports nothing through either accessor.
func NoActiveSpan(t testing.TB) {
	t.Helper()
	register(t, trace.SpanContext{}, false, false)
}

func register(t testing.TB, sc trace.SpanContext, span, ids bool) {
	t.Helper()

	runtime.RegisterSpanFromGLSFunc(func() trace.Span {
		if !span {
			return nil
		}

		return trace.SpanFromContext(trace.ContextWithSpanContext(context.Background(), sc))
	})
	runtime.RegisterTraceAndSpanIDFunc(func() (string, string) {
		if !ids {
			return "", ""
		}

		return sc.TraceID().String(), sc.SpanID().String()
	})

	t.Cleanup(func() {
		runtime.RegisterSpanFromGLSFunc(func() trace.Span { return nil })
		runtime.RegisterTraceAndSpanIDFunc(func() (string, string) { return "", "" })
	})
}

// AssertTrace checks the record's trace correlation: want=true needs the
// span on the emitted context and exactly one trace_id and one span_id
// attribute carrying its IDs; want=false needs none of them.
func AssertTrace(t testing.TB, e Emitted, want bool) {
	t.Helper()

	got := e.SpanContext()
	a := e.Attrs()

	if !want {
		if got.IsValid() || len(a["trace_id"]) != 0 || len(a["span_id"]) != 0 {
			t.Errorf("no span is active but the record carries one: native=%v attrs=%v", got, a)
		}

		return
	}

	if got.TraceID() != TraceID || got.SpanID() != SpanID {
		t.Errorf("native trace fields = %v/%v, want %v/%v", got.TraceID(), got.SpanID(), TraceID, SpanID)
	}

	if v := a["trace_id"]; len(v) != 1 || v[0] != TraceID.String() {
		t.Errorf("trace_id attribute = %v, want exactly [%s]", v, TraceID)
	}

	if v := a["span_id"]; len(v) != 1 || v[0] != SpanID.String() {
		t.Errorf("span_id attribute = %v, want exactly [%s]", v, SpanID)
	}
}

// AssertNativeTrace is AssertTrace for hooks that set the record's native
// trace fields but add no trace_id and span_id attributes.
func AssertNativeTrace(t testing.TB, e Emitted, want bool) {
	t.Helper()

	got := e.SpanContext()
	if !want {
		if got.IsValid() {
			t.Errorf("no span is active but the record carries %v", got)
		}

		return
	}

	if got.TraceID() != TraceID || got.SpanID() != SpanID {
		t.Errorf("native trace fields = %v/%v, want %v/%v", got.TraceID(), got.SpanID(), TraceID, SpanID)
	}
}
