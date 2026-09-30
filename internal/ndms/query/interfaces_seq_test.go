package query

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// blockingGetter держит запрос списка и точечное чтение `show interface`,
// пока тест не откроет gate: так хук приходит, когда запрос уже ушёл, а ответ
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

func (b *blockingGetter) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if b.gate != nil && extractShowInterfaceName(payload) != "" {
		b.entered <- struct{}{}
		<-b.gate
	}
	return b.Getter.Post(ctx, payload)
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

// Refresh известной записи: хук слоя, пришедший, пока точечное чтение в
// полёте, новее ответа — ответ его не затирает.
func TestInterfaceStore_RefreshKnown_LayerHookWins(t *testing.T) {
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
	go func() { _, _ = s.Refresh(ctx, "Wireguard0"); close(done) }()
	bg.waitBlocked(t)
	s.OnLayerChanged("Wireguard0", "conf", "disabled")
	close(bg.gate)
	<-done
	got, err := s.Get(ctx, "Wireguard0")
	if err != nil || got == nil || got.ConfLayer != "disabled" {
		t.Fatalf("stale point read overwrote a newer hook: %#v, %v", got, err)
	}
}

// Refresh известной записи: ifdestroyed, пришедший, пока чтение в полёте, —
// записи нет; прочитанная до сноса копия не выдаётся за «есть» (снос прокси
// и шлюз владения OpkgTun послали бы команды по снятому имени).
func TestInterfaceStore_RefreshKnown_DestroyedMidReadIsAbsent(t *testing.T) {
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
		rec *ndms.Interface
		err error
	}
	done := make(chan res, 1)
	go func() { r, err := s.Refresh(ctx, "Wireguard0"); done <- res{r, err} }()
	bg.waitBlocked(t)
	s.OnDestroyed("Wireguard0") // ответ NDMS уже ушёл с записью, хук — после
	close(bg.gate)
	r := <-done
	if r.err != nil || r.rec != nil {
		t.Fatalf("want (nil, nil) after destroy mid-read, got (%#v, %v)", r.rec, r.err)
	}
	if got, _ := s.Get(ctx, "Wireguard0"); got != nil {
		t.Fatalf("destroyed Wireguard0 resurrected by the point read: %#v", got)
	}
}
