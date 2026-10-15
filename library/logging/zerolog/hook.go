// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package zerolog adds trace_id and span_id to github.com/rs/zerolog events
// and exports them as OpenTelemetry log records, in addition to the
// application's own output. otelc has no zerolog instrumentation.
//
// zerolog encodes fields straight into a JSON buffer and keeps no structured
// form, so the export reads the finished JSON line where it is written. That
// also captures fields added with With(), which a call-time hook could not
// see. It requires the default JSON encoding: lines are skipped when zerolog
// is built with the binary_log tag.
package zerolog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otelc/pkg/hook"

	logconfig "library/logging"
)

const (
	// exportKey switches the export on and off; traceKey the trace_id and
	// span_id fields. They are independent.
	exportKey = "LOG_BRIDGE_ZEROLOG"
	traceKey  = "LOG_TRACE_ZEROLOG"
	scopeName = "library/logging/zerolog"
)

var bridge logconfig.Bridge[otellog.Logger]

func newLogger(p otellog.LoggerProvider) otellog.Logger {
	return p.Logger(scopeName)
}

// ---- trace_id and span_id on every event ----

// BeforeEventMsg runs before (*zerolog.Event).msg, which every Msg, Msgf and
// Send ends in. It adds trace_id and span_id fields the way a zerolog.Hook
// would, so they appear in the application's own output as well as in the
// export. The span comes from a context given to the event with Ctx, or
// else from the goroutine's active span. Do not add the same fields
// yourself, or they will appear twice.
func BeforeEventMsg(ictx hook.HookContext, e *zerolog.Event, msg string) {
	fmt.Println("<<BeforeEventMsg>>1")
	if e == nil || !logconfig.Enabled(traceKey) {
		return
	}
	fmt.Println("<<BeforeEventMsg>>2")

	traceID, spanID := logconfig.SpanIDs(logconfig.WithActiveSpan(e.GetCtx()))
	if traceID == "" {
		return
	}
	fmt.Println("<<BeforeEventMsg>>", "traceID", traceID, "spanID", spanID)

	e.Str(logconfig.TraceIDKey, traceID).Str(logconfig.SpanIDKey, spanID)
}

// ---- export ----

// BeforeNew runs before zerolog.New, which also backs Logger.Output. It puts
// a tee in front of the logger's writer, so every event is written as before
// and then exported once. Wrapping the outermost writer means a
// MultiLevelWriter is exported once, not once per destination.
//
// New can run while packages initialise (zerolog/log builds its global logger
// that way), so this only allocates; the OpenTelemetry work happens at write
// time.
func BeforeNew(ictx hook.HookContext, w io.Writer) {
	if w == nil || !logconfig.Enabled(exportKey) {
		return
	}
	fmt.Println("<<BeforeNew>>1")

	if _, wrapped := w.(*exportWriter); wrapped {
		return
	}
	fmt.Println("<<BeforeNew>>2")

	ictx.SetParam(0, &exportWriter{inner: w})
}

// exportWriter forwards to the wrapped writer, then exports what it wrote. It
// implements zerolog.LevelWriter so the event's level is known.
type exportWriter struct {
	inner io.Writer
}

func (w *exportWriter) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	export(zerolog.NoLevel, p)

	return n, err
}

func (w *exportWriter) WriteLevel(level zerolog.Level, p []byte) (int, error) {
	var (
		n   int
		err error
	)

	if lw, ok := w.inner.(zerolog.LevelWriter); ok {
		n, err = lw.WriteLevel(level, p)
	} else {
		n, err = w.inner.Write(p)
	}

	export(level, p)

	return n, err
}

// export turns one JSON line into a log record. p is only valid until the
// writer returns, so everything is copied out before then.
func export(level zerolog.Level, p []byte) {
	if !logconfig.Enabled(exportKey) {
		return
	}

	fields, ok := decode(p)
	if !ok {
		return
	}

	ctx := recordContext(fields)
	severity := severityOf(level)

	lg := bridge.Get(newLogger)
	if !lg.Enabled(ctx, otellog.EnabledParameters{Severity: severity}) {
		return
	}

	var rec otellog.Record

	rec.SetTimestamp(timestampOf(fields))
	rec.SetObservedTimestamp(time.Now())
	rec.SetSeverity(severity)
	rec.SetSeverityText(severityText(level))

	if msg, ok := fields[zerolog.MessageFieldName].(string); ok {
		rec.SetBody(attribute.StringValue(msg))
	}

	rec.AddAttributes(attributesOf(fields)...)

	// An event logged without the BeforeEventMsg fields still gets them when
	// a span is active on the goroutine.
	if _, has := fields[logconfig.TraceIDKey]; !has {
		if traceID, spanID := logconfig.SpanIDs(ctx); traceID != "" {
			rec.AddAttributes(
				attribute.String(logconfig.TraceIDKey, traceID),
				attribute.String(logconfig.SpanIDKey, spanID),
			)
		}
	}

	lg.Emit(ctx, rec)
}

func decode(p []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.UseNumber()

	var fields map[string]any
	if err := dec.Decode(&fields); err != nil {
		return nil, false
	}

	return fields, true
}

// recordContext carries the record's span for its native trace fields: the one
// named by the line's own trace_id and span_id, else the goroutine's.
func recordContext(fields map[string]any) context.Context {
	traceID, _ := fields[logconfig.TraceIDKey].(string)
	spanID, _ := fields[logconfig.SpanIDKey].(string)

	if ctx, ok := logconfig.WithSpanIDs(context.Background(), traceID, spanID); ok {
		return ctx
	}

	return logconfig.WithActiveSpan(context.Background())
}

func severityOf(l zerolog.Level) otellog.Severity {
	switch l {
	case zerolog.TraceLevel:
		return otellog.SeverityTrace
	case zerolog.DebugLevel:
		return otellog.SeverityDebug
	case zerolog.WarnLevel:
		return otellog.SeverityWarn
	case zerolog.ErrorLevel:
		return otellog.SeverityError
	case zerolog.FatalLevel, zerolog.PanicLevel:
		return otellog.SeverityFatal
	default: // InfoLevel, and NoLevel from Log()
		return otellog.SeverityInfo
	}
}

func severityText(l zerolog.Level) string {
	if text := strings.ToUpper(l.String()); text != "" {
		return text
	}

	return "INFO"
}

// timestampOf reads the line's time field in whatever format zerolog was
// configured to write it, falling back to now.
func timestampOf(fields map[string]any) time.Time {
	switch v := fields[zerolog.TimestampFieldName].(type) {
	case string:
		for _, layout := range []string{zerolog.TimeFieldFormat, time.RFC3339Nano} {
			if layout == "" {
				continue
			}

			if t, err := time.Parse(layout, v); err == nil {
				return t
			}
		}

	case json.Number:
		n, err := v.Int64()
		if err != nil {
			break
		}

		switch zerolog.TimeFieldFormat {
		case zerolog.TimeFormatUnix:
			return time.Unix(n, 0)
		case zerolog.TimeFormatUnixMs:
			return time.UnixMilli(n)
		case zerolog.TimeFormatUnixMicro:
			return time.UnixMicro(n)
		case zerolog.TimeFormatUnixNano:
			return time.Unix(0, n)
		}
	}

	return time.Now()
}

// attributesOf returns every field except the ones that become the record's
// body, severity and timestamp, in key order so output is deterministic.
func attributesOf(fields map[string]any) []attribute.KeyValue {
	keys := make([]string, 0, len(fields))

	for k := range fields {
		switch k {
		case zerolog.MessageFieldName, zerolog.LevelFieldName, zerolog.TimestampFieldName:
			continue
		}

		keys = append(keys, k)
	}

	sort.Strings(keys)

	out := make([]attribute.KeyValue, 0, len(keys)+2)

	for _, k := range keys {
		if kv, ok := attributeOf(k, fields[k]); ok {
			out = append(out, kv)
		}
	}

	return out
}

// attributeOf converts a decoded JSON value. Nested objects and arrays, such
// as zerolog Dict and Array fields, are kept as their JSON text.
func attributeOf(key string, v any) (attribute.KeyValue, bool) {
	switch x := v.(type) {
	case nil:
		return attribute.KeyValue{}, false

	case string:
		return attribute.String(key, x), true

	case bool:
		return attribute.Bool(key, x), true

	case json.Number:
		if i, err := x.Int64(); err == nil {
			return attribute.Int64(key, i), true
		}

		if f, err := x.Float64(); err == nil {
			return attribute.Float64(key, f), true
		}

		return attribute.String(key, x.String()), true

	default:
		b, err := json.Marshal(x)
		if err != nil {
			return attribute.KeyValue{}, false
		}

		return attribute.String(key, string(b)), true
	}
}
