package mcptest

import (
	"context"
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
	if err := f.RemoveDNSRoute(ctx, r.ID); err != nil {
		t.Fatal(err)
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
