package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
			for _, s := range reconcileConfigSteps(dir, configDir, "info", "", 0, "", nil) {
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
	for _, s := range reconcileConfigSteps(dir, configDir, "info", "", 0, "", nil) {
		s.run()
	}
	m := mergedMap(t, configDir)
	dns, _ := m["dns"].(map[string]any)
	if dns["strategy"] != baseDefaultDNSStrategy {
		t.Errorf("dns.strategy = %v, want %s из 99-defaults", dns["strategy"], baseDefaultDNSStrategy)
	}
	if dns["optimistic"] != true {
		t.Errorf("dns.optimistic = %v, want true из 99-defaults", dns["optimistic"])
	}
	route, _ := m["route"].(map[string]any)
	// Наш configmerge сохраняет объектную форму как есть; sing-box сворачивает
	// её в строку — обе означают один сервер (стенд-проверено).
	res, _ := route["default_domain_resolver"].(map[string]any)
	if res == nil || res["server"] != baseDefaultDomainResolver {
		t.Errorf("default_domain_resolver = %v, want server=%s из 99-defaults",
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
	// Значение чужого резолвера сохраняется, но форма приводится к объектной:
	// 99-defaults несёт объект, а слить объект со строкой merge sing-box не
	// умеет («cannot merge json object into string», FATAL). Пока база была
	// единственным источником дефолта, коллизии не возникало.
	route, _ := base["route"].(map[string]any)
	res, _ := route["default_domain_resolver"].(map[string]any)
	if res == nil || res["server"] != "my-resolver" {
		t.Errorf("чужой резолвер обязан остаться (в объектной форме): %v", route)
	}
	m := mergedMap(t, configDir)
	mdns, _ := m["dns"].(map[string]any)
	if mdns["strategy"] != "ipv6_only" {
		t.Errorf("merged strategy = %v, want ipv6_only (база первая)", mdns["strategy"])
	}
	mroute, _ := m["route"].(map[string]any)
	mres, _ := mroute["default_domain_resolver"].(map[string]any)
	if mres == nil || mres["server"] != "my-resolver" {
		t.Errorf("merged резолвер = %v, want server=my-resolver (база первая)", mroute["default_domain_resolver"])
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
	base := freshBaseConfig("info", "", 0, defaultCacheDBPath)
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

// Тип резолвера в 99-defaults обязан совпадать с тем, что пишут режимные
// слоты (объект {"server": …}). Слить объект со строкой sing-box не умеет —
// «cannot merge json object into string», FATAL на старте. Наш configmerge
// такую пару проглатывает, поэтому ловушку держит этот тест, а не merge-тест.
func TestDerivedDefaultsSlot_ResolverIsObjectLikeSlots(t *testing.T) {
	route, _ := derivedDefaultsSlot()["route"].(map[string]any)
	res, ok := route["default_domain_resolver"].(map[string]any)
	if !ok {
		t.Fatalf("резолвер обязан быть объектом (как в 20/21), got %T", route["default_domain_resolver"])
	}
	if res["server"] != baseDefaultDomainResolver {
		t.Errorf("resolver.server = %v, want %s", res["server"], baseDefaultDomainResolver)
	}
}

// Стрижка базы обязана снимать обе исторические формы нашего значения.
func TestStripOurDerivedDefaults_BothResolverForms(t *testing.T) {
	for name, val := range map[string]any{
		"строка": baseDefaultDomainResolver,
		"объект": map[string]any{"server": baseDefaultDomainResolver},
	} {
		t.Run(name, func(t *testing.T) {
			base := map[string]any{"route": map[string]any{"default_domain_resolver": val}}
			if !stripOurDerivedDefaults(base) {
				t.Fatal("наше значение обязано сниматься")
			}
			route, _ := base["route"].(map[string]any)
			if _, has := route["default_domain_resolver"]; has {
				t.Errorf("резолвер остался: %v", route)
			}
		})
	}
}

// Историческая база несёт резолвер СТРОКОЙ. 99-defaults несёт объект, а слить
// объект со строкой merge sing-box не умеет — значит форму надо выровнять,
// сохранив значение. Без этого апгрейд установки с ручной правкой в базе
// уронил бы движок в FATAL.
func TestStripOurDerivedDefaults_NormalisesForeignStringResolver(t *testing.T) {
	base := map[string]any{"route": map[string]any{"default_domain_resolver": "custom"}}
	if !stripOurDerivedDefaults(base) {
		t.Fatal("смена формы обязана считаться изменением — иначе не запишется")
	}
	route, _ := base["route"].(map[string]any)
	res, ok := route["default_domain_resolver"].(map[string]any)
	if !ok {
		t.Fatalf("чужая строка обязана стать объектом, got %T", route["default_domain_resolver"])
	}
	if res["server"] != "custom" {
		t.Errorf("значение потеряно: %v", res)
	}
}

// derivedDefaultsSlot несёт скаляры мимо строгого DTO — прямиком в JSON. Без
// сверки со схемой форк-сборки (#806) переименование или удаление ключа
// апстримом (например, dns.optimistic) молча ушло бы в конфиг, который
// sing-box отвергает при загрузке.
func TestDerivedDefaultsSlot_KeysMatchSchema(t *testing.T) {
	raw, err := os.ReadFile("vlink/testdata/singbox-schema.json")
	if err != nil {
		t.Fatalf("read schema: %v (run scripts/regen-singbox-schema.sh)", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	defs, _ := doc["$defs"].(map[string]any)
	props, _ := doc["properties"].(map[string]any)

	resolve := func(node map[string]any) map[string]any {
		ref, _ := node["$ref"].(string)
		name := strings.TrimPrefix(ref, "#/$defs/")
		resolved, _ := defs[name].(map[string]any)
		return resolved
	}

	want := derivedDefaultsSlot()
	for _, section := range []string{"dns", "route"} {
		root, _ := props[section].(map[string]any)
		schemaProps, _ := resolve(root)["properties"].(map[string]any)
		wantKeys, _ := want[section].(map[string]any)
		for key := range wantKeys {
			if _, ok := schemaProps[key]; !ok {
				t.Errorf("%s.%s отсутствует в схеме sing-box", section, key)
			}
		}
	}
}
