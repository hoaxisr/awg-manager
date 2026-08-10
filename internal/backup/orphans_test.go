package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestKillPIDFile_ForeignProcess_NoSignal воспроизводит сценарий из отчёта:
// после ребута pid из протухшего pidfile мог достаться постороннему процессу.
// killPIDFile не должен слать ему сигналы — только убрать файл.
//
// Тест сам родитель sleep — если бы killPIDFile всё-таки послал сигнал,
// процесс стал бы зомби (IsAlive на зомби == true, kill(pid,0) не различает
// живой процесс и незареапленный труп), поэтому проверяем факт получения
// сигнала через cmd.Wait() в горутине, а не через IsAlive.
func TestKillPIDFile_ForeignProcess_NoSignal(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()

	dir := t.TempDir()
	path := filepath.Join(dir, "wdtt-client.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}

	killPIDFile(path, wdttBinaryNames)

	select {
	case <-done:
		t.Fatal("посторонний процесс (sleep) получил сигнал и завершился")
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("протухший pidfile должен быть удалён")
	}
}

// TestKillPIDFile_OwnProcess_KillsGroup — pid реально принадлежит одному из
// известных бинарей (argv0 basename совпадает) — killPIDFile должен убить его.
func TestKillPIDFile_OwnProcess_KillsGroup(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Args[0] = "wdtt-client" // argv0 basename, как если бы это был наш процесс
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()

	dir := t.TempDir()
	path := filepath.Join(dir, "wdtt-client.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}

	killPIDFile(path, wdttBinaryNames)

	select {
	case <-done:
	case <-time.After(orphanKillWait + time.Second):
		t.Fatal("свой процесс должен быть убит")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("pidfile должен быть удалён после убийства")
	}
}

// TestKillPIDFile_StalePID_JustRemoves — мёртвый pid: файл убирается без сигналов.
func TestKillPIDFile_StalePID_JustRemoves(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "1")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait() // гарантированно мёртв и реапнут

	dir := t.TempDir()
	path := filepath.Join(dir, "freeturn-server.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}

	killPIDFile(path, freeturnBinaryNames)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("pidfile мёртвого pid должен быть удалён")
	}
}

func TestKillOrphanProxyProcesses_IgnoresUnrelatedPIDFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.pid"), []byte("1"), 0644); err != nil {
		t.Fatal(err)
	}
	// Не должно паниковать и не должно трогать неизвестные pidfile'ы.
	KillOrphanProxyProcesses(dir)
	if _, err := os.Stat(filepath.Join(dir, "other.pid")); err != nil {
		t.Fatalf("неродственный pidfile не должен быть тронут: %v", err)
	}
}
