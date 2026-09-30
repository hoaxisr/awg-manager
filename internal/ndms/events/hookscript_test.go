package events

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runHookScript кладёт embed-скрипт в <root>/<hook>.d/ с путём spool,
// подменённым на spool, и исполняет его настоящим /bin/sh с пустым PATH:
// любая внешняя утилита в скрипте упадёт «not found».
func runHookScript(t *testing.T, root, hook, spool string, env ...string) (string, error) {
	t.Helper()
	if !strings.Contains(hookScriptContent, DefaultSpoolPath) {
		t.Fatalf("скрипт пишет не в %s", DefaultSpoolPath)
	}
	dir := filepath.Join(root, hook+".d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "50-awg-manager.sh")
	body := strings.Replace(hookScriptContent, DefaultSpoolPath, spool, 1)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", path)
	cmd.Env = append([]string{"PATH="}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestHookScript_AppendsOneLine(t *testing.T) {
	root := t.TempDir()
	spool := filepath.Join(root, "run", "ndm-hooks")
	if err := os.MkdirAll(filepath.Dir(spool), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runHookScript(t, root, "ifcreated", spool,
		"id=Wireguard1", "system_name=nwg1", "layer=", "level=", "address=")
	if err != nil || out != "" {
		t.Fatalf("exit: %v, вывод: %q", err, out)
	}
	data, err := os.ReadFile(spool)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 1 || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("spool: want одну строку с \\n, got %q", data)
	}
	v, err := url.ParseQuery(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	ev, err := ParseHookForm(v)
	if err != nil {
		t.Fatal(err)
	}
	if ev != (Event{Type: EventIfCreated, ID: "Wireguard1", SystemName: "nwg1"}) {
		t.Fatalf("event: %#v", ev)
	}
}

// Демон не запущен, каталога spool нет — хук молча выходит с 0 и не держит
// очередь NDMS; ничего не создаёт и ничего не пишет в stderr.
func TestHookScript_NoSpoolDir_ExitZero(t *testing.T) {
	root := t.TempDir()
	spool := filepath.Join(root, "missing", "ndm-hooks")
	out, err := runHookScript(t, root, "ifdestroyed", spool, "id=Wireguard1")
	if err != nil || out != "" {
		t.Fatalf("exit: %v, вывод: %q", err, out)
	}
	if _, err := os.Stat(filepath.Dir(spool)); !os.IsNotExist(err) {
		t.Fatalf("каталог spool создан: %v", err)
	}
}
