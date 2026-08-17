package server

const ProtocolVersion = "2025-06-18"

var methodToSpanName = map[string]string{
	"notifications/initialized": "mcp.initialized",
	"tools/list":                "mcp.tools.list",
	"tools/call":                "mcp.tools.execute",
	"prompts/get":               "mcp.prompts.get",
	"resources/read":            "mcp.resources.read",
}

var protocolLevelMethods = map[string]bool{
	"initialize":      true,
	"server/discover": true,
}
