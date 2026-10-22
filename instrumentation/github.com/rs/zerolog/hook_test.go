package zerolog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook/hooktest"

	"github.hdfcbank.com/HDFCBANK/ps-otel/instrumentation/tool/logtest"
	logconfig "github.hdfcbank.com/HDFCBANK/ps-otel/instrumentation/tool/util"
)

// appLogger does what the injected code does: BeforeNew rewrites New's
// writer parameter, and New then runs with it.
func appLogger(w io.Writer) *zerolog.Logger {
	ictx := hooktest.NewMockHookContext(w)
	BeforeNew(ictx, w)

	l := zerolog.New(ictx.GetParam(0).(io.Writer))

	return &l
}

func ptr(l zerolog.Logger) *zerolog.Logger { return &l }

// logMsg runs BeforeEventMsg and then Msg, as the injected code does.
func logMsg(ev *zerolog.Event, msg string) {
	BeforeEventMsg(hooktest.NewMockHookContext(), ev, msg)
	ev.Msg(msg)
}

func appLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), buf.String())

	return line
}

func setTimeFormat(t *testing.T, format string) {
	t.Helper()

	previous := zerolog.TimeFieldFormat
	zerolog.TimeFieldFormat = format
	t.Cleanup(func() { zerolog.TimeFieldFormat = previous })
}

func TestBeforeNew(t *testing.T) {
	t.Run("puts the tee in front of the writer", func(t *testing.T) {
		var buf bytes.Buffer
		ictx := hooktest.NewMockHookContext(&buf)

		BeforeNew(ictx, &buf)

		w, ok := ictx.GetParam(0).(*exportWriter)
		require.True(t, ok)
		assert.Same(t, &buf, w.inner)
	})

	t.Run("leaves a nil writer for zerolog to handle", func(t *testing.T) {
		ictx := hooktest.NewMockHookContext(nil)

		BeforeNew(ictx, nil)

		assert.Nil(t, ictx.GetParam(0))
	})

	t.Run("does not wrap a writer twice", func(t *testing.T) {
		wrapped := &exportWriter{inner: io.Discard}
		ictx := hooktest.NewMockHookContext(wrapped)

		BeforeNew(ictx, wrapped)

		assert.Same(t, wrapped, ictx.GetParam(0))
	})

	t.Run("does nothing when the export is disabled", func(t *testing.T) {
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)
		var buf bytes.Buffer
		ictx := hooktest.NewMockHookContext(&buf)

		BeforeNew(ictx, &buf)

		assert.Same(t, &buf, ictx.GetParam(0))
	})
}

type levelRecorder struct {
	levels []zerolog.Level
	plain  int
}

func (l *levelRecorder) Write(p []byte) (int, error) { l.plain++; return len(p), nil }
func (l *levelRecorder) WriteLevel(level zerolog.Level, p []byte) (int, error) {
	l.levels = append(l.levels, level)

	return len(p), nil
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 3, errors.New("disk full") }

func TestExportWriter(t *testing.T) {
	const jsonLine = `{"level":"warn","message":"m"}` + "\n"

	t.Run("forwards the level to a LevelWriter", func(t *testing.T) {
		rec := logtest.Install(t)
		inner := &levelRecorder{}
		w := &exportWriter{inner: inner}

		n, err := w.WriteLevel(zerolog.WarnLevel, []byte(jsonLine))

		require.NoError(t, err)
		assert.Equal(t, len(jsonLine), n)
		assert.Equal(t, []zerolog.Level{zerolog.WarnLevel}, inner.levels)
		assert.Zero(t, inner.plain)
		require.Len(t, rec.Records(), 1)
		assert.Equal(t, log.SeverityWarn, rec.Records()[0].Record.Severity())
	})

	t.Run("falls back to Write for a plain writer", func(t *testing.T) {
		rec := logtest.Install(t)
		var buf bytes.Buffer

		_, err := (&exportWriter{inner: &buf}).WriteLevel(zerolog.InfoLevel, []byte(jsonLine))

		require.NoError(t, err)
		assert.Equal(t, jsonLine, buf.String())
		assert.Len(t, rec.Records(), 1)
	})

	t.Run("Write has no level, so the record is INFO", func(t *testing.T) {
		rec := logtest.Install(t)

		_, err := (&exportWriter{inner: io.Discard}).Write([]byte(`{"message":"m"}`))

		require.NoError(t, err)
		require.Len(t, rec.Records(), 1)
		assert.Equal(t, log.SeverityInfo, rec.Records()[0].Record.Severity())
		assert.Equal(t, "INFO", rec.Records()[0].Record.SeverityText())
	})

	t.Run("returns the wrapped writer's result and still exports", func(t *testing.T) {
		rec := logtest.Install(t)

		n, err := (&exportWriter{inner: failingWriter{}}).Write([]byte(`{"message":"m"}`))

		assert.Equal(t, 3, n)
		assert.EqualError(t, err, "disk full")
		assert.Len(t, rec.Records(), 1)
	})

	t.Run("passes output that is not JSON through without exporting it", func(t *testing.T) {
		rec := logtest.Install(t)
		var buf bytes.Buffer

		_, err := (&exportWriter{inner: &buf}).Write([]byte("not json\n"))

		require.NoError(t, err)
		assert.Equal(t, "not json\n", buf.String())
		assert.Empty(t, rec.Records())
	})
}

func TestEvents(t *testing.T) {
	for _, sc := range logtest.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			rec := logtest.Install(t)
			sc.Apply(t)
			var buf bytes.Buffer
			l := appLogger(&buf)

			logMsg(l.Info().Str("k", "v"), "hello")

			got := rec.Records()
			require.Len(t, got, 1)
			assert.Equal(t, "hello", got[0].Body())
			assert.Equal(t, log.SeverityInfo, got[0].Record.Severity())
			assert.Equal(t, []string{"v"}, got[0].Attrs()["k"])
			logtest.AssertTrace(t, got[0], sc.WantTrace)

			for _, k := range []string{"message", "level", "time"} {
				assert.NotContains(t, got[0].Attrs(), k, "%q becomes the body, severity or timestamp, not an attribute", k)
			}

			line := appLine(t, &buf)
			assert.Equal(t, "hello", line["message"])
			assert.Equal(t, "v", line["k"])
		})
	}

	t.Run("the trace fields are added to the application's own output", func(t *testing.T) {
		logtest.Install(t)
		logtest.ActiveSpan(t)
		var buf bytes.Buffer

		l := appLogger(&buf)
		logMsg(l.Info(), "hello")

		line := appLine(t, &buf)
		assert.Equal(t, logtest.TraceID.String(), line["trace_id"])
		assert.Equal(t, logtest.SpanID.String(), line["span_id"])
	})

	t.Run("no span, no trace fields", func(t *testing.T) {
		logtest.Install(t)
		logtest.NoActiveSpan(t)
		var buf bytes.Buffer

		l := appLogger(&buf)
		logMsg(l.Info(), "hello")

		line := appLine(t, &buf)
		assert.NotContains(t, line, "trace_id")
		assert.NotContains(t, line, "span_id")
	})

	t.Run("a context given with Ctx supplies the span", func(t *testing.T) {
		rec := logtest.Install(t)
		logtest.NoActiveSpan(t)
		var buf bytes.Buffer
		ctx := trace.ContextWithSpanContext(context.Background(), logtest.SpanContext())

		l := appLogger(&buf)
		logMsg(l.Info().Ctx(ctx), "hello")

		assert.Equal(t, logtest.TraceID.String(), appLine(t, &buf)["trace_id"])
		require.Len(t, rec.Records(), 1)
		logtest.AssertTrace(t, rec.Records()[0], true)
	})

	t.Run("fields added with With are exported", func(t *testing.T) {
		rec := logtest.Install(t)
		logtest.ActiveSpan(t)

		l := ptr(appLogger(io.Discard).With().Str("svc", "billing").Logger())
		logMsg(l.Info(), "hello")

		require.Len(t, rec.Records(), 1)
		assert.Equal(t, []string{"billing"}, rec.Records()[0].Attrs()["svc"])
	})

	t.Run("severity follows the level", func(t *testing.T) {
		tests := map[zerolog.Level]log.Severity{
			zerolog.TraceLevel: log.SeverityTrace,
			zerolog.DebugLevel: log.SeverityDebug,
			zerolog.InfoLevel:  log.SeverityInfo,
			zerolog.WarnLevel:  log.SeverityWarn,
			zerolog.ErrorLevel: log.SeverityError,
		}

		for level, want := range tests {
			rec := logtest.Install(t)
			l := ptr(appLogger(io.Discard).Level(zerolog.TraceLevel))

			logMsg(l.WithLevel(level), "m")

			require.Len(t, rec.Records(), 1, level.String())
			assert.Equal(t, want, rec.Records()[0].Record.Severity(), level.String())
		}
	})

	t.Run("an event with no level is INFO", func(t *testing.T) {
		rec := logtest.Install(t)

		logMsg(appLogger(io.Discard).Log(), "m")

		require.Len(t, rec.Records(), 1)
		assert.Equal(t, log.SeverityInfo, rec.Records()[0].Record.Severity())
		assert.Equal(t, "INFO", rec.Records()[0].Record.SeverityText())
	})

	t.Run("an event the application's own level drops is not exported", func(t *testing.T) {
		rec := logtest.Install(t)
		l := ptr(appLogger(io.Discard).Level(zerolog.WarnLevel))

		require.NotPanics(t, func() { logMsg(l.Info(), "dropped") }, "the event is nil; both the hook and Msg must cope")
		assert.Empty(t, rec.Records())
	})

	t.Run("BeforeEventMsg ignores a nil event", func(t *testing.T) {
		require.NotPanics(t, func() { BeforeEventMsg(hooktest.NewMockHookContext(), nil, "m") })
	})

	t.Run("field types are kept", func(t *testing.T) {
		rec := logtest.Install(t)

		logMsg(appLogger(io.Discard).Info().
			Int("n", 5).Float64("f", 1.5).Bool("b", true).
			Dict("d", zerolog.Dict().Str("a", "b")).Err(io.EOF), "m")

		require.Len(t, rec.Records(), 1)
		attrs := rec.Records()[0].Attrs()
		assert.Equal(t, []string{"5"}, attrs["n"])
		assert.Equal(t, []string{"1.5"}, attrs["f"])
		assert.Equal(t, []string{"true"}, attrs["b"])
		assert.Equal(t, []string{`{"a":"b"}`}, attrs["d"], "nested objects are kept as JSON text")
		assert.Equal(t, []string{"EOF"}, attrs["error"])
	})

	t.Run("an event with no message has an empty body", func(t *testing.T) {
		rec := logtest.Install(t)
		ev := appLogger(io.Discard).Info().Str("k", "v")

		BeforeEventMsg(hooktest.NewMockHookContext(), ev, "")
		ev.Send()

		require.Len(t, rec.Records(), 1)
		assert.Empty(t, rec.Records()[0].Body())
		assert.Equal(t, []string{"v"}, rec.Records()[0].Attrs()["k"])
	})

	t.Run("the timestamp is the line's time field", func(t *testing.T) {
		rec := logtest.Install(t)
		fixed := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
		previous := zerolog.TimestampFunc
		zerolog.TimestampFunc = func() time.Time { return fixed }
		t.Cleanup(func() { zerolog.TimestampFunc = previous })

		logMsg(ptr(appLogger(io.Discard).With().Timestamp().Logger()).Info(), "m")

		require.Len(t, rec.Records(), 1)
		assert.True(t, fixed.Equal(rec.Records()[0].Record.Timestamp()), rec.Records()[0].Record.Timestamp())
	})

	t.Run("a multi-destination writer is exported once", func(t *testing.T) {
		rec := logtest.Install(t)
		var a, b bytes.Buffer

		logMsg(appLogger(zerolog.MultiLevelWriter(&a, &b)).Info(), "hello")

		assert.Len(t, rec.Records(), 1)
		assert.NotZero(t, a.Len())
		assert.NotZero(t, b.Len())
	})

	t.Run("an event that already has trace_id: one attribute is exported, the application's own line holds it twice", func(t *testing.T) {
		rec := logtest.Install(t)
		logtest.ActiveSpan(t)
		var buf bytes.Buffer

		logMsg(appLogger(&buf).Info().
			Str("trace_id", logtest.TraceID.String()).Str("span_id", logtest.SpanID.String()), "m")

		require.Len(t, rec.Records(), 1)
		attrs := rec.Records()[0].Attrs()
		assert.Len(t, attrs["trace_id"], 1, "decoding the line collapses the duplicate key")
		assert.Len(t, attrs["span_id"], 1)
		assert.Equal(t, 2, strings.Count(buf.String(), `"trace_id"`),
			"BeforeEventMsg cannot see fields already on the event, so it adds its own; do not add them yourself")
	})
}

func TestSwitches(t *testing.T) {
	t.Run("disabled after the logger was built: nothing is exported and no trace fields are added", func(t *testing.T) {
		rec := logtest.Install(t)
		logtest.ActiveSpan(t)
		var buf bytes.Buffer
		l := appLogger(&buf) // built while enabled, so the tee is in place

		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)
		logMsg(l.Info(), "hello")

		assert.Empty(t, rec.Records(), "the export is checked at write time too")

		line := appLine(t, &buf)
		assert.Equal(t, "hello", line["message"], "the application's own output still works")
		assert.NotContains(t, line, "trace_id", "BeforeEventMsg shares the same switch")
	})

	t.Run("disabled before the logger was built: the writer is not wrapped", func(t *testing.T) {
		rec := logtest.Install(t)
		logtest.ActiveSpan(t)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)
		var buf bytes.Buffer

		logMsg(appLogger(&buf).Info(), "hello")

		assert.Empty(t, rec.Records())
		assert.NotContains(t, appLine(t, &buf), "trace_id")
	})

	t.Run("OpenTelemetry filtering the record out", func(t *testing.T) {
		rec := logtest.Install(t)
		rec.SetEnabled(false)

		logMsg(appLogger(io.Discard).Info(), "hello")

		assert.Empty(t, rec.Records())
	})
}

func TestSeverityOf(t *testing.T) {
	tests := map[zerolog.Level]log.Severity{
		zerolog.TraceLevel: log.SeverityTrace,
		zerolog.DebugLevel: log.SeverityDebug,
		zerolog.InfoLevel:  log.SeverityInfo,
		zerolog.WarnLevel:  log.SeverityWarn,
		zerolog.ErrorLevel: log.SeverityError,
		zerolog.FatalLevel: log.SeverityFatal,
		zerolog.PanicLevel: log.SeverityFatal,
		zerolog.NoLevel:    log.SeverityInfo,
	}

	for level, want := range tests {
		assert.Equal(t, want, severityOf(level), level.String())
	}
}

func TestSeverityText(t *testing.T) {
	assert.Equal(t, "WARN", severityText(zerolog.WarnLevel))
	assert.Equal(t, "ERROR", severityText(zerolog.ErrorLevel))
	assert.Equal(t, "INFO", severityText(zerolog.NoLevel), "NoLevel has no text of its own")
}

func TestTimestampOf(t *testing.T) {
	want := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		format string
		value  any
		want   time.Time // zero: expect "now"
	}{
		{"RFC3339 string", time.RFC3339, want.Format(time.RFC3339), want},
		{"RFC3339Nano string with a different configured layout", time.Kitchen, want.Format(time.RFC3339Nano), want},
		{"unix seconds", zerolog.TimeFormatUnix, json.Number("1792058400"), want},
		{"unix milliseconds", zerolog.TimeFormatUnixMs, json.Number("1792058400000"), want},
		{"unix microseconds", zerolog.TimeFormatUnixMicro, json.Number("1792058400000000"), want},
		{"unix nanoseconds", zerolog.TimeFormatUnixNano, json.Number("1792058400000000000"), want},
		{"an unparseable string falls back to now", time.RFC3339, "yesterday-ish", time.Time{}},
		{"a number in a layout that is not numeric falls back to now", time.RFC3339, json.Number("5"), time.Time{}},
		{"a fractional number falls back to now", zerolog.TimeFormatUnix, json.Number("1.5"), time.Time{}},
		{"missing", time.RFC3339, nil, time.Time{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTimeFormat(t, tt.format)

			fields := map[string]any{}
			if tt.value != nil {
				fields[zerolog.TimestampFieldName] = tt.value
			}

			before := time.Now()
			got := timestampOf(fields)

			if tt.want.IsZero() {
				assert.WithinDuration(t, before, got, 5*time.Second)
			} else {
				assert.True(t, tt.want.Equal(got), got)
			}
		})
	}
}

func TestAttributeOf(t *testing.T) {
	tests := []struct {
		name   string
		in     any
		want   string
		wantOK bool
	}{
		{"string", "v", "v", true},
		{"bool", true, "true", true},
		{"integer", json.Number("42"), "42", true},
		{"float", json.Number("1.5"), "1.5", true},
		{"integer too large for int64 is kept as a float", json.Number("1e30"), "1e+30", true},
		{"nil is dropped", nil, "", false},
		{"array is kept as JSON text", []any{"a", "b"}, `["a","b"]`, true},
		{"object is kept as JSON text", map[string]any{"a": "b"}, `{"a":"b"}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kv, ok := attributeOf("key", tt.in)

			require.Equal(t, tt.wantOK, ok)

			if ok {
				assert.Equal(t, "key", string(kv.Key))
				assert.Equal(t, tt.want, kv.Value.Emit())
			}
		})
	}
}

func TestAttributesOf(t *testing.T) {
	fields := map[string]any{
		zerolog.MessageFieldName:   "m",
		zerolog.LevelFieldName:     "info",
		zerolog.TimestampFieldName: "t",
		"b":                        "2",
		"a":                        "1",
		"gone":                     nil,
	}

	got := attributesOf(fields)

	require.Len(t, got, 2)
	assert.Equal(t, "a", string(got[0].Key), "keys are sorted so output is deterministic")
	assert.Equal(t, "b", string(got[1].Key))
}

func TestDecode(t *testing.T) {
	t.Run("a JSON line with a trailing newline", func(t *testing.T) {
		fields, ok := decode([]byte(`{"n":12345678901234567890,"s":"x"}` + "\n"))

		require.True(t, ok)
		assert.Equal(t, json.Number("12345678901234567890"), fields["n"], "numbers are not squeezed through float64")
		assert.Equal(t, "x", fields["s"])
	})

	for name, in := range map[string]string{"not JSON": "oops", "empty": "", "an array": `["a"]`, "truncated": `{"a":`} {
		t.Run(name, func(t *testing.T) {
			_, ok := decode([]byte(in))
			assert.False(t, ok)
		})
	}
}

func TestRecordContext(t *testing.T) {
	t.Run("the line's own IDs win over the goroutine's", func(t *testing.T) {
		logtest.ActiveSpan(t)

		ctx := recordContext(map[string]any{
			logconfig.TraceIDKey: logtest.TraceID.String(),
			logconfig.SpanIDKey:  logtest.ClientSpan.String(),
		})

		assert.Equal(t, logtest.ClientSpan, trace.SpanContextFromContext(ctx).SpanID())
	})

	t.Run("malformed IDs fall back to the goroutine's span", func(t *testing.T) {
		logtest.ActiveSpan(t)

		ctx := recordContext(map[string]any{logconfig.TraceIDKey: "nope", logconfig.SpanIDKey: "nope"})

		assert.Equal(t, logtest.SpanID, trace.SpanContextFromContext(ctx).SpanID())
	})

	t.Run("no IDs and no goroutine span gives no span", func(t *testing.T) {
		logtest.NoActiveSpan(t)

		assert.False(t, trace.SpanContextFromContext(recordContext(map[string]any{})).IsValid())
	})
}
