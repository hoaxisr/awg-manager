package query

import (
	"context"
	"testing"
	"time"
)

// blockingGetter держит запрос списка, пока тест не откроет gate: так хук
// приходит, когда список уже ушёл, а ответ ещё не применён. gate == nil —
// список не блокируется (бутстрап).
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
