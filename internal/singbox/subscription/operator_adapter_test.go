package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

// Service-link vless entries (non-UUID userinfo, localhost) must be skipped
// without aborting the refresh before real servers are added. Regression for
// subscriptions that prefix info banners before valid share-links.
func TestOperatorAdapter_AddOutbound_SkipsInvalidUUIDBeforeValid(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	adapter := NewOperatorAdapter(orch, nil, nil)

	serviceLink := map[string]any{
		"type":        "vless",
		"server":      "localhost",
		"server_port": float64(80),
		"uuid":        "xxxxxxxxxx1",
	}
	serviceJSON, _ := json.Marshal(serviceLink)
	if err := adapter.AddOutbound("sub-test-bad", serviceJSON); err != nil {
		t.Fatalf("AddOutbound(service): %v", err)
	}
	if tags := adapter.DeclaredOutboundTags(); len(tags) != 0 {
		t.Fatalf("service link must not materialize, got tags %v", tags)
	}

	validLink := map[string]any{
		"type":        "vless",
		"server":      "92.255.79.106",
		"server_port": float64(443),
		"uuid":        "2f1351f5-5cdf-4f7b-bfc8-44b0ac090a26",
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "hhg.mediastreamer.online",
			"reality": map[string]any{
				"enabled":    true,
				"public_key": "IkIZDyseOnYgKYyhgPLsDMqnfXLYGZmaTXMDTv3lkgY",
				"short_id":   "6aa9d760",
			},
			"utls": map[string]any{
				"enabled":     true,
				"fingerprint": "chrome",
			},
		},
	}
	validJSON, _ := json.Marshal(validLink)
	if err := adapter.AddOutbound("sub-test-good", validJSON); err != nil {
		t.Fatalf("AddOutbound(valid): %v", err)
	}
	tags := adapter.DeclaredOutboundTags()
	if len(tags) != 1 || tags[0] != "sub-test-good" {
		t.Fatalf("DeclaredOutboundTags = %v, want [sub-test-good]", tags)
	}

	// Drops are reported after the batch commits (#331): mutations only
	// accumulate; the validation/drop pipeline runs once at Reload.
	if err := adapter.Reload(context.Background()); err != nil {
		t.Fatalf("Reload (commit): %v", err)
	}

	drops := adapter.LastFilterDrops()
	if len(drops) != 1 || !strings.Contains(drops[0].Reason, "uuid") {
		t.Fatalf("LastFilterDrops = %+v, want one invalid uuid drop", drops)
	}
}

func TestOperatorAdapter_AddOutbound_ServiceLinksThenValidShareLinks(t *testing.T) {
	body := []byte(strings.Join([]string{
		"vless://xxxxxxxxxx1@localhost:80?x=1#📆 Осталось: 8 дней",
		"vless://xxxxxxxxxx1@localhost:444#➡️t.me/@awgmanager",
		"vless://xxxxxxxxxx3@localhost:8443?x=1#🟢 Остаток трафика LTE: 67/69GB",
		"vless://2f1351f5-5cdf-4f7b-bfc8-44b0ac090a26@92.255.79.106:443?type=tcp&security=reality&pbk=IkIZDyseOnYgKYyhgPLsDMqnfXLYGZmaTXMDTv3lkgY&fp=chrome&sni=hhg.mediastreamer.online&sid=6aa9d760&spx=%2F&flow=xtls-rprx-vision#UK",
		"vless://780649d3-869d-4413-a211-5210f9b76255@5.129.199.240:443?type=tcp&security=reality&pbk=XhLR-adXQuL-yV80ZPvLlo701PgGg-FmRVpLDGs8Qjo&fp=chrome&sni=k.mediastreamer.online&sid=b39f2d5a2f68&spx=%2F&flow=xtls-rprx-vision#NL",
	}, " "))

	lines := NormalizeBody(body, "text/plain")
	parseRes := vlink.ParseBatch(lines)
	if len(parseRes.Outbounds) != 5 {
		t.Fatalf("parsed %d outbounds, want 5 (3 service + 2 valid)", len(parseRes.Outbounds))
	}

	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	adapter := NewOperatorAdapter(orch, nil, nil)

	subID := "0e5a2268"
	for _, p := range parseRes.Outbounds {
		tag := StableTag(subID, p)
		jsonWithTag := replaceTag(p.Outbound, tag)
		if err := adapter.AddOutbound(tag, jsonWithTag); err != nil {
			t.Fatalf("AddOutbound %q: %v", tag, err)
		}
	}

	tags := adapter.DeclaredOutboundTags()
	if len(tags) != 2 {
		t.Fatalf("DeclaredOutboundTags = %d %v, want 2 valid servers", len(tags), tags)
	}
}

// Коллизия тега подписки с пользовательским слотом (90-user.json)
// непоправима drop-циклом flush — конфликтующий outbound живёт в слоте,
// который пишет только сам пользователь. Flush обязан упасть сразу с
// честной атрибуцией вместо непрозрачного «could not isolate outbound».
func TestOperatorAdapter_Flush_UserSlotCollisionFailsFast(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	for _, meta := range orchestrator.KnownSlots() {
		if meta.Slot == orchestrator.SlotUser {
			if err := orch.Register(meta); err != nil {
				t.Fatalf("register user slot: %v", err)
			}
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// Пользовательский слот уже объявляет тег и включён.
	if err := os.WriteFile(filepath.Join(dir, "90-user.json"),
		[]byte(`{"outbounds":[{"type":"direct","tag":"my-proxy"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := orch.SetEnabled(orchestrator.SlotUser, true); err != nil {
		t.Fatal(err)
	}

	adapter := NewOperatorAdapter(orch, nil, nil)
	ss, _ := json.Marshal(map[string]any{
		"type":        "shadowsocks",
		"server":      "198.51.100.10",
		"server_port": float64(8388),
		"method":      "2022-blake3-aes-128-gcm",
		"password":    "secret",
	})
	if err := adapter.AddOutbound("my-proxy", ss); err != nil {
		t.Fatalf("AddOutbound: %v", err)
	}

	err := adapter.Reload(context.Background())
	if err == nil {
		t.Fatal("expected user-slot collision error, got nil")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("want ErrValidation, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "90-user.json") || !strings.Contains(msg, `"my-proxy"`) ||
		!strings.Contains(msg, "редакторе конфигурации") {
		t.Errorf("error must honestly attribute the user-slot conflict: %s", msg)
	}
}

type recProxies struct{ removed []int }

func (r *recProxies) NextFreeIndex(context.Context, map[int]bool) (int, error) { return 0, nil }
func (r *recProxies) EnsureProxy(context.Context, int, int, string) error      { return nil }
func (r *recProxies) RemoveProxy(_ context.Context, idx int) error {
	r.removed = append(r.removed, idx)
	return nil
}

func newAdapterForRemoveTests(t *testing.T, pm ProxyRegistrar) *OperatorAdapter {
	t.Helper()
	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	return NewOperatorAdapter(orch, pm, nil)
}

// RemoveInbound снимает ровно один inbound по тегу; чужие остаются, отсутствующий
// тег — no-op без ошибки.
func TestOperatorAdapter_RemoveInbound_ByTagOnly(t *testing.T) {
	a := newAdapterForRemoveTests(t, nil)
	for _, in := range []struct {
		tag  string
		port int
	}{{"sub-in-a", 11003}, {"sub-in-b", 11004}} {
		if err := a.AddInbound(in.tag, []byte(fmt.Sprintf(`{"type":"mixed","listen":"127.0.0.1","listen_port":%d}`, in.port))); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.RemoveInbound("sub-in-a"); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveInbound("sub-in-zzz"); err != nil {
		t.Fatalf("отсутствующий тег — no-op: %v", err)
	}
	if len(a.cfg.Inbounds) != 1 || a.cfg.Inbounds[0].(map[string]any)["tag"] != "sub-in-b" {
		t.Fatalf("inbounds = %v, want только sub-in-b", a.cfg.Inbounds)
	}
}

// RemoveRouteRule снимает правило по ПАРЕ (inbound, outbound): совпадение по одному
// полю не считается.
func TestOperatorAdapter_RemoveRouteRule_MatchesPair(t *testing.T) {
	a := newAdapterForRemoveTests(t, nil)
	for _, r := range []string{
		`{"inbound":"sub-in-a","outbound":"sel-1"}`,
		`{"inbound":"sub-in-a","outbound":"sel-2"}`,
		`{"inbound":"sub-in-b","outbound":"sel-1"}`,
	} {
		if err := a.AddRouteRule([]byte(r)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.RemoveRouteRule("sub-in-a", "sel-1"); err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, v := range a.routeRules() {
		r := v.(map[string]any)
		left = append(left, r["inbound"].(string)+"→"+r["outbound"].(string))
	}
	want := []string{"sub-in-a→sel-2", "sub-in-b→sel-1"}
	if !reflect.DeepEqual(left, want) {
		t.Fatalf("rules = %v, want %v", left, want)
	}
}

func TestOperatorAdapter_RemoveProxy_DelegatesOrErrorsWithoutRegistrar(t *testing.T) {
	if err := newAdapterForRemoveTests(t, nil).RemoveProxy(context.Background(), 5); err == nil {
		t.Fatal("без ProxyRegistrar обязана быть ошибка")
	}
	pm := &recProxies{}
	if err := newAdapterForRemoveTests(t, pm).RemoveProxy(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pm.removed, []int{5}) {
		t.Fatalf("removed = %v, want [5]", pm.removed)
	}
}
