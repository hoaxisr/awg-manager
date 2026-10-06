package dnsroute

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Н10b: снос туннеля снимает свои строки dns-proxy route по хранилищу —
// без чтения sc-вида и без сохранения (F568-Flush) под замком туннеля;
// sc-вид после этого грязен до сохранения, начатого позже.
// Мутация: вернуть reconcileAll → чтение /show/sc/dns-proxy/route +1, красный.
func TestOnTunnelDelete_NoReadNoReconcile(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	q, c, poster, fg := newTestNDMS()
	svc := &ServiceImpl{store: store, queries: q, commands: c}
	ctx := context.Background()
	list, err := svc.Create(ctx, DomainList{
		Name:          "solo",
		ManualDomains: []string{"example.com"},
		Routes: []RouteTarget{
			{Interface: "Wireguard0", TunnelID: "doomed"},
			{Interface: "Wireguard1", TunnelID: "keeper"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reads := fg.Calls("/show/sc/dns-proxy/route")
	before := len(poster.Payloads())

	if err := svc.OnTunnelDelete(ctx, "doomed"); err != nil {
		t.Fatal(err)
	}
	if n := fg.Calls("/show/sc/dns-proxy/route") - reads; n != 0 {
		t.Fatalf("чтений sc-вида на сносе: %d", n)
	}
	var got []string
	for _, p := range poster.Payloads()[before:] {
		b, _ := json.Marshal(p)
		got = append(got, string(b))
	}
	want := `{"dns-proxy":{"route":[{"group":"` + buildGroupName(list.ID, list.Name, 1) + `","interface":"Wireguard0","no":true}]}}`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("POST на сносе %v, ждали только снос своей строки %s", got, want)
	}

	// sc-вид грязен: первая же сверка сохраняет до чтения.
	if err := c.DNSRoutes.FlushPendingSave(ctx); err != nil {
		t.Fatal(err)
	}
	last, _ := json.Marshal(poster.Payloads()[len(poster.Payloads())-1])
	if !strings.Contains(string(last), `"save"`) {
		t.Fatalf("после сноса sc-вид не помечен грязным: последний POST %s", last)
	}
}
