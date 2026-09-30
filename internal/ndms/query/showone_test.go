package query

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
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
	p, _, _ := q.Interfaces.Lookup(ctx, "Wireguard0")
	f.Remove("Wireguard0")
	var rc rciRCInterface
	if err := q.Interfaces.showRC(ctx, p, "", &rc); !errors.Is(err, ErrGone) {
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

// 404 на поддереве rc (у живого сервера нет секции asc) — ошибка, но не
// «записи нет»: живая запись остаётся в кэше (R12).
func TestShowRC_Suffix404_KeepsRecord(t *testing.T) {
	const asc = "/show/rc/interface/Wireguard0/wireguard/asc"
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, `{"Wireguard0":{"id":"Wireguard0","type":"Wireguard"}}`)
	fg.SetError(asc, &transport.HTTPError{Method: "GET", Path: asc, Status: 404})
	q := NewQueries(Deps{Getter: fg, Logger: NopLogger()})
	ctx := context.Background()
	_, err := q.WGServers.ASC3Fields(ctx, "Wireguard0")
	if err == nil || errors.Is(err, ErrGone) {
		t.Fatalf("want plain error, got %v", err)
	}
	if _, ok, _ := q.Interfaces.Lookup(ctx, "Wireguard0"); !ok {
		t.Fatal("404 on rc subtree must not evict a live record")
	}
}
