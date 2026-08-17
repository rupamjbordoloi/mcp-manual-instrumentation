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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	servertrace "library/server"
)

type Input struct {
	Name string `json:"name" jsonschema:"the name of the person to greet"`
}

type Output struct {
	Greeting string `json:"greeting" jsonschema:"the greeting to tell to the user"`
}

// SayHi is pure business logic. tool.greet's span is injected around every
// call to this function by the mcp_server_tool_greet rule — no OTel import
// or call appears here.
func SayHi(ctx context.Context, req *mcp.CallToolRequest, input Input) (
	*mcp.CallToolResult,
	Output,
	error,
) {
	if input.Name == "" {
		return nil, Output{}, errors.New("name is required")
	}
	return nil, Output{Greeting: "Hi " + input.Name}, nil
}

func main() {
	log.Println("Starting server....")

	getServer := func(r *http.Request) *mcp.Server {
		server := mcp.NewServer(&mcp.Implementation{Name: "greeter", Version: "v1.0.0"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "say hi"}, SayHi)
		return server
	}

	// The one composition line that remains: wrapping the MCP handler with
	// the JSON-RPC protocol middleware. See servertrace.hook.go's
	// AfterNewStreamableHTTPHandler comment for why this can't be a
	// fully transparent hook.
	mcpHandler := servertrace.ProtocolMiddleware(mcp.NewStreamableHTTPHandler(getServer, nil))

	// otelc's built-in net/http/server instrumentation (blank-imported in
	// otel.instrumentation.go) instruments this mux automatically — no
	// otelhttp.NewHandler call needed here.
	mux := http.NewServeMux()
	mux.Handle("POST /mcp", mcpHandler)
	mux.Handle("GET /mcp", mcpHandler)
	mux.Handle("DELETE /mcp", mcpHandler)

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
	if err := servertrace.Shutdown(shutdownCtx); err != nil {
		log.Printf("otel shutdown error: %v", err)
	}
}
