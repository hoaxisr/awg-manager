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
	writeStub("opkg", "exit "+opkgExit)
	writeStub("logger", `echo "$@" >> `+logFile)

	script := strings.Replace(buildUpgradeScript(ipkPath, binPath), "sleep 2;", ":;", 1)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}

	raw, _ := os.ReadFile(logFile)
	_, statErr := os.Stat(ipkPath)
	return string(raw), statErr == nil
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
