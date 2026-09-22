// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package client

import (
	"context"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook"
	"go.opentelemetry.io/otelc/pkg/runtime"
)

const (
	instrumentationName = "go.opentelemetry.io/otelc/instrumentation/mcp/client"
	instrumentationKey  = "MCP_CLIENT"

	// OpenTelemetry MCP / JSON-RPC semantic-convention attributes.
	attrMCPMethodName      = "mcp.method.name"
	attrMCPProtocolVersion = "mcp.protocol.version"
	attrMCPSessionID       = "mcp.session.id"

	attrGenAIToolName      = "gen_ai.tool.name"
	attrGenAIOperationName = "gen_ai.operation.name"

	attrRPCSystemName = "rpc.system.name"
	attrRPCMethod     = "rpc.method"

	// Retained temporarily for backwards compatibility with dashboards that
	// were created from the previous instrumentation.
	attrToolNameLegacy = "mcp.tool.name"

	attrToolSuccess = "tool.success"
	attrErrorType   = "error.type"

	// Current GenAI semantic-convention value for MCP tools/call.
	genAIOperationExecuteTool = "execute_tool"

	// Tool errors represented by CallToolResult.IsError=true.
	//
	// This is intentionally a low-cardinality value. Do not put the actual
	// tool error message into an attribute.
	toolErrorType = "tool_error"

	// MCP 2026-07-28 removed the protocol-level session lifecycle.
	statelessMCPProtocolVersion = "2026-07-28"

	// Reserved _meta key used by modern MCP requests.
	mcpProtocolVersionMetaKey = "io.modelcontextprotocol/protocolVersion"
)

var (
	logger   = runtime.Logger()
	tracer   trace.Tracer
	initOnce sync.Once
)

func initInstrumentation() {
	initOnce.Do(func() {
		tracer = otel.GetTracerProvider().Tracer(
			instrumentationName,
			trace.WithInstrumentationVersion(runtime.ModuleVersion()),
		)

		logger.Info("MCP client instrumentation initialized")
	})
}

type mcpClientEnabler struct{}

func (mcpClientEnabler) Enable() bool {
	return runtime.Instrumented(instrumentationKey)
}

var clientEnabler = mcpClientEnabler{}

// sessionParentCtx maps *mcp.ClientSession -> sessionEntry.
//
// It is populated after Connect succeeds and is used by the client protocol
// middleware for post-connect operations.
//
// The session span is the logical root for the MCP client-side lifecycle:
//
//	mcp.session
//	   ├── initialize
//	   ├── tools/call greet
//	   ├── tools/list
//	   └── mcp.shutdown
//
// IMPORTANT:
// The session span context is kept LOCAL, not REMOTE.
//
// The HTTP client instrumentation creates a child span from the MCP operation
// span. The server then receives that HTTP client span as a remote parent.
var sessionParentCtx sync.Map // map[*mcp.ClientSession]sessionEntry

type sessionEntry struct {
	span            trace.Span
	spanContext     trace.SpanContext
	protocolVersion string
	sessionID       string
}

// finishSpan completes an OpenTelemetry span.
//
// errorTypeValue is used for protocol-level or semantic errors where there
// may be no Go error object, such as CallToolResult.IsError=true.
//
// We deliberately do not store arbitrary error strings as attributes because
// they can contain sensitive data and create high-cardinality telemetry.
func finishSpan(
	span trace.Span,
	err error,
	errorTypeValue string,
) {
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(
			attribute.String(attrErrorType, errorType(err)),
		)
		span.SetStatus(codes.Error, err.Error())
	} else if errorTypeValue != "" {
		span.SetAttributes(
			attribute.String(attrErrorType, errorTypeValue),
		)
		span.SetStatus(codes.Error, errorTypeValue)
	} else {
		span.SetStatus(codes.Ok, "")
	}

	spanID := span.SpanContext().SpanID()

	span.End()

	logger.Debug(
		"finishSpan",
		"span_id",
		spanID,
	)
}

// errorType intentionally returns a low-cardinality classification.
//
// Do not use err.Error() as an attribute.
func errorType(err error) string {
	if err == nil {
		return ""
	}

	return "mcp_error"
}

// ---- NewClient hook ----

func AfterNewClient(
	ictx hook.HookContext,
	c *mcp.Client,
) {
	if !clientEnabler.Enable() {
		return
	}

	initInstrumentation()

	logger.Debug(
		"AfterNewClient: installing protocol middleware",
	)

	c.AddSendingMiddleware(clientProtocolMiddleware)
}

// clientProtocolMiddleware creates one client span per outbound MCP method.
//
// During Connect:
//
//	mcp.session
//	   ├── server/discover
//	   ├── initialize
//	   └── notifications/initialized
//
// After Connect:
//
//	mcp.session
//	   ├── tools/call greet
//	   ├── tools/list
//	   ├── resources/read
//	   └── ...
//
// The session span is used as a LOCAL parent.
//
// This is intentional. The subsequent HTTP client span becomes a child of the
// MCP operation span. When the HTTP request reaches the MCP server,
// net/http server instrumentation extracts the HTTP client span context as
// the remote parent.
//
// This produces the desired distributed trace:
//
//	client MCP span
//	    ↓
//	HTTP client span
//	    ↓
//	HTTP server span
//	    ↓
//	server MCP span
//	    ↓
//	tool span
func clientProtocolMiddleware(
	next mcp.MethodHandler,
) mcp.MethodHandler {
	return func(
		ctx context.Context,
		method string,
		req mcp.Request,
	) (mcp.Result, error) {
		if !clientEnabler.Enable() {
			return next(ctx, method, req)
		}

		initInstrumentation()

		// Resolve the ClientSession associated with the outbound request.
		var (
			sessionEntryValue sessionEntry
			hasSessionEntry   bool
		)

		if session := req.GetSession(); session != nil {
			if clientSession, ok := session.(*mcp.ClientSession); ok {
				if value, ok := sessionParentCtx.Load(clientSession); ok {
					if entry, ok := value.(sessionEntry); ok {
						sessionEntryValue = entry
						hasSessionEntry = true
					}
				}
			}
		}

		// IMPORTANT:
		//
		// Use the session span as a LOCAL parent, but ONLY for the methods
		// that make up the connect-time handshake (server/discover,
		// initialize, notifications/initialized).
		//
		// Every operation issued after Connect has already returned (
		// tools/call, tools/list, resources/read, ...) is a call the
		// application makes explicitly with its own context, which may
		// carry a meaningful, unrelated parent span (e.g. one HTTP request
		// out of many served over the lifetime of a single long-lived
		// session). Forcibly repinning that context onto the original
		// mcp.session span would silently discard the caller's context and
		// collapse every subsequent call back into one trace, regardless
		// of when or why it was made.
		//
		// The previous implementation used:
		//
		//     trace.ContextWithRemoteSpanContext(...)
		//
		// That caused the client MCP operation span to have a remote parent
		// even though both spans were created by the same process.
		//
		// This version keeps the client-side hierarchy local, and scopes it
		// to the handshake only.
		if hasSessionEntry &&
			sessionEntryValue.spanContext.IsValid() &&
			isSessionLifecycleMethod(method) {
			ctx = trace.ContextWithSpanContext(
				ctx,
				sessionEntryValue.spanContext.WithRemote(false),
			)
		}

		toolName := requestToolName(
			method,
			req,
		)

		protocolVersion := requestProtocolVersion(
			method,
			req,
			sessionEntryValue,
			hasSessionEntry,
		)

		sessionID := requestSessionID(
			req,
			sessionEntryValue,
			hasSessionEntry,
		)

		spanName := clientMethodToSpanName(
			method,
			toolName,
		)

		attributes := []attribute.KeyValue{
			attribute.String(
				attrMCPMethodName,
				method,
			),
			attribute.String(
				attrRPCSystemName,
				"jsonrpc",
			),
			attribute.String(
				attrRPCMethod,
				method,
			),
		}

		appendMCPContextAttributes(
			&attributes,
			protocolVersion,
			sessionID,
		)

		if method == "tools/call" && toolName != "" {
			attributes = append(
				attributes,
				attribute.String(
					attrGenAIToolName,
					toolName,
				),
				attribute.String(
					attrGenAIOperationName,
					genAIOperationExecuteTool,
				),

				// Backwards compatibility with the previous instrumentation.
				attribute.String(
					attrToolNameLegacy,
					toolName,
				),
			)
		}

		ctx, methodSpan := tracer.Start(
			ctx,
			spanName,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attributes...),
		)

		logger.Debug(
			"clientProtocolMiddleware",
			"created_span",
			spanName,
			"span_id",
			methodSpan.SpanContext().SpanID(),
			"method",
			method,
			"tool",
			toolName,
			"protocol_version",
			protocolVersion,
			"session_id",
			sessionID,
		)

		result, err := next(
			ctx,
			method,
			req,
		)

		switch {
		case err != nil:
			// JSON-RPC / MCP protocol-level failure.
			finishSpan(
				methodSpan,
				err,
				"",
			)

		case method == "tools/call" &&
			isToolResultError(result):
			// MCP tool-level failure represented inside the successful
			// CallToolResult payload.
			finishSpan(
				methodSpan,
				nil,
				toolErrorType,
			)

		default:
			finishSpan(
				methodSpan,
				nil,
				"",
			)
		}

		logger.Debug(
			"clientProtocolMiddleware",
			"ending",
			method,
		)

		return result, err
	}
}

// requestToolName extracts the tool name only for tools/call.
//
// Do NOT attach:
//
//	mcp.tool.name = initialize
//	mcp.tool.name = tools/list
//
// A tool name exists only for a tools/call operation.
func requestToolName(
	method string,
	req mcp.Request,
) string {
	if method != "tools/call" || req == nil {
		return ""
	}

	if callRequest, ok := req.(*mcp.CallToolRequest); ok &&
		callRequest != nil {
		return callRequest.Params.Name
	}

	if params := req.GetParams(); params != nil {
		if callParams, ok := params.(*mcp.CallToolParams); ok &&
			callParams != nil {
			return callParams.Name
		}
	}

	return ""
}

// requestProtocolVersion determines the protocol version associated with the
// outbound MCP operation.
//
// Precedence:
//
//  1. Per-request _meta protocol version.
//     This is the authoritative source for modern 2026-07-28 style requests.
//  2. initialize.params.protocolVersion.
//  3. Negotiated ClientSession.InitializeResult().ProtocolVersion stored in
//     sessionEntry.
//
// This makes the instrumentation work with both the legacy session lifecycle
// and the modern per-request lifecycle.
func requestProtocolVersion(
	method string,
	req mcp.Request,
	entry sessionEntry,
	hasEntry bool,
) string {
	if req != nil {
		if params := req.GetParams(); params != nil {
			if meta := params.GetMeta(); meta != nil {
				if value, ok := meta[mcpProtocolVersionMetaKey]; ok {
					if version, ok := value.(string); ok && version != "" {
						return version
					}
				}
			}

			// Legacy initialize carries the proposed protocol version in
			// initialize.params.protocolVersion.
			if method == "initialize" {
				if initializeParams, ok := params.(*mcp.InitializeParams); ok &&
					initializeParams != nil &&
					initializeParams.ProtocolVersion != "" {
					return initializeParams.ProtocolVersion
				}
			}
		}
	}

	if hasEntry && entry.protocolVersion != "" {
		return entry.protocolVersion
	}

	return ""
}

// requestSessionID obtains the MCP session ID.
//
// The session ID is recorded only for legacy/session-based MCP. The
// 2026-07-28 protocol removed the protocol-level session model.
func requestSessionID(
	req mcp.Request,
	entry sessionEntry,
	hasEntry bool,
) string {
	if hasEntry &&
		shouldRecordSessionID(entry.protocolVersion) &&
		entry.sessionID != "" {
		return entry.sessionID
	}

	if req != nil {
		if session := req.GetSession(); session != nil {
			sessionID := session.ID()

			if shouldRecordSessionID(entry.protocolVersion) {
				return sessionID
			}
		}
	}

	return ""
}

// appendMCPContextAttributes adds shared MCP semantic attributes.
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
			attribute.String(
				attrMCPProtocolVersion,
				protocolVersion,
			),
		)
	}

	if shouldRecordSessionID(protocolVersion) &&
		sessionID != "" {
		*attributes = append(
			*attributes,
			attribute.String(
				attrMCPSessionID,
				sessionID,
			),
		)
	}
}

// shouldRecordSessionID determines whether the protocol version represents a
// session-oriented MCP lifecycle.
//
// MCP 2026-07-28 removed initialize/initialized and MCP-Session-Id from the
// protocol. Legacy versions use protocol-level sessions.
func shouldRecordSessionID(protocolVersion string) bool {
	if protocolVersion == "" {
		// If the version is not known, preserve compatibility with the
		// existing session-based SDK behavior.
		return true
	}

	return protocolVersion != statelessMCPProtocolVersion
}

// isSessionLifecycleMethod reports whether method is part of the initial
// MCP handshake performed inside Connect.
//
// Only these methods should be force-parented onto the long-lived
// mcp.session span. Everything else is issued after Connect has already
// returned and must respect whatever context the caller passed to it.
func isSessionLifecycleMethod(method string) bool {
	switch method {
	case "server/discover",
		"initialize",
		"notifications/initialized":
		return true
	}

	return false
}

// isToolResultError reports whether an MCP tools/call result represents a
// tool execution failure encoded inside a successful JSON-RPC result.
func isToolResultError(result mcp.Result) bool {
	if result == nil {
		return false
	}

	callResult, ok := result.(*mcp.CallToolResult)
	return ok &&
		callResult != nil &&
		callResult.IsError
}

// clientMethodToSpanName follows the MCP operation span naming rule.
//
// For tools/call:
//
//	tools/call <tool>
//
// For other operations:
//
//	<mcp.method.name>
func clientMethodToSpanName(
	method string,
	toolName string,
) string {
	if method == "tools/call" && toolName != "" {
		return "tools/call " + toolName
	}

	return method
}

// ---- Connect hooks ----
//
// BeforeConnect creates exactly one mcp.session span.
//
// Protocol operation spans are created by clientProtocolMiddleware:
//
//	server/discover
//	initialize
//	notifications/initialized
//
// This avoids duplicate initialize/discovery spans.
//
// Param indices:
//
//	recv=0
//	ctx=1
//	t=2
//	opts=3
func BeforeConnect(
	ictx hook.HookContext,
	recv *mcp.Client,
	ctx context.Context,
	t mcp.Transport,
	opts *mcp.ClientSessionOptions,
) {
	if !clientEnabler.Enable() {
		return
	}

	initInstrumentation()

	logger.Debug(
		"BeforeConnect called",
	)

	sessionCtx, sessionSpan := tracer.Start(
		ctx,
		"mcp.session",
		trace.WithSpanKind(trace.SpanKindClient),
	)

	logger.Debug(
		"creating mcp.session span",
		"span_id",
		sessionSpan.SpanContext().SpanID(),
	)

	ictx.SetData(
		map[string]interface{}{
			"sessionSpan": sessionSpan,
			"sessionCtx":  sessionCtx,
		},
	)

	// Thread the session context into the SDK Connect call so that all
	// protocol operations generated during connection inherit mcp.session.
	ictx.SetParam(
		1,
		sessionCtx,
	)
}

func AfterConnect(
	ictx hook.HookContext,
	session *mcp.ClientSession,
	err error,
) {
	sessionSpan, ok := ictx.GetKeyData("sessionSpan").(trace.Span)
	if !ok || sessionSpan == nil {
		logger.Debug(
			"AfterConnect: no sessionSpan from before hook",
		)
		return
	}

	if err != nil {
		finishSpan(
			sessionSpan,
			err,
			"",
		)

		logger.Debug(
			"AfterConnect called with error",
			"error",
			err,
		)

		return
	}

	if session == nil {
		finishSpan(
			sessionSpan,
			nil,
			"",
		)

		logger.Debug(
			"AfterConnect: nil session",
		)

		return
	}

	protocolVersion := ""
	if initializeResult := session.InitializeResult(); initializeResult != nil {
		protocolVersion = initializeResult.ProtocolVersion
	}

	sessionID := session.ID()

	// Add negotiated protocol/session information to the already-running
	// session span.
	if protocolVersion != "" {
		sessionSpan.SetAttributes(
			attribute.String(
				attrMCPProtocolVersion,
				protocolVersion,
			),
		)
	}

	if shouldRecordSessionID(protocolVersion) &&
		sessionID != "" {
		sessionSpan.SetAttributes(
			attribute.String(
				attrMCPSessionID,
				sessionID,
			),
		)
	}

	sessionParentCtx.Store(
		session,
		sessionEntry{
			span:            sessionSpan,
			spanContext:     sessionSpan.SpanContext(),
			protocolVersion: protocolVersion,
			sessionID:       sessionID,
		},
	)

	logger.Debug(
		"AfterConnect completed",
		"protocol_version",
		protocolVersion,
		"session_id",
		sessionID,
	)
}

// ---- Close hooks ----

func BeforeClose(
	ictx hook.HookContext,
	recv *mcp.ClientSession,
) {
	if !clientEnabler.Enable() {
		return
	}

	initInstrumentation()

	val, ok := sessionParentCtx.Load(recv)
	if !ok {
		logger.Debug(
			"BeforeClose: no session entry",
		)
		return
	}

	sessionParentCtx.Delete(recv)

	entry, ok := val.(sessionEntry)
	if !ok {
		logger.Debug(
			"BeforeClose: invalid session entry",
		)
		return
	}

	// The shutdown span is a local child of mcp.session.
	//
	// Do NOT use ContextWithRemoteSpanContext here. This is still the same
	// client process and therefore a local relationship.
	shutdownCtx := trace.ContextWithSpanContext(
		context.Background(),
		entry.spanContext.WithRemote(false),
	)

	_, shutdownSpan := tracer.Start(
		shutdownCtx,
		"mcp.shutdown",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(
				attrMCPMethodName,
				"shutdown",
			),
			attribute.String(
				attrRPCSystemName,
				"jsonrpc",
			),
			attribute.String(
				attrRPCMethod,
				"shutdown",
			),
		),
	)

	appendMCPContextToSpan(
		shutdownSpan,
		entry.protocolVersion,
		entry.sessionID,
	)

	logger.Debug(
		"creating mcp.shutdown span",
		"span_id",
		shutdownSpan.SpanContext().SpanID(),
	)

	ictx.SetData(
		map[string]interface{}{
			"shutdownSpan": shutdownSpan,
			"sessionSpan":  entry.span,
		},
	)
}

func AfterClose(
	ictx hook.HookContext,
	err error,
) {
	shutdownSpan, ok := ictx.GetKeyData("shutdownSpan").(trace.Span)
	if !ok || shutdownSpan == nil {
		logger.Debug(
			"AfterClose: no shutdown span",
		)
		return
	}

	finishSpan(
		shutdownSpan,
		err,
		"",
	)

	// mcp.session remains open until the logical session has completely
	// closed, so it ends after mcp.shutdown.
	if sessionSpan, ok := ictx.GetKeyData("sessionSpan").(trace.Span); ok && sessionSpan != nil {
		finishSpan(
			sessionSpan,
			err,
			"",
		)
	}
}

// appendMCPContextToSpan adds protocol/session attributes directly to an
// existing span.
func appendMCPContextToSpan(
	span trace.Span,
	protocolVersion string,
	sessionID string,
) {
	if span == nil {
		return
	}

	if protocolVersion != "" {
		span.SetAttributes(
			attribute.String(
				attrMCPProtocolVersion,
				protocolVersion,
			),
		)
	}

	if shouldRecordSessionID(protocolVersion) &&
		sessionID != "" {
		span.SetAttributes(
			attribute.String(
				attrMCPSessionID,
				sessionID,
			),
		)
	}
}

// ---- Process shutdown ----

func AfterMain(
	ictx hook.HookContext,
) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	if err := runtime.Shutdown(ctx); err != nil {
		logger.Error(
			"error flushing telemetry during shutdown",
			"error",
			err,
		)
	} else {
		logger.Info(
			"OpenTelemetry SDK shutdown completed successfully",
		)
	}
}
