package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	fmt.Println("executing sayhi------->>>")
	time.Sleep(time.Second * 5)
	fmt.Println("executed sayhi------->>>")
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
	fmt.Println("executed sayhi------->>>")
	return nil, Output{Greeting: "Bye " + input.Name}, nil
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			fmt.Printf("\n=== MCP HTTP POST ===\n")
			// fmt.Printf("Path: %s\n", r.URL.Path)
			// fmt.Printf("Body: %s\n", body)
			fmt.Println("Headers start->", r.Header.Get("Traceparent"))

			// Restore body so MCP SDK can read it.
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		next.ServeHTTP(w, r)
	})
}

// main has zero OTel code — AfterMain flush and all protocol/tool spans are
// injected by otelc rules. The only application-level concern here is the
// HTTP server lifecycle and graceful shutdown.
func main() {
	log.Println("Starting server....")
	runServer(":8080")

	// getServer := func(r *http.Request) *mcp.Server {
	// 	server := mcp.NewServer(&mcp.Implementation{Name: "greeter", Version: "v1.0.0"}, nil)
	// 	mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "say hi"}, SayHi)
	// 	mcp.AddTool(server, &mcp.Tool{Name: "bye", Description: "say bye"}, SayBye)
	// 	return server
	// }

	// // No ProtocolMiddleware wrapper here — protocol-level spans are now
	// // injected into the SDK's own receiving middleware chain via the
	// // AfterAddReceivingMiddleware hook. main.go has zero tracing references.
	// mcpHandler := server.SpanMiddleware(loggingMiddleware(mcp.NewStreamableHTTPHandler(getServer, nil)))

	// mux := http.NewServeMux()
	// mux.Handle("POST /mcp", mcpHandler)
	// mux.Handle("GET /mcp", mcpHandler)
	// mux.Handle("DELETE /mcp", mcpHandler)

	// srv := &http.Server{Addr: ":8080", Handler: mux}

	// go func() {
	// 	log.Println("MCP server listening on :8080/mcp")
	// 	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
	// 		log.Fatalf("server error: %v", err)
	// 	}
	// }()

	// stop := make(chan os.Signal, 1)
	// signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	// <-stop

	// log.Println("Shutting down...")
	// shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	// defer cancel()
	// _ = srv.Shutdown(shutdownCtx)
}

func runServer(url string) {
	// Create an MCP server.
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "greeter",
		Version: "v1.0.0",
	}, nil)

	// Add MCP-level logging middleware.
	// server.AddReceivingMiddleware(library.ServerProtocolMiddleware)

	mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "say hi"}, SayHi)
	mcp.AddTool(server, &mcp.Tool{Name: "bye", Description: "say bye"}, SayBye)

	// Create the streamable HTTP handler.
	mcpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return server
	},
		nil,
		// &mcp.StreamableHTTPOptions{Stateless: true},
	)

	srv := &http.Server{Addr: url, Handler: mcpHandler}

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
}
