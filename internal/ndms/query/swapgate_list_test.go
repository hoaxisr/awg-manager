package query

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/netdev"
)

// D-N1: пока бэкенд подменяет устройство под записью (Hold), наш список ждёт
// и в NDMS не уходит; отпустили — уходит. Гейт — из Deps (один на процесс).
// Мутация: убрать Read из fetchListMap → список уходит до отпускания, красный.
func TestFetchListMap_WaitsForSwap(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "OpkgTun7", Type: "OpkgTun"})
	gate := &netdev.SwapGate{}
	q := NewQueries(Deps{Getter: f, Logger: NopLogger(), SwapGate: gate})
	ctx := context.Background()

	entered, unblock := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- gate.Hold(ctx, func(netdev.Swapper) error {
			close(entered)
			<-unblock
			return nil
		})
	}()
	<-entered

	base := f.ListCalls()
	listed := make(chan error, 1)
	go func() {
		_, err := q.Interfaces.List(ctx)
		listed <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if got := f.ListCalls() - base; got != 0 {
		close(unblock)
		t.Fatalf("список ушёл в NDMS во время подмены: +%d", got)
	}

	close(unblock)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-listed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("список не прошёл после отпускания барьера")
	}
	if got := f.ListCalls() - base; got != 1 {
		t.Fatalf("списков после отпускания: +%d, want +1", got)
	}
}
