package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// F146: удаление инстанса убирает его сокет, журнал, pid и каталог состояния,
// не задевая файлы соседа с тем же impl/role. Имена берутся у той же функции,
// что и фабрика, а не из литералов.
func TestProxyRemoveRuntime(t *testing.T) {
	dir := t.TempDir()
	mine, err := proxyRuntimePathsFor(dir, instancestore.KindFreeTurnClient, "a")
	if err != nil {
		t.Fatal(err)
	}
	touch := func(p string) {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	touch(mine.sock)
	touch(mine.log)
	touch(mine.pid)
	if err := os.MkdirAll(mine.state, 0o700); err != nil {
		t.Fatal(err)
	}
	touch(filepath.Join(mine.state, "client_config.json"))
	other, _ := proxyRuntimePathsFor(dir, instancestore.KindFreeTurnClient, "b")
	touch(other.sock)

	rm := proxyRemoveRuntime(dir, func(instancestore.Kind) string { return "" })
	if err := rm(instancestore.Record{ID: "a", Kind: instancestore.KindFreeTurnClient}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, p := range []string{mine.sock, mine.log, mine.pid, mine.state} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s остался", filepath.Base(p))
		}
	}
	if _, err := os.Stat(other.sock); err != nil {
		t.Errorf("сосед задет: %v", err)
	}
	// Повторный вызов на пустом месте — не ошибка: pid Runner.Stop уже снял.
	if err := rm(instancestore.Record{ID: "a", Kind: instancestore.KindFreeTurnClient}); err != nil {
		t.Fatalf("повтор: %v", err)
	}
	if err := rm(instancestore.Record{ID: "a", Kind: instancestore.Kind("nope")}); err == nil {
		t.Fatal("неизвестная роль должна давать ошибку, а не молчать")
	}
}

// F146 (ревю): ребёнок, переживший teardown (удаление в окне старта), гасится
// по pid-файлу до снятия файлов — иначе сирота жила бы до ребута без pid и
// сокета, и следующий инстанс с тем же ID не смог бы её ни усыновить, ни убить.
func TestProxyRemoveRuntimeKillsSurvivor(t *testing.T) {
	dir := t.TempDir()
	p, err := proxyRuntimePathsFor(dir, instancestore.KindFreeTurnClient, "a")
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "60")
	childproc.SetProcessGroup(child)
	if err := child.Start(); err != nil {
		t.Skipf("sleep недоступен: %v", err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _, _ = child.Process.Wait() })
	// Сразу после Start /proc/<pid>/cmdline ещё пуст (замер: 274/300 на
	// холостой машине), и fail-closed MatchesBinary объявит ребёнка чужим —
	// RemoveRuntime, опознающий по /proc, его не тронет. Выживший в сценарии
	// теста уже работает, поэтому ждём, пока /proc его покажет.
	deadline := time.Now().Add(5 * time.Second)
	for !childproc.MatchesBinary(child.Process.Pid, "sleep") {
		if time.Now().After(deadline) {
			t.Fatal("ребёнок так и не стал опознаваем по /proc")
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.WriteFile(p.pid, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}

	rm := proxyRemoveRuntime(dir, func(instancestore.Kind) string { return "sleep" })
	if err := rm(instancestore.Record{ID: "a", Kind: instancestore.KindFreeTurnClient}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ребёнок пережил удаление инстанса")
	}
	if _, err := os.Lstat(p.pid); !os.IsNotExist(err) {
		t.Error("pid-файл остался")
	}
}
