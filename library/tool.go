package mcptrace

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("mcp")

func finishSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

func WrapTool[In, Out any](
	name string,
	version string,
	handler func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error),
) func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input In) (result *mcp.CallToolResult, out Out, err error) {
		ctx, span := tracer.Start(ctx, "tool."+name,
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithAttributes(
				attribute.String("tool.name", name),
				attribute.String("tool.version", version),
			),
		)
		defer func() {
			switch {
			case err != nil:
				span.SetAttributes(
					attribute.Bool("tool.success", false),
					attribute.String("tool.error", err.Error()),
				)
			case result != nil && result.IsError:
				span.SetAttributes(attribute.Bool("tool.success", false))
			default:
				span.SetAttributes(attribute.Bool("tool.success", true))
			}
			finishSpan(span, err)
		}()

		result, out, err = handler(ctx, req, input)
		return result, out, err
	}
}

func ChildSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(err error)) {
	ctx, span := tracer.Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, func(err error) {
		finishSpan(span, err)
	}
}
