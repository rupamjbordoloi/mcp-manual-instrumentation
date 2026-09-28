// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package server

import (
	"reflect"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otelc/pkg/runtime"
)

const (
	// OpenTelemetry MCP / JSON-RPC semantic-convention attributes.
	attrMCPMethodName       = "mcp.method.name"
	attrMCPProtocolVersion  = "mcp.protocol.version"
	attrMCPSessionID        = "mcp.session.id"
	attrGenAIToolName       = "gen_ai.tool.name"
	attrGenAIOperationName  = "gen_ai.operation.name"
	attrErrorType           = "error.type"
	attrRPCSystemName       = "rpc.system.name"
	attrRPCMethod           = "rpc.method"
	attrToolNameLegacy      = "tool.name"
	attrToolSuccess         = "tool.success"
	attrNetworkTransport    = "network.transport"
	attrNetworkProtocolName = "network.protocol.name"

	// Current GenAI semantic convention value for MCP tools/call.
	genAIOperationExecuteTool = "execute_tool"

	// MCP tool errors are represented inside a successful JSON-RPC result
	// using CallToolResult.IsError=true.
	toolErrorType = "tool_error"

	// MCP 2026-07-28 introduced the stateless lifecycle. Protocol-level
	// sessions are not part of that lifecycle.
	statelessMCPProtocolVersion = "2026-07-28"
)

var logger = runtime.Logger()

type mcpServerEnabler struct{}

func (mcpServerEnabler) Enable() bool {
	return runtime.Instrumented(instrumentationKey)
}

var serverEnabler = mcpServerEnabler{}

// errorType returns a low-cardinality error classification.
//
// We intentionally do not expose err.Error() as an attribute. Error messages
// can contain user data, secrets, paths, request contents, or other
// high-cardinality information. Shared by span error attributes (hook_traces.go)
// and metric error.type attributes (hook_metrics.go).
func errorType(err error) string {
	if err == nil {
		return ""
	}

	t := reflect.TypeOf(err)
	if t == nil {
		return "_OTHER"
	}

	// Preserve the useful concrete Go error type while avoiding a potentially
	// enormous error-message cardinality.
	return t.String()
}

// isToolResultError reports whether an MCP method result represents a tool
// execution error inside the JSON-RPC result payload.
func isToolResultError(result mcp.Result) bool {
	if result == nil {
		return false
	}

	callResult, ok := result.(*mcp.CallToolResult)
	return ok && callResult != nil && callResult.IsError
}

// requestProtocolVersion obtains the MCP protocol version without relying on
// private fields from the SDK.
//
// Current versions of go-sdk expose ProtocolVersion() on ServerRequest.
// The interface assertion keeps this instrumentation loosely coupled to the
// concrete generic request type.
func requestProtocolVersion(req mcp.Request) string {
	if req == nil {
		return ""
	}

	type protocolVersionProvider interface {
		ProtocolVersion() string
	}

	if provider, ok := req.(protocolVersionProvider); ok {
		return provider.ProtocolVersion()
	}

	// Legacy initialize requests carry the protocol version directly in
	// InitializeParams.
	if params := req.GetParams(); params != nil {
		if initializeParams, ok := params.(*mcp.InitializeParams); ok &&
			initializeParams != nil {
			return initializeParams.ProtocolVersion
		}
	}

	return ""
}

// requestSessionID obtains the MCP session ID when the SDK exposes one.
//
// We only record it for protocol versions that actually use protocol-level
// sessions; see shouldRecordSessionID.
func requestSessionID(req mcp.Request) string {
	if req == nil {
		return ""
	}

	session := req.GetSession()
	if session == nil {
		return ""
	}

	return session.ID()
}

// requestToolName extracts the tool name only for tools/call.
//
// The previous implementation incorrectly recorded:
//
//	mcp.tool.name = initialize
//	mcp.tool.name = server/discover
//	mcp.tool.name = tools/list
//
// which is semantically incorrect. A tool name exists only for a tool call.
func requestToolName(req mcp.Request, method string) string {
	if method != "tools/call" || req == nil {
		return ""
	}

	params := req.GetParams()
	if params == nil {
		return ""
	}

	callParams, ok := params.(*mcp.CallToolParamsRaw)
	if !ok || callParams == nil {
		return ""
	}

	return callParams.Name
}

// methodToSpanName follows the MCP semantic-convention span naming rule:
//
//	{mcp.method.name} {target}
//
// For tools/call the target is the tool name. For methods without a
// low-cardinality target, the method name alone is used.
func methodToSpanName(method string, toolName string) string {
	if method == "tools/call" && toolName != "" {
		return "tools/call " + toolName
	}

	return method
}

// appendMCPContextAttributes adds protocol/session context shared by MCP
// operation spans and metrics.
//
// mcp.session.id is deliberately omitted for the 2026-07-28 stateless
// lifecycle because that protocol revision removed the protocol-level
// session model.
func appendMCPContextAttributes(
	attributes *[]attribute.KeyValue,
	protocolVersion string,
	sessionID string,
) {
	if attributes == nil {
		return
	}

	if protocolVersion != "" {
		*attributes = append(
			*attributes,
			attribute.String(attrMCPProtocolVersion, protocolVersion),
		)
	}

	if shouldRecordSessionID(protocolVersion) && sessionID != "" {
		*attributes = append(
			*attributes,
			attribute.String(attrMCPSessionID, sessionID),
		)
	}
}

// shouldRecordSessionID reports whether the negotiated MCP version uses the
// legacy/session-oriented lifecycle.
//
// MCP 2026-07-28 introduced a stateless lifecycle. The current MCP semantic
// conventions explicitly make mcp.session.id conditional on the request
// actually being part of a session.
func shouldRecordSessionID(protocolVersion string) bool {
	if protocolVersion == "" {
		// If the protocol version is unavailable, preserve the existing
		// behavior rather than assuming stateless mode.
		return true
	}

	return protocolVersion != statelessMCPProtocolVersion
}

// isHTTPMCPRequest identifies Streamable HTTP requests using the public
// RequestExtra HTTP header field supplied by the MCP SDK.
func isHTTPMCPRequest(req mcp.Request) bool {
	if req == nil {
		return false
	}

	extra := req.GetExtra()
	return extra != nil && extra.Header != nil
}

// isNotification reports whether the given JSON-RPC method is a one-way
// notification for which serverProtocolMiddleware intentionally avoids
// creating a server operation span or recording mcp.server.operation.duration.
//
// Notifications are fire-and-forget and do not have a normal response
// lifecycle to measure here.
func isNotification(method string) bool {
	switch method {
	case "notifications/initialized",
		"notifications/roots/list_changed",
		"notifications/progress",
		"notifications/cancelled":
		return true
	}

	return false
}
