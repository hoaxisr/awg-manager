package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/sbconfig"
)

func TestStrictUplinkRefreshBindsNewNodesAndPreservesDNSOnReload(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "applied.json"))
	t.Cleanup(orch.Close)
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	a := NewOperatorAdapter(orch, nil, nil)
	binding := uplink.Binding{Device: "wan0", IPv4: "192.0.2.2", DNSServers: []string{"192.0.2.53"}}
	a.SetUplinkBindingsFn(func() []sbconfig.Spec { return []sbconfig.Spec{{Tag: "sub", Binding: binding, Group: true}} })
	add := func(tag, server string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"tag": tag, "type": "vless", "server": server, "server_port": 443, "uuid": "11111111-2222-4333-8444-555555555555"})
		if err := a.AddOutbound(tag, raw); err != nil {
			t.Fatal(err)
		}
	}
	group := func(members ...string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"type": "selector", "outbounds": members})
		if err := a.UpdateOutbound("sub", raw); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, SlotSubscriptionsMeta.Filename))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err = json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	add("node1", "edge1.example.com")
	group("node1")
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sbconfig.Verify(read(), "sub", "wan0", true); err != nil {
		t.Fatal(err)
	}
	add("node2", "edge2.example.com")
	group("node1", "node2")
	binding = uplink.Binding{Device: "wan1", IPv4: "198.51.100.2", DNSServers: []string{"198.51.100.53"}}
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sbconfig.Verify(read(), "sub", "wan1", true); err != nil {
		t.Fatal(err)
	}
	// A later batch fails closed and rolls back BOTH DNS and outbound maps.
	before, _ := json.Marshal(a.cfg)
	add("node3", "edge3.example.com")
	group("node1", "node2", "node3")
	binding.DNSServers = nil
	if err := a.Reload(context.Background()); err == nil {
		t.Fatal("missing provider DNS accepted")
	}
	after, _ := json.Marshal(a.cfg)
	if string(before) != string(after) {
		t.Fatalf("failed strict refresh changed committed config\nbefore=%s\nafter=%s", before, after)
	}
	loaded := NewOperatorAdapter(orch, nil, nil)
	if err := loaded.LoadFromDisk(dir); err != nil {
		t.Fatal(err)
	}
	if len(loaded.cfg.DNS) == 0 {
		t.Fatal("bound DNS lost when loading slot")
	}
}

func TestStrictBindingPreferenceRollsBackAndIsNotAliased(t *testing.T) {
	s, mut := newTestService(t)
	sub, err := s.Create(context.Background(), CreateInput{Inline: "vless://11111111-2222-4333-8444-555555555555@edge.example.com:443?security=tls#node", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	b := &uplink.Binding{Device: "wan0", IPv4: "192.0.2.2", DNSServers: []string{"192.0.2.53"}}
	expected := ""
	mut.reloadErr = errors.New("validation failed")
	if _, err = s.update(context.Background(), sub.ID, UpdatePatch{BindInterface: &b.Device, ExpectedBind: &expected, UplinkBinding: &b}); err == nil {
		t.Fatal("expected failed apply")
	}
	saved, _ := s.Get(sub.ID)
	if saved.UplinkBinding != nil || saved.BindInterface != "" {
		t.Fatal("desired binding not rolled back")
	}
	mut.reloadErr = nil
	saved, err = s.update(context.Background(), sub.ID, UpdatePatch{BindInterface: &b.Device, ExpectedBind: &expected, UplinkBinding: &b})
	if err != nil {
		t.Fatal(err)
	}
	saved.UplinkBinding.DNSServers[0] = "mutated"
	again, _ := s.Get(sub.ID)
	if again.UplinkBinding.DNSServers[0] != "192.0.2.53" {
		t.Fatal("mutable binding leaked from store")
	}
	stale := "old-wan"
	if _, err = s.update(context.Background(), sub.ID, UpdatePatch{BindInterface: &b.Device, ExpectedBind: &stale, UplinkBinding: &b}); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal(err)
	}
	auto := ""
	if _, err = s.Update(sub.ID, UpdatePatch{BindInterface: &auto}); err != nil {
		t.Fatal(err)
	}
	released, _ := s.Get(sub.ID)
	if released.UplinkBinding != nil || released.BindInterface != "" {
		t.Fatal("explicit manual auto selection did not release strict binding")
	}
}

func TestReleaseUplinkRemovesOnlyManagedDialFields(t *testing.T) {
	ob := map[string]any{"tag": "node", "server": "edge.example.com", "tls": map[string]any{"server_name": "cover.example.com"}, "inet4_bind_address": "192.0.2.2", "domain_resolver": map[string]any{"server": sbconfig.DNSTag("node")}}
	released := releaseUplinkDialFields(ob)
	if released["domain_resolver"] != nil || released["inet4_bind_address"] != nil || released["server"] != "edge.example.com" {
		t.Fatal(released)
	}
	if ob["domain_resolver"] == nil || ob["inet4_bind_address"] == nil {
		t.Fatal("borrowed outbound map was mutated")
	}
	custom := map[string]any{"tag": "node", "domain_resolver": map[string]any{"server": "user-resolver"}}
	if releaseUplinkDialFields(custom)["domain_resolver"] == nil {
		t.Fatal("unrelated user resolver removed")
	}
}

// A live manual selection is stored before the config's selector.default is
// refreshed. WAN activation restarts sing-box, so it must persist that choice
// without fetching a subscription over the failing provider.
func TestStrictUplinkBindingPersistsSelectedURLMember(t *testing.T) {
	s, mut := newTestService(t)
	body := feedLinkA + feedLinkB
	srv := serveLinks(t, &body)
	sub, err := s.Create(context.Background(), CreateInput{URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	active := tagOf(sub, 1)
	if err = s.SetActiveMember(context.Background(), sub.ID, active); err != nil {
		t.Fatal(err)
	}
	oldDefault, _ := selectorDefault(t, mut, sub.SelectorTag)
	if oldDefault == active {
		t.Fatal("fixture did not retain an older selector default")
	}
	srv.Close() // WAN rebind must work with the source server unavailable.
	binding := &uplink.Binding{Device: "wan0", IPv4: "192.0.2.2", DNSServers: []string{"192.0.2.53"}}
	expected := sub.BindInterface
	saved, err := s.update(context.Background(), sub.ID, UpdatePatch{BindInterface: &binding.Device, ExpectedBind: &expected, UplinkBinding: &binding})
	if err != nil {
		t.Fatal(err)
	}
	actual, members := selectorDefault(t, mut, sub.SelectorTag)
	if actual != active || saved.ActiveMember != active || len(members) != 2 {
		t.Fatalf("WAN rebind lost manual member: default=%q stored=%q want=%q members=%d", actual, saved.ActiveMember, active, len(members))
	}
}
