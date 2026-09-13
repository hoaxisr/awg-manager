package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
)

// scopedSession mounts the whole stack a real client goes through — the
// key middleware in front of the MCP handler — because the scope is
// carried from the authenticated request into the tool call. Testing the
// middleware alone would not prove the key actually reaches the tool.
func scopedSession(t *testing.T, readOnly bool) *sdk.ClientSession {
	t.Helper()
	srv := mcpsrv.NewServer(mcptest.New(), "test")
	srv.AddReceivingMiddleware(mcpsrv.RequireWriteScope())
	handler := mcpsrv.KeyMiddleware(mcpsrv.AuthConfig{
		Enabled: func() bool { return true },
		Verify: func(tok string) (mcpsrv.KeyInfo, bool) {
			if tok == "awgm_good" {
				return mcpsrv.KeyInfo{ID: "k1", Name: "agent", ReadOnly: readOnly}, true
			}
			return mcpsrv.KeyInfo{}, false
		},
	}, mcpsrv.NewHTTPHandler(srv))

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearer{http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type bearer struct{ rt http.RoundTripper }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer awgm_good")
	return b.rt.RoundTrip(r)
}

// TestScope_ReadOnlyKeyCannotWrite — ключ только для чтения и есть весь
// смысл областей: он должен доходить до инструментов чтения и упираться в
// любую запись.
func TestScope_ReadOnlyKeyCannotWrite(t *testing.T) {
	s := scopedSession(t, true)

	res, out := callTool(t, s, "list_tunnels", nil)
	if res.IsError {
		t.Fatalf("a read-only key must still read: %s", toolText(res))
	}
	if len(out["tunnels"].([]any)) != 2 {
		t.Fatalf("tunnels = %v", out["tunnels"])
	}

	writes := map[string]map[string]any{
		"control_tunnel":           {"tunnelId": "tn-1", "action": "stop"},
		"create_tunnel":            {"name": "X", "config": "[Interface]\n[Peer]\n"},
		"remove_dns_route":         {"routeId": "dl-1"},
		"set_dns_route_enabled":    {"routeId": "dl-1", "enabled": false},
		"update_dns_route":         {"routeId": "dl-1", "name": "X"},
		"set_client_route_enabled": {"clientIp": "192.168.1.10", "enabled": false},
		"control_singbox":          {"action": "stop"},
		"run_pingcheck":            {},
	}
	for name, args := range writes {
		res, _ := callTool(t, s, name, args)
		if !res.IsError {
			t.Errorf("%s went through on a read-only key", name)
			continue
		}
		if txt := toolText(res); !strings.Contains(strings.ToLower(txt), "read-only") {
			t.Errorf("%s: the refusal must name the cause, got %q", name, txt)
		}
	}
}

// TestScope_ReadOnlyKeyChangesNothing — отказ обязан случиться до
// инструмента, а не после: «нельзя» после применения было бы худшим из
// исходов.
func TestScope_ReadOnlyKeyChangesNothing(t *testing.T) {
	fake := mcptest.New()
	srv := mcpsrv.NewServer(fake, "test")
	srv.AddReceivingMiddleware(mcpsrv.RequireWriteScope())
	handler := mcpsrv.KeyMiddleware(mcpsrv.AuthConfig{
		Enabled: func() bool { return true },
		Verify:  func(string) (mcpsrv.KeyInfo, bool) { return mcpsrv.KeyInfo{ID: "k1", Name: "ro", ReadOnly: true}, true },
	}, mcpsrv.NewHTTPHandler(srv))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	client := sdk.NewClient(&sdk.Implementation{Name: "c", Version: "0"}, nil)
	s, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{http.DefaultTransport}}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if res, _ := callTool(t, s, "remove_dns_route", map[string]any{"routeId": "dl-1"}); !res.IsError {
		t.Fatal("the delete must be refused")
	}
	routes, _ := fake.ListDNSRoutes(context.Background())
	if len(routes) != 1 {
		t.Fatalf("the refused delete still ran: routes = %d", len(routes))
	}
}

// TestScope_FullKeyIsUnaffected — область по умолчанию полная, и
// существующие ключи не должны ничего заметить.
func TestScope_FullKeyIsUnaffected(t *testing.T) {
	s := scopedSession(t, false)

	if res, _ := callTool(t, s, "control_tunnel", map[string]any{"tunnelId": "tn-1", "action": "stop"}); res.IsError {
		t.Fatalf("a full key must still write: %s", toolText(res))
	}
	if res, _ := callTool(t, s, "list_tunnels", nil); res.IsError {
		t.Fatal("a full key must still read")
	}
}

// TestScope_UnknownToolIsTreatedAsAWrite — набор читающих инструментов
// перечислен вручную, и новый инструмент в него попасть забудут. Тогда он
// обязан считаться записью: пропустить незнакомое — это тихая дыра.
func TestScope_UnknownToolIsTreatedAsAWrite(t *testing.T) {
	if mcpsrv.IsReadOnlyTool("some_tool_that_does_not_exist") {
		t.Fatal("an unknown tool must not be treated as read-only")
	}
	if !mcpsrv.IsReadOnlyTool("list_tunnels") {
		t.Fatal("list_tunnels is read-only")
	}
}

// TestScope_ReadOnlySetMatchesTheAnnotations — список читающих
// инструментов и аннотации инструментов — два независимых утверждения об
// одном и том же. Разойдясь, они дадут либо дыру, либо необъяснимый отказ.
func TestScope_ReadOnlySetMatchesTheAnnotations(t *testing.T) {
	s, _ := newTestSession(t)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		annotated := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		listed := mcpsrv.IsReadOnlyTool(tool.Name)
		// A tool that changes nothing but hands out credentials is
		// read-only by annotation and still off-limits to a read-only
		// key; the exception list is the only place that is allowed.
		if mcpsrv.ExportsCredentials(tool.Name) {
			if !annotated || listed {
				t.Errorf("%s: a credential exporter must be annotated read-only and kept OUT of the read-only scope", tool.Name)
			}
			continue
		}
		if annotated != listed {
			t.Errorf("%s: readOnlyHint=%v but the scope list says read-only=%v", tool.Name, annotated, listed)
		}
	}
}

// TestScope_ReadOnlyKeyCannotExportCredentials — ревью нашло: набор
// читающих инструментов включал экспорт конфигов, а они отдают приватные
// ключи. Пользователю при этом обещали ключ, который «ничего не может
// изменить», и он передавал его агенту с ограниченным доверием — который
// этим ключом мог получить рабочий VPN-доступ откуда угодно.
func TestScope_ReadOnlyKeyCannotExportCredentials(t *testing.T) {
	s := scopedSession(t, true)
	for name, args := range map[string]map[string]any{
		"export_tunnel_config":   {"tunnelId": "tn-1"},
		"get_server_peer_config": {"serverId": "Wireguard0", "publicKey": "pub-laptop="},
	} {
		res, _ := callTool(t, s, name, args)
		if !res.IsError {
			t.Errorf("%s handed out a private key on a read-only key", name)
			continue
		}
		if txt := toolText(res); !strings.Contains(strings.ToLower(txt), "read-only") {
			t.Errorf("%s: the refusal must name the cause, got %q", name, txt)
		}
	}
}
