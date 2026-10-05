package events

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runHookScript кладёт embed-скрипт в <root>/<hook>.d/ с каталогом spool,
// подменённым на каталог spool (имя файла то же), и исполняет его настоящим
// /bin/sh с пустым PATH: любая внешняя утилита в скрипте упадёт «not found».
func runHookScript(t *testing.T, root, hook, spool string, env ...string) (string, error) {
	t.Helper()
	return runHookScriptBody(t, hookScriptContent, root, hook, spool, env...)
}

// runHookScriptBody — то же для изменённого текста скрипта.
func runHookScriptBody(t *testing.T, script, root, hook, spool string, env ...string) (string, error) {
	t.Helper()
	if !strings.Contains(script, DefaultSpoolPath) {
		t.Fatalf("скрипт пишет не в %s", DefaultSpoolPath)
	}
	dir := filepath.Join(root, hook+".d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "50-awg-manager.sh")
	if filepath.Base(spool) != filepath.Base(DefaultSpoolPath) {
		t.Fatalf("spool %s: имя файла не %s", spool, filepath.Base(DefaultSpoolPath))
	}
	body := strings.ReplaceAll(script, filepath.Dir(DefaultSpoolPath), filepath.Dir(spool))
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
	spool := filepath.Join(root, "run", "hooks", "ndm-hooks")
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
	// t= — аптайм из /proc/uptime (В1): у живой системы он больше нуля.
	if ev.ScriptUptime <= 0 {
		t.Fatalf("t=: аптайм не дошёл: %q", lines[0])
	}
	ev.ScriptUptime = 0
	if ev != (Event{Type: EventIfCreated, ID: "Wireguard1", SystemName: "nwg1"}) {
		t.Fatalf("event: %#v", ev)
	}
}

// Скрипт исполняется NDMS на каждый хук и не должен ни fork'аться, ни
// блокироваться (F571): в командной позиции — только встроенные read, echo,
// exit и присваивания. Прогон с пустым PATH (выше) ловит внешнюю утилиту
// лишь на исполненной ветке; здесь — весь текст.
func TestHookScript_BuiltinsOnly(t *testing.T) {
	if !strings.Contains(hookScriptContent, "&t=${up}") {
		t.Fatal("в строке нет &t=${up}")
	}
	allowed := map[string]bool{"read": true, "echo": true, "exit": true}
	cont := false
	for i, line := range strings.Split(hookScriptContent, "\n") {
		trimmed := strings.TrimSpace(line)
		wasCont := cont
		cont = strings.HasSuffix(trimmed, "\\")
		if wasCont || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, cmd := range strings.Split(trimmed, ";") {
			word, _, _ := strings.Cut(strings.TrimSpace(cmd), " ")
			if word == "" {
				continue
			}
			if k, _, ok := strings.Cut(word, "="); ok && k != "" && !strings.ContainsAny(k, "$\"'") {
				continue // присваивание
			}
			if !allowed[word] {
				t.Errorf("строка %d: команда %q — не встроенная из разрешённых", i+1, word)
			}
		}
	}
}

// /proc/uptime не открылся: t= пустой, а не значение переменной up, которую
// NDMS сам передаёт хукам; строка всё равно пишется, stderr пуст.
func TestHookScript_UptimeUnreadable_EmptyT(t *testing.T) {
	root := t.TempDir()
	spool := filepath.Join(root, "run", "hooks", "ndm-hooks")
	if err := os.MkdirAll(filepath.Dir(spool), 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hookScriptContent, "</proc/uptime") {
		t.Fatal("скрипт не читает /proc/uptime")
	}
	script := strings.ReplaceAll(hookScriptContent, "</proc/uptime", "<"+filepath.Join(root, "no-uptime"))

	out, err := runHookScriptBody(t, script, root, "ifipchanged", spool, "id=PPPoE0", "up=1", "address=203.0.113.9")
	if err != nil || out != "" {
		t.Fatalf("exit: %v, вывод: %q", err, out)
	}
	data, err := os.ReadFile(spool)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(string(data), "\n")
	if !strings.HasSuffix(line, "&t=") {
		t.Fatalf("t= не пустой: %q", line)
	}
	if ev, err := ParseHookForm(spoolValues(line)); err != nil || ev.ScriptUptime != 0 {
		t.Fatalf("ScriptUptime: %v, %v", ev.ScriptUptime, err)
	}
}

// Демон не запущен — каталога spool нет (Stop его сносит): хук молча выходит
// с 0, не держит очередь NDMS, ничего не создаёт и ничего не пишет в stderr.
func TestHookScript_NoSpoolDir_ExitZero(t *testing.T) {
	root := t.TempDir()
	run := filepath.Join(root, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(run, "hooks", "ndm-hooks")
	out, err := runHookScript(t, root, "ifdestroyed", spool, "id=Wireguard1")
	if err != nil || out != "" {
		t.Fatalf("exit: %v, вывод: %q", err, out)
	}
	if _, err := os.Stat(filepath.Dir(spool)); !os.IsNotExist(err) {
		t.Fatalf("каталог spool создан: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run, "ndm-hooks")); !os.IsNotExist(err) {
		t.Fatalf("spool создан мимо каталога: %v", err)
	}
}
