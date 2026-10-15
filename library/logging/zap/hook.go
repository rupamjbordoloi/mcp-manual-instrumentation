// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package zap exports zap entries through the otelzap bridge, in addition to
// whatever core the application already uses.
package zap

import (
	"context"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	logconfig "library/logging"
)

const (
	instrumentationKey = "LOG_BRIDGE_ZAP"
	scopeName          = "library/logging/zap"

	// contextFieldKey is the key of the field that carries the context to
	// the otelzap core. The core looks for a context.Context value, not for
	// a particular key, so the name only matters when debugging.
	contextFieldKey = "context"
)

var bridge logconfig.Bridge[zapcore.Core]

func newCore(p log.LoggerProvider) zapcore.Core {
	return ctxCore{otelzap.NewCore(scopeName, otelzap.WithLoggerProvider(p))}
}

// BeforeCheck runs before (*zap.Logger).check, the single function every
// Logger and SugaredLogger call goes through. It keeps the logger for
// AfterCheck, which only receives the result.
func BeforeCheck(ictx hook.HookContext, l *zap.Logger, lvl zapcore.Level, msg string) {
	ictx.SetData(l)
}

// AfterCheck adds the bridge as an extra core on the checked entry, so zap
// writes the entry to it right after the application's own cores.
func AfterCheck(ictx hook.HookContext, ce *zapcore.CheckedEntry) {
	if ce == nil || !logconfig.Enabled(instrumentationKey) {
		return
	}

	l, _ := ictx.GetData().(*zap.Logger)
	if l == nil {
		return
	}

	// Follow the application's own level, and skip loggers that already
	// use the bridge as their core.
	core := l.Core()
	if !core.Enabled(ce.Level) {
		return
	}

	if _, bridged := core.(*otelzap.Core); bridged {
		return
	}

	// ce is non-nil, so AddCore appends in place and returns the same entry.
	bridge.Get(newCore).Check(ce.Entry, ce)
}

// ctxCore adds the active span's context to every entry it writes. The
// otelzap core takes trace context from a context.Context field, which zap
// call sites rarely pass.
type ctxCore struct {
	zapcore.Core
}

func (c ctxCore) With(fields []zapcore.Field) zapcore.Core {
	return ctxCore{c.Core.With(fields)}
}

// Check registers c, not the wrapped core, so that Write below is used.
func (c ctxCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}

	return ce
}

func (c ctxCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	return c.Core.Write(ent, withActiveSpan(fields))
}

// withActiveSpan returns fields plus a context field, unless a call site
// already supplied one or there is no active span. It never modifies fields:
// zap passes the same slice to every core.
func withActiveSpan(fields []zapcore.Field) []zapcore.Field {
	for _, f := range fields {
		if _, ok := f.Interface.(context.Context); ok {
			return fields
		}
	}

	ctx := logconfig.WithActiveSpan(context.Background())
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return fields
	}

	out := make([]zapcore.Field, 0, len(fields)+1)
	out = append(out, fields...)

	return append(out, zapcore.Field{Key: contextFieldKey, Type: zapcore.ReflectType, Interface: ctx})
}
