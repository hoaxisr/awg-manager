package singbox

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Таблица: каждый патчер набора × три фикстуры (нет файла / файл-каталог /
// битый JSON). Семантика Р3: файла нет — тишина; файл есть, но не
// читается/не парсится — Warn с именем шага.
func TestPatchers_WarnOnBrokenFile_SilentOnMissing(t *testing.T) {
	type tc struct {
		name string // ожидаемое имя шага в Warn
		run  func(path string, log *slog.Logger)
	}
	cases := []tc{
		{"patch-base-clash-port", func(p string, l *slog.Logger) { patchBaseClashPort(p, l) }},
		{"patch-base-log-level", func(p string, l *slog.Logger) { patchBaseLogLevel(p, "info", l) }},
		{"patch-base-domain-resolver", func(p string, l *slog.Logger) { patchBaseDomainResolver(p, l) }},
		{"patch-base-direct-outbound", func(p string, l *slog.Logger) { patchBaseDirectOutbound(p, l) }},
		{"patch-base-cache-file", func(p string, l *slog.Logger) { patchBaseCacheFilePath(p, l) }},
		{"patch-base-dns-strategy", func(p string, l *slog.Logger) { patchBaseDNSStrategy(p, l) }},
		{"strip-base-owned-blocks", func(p string, l *slog.Logger) { patchTunnelsSlotStripBaseOwnedBlocks(p, l) }},
		{"remove-route-final", func(p string, l *slog.Logger) { removeFinalFromBase(p, l) }},
		{"remove-dns-final", func(p string, l *slog.Logger) { removeDNSFinalFromBase(p, l) }},
		{"outbound-compat", func(p string, l *slog.Logger) { patchTunnelsSlotEnsureNaiveUDPOverTCP(p, l) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			dir := t.TempDir()

			// 1. Файла нет — тишина.
			c.run(filepath.Join(dir, "missing.json"), log)
			if strings.Contains(buf.String(), "WARN") {
				t.Fatalf("missing file must be silent, got: %s", buf.String())
			}

			// 2. «Файл»-каталог — ReadFile падает → Warn с именем шага.
			buf.Reset()
			broken := filepath.Join(dir, "broken.json")
			if err := os.MkdirAll(broken, 0o755); err != nil {
				t.Fatal(err)
			}
			c.run(broken, log)
			if !strings.Contains(buf.String(), "WARN") || !strings.Contains(buf.String(), c.name) {
				t.Fatalf("want WARN with step %q, got: %s", c.name, buf.String())
			}

			// 3. Битый JSON → Warn с именем шага.
			buf.Reset()
			bad := filepath.Join(dir, "bad.json")
			if err := os.WriteFile(bad, []byte("{oops"), 0o644); err != nil {
				t.Fatal(err)
			}
			c.run(bad, log)
			if !strings.Contains(buf.String(), "WARN") || !strings.Contains(buf.String(), c.name) {
				t.Fatalf("want WARN with step %q on bad json, got: %s", c.name, buf.String())
			}
		})
	}
}

// stripStrayDirectPlaceholder работает по каталогу, а не по файлу: битый
// слот-файл внутри — Warn с именем шага, пустой каталог — тишина.
func TestStripStrayDirectPlaceholder_WarnsOnBrokenSlot(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	dir := t.TempDir()

	stripStrayDirectPlaceholder(dir, log)
	if strings.Contains(buf.String(), "WARN") {
		t.Fatalf("empty dir must be silent, got: %s", buf.String())
	}

	buf.Reset()
	if err := os.WriteFile(filepath.Join(dir, "10-tunnels.json"), []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	stripStrayDirectPlaceholder(dir, log)
	if !strings.Contains(buf.String(), "WARN") || !strings.Contains(buf.String(), "strip-stray-direct") {
		t.Fatalf("want WARN with step %q, got: %s", "strip-stray-direct", buf.String())
	}
}

// ensureLegacyConfigMigrated молчит без legacy-файла и предупреждает, когда
// legacy есть, но распарсить его не удалось (файл при этом остаётся на месте
// для ретрая — поведение не меняется).
func TestEnsureLegacyConfigMigrated_WarnsOnBrokenLegacy(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	dir := t.TempDir()

	ensureLegacyConfigMigrated(dir, log)
	if strings.Contains(buf.String(), "WARN") {
		t.Fatalf("missing legacy must be silent, got: %s", buf.String())
	}

	buf.Reset()
	legacy := filepath.Join(dir, "config.json")
	if err := os.WriteFile(legacy, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureLegacyConfigMigrated(dir, log)
	if !strings.Contains(buf.String(), "WARN") || !strings.Contains(buf.String(), "migrate-legacy-tunnels") {
		t.Fatalf("want WARN with step %q, got: %s", "migrate-legacy-tunnels", buf.String())
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy must stay in place for retry: %v", err)
	}
}

// routerOwnsDNSStrategy подглядывает в чужой 20-router.json три вызова вглубь;
// битый router-слот обязан всплыть Warn'ом от шага remove-dns-final.
func TestRemoveDNSFinalFromBase_WarnsOnBrokenRouterSlot(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	dir := t.TempDir()

	basePath := filepath.Join(dir, "00-base.json")
	if err := writeJSONFile(basePath, map[string]any{
		"dns": map[string]any{"strategy": "prefer_ipv4"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"), []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}

	removeDNSFinalFromBase(basePath, log)
	if !strings.Contains(buf.String(), "WARN") || !strings.Contains(buf.String(), "remove-dns-final") {
		t.Fatalf("want WARN with step %q, got: %s", "remove-dns-final", buf.String())
	}
}
