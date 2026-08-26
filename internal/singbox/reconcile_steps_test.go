package singbox

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// --- фикстуры ---

func writeFixtureJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(path, v); err != nil {
		t.Fatal(err)
	}
}

// dirtyLegacyFixture — грязный легаси-моноконфиг (до config.d): device-proxy
// артефакты, наш dns-bootstrap, ipv4_only, чужой clash-порт, route.final,
// naive без udp_over_tcp, несовместимый hysteria2.
func dirtyLegacyFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixtureJSON(t, filepath.Join(dir, "config.json"), map[string]any{
		"log":          map[string]any{"level": "debug"},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": "127.0.0.1:9090"}},
		"dns": map[string]any{
			"strategy": "ipv4_only", "final": "dns-doh",
			"servers": []any{
				map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": "1.1.1.1"},
				map[string]any{"type": "udp", "tag": "my-dns", "server": "9.9.9.9"},
			},
			"rules": []any{map[string]any{"domain": []any{"example.com"}, "server": "my-dns"}},
		},
		"inbounds": []any{
			map[string]any{"type": "mixed", "tag": "device-proxy-in", "listen": "127.0.0.1", "listen_port": 1080},
			map[string]any{"type": "mixed", "tag": "proxy-nv1", "listen": "127.0.0.1", "listen_port": 2080},
		},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
			map[string]any{"type": "naive", "tag": "nv1", "server": "s", "server_port": 443},
			map[string]any{"type": "hysteria2", "tag": "h1", "server": "s", "server_port": 443,
				"tls": map[string]any{"disable_sni": true, "insecure": false}},
		},
		"route": map[string]any{"final": "direct", "rules": []any{
			map[string]any{"inbound": "device-proxy-in", "outbound": "device-proxy-out"},
			map[string]any{"inbound": "proxy-nv1", "outbound": "nv1"},
		}},
	})
	return dir
}

// dirtyTreeFixture — грязное дерево config.d: base с route.final/dns.final/
// ipv4_only/чужим clash-портом/относительным cache-путём и direct НЕ первым;
// загрязнённый 10-tunnels (log, dns-bootstrap, дубль direct, naive,
// hysteria2); 20-router.json со strategy (гейт стрижки); легаси config.json
// рядом с уже существующим 10-tunnels — ensureLegacyConfigMigrated обязан
// no-op.
func dirtyTreeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cd := filepath.Join(dir, "config.d")
	writeFixtureJSON(t, filepath.Join(cd, "00-base.json"), map[string]any{
		"log": map[string]any{"level": "debug"},
		"experimental": map[string]any{
			"clash_api":  map[string]any{"external_controller": "127.0.0.1:9090"},
			"cache_file": map[string]any{"enabled": true, "path": "cache.db"},
		},
		"dns": map[string]any{
			"strategy": "ipv4_only", "final": "dns-bootstrap",
			"servers": []any{map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": "1.1.1.1"}},
		},
		"outbounds": []any{
			map[string]any{"type": "block", "tag": "blackhole"},
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{"final": "direct"},
	})
	writeFixtureJSON(t, filepath.Join(cd, "10-tunnels.json"), map[string]any{
		"log": map[string]any{"level": "debug"},
		"dns": map[string]any{
			"strategy": "prefer_ipv4",
			"servers":  []any{map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": "1.1.1.1"}},
		},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
			map[string]any{"type": "naive", "tag": "nv1", "server": "s", "server_port": 443},
			map[string]any{"type": "hysteria2", "tag": "h1", "server": "s", "server_port": 443,
				"tls": map[string]any{"disable_sni": true, "insecure": false}},
		},
		"route": map[string]any{"rules": []any{}},
	})
	writeFixtureJSON(t, filepath.Join(cd, "20-router.json"), map[string]any{
		"dns":   map[string]any{"strategy": "ipv4_only", "final": "dns-doh"},
		"route": map[string]any{"final": "direct"},
	})
	writeFixtureJSON(t, filepath.Join(cd, "40-subscriptions.json"), map[string]any{
		"outbounds": []any{
			map[string]any{"type": "naive", "tag": "sub-nv", "server": "s", "server_port": 443},
			map[string]any{"type": "hysteria2", "tag": "sub-h2", "server": "s", "server_port": 443,
				"tls": map[string]any{"disable_sni": true, "insecure": false}},
		},
	})
	writeFixtureJSON(t, filepath.Join(dir, "config.json"), map[string]any{
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
	})
	return dir
}

// --- снапшот дерева ---

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diffTree(t *testing.T, want, got map[string]string) string {
	t.Helper()
	keys := map[string]bool{}
	for k := range want {
		keys[k] = true
	}
	for k := range got {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		if want[n] != got[n] {
			return n + ":\n--- want ---\n" + want[n] + "\n--- got ---\n" + got[n]
		}
	}
	return ""
}

// runReconcile прогоняет закреплённый первым MigrateLegacyConfigDir и
// затем шаги набора в заданном порядке.
func runReconcile(t *testing.T, dir string, reversed bool) {
	t.Helper()
	if err := MigrateLegacyConfigDir(dir); err != nil {
		t.Fatalf("MigrateLegacyConfigDir: %v", err)
	}
	steps := reconcileConfigSteps(dir, filepath.Join(dir, "config.d"), "info", "", 0, nil)
	if reversed {
		for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
			steps[i], steps[j] = steps[j], steps[i]
		}
	}
	for _, s := range steps {
		s.run()
	}
}

var reconcileFixtures = map[string]func(t *testing.T) string{
	"legacy": dirtyLegacyFixture,
	"tree":   dirtyTreeFixture,
}

// СТРАХОВКА: идемпотентность набора целиком — второй прогон не меняет ни байта.
func TestReconcileConfigSteps_IdempotentWholeSet(t *testing.T) {
	for name, mk := range reconcileFixtures {
		t.Run(name, func(t *testing.T) {
			dir := mk(t)
			runReconcile(t, dir, false)
			first := snapshotTree(t, dir)
			runReconcile(t, dir, false)
			second := snapshotTree(t, dir)
			if d := diffTree(t, first, second); d != "" {
				t.Fatalf("второй прогон изменил дерево: %s", d)
			}
		})
	}
}

// СТРАХОВКА: коммутативность — прямой и перевёрнутый порядок (кроме
// закреплённого первым MigrateLegacyConfigDir) дают идентичные деревья.
func TestReconcileConfigSteps_CommuteReversed(t *testing.T) {
	for name, mk := range reconcileFixtures {
		t.Run(name, func(t *testing.T) {
			fwdDir := mk(t)
			runReconcile(t, fwdDir, false)
			fwd := snapshotTree(t, fwdDir)

			revDir := mk(t)
			runReconcile(t, revDir, true)
			rev := snapshotTree(t, revDir)

			if d := diffTree(t, fwd, rev); d != "" {
				t.Fatalf("перевёрнутый порядок дал другое дерево: %s", d)
			}
		})
	}
}

// СТРАХОВКА: конвергенция к канону после исцеления dirtyTreeFixture.
func TestReconcileConfigSteps_ConvergesToCanon(t *testing.T) {
	dir := dirtyTreeFixture(t)
	runReconcile(t, dir, false)
	cd := filepath.Join(dir, "config.d")

	base := readJSONMap(t, filepath.Join(cd, "00-base.json"))
	route, _ := base["route"].(map[string]any)
	if route != nil {
		if _, has := route["final"]; has {
			t.Fatalf("route.final должен быть вычищен из base: %v", route)
		}
	}
	dns, _ := base["dns"].(map[string]any)
	if dns == nil {
		t.Fatalf("dns-блок base пропал: %v", base)
	}
	if _, has := dns["final"]; has {
		t.Fatalf("dns.final должен быть вычищен из base: %v", dns)
	}

	// dns.strategy есть в base XOR задана в routing-слоте.
	_, baseHasStrategy := dns["strategy"]
	routerHasStrategy := false
	if router := readJSONMap(t, filepath.Join(cd, "20-router.json")); router != nil {
		if rdns, _ := router["dns"].(map[string]any); rdns != nil {
			s, _ := rdns["strategy"].(string)
			routerHasStrategy = s != ""
		}
	}
	if baseHasStrategy == routerHasStrategy {
		t.Fatalf("dns.strategy обязана быть ровно в одном слоте: base=%v router=%v",
			baseHasStrategy, routerHasStrategy)
	}

	// Плейсхолдер direct — ровно один на дерево и первый в base.
	total := 0
	entries, err := os.ReadDir(cd)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		m := readJSONMap(t, filepath.Join(cd, e.Name()))
		obs, _ := m["outbounds"].([]any)
		for _, v := range obs {
			ob, _ := v.(map[string]any)
			if ob == nil {
				continue
			}
			if ob["type"] == "direct" && ob["tag"] == "direct" {
				total++
			}
		}
	}
	if total != 1 {
		t.Fatalf("плейсхолдер direct обязан быть ровно один на дерево, найдено %d", total)
	}
	baseObs, _ := base["outbounds"].([]any)
	if len(baseObs) == 0 {
		t.Fatalf("base без outbounds: %v", base)
	}
	first, _ := baseObs[0].(map[string]any)
	if first == nil || first["type"] != "direct" || first["tag"] != "direct" {
		t.Fatalf("первым outbound'ом base обязан быть плейсхолдер direct, got %v", baseObs[0])
	}
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

// КРАСНЫЙ ДО ФИКСА: полнота — каждый шаг набора обязан иметь фикстуру
// идемпотентности; шаг без фикстуры валит тест по имени.
func TestReconcileConfigSteps_EachStepIdempotent(t *testing.T) {
	fixtures := map[string]func(t *testing.T) string{
		stepEnsureBaseConfig: func(t *testing.T) string { // грязный base
			dir := t.TempDir()
			writeFixtureJSON(t, filepath.Join(dir, "config.d", "00-base.json"), map[string]any{
				"log":          map[string]any{"level": "debug"},
				"experimental": map[string]any{"clash_api": map[string]any{"external_controller": "127.0.0.1:9090"}},
				"dns":          map[string]any{"strategy": "ipv4_only"},
				"outbounds":    []any{map[string]any{"type": "block", "tag": "blackhole"}},
			})
			return dir
		},
		stepMigrateLegacyTunnels: func(t *testing.T) string { // legacy без config.d/10-tunnels
			dir := t.TempDir()
			writeFixtureJSON(t, filepath.Join(dir, "config.d", "00-base.json"), freshBaseConfig("info", "", 0))
			writeFixtureJSON(t, filepath.Join(dir, "config.json"), map[string]any{
				"outbounds": []any{map[string]any{"type": "naive", "tag": "nv1", "server": "s", "server_port": 443}},
				"route":     map[string]any{"rules": []any{}},
			})
			return dir
		},
		stepStripBaseOwnedBlocks: func(t *testing.T) string { // 10-tunnels с log+dns-bootstrap
			dir := t.TempDir()
			writeFixtureJSON(t, filepath.Join(dir, "config.d", "10-tunnels.json"), map[string]any{
				"log": map[string]any{"level": "debug"},
				"dns": map[string]any{
					"servers": []any{map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": "1.1.1.1"}},
				},
				"outbounds": []any{},
			})
			return dir
		},
		stepOutboundCompat: func(t *testing.T) string { // naive + hysteria2 в 10-tunnels и 40-subscriptions
			dir := t.TempDir()
			writeFixtureJSON(t, filepath.Join(dir, "config.d", "10-tunnels.json"), map[string]any{
				"outbounds": []any{
					map[string]any{"type": "naive", "tag": "nv1", "server": "s", "server_port": 443},
					map[string]any{"type": "hysteria2", "tag": "h1", "server": "s", "server_port": 443,
						"tls": map[string]any{"disable_sni": true, "insecure": false}},
				},
			})
			writeFixtureJSON(t, filepath.Join(dir, "config.d", "40-subscriptions.json"), map[string]any{
				"outbounds": []any{
					map[string]any{"type": "naive", "tag": "sub-nv", "server": "s", "server_port": 443},
					map[string]any{"type": "hysteria2", "tag": "sub-h2", "server": "s", "server_port": 443,
						"tls": map[string]any{"disable_sni": true, "insecure": false}},
				},
			})
			return dir
		},
		stepStripStrayDirect: func(t *testing.T) string { // дубль direct в 10-tunnels
			dir := t.TempDir()
			writeFixtureJSON(t, filepath.Join(dir, "config.d", "10-tunnels.json"), map[string]any{
				"outbounds": []any{
					map[string]any{"type": "direct", "tag": "direct"},
					map[string]any{"type": "vless", "tag": "v1", "server": "s", "server_port": 443, "uuid": "u"},
				},
			})
			return dir
		},
		stepRemoveRouteFinal: func(t *testing.T) string { // base c route.final
			dir := t.TempDir()
			writeFixtureJSON(t, filepath.Join(dir, "config.d", "00-base.json"), map[string]any{
				"route": map[string]any{"final": "direct", "default_domain_resolver": "dns-bootstrap"},
			})
			return dir
		},
		stepDerivedDefaults: func(t *testing.T) string { // base с нашими дефолтами + 21-fakeip со своим
			dir := t.TempDir()
			cd := filepath.Join(dir, "config.d")
			writeFixtureJSON(t, filepath.Join(cd, "00-base.json"), map[string]any{
				"dns": map[string]any{"strategy": "prefer_ipv4"},
			})
			writeFixtureJSON(t, filepath.Join(cd, "21-fakeip.json"), map[string]any{
				"dns": map[string]any{"strategy": "ipv4_only"},
			})
			return dir
		},
		stepRemoveDNSFinal: func(t *testing.T) string { // base c dns.final + 20-router со strategy
			dir := t.TempDir()
			cd := filepath.Join(dir, "config.d")
			writeFixtureJSON(t, filepath.Join(cd, "00-base.json"), map[string]any{
				"dns": map[string]any{"final": "dns-bootstrap", "strategy": "prefer_ipv4"},
			})
			writeFixtureJSON(t, filepath.Join(cd, "20-router.json"), map[string]any{
				"dns": map[string]any{"strategy": "ipv4_only"},
			})
			return dir
		},
	}

	for _, s := range reconcileConfigSteps("", "", "info", "", 0, nil) {
		mk, ok := fixtures[s.name]
		if !ok {
			t.Fatalf("шаг %q без фикстуры идемпотентности — дополните таблицу", s.name)
		}
		t.Run(s.name, func(t *testing.T) {
			dir := mk(t)
			step := findReconcileStep(t, dir, s.name)
			step.run()
			first := snapshotTree(t, dir)
			step = findReconcileStep(t, dir, s.name)
			step.run()
			second := snapshotTree(t, dir)
			if d := diffTree(t, first, second); d != "" {
				t.Fatalf("шаг %q не идемпотентен: %s", s.name, d)
			}
		})
	}
}

func findReconcileStep(t *testing.T, dir, name string) reconcileStep {
	t.Helper()
	for _, s := range reconcileConfigSteps(dir, filepath.Join(dir, "config.d"), "info", "", 0, nil) {
		if s.name == name {
			return s
		}
	}
	t.Fatalf("шаг %q не найден в наборе", name)
	return reconcileStep{}
}

// КРАСНЫЙ ДО ФИКСА (против набора, где шаг outbound-compat целится только в
// 10-tunnels.json): шаг обязан лечить ОБА слота-продюсера outbound'ов — и
// 10-tunnels.json (пишет UI), и 40-subscriptions.json (пишет подписочный
// адаптер). Идемпотентности здесь мало: no-op тоже идемпотентен, поэтому
// нацеливание закрепляется ассертом на результат.
func TestReconcileConfigSteps_OutboundCompatHealsBothSlots(t *testing.T) {
	dir := t.TempDir()
	cd := filepath.Join(dir, "config.d")
	incompatible := func() map[string]any {
		return map[string]any{
			"outbounds": []any{
				map[string]any{"type": "naive", "tag": "nv", "server": "s", "server_port": 443},
				map[string]any{"type": "hysteria2", "tag": "h2", "server": "s", "server_port": 443,
					"tls": map[string]any{"disable_sni": true, "insecure": false}},
			},
		}
	}
	writeFixtureJSON(t, filepath.Join(cd, "10-tunnels.json"), incompatible())
	writeFixtureJSON(t, filepath.Join(cd, "40-subscriptions.json"), incompatible())

	runReconcile(t, dir, false)

	for _, slot := range []string{"10-tunnels.json", "40-subscriptions.json"} {
		m := readJSONMap(t, filepath.Join(cd, slot))
		if m == nil {
			t.Fatalf("слот %s пропал", slot)
		}
		naive := findOutboundByTag(t, m, "nv")
		if _, ok := naive["udp_over_tcp"]; !ok {
			t.Fatalf("%s: naive без udp_over_tcp: %v", slot, naive)
		}
		hy2 := findOutboundByTag(t, m, "h2")
		if hy2["disable_chrome_parrot"] != true {
			t.Fatalf("%s: hysteria2 без disable_chrome_parrot: %v", slot, hy2)
		}
	}
}

func findOutboundByTag(t *testing.T, slot map[string]any, tag string) map[string]any {
	t.Helper()
	obs, _ := slot["outbounds"].([]any)
	for _, v := range obs {
		ob, _ := v.(map[string]any)
		if ob != nil && ob["tag"] == tag {
			return ob
		}
	}
	t.Fatalf("outbound %q не найден: %v", tag, slot["outbounds"])
	return nil
}
