package singbox

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
)

// route.default_domain_resolver — такой же скаляр first-file-wins, как
// route.final и dns.final: 00-base.json лексически первый и затеняет
// роутер-слот. Поэтому базой ключ владеет ТОЛЬКО пока роутер его не задал.
func TestReconcileBaseDomainResolverMap(t *testing.T) {
	cases := []struct {
		name        string
		route       map[string]any
		routingOwns bool
		wantChanged bool
		wantValue   any
		wantAbsent  bool
	}{
		{
			name:        "роутер задал свой — база уступает",
			route:       map[string]any{"default_domain_resolver": "dns-bootstrap"},
			routingOwns: true,
			wantChanged: true,
			wantAbsent:  true,
		},
		{
			name:        "роутер не задал, в базе нет — база возвращает дефолт",
			route:       map[string]any{},
			routingOwns: false,
			wantChanged: true,
			wantValue:   "dns-bootstrap",
		},
		{
			name:        "роутер не задал, в базе есть — без изменений",
			route:       map[string]any{"default_domain_resolver": "dns-bootstrap"},
			routingOwns: false,
			wantChanged: false,
			wantValue:   "dns-bootstrap",
		},
		{
			name:        "роутер задал, в базе уже нет — без изменений",
			route:       map[string]any{},
			routingOwns: true,
			wantChanged: false,
			wantAbsent:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := map[string]any{"route": c.route}
			if got := reconcileBaseDomainResolverMap(m, c.routingOwns); got != c.wantChanged {
				t.Errorf("changed = %v, want %v", got, c.wantChanged)
			}
			route, _ := m["route"].(map[string]any)
			v, has := route["default_domain_resolver"]
			if c.wantAbsent && has {
				t.Errorf("default_domain_resolver присутствует (%v), ожидалось отсутствие", v)
			}
			if !c.wantAbsent && v != c.wantValue {
				t.Errorf("default_domain_resolver = %v, want %v", v, c.wantValue)
			}
		})
	}
}

// Блока route в базе нет — не материализуем чужую секцию, если владеет роутер;
// но без роутера ключ обязателен, иначе sing-box 1.13+ не стартует вовсе.
func TestReconcileBaseDomainResolverMap_NoRouteBlock(t *testing.T) {
	m := map[string]any{}
	if !reconcileBaseDomainResolverMap(m, false) {
		t.Fatal("changed = false, want true: без роутер-слота ключ обязателен")
	}
	route, _ := m["route"].(map[string]any)
	if route["default_domain_resolver"] != "dns-bootstrap" {
		t.Errorf("route = %#v", route)
	}

	m2 := map[string]any{}
	if reconcileBaseDomainResolverMap(m2, true) {
		t.Error("changed = true: при владеющем роутере пустую базу трогать незачем")
	}
}

// Сквозная проверка на РЕЗУЛЬТАТЕ МЕРЖА — того, чего не хватало: раньше
// fakeip-tun ставил свой resolver в 20-router.json, а рантайм молча оставлял
// базовый dns-bootstrap, и пользовательский FakeIPRealServer ни на что не
// влиял.
func TestMergedDomainResolver_FakeIPWins(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	writeFixtureJSON(t, filepath.Join(configDir, "20-router.json"), map[string]any{
		"dns":   map[string]any{"servers": []any{map[string]any{"type": "udp", "tag": "real", "server": "9.9.9.9"}}},
		"route": map[string]any{"default_domain_resolver": map[string]any{"server": "real"}},
	})

	for _, s := range reconcileConfigSteps(dir, configDir, "info", "", nil) {
		s.run()
	}

	merged, err := configmerge.MergeDir(configDir)
	if err != nil {
		t.Fatalf("MergeDir: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(merged), &m); err != nil {
		t.Fatal(err)
	}
	route, _ := m["route"].(map[string]any)
	got, _ := route["default_domain_resolver"].(map[string]any)
	if got == nil || got["server"] != "real" {
		t.Errorf("merged default_domain_resolver = %#v, want {server: real}", route["default_domain_resolver"])
	}
}
