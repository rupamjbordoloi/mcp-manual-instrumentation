package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	config "library"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const clientName = "mcp-client"

var tracer = otel.Tracer("mcp")

func setupOTel(ctx context.Context) (func(context.Context) error, error) {
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint("localhost:4317"),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(clientName)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp.Shutdown, nil
}

func run(ctx context.Context) (err error) {
	ctx, sessionSpan := tracer.Start(ctx, "mcp.session", trace.WithSpanKind(trace.SpanKindClient))
	defer func() {
		if err != nil {
			sessionSpan.SetStatus(codes.Error, err.Error())
		} else {
			sessionSpan.SetStatus(codes.Ok, "")
		}
		sessionSpan.End()
	}()

	sessionSpan.SetAttributes(
		attribute.String("mcp.session.id", sessionSpan.SpanContext().TraceID().String()),
		attribute.String("mcp.client.name", clientName),
		attribute.String("mcp.protocol.version", config.ProtocolVersion),
	)

	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "v1.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://localhost:8080/mcp",
		HTTPClient: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
			Timeout:   10 * time.Second,
		},
	}

	initCtx, initSpan := tracer.Start(ctx, "mcp.initialize", trace.WithSpanKind(trace.SpanKindClient))
	session, err := client.Connect(initCtx, transport, nil)
	if err != nil {
		finishClientSpan(initSpan, err)
		return err
	}
	finishClientSpan(initSpan, nil)

	sessionSpan.SetAttributes(attribute.String("mcp.server.name", "greeter"))

	defer func() {
		_, shutdownSpan := tracer.Start(ctx, "mcp.shutdown", trace.WithSpanKind(trace.SpanKindClient))
		closeErr := session.Close()
		finishClientSpan(shutdownSpan, closeErr)
	}()

	callCtx, callSpan := tracer.Start(ctx, "mcp.tools.call",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", "tools/call"),
			attribute.String("mcp.tool.name", "greet"),
		),
	)

	res, callErr := session.CallTool(callCtx, &mcp.CallToolParams{
		Name:      "greet",
		Arguments: map[string]any{"name": "you"},
	})
	if callErr != nil {
		finishClientSpan(callSpan, callErr)
		return callErr
	}

	_, deserializeSpan := tracer.Start(callCtx, "mcp.deserialize_response", trace.WithSpanKind(trace.SpanKindInternal))
	if res.IsError {
		deserializeErr := fmt.Errorf("tool call returned an error result")
		finishClientSpan(deserializeSpan, deserializeErr)
		finishClientSpan(callSpan, deserializeErr)
		return deserializeErr
	}
	for _, c := range res.Content {
		log.Print(c.(*mcp.TextContent).Text)
	}
	finishClientSpan(deserializeSpan, nil)
	finishClientSpan(callSpan, nil)

	return nil
}

func finishClientSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

func main() {
	ctx := context.Background()

	shutdown, err := setupOTel(ctx)
	if err != nil {
		log.Fatalf("otel setup failed: %v", err)
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(flushCtx); err != nil {
			log.Printf("otel shutdown error: %v", err)
		}
	}()

	log.Println("Calling mcp server...")
	if err := run(ctx); err != nil {
		log.Fatalf("run failed: %v", err)
	}
}
