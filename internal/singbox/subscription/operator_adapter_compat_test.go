package subscription

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

func slotOutboundByTag(t *testing.T, dir, tag string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "40-subscriptions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	obs, _ := m["outbounds"].([]any)
	for _, v := range obs {
		ob, _ := v.(map[string]any)
		if ob != nil && ob["tag"] == tag {
			return ob
		}
	}
	t.Fatalf("outbound %q не найден в слоте: %s", tag, raw)
	return nil
}

// hysteria2, приехавший подпиской, обязан получить disable_chrome_parrot в
// записанном слоте: sing-box 1.14.0-beta.7 включил chrome-парротинг по
// умолчанию, и при disable_sni && !insecure туннель мёртв.
func TestSubscriptionSlot_Hysteria2GetsChromeParrotFix(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	a := NewOperatorAdapter(orch, nil, nil)

	ob := map[string]any{
		"type": "hysteria2", "server": "h.example", "server_port": float64(443),
		"password": "p",
		"tls": map[string]any{"enabled": true, "server_name": "h.example",
			"disable_sni": true, "insecure": false},
	}
	b, _ := json.Marshal(ob)
	if err := a.AddOutbound("sub-h2", b); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := a.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	got := slotOutboundByTag(t, dir, "sub-h2")
	if got["disable_chrome_parrot"] != true {
		t.Fatalf("hysteria2 из подписки обязан получить disable_chrome_parrot, got: %v", got)
	}
}

// naive из подписки обязан получить udp_over_tcp — без него UDP через naive
// мёртв (тот же фикс, что Config.Save даёт туннелям из UI).
func TestSubscriptionSlot_NaiveGetsUDPOverTCPFix(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	a := NewOperatorAdapter(orch, nil, nil)

	ob := map[string]any{
		"type": "naive", "server": "n.example", "server_port": float64(443),
		"username": "u", "password": "p",
	}
	b, _ := json.Marshal(ob)
	if err := a.AddOutbound("sub-nv", b); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := a.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	got := slotOutboundByTag(t, dir, "sub-nv")
	uot, _ := got["udp_over_tcp"].(map[string]any)
	if uot == nil || uot["enabled"] != true {
		t.Fatalf("naive из подписки обязан получить udp_over_tcp, got: %v", got)
	}
}

// UpdateOutbound — второй вход в слот; фикс обязан работать и там.
func TestSubscriptionSlot_UpdateOutboundGetsCompatFix(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	a := NewOperatorAdapter(orch, nil, nil)

	clean := map[string]any{
		"type": "hysteria2", "server": "h.example", "server_port": float64(443),
		"password": "p",
		"tls":      map[string]any{"enabled": true, "server_name": "h.example"},
	}
	b, _ := json.Marshal(clean)
	if err := a.AddOutbound("sub-h2", b); err != nil {
		t.Fatalf("add: %v", err)
	}

	dirty := map[string]any{
		"type": "hysteria2", "server": "h.example", "server_port": float64(443),
		"password": "p",
		"tls": map[string]any{"enabled": true, "server_name": "h.example",
			"disable_sni": true, "insecure": false},
	}
	b2, _ := json.Marshal(dirty)
	if err := a.UpdateOutbound("sub-h2", b2); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := a.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	got := slotOutboundByTag(t, dir, "sub-h2")
	if got["disable_chrome_parrot"] != true {
		t.Fatalf("UpdateOutbound обязан применять компат-фикс, got: %v", got)
	}
}
