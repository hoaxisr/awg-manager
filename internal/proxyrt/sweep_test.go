package proxyrt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeScanner struct {
	out []OwnedResource
	err error
}

func (f fakeScanner) Scan(context.Context, []string) ([]OwnedResource, error) {
	return f.out, f.err
}

type fakeRemover struct {
	mu      sync.Mutex
	removed []string
	delay   time.Duration
	err     error
}

func (f *fakeRemover) Remove(_ context.Context, r OwnedResource) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.removed = append(f.removed, r.Name)
	f.mu.Unlock()
	return f.err
}

func TestSweepRemovesOnlyUndeclared(t *testing.T) {
	sc := fakeScanner{out: []OwnedResource{
		{Label: "AWGM WDTT client", Name: "OpkgTun18"},
		{Label: "AWGM WDTT client", Name: "OpkgTun19"},
		{Label: "AWGM WDTT", Name: "OpkgTun20"},
	}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}),
		[]string{"AWGM WDTT client", "AWGM WDTT"})

	removed, err := sw.Sweep(context.Background(), map[string]bool{"OpkgTun18": true, "OpkgTun20": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "OpkgTun19" {
		t.Fatalf("удалено %v, ожидали только OpkgTun19", removed)
	}
}

func TestSweepNeverRemovesDeclared(t *testing.T) {
	// Объявленный ресурс не удаляется никогда: ни по «процесс не бежит», ни по
	// таймеру. Выключенный инстанс продолжает объявлять свои ресурсы.
	sc := fakeScanner{out: []OwnedResource{{Label: "AWGM WDTT client", Name: "OpkgTun18"}}}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}), []string{"AWGM WDTT client"})

	removed, err := sw.Sweep(context.Background(), map[string]bool{"OpkgTun18": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("удалено %v, ожидали пусто", removed)
	}
}

func TestSweepFailedScanRemovesNothing(t *testing.T) {
	// «Не знаем» не равно «наш и лишний». Скан упал — не сносим ничего.
	sc := fakeScanner{err: errors.New("rci недоступен")}
	rm := &fakeRemover{}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}), []string{"AWGM WDTT client"})

	removed, err := sw.Sweep(context.Background(), map[string]bool{})
	if err == nil {
		t.Fatal("ожидали ошибку скана")
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if len(removed) != 0 || len(rm.removed) != 0 {
		t.Fatalf("при упавшем скане удалять нельзя, удалено %v", rm.removed)
	}
}

func TestSweepReportsRemoveError(t *testing.T) {
	sc := fakeScanner{out: []OwnedResource{{Label: "AWGM WDTT client", Name: "OpkgTun19"}}}
	rm := &fakeRemover{err: errors.New("rci отказал")}
	sw := NewSweeper(sc, rm, NewAllocator(IndexRange{Min: 17, Max: 49}), []string{"AWGM WDTT client"})

	removed, err := sw.Sweep(context.Background(), map[string]bool{})
	if err == nil {
		t.Fatal("ошибка сноса обязана доехать наружу, а не проглотиться")
	}
	if len(removed) != 0 {
		t.Fatalf("неудавшийся снос не должен попадать в список удалённых: %v", removed)
	}
}

func TestSweepDoesNotHoldAllocatorLockDuringRemoval(t *testing.T) {
	// Снос — это RCI-вызовы на секунды. Держать на них лок аллокатора значит
	// остановить выделение номеров всем инстансам.
	alloc := NewAllocator(IndexRange{Min: 17, Max: 49})
	sc := fakeScanner{out: []OwnedResource{{Label: "L", Name: "OpkgTun19"}}}
	rm := &fakeRemover{delay: 150 * time.Millisecond}
	sw := NewSweeper(sc, rm, alloc, []string{"L"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		sw.Sweep(context.Background(), map[string]bool{})
	}()

	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	if _, err := alloc.AllocIndex(0, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited > 100*time.Millisecond {
		t.Fatalf("выделение номера ждало лок %v — уборщик держит его на время сносов", waited)
	}
	<-done
}
