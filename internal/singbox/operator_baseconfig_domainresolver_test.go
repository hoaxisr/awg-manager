package singbox

import (
	"encoding/json"
	"os"
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
func TestMergedDomainResolver_RoutingSlotWins(t *testing.T) {
	// Оба слота проверяются отдельно: fakeip-tun пишет резолвер в
	// 21-fakeip.json (orchestrator.SlotFakeIP), а 20-router.json в этом
	// режиме припаркован — фикстура только по router-слоту оставляла бы
	// боевой путь непроверенным.
	for _, slot := range []string{"20-router.json", "21-fakeip.json"} {
		t.Run(slot, func(t *testing.T) {
			dir := t.TempDir()
			configDir := filepath.Join(dir, "config.d")
			writeFixtureJSON(t, filepath.Join(configDir, slot), map[string]any{
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
		})
	}
}

// Рантайм-примирение (то, что зовёт роутер-сервис после активации слота):
// база уступает ключ без перезапуска демона, и повторный вызов ничего не
// меняет.
func TestOperator_ReconcileBaseDomainResolver(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	op := NewOperator(OperatorDeps{Dir: dir})
	basePath := filepath.Join(configDir, "00-base.json")
	if v := readBaseFixture(t, basePath)["route"].(map[string]any)["default_domain_resolver"]; v != "dns-bootstrap" {
		t.Fatalf("предусловие: база должна нести свой ключ, got %v", v)
	}

	writeFixtureJSON(t, filepath.Join(configDir, "21-fakeip.json"), map[string]any{
		"route": map[string]any{"default_domain_resolver": map[string]any{"server": "real"}},
	})
	if err := op.ReconcileBaseDomainResolver(); err != nil {
		t.Fatalf("ReconcileBaseDomainResolver: %v", err)
	}
	route, _ := readBaseFixture(t, basePath)["route"].(map[string]any)
	if v, has := route["default_domain_resolver"]; has {
		t.Errorf("база не уступила ключ: %v", v)
	}

	if err := op.ReconcileBaseDomainResolver(); err != nil {
		t.Fatalf("повторный вызов: %v", err)
	}

	// Слот исчез (роутер выключен) — ключ возвращается, иначе резолвера
	// доменных адресов не остаётся ни у кого.
	if err := os.Remove(filepath.Join(configDir, "21-fakeip.json")); err != nil {
		t.Fatal(err)
	}
	if err := op.ReconcileBaseDomainResolver(); err != nil {
		t.Fatalf("после снятия слота: %v", err)
	}
	route, _ = readBaseFixture(t, basePath)["route"].(map[string]any)
	if route["default_domain_resolver"] != "dns-bootstrap" {
		t.Errorf("ключ не вернулся: %#v", route)
	}
}

// Уступать роутеру можно только СВОЁ значение. Если пользователь вписал в
// базу собственный резолвер, стирать его нельзя: это ручная правка, а её
// восстановление константой дало бы ссылку на несуществующий тег — на боевой
// сборке 1.14.0-beta.17-awgm.13 это FATAL «default domain resolver not found»
// (проверено на стенде 2026-08-20).
func TestReconcileBaseDomainResolverMap_KeepsUserValue(t *testing.T) {
	m := map[string]any{"route": map[string]any{"default_domain_resolver": "my-dns"}}
	if reconcileBaseDomainResolverMap(m, true) {
		t.Error("changed = true: чужое значение уступать нельзя")
	}
	route, _ := m["route"].(map[string]any)
	if route["default_domain_resolver"] != "my-dns" {
		t.Errorf("default_domain_resolver = %v, want my-dns", route["default_domain_resolver"])
	}

	// Объектная форма {"server": ...} — тоже не наша, не трогаем.
	m2 := map[string]any{"route": map[string]any{
		"default_domain_resolver": map[string]any{"server": "my-dns"},
	}}
	if reconcileBaseDomainResolverMap(m2, true) {
		t.Error("changed = true: объектную форму тоже уступать нельзя")
	}
}

// ApplyLogLevel пересоздаёт базу, если файла нет, — и свежая база несёт свой
// default_domain_resolver. Если ключом уже владеет слот fakeip, база обязана
// уступить сразу, иначе затенение возвращается до следующего перезапуска.
func TestOperator_ApplyLogLevel_YieldsResolverToRoutingSlot(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	writeFixtureJSON(t, filepath.Join(configDir, "21-fakeip.json"), map[string]any{
		"dns":   map[string]any{"servers": []any{map[string]any{"type": "udp", "tag": "real", "server": "9.9.9.9"}}},
		"route": map[string]any{"default_domain_resolver": map[string]any{"server": "real"}},
	})
	op := NewOperator(OperatorDeps{Dir: dir})
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.Remove(basePath); err != nil {
		t.Fatal(err)
	}

	if err := op.ApplyLogLevel("debug"); err != nil {
		t.Fatalf("ApplyLogLevel: %v", err)
	}
	base := readBaseFixture(t, basePath)
	route, _ := base["route"].(map[string]any)
	if v, has := route["default_domain_resolver"]; has {
		t.Errorf("база вернула себе ключ (%v) при владеющем слоте fakeip", v)
	}
}
