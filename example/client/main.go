package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpSession is established once at startup and reused for every incoming
// HTTP request. Each request supplies its own fresh context to CallTool
// (via req.Context()), so every tool call gets its own trace instead of
// nesting under the long-lived mcp.session span created at Connect time.
var mcpSession *mcp.ClientSession

// toolCall is one entry in the /call request body.
type toolCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// callRequest is the JSON body accepted by the /call endpoint: one or more
// tools to execute.
type callRequest struct {
	Tools []toolCall `json:"tools"`
}

// toolCallResult is one entry in the /call response body. Error is set
// instead of Result when that specific tool call failed — one failing
// tool doesn't stop the others from running or being reported.
type toolCallResult struct {
	Tool   string              `json:"tool"`
	Result *mcp.CallToolResult `json:"result,omitempty"`
	Error  string              `json:"error,omitempty"`
}

// callHandler runs every requested tool call on the shared mcpSession,
// concurrently, and returns all of their results together.
//
// All calls share req.Context(), so every resulting "tools/call <tool>"
// span — client-side and, via the HTTP-span bridge, server-side too — is
// parented under this same incoming request's span: one "POST /call"
// trace containing every tool call made in this request, run in parallel
// as sibling spans, instead of one trace per tool.
func callHandler(w http.ResponseWriter, req *http.Request) {
	var body callRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Tools) == 0 {
		http.Error(w, "at least one tool is required", http.StatusBadRequest)
		return
	}
	for _, t := range body.Tools {
		if t.Tool == "" {
			http.Error(w, "tool name is required for every entry", http.StatusBadRequest)
			return
		}
	}

	results := make([]toolCallResult, len(body.Tools))

	var wg sync.WaitGroup
	wg.Add(len(body.Tools))
	for i, t := range body.Tools {
		go func(i int, t toolCall) {
			defer wg.Done()
			results[i] = runTool(req.Context(), t)
		}(i, t)
	}
	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Results []toolCallResult `json:"results"`
	}{Results: results})
}

// runTool executes one tool call and turns any failure — transport error
// or a tool-reported error result — into a toolCallResult.Error string
// rather than an error return, so callHandler can run every tool without
// one failure aborting the rest.
func runTool(ctx context.Context, t toolCall) toolCallResult {
	res, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      t.Tool,
		Arguments: t.Args,
	})
	if err != nil {
		return toolCallResult{Tool: t.Tool, Error: err.Error()}
	}
	if res.IsError {
		return toolCallResult{Tool: t.Tool, Error: "tool call returned an error"}
	}
	return toolCallResult{Tool: t.Tool, Result: res}
}

// connect establishes the MCP session exactly once, at startup.
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
