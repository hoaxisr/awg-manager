package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// bootstrapServerOf извлекает адрес сервера dns-bootstrap из базового конфига.
func bootstrapServerOf(t *testing.T, base map[string]any) string {
	t.Helper()
	dns, ok := base["dns"].(map[string]any)
	if !ok {
		t.Fatalf("dns block missing: %#v", base["dns"])
	}
	servers, _ := dns["servers"].([]any)
	for _, v := range servers {
		s, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if s["tag"] == "dns-bootstrap" {
			addr, _ := s["server"].(string)
			return addr
		}
	}
	t.Fatalf("dns-bootstrap server missing: %#v", servers)
	return ""
}

// Адрес bootstrap-резолвера настраиваемый: 1.1.1.1 в некоторых регионах
// блокируется у мобильных операторов (issue #770). Пустая настройка
// оставляет исторический дефолт.
func TestFreshBaseConfig_BootstrapServer(t *testing.T) {
	cases := []struct {
		name      string
		bootstrap string
		want      string
	}{
		{"пусто — исторический дефолт", "", "1.1.1.1"},
		{"настроенный адрес", "8.8.8.8", "8.8.8.8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := freshBaseConfig("info", c.bootstrap, 0, defaultCacheDBPath)
			if got := bootstrapServerOf(t, base); got != c.want {
				t.Errorf("dns-bootstrap.server = %q, want %q", got, c.want)
			}
		})
	}
}

// baseWithServers собирает 00-base.json с заданным списком dns.servers.
func baseWithServers(t *testing.T, dir string, servers []any) string {
	t.Helper()
	path := filepath.Join(dir, "00-base.json")
	writeFixtureJSON(t, path, map[string]any{
		"dns":   map[string]any{"strategy": "prefer_ipv4", "servers": servers},
		"route": map[string]any{"default_domain_resolver": "dns-bootstrap"},
	})
	return path
}

func bootstrapEntry(addr string) any {
	return map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": addr}
}

// Шаг примирения владеет адресом bootstrap только когда настройка задана:
// до её появления адрес правили руками прямо в файле, и такие правки обязаны
// выжить.
func TestPatchBaseBootstrapDNS(t *testing.T) {
	cases := []struct {
		name    string
		servers []any
		want    string // настройка
		expect  string // адрес в файле после патча
	}{
		{
			name:    "настройка пуста — файл не трогаем",
			servers: []any{bootstrapEntry("9.9.9.9")},
			want:    "",
			expect:  "9.9.9.9",
		},
		{
			name:    "настройка задана — адрес приводится к ней",
			servers: []any{bootstrapEntry("1.1.1.1")},
			want:    "8.8.8.8",
			expect:  "8.8.8.8",
		},
		{
			name:    "адрес уже совпадает — no-op",
			servers: []any{bootstrapEntry("8.8.8.8")},
			want:    "8.8.8.8",
			expect:  "8.8.8.8",
		},
		{
			name:    "чужие серверы рядом не задеты",
			servers: []any{map[string]any{"type": "udp", "tag": "dns-custom", "server": "192.168.0.1"}, bootstrapEntry("1.1.1.1")},
			want:    "77.88.8.8",
			expect:  "77.88.8.8",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := baseWithServers(t, t.TempDir(), c.servers)
			var before time.Time
			if c.want == "" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				before = info.ModTime()
				time.Sleep(10 * time.Millisecond)
			}
			patchBaseBootstrapDNS(path, c.want)
			patchBaseBootstrapDNS(path, c.want) // идемпотентность
			if c.want == "" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !info.ModTime().Equal(before) {
					t.Errorf("файл переписан при пустой настройке и существующей записи: %v → %v", before, info.ModTime())
				}
			}
			base := readBaseFixture(t, path)
			if got := bootstrapServerOf(t, base); got != c.expect {
				t.Errorf("dns-bootstrap.server = %q, want %q", got, c.expect)
			}
			dns, _ := base["dns"].(map[string]any)
			servers, _ := dns["servers"].([]any)
			if len(servers) != len(c.servers) {
				t.Errorf("dns.servers = %d entries, want %d", len(servers), len(c.servers))
			}
		})
	}
}

// Записи dns-bootstrap в базе нет (пользователь её удалил) — патчер её
// ВОССТАНАВЛИВАЕТ (F44): 99-defaults.json ссылается на этот тег безусловно,
// и без записи наш же валидатор блокирует каждый reload/cold start
// unknown-dns-server. Соседний пользовательский сервер не задет.
func TestPatchBaseBootstrapDNS_NoBootstrapEntry(t *testing.T) {
	path := baseWithServers(t, t.TempDir(), []any{
		map[string]any{"type": "udp", "tag": "dns-custom", "server": "192.168.0.1"},
	})
	patchBaseBootstrapDNS(path, "8.8.8.8")

	base := readBaseFixture(t, path)
	if got := bootstrapServerOf(t, base); got != "8.8.8.8" {
		t.Errorf("dns-bootstrap.server = %q, want 8.8.8.8 (запись должна быть создана)", got)
	}
	dns, _ := base["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("dns.servers = %#v, want 2 entries (custom + созданный bootstrap)", servers)
	}
	foundCustom := false
	for _, v := range servers {
		s, _ := v.(map[string]any)
		if s["tag"] == "dns-custom" {
			foundCustom = true
			if s["server"] != "192.168.0.1" {
				t.Errorf("custom server changed: %#v", s)
			}
		}
	}
	if !foundCustom {
		t.Error("dns-custom server missing after self-heal")
	}
}

// Записи нет и настройка пуста — создаём с историческим дефолтом, а не
// оставляем движок без резолвера.
func TestPatchBaseBootstrapDNS_NoBootstrapEntry_EmptySetting(t *testing.T) {
	path := baseWithServers(t, t.TempDir(), []any{
		map[string]any{"type": "udp", "tag": "dns-custom", "server": "192.168.0.1"},
	})
	patchBaseBootstrapDNS(path, "")

	base := readBaseFixture(t, path)
	if got := bootstrapServerOf(t, base); got != defaultBootstrapDNS {
		t.Errorf("dns-bootstrap.server = %q, want %q (исторический дефолт)", got, defaultBootstrapDNS)
	}
}

// Блока dns вообще нет — недостающие уровни достраиваются, а не молчим.
func TestPatchBaseBootstrapDNS_NoDNSBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "00-base.json")
	writeFixtureJSON(t, path, map[string]any{"log": map[string]any{"level": "info"}})

	patchBaseBootstrapDNS(path, "8.8.8.8")

	base := readBaseFixture(t, path)
	if got := bootstrapServerOf(t, base); got != "8.8.8.8" {
		t.Errorf("dns-bootstrap.server = %q, want 8.8.8.8", got)
	}
}

// readBaseFixture читает 00-base.json из временного каталога.
func readBaseFixture(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Настройка доезжает до конфига обоими путями: при первой записи файла и
// при примирении уже существующего.
func TestOperator_BootstrapDNSReachesBaseConfig(t *testing.T) {
	t.Run("свежий config.d", func(t *testing.T) {
		dir := t.TempDir()
		NewOperator(OperatorDeps{Dir: dir, BootstrapDNS: func() string { return "8.8.8.8" }})
		base := readBaseFixture(t, filepath.Join(dir, "config.d", "00-base.json"))
		if got := bootstrapServerOf(t, base); got != "8.8.8.8" {
			t.Errorf("dns-bootstrap.server = %q, want 8.8.8.8", got)
		}
	})

	t.Run("существующий файл примиряется", func(t *testing.T) {
		dir := t.TempDir()
		path := baseWithServers(t, filepath.Join(dir, "config.d"), []any{bootstrapEntry("1.1.1.1")})
		NewOperator(OperatorDeps{Dir: dir, BootstrapDNS: func() string { return "77.88.8.8" }})
		base := readBaseFixture(t, path)
		if got := bootstrapServerOf(t, base); got != "77.88.8.8" {
			t.Errorf("dns-bootstrap.server = %q, want 77.88.8.8", got)
		}
	})
}

// Смена настройки в рантайме: адрес доезжает до файла без перезапуска
// демона. Пустое значение снимает владение и файл не трогает.
func TestOperator_ApplyBootstrapDNS(t *testing.T) {
	dir := t.TempDir()
	path := baseWithServers(t, filepath.Join(dir, "config.d"), []any{bootstrapEntry("1.1.1.1")})
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir})

	if err := op.ApplyBootstrapDNS("8.8.8.8"); err != nil {
		t.Fatalf("ApplyBootstrapDNS: %v", err)
	}
	if got := bootstrapServerOf(t, readBaseFixture(t, path)); got != "8.8.8.8" {
		t.Errorf("после применения dns-bootstrap.server = %q, want 8.8.8.8", got)
	}

	if err := op.ApplyBootstrapDNS(""); err != nil {
		t.Fatalf("ApplyBootstrapDNS(\"\"): %v", err)
	}
	if got := bootstrapServerOf(t, readBaseFixture(t, path)); got != "8.8.8.8" {
		t.Errorf("снятие настройки переписало файл: %q, want 8.8.8.8", got)
	}
}

// Запись dns-bootstrap удалена руками (файл при этом на месте) —
// ApplyBootstrapDNS создаёт её заново: рантайм-путь обязан лечить тот же
// дефект F44, что и бутовый патчер, а не только адрес существующей записи.
func TestOperator_ApplyBootstrapDNS_NoBootstrapEntry_CreatesIt(t *testing.T) {
	dir := t.TempDir()
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir})
	basePath := filepath.Join(dir, "config.d", "00-base.json")

	base := readBaseFixture(t, basePath)
	dns, _ := base["dns"].(map[string]any)
	dns["servers"] = []any{map[string]any{"type": "udp", "tag": "dns-custom", "server": "192.168.0.1"}}
	raw, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(basePath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := op.ApplyBootstrapDNS("8.8.8.8"); err != nil {
		t.Fatalf("ApplyBootstrapDNS: %v", err)
	}
	got := readBaseFixture(t, basePath)
	if addr := bootstrapServerOf(t, got); addr != "8.8.8.8" {
		t.Errorf("dns-bootstrap.server = %q, want 8.8.8.8 (запись должна быть создана заново)", addr)
	}
	servers, _ := got["dns"].(map[string]any)["servers"].([]any)
	foundCustom := false
	for _, v := range servers {
		s, _ := v.(map[string]any)
		if s["tag"] == "dns-custom" {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Error("dns-custom server missing after self-heal")
	}
}

// Пересоздание базы на пути ApplyLogLevel (файл удалён руками, снесён при
// переустановке движка) не должно терять настройку: оператор обязан помнить
// адрес, а не подставлять исторический дефолт.
func TestOperator_ApplyLogLevel_KeepsBootstrapDNS(t *testing.T) {
	dir := t.TempDir()
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir, BootstrapDNS: func() string { return "8.8.8.8" }})
	basePath := filepath.Join(dir, "config.d", "00-base.json")
	if err := os.Remove(basePath); err != nil {
		t.Fatal(err)
	}

	if err := op.ApplyLogLevel("debug"); err != nil {
		t.Fatalf("ApplyLogLevel: %v", err)
	}
	if got := bootstrapServerOf(t, readBaseFixture(t, basePath)); got != "8.8.8.8" {
		t.Errorf("dns-bootstrap.server = %q, want 8.8.8.8", got)
	}
}

// Контракт «менять нечего → не писать»: на нём висит вся защита от лишних
// перезаписей файла и лишних reload'ов sing-box на каждом буте и сохранении
// настроек. Мутационный прогон показал, что через содержимое файла он не
// наблюдается, поэтому проверяется решение напрямую.
func TestSetBootstrapServer_ReportsChange(t *testing.T) {
	cases := []struct {
		name string
		base map[string]any
		want bool
	}{
		{
			name: "адрес другой — меняем",
			base: map[string]any{"dns": map[string]any{"servers": []any{bootstrapEntry("1.1.1.1")}}},
			want: true,
		},
		{
			name: "адрес совпадает — не трогаем",
			base: map[string]any{"dns": map[string]any{"servers": []any{bootstrapEntry("8.8.8.8")}}},
			want: false,
		},
		{
			name: "записи dns-bootstrap нет — не трогаем",
			base: map[string]any{"dns": map[string]any{"servers": []any{
				map[string]any{"type": "udp", "tag": "dns-custom", "server": "192.168.0.1"},
			}}},
			want: false,
		},
		{
			name: "блока dns нет — не трогаем",
			base: map[string]any{},
			want: false,
		},
		{
			name: "серверов нет вовсе — не трогаем",
			base: map[string]any{"dns": map[string]any{"strategy": "prefer_ipv4"}},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := setBootstrapServer(c.base, "8.8.8.8"); got != c.want {
				t.Errorf("setBootstrapServer = %v, want %v", got, c.want)
			}
		})
	}
}

// Пропал только файл, каталог на месте — базу ВОССТАНАВЛИВАЕМ вместе с
// применяемой настройкой. Прежде ApplyBootstrapDNS/ApplyClashPort здесь молча
// выходили: настройка оставалась в settings.json, но до базы не доезжала до
// следующего бута, — при том что ApplyLogLevel базу пересоздавал.
func TestOperator_ApplyBootstrapDNS_NoBaseFile_RestoresIt(t *testing.T) {
	dir := t.TempDir()
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir, SingboxLogLevel: func() string { return "debug" }})
	basePath := filepath.Join(dir, "config.d", "00-base.json")
	if err := os.Remove(basePath); err != nil {
		t.Fatal(err)
	}
	if err := op.ApplyBootstrapDNS("8.8.8.8"); err != nil {
		t.Fatalf("ApplyBootstrapDNS без файла = %v, want nil", err)
	}
	base := readBaseFixture(t, basePath)
	if got := bootstrapServerOf(t, base); got != "8.8.8.8" {
		t.Errorf("dns-bootstrap.server = %q, want 8.8.8.8", got)
	}
	// Восстановленная база не должна терять прочие условно-свои скаляры.
	logBlock, _ := base["log"].(map[string]any)
	if lvl, _ := logBlock["level"].(string); lvl != "debug" {
		t.Errorf("log.level = %q, want debug (настройка потеряна при восстановлении)", lvl)
	}
}

// Нет самого config.d — движок удалён вместе с каталогом. Правка настройки не
// должна его воскрешать: молчим и ничего не создаём. Прежде ApplyLogLevel
// именно воскрешал.
func TestOperator_ApplyBaseScalars_NoConfigDir_StaysSilent(t *testing.T) {
	dir := t.TempDir()
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir})
	// config.d сносим ПОСЛЕ подключения оркестратора: Bootstrap успевает
	// увидеть каталог (иначе упал бы сам), проверяемое здесь молчание —
	// про снос каталога в рантайме, уже после старта.
	configDir := filepath.Join(dir, "config.d")
	if err := os.RemoveAll(configDir); err != nil {
		t.Fatal(err)
	}
	for name, apply := range map[string]func() error{
		"ApplyBootstrapDNS": func() error { return op.ApplyBootstrapDNS("8.8.8.8") },
		"ApplyLogLevel":     func() error { return op.ApplyLogLevel("debug") },
		"ApplyClashPort":    func() error { return op.ApplyClashPort(9091) },
	} {
		if err := apply(); err != nil {
			t.Errorf("%s без каталога = %v, want nil", name, err)
		}
		if _, err := os.Stat(configDir); !os.IsNotExist(err) {
			t.Fatalf("%s воскресил config.d", name)
		}
	}
}

// Набор шагов примирения гоняется каждый бут. Существующие харнессы
// идемпотентности передают пустой bootstrapDNS, из-за чего новый подшаг в них
// гарантированный no-op — здесь набор гоняется с непустой настройкой и с
// routing-слотом, владеющим резолвером, то есть по обеим новым веткам.
func TestReconcileConfigSteps_IdempotentWithBootstrapDNS(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	baseWithServers(t, configDir, []any{bootstrapEntry("1.1.1.1")})
	writeFixtureJSON(t, filepath.Join(configDir, "21-fakeip.json"), map[string]any{
		"route": map[string]any{"default_domain_resolver": map[string]any{"server": "real"}},
	})

	run := func() {
		for _, s := range reconcileConfigSteps(dir, configDir, "info", "8.8.8.8", 0, "", nil) {
			s.run()
		}
	}
	run()
	first := snapshotTree(t, dir)
	run()
	if d := diffTree(t, first, snapshotTree(t, dir)); d != "" {
		t.Fatalf("набор не идемпотентен при заданном bootstrapDNS: %s", d)
	}

	base := readBaseFixture(t, filepath.Join(configDir, "00-base.json"))
	if got := bootstrapServerOf(t, base); got != "8.8.8.8" {
		t.Errorf("dns-bootstrap.server = %q, want 8.8.8.8", got)
	}
	route, _ := base["route"].(map[string]any)
	if v, has := route["default_domain_resolver"]; has {
		t.Errorf("база держит ключ (%v) при владеющем слоте fakeip", v)
	}
}

// Настройка может прийти невалидной из settings.json (downgrade, ручная
// правка): API её больше не отвергает, чтобы не запирать пользователя вне
// всех настроек, — значит отсеивать обязан оператор. Иначе домен уезжает в
// 00-base.json, и sing-box падает: «missing domain resolver for domain server
// address» (проверено настоящим бинарём).
func TestOperator_BootstrapDNS_IgnoresNonIP(t *testing.T) {
	for _, bad := range []string{"dns.google", "8.8.8.8:53", "не адрес"} {
		t.Run(bad, func(t *testing.T) {
			dir := t.TempDir()
			op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir, BootstrapDNS: func() string { return bad }})
			basePath := filepath.Join(dir, "config.d", "00-base.json")
			if got := bootstrapServerOf(t, readBaseFixture(t, basePath)); got != "1.1.1.1" {
				t.Errorf("свежая база: dns-bootstrap.server = %q, want 1.1.1.1", got)
			}

			// И рантайм-путь тоже.
			if err := op.ApplyBootstrapDNS(bad); err != nil {
				t.Fatalf("ApplyBootstrapDNS: %v", err)
			}
			if got := bootstrapServerOf(t, readBaseFixture(t, basePath)); got != "1.1.1.1" {
				t.Errorf("после применения: dns-bootstrap.server = %q, want 1.1.1.1", got)
			}
		})
	}
}

// Контракт «менять нечего → не писать» на уровне ФАКТА записи, а не
// возвращаемого значения: лишняя запись означает лишний reload sing-box.
func TestOperator_ApplyBootstrapDNS_DoesNotRewriteWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := baseWithServers(t, filepath.Join(dir, "config.d"), []any{bootstrapEntry("8.8.8.8")})
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir})

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := op.ApplyBootstrapDNS("8.8.8.8"); err != nil {
		t.Fatalf("ApplyBootstrapDNS: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("файл переписан при неизменном адресе: %v → %v", before.ModTime(), after.ModTime())
	}

	// Контроль: при другом адресе запись обязана произойти.
	if err := op.ApplyBootstrapDNS("9.9.9.9"); err != nil {
		t.Fatalf("ApplyBootstrapDNS: %v", err)
	}
	changed, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ModTime().Equal(before.ModTime()) {
		t.Error("файл не переписан при смене адреса")
	}
}

// 00-base.json с литеральным null — валидный JSON, дающий nil-карту. Запись в
// неё паникует. У прежнего ApplyLogLevel был свой nil-guard, и при сведении к
// общему mutateBase он терялся; тест на старом коде падает паникой.
func TestOperator_ApplyBaseScalars_NullBaseDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir})
	basePath := filepath.Join(dir, "config.d", "00-base.json")
	if err := os.WriteFile(basePath, []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := op.ApplyLogLevel("debug"); err != nil {
		t.Fatalf("ApplyLogLevel на null-базе: %v", err)
	}
	base := readBaseFixture(t, basePath)
	logBlock, _ := base["log"].(map[string]any)
	if lvl, _ := logBlock["level"].(string); lvl != "debug" {
		t.Errorf("log.level = %q, want debug", lvl)
	}
}

// Интеграционный пин F44: набор шагов бутового примирения самолечит
// отсутствующую запись dns-bootstrap так, что merged-конфиг проходит НАШ
// собственный кросс-слотовый валидатор. На старом коде (патчер не создаёт
// отсутствующую запись) это репро факта §0 плана — валидатор давал бы
// unknown-dns-server на ссылку 99-defaults.json → route.default_domain_resolver.
func TestReconcileConfigSteps_HealsMissingBootstrapEntry(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	baseWithServers(t, configDir, []any{
		map[string]any{"type": "udp", "tag": "dns-custom", "server": "192.168.0.1"},
	})

	for _, s := range reconcileConfigSteps(dir, configDir, "info", "", 0, "", nil) {
		s.run()
	}

	proc := NewProcess("", configDir, filepath.Join(dir, "singbox.pid"))
	orch := singboxorch.NewWithAppliedPath(configDir, proc, filepath.Join(t.TempDir(), "singbox-applied.json"))
	for _, meta := range singboxorch.KnownSlots() {
		switch meta.Slot {
		case singboxorch.SlotBase, singboxorch.SlotDefaults:
		default:
			continue
		}
		if err := orch.Register(meta); err != nil {
			t.Fatalf("register %s: %v", meta.Slot, err)
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	res := orch.Validate()
	if !res.Ok() {
		t.Fatalf("merged config невалиден после самолечения: %s", res.Error())
	}
}

// mutateBase больше не пишет 00-base.json напрямую мимо оркестратора: без
// него правка отвергается ошибкой, а не молчаливой прямой записью плюс
// reload (тот же класс дефекта, что уже снят в writeTunnelsSlot).
func TestMutateBase_WithoutOrchestratorFails(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir})
	basePath := filepath.Join(dir, "config.d", "00-base.json")
	before, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}

	if err := op.ApplyLogLevel("debug"); err == nil {
		t.Fatal("ApplyLogLevel без оркестратора = nil, want ошибку")
	}

	after, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("00-base.json переписан без оркестратора: было %s, стало %s", before, after)
	}
}

// Не-объектный dns — чужое содержимое неизвестной формы: самолечение записи
// НЕ должно подменять его нашей картой. Такой 00-base.json движок и так не
// загрузит, но затирать пользовательское мы не вправе (F44, ревью ветки).
// Литеральный null — не содержимое, а отсутствие: его достраиваем.
//
// Краснеет на мутации «убрать гарды has/hasServers» — dns:"user-string"
// заменяется картой с нашей записью.
func TestReconcileBootstrapServer_LeavesNonObjectDNSAlone(t *testing.T) {
	t.Run("dns строкой", func(t *testing.T) {
		base := map[string]any{"dns": "user-string"}
		if reconcileBootstrapServer(base, "9.9.9.9") {
			t.Fatal("не-объектный dns объявлен изменённым")
		}
		if base["dns"] != "user-string" {
			t.Fatalf("чужое содержимое dns затёрто: %#v", base["dns"])
		}
	})
	t.Run("servers строкой", func(t *testing.T) {
		base := map[string]any{"dns": map[string]any{"servers": "oops"}}
		if reconcileBootstrapServer(base, "9.9.9.9") {
			t.Fatal("не-массивные servers объявлены изменёнными")
		}
		dns, _ := base["dns"].(map[string]any)
		if dns["servers"] != "oops" {
			t.Fatalf("чужое содержимое dns.servers затёрто: %#v", dns["servers"])
		}
	})
	t.Run("dns null достраивается", func(t *testing.T) {
		base := map[string]any{"dns": nil}
		if !reconcileBootstrapServer(base, "9.9.9.9") {
			t.Fatal("null-dns не достроен, ожидалось создание записи")
		}
		dns, _ := base["dns"].(map[string]any)
		servers, _ := dns["servers"].([]any)
		if len(servers) != 1 {
			t.Fatalf("запись не создана: %#v", dns)
		}
	})
}
