package slog

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook/hooktest"

	"github.hdfcbank.com/HDFCBANK/ps-otel/instrumentation/tool/logtest"
)

func appLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level}))
}

func logOnce(l *slog.Logger, ctx context.Context, level slog.Level, msg string, args ...any) {
	BeforeSlogLog(hooktest.NewMockHookContext(), l, ctx, level, msg, args...)
}

func TestBeforeSlogLog(t *testing.T) {
	for _, sc := range logtest.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			rec := logtest.Install(t)
			sc.Apply(t)

			logOnce(appLogger(slog.LevelInfo), context.Background(), slog.LevelInfo, "hello", "k", "v")

			got := rec.Records()
			require.Len(t, got, 1)
			assert.Equal(t, "hello", got[0].Body())
			assert.Equal(t, []string{"v"}, got[0].Attrs()["k"], "call-site attributes are exported")
			logtest.AssertNativeTrace(t, got[0], sc.WantTrace)
		})
	}

	t.Run("severity follows the slog level", func(t *testing.T) {
		tests := map[slog.Level]log.Severity{
			slog.LevelDebug: log.SeverityDebug,
			slog.LevelInfo:  log.SeverityInfo,
			slog.LevelWarn:  log.SeverityWarn,
			slog.LevelError: log.SeverityError,
		}

		for level, want := range tests {
			rec := logtest.Install(t)
			logtest.ActiveSpan(t)

			logOnce(appLogger(slog.LevelDebug), context.Background(), level, "m")

			require.Len(t, rec.Records(), 1, level.String())
			assert.Equal(t, want, rec.Records()[0].Record.Severity(), level.String())
		}
	})

	t.Run("a span only on the call-site context (InfoContext)", func(t *testing.T) {
		rec := logtest.Install(t)
		logtest.NoActiveSpan(t)

		ctx := trace.ContextWithSpanContext(context.Background(), logtest.SpanContext())
		logOnce(appLogger(slog.LevelInfo), ctx, slog.LevelInfo, "hello")

		require.Len(t, rec.Records(), 1)
		logtest.AssertNativeTrace(t, rec.Records()[0], true)
	})

	t.Run("a nil context is accepted", func(t *testing.T) {
		rec := logtest.Install(t)
		logtest.ActiveSpan(t)

		var ctx context.Context
		logOnce(appLogger(slog.LevelInfo), ctx, slog.LevelInfo, "hello")

		require.Len(t, rec.Records(), 1)
		logtest.AssertNativeTrace(t, rec.Records()[0], true)
	})

	t.Run("a record the application's own level drops is not exported", func(t *testing.T) {
		rec := logtest.Install(t)

		logOnce(appLogger(slog.LevelError), context.Background(), slog.LevelInfo, "dropped", "k", "v")

		assert.Empty(t, rec.Records())
	})

	t.Run("a logger that already writes through the bridge is not exported twice", func(t *testing.T) {
		rec := logtest.Install(t)
		l := slog.New(otelslog.NewHandler("app"))

		logOnce(l, context.Background(), slog.LevelInfo, "hello") // the hook
		l.Info("hello")                                           // what slog then does itself

		assert.Len(t, rec.Records(), 1, "only the logger's own export; the hook added none")
	})

	t.Run("a call-site trace_id attribute is exported as given", func(t *testing.T) {
		rec := logtest.Install(t)
		logtest.ActiveSpan(t)

		logOnce(appLogger(slog.LevelInfo), context.Background(), slog.LevelInfo, "hello", "trace_id", "from-app")

		require.Len(t, rec.Records(), 1)
		assert.Equal(t, []string{"from-app"}, rec.Records()[0].Attrs()["trace_id"])
	})

	t.Run("a provider installed after start-up is picked up", func(t *testing.T) {
		first := logtest.Install(t)
		logOnce(appLogger(slog.LevelInfo), context.Background(), slog.LevelInfo, "one")

		second := logtest.Install(t) // as when the runtime replaces the placeholder provider
		logOnce(appLogger(slog.LevelInfo), context.Background(), slog.LevelInfo, "two")

		require.Len(t, first.Records(), 1)
		require.Len(t, second.Records(), 1)
		assert.Equal(t, "two", second.Records()[0].Body())
	})

	t.Run("not exported when the instrumentation is disabled", func(t *testing.T) {
		rec := logtest.Install(t)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)

		logOnce(appLogger(slog.LevelInfo), context.Background(), slog.LevelInfo, "hello")

		assert.Empty(t, rec.Records())
	})

	t.Run("not exported when OpenTelemetry filters the record out", func(t *testing.T) {
		rec := logtest.Install(t)
		rec.SetEnabled(false)

		logOnce(appLogger(slog.LevelInfo), context.Background(), slog.LevelInfo, "hello")

		assert.Empty(t, rec.Records())
	})
}

func TestBeforeSlogLogAttrs(t *testing.T) {
	for _, sc := range logtest.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			rec := logtest.Install(t)
			sc.Apply(t)

			BeforeSlogLogAttrs(hooktest.NewMockHookContext(), appLogger(slog.LevelInfo), context.Background(),
				slog.LevelInfo, "hello", slog.String("k", "v"), slog.Int("n", 5), slog.Bool("b", true))

			got := rec.Records()
			require.Len(t, got, 1)
			assert.Equal(t, "hello", got[0].Body())

			attrs := got[0].Attrs()
			assert.Equal(t, []string{"v"}, attrs["k"])
			assert.Equal(t, []string{"5"}, attrs["n"])
			assert.Equal(t, []string{"true"}, attrs["b"])
			logtest.AssertNativeTrace(t, got[0], sc.WantTrace)
		})
	}

	t.Run("honours the application's level", func(t *testing.T) {
		rec := logtest.Install(t)

		BeforeSlogLogAttrs(hooktest.NewMockHookContext(), appLogger(slog.LevelError), context.Background(),
			slog.LevelInfo, "dropped")

		assert.Empty(t, rec.Records())
	})

	t.Run("honours the disabled switch", func(t *testing.T) {
		rec := logtest.Install(t)
		t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)

		BeforeSlogLogAttrs(hooktest.NewMockHookContext(), appLogger(slog.LevelInfo), context.Background(),
			slog.LevelInfo, "disabled")

		assert.Empty(t, rec.Records())
	})
}
