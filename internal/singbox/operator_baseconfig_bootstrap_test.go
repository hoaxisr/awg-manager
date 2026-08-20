package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
			base := freshBaseConfig("info", c.bootstrap)
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
			patchBaseBootstrapDNS(path, c.want)
			patchBaseBootstrapDNS(path, c.want) // идемпотентность
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

// Записи dns-bootstrap в базе нет (пользователь её удалил) — патчер не
// выдумывает сервер: структуру файла он не проектирует, а только правит адрес.
func TestPatchBaseBootstrapDNS_NoBootstrapEntry(t *testing.T) {
	path := baseWithServers(t, t.TempDir(), []any{
		map[string]any{"type": "udp", "tag": "dns-custom", "server": "192.168.0.1"},
	})
	patchBaseBootstrapDNS(path, "8.8.8.8")

	base := readBaseFixture(t, path)
	dns, _ := base["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("dns.servers = %#v, want the single custom entry untouched", servers)
	}
	if s, _ := servers[0].(map[string]any); s["tag"] != "dns-custom" || s["server"] != "192.168.0.1" {
		t.Errorf("custom server changed: %#v", servers[0])
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
	op := NewOperator(OperatorDeps{Dir: dir})

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
