package mcptest

import (
	"context"
	"sync"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

func TestFake_ImplementsDepsAndMutates(t *testing.T) {
	var _ mcpsrv.Deps = (*Fake)(nil)
	f := New()
	ctx := context.Background()

	tunnels, err := f.ListTunnels(ctx)
	if err != nil || len(tunnels) != 2 {
		t.Fatalf("ListTunnels = %v, %v", tunnels, err)
	}
	if err := f.ControlTunnel(ctx, "tn-2", mcpsrv.ActionStart); err != nil {
		t.Fatal(err)
	}
	d, err := f.GetTunnel(ctx, "tn-2")
	if err != nil || d.State != "running" {
		t.Fatalf("after start: %+v, %v", d, err)
	}
	if err := f.ControlTunnel(ctx, "nope", mcpsrv.ActionStop); err == nil {
		t.Fatal("unknown tunnel accepted")
	}
	if err := f.ControlTunnel(ctx, "tn-1", "explode"); err == nil {
		t.Fatal("unknown action accepted")
	}

	r, err := f.AddDNSRoute(ctx, mcpsrv.DNSRouteInput{Name: "yt", Domains: []string{"youtube.com"}, TunnelID: "tn-1"})
	if err != nil || r.ID == "" {
		t.Fatalf("AddDNSRoute = %+v, %v", r, err)
	}
	routes, _ := f.ListDNSRoutes(ctx)
	if len(routes) != 2 {
		t.Fatalf("ListDNSRoutes len = %d, want 2", len(routes))
	}
	deleted, err := f.RemoveDNSRoute(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ID != r.ID || deleted.Name != "yt" {
		t.Fatalf("RemoveDNSRoute returned %+v, want the deleted record", deleted)
	}

	cr, err := f.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.50", TunnelID: "tn-1"})
	if err != nil || cr == nil || cr.TunnelID != "tn-1" {
		t.Fatalf("SetClientRoute = %+v, %v", cr, err)
	}
	cr, err = f.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.50", TunnelID: ""})
	if err != nil || cr != nil {
		t.Fatalf("SetClientRoute(remove) = %+v, %v", cr, err)
	}

	entries, total, err := f.GetLogs(ctx, mcpsrv.LogsQuery{Lines: 2})
	if err != nil || len(entries) != 2 || total < 2 {
		t.Fatalf("GetLogs = %d/%d, %v", len(entries), total, err)
	}
	if len(f.OpenAPISpec()) == 0 {
		t.Fatal("empty openapi spec")
	}
}

func TestFake_GetLogs_MinimumLevelFiltering(t *testing.T) {
	f := New()
	ctx := context.Background()

	entries, total, err := f.GetLogs(ctx, mcpsrv.LogsQuery{Level: "warn", Lines: 500})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2 (warn + error only)", total)
	}
	for _, e := range entries {
		if e.Level == "info" {
			t.Fatalf("GetLogs(level=warn) leaked an info entry: %+v", e)
		}
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
}

// TestFake_ConcurrentSystemStatusAndControlSingbox exercises SystemStatus and
// ControlSingbox from many goroutines at once. It exists to be run with
// -race: it demonstrates (rather than merely asserts) that Fake's Singbox
// field is safe under concurrent reads and writes, which is a realistic
// scenario since cmd/mcp-dev serves one Fake to a live MCP client.
func TestFake_ConcurrentSystemStatusAndControlSingbox(t *testing.T) {
	f := New()
	ctx := context.Background()

	var wg sync.WaitGroup
	const iterations = 50
	wg.Add(2 * iterations)
	for i := 0; i < iterations; i++ {
		go func() {
			defer wg.Done()
			if _, err := f.SystemStatus(ctx); err != nil {
				t.Error(err)
			}
		}()
		go func(i int) {
			defer wg.Done()
			action := "start"
			if i%2 == 0 {
				action = "stop"
			}
			if _, err := f.ControlSingbox(ctx, action); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}

// TestFake_SetClientRoutePreservesFallbackAndEnabled — фейк обязан вести
// себя как localdeps: обновление без fallback не сбрасывает «drop», а
// собственный флаг Enabled пользователя не включается молча обратно.
func TestFake_SetClientRoutePreservesFallbackAndEnabled(t *testing.T) {
	f := New()
	ctx := context.Background()
	created, err := f.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.10", TunnelID: "tn-1", Fallback: "drop"})
	if err != nil || created == nil {
		t.Fatalf("create: %v %+v", err, created)
	}
	f.ClientRoutes[0].Enabled = false
	f.ClientRoutes[0].ClientHostname = "laptop"

	got, err := f.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.10", TunnelID: "tn-2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Fallback != "drop" {
		t.Fatalf("an update without fallback reset the kill-switch: %+v", got)
	}
	if got.Enabled {
		t.Fatalf("the user's enabled flag was flipped back on: %+v", got)
	}
	if got.ClientHostname != "laptop" {
		t.Fatalf("hostname lost on update: %+v", got)
	}
	if len(f.ClientRoutes) != 1 {
		t.Fatalf("duplicate route: %+v", f.ClientRoutes)
	}

	// A new route still defaults to bypass.
	fresh, err := f.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.99", TunnelID: "tn-1"})
	if err != nil || fresh.Fallback != "bypass" {
		t.Fatalf("new route: %v %+v", err, fresh)
	}
}

// TestFake_ImportTunnelIsDisabled — service.Import жёстко ставит
// Enabled=false; фейк обязан отвечать так же, иначе тесты не поймают
// расхождение описания инструмента с реальностью.
func TestFake_ImportTunnelIsDisabled(t *testing.T) {
	f := New()
	got, _, err := f.ImportTunnel(context.Background(), "New", "[Interface]\n[Peer]\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.State != "stopped" {
		t.Fatalf("an imported tunnel is disabled and stopped, got %+v", got)
	}
}

// TestFake_AddDNSRouteIsAlwaysEnabled — dnsroute.Create поднимает
// маршрутизацию сразу и не умеет создавать выключенный список; входа
// enabled у MCP больше нет.
func TestFake_AddDNSRouteIsAlwaysEnabled(t *testing.T) {
	f := New()
	got, err := f.AddDNSRoute(context.Background(), mcpsrv.DNSRouteInput{Name: "GH", Domains: []string{"github.com"}, TunnelID: "tn-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Fatalf("a list created through MCP is always enabled, got %+v", got)
	}
}
