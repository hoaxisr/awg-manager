package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
)

// Главный инвариант всей схемы, сквозной — через НАСТОЯЩИЙ merge: слот режима
// со своим ключом обязан победить дефолт, а без владельца обязан действовать
// дефолт. Пока это держится, примирять 00-base не нужно ничем.
func TestMergedScalars_SlotWinsOverDefaults(t *testing.T) {
	// Оба режимных слота отдельно: fakeip-tun пишет резолвер в 21-fakeip.json,
	// а 20-router.json в этом режиме припаркован — фикстура только по
	// router-слоту оставляла бы боевой путь непроверенным.
	for _, slot := range []string{"20-router.json", "21-fakeip.json"} {
		t.Run(slot, func(t *testing.T) {
			dir := t.TempDir()
			configDir := filepath.Join(dir, "config.d")
			writeFixtureJSON(t, filepath.Join(configDir, slot), map[string]any{
				"dns": map[string]any{
					"strategy": "ipv4_only",
					"servers":  []any{map[string]any{"type": "udp", "tag": "real", "server": "9.9.9.9"}},
				},
				"route": map[string]any{"default_domain_resolver": map[string]any{"server": "real"}},
			})
			for _, s := range reconcileConfigSteps(dir, configDir, "info", "", nil) {
				s.run()
			}
			m := mergedMap(t, configDir)
			route, _ := m["route"].(map[string]any)
			got, _ := route["default_domain_resolver"].(map[string]any)
			if got == nil || got["server"] != "real" {
				t.Errorf("default_domain_resolver = %#v, want {server: real}", route["default_domain_resolver"])
			}
			dns, _ := m["dns"].(map[string]any)
			if dns["strategy"] != "ipv4_only" {
				t.Errorf("dns.strategy = %v, want ipv4_only (слот владеет)", dns["strategy"])
			}
		})
	}
}

func TestMergedScalars_DefaultsApplyWithoutOwner(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	for _, s := range reconcileConfigSteps(dir, configDir, "info", "", nil) {
		s.run()
	}
	m := mergedMap(t, configDir)
	dns, _ := m["dns"].(map[string]any)
	if dns["strategy"] != baseDefaultDNSStrategy {
		t.Errorf("dns.strategy = %v, want %s из 99-defaults", dns["strategy"], baseDefaultDNSStrategy)
	}
	route, _ := m["route"].(map[string]any)
	if route["default_domain_resolver"] != baseDefaultDomainResolver {
		t.Errorf("default_domain_resolver = %v, want %s из 99-defaults",
			route["default_domain_resolver"], baseDefaultDomainResolver)
	}
}

// Апгрейд существующей установки: наши значения из базы уезжают, дефолт
// подхватывает 99 — merged не меняется.
func TestReconcileDerivedDefaults_MigratesOurValuesOutOfBase(t *testing.T) {
	for _, legacy := range []string{"prefer_ipv4", "ipv4_only"} {
		t.Run(legacy, func(t *testing.T) {
			dir := t.TempDir()
			configDir := filepath.Join(dir, "config.d")
			basePath := filepath.Join(configDir, "00-base.json")
			writeFixtureJSON(t, basePath, map[string]any{
				"dns":   map[string]any{"strategy": legacy},
				"route": map[string]any{"default_domain_resolver": baseDefaultDomainResolver},
			})

			reconcileDerivedDefaults(configDir)

			base := readJSONMap(t, basePath)
			dns, _ := base["dns"].(map[string]any)
			if _, has := dns["strategy"]; has {
				t.Errorf("наше значение strategy обязано уехать из базы: %v", dns)
			}
			route, _ := base["route"].(map[string]any)
			if _, has := route["default_domain_resolver"]; has {
				t.Errorf("наш резолвер обязан уехать из базы: %v", route)
			}
			if got := readJSONMap(t, filepath.Join(configDir, "99-defaults.json")); got == nil {
				t.Fatal("99-defaults.json не создан")
			}
		})
	}
}

// Чужое значение — осознанный выбор пользователя: остаётся в базе и продолжает
// затенять (00-base лексически первый). Раньше strategy сносилась безусловно,
// а резолвер берёгся — несимметричность устранена в пользу бережного варианта.
func TestReconcileDerivedDefaults_KeepsUserValues(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	basePath := filepath.Join(configDir, "00-base.json")
	writeFixtureJSON(t, basePath, map[string]any{
		"dns":   map[string]any{"strategy": "ipv6_only"},
		"route": map[string]any{"default_domain_resolver": "my-resolver"},
	})

	reconcileDerivedDefaults(configDir)

	base := readJSONMap(t, basePath)
	dns, _ := base["dns"].(map[string]any)
	if dns["strategy"] != "ipv6_only" {
		t.Errorf("чужая strategy обязана остаться: %v", dns)
	}
	route, _ := base["route"].(map[string]any)
	if route["default_domain_resolver"] != "my-resolver" {
		t.Errorf("чужой резолвер обязан остаться: %v", route)
	}
	m := mergedMap(t, configDir)
	mdns, _ := m["dns"].(map[string]any)
	if mdns["strategy"] != "ipv6_only" {
		t.Errorf("merged strategy = %v, want ipv6_only (база первая)", mdns["strategy"])
	}
}

// Шаг гоняется каждый бут — второй прогон обязан быть no-op по содержимому.
func TestReconcileDerivedDefaults_Idempotent(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	writeFixtureJSON(t, filepath.Join(configDir, "00-base.json"), map[string]any{
		"dns": map[string]any{"strategy": baseDefaultDNSStrategy},
	})

	reconcileDerivedDefaults(configDir)
	first, err := os.ReadFile(filepath.Join(configDir, "99-defaults.json"))
	if err != nil {
		t.Fatal(err)
	}
	baseFirst, err := os.ReadFile(filepath.Join(configDir, "00-base.json"))
	if err != nil {
		t.Fatal(err)
	}

	reconcileDerivedDefaults(configDir)
	second, _ := os.ReadFile(filepath.Join(configDir, "99-defaults.json"))
	baseSecond, _ := os.ReadFile(filepath.Join(configDir, "00-base.json"))
	if string(first) != string(second) {
		t.Errorf("99-defaults изменился на повторе:\n%s\n%s", first, second)
	}
	if string(baseFirst) != string(baseSecond) {
		t.Errorf("00-base изменился на повторе:\n%s\n%s", baseFirst, baseSecond)
	}
}

// Свежая установка не должна нести оба скаляра в базе: там они лежали бы в
// ВЫИГРЫВАЮЩЕЙ позиции merge и затеняли бы выбор режимного слота.
func TestFreshBaseConfig_OmitsDerivedScalars(t *testing.T) {
	base := freshBaseConfig("info", "")
	dns, _ := base["dns"].(map[string]any)
	if _, has := dns["strategy"]; has {
		t.Errorf("свежая база не должна нести dns.strategy: %v", dns)
	}
	route, _ := base["route"].(map[string]any)
	if _, has := route["default_domain_resolver"]; has {
		t.Errorf("свежая база не должна нести default_domain_resolver: %v", route)
	}
}

func mergedMap(t *testing.T, configDir string) map[string]any {
	t.Helper()
	merged, err := configmerge.MergeDir(configDir)
	if err != nil {
		t.Fatalf("MergeDir: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(merged), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
