package obfuscator

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type logSpy struct {
	mu    sync.Mutex
	lines []string
}

func (s *logSpy) AppLog(_ logging.Level, _, _, action, _, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, action+": "+msg)
}

func (s *logSpy) has(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// fakeBinary — sh-скрипт: печатает аргументы в stderr и спит.
func fakeBinary(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "awgm-wg-obfuscator-phobos")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho \"started $*\" >&2\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestRunner(t *testing.T) (*Runner, *logSpy) {
	t.Helper()
	setTestDirs(t)
	bin := fakeBinary(t)
	spy := &logSpy{}
	r := NewRunner(RunnerDeps{
		BinaryFor: func(context.Context, string) (string, error) { return bin, nil },
		Log:       logging.NewScopedLogger(spy, logging.GroupTunnel, logging.SubOps),
	})
	// Фейк — sh-скрипт: argv0 = sh, а не awgm-wg-obfuscator-*; без шва Stop не
	// послал бы SIGTERM, а Alive был бы ложно-false (см. childproc/pidmatch.go).
	r.matchFn = func(int) bool { return true }
	t.Cleanup(func() { _ = r.Stop("awg20") })
	return r, spy
}

func obf() *storage.Obfuscator {
	return &storage.Obfuscator{Flavor: "phobos", Target: "h:1", Key: "k", Masking: "STUN", LocalPort: 39050}
}

func TestRunner_StartStop(t *testing.T) {
	r, spy := newTestRunner(t)
	if err := r.Start(context.Background(), "awg20", obf()); err != nil {
		t.Fatal(err)
	}
	if !r.Alive("awg20") {
		t.Fatal("not alive after start")
	}
	if _, err := os.Stat(filepath.Join(RunDir, "awg20.pid")); err != nil {
		t.Fatal("pidfile missing")
	}
	if _, err := os.Stat(ConfPath("awg20")); err != nil {
		t.Fatal("conf missing")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !spy.has("--config") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !spy.has("--config " + ConfPath("awg20")) {
		t.Fatalf("stderr not forwarded: %v", spy.lines)
	}
	if err := r.Stop("awg20"); err != nil {
		t.Fatal(err)
	}
	if r.Alive("awg20") {
		t.Fatal("alive after stop")
	}
	if _, err := os.Stat(filepath.Join(RunDir, "awg20.pid")); !os.IsNotExist(err) {
		t.Fatal("pidfile not removed")
	}
}

func TestRunner_StartIsIdempotentAndRestartsOnConfChange(t *testing.T) {
	r, _ := newTestRunner(t)
	o := obf()
	if err := r.Start(context.Background(), "awg20", o); err != nil {
		t.Fatal(err)
	}
	pid1 := r.pid("awg20")
	if err := r.Start(context.Background(), "awg20", o); err != nil {
		t.Fatal(err)
	}
	if r.pid("awg20") != pid1 {
		t.Fatal("same conf must not respawn")
	}
	o.Key = "changed"
	if err := r.Start(context.Background(), "awg20", o); err != nil {
		t.Fatal(err)
	}
	if r.pid("awg20") == pid1 {
		t.Fatal("changed conf must respawn")
	}
}

func TestRunner_AdoptAll(t *testing.T) {
	r, _ := newTestRunner(t)
	if err := r.Start(context.Background(), "awg20", obf()); err != nil {
		t.Fatal(err)
	}
	o2 := obf()
	o2.LocalPort = 39051
	if err := r.Start(context.Background(), "awg21", o2); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Stop("awg21") })
	r2 := NewRunner(RunnerDeps{BinaryFor: r.deps.BinaryFor, Log: r.deps.Log})
	r2.matchFn = func(int) bool { return true }
	// Хвост усыновителя гасим сами: иначе горутина переживёт тест.
	t.Cleanup(func() { _ = r2.Stop("awg20") })
	// awg20 — живой туннель, усыновить; awg21 — удалён/выключен, погасить.
	got := r2.AdoptAll(func(id string) bool { return id == "awg20" })
	if len(got) != 1 || got[0] != "awg20" {
		t.Fatalf("adopt = %v", got)
	}
	if !r2.Alive("awg20") || r2.Alive("awg21") {
		t.Fatalf("alive: awg20=%v awg21=%v", r2.Alive("awg20"), r2.Alive("awg21"))
	}
	// Мёртвый pidfile — убирается.
	_ = os.WriteFile(filepath.Join(RunDir, "awg22.pid"), []byte("999999"), 0o644)
	r2.AdoptAll(func(string) bool { return true })
	if _, err := os.Stat(filepath.Join(RunDir, "awg22.pid")); !os.IsNotExist(err) {
		t.Fatal("stale pidfile kept")
	}
}

// Нечитаемый pidfile тоже убирается: иначе AdoptAll спотыкался бы о него на
// каждом старте демона.
func TestRunner_AdoptAllRemovesGarbagePidfile(t *testing.T) {
	r, _ := newTestRunner(t)
	p := filepath.Join(RunDir, "awg23.pid")
	if err := os.MkdirAll(RunDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.AdoptAll(func(string) bool { return true })
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("битый pidfile остался: %v", err)
	}
}

func TestRunner_StartRefusesBusyPort(t *testing.T) {
	r, _ := newTestRunner(t)
	o := obf()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: o.LocalPort})
	if err != nil {
		t.Skip("cannot bind loopback in this sandbox")
	}
	defer c.Close()
	err = r.Start(context.Background(), "awg20", o)
	if err == nil || !strings.Contains(err.Error(), "занят") {
		t.Fatalf("err = %v", err)
	}
	if r.Alive("awg20") {
		t.Fatal("процесс поднят на занятом порту")
	}
}

// Мёртвый pidfile уносит с собой хвост stderr: иначе горутина умершего
// процесса остаётся навсегда на живом раннере.
func TestRunner_AdoptAllDropsTailOfDeadProcess(t *testing.T) {
	r, _ := newTestRunner(t)
	if err := r.Start(context.Background(), "awg20", obf()); err != nil {
		t.Fatal(err)
	}
	pid := r.pid("awg20")
	_ = childproc.KillGroup(pid)
	for deadline := time.Now().Add(3 * time.Second); r.Alive("awg20") && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if r.Alive("awg20") {
		t.Fatal("процесс не умер")
	}
	r.AdoptAll(func(string) bool { return true })
	if _, ok := r.tails["awg20"]; ok {
		t.Fatal("хвост мёртвого процесса не погашен")
	}
}

func TestRunner_AliveRequiresOurBinary(t *testing.T) {
	r, _ := newTestRunner(t)
	if err := r.Start(context.Background(), "awg20", obf()); err != nil {
		t.Fatal(err)
	}
	// Вернуть шов до Cleanup раннера (LIFO): иначе Stop не погасит sh-скрипт.
	t.Cleanup(func() { r.matchFn = func(int) bool { return true } })
	r.matchFn = func(int) bool { return false } // pid переиспользован чужим процессом
	if r.Alive("awg20") {
		t.Fatal("foreign pid must not count as alive")
	}
}
