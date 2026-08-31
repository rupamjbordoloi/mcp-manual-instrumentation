package main

import (
	"context"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// run contains only business logic.
// mcp.session starts inside (*Client).Connect (BeforeConnect hook).
// mcp.initialize ends inside (*Client).Connect (AfterConnect hook).
// mcp.tools.call wraps (*ClientSession).CallTool (BeforeCallTool/AfterCallTool hooks).
// mcp.shutdown and mcp.session end inside (*ClientSession).Close (BeforeClose/AfterClose hooks).
// Zero OTel imports or calls anywhere in this file.
func run(ctx context.Context) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)

	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://localhost:8080",
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

func main() {
	log.Println("Calling mcp server...")
	if err := run(context.Background()); err != nil {
		log.Printf("run failed: %v", err)
	}
}
