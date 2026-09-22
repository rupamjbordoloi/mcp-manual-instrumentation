package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpSession is established once at process startup and reused for every
// incoming HTTP request until this process is terminated. This is the key
// change: previously Connect -> CallTool -> Close all shared one context,
// so mcp.session (started in BeforeConnect, ended in AfterClose) was the
// ancestor of every tool-call span. Now Connect happens exactly once,
// standalone, and each tool call rides on a fresh context supplied by an
// incoming HTTP request.
var mcpSession *mcp.ClientSession

// callRequest is the JSON body accepted by the /call endpoint.
type callRequest struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// callHandler forwards one HTTP request to one MCP tool call, reusing the
// long-lived mcpSession. mcp.tools.call wraps (*ClientSession).CallTool
// (BeforeCallTool/AfterCallTool hooks) exactly as before — what changes is
// the context it runs under. req.Context() here is created fresh by the
// net/http/server instrumentation for each incoming request, so every
// mcp.tools.call span now starts its own trace instead of nesting inside
// the single mcp.session span created at startup.
func callHandler(w http.ResponseWriter, req *http.Request) {
	var body callRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Tool == "" {
		http.Error(w, "tool is required", http.StatusBadRequest)
		return
	}

	fmt.Println(body.Tool, body.Args)

	res, err := mcpSession.CallTool(req.Context(), &mcp.CallToolParams{
		Name: body.Tool,
		// Arguments: body.Args,
		Arguments: map[string]string{"name": "HDFC"},
	})
	out, err := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if res.IsError {
		fmt.Println(res.Content, err)
		http.Error(w, "tool call returned an error", http.StatusUnprocessableEntity)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// connect establishes the MCP session exactly once. mcp.session starts
// inside (*Client).Connect (BeforeConnect hook) and mcp.initialize ends
// inside (*Client).Connect (AfterConnect hook) — both happen a single time
// at process startup, standalone from any tool-call trace.
func connect(ctx context.Context) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)

	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://localhost:8080",
	}

	return client.Connect(ctx, transport, nil)
}

// main has zero business logic beyond wiring: connect once, serve HTTP
// requests against that one session, and shut down cleanly on signal.
// mcp.shutdown and mcp.session end inside (*ClientSession).Close
// (BeforeClose/AfterClose hooks), and AfterMain flush is injected by the
// mcp_client_shutdown_on_exit rule — no OTel code needed here.
func main() {
	log.Println("Connecting to mcp server...")
	session, err := connect(context.Background())
	if err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	mcpSession = session
	defer mcpSession.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/call", callHandler)

	srv := &http.Server{Addr: ":8090", Handler: mux}

	go func() {
		log.Println("Client-facing HTTP server listening on :8090")
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
