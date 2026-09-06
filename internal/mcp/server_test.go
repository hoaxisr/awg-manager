package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
)

// newTestSession serves NewHTTPHandler over httptest and connects a go-sdk
// client to it. No auth middleware here — Task 7 tests that separately.
func newTestSession(t *testing.T) (*sdk.ClientSession, *mcptest.Fake) {
	t.Helper()
	fake := mcptest.New()
	return connect(t, mcpsrv.NewServer(fake, "test")), fake
}

// connect mounts srv over the real Streamable HTTP transport and returns
// a connected client session.
func connect(t *testing.T, srv *sdk.Server) *sdk.ClientSession {
	t.Helper()
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
	return session
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
	// Full 23-tool catalogue across all five register* groups.
	want := []string{
		"get_system_status", "get_logs", "test_connectivity", "get_monitoring_matrix", "run_pingcheck",
		"list_tunnels", "get_tunnel", "control_tunnel", "create_tunnel", "replace_tunnel_config", "export_tunnel_config",
		"list_dns_routes", "add_dns_route", "remove_dns_route",
		"list_static_routes", "add_static_route", "remove_static_route",
		"list_client_routes", "set_client_route",
		"list_access_policies", "list_devices",
		"list_managed_servers", "control_singbox",
	}
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
	if !byName["list_tunnels"].Annotations.ReadOnlyHint {
		t.Error("list_tunnels must be read-only")
	}
	if a := byName["control_tunnel"].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
		t.Error("control_tunnel must be a non-destructive write")
	}
	// Deleting a routing list is permanent and MCP cannot recreate it, so
	// the host must get the chance to ask the user first.
	for _, n := range []string{"remove_dns_route", "remove_static_route"} {
		a := byName[n].Annotations
		if a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
			t.Errorf("%s must be annotated destructive", n)
		}
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

// blockingCalls is a receiving middleware that makes every tools/call hang
// until its context is done, standing in for a dependency that never
// answers (a probe against a dead endpoint).
func blockingCalls(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

// TestCallDeadline_BoundsHangingTool — a tool whose dependency never
// returns is cut off at the deadline instead of running until the
// dependency gives up on its own.
func TestCallDeadline_BoundsHangingTool(t *testing.T) {
	srv := mcpsrv.NewServer(mcptest.New(), "test")
	srv.AddReceivingMiddleware(mcpsrv.CallDeadline(100*time.Millisecond, context.Background()), blockingCalls)
	s := connect(t, srv)

	start := time.Now()
	_, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: "list_tunnels"})
	if err == nil {
		t.Fatal("expected the deadline to surface as an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("call took %v; the deadline did not fire", elapsed)
	}
}

// TestCallDeadline_ShutdownCancelsInFlightCalls — daemon shutdown must not
// wait on tool calls nobody will read.
func TestCallDeadline_ShutdownCancelsInFlightCalls(t *testing.T) {
	shutdown, stop := context.WithCancel(context.Background())
	srv := mcpsrv.NewServer(mcptest.New(), "test")
	srv.AddReceivingMiddleware(mcpsrv.CallDeadline(time.Minute, shutdown), blockingCalls)
	s := connect(t, srv)

	done := make(chan error, 1)
	go func() {
		_, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: "list_tunnels"})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the call reach the blocker
	stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled call must not report success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel the in-flight call")
	}
}

// TestCallDeadline_LeavesOtherMethodsAlone — list/initialize traffic is
// in-memory and carries no deadline; the blocker below would otherwise
// hang tools/list too.
func TestCallDeadline_LeavesOtherMethodsAlone(t *testing.T) {
	srv := mcpsrv.NewServer(mcptest.New(), "test")
	srv.AddReceivingMiddleware(mcpsrv.CallDeadline(100*time.Millisecond, context.Background()), blockingCalls)
	s := connect(t, srv)
	if _, err := s.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}
