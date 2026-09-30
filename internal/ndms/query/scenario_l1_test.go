package query

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// L1 F546 (стенд 30.09): чужой Wireguard19 создан, затем снят снаружи без
// доставленного ifdestroyed; панель читает /system-tunnels и серверы, а хук
// слоя уже сбросил список серверов. Runtime — из снимка полного списка: ни
// одного запроса по имени, E == 0.
//
// Task 42 — runtime-половина: GetSystemTunnel и ListSystemTunnelsFresh после
// сноса. Get и List серверов после сноса ещё обогащаются rc по имени —
// полный L1 включается вместе с деревом rc (Task 44).
func TestScenario_ForeignWGRemovedNoHook_PanelReadsNoE(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := NewQueries(Deps{Getter: f, Logger: NopLogger()})
	if _, err := q.Interfaces.List(ctx); err != nil { // карта тёплая, как в проде
		t.Fatal(err)
	}

	f.Add(ndms.Interface{ID: "Wireguard19", Type: "Wireguard", State: "up", Link: "up"})
	f.SetDetail("Wireguard19", json.RawMessage(`{"wireguard":{"public-key":"PUB19=","peer":[{"public-key":"P=","rxbytes":7,"online":true}]}}`))
	f.SetRC("Wireguard19", json.RawMessage(`{"wireguard":{"peer":[{"key":"P=","allow-ips":[{"address":"10.19.0.2","mask":"255.255.255.255"}]}]}}`))
	for _, h := range f.DrainHooks() { // хуки доставлены
		if h.Type == "ifcreated" {
			q.Interfaces.OnCreated(h.ID)
		}
	}
	if err := q.Interfaces.ReconcilePending(ctx); err != nil {
		t.Fatal(err)
	}

	if st, err := q.WGServers.GetSystemTunnel(ctx, "Wireguard19"); err != nil || st.Peer == nil || st.Peer.PublicKey != "P=" {
		t.Fatalf("GetSystemTunnel до сноса: %+v %v", st, err)
	}
	if list, err := q.WGServers.ListSystemTunnelsFresh(ctx); err != nil || len(list) != 1 {
		t.Fatalf("ListSystemTunnelsFresh до сноса: %+v %v", list, err)
	}
	if srv, err := q.WGServers.Get(ctx, "Wireguard19"); err != nil || srv.PublicKey != "PUB19=" {
		t.Fatalf("Get до сноса: %+v %v", srv, err)
	}

	f.Remove("Wireguard19") // хук потерян/в очереди
	q.WGServers.InvalidateAll()
	posts := len(f.Posts)

	if _, err := q.WGServers.GetSystemTunnel(ctx, "Wireguard19"); err != nil {
		t.Fatalf("GetSystemTunnel после сноса: %v", err)
	}
	if f.E != 0 {
		t.Fatalf("GetSystemTunnel по снятому: E=%d, want 0", f.E)
	}
	list, err := q.WGServers.ListSystemTunnelsFresh(ctx)
	if err != nil {
		t.Fatalf("ListSystemTunnelsFresh после сноса: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("снятый Wireguard19 в свежем списке: %+v", list)
	}
	if f.E != 0 {
		t.Fatalf("E=%d, want 0", f.E)
	}
	for _, p := range f.Posts[posts:] {
		if strings.Contains(p, "Wireguard19") {
			t.Fatalf("POST с именем снятого интерфейса: %s", p)
		}
	}
}

// Решение перед созданием — по только что прочитанному списку: +1 список на
// вызов, без чтений по имени.
func TestFindFreeIndex_FreshList(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard"})
	q := NewQueries(Deps{Getter: f, Logger: NopLogger()})
	if _, err := q.Interfaces.List(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		before := f.ListCalls()
		n, err := q.WGServers.FindFreeIndex(ctx)
		if err != nil || n != 2 {
			t.Fatalf("FindFreeIndex = %d, %v; want 2", n, err)
		}
		if got := f.ListCalls() - before; got != 1 {
			t.Fatalf("вызов %d: списков %d, want 1", i, got)
		}
	}
	if f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("E=%d Posts=%v", f.E, f.Posts)
	}
}
