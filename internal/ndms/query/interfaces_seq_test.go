package query

import (
	"context"
	"testing"
	"time"
)

// blockingGetter держит запрос списка, пока тест не откроет gate: так хук приходит, когда запрос уже ушёл, а ответ
// ещё не применён. gate == nil — ничего не блокируется (бутстрап).
type blockingGetter struct {
	Getter
	gate    chan struct{}
	entered chan struct{}
}

func (b *blockingGetter) Get(ctx context.Context, path string, dst any) error {
	if path == ifaceListPath && b.gate != nil {
		b.entered <- struct{}{}
		<-b.gate
	}
	return b.Getter.Get(ctx, path, dst)
}

func (b *blockingGetter) waitBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(time.Second):
		t.Fatal("list request did not reach the getter")
	}
}

// startListInFlight бутстрапит стор и запускает InvalidateAll, который
// останавливается на запросе списка. Возвращает стор, геттер и канал
// завершения InvalidateAll.
func startListInFlight(t *testing.T) (*InterfaceStore, *blockingGetter, chan struct{}) {
	t.Helper()
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList) // Wireguard0, Bridge0
	bg := &blockingGetter{Getter: fg, entered: make(chan struct{}, 1)}
	s := NewInterfaceStore(bg, NopLogger())
	if _, err := s.Get(context.Background(), "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	bg.gate = make(chan struct{})
	done := make(chan struct{})
	go func() { s.InvalidateAll(); close(done) }() // список ушёл ДО хука
	bg.waitBlocked(t)
	return s, bg, done
}

func TestInterfaceStore_ListRace_DoesNotResurrectDestroyed(t *testing.T) {
	s, bg, done := startListInFlight(t)
	s.OnDestroyed("Wireguard0") // хук пришёл, пока список в полёте
	close(bg.gate)
	<-done
	ctx := context.Background()
	if got, _ := s.Get(ctx, "Wireguard0"); got != nil {
		t.Fatalf("destroyed Wireguard0 resurrected by a stale list: %#v", got)
	}
	if got, _ := s.Get(ctx, "Bridge0"); got == nil {
		t.Fatal("untouched Bridge0 must be applied from the list")
	}
}

func TestInterfaceStore_ListRace_LayerHookWins(t *testing.T) {
	s, bg, done := startListInFlight(t)
	s.OnLayerChanged("Wireguard0", "conf", "disabled") // хук новее списка
	close(bg.gate)
	<-done
	got, err := s.Get(context.Background(), "Wireguard0")
	if err != nil || got == nil {
		t.Fatalf("Wireguard0 must stay in cache: %#v, %v", got, err)
	}
	if got.ConfLayer != "disabled" {
		t.Fatalf("ConfLayer = %q, want %q: stale list overwrote a newer hook", got.ConfLayer, "disabled")
	}
}

// Snapshot(Live): хук слоя, пришедший, пока список в полёте, новее ответа —
// ответ его не затирает.
func TestInterfaceStore_SnapshotLive_LayerHookWins(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList) // Wireguard0 conf=running
	bg := &blockingGetter{Getter: fg, entered: make(chan struct{}, 1)}
	s := NewInterfaceStore(bg, NopLogger())
	ctx := context.Background()
	if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	bg.gate = make(chan struct{})
	done := make(chan struct{})
	go func() { _, _ = s.Snapshot(ctx, SnapshotLive); close(done) }()
	bg.waitBlocked(t)
	s.OnLayerChanged("Wireguard0", "conf", "disabled")
	close(bg.gate)
	<-done
	got, err := s.Get(ctx, "Wireguard0")
	if err != nil || got == nil || got.ConfLayer != "disabled" {
		t.Fatalf("stale list overwrote a newer hook: %#v, %v", got, err)
	}
}

// Snapshot(Live): ifdestroyed, пришедший, пока список в полёте, — записи нет
// ни в карте, ни в снимке; прочитанная до сноса копия не выдаётся за «есть».
func TestInterfaceStore_SnapshotLive_DestroyedMidListIsAbsent(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	bg := &blockingGetter{Getter: fg, entered: make(chan struct{}, 1)}
	s := NewInterfaceStore(bg, NopLogger())
	ctx := context.Background()
	if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	bg.gate = make(chan struct{})
	type res struct {
		snap *Snapshot
		err  error
	}
	done := make(chan res, 1)
	go func() { r, err := s.Snapshot(ctx, SnapshotLive); done <- res{r, err} }()
	bg.waitBlocked(t)
	s.OnDestroyed("Wireguard0") // ответ NDMS уже ушёл с записью, хук — после
	close(bg.gate)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if _, ok := r.snap.Record("Wireguard0"); ok {
		t.Fatal("snapshot has Wireguard0 destroyed mid-list")
	}
	if _, ok := r.snap.Raw("Wireguard0"); ok {
		t.Fatal("snapshot has raw of Wireguard0 destroyed mid-list")
	}
	if got, _ := s.Get(ctx, "Wireguard0"); got != nil {
		t.Fatalf("destroyed Wireguard0 resurrected by the list: %#v", got)
	}
}
