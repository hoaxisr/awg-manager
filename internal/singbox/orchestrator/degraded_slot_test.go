package orchestrator

// Тесты контракта issue #465: деградированный слот 30-deviceproxy.json
// (селектор без ссылок на композиты выключенного слота 21-routing,
// default = default-член композита) должен пережить prune байт-в-байт,
// а EnabledOutboundTags — отдавать ровно ту видимость тегов, по которой
// prune режет висячие ссылки.

import (
	"os"
	"path/filepath"
	"testing"
)

// setupDegradedOrch регистрирует awg (AlwaysOn, каталог тегов), router
// (выключен — файл припаркован в disabled/) и deviceproxy (включён).
func setupDegradedOrch(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	dir := t.TempDir()
	o := New(dir, nil)
	for _, meta := range KnownSlots() {
		switch meta.Slot {
		case SlotAwg, SlotRouting, SlotDeviceProxy:
			if err := o.Register(meta); err != nil {
				t.Fatalf("register %s: %v", meta.Slot, err)
			}
		}
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	awgJSON := []byte(`{
  "outbounds": [
    {"type": "direct", "tag": "awg-awg10", "bind_interface": "nwg10"}
  ]
}`)
	if err := os.WriteFile(filepath.Join(dir, "15-awg.json"), awgJSON, 0644); err != nil {
		t.Fatal(err)
	}

	// 21-routing.json существует, но припаркован (движок выключен).
	routerJSON := []byte(`{
  "outbounds": [
    {"type": "selector", "tag": "vpn", "outbounds": ["awg-awg10"], "default": "awg-awg10"}
  ]
}`)
	if err := os.MkdirAll(filepath.Join(dir, disabledSubdir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, disabledSubdir, "21-routing.json"), routerJSON, 0644); err != nil {
		t.Fatal(err)
	}
	o.enabled[SlotRouting] = false
	o.enabled[SlotDeviceProxy] = true
	return o, dir
}

// Деградированный слот 30 (выход генерации после FIX #465) не содержит
// висячих ссылок → prune обязан оставить его байт-в-байт: между
// выключением роутера и перегенерацией слота ничего не теряется.
func TestPrune_DegradedDeviceProxySlot_IsNoOp(t *testing.T) {
	o, dir := setupDegradedOrch(t)
	dpJSON := []byte(`{
  "inbounds": [
    {"type": "mixed", "tag": "device-proxy-office-in", "listen": "0.0.0.0", "listen_port": 1099}
  ],
  "outbounds": [
    {"type": "selector", "tag": "device-proxy-office-selector", "outbounds": ["direct", "awg-awg10"], "default": "awg-awg10"}
  ],
  "route": {"rules": [{"inbound": ["device-proxy-office-in"], "outbound": "device-proxy-office-selector"}]}
}`)
	dpPath := filepath.Join(dir, "30-deviceproxy.json")
	if err := os.WriteFile(dpPath, dpJSON, 0644); err != nil {
		t.Fatal(err)
	}

	o.mu.Lock()
	logs := o.pruneDanglingSelectorRefsLocked()
	o.mu.Unlock()

	got, err := os.ReadFile(dpPath)
	if err != nil {
		t.Fatalf("read after prune: %v", err)
	}
	if string(got) != string(dpJSON) {
		t.Errorf("degraded deviceproxy slot mutated by prune:\nwant %s\ngot  %s\nlogs: %v", dpJSON, got, logs)
	}
	if len(logs) != 0 {
		t.Errorf("prune must be a no-op on the degraded slot, logs: %v", logs)
	}
}

// Вариант FIX-3 (#465): выбранный тег недоступен и НЕ является router-
// композитом (например, subscription-selector при припаркованном слоте) —
// генерация не эмитит "default" вовсе. Такой слот 30 тоже обязан пережить
// prune байт-в-байт: нет default'а — нечего вычищать на каждом reload.
func TestPrune_DegradedDeviceProxySlot_NoDefault_IsNoOp(t *testing.T) {
	o, dir := setupDegradedOrch(t)
	dpJSON := []byte(`{
  "inbounds": [
    {"type": "mixed", "tag": "device-proxy-office-in", "listen": "0.0.0.0", "listen_port": 1099}
  ],
  "outbounds": [
    {"type": "selector", "tag": "device-proxy-office-selector", "outbounds": ["direct", "awg-awg10"]}
  ],
  "route": {"rules": [{"inbound": ["device-proxy-office-in"], "outbound": "device-proxy-office-selector"}]}
}`)
	dpPath := filepath.Join(dir, "30-deviceproxy.json")
	if err := os.WriteFile(dpPath, dpJSON, 0644); err != nil {
		t.Fatal(err)
	}

	o.mu.Lock()
	logs := o.pruneDanglingSelectorRefsLocked()
	o.mu.Unlock()

	got, err := os.ReadFile(dpPath)
	if err != nil {
		t.Fatalf("read after prune: %v", err)
	}
	if string(got) != string(dpJSON) {
		t.Errorf("no-default deviceproxy slot mutated by prune:\nwant %s\ngot  %s\nlogs: %v", dpJSON, got, logs)
	}
	if len(logs) != 0 {
		t.Errorf("prune must be a no-op on the no-default slot, logs: %v", logs)
	}
}

// Контроль: НЕдеградированный слот 30 (ссылка на vpn при выключенном
// роутере) prune по-прежнему чинит — вырезает члена и default. Это
// защита переходного окна, а не основной путь после FIX #465.
func TestPrune_DanglingCompositeRefInDeviceProxySlot_StillPruned(t *testing.T) {
	o, dir := setupDegradedOrch(t)
	dpJSON := []byte(`{
  "outbounds": [
    {"type": "selector", "tag": "device-proxy-office-selector", "outbounds": ["direct", "vpn"], "default": "vpn"}
  ]
}`)
	dpPath := filepath.Join(dir, "30-deviceproxy.json")
	if err := os.WriteFile(dpPath, dpJSON, 0644); err != nil {
		t.Fatal(err)
	}

	o.mu.Lock()
	logs := o.pruneDanglingSelectorRefsLocked()
	o.mu.Unlock()

	got, err := os.ReadFile(dpPath)
	if err != nil {
		t.Fatalf("read after prune: %v", err)
	}
	if string(got) == string(dpJSON) {
		t.Errorf("dangling composite ref survived prune:\n%s", got)
	}
	if len(logs) == 0 {
		t.Error("prune must report what it stripped")
	}
}

// EnabledOutboundTags: видимость тегов ровно как у prune — теги
// выключенных слотов отсутствуют, builtins присутствуют, exclude
// убирает собственный слот потребителя.
func TestEnabledOutboundTags_VisibilityMatchesPrune(t *testing.T) {
	o, dir := setupDegradedOrch(t)
	dpJSON := []byte(`{
  "outbounds": [
    {"type": "selector", "tag": "device-proxy-office-selector", "outbounds": ["direct", "awg-awg10"]}
  ]
}`)
	if err := os.WriteFile(filepath.Join(dir, "30-deviceproxy.json"), dpJSON, 0644); err != nil {
		t.Fatal(err)
	}

	tags := o.EnabledOutboundTags(SlotDeviceProxy)
	if !tags["awg-awg10"] {
		t.Error("awg-awg10 (AlwaysOn slot 15) must be visible")
	}
	if tags["vpn"] {
		t.Error("vpn from the PARKED router slot must not be visible")
	}
	for _, b := range []string{"direct", "block", "dns"} {
		if !tags[b] {
			t.Errorf("builtin %q missing", b)
		}
	}
	if tags["device-proxy-office-selector"] {
		t.Error("excluded slot's own tags must not be visible")
	}

	// Включаем слот 20 (переносим файл в active) — vpn появляется.
	if err := o.SetEnabledSilent(SlotRouting, true); err != nil {
		t.Fatalf("SetEnabledSilent: %v", err)
	}
	tags = o.EnabledOutboundTags(SlotDeviceProxy)
	if !tags["vpn"] {
		t.Error("vpn must be visible once the router slot is enabled")
	}
}

// EnabledOutboundTags must include ENDPOINT tags, not just outbound tags:
// validate.go collects c.Endpoints into the same tag namespace (an awg3
// endpoint is addressed by tag exactly like an outbound), so the oracle
// that guards selector/device-proxy refs against dangling-prune has to
// agree — otherwise an awg3 endpoint tag looks dangling and gets stripped.
func TestEnabledOutboundTags_IncludesEndpointTags(t *testing.T) {
	o, dir := setupDegradedOrch(t)
	awgJSON := []byte(`{
  "endpoints": [
    {"type": "wireguard", "tag": "awg-x"}
  ]
}`)
	if err := os.WriteFile(filepath.Join(dir, "15-awg.json"), awgJSON, 0644); err != nil {
		t.Fatal(err)
	}

	tags := o.EnabledOutboundTags(SlotDeviceProxy)
	if !tags["awg-x"] {
		t.Error("awg-x endpoint tag (AlwaysOn slot 15) must be visible")
	}
}
