package obfuscator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakeProc — /proc/awgm_relay: add/del по порту, list по живым слотам.
type fakeProc struct {
	mu      sync.Mutex
	slots   map[int]string
	adds    int
	delErr  error
	addGate chan struct{} // не nil — add ждёт закрытия
}

func (p *fakeProc) write(path string, b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := string(b)
	var port int
	fmt.Sscanf(strings.TrimPrefix(line, "127.0.0.1:"), "%d", &port)
	switch {
	case strings.HasSuffix(path, "/add"):
		if g := p.addGate; g != nil {
			p.mu.Unlock()
			<-g
			p.mu.Lock()
		}
		if _, ok := p.slots[port]; ok {
			return fmt.Errorf("file exists")
		}
		p.slots[port] = line
		p.adds++
	case strings.HasSuffix(path, "/del"):
		if p.delErr != nil {
			return p.delErr
		}
		delete(p.slots, port)
	}
	return nil
}

func (p *fakeProc) read(string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var b strings.Builder
	for port := range p.slots {
		fmt.Fprintf(&b, "127.0.0.1:%d 1.2.3.4:5 transform=phobos masking=none rx=0\n", port)
	}
	return []byte(b.String()), nil
}

func newKernelFixture(t *testing.T) (*KernelRunner, *fakeProc) {
	setTestDirs(t)
	p := &fakeProc{slots: map[int]string{}}
	k := NewKernelRunner(KernelDeps{
		Ensure: func(context.Context) error { return nil }, ProcWrite: p.write, ProcRead: p.read,
	})
	return k, p
}

var phobosObf = &storage.Obfuscator{Flavor: "phobos", Target: "vpn.example.com:51900", Key: "k", Masking: "NONE", LocalPort: 39001}

func TestKernelRunner_StartAliveStop(t *testing.T) {
	k, p := newKernelFixture(t)
	if err := k.Start(context.Background(), "awg30", phobosObf, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	if !k.Alive("awg30") || k.Backend("awg30") != BackendKernel || len(p.slots) != 1 {
		t.Fatalf("alive=%v slots=%v", k.Alive("awg30"), p.slots)
	}
	if err := k.Stop("awg30"); err != nil || k.Alive("awg30") || len(p.slots) != 0 {
		t.Fatalf("stop: %v %v", err, p.slots)
	}
}

// Review Focus 1: рестарт демона — новый KernelRunner, слот жив, строка та же.
func TestKernelRunner_StartIdempotentAfterRestart(t *testing.T) {
	k, p := newKernelFixture(t)
	if err := k.Start(context.Background(), "awg30", phobosObf, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	k2 := NewKernelRunner(KernelDeps{Ensure: func(context.Context) error { return nil }, ProcWrite: p.write, ProcRead: p.read})
	if !k2.Alive("awg30") {
		t.Fatal("после рестарта слот не усыновлён")
	}
	if err := k2.Start(context.Background(), "awg30", phobosObf, "198.51.100.1"); err != nil || p.adds != 1 {
		t.Fatalf("повторный add живого слота: err=%v adds=%d", err, p.adds)
	}
}

func TestKernelRunner_NewIPRecreatesSlot(t *testing.T) {
	k, p := newKernelFixture(t)
	_ = k.Start(context.Background(), "awg30", phobosObf, "198.51.100.1")
	if err := k.Start(context.Background(), "awg30", phobosObf, "198.51.100.2"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.slots[39001], "198.51.100.2:51900") || p.adds != 2 {
		t.Fatalf("слот не пересоздан: %v adds=%d", p.slots, p.adds)
	}
}

// Review-находка: отказ подготовки RunDir не должен создавать слот в ядре.
func TestKernelRunner_Start_MkdirAllFailsNoAdd(t *testing.T) {
	k, p := newKernelFixture(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	RunDir = filepath.Join(blocker, "sub") // MkdirAll под обычным файлом — ENOTDIR
	if err := k.Start(context.Background(), "awg30", phobosObf, "198.51.100.1"); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if p.adds != 0 {
		t.Fatalf("add вызван при отказе подготовки RunDir: adds=%d slots=%v", p.adds, p.slots)
	}
}

func TestKernelRunner_Sweep(t *testing.T) {
	k, p := newKernelFixture(t)
	p.slots[39050] = "orphan"
	_ = k.Start(context.Background(), "awg30", phobosObf, "198.51.100.1")
	removed := k.Sweep(func(port int) bool { return port == 39001 })
	if len(removed) != 1 || removed[0] != 39050 || len(p.slots) != 1 {
		t.Fatalf("sweep: removed=%v slots=%v", removed, p.slots)
	}
}

// F477: отказ del не теряется — Stop возвращает ошибку, а запись слота
// остаётся (слот в ядре жив, забыть о нём = сирота до Sweep).
func TestKernelRunner_StopDelFailureKeepsRecord(t *testing.T) {
	k, p := newKernelFixture(t)
	if err := k.Start(context.Background(), "a", phobosObf, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	p.delErr = fmt.Errorf("busy")
	if err := k.Stop("a"); err == nil {
		t.Fatal("отказ del проглочен")
	}
	if !k.Alive("a") {
		t.Fatal("запись живого слота удалена")
	}
}

// F477: Alive посреди Start (старый слот снят, новый ещё не записан) не
// должен видеть «релей не запущен» — ждёт завершения Start.
func TestKernelRunner_AliveWaitsForStart(t *testing.T) {
	k, p := newKernelFixture(t)
	if err := k.Start(context.Background(), "a", phobosObf, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.addGate = make(chan struct{})
	p.mu.Unlock()
	done := make(chan error)
	go func() { done <- k.Start(context.Background(), "a", phobosObf, "198.51.100.2") }()
	time.Sleep(50 * time.Millisecond) // Start дошёл до add
	alive := make(chan bool, 1)
	go func() { alive <- k.Alive("a") }()
	select {
	case v := <-alive:
		t.Fatalf("Alive ответил посреди Start: %v", v)
	case <-time.After(100 * time.Millisecond):
	}
	close(p.addGate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !<-alive {
		t.Fatal("после Start релей не жив")
	}
}
