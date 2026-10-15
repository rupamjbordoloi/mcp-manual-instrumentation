package server

const ProtocolVersion = "2025-06-18"

const (
	instrumentationName = "go.opentelemetry.io/otelc/instrumentation/mcp/server"
	instrumentationKey  = "MCP"
)

// httpSpanContextHeader is the private RequestExtra.Header key used to
// bridge the HTTP server span from BeforeStreamableHTTP into
// serverProtocolMiddleware. See hook.go.
const httpSpanContextHeader = "X-OTel-MCP-HTTP-Span"
