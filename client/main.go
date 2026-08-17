package main

import (
	"context"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	clienttrace "library/client"
)

// run contains only business logic. The mcp.session root span is injected
// into this function's body at compile time by the mcp_client_session_root_span
// rule — there is no OTel import or call anywhere in this file.
func run(ctx context.Context) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)

	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://localhost:8080/mcp",
		// No manual otelhttp.NewTransport wrapping — otelc's built-in
		// net/http/client instrumentation (blank-imported in
		// otel.instrumentation.go) instruments the default transport
		// automatically at compile time.
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "greet",
		Arguments: map[string]any{"name": "you"},
	})
	if err != nil {
		return err
	}
	if res.IsError {
		return http.ErrHandlerTimeout // placeholder; replace with your own sentinel if preferred
	}
	for _, c := range res.Content {
		log.Print(c.(*mcp.TextContent).Text)
	}
	return nil
}

func main() {
	ctx := context.Background()

	// The one composition line left in application code: flushing the
	// tracer provider on exit isn't something a compile-time hook can do
	// for you, since it must run after run() returns, not around a single
	// intercepted function call.
	defer func() {
		if err := clienttrace.Shutdown(context.Background()); err != nil {
			log.Printf("otel shutdown error: %v", err)
		}
	}()

	log.Println("Calling mcp server...")
	if err := run(ctx); err != nil {
		log.Fatalf("run failed: %v", err)
	}
}
