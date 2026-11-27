package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runUpgradeScript выполняет скрипт апгрейда реальным sh со стабами
// opkg/logger в PATH. Возвращает содержимое лога стаба logger.
func runUpgradeScript(t *testing.T, opkgExit string, binContent []byte) (loggerOut string, ipkKept bool) {
	t.Helper()
	r := runUpgradeScriptFull(t, opkgExit, binContent)
	return r.logger, r.ipkKept
}

type upgradeScriptRun struct {
	logger   string // что получил стаб logger
	opkgArgs string // вызовы opkg по строке на вызов
	stdout   string // то, что ушло бы в /tmp/awg-manager-upgrade.log
	exitCode int
	ipkKept  bool
}

// runUpgradeScriptFull — то же, но с кодом выхода, выводом и вызовами opkg.
// opkg update всегда успешен; opkgExit — код opkg install.
func runUpgradeScriptFull(t *testing.T, opkgExit string, binContent []byte) upgradeScriptRun {
	t.Helper()
	dir := t.TempDir()

	ipkPath := filepath.Join(dir, "awg-manager.ipk")
	if err := os.WriteFile(ipkPath, []byte("ipk"), 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(dir, "awg-manager")
	if binContent != nil {
		if err := os.WriteFile(binPath, binContent, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	stubs := filepath.Join(dir, "stubs")
	if err := os.Mkdir(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "logger.out")
	writeStub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	argsFile := filepath.Join(dir, "opkg.args")
	writeStub("opkg", `echo "$@" >> `+argsFile+`
[ "$1" = update ] && exit 0
exit `+opkgExit)
	writeStub("logger", `echo "$@" >> `+logFile)

	script := strings.Replace(buildUpgradeScript(ipkPath, binPath), "sleep 2;", ":;", 1)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("script failed to run: %v\n%s", err, out)
	}

	raw, _ := os.ReadFile(logFile)
	args, _ := os.ReadFile(argsFile)
	_, statErr := os.Stat(ipkPath)
	return upgradeScriptRun{logger: string(raw), opkgArgs: string(args), stdout: string(out), exitCode: code, ipkKept: statErr == nil}
}

func elfBytes() []byte {
	return append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 64)...)
}

// Успех: opkg ok + валидный ELF — IPK удалён, в syslog успех.
func TestUpgradeScript_SuccessRemovesIPK(t *testing.T) {
	out, kept := runUpgradeScript(t, "0", elfBytes())
	if kept {
		t.Error("ipk must be removed after verified install")
	}
	if strings.Contains(out, "FAILED") || !strings.Contains(out, "awg-manager") {
		t.Errorf("logger out = %q, want success line", out)
	}
}

// #571: opkg «успешен», но бинарь 0 байт — IPK сохранён, громкий syslog.
func TestUpgradeScript_EmptyBinaryFails(t *testing.T) {
	out, kept := runUpgradeScript(t, "0", []byte{})
	if !kept {
		t.Error("ipk must be kept when binary is broken")
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("logger out = %q, want FAILED line", out)
	}
}

// Не-ELF мусор вместо бинаря — тоже провал верификации.
func TestUpgradeScript_GarbageBinaryFails(t *testing.T) {
	out, kept := runUpgradeScript(t, "0", []byte("#!/bin/sh\nexit 0\n"))
	if !kept || !strings.Contains(out, "FAILED") {
		t.Errorf("garbage binary: kept=%v logger=%q, want kept+FAILED", kept, out)
	}
}

// Ошибка самого opkg — IPK сохранён, громкий syslog с кодом.
func TestUpgradeScript_OpkgErrorFails(t *testing.T) {
	out, kept := runUpgradeScript(t, "9", elfBytes())
	if !kept {
		t.Error("ipk must be kept when opkg fails")
	}
	if !strings.Contains(out, "FAILED") || !strings.Contains(out, "9") {
		t.Errorf("logger out = %q, want FAILED with rc=9", out)
	}
}

// F933: список пакетов обновляется до установки, иначе зависимости качаются
// по протухшему списку.
func TestUpgradeScript_UpdatesListsBeforeInstall(t *testing.T) {
	r := runUpgradeScriptFull(t, "0", elfBytes())
	if r.exitCode != 0 {
		t.Fatalf("exit = %d, want 0; out=%q", r.exitCode, r.stdout)
	}
	lines := strings.Split(strings.TrimSpace(r.opkgArgs), "\n")
	if len(lines) != 2 || lines[0] != "update" || !strings.HasPrefix(lines[1], "install ") {
		t.Fatalf("opkg calls = %q, want update then install", r.opkgArgs)
	}
}

// F933: провал виден демону — ненулевой код и итог в выводе (логе установки).
func TestUpgradeScript_FailureExitsNonZeroWithMarker(t *testing.T) {
	for _, tc := range []struct {
		name, opkgExit string
		bin            []byte
	}{
		{"opkg error", "255", elfBytes()},
		{"corrupt binary", "0", []byte{}},
	} {
		r := runUpgradeScriptFull(t, tc.opkgExit, tc.bin)
		if r.exitCode == 0 || !strings.Contains(r.stdout, "upgrade FAILED: opkg rc="+tc.opkgExit) {
			t.Errorf("%s: exit=%d stdout=%q, want non-zero and FAILED marker", tc.name, r.exitCode, r.stdout)
		}
	}
}
