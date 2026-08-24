package main

import (
	"context"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// run contains only business logic. Every span — mcp.session, mcp.initialize,
// mcp.tools.call, mcp.deserialize_response, mcp.shutdown — is injected by
// otelc rules targeting the mcp package and main.run. Zero OTel imports here.
func run(ctx context.Context) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)

	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://localhost:8080/mcp",
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
		return http.ErrHandlerTimeout
	}
	for _, c := range res.Content {
		log.Print(c.(*mcp.TextContent).Text)
	}
	return nil
}

// main has zero OTel code — AfterMain flush, mcp.session root span, and all
// protocol spans are injected entirely by otelc rules at compile time.
func main() {
	log.Println("Calling mcp server...")
	if err := run(context.Background()); err != nil {
		log.Printf("run failed: %v", err)
	}
}
