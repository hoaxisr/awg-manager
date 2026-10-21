package orchestrator_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// F701: привязки «inbound → свой выход» (прокси для устройств, NDMS-прокси
// подписок) обязаны стоять в склеенном конфиге раньше пользовательских правил
// слотов 20/21. sing-box берёт первое совпавшее правило, и правило вида
// «youtube → X» иначе уводит трафик этих входов мимо выбранного выхода.
// Формы правил — как у продюсеров: ensureDeviceProxyInstanceRouteRule
// (singbox/config.go) и subscription.BuildRouteRule.
func TestMerge_InboundBindingsPrecedeUserRules(t *testing.T) {
	const userRule = `{"route":{"rules":[{"domain_suffix":["example.com"],"outbound":"user-X"}]}}`
	bodies := map[orchestrator.Slot]string{
		orchestrator.SlotRouter:        userRule,
		orchestrator.SlotFakeIP:        userRule,
		orchestrator.SlotDeviceProxy:   `{"route":{"rules":[{"inbound":"device-proxy-in","outbound":"device-proxy-selector"}]}}`,
		orchestrator.SlotSubscriptions: `{"route":{"rules":[{"inbound":"sub-ru-in","outbound":"sub-ru"}]}}`,
	}
	dir := t.TempDir()
	for _, m := range orchestrator.KnownSlots() {
		if body, ok := bodies[m.Slot]; ok {
			if err := os.WriteFile(filepath.Join(dir, m.Filename), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}

	merged, err := configmerge.MergeDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(merged), &cfg); err != nil {
		t.Fatal(err)
	}

	firstUser, bindings := -1, 0
	for i, r := range cfg.Route.Rules {
		if r["outbound"] == "user-X" && firstUser < 0 {
			firstUser = i
		}
		if _, ok := r["inbound"]; ok {
			bindings++
			if firstUser >= 0 {
				t.Errorf("rule %d (inbound %v) merged after user rule %d — user rules shadow it", i, r["inbound"], firstUser)
			}
		}
	}
	if firstUser < 0 || bindings != 2 {
		t.Fatalf("fixture not merged as expected: firstUser=%d bindings=%d rules=%v", firstUser, bindings, cfg.Route.Rules)
	}
}

// KnownSlots документирован как порядок загрузки, а sing-box грузит
// config.d по имени файла — расхождение списка с именами вводит в
// заблуждение каждого, кто рассуждает о приоритете слотов по списку.
func TestKnownSlots_OrderMatchesFilenames(t *testing.T) {
	slots := orchestrator.KnownSlots()
	for i := 1; i < len(slots); i++ {
		if slots[i-1].Filename >= slots[i].Filename {
			t.Errorf("KnownSlots out of load order: %s before %s", slots[i-1].Filename, slots[i].Filename)
		}
	}
}
