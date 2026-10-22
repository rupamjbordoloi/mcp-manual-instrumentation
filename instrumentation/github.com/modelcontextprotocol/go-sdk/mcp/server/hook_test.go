package server

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
)

type valueError struct{}

func (valueError) Error() string { return "value error" }

func TestErrorType(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain error", errors.New("boom"), "*errors.errorString"},
		{"wrapped error", fmt.Errorf("ctx: %w", errors.New("boom")), "*fmt.wrapError"},
		{"JSON-RPC error (the type name is whatever the SDK aliases it to)", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams}, fmt.Sprintf("%T", &jsonrpc.Error{})},
		{"value-typed error", valueError{}, "server.valueError"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, errorType(tt.err))
		})
	}

	t.Run("never exposes the error message", func(t *testing.T) {
		assert.NotContains(t, errorType(errors.New("secret token abc123")), "abc123")
	})
}

func TestIsToolResultError(t *testing.T) {
	var typedNil *mcp.CallToolResult

	tests := []struct {
		name   string
		result mcp.Result
		want   bool
	}{
		{"nil result", nil, false},
		{"typed nil pointer", typedNil, false},
		{"a result that is not a tool result", &mcp.ListToolsResult{}, false},
		{"successful tool result", &mcp.CallToolResult{}, false},
		{"tool result with isError", &mcp.CallToolResult{IsError: true}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isToolResultError(tt.result))
		})
	}
}

func TestResultErrorType(t *testing.T) {
	boom := errors.New("boom")

	tests := []struct {
		name   string
		result mcp.Result
		err    error
		want   string
	}{
		{"success", &mcp.CallToolResult{}, nil, ""},
		{"nil result and no error", nil, nil, ""},
		{"error", nil, boom, "*errors.errorString"},
		{"tool-level failure inside a successful response", &mcp.CallToolResult{IsError: true}, nil, toolErrorType},
		{"an error wins over a tool-level failure", &mcp.CallToolResult{IsError: true}, boom, "*errors.errorString"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resultErrorType(tt.result, tt.err))
		})
	}
}

func TestMethodToSpanName(t *testing.T) {
	tests := []struct{ method, tool, want string }{
		{"tools/call", "greet", "tools/call greet"},
		{"tools/call", "", "tools/call"},
		{"tools/list", "", "tools/list"},
		{"tools/list", "ignored", "tools/list"},
		{"resources/read", "", "resources/read"},
		{"initialize", "", "initialize"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, methodToSpanName(tt.method, tt.tool), tt.method+"/"+tt.tool)
	}
}

func TestIsNotification(t *testing.T) {
	for _, m := range []string{
		"notifications/initialized", "notifications/roots/list_changed",
		"notifications/progress", "notifications/cancelled",
	} {
		assert.True(t, isNotification(m), m)
	}

	for _, m := range []string{"initialize", "tools/call", "tools/list", "resources/read", "ping", "", "notifications/unknown"} {
		assert.False(t, isNotification(m), m)
	}
}

func TestShouldRecordSessionID(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"", true}, // unknown: keep the existing behaviour
		{"2024-11-05", true},
		{"2025-06-18", true},
		{"2025-11-25", true},
		{statelessMCPProtocolVersion, false},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, shouldRecordSessionID(tt.version), tt.version)
	}
}

func TestAppendMCPContextAttributes(t *testing.T) {
	asMap := func(attrs []attribute.KeyValue) map[string]string {
		out := map[string]string{}
		for _, a := range attrs {
			out[string(a.Key)] = a.Value.Emit()
		}

		return out
	}

	tests := []struct {
		name            string
		version, sessID string
		want            map[string]string
	}{
		{"version and session", "2025-06-18", "sess-1", map[string]string{attrMCPProtocolVersion: "2025-06-18", attrMCPSessionID: "sess-1"}},
		{"version only", "2025-06-18", "", map[string]string{attrMCPProtocolVersion: "2025-06-18"}},
		{"session only (version unknown)", "", "sess-1", map[string]string{attrMCPSessionID: "sess-1"}},
		{"stateless protocol drops the session ID", statelessMCPProtocolVersion, "sess-1", map[string]string{attrMCPProtocolVersion: statelessMCPProtocolVersion}},
		{"nothing known", "", "", map[string]string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attrs []attribute.KeyValue

			appendMCPContextAttributes(&attrs, tt.version, tt.sessID)

			assert.Equal(t, tt.want, asMap(attrs))
		})
	}

	t.Run("a nil destination is ignored", func(t *testing.T) {
		assert.NotPanics(t, func() { appendMCPContextAttributes(nil, "2025-06-18", "s") })
	})

	t.Run("appends to what is already there", func(t *testing.T) {
		attrs := []attribute.KeyValue{attribute.String("keep", "me")}

		appendMCPContextAttributes(&attrs, "2025-06-18", "")

		assert.Len(t, attrs, 2)
		assert.Equal(t, "keep", string(attrs[0].Key))
	})
}

func TestRequestToolName(t *testing.T) {
	call := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "greet"}}

	tests := []struct {
		name   string
		req    mcp.Request
		method string
		want   string
	}{
		{"tools/call", call, "tools/call", "greet"},
		{"only tools/call carries a tool name", call, "tools/list", ""},
		{"nil request", nil, "tools/call", ""},
		{"params of another type", &mcp.ListToolsRequest{Params: &mcp.ListToolsParams{}}, "tools/call", ""},
		{"nil params", &mcp.CallToolRequest{}, "tools/call", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, requestToolName(tt.req, tt.method))
		})
	}
}

func TestIsHTTPMCPRequest(t *testing.T) {
	tests := []struct {
		name string
		req  mcp.Request
		want bool
	}{
		{"nil", nil, false},
		{"no extra (stdio, in-memory)", &mcp.CallToolRequest{}, false},
		{"extra without headers", &mcp.CallToolRequest{Extra: &mcp.RequestExtra{}}, false},
		{"Streamable HTTP supplies headers", &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: http.Header{}}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isHTTPMCPRequest(tt.req))
		})
	}
}

func TestEnabler(t *testing.T) {
	assert.True(t, serverEnabler.Enable())

	t.Setenv("OTEL_GO_DISABLED_INSTRUMENTATIONS", instrumentationKey)
	assert.False(t, serverEnabler.Enable())
}

func TestRequestProtocolVersionAndSessionID(t *testing.T) {
	h := newHarness(t, nil)

	t.Run("a nil request has neither", func(t *testing.T) {
		assert.Empty(t, requestProtocolVersion(nil))
		assert.Empty(t, requestSessionID(nil))
	})

	t.Run("the session ID is the session's own", func(t *testing.T) {
		assert.Equal(t, h.session.ID(), requestSessionID(&mcp.ListToolsRequest{Session: h.session}))
	})

	t.Run("the protocol version comes from the request when the SDK offers it, otherwise from initialize's params", func(t *testing.T) {
		req := &mcp.ListToolsRequest{Session: h.session, Params: &mcp.ListToolsParams{}}

		if provider, ok := mcp.Request(req).(interface{ ProtocolVersion() string }); ok {
			assert.Equal(t, provider.ProtocolVersion(), requestProtocolVersion(req), "delegates to the SDK")
		} else {
			assert.Empty(t, requestProtocolVersion(req))
		}

		init := &mcp.ServerRequest[*mcp.InitializeParams]{Session: h.session, Params: &mcp.InitializeParams{ProtocolVersion: "2025-06-18"}}

		if provider, ok := mcp.Request(init).(interface{ ProtocolVersion() string }); ok {
			assert.Equal(t, provider.ProtocolVersion(), requestProtocolVersion(init))
		} else {
			assert.Equal(t, "2025-06-18", requestProtocolVersion(init))
		}
	})
}
