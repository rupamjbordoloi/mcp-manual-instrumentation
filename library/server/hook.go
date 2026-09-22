// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package server

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.opentelemetry.io/otelc/pkg/hook"
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

type serverRequestData struct {
	start   time.Time
	method  string
	httpCtx context.Context
}

var (
	logger          = runtime.Logger()
	tracer          trace.Tracer
	initOnce        sync.Once
	metricsOnce     sync.Once
	requestDuration metric.Float64Histogram
)

func initInstrumentation() {
	initOnce.Do(func() {
		tracer = otel.GetTracerProvider().Tracer(
			instrumentationName,
			trace.WithInstrumentationVersion(runtime.ModuleVersion()),
		)

		logger.Info("MCP server instrumentation initialized")
	})
}

func initMetrics() {
	metricsOnce.Do(func() {
		logger.Info("initMetrics............")
		meter := otel.Meter(
			"go.opentelemetry.io/otelc/instrumentation/net/http/server",
		)

		requestDuration, _ = meter.Float64Histogram(
			"http.server.request.duration",
			metric.WithUnit("s"),
			metric.WithDescription(
				"Duration of HTTP server requests",
			),
		)
	})
}

type mcpServerEnabler struct{}

func (mcpServerEnabler) Enable() bool {
	return runtime.Instrumented(instrumentationKey)
}

var serverEnabler = mcpServerEnabler{}

// finishSpan finishes an OpenTelemetry span.
//
// semanticErrorType is used for failures that are represented in the
// successful protocol result itself, such as CallToolResult.IsError=true.
// It deliberately uses a low-cardinality value rather than recording the
// arbitrary error/result message as an attribute.
func finishSpan(
	span trace.Span,
	err error,
	semanticErrorType string,
) {
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(
			attribute.String(attrErrorType, errorType(err)),
		)
		span.SetStatus(codes.Error, err.Error())
	} else if semanticErrorType != "" {
		span.SetAttributes(
			attribute.String(attrErrorType, semanticErrorType),
		)
		span.SetStatus(codes.Error, semanticErrorType)
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

// errorType returns a low-cardinality error classification.
//
// We intentionally do not expose err.Error() as an attribute. Error messages
// can contain user data, secrets, paths, request contents, or other
// high-cardinality information.
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

// ---- Tool dispatch hook ----

func BeforeCallTool(
	ictx hook.HookContext,
	recv *mcp.Server,
	ctx context.Context,
	req *mcp.CallToolRequest,
) {
	if !serverEnabler.Enable() {
		return
	}

	initInstrumentation()

	toolName := req.Params.Name
	protocolVersion := requestProtocolVersion(req)
	sessionID := requestSessionID(req)

	logger.Debug(
		"BeforeCallTool called",
		"tool",
		toolName,
		"protocol_version",
		protocolVersion,
		"session_id",
		sessionID,
	)

	PrintParentSpan(ctx)

	attributes := []attribute.KeyValue{
		attribute.String(attrMCPMethodName, "tools/call"),
		attribute.String(attrGenAIToolName, toolName),
		attribute.String(attrGenAIOperationName, genAIOperationExecuteTool),

		// Keep the old attribute temporarily for backwards compatibility with
		// existing dashboards built from this instrumentation.
		attribute.String(attrToolNameLegacy, toolName),

		attribute.String(attrRPCSystemName, "jsonrpc"),
		attribute.String(attrRPCMethod, "tools/call"),
	}

	appendMCPContextAttributes(
		&attributes,
		protocolVersion,
		sessionID,
	)

	newCtx, span := tracer.Start(
		ctx,
		"tool."+toolName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attributes...),
	)

	ictx.SetData(
		map[string]any{
			"span": span,
		},
	)

	// Parameter 1 is the context passed to the tool handler.
	ictx.SetParam(1, newCtx)

	logger.Debug(
		"BeforeCallTool",
		"creating_span",
		"tool."+toolName,
		"span_id",
		span.SpanContext().SpanID(),
	)
}

func AfterCallTool(
	ictx hook.HookContext,
	res *mcp.CallToolResult,
	err error,
) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		logger.Debug("AfterCallTool: no span from before hook")
		return
	}

	switch {
	case err != nil:
		span.SetAttributes(
			attribute.Bool(attrToolSuccess, false),
		)

		finishSpan(span, err, "")

	case res != nil && res.IsError:
		// MCP represents a tool execution failure inside a successful
		// JSON-RPC result using isError=true.
		//
		// This should be surfaced as an OpenTelemetry error, but we do not
		// record arbitrary result content because it may contain sensitive
		// data.
		span.SetAttributes(
			attribute.Bool(attrToolSuccess, false),
			attribute.String(attrErrorType, toolErrorType),
		)

		finishSpan(span, nil, toolErrorType)

	default:
		span.SetAttributes(
			attribute.Bool(attrToolSuccess, true),
		)

		finishSpan(span, nil, "")
	}

	logger.Debug("AfterCallTool completed")
}

// ---- Protocol middleware ----

func AfterNewServer(
	ictx hook.HookContext,
	s *mcp.Server,
) {
	if !serverEnabler.Enable() {
		return
	}

	initInstrumentation()

	logger.Debug(
		"AfterNewServer: installing protocol middleware",
	)

	s.AddReceivingMiddleware(serverProtocolMiddleware)
}

// serverProtocolMiddleware creates one span per meaningful inbound MCP method.
//
// Notifications that do not have meaningful server-side work are intentionally
// not instrumented here.
//
// The MCP SDK may process multiple JSON-RPC messages over the same HTTP
// connection/request. Each MCP method gets its own span and is parented to
// the HTTP server span when the HTTP trace context can be recovered.
//
// Tool calls use the current MCP semantic-convention shape:
//
//	tools/call <tool>
//
// with:
//
//	mcp.method.name       = tools/call
//	gen_ai.tool.name      = <tool>
//	gen_ai.operation.name = execute_tool
//
// The HTTP span remains available underneath the MCP transport boundary.
func serverProtocolMiddleware(
	next mcp.MethodHandler,
) mcp.MethodHandler {
	return func(
		ctx context.Context,
		method string,
		req mcp.Request,
	) (mcp.Result, error) {
		if !serverEnabler.Enable() {
			return next(ctx, method, req)
		}

		initInstrumentation()

		if isNotification(method) {
			return next(ctx, method, req)
		}

		// IMPORTANT:
		//
		// In stateful Streamable HTTP, ctx can belong to the persistent MCP
		// session rather than the current HTTP POST.
		//
		// Recover the current HTTP server span from the request metadata
		// injected by BeforeStreamableHTTP.
		if extra := req.GetExtra(); extra != nil && extra.Header != nil {
			if traceparent := extra.Header.Get(httpSpanContextHeader); traceparent != "" {
				carrier := propagation.MapCarrier{
					"traceparent": traceparent,
				}

				ctx = otel.GetTextMapPropagator().Extract(
					ctx,
					carrier,
				)

				// The HTTP server span was created locally by net/http
				// instrumentation, so it is not a remote parent.
				if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
					ctx = trace.ContextWithSpanContext(
						ctx,
						sc.WithRemote(false),
					)
				}
			}
		}

		protocolVersion := requestProtocolVersion(req)
		sessionID := requestSessionID(req)
		toolName := requestToolName(req, method)

		spanName := methodToSpanName(
			method,
			toolName,
		)

		attributes := []attribute.KeyValue{
			attribute.String(attrMCPMethodName, method),
			attribute.String(attrRPCSystemName, "jsonrpc"),
			attribute.String(attrRPCMethod, method),
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
			)
		}

		// HTTP RequestExtra is populated by the Streamable HTTP transport.
		// We can therefore safely identify HTTP transport here without
		// guessing for other/custom transports.
		if isHTTPMCPRequest(req) {
			attributes = append(
				attributes,
				attribute.String(
					attrNetworkTransport,
					"tcp",
				),
				attribute.String(
					attrNetworkProtocolName,
					"http",
				),
			)
		}

		ctx, methodSpan := tracer.Start(
			ctx,
			spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attributes...),
		)

		logger.Debug(
			"serverProtocolMiddleware",
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

		switch method {
		case "initialize":
			methodSpan.AddEvent("protocol.validate")
			methodSpan.AddEvent("capability.negotiation")

		case "server/discover":
			methodSpan.AddEvent("capability.enumeration")
		}

		result, err := next(
			ctx,
			method,
			req,
		)

		switch {
		case err != nil:
			finishSpan(
				methodSpan,
				err,
				"",
			)

		case isToolResultError(result):
			// CallToolResult.IsError=true is a tool-level failure encoded
			// inside an otherwise successful JSON-RPC response.
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
		logger.Debug("is recording...", "method", method, "span IsRecording", methodSpan.IsRecording())

		return result, err
	}
}

// appendMCPContextAttributes adds protocol/session context shared by MCP
// operation spans.
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
			attribute.String(
				attrMCPProtocolVersion,
				protocolVersion,
			),
		)
	}

	if shouldRecordSessionID(protocolVersion) && sessionID != "" {
		*attributes = append(
			*attributes,
			attribute.String(
				attrMCPSessionID,
				sessionID,
			),
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
func requestToolName(
	req mcp.Request,
	method string,
) string {
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
func methodToSpanName(
	method string,
	toolName string,
) string {
	if method == "tools/call" && toolName != "" {
		return "tools/call " + toolName
	}

	return method
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
// notification for which this middleware intentionally avoids creating a
// server operation span.
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

func PrintParentSpan(ctx context.Context) {
	spanContext := trace.SpanContextFromContext(ctx)

	if spanContext.IsValid() {
		parentSpanID := spanContext.SpanID().String()
		traceID := spanContext.TraceID().String()

		logger.Debug(
			"PrintParentSpan",
			"parent_span_id",
			parentSpanID,
		)

		logger.Debug(
			"PrintParentSpan",
			"trace_id",
			traceID,
		)
	} else {
		logger.Debug(
			"No valid parent span found in the context (this will be a root span).",
		)
	}
}

// BeforeStreamableHTTP bridges the current HTTP server span into the MCP
// request context.
//
// In stateful Streamable HTTP, the MCP SDK may use a persistent session
// context for subsequent JSON-RPC messages. The HTTP server span, however,
// belongs to the individual HTTP request.
//
// We therefore copy the current HTTP traceparent into RequestExtra metadata
// via the private bridge header. serverProtocolMiddleware extracts it again
// before creating the MCP server span.
//
// Keep the request context itself unchanged.
func BeforeStreamableHTTP(
	ictx hook.HookContext,
	recv *mcp.StreamableServerTransport,
	w http.ResponseWriter,
	req *http.Request,
) {
	if !serverEnabler.Enable() {
		return
	}

	initInstrumentation()

	initMetrics()

	data := &serverRequestData{
		start:   time.Now(),
		method:  req.Method,
		httpCtx: req.Context(),
	}
	fmt.Println(data)

	ictx.SetData(data)
	logger.Info("BeforeStreamableHTTP............")

	// Only bridge POST requests. GET is the long-lived streaming transport
	// connection and should not be used as the parent for individual MCP
	// operations.
	if req.Method != http.MethodPost {
		return
	}

	// This context contains the HTTP server span created by the net/http
	// auto-instrumentation.
	httpCtx := req.Context()

	sc := trace.SpanContextFromContext(httpCtx)
	if !sc.IsValid() {
		logger.Debug(
			"BeforeStreamableHTTP: no valid HTTP server span",
		)
		return
	}

	// Serialize the CURRENT HTTP server span context into the private
	// request header. The MCP SDK copies this header into RequestExtra.Header
	// for the corresponding JSON-RPC request.
	carrier := propagation.MapCarrier{}

	otel.GetTextMapPropagator().Inject(
		httpCtx,
		carrier,
	)

	traceparent := carrier.Get("traceparent")
	if traceparent == "" {
		return
	}

	req.Header.Set(
		httpSpanContextHeader,
		traceparent,
	)

	// Keep the request context unchanged.
	//
	// The important part is the per-HTTP-request span context stored in
	// RequestExtra.Header.
	ictx.SetParam(
		2,
		req,
	)

	logger.Debug(
		"BeforeStreamableHTTP: bridged HTTP trace context",
		"trace_id",
		sc.TraceID(),
		"span_id",
		sc.SpanID(),
	)
}

func AfterStreamableHTTP(ictx hook.HookContext, mcpHandler *mcp.StreamableHTTPHandler) {
	_ = otelhttp.NewHandler(
		mcpHandler,
		"mcp",
		otelhttp.WithTracerProvider(noop.NewTracerProvider()),
	)
	// return instrumentedHandler

}
