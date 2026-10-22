package zap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook/hooktest"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.hdfcbank.com/HDFCBANK/ps-otel/instrumentation/tool/logtest"
)

func appLogger(level zapcore.LevelEnabler) (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(level)

	return zap.New(core), logs
}

// emit does what the injected hooks and the logging call do around
// (*zap.Logger).check: BeforeCheck, check, AfterCheck, then write the entry.
func emit(l *zap.Logger, lvl zapcore.Level, msg string, fields ...zapcore.Field) {
	ictx := hooktest.NewMockHookContext()

	BeforeCheck(ictx, l, lvl, msg)
	ce := l.Check(lvl, msg)
	AfterCheck(ictx, ce)

	if ce != nil {
		ce.Write(fields...)
	}
}

func TestHooks(t *testing.T) {
	for _, sc := range logtest.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			rec := logtest.Install(t)
			sc.Apply(t)
			l, _ := appLogger(zap.InfoLevel)

			emit(l, zap.InfoLevel, "hello", zap.String("k", "v"))

			got := rec.Records()
			require.Len(t, got, 1)
			assert.Equal(t, "hello", got[0].Body())
			assert.Equal(t, []string{"v"}, got[0].Attrs()["k"], "call-site fields are exported")
			logtest.AssertNativeTrace(t, got[0], sc.WantTrace)
		})
	}

	t.Run("severity follows the zap level", func(t *testing.T) {
		tests := map[zapcore.Level]log.Severity{
			zap.DebugLevel: log.SeverityDebug,
			zap.InfoLevel:  log.SeverityInfo,
			zap.WarnLevel:  log.SeverityWarn,
			zap.ErrorLevel: log.SeverityError,
		}

		for level, want := range tests {
			rec := logtest.Install(t)
			logtest.ActiveSpan(t)
			l, _ := appLogger(zap.DebugLevel)

			emit(l, level, "m")

			require.Len(t, rec.Records(), 1, level.String())
			assert.Equal(t, want, rec.Records()[0].Record.Severity(), level.String())
		}
	})

	t.Run("the application's own core is untouched", func(t *testing.T) {
		logtest.Install(t)
		logtest.ActiveSpan(t)
		l, logs := appLogger(zap.InfoLevel)
		fields := []zapcore.Field{zap.String("k", "v")}

		emit(l, zap.InfoLevel, "hello", fields...)

		require.Equal(t, 1, logs.Len())
		assert.Equal(t, map[string]any{"k": "v"}, logs.All()[0].ContextMap(),
			"the span is added for the bridge only, not to the application's fields")
		assert.Len(t, fields, 1, "the slice zap passes to every core is not modified")
	})

	t.Run("an entry the application's own level drops is not exported", func(t *testing.T) {
		rec := logtest.Install(t)
		l, _ := appLogger(zap.ErrorLevel)

		emit(l, zap.InfoLevel, "dropped")

		assert.Empty(t, rec.Records())
	})

	t.Run("a logger that already uses the bridge core is not exported twice", func(t *testing.T) {
		rec := logtest.Install(t)
		l := zap.New(otelzap.NewCore("app"))

		emit(l, zap.InfoLevel, "hello")

		assert.Len(t, rec.Records(), 1, "exactly the one the logger's own core produced")
	})

	t.Run("a context field from the call site is honoured", func(t *testing.T) {
		rec := logtest.Install(t)
		logtest.NoActiveSpan(t)
		l, _ := appLogger(zap.InfoLevel)
		ctx := trace.ContextWithSpanContext(context.Background(), logtest.SpanContext())

		emit(l, zap.InfoLevel, "hello", zap.Any("ctx", ctx))

		require.Len(t, rec.Records(), 1)
		logtest.AssertNativeTrace(t, rec.Records()[0], true)
	})

	t.Run("a context field the call site passed is left as it is, even without a span", func(t *testing.T) {
		rec := logtest.Install(t)
		logtest.ActiveSpan(t)
		l, _ := appLogger(zap.InfoLevel)

		emit(l, zap.InfoLevel, "hello", zap.Any("ctx", context.Background()))

		require.Len(t, rec.Records(), 1)
		logtest.AssertNativeTrace(t, rec.Records()[0], false)
	})

	t.Run("not exported when the instrumentation is disabled", func(t *testing.T) {
		rec := logtest.Install(t)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)
		l, _ := appLogger(zap.InfoLevel)

		emit(l, zap.InfoLevel, "hello")

		assert.Empty(t, rec.Records())
	})

	t.Run("not exported when OpenTelemetry filters the entry out", func(t *testing.T) {
		rec := logtest.Install(t)
		rec.SetEnabled(false)
		l, _ := appLogger(zap.InfoLevel)

		emit(l, zap.InfoLevel, "hello")

		assert.Empty(t, rec.Records())
	})

	t.Run("a terminal-level entry the application's own core disabled is not exported", func(t *testing.T) {
		rec := logtest.Install(t)
		l, _ := appLogger(zap.FatalLevel) // Panic is below this, so the core would drop it
		ictx := hooktest.NewMockHookContext()

		BeforeCheck(ictx, l, zap.PanicLevel, "boom")
		ce := l.Check(zap.PanicLevel, "boom") // zap still returns an entry, so it can panic
		require.NotNil(t, ce)

		AfterCheck(ictx, ce) // not written: writing a Panic entry panics

		assert.Empty(t, rec.Records())
	})

	t.Run("AfterCheck without BeforeCheck, or with no entry, does nothing", func(t *testing.T) {
		rec := logtest.Install(t)
		l, _ := appLogger(zap.InfoLevel)
		ce := l.Check(zap.InfoLevel, "hello")

		require.NotPanics(t, func() {
			AfterCheck(hooktest.NewMockHookContext(), ce)
			AfterCheck(hooktest.NewMockHookContext(), nil)
		})
		assert.Empty(t, rec.Records())
	})
}

func TestWithActiveSpan(t *testing.T) {
	logtest.Install(t)

	t.Run("returns the same fields when there is no span", func(t *testing.T) {
		logtest.NoActiveSpan(t)
		fields := []zapcore.Field{zap.String("k", "v")}

		assert.Equal(t, fields, withActiveSpan(fields))
	})

	t.Run("adds a context field and does not touch the input", func(t *testing.T) {
		logtest.ActiveSpan(t)
		fields := make([]zapcore.Field, 1, 8) // spare capacity: an append would write into it
		fields[0] = zap.String("k", "v")

		out := withActiveSpan(fields)

		require.Len(t, out, 2)
		_, isCtx := out[1].Interface.(context.Context)
		assert.True(t, isCtx)
		assert.Len(t, fields, 1)
		assert.Equal(t, zapcore.Field{}, fields[:2][1], "spare capacity was not written to")
	})

	t.Run("a context field already present is left alone", func(t *testing.T) {
		logtest.ActiveSpan(t)
		fields := []zapcore.Field{zap.Any("ctx", context.Background())}

		assert.Equal(t, fields, withActiveSpan(fields))
	})
}

func TestCtxCoreWith(t *testing.T) {
	rec := logtest.Install(t)
	logtest.ActiveSpan(t)

	core := newCore(otel.GetLoggerProvider())
	derived := core.With([]zapcore.Field{zap.String("k", "v")})

	assert.IsType(t, ctxCore{}, derived, "a derived core keeps the span-adding wrapper")

	require.NoError(t, derived.Write(zapcore.Entry{Level: zap.InfoLevel, Message: "hello"}, nil))

	require.Len(t, rec.Records(), 1)
	assert.Equal(t, []string{"v"}, rec.Records()[0].Attrs()["k"], "fields bound with With reach the export")
	logtest.AssertNativeTrace(t, rec.Records()[0], true)
}
