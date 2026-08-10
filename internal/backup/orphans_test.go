package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
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

// TestKillPIDFile_EscalationKillsHelperGroup нацелен именно на group-семантику
// эскалации (TerminateGroup/KillGroup), которую TestKillPIDFile_OwnProcess_KillsGroup
// не проверяет: одиночный sleep без своей сессии умирает от ЛЮБОГО варианта
// (Terminate/Kill или TerminateGroup/KillGroup) — сигнал доходит напрямую,
// group-часть кода никогда не задействуется.
//
// Здесь «наш» процесс (argv0 == wdtt-client, как реальные wdtt/freeturn
// процессы стартует с Setsid — childproc.SetProcessGroup, поэтому pid
// становится и pid, и pgid сессии) игнорирует SIGTERM и форкает хелпера в той
// же группе, который тоже игнорирует SIGTERM (но не SIGKILL). Первая фаза
// (TerminateGroup, SIGTERM) не убивает НИКОГО — вынуждает killPIDFile
// дождаться orphanKillWait и эскалировать. Escalation обязана убить ВСЮ
// группу (KillGroup, SIGKILL) — если бы вместо неё стоял одиночный Kill(pid),
// хелпер (другой pid) остался бы жить вечно, и тест бы завис на своём
// bounded-poll и упал по таймауту.
func TestKillPIDFile_EscalationKillsHelperGroup(t *testing.T) {
	dir := t.TempDir()
	helperPidPath := filepath.Join(dir, "helper.pid")

	// Родитель игнорирует TERM и форкает в той же группе (без своего Setsid)
	// хелпера, тоже игнорирующего TERM (диспозиция SIG_IGN переживает exec) —
	// оба обязаны пережить фазу SIGTERM и дожить до эскалации SIGKILL.
	script := "trap '' TERM\n" +
		"( trap '' TERM; exec sleep 60 ) &\n" +
		"echo $! > \"$1\"\n" +
		"wait\n"
	cmd := exec.Command("/bin/sh", "-c", script, "sh0", helperPidPath)
	cmd.Args[0] = "wdtt-client" // argv0 basename, как если бы это был наш процесс
	childproc.SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sh: %v", err)
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = childproc.KillGroup(pid) // страховка, если тест упадёт раньше эскалации
		<-done
	})

	var helperPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(helperPidPath)
		if err == nil && strings.TrimSpace(string(raw)) != "" {
			if v, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				helperPID = v
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if helperPID == 0 {
		t.Fatal("хелпер не записал свой pid вовремя")
	}

	path := filepath.Join(dir, "wdtt-client.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	killPIDFile(path, wdttBinaryNames)
	if elapsed := time.Since(start); elapsed < orphanKillWait {
		t.Fatalf("killPIDFile вернулся раньше orphanKillWait (%v) — эскалация не произошла: %v", orphanKillWait, elapsed)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("родительский процесс должен быть убит эскалацией")
	}

	// MatchesBinary — не IsAlive/procfs-state: kill(pid,0) успешен и для
	// зомби, а голый /proc/pid/stat рискует поймать переиспользованный чужой
	// pid. bounded-poll — доставка SIGKILL асинхронна.
	killed := make(chan struct{})
	go func() {
		for childproc.MatchesBinary(helperPID, "sleep") {
			time.Sleep(20 * time.Millisecond)
		}
		close(killed)
	}()
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatalf("хелпер (pid %d) в той же группе всё ещё жив — group-kill не сработал", helperPID)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("pidfile должен быть удалён после убийства")
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
