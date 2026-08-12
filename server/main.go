package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	config "library"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const serverName = "greeter"

type Input struct {
	Name string `json:"name" jsonschema:"the name of the person to greet"`
}

type Output struct {
	Greeting string `json:"greeting" jsonschema:"the greeting to tell to the user"`
}

// sayHi is pure business logic — zero tracing code beyond the one nested
// business-logic span, which demonstrates where DB/Redis/external-HTTP child
// spans belong once this tool grows.
func sayHi(ctx context.Context, req *mcp.CallToolRequest, input Input) (
	*mcp.CallToolResult,
	Output,
	error,
) {
	if input.Name == "" {
		return nil, Output{}, errors.New("name is required")
	}

	_, end := config.ChildSpan(ctx, "business_logic.build_greeting",
		attribute.String("input.name", input.Name),
	)
	greeting := "Hi " + input.Name
	end(nil)

	return nil, Output{Greeting: greeting}, nil
}

func setupOTel(ctx context.Context) (func(context.Context) error, error) {
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint("localhost:4317"),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("mcp-server")))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp.Shutdown, nil
}

func main() {
	ctx := context.Background()

	shutdown, err := setupOTel(ctx)
	if err != nil {
		log.Fatalf("otel setup failed: %v", err)
	}

	log.Println("Starting server....")

	getServer := func(r *http.Request) *mcp.Server {
		server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: "v1.0.0"}, nil)
		mcp.AddTool(server,
			&mcp.Tool{Name: "greet", Description: "say hi"},
			config.WrapTool("greet", "1.0", sayHi),
		)
		return server
	}

	mcpHandler := mcp.NewStreamableHTTPHandler(getServer, nil)
	protocolHandler := config.ProtocolMiddleware(mcpHandler)

	// otelhttp.NewHandler auto-detects the route pattern from the
	// ServeMux registration below (Go 1.22+ method-pattern routing),
	// producing span names like "POST /mcp" and an http.route attribute
	// with no extra wrapper needed — WithRouteTag was removed in
	// contrib v0.70.0 because this became automatic.
	instrumentedHandler := otelhttp.NewHandler(protocolHandler, "mcp-server")

	mux := http.NewServeMux()
	mux.Handle("POST /mcp", instrumentedHandler)
	mux.Handle("GET /mcp", instrumentedHandler)
	mux.Handle("DELETE /mcp", instrumentedHandler)

	srv := &http.Server{Addr: ":8080", Handler: mux}

	go func() {
		log.Println("MCP server listening on :8080/mcp")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("Shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if err := shutdown(shutdownCtx); err != nil {
		log.Printf("otel shutdown error: %v", err)
	}
}
