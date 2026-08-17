package server

import (
	"context"
	"log"
	"time"

	_ "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer trace.Tracer
var tp *sdktrace.TracerProvider

func init() {
	ctx := context.Background()

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint("localhost:4317"),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		log.Printf("otel-mcp-server: failed to create exporter: %v", err)
		tracer = otel.Tracer("mcp")
		return
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("mcp-server")))
	if err != nil {
		log.Printf("otel-mcp-server: failed to create resource: %v", err)
		tracer = otel.Tracer("mcp")
		return
	}

	tp = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tracer = otel.Tracer("mcp")
}

func Shutdown(ctx context.Context) error {
	if tp == nil {
		return nil
	}
	flushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return tp.Shutdown(flushCtx)
}
