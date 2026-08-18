package procres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func probeScript(t *testing.T, dir string, line string, rc int) string {
	t.Helper()
	body := fmt.Sprintf("case \"$1\" in --awgm-protocol) echo '%s'; exit %d;; esac\nexit 3\n", line, rc)
	return writeScript(t, dir, "bin.sh", body)
}

func TestGateAcceptsMatchingProbe(t *testing.T) {
	dir := t.TempDir()
	bin := probeScript(t, dir,
		`{"v":1,"impl":"wt-client","role":"client","modes":["raw","wg"],"commands":["state","attach-tun","detach-tun"]}`, 0)
	g := NewGate()
	if err := g.Check(context.Background(), bin, "wt-client", "client", []string{"state", "attach-tun"}); err != nil {
		t.Fatal(err)
	}
}

func TestGateRejects(t *testing.T) {
	cases := []struct {
		name, line string
		rc         int
		impl, role string
		need       []string
		wantErr    string
	}{
		{"чужой мажор", `{"v":2,"impl":"wt-client","role":"client","commands":["state"]}`, 0,
			"wt-client", "client", []string{"state"}, "верси"},
		{"чужой impl", `{"v":1,"impl":"wdtt-server","role":"server","commands":["state"]}`, 0,
			"wt-client", "client", []string{"state"}, "impl"},
		{"нет нужной команды", `{"v":1,"impl":"wt-client","role":"client","commands":["state"]}`, 0,
			"wt-client", "client", []string{"attach-tun"}, "attach-tun"},
		{"ненулевой код возврата", `{"v":1,"impl":"wt-client","role":"client","commands":["state"]}`, 1,
			"wt-client", "client", []string{"state"}, "проб"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := probeScript(t, dir, c.line, c.rc)
			err := NewGate().Check(context.Background(), bin, c.impl, c.role, c.need)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.wantErr)) {
				t.Fatalf("Check = %v, ожидали упоминание %q", err, c.wantErr)
			}
		})
	}
}

func TestGateDoesNotCacheProbeExecFailure(t *testing.T) {
	// M8: ошибка ЗАПУСКА пробы (ENOEXEC и родня) — временная, кэшировать её
	// нельзя. Подменяем содержимое файла при НЕИЗМЕННЫХ mtime и размере:
	// если бы вердикт кэшировался, вторая проверка залипла бы на первом отказе.
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	okScript := "#!/bin/sh\ncase \"$1\" in --awgm-protocol) echo '{\"v\":1,\"impl\":\"wt-client\",\"role\":\"client\",\"commands\":[\"state\"]}'; exit 0;; esac\nexit 3\n"
	garbage := strings.Repeat("\x00", len(okScript)) // тот же размер, не исполняется как скрипт
	if err := os.WriteFile(bin, []byte(garbage), 0o755); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1700000000, 0)
	if err := os.Chtimes(bin, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	g := NewGate()
	if err := g.Check(context.Background(), bin, "wt-client", "client", []string{"state"}); err == nil {
		t.Fatal("мусорный бинарь обязан провалить пробу")
	}
	if err := os.WriteFile(bin, []byte(okScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(bin, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(context.Background(), bin, "wt-client", "client", []string{"state"}); err != nil {
		t.Fatalf("ошибка запуска пробы залипла в кэше: %v", err)
	}
}

func TestGateCachesByFileIdentity(t *testing.T) {
	// Проба стоит запуска процесса; на слабом железе гонять её на каждый
	// прогон нельзя. Кэш — по (путь, mtime, размер): подмена бинаря обязана
	// сбрасывать вердикт.
	dir := t.TempDir()
	bin := probeScript(t, dir,
		`{"v":1,"impl":"wt-client","role":"client","commands":["state","attach-tun","detach-tun"]}`, 0)
	g := NewGate()
	if err := g.Check(context.Background(), bin, "wt-client", "client", []string{"state"}); err != nil {
		t.Fatal(err)
	}
	// Подменяем бинарь на говорящий на другой версии.
	probeScript(t, dir, `{"v":2,"impl":"wt-client","role":"client","commands":["state"]}`, 0)
	if err := g.Check(context.Background(), bin, "wt-client", "client", []string{"state"}); err == nil {
		t.Fatal("подмена бинаря обязана сбрасывать кэш вердикта")
	}
}
