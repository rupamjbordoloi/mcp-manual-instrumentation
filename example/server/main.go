package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	zlog "github.com/rs/zerolog/log"
	"go.uber.org/zap"
)

type Input struct {
	Name string `json:"name" jsonschema:"the name of the person to greet"`
}

type Output struct {
	Greeting string `json:"greeting" jsonschema:"the greeting to tell to the user"`
}

// SayHi is pure business logic — zero OTel code. tool.SayHi's span is
// injected by the mcp_server_call_tool rule targeting (*Server).callTool,
// which fires for every registered tool regardless of name. Adding
// tool.weather, tool.sql, etc. requires no YAML or tracing changes at all.
func SayHi(ctx context.Context, req *mcp.CallToolRequest, input Input) (
	*mcp.CallToolResult,
	Output,
	error,
) {
	if input.Name == "" {
		return nil, Output{}, errors.New("name is required")
	}
	time.Sleep(5 * time.Second)
	log.Println("Default Log: Printing from greet tool")
	slog.InfoContext(ctx, "Slog: Printing from greet tool")

	logger, _ := zap.NewProduction()
	defer logger.Sync()
	logger.Info("Zap: Printing from greet tool")

	zlog.Print("zerolog: Printing from greet tool")
	zlog.Info().Msg("zerolog info: Printing from greet tool")

	return nil, Output{Greeting: "Hi " + input.Name}, nil
}

func SayBye(ctx context.Context, req *mcp.CallToolRequest, input Input) (
	*mcp.CallToolResult,
	Output,
	error,
) {
	if input.Name == "" {
		return nil, Output{}, errors.New("name is required")
	}
	time.Sleep(4 * time.Second)
	return nil, Output{Greeting: "Bye " + input.Name}, nil
}

// main has zero OTel code — AfterMain flush and all protocol/tool spans are
// injected by otelc rules. The only application-level concern here is the
// HTTP server lifecycle and graceful shutdown.
func main() {
	log.Println("Starting server....")
	runServer(":8080")
}

func runServer(url string) {
	// Create an MCP server.
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "greeter",
		Version: "v1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "say hi"}, SayHi)
	mcp.AddTool(server, &mcp.Tool{Name: "bye", Description: "say bye"}, SayBye)

	// Tracing and HTTP metrics are applied automatically at build time by
	// mcp.otelc.yaml (mcp_server_streamable_http_request, http_metrics_wrap)
	// — no manual wrapping needed here.
	mcpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return server
	}, nil)

	srv := &http.Server{Addr: url, Handler: mcpHandler}

	go func() {
		log.Println("MCP server listening on :8080")
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
}
