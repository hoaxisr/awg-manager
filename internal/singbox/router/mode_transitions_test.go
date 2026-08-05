package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// Шесть переходов между тремя режимами (все упорядоченные пары) не теряют
// пользовательскую маршрутизацию и оставляют ровно один режимный слот активным
// плюс общий. Главный инвариант подэтапа 5D0: правила, наборы и outbound'ы
// живут в общем слоте 21-routing.json, поэтому переключение режима не имеет
// права их трогать — а регресс здесь виден не в браузере, а пропавшим у
// пользователя интернетом.
func TestSwitchRoutingMode_AllSixTransitions(t *testing.T) {
	h := newTransitionHarness(t)
	dir := h.dir

	seed := `{
	  "outbounds":[
	    {"tag":"proxy-out","type":"socks","server":"1.2.3.4"}
	  ],
	  "route":{
	    "final":"proxy-out",
	    "rule_set":[{"tag":"geosite-x","type":"remote","format":"binary","url":"https://example.org/x.srs","download_detour":"direct","update_interval":"24h"}],
	    "rules":[{"action":"route","domain_suffix":["example.com"],"outbound":"proxy-out","rule_set":["geosite-x"]}]
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, "21-routing.json"), []byte(seed), 0644); err != nil {
		t.Fatalf("seed shared slot: %v", err)
	}

	h.seedState(t, stateTProxy, false)
	if err := h.svc.SwitchRoutingMode(context.Background(), stateTProxy); err != nil {
		t.Fatalf("начальный переход off→tproxy: %v", err)
	}
	assertModeSlotState(t, h, stateTProxy)
	modeSlotsSingboxCheck(t, dir, stateTProxy)

	order := []string{stateFakeIPTun, statePolicyTun, stateTProxy, statePolicyTun, stateFakeIPTun, stateTProxy}
	prev := stateTProxy
	for _, target := range order {
		if err := h.svc.SwitchRoutingMode(context.Background(), target); err != nil {
			t.Fatalf("переход %s→%s: %v", prev, target, err)
		}
		t.Logf("переход %s→%s ok", prev, target)
		assertModeSlotState(t, h, target)
		modeSlotsSingboxCheck(t, dir, target)
		prev = target
	}

	// После круга все три режимных файла существуют, два — под disabled/.
	parked := 0
	for _, name := range []string{"20-tproxy.json", "20-policytun.json", "20-fakeip.json"} {
		if _, err := os.Stat(filepath.Join(dir, "disabled", name)); err == nil {
			parked++
		}
	}
	if parked != 2 {
		t.Errorf("в disabled/ ожидалось два режимных файла, найдено %d", parked)
	}
}

func assertModeSlotState(t *testing.T, h *transitionHarness, mode string) {
	t.Helper()

	want := ModeSlot(mode)
	active := map[orchestrator.Slot]bool{}
	for _, st := range h.svc.deps.Orch.Snapshot() {
		if st.Enabled {
			active[st.Slot] = true
		}
	}
	for _, slot := range modeSlots() {
		if slot == want && !active[slot] {
			t.Errorf("режим %s: слот %s обязан быть активен", mode, slot)
		}
		if slot != want && active[slot] {
			t.Errorf("режим %s: посторонний режимный слот %s активен", mode, slot)
		}
	}
	if !active[orchestrator.SlotRouting] {
		t.Errorf("режим %s: общий слот 21-routing выключен", mode)
	}

	raw, err := os.ReadFile(filepath.Join(h.dir, "21-routing.json"))
	if err != nil {
		t.Fatalf("режим %s: читать общий слот: %v", mode, err)
	}
	var cfg RouterConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("режим %s: разбор общего слота: %v", mode, err)
	}
	if cfg.Route.Final != "proxy-out" {
		t.Errorf("режим %s: route.final = %q, want proxy-out", mode, cfg.Route.Final)
	}
	found := false
	for _, r := range cfg.Route.Rules {
		if len(r.DomainSuffix) == 1 && r.DomainSuffix[0] == "example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("режим %s: пользовательское правило потеряно:\n%s", mode, raw)
	}
	if len(cfg.Route.RuleSet) != 1 || cfg.Route.RuleSet[0].Tag != "geosite-x" {
		t.Errorf("режим %s: набор правил потерян: %+v", mode, cfg.Route.RuleSet)
	}
	hasProxy := false
	for _, o := range cfg.Outbounds {
		if o.Tag == "proxy-out" {
			hasProxy = true
		}
	}
	if !hasProxy {
		t.Errorf("режим %s: outbound proxy-out потерян: %+v", mode, cfg.Outbounds)
	}
}

// modeSlotsSingboxCheck прогоняет активный набор слотов через настоящий
// sing-box check (когда бинарь доступен) и статически проверяет ссылки
// domain_resolver.
//
// Статическая проверка нужна отдельно от бинаря: висячую ссылку у DNS-сервера
// `check` пропускает (падает только `run`), а висячий
// route.default_domain_resolver в слоте вообще маскируется скаляром
// 00-base.json — при слиянии first-file-wins побеждает базовый, и ни check, ни
// run её не показывают.
func modeSlotsSingboxCheck(t *testing.T, srcDir, mode string) {
	t.Helper()
	dir := t.TempDir()
	base := map[string]any{
		"log": map[string]any{"level": "warn"},
		"dns": map[string]any{
			"strategy": "prefer_ipv4",
			"servers":  []any{map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": "1.1.1.1"}},
		},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"default_domain_resolver": "dns-bootstrap"},
	}
	writeSlotJSON(t, dir, "00-base.json", base)

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	tags := map[string]bool{"dns-bootstrap": true}
	files := map[string]*RouterConfig{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0644); err != nil {
			t.Fatalf("copy %s: %v", e.Name(), err)
		}
		var cfg RouterConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		files[e.Name()] = &cfg
		for _, s := range cfg.DNS.Servers {
			tags[s.Tag] = true
		}
	}
	for name, cfg := range files {
		if r := cfg.Route.DefaultDomainResolver; r != nil && !tags[r.Server] {
			t.Errorf("режим %s: %s route.default_domain_resolver → %q, такого DNS-сервера нет", mode, name, r.Server)
		}
		for _, o := range cfg.Outbounds {
			if o.DomainResolver != nil && !tags[o.DomainResolver.Server] {
				t.Errorf("режим %s: %s outbound %q domain_resolver → %q, такого DNS-сервера нет", mode, name, o.Tag, o.DomainResolver.Server)
			}
		}
		for _, s := range cfg.DNS.Servers {
			if s.DomainResolver != nil && !tags[s.DomainResolver.Server] {
				t.Errorf("режим %s: %s dns-сервер %q domain_resolver → %q, такого DNS-сервера нет", mode, name, s.Tag, s.DomainResolver.Server)
			}
		}
	}
	t.Logf("режим %s: слоты %v", mode, slotFileNames(files))
	singboxCheck(t, dir)
}

func slotFileNames(m map[string]*RouterConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
