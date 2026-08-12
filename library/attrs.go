package mcptrace

const (
	// ProtocolVersion is the MCP protocol version this service negotiates.
	ProtocolVersion = "2025-06-18"
)

// methodToSpanName maps a JSON-RPC method to its protocol-level span name.
// "initialize" is intentionally handled separately in middleware.go — it
// does not use the generic dispatch path this map otherwise assumes.
var methodToSpanName = map[string]string{
	"notifications/initialized": "mcp.initialized",
	"server/discover":           "mcp.discovery",
	"tools/list":                "mcp.tools.list",
	"tools/call":                "mcp.tools.execute",
	"prompts/get":               "mcp.prompts.get",
	"resources/read":            "mcp.resources.read",
}
