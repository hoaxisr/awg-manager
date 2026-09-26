package obfuscator

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeRelay struct {
	name     string
	alive    map[string]bool
	startErr error
	stops    int
}

func newFake(name string) *fakeRelay { return &fakeRelay{name: name, alive: map[string]bool{}} }
func (f *fakeRelay) Start(_ context.Context, id string, _ *storage.Obfuscator, _ string) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.alive[id] = true
	return nil
}
func (f *fakeRelay) Stop(id string) error { delete(f.alive, id); f.stops++; return nil }
func (f *fakeRelay) Alive(id string) bool { return f.alive[id] }
func (f *fakeRelay) Backend(id string) string {
	if f.alive[id] {
		return f.name
	}
	return ""
}

func TestDispatcher_PicksKernelForPhobos(t *testing.T) {
	p, k := newFake(BackendProcess), newFake(BackendKernel)
	d := NewDispatcher(p, k, func(*storage.Obfuscator, string) bool { return true }, nil)
	if err := d.Start(context.Background(), "a", phobosObf, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	if !k.alive["a"] || p.alive["a"] || d.Backend("a") != BackendKernel {
		t.Fatalf("kernel=%v process=%v", k.alive, p.alive)
	}
}

func TestDispatcher_FallbackToProcess(t *testing.T) {
	p, k := newFake(BackendProcess), newFake(BackendKernel)
	k.startErr = errors.New("insmod failed")
	d := NewDispatcher(p, k, func(*storage.Obfuscator, string) bool { return true }, nil)
	if err := d.Start(context.Background(), "a", phobosObf, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	if !p.alive["a"] || d.Backend("a") != BackendProcess {
		t.Fatal("откат на процесс не случился")
	}
}

// Review Focus 2: переключение бэкенда гасит другой — двух релеев на порту нет.
func TestDispatcher_SwitchStopsOtherBackend(t *testing.T) {
	p, k := newFake(BackendProcess), newFake(BackendKernel)
	kernel := true
	d := NewDispatcher(p, k, func(*storage.Obfuscator, string) bool { return kernel }, nil)
	_ = d.Start(context.Background(), "a", phobosObf, "198.51.100.1")
	kernel = false
	if err := d.Start(context.Background(), "a", phobosObf, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	if k.alive["a"] || !p.alive["a"] {
		t.Fatalf("после переключения: kernel=%v process=%v", k.alive, p.alive)
	}
}

// Без выбора (рестарт демона, cleanup): Alive по обоим, Stop гасит оба.
func TestDispatcher_NoChoiceUsesBoth(t *testing.T) {
	p, k := newFake(BackendProcess), newFake(BackendKernel)
	k.alive["a"] = true
	d := NewDispatcher(p, k, func(*storage.Obfuscator, string) bool { return false }, nil)
	if !d.Alive("a") || d.Backend("a") != BackendKernel {
		t.Fatal("живой kernel-слот не виден без выбора")
	}
	_ = d.Stop("a")
	if k.alive["a"] || k.stops != 1 || p.stops != 1 {
		t.Fatalf("Stop без выбора: kernel.stops=%d process.stops=%d", k.stops, p.stops)
	}
}
