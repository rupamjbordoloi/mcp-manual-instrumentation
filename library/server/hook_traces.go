// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package server

import (
	"context"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otelc/pkg/hook"
	"go.opentelemetry.io/otelc/pkg/runtime"
)

var (
	tracerOnce sync.Once
	tracer     trace.Tracer
)

func initTracing() {
	tracerOnce.Do(func() {
		tracer = otel.GetTracerProvider().Tracer(instrumentationName, trace.WithInstrumentationVersion(runtime.ModuleVersion()))
		logger.Info("MCP server trace instrumentation initialized")
	})
}

// finishSpan finishes an OpenTelemetry span.
//
// semanticErrorType is used for failures that are represented in the
// successful protocol result itself, such as CallToolResult.IsError=true.
// It deliberately uses a low-cardinality value rather than recording the
// arbitrary error/result message as an attribute.
func finishSpan(span trace.Span, err error, semanticErrorType string) {
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

	logger.Debug("finishSpan", "span_id", spanID)
}

func PrintParentSpan(ctx context.Context) {
	spanContext := trace.SpanContextFromContext(ctx)

	if spanContext.IsValid() {
		parentSpanID := spanContext.SpanID().String()
		traceID := spanContext.TraceID().String()

		logger.Debug("PrintParentSpan", "parent_span_id", parentSpanID)

		logger.Debug("PrintParentSpan", "trace_id", traceID)
	} else {
		logger.Debug(
			"No valid parent span found in the context (this will be a root span).",
		)
	}
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

	initTracing()

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

	appendMCPContextAttributes(&attributes, protocolVersion, sessionID)

	newCtx, span := tracer.Start(
		ctx,
		"tool."+toolName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attributes...),
	)

	// req.Params.Arguments is json.RawMessage: the exact wire bytes of the
	// call's arguments, with no marshaling needed to size it.
	requestSize := int64(len(req.Params.Arguments))

	ictx.SetData(
		map[string]any{
			"span": span,
			"tool": beginToolObservation(toolName, requestSize),
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

func AfterCallTool(ictx hook.HookContext, res *mcp.CallToolResult, err error) {
	span, ok := ictx.GetKeyData("span").(trace.Span)
	if !ok || span == nil {
		logger.Debug("AfterCallTool: no span from before hook")
		return
	}

	// Record with the tool span in ctx so metric exemplars link to the trace.
	if obs, ok := ictx.GetKeyData("tool").(toolObservation); ok {
		obs.end(trace.ContextWithSpan(context.Background(), span), res, err)
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

func AfterNewServer(ictx hook.HookContext, s *mcp.Server) {
	if !serverEnabler.Enable() {
		return
	}

	initTracing()
	metrics()

	logger.Debug("AfterNewServer: installing protocol middleware")

	s.AddReceivingMiddleware(serverProtocolMiddleware)
}

// serverProtocolMiddleware creates one span per meaningful inbound MCP
// method (notifications are skipped — see isNotification), parented to the
// HTTP server span recovered via the BeforeStreamableHTTP bridge. Tool
// calls are named "tools/call <tool>" with gen_ai.tool.name/
// gen_ai.operation.name set per the current MCP semantic conventions.
//
// It also records the per-operation metrics (see operationObservation in
// metrics.go). They share resultErrorType with the span, so tracing and
// metrics always agree on whether a request failed.
func serverProtocolMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if !serverEnabler.Enable() {
			return next(ctx, method, req)
		}

		initTracing()

		if isNotification(method) {
			return next(ctx, method, req)
		}

		// In stateful Streamable HTTP, ctx may belong to the persistent MCP
		// session rather than this specific HTTP POST, so recover the
		// current HTTP server span from the header BeforeStreamableHTTP set.
		if extra := req.GetExtra(); extra != nil && extra.Header != nil {
			if traceparent := extra.Header.Get(httpSpanContextHeader); traceparent != "" {
				carrier := propagation.MapCarrier{
					"traceparent": traceparent,
				}

				ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)

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

		spanName := methodToSpanName(method, toolName)

		attributes := []attribute.KeyValue{
			attribute.String(attrMCPMethodName, method),
			attribute.String(attrRPCSystemName, "jsonrpc"),
			attribute.String(attrRPCMethod, method),
		}

		appendMCPContextAttributes(&attributes, protocolVersion, sessionID)

		if method == "tools/call" && toolName != "" {
			attributes = append(
				attributes,
				attribute.String(attrGenAIToolName, toolName),
				attribute.String(attrGenAIOperationName, genAIOperationExecuteTool),
			)
		}

		// HTTP RequestExtra is populated by the Streamable HTTP transport.
		// We can therefore safely identify HTTP transport here without
		// guessing for other/custom transports.
		if isHTTPMCPRequest(req) {
			attributes = append(
				attributes,
				attribute.String(attrNetworkTransport, "tcp"),
				attribute.String(attrNetworkProtocolName, "http"),
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

		obs := beginOperation(method, toolName, protocolVersion, isHTTPMCPRequest(req))

		result, err := next(ctx, method, req)

		errType := resultErrorType(result, err)

		if err != nil {
			finishSpan(methodSpan, err, "")
		} else {
			// CallToolResult.IsError=true is a tool-level failure encoded
			// inside an otherwise successful JSON-RPC response.
			finishSpan(methodSpan, nil, errType)
		}

		obs.end(ctx, errType)

		return result, err
	}
}

// BeforeStreamableHTTP runs once per inbound HTTP request (this is
// (*StreamableServerTransport).ServeHTTP — see mcp.otelc.yaml) and copies
// the current HTTP traceparent into the request's RequestExtra.Header via
// a private bridge header. serverProtocolMiddleware extracts it again to
// recover the correct per-request parent, since the MCP SDK may otherwise
// dispatch a message using the persistent session context rather than the
// context of the HTTP request that actually delivered it.
func BeforeStreamableHTTP(
	ictx hook.HookContext,
	recv *mcp.StreamableServerTransport,
	w http.ResponseWriter,
	req *http.Request,
) {
	if !serverEnabler.Enable() {
		return
	}

	initTracing()

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
		logger.Debug("BeforeStreamableHTTP: no valid HTTP server span")
		return
	}

	// Serialize the CURRENT HTTP server span context into the private
	// request header. The MCP SDK copies this header into RequestExtra.Header
	// for the corresponding JSON-RPC request.
	carrier := propagation.MapCarrier{}

	otel.GetTextMapPropagator().Inject(httpCtx, carrier)

	traceparent := carrier.Get("traceparent")
	if traceparent == "" {
		return
	}

	req.Header.Set(httpSpanContextHeader, traceparent)

	// Keep the request context unchanged.
	//
	// The important part is the per-HTTP-request span context stored in
	// RequestExtra.Header.
	ictx.SetParam(2, req)

	logger.Debug(
		"BeforeStreamableHTTP: bridged HTTP trace context",
		"trace_id",
		sc.TraceID(),
		"span_id",
		sc.SpanID(),
	)
}
