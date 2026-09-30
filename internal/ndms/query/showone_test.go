package query

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func TestShowOne_UnableToFind_EvictsAndStopsReading(t *testing.T) {
	// Потерян ifdestroyed: кэш ещё знает Wireguard0, NDMS — уже нет.
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
	s := NewInterfaceStore(f, NopLogger())
	ctx := context.Background()
	p, ok, err := s.Lookup(ctx, "Wireguard0")
	if err != nil || !ok {
		t.Fatal("bootstrap must see Wireguard0")
	}
	f.Remove("Wireguard0") // хук теряем
	if _, err := s.ShowRaw(ctx, p); !errors.Is(err, ErrGone) || f.E != 1 {
		t.Fatalf("first read after loss: err=%v E=%d (one E is the price of the lost hook)", err, f.E)
	}
	if _, ok, _ := s.Lookup(ctx, "Wireguard0"); ok {
		t.Fatal("record must be evicted after unable-to-find")
	}
	if d, err := s.FetchSummary(ctx, "Wireguard0"); d != nil || err != nil || f.E != 1 {
		t.Fatalf("second read must not go to NDMS: d=%v err=%v E=%d", d, err, f.E)
	}
}

func TestShowRC_404_Evicts(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	q := NewQueries(Deps{Getter: f, Logger: NopLogger()})
	ctx := context.Background()
	_, _ = q.Interfaces.List(ctx)
	f.Remove("Wireguard0")
	if _, err := q.WGServers.PeersRCFresh(ctx, "Wireguard0"); !errors.Is(err, ErrGone) {
		t.Fatalf("want ErrGone, got %v", err)
	}
	if _, ok, _ := q.Interfaces.Lookup(ctx, "Wireguard0"); ok {
		t.Fatal("404 on rc must evict")
	}
}

func TestLookup_BootstrapError_NoProbe(t *testing.T) {
	fg := newFakeGetter()
	fg.SetError(ifaceListPath, errors.New("rci down"))
	s := NewInterfaceStore(fg, NopLogger())
	if _, _, err := s.Lookup(context.Background(), "Wireguard0"); err == nil {
		t.Fatal("bootstrap error must surface, not be swallowed into 'may exist'")
	}
	if fg.PostInterfaceCalls("Wireguard0") != 0 {
		t.Fatal("no point read on bootstrap failure")
	}
}
