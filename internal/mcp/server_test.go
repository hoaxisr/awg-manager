package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
)

// newTestSession serves NewHTTPHandler over httptest and connects a go-sdk
// client to it. No auth middleware here — Task 7 tests that separately.
func newTestSession(t *testing.T) (*sdk.ClientSession, *mcptest.Fake) {
	t.Helper()
	fake := mcptest.New()
	srv := mcpsrv.NewServer(fake, "test")
	ts := httptest.NewServer(mcpsrv.NewHTTPHandler(srv))
	t.Cleanup(ts.Close)
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{},
		DisableStandaloneSSE: true, // stateless server answers GET with 405
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, fake
}

// callTool invokes a tool and decodes StructuredContent into a map.
func callTool(t *testing.T, s *sdk.ClientSession, name string, args map[string]any) (*sdk.CallToolResult, map[string]any) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	out := map[string]any{}
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &out)
	}
	return res, out
}

func toolText(res *sdk.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func TestServer_ListsToolsWithAnnotations(t *testing.T) {
	s, _ := newTestSession(t)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*sdk.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
		if tool.Annotations == nil {
			t.Errorf("tool %s has no annotations", tool.Name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("tool %s has no output schema", tool.Name)
		}
	}
	// Task 5 registers only the system tool group; Task 6 adds the other 18
	// tools and expands this list to the full 23-tool catalogue.
	want := []string{"get_system_status", "get_logs", "test_connectivity", "get_monitoring_matrix", "run_pingcheck"}
	for _, n := range want {
		if byName[n] == nil {
			t.Errorf("missing tool %s", n)
		}
	}
	if len(res.Tools) != len(want) {
		t.Errorf("tool count = %d, want %d", len(res.Tools), len(want))
	}
	if !byName["get_system_status"].Annotations.ReadOnlyHint {
		t.Error("get_system_status must be read-only")
	}
	if a := byName["run_pingcheck"].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
		t.Error("run_pingcheck must be a non-destructive write")
	}
}

func TestServer_GetSystemStatus(t *testing.T) {
	s, _ := newTestSession(t)
	res, out := callTool(t, s, "get_system_status", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	if out["bootPhase"] != "ready" || out["anyWANUp"] != true {
		t.Fatalf("out = %v", out)
	}
}

func TestServer_ToolErrorIsNotProtocolError(t *testing.T) {
	s, fake := newTestSession(t)
	fake.Err = context.DeadlineExceeded
	res, _ := callTool(t, s, "get_system_status", nil)
	if !res.IsError {
		t.Fatal("expected IsError")
	}
	if toolText(res) == "" {
		t.Fatal("expected a human-readable message")
	}
}

func TestServer_OpenAPIResource(t *testing.T) {
	s, fake := newTestSession(t)
	res, err := s.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: mcpsrv.ResourceOpenAPI})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Contents) != 1 || res.Contents[0].Text != string(fake.Spec) || res.Contents[0].MIMEType != "application/yaml" {
		t.Fatalf("contents = %+v", res.Contents)
	}
}
