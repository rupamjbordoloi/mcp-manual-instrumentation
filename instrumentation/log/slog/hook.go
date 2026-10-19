// Package slog exports log/slog records through the otelslog bridge, in
// addition to whatever handler the application already uses.
package slog

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otelc/pkg/hook"

	logconfig "github.hdfcbank.com/HDFCBANK/ps-otel/instrumentation/tool/util"
)

const (
	instrumentationKey = "logs/slog"
	scopeName          = "github.hdfcbank.com/HDFCBANK/ps-otel/instrumentation/log/slog"
)

var bridge logconfig.Bridge[*otelslog.Handler]

func newHandler(p log.LoggerProvider) *otelslog.Handler {
	return otelslog.NewHandler(scopeName, otelslog.WithLoggerProvider(p))
}

// BeforeSlogLog runs before (*slog.Logger).log, which backs Debug, Info,
// Warn, Error, their Context variants and Log.
func BeforeSlogLog(
	ictx hook.HookContext,
	l *slog.Logger,
	ctx context.Context,
	level slog.Level,
	msg string,
	args ...any,
) {
	if !shouldExport(l, ctx, level) {
		return
	}

	r := slog.NewRecord(time.Now(), level, msg, 0)
	r.Add(args...)
	export(ctx, r)
}

// BeforeSlogLogAttrs runs before (*slog.Logger).logAttrs, which backs
// LogAttrs.
func BeforeSlogLogAttrs(
	ictx hook.HookContext,
	l *slog.Logger,
	ctx context.Context,
	level slog.Level,
	msg string,
	attrs ...slog.Attr,
) {
	if !shouldExport(l, ctx, level) {
		return
	}

	r := slog.NewRecord(time.Now(), level, msg, 0)
	r.AddAttrs(attrs...)
	export(ctx, r)
}

// shouldExport follows the application's own level: a record its handler
// would drop is not exported either. A logger that already writes through
// the bridge is skipped so records are not exported twice.
func shouldExport(l *slog.Logger, ctx context.Context, level slog.Level) bool {
	if !logconfig.Enabled(instrumentationKey) || !l.Enabled(ctx, level) {
		return false
	}

	_, bridged := l.Handler().(*otelslog.Handler)

	return !bridged
}

func export(ctx context.Context, r slog.Record) {
	ctx = logconfig.WithActiveSpan(ctx)

	h := bridge.Get(newHandler)
	if h.Enabled(ctx, r.Level) {
		_ = h.Handle(ctx, r)
	}
}
