package query

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// F546: читатели состояния берут снимок полного списка вместо чтения по имени.

func snapshotFake() *FakeNDMS {
	f := NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0", ConfLayer: "running", State: "up", Link: "up"},
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1", ConfLayer: "running", State: "up", Link: "up"},
	)
	f.SetDetail("Wireguard0", json.RawMessage(`{"wireguard":{"peer":[{"rxbytes":1}]}}`))
	f.SetDetail("Wireguard1", json.RawMessage(`{"wireguard":{"peer":[{"rxbytes":1}]}}`))
	return f
}

func TestSnapshot_RecentServesMemory(t *testing.T) {
	ctx := context.Background()
	f := snapshotFake()
	s := NewInterfaceStore(f, NopLogger())
	for range 2 {
		snap, err := s.Snapshot(ctx, SnapshotRecent)
		if err != nil {
			t.Fatal(err)
		}
		raw, ok := snap.Raw("Wireguard0")
		if !ok || !strings.Contains(string(raw), `"rxbytes":1`) {
			t.Fatalf("Raw(Wireguard0) = %s, %v", raw, ok)
		}
	}
	if f.ListCalls() != 1 {
		t.Fatalf("ListCalls = %d, want 1 (bootstrap)", f.ListCalls())
	}
}

func TestSnapshot_LiveAlwaysLists(t *testing.T) {
	ctx := context.Background()
	f := snapshotFake()
	s := NewInterfaceStore(f, NopLogger())
	for range 2 {
		if _, err := s.Snapshot(ctx, SnapshotLive); err != nil {
			t.Fatal(err)
		}
	}
	if f.ListCalls() != 3 {
		t.Fatalf("ListCalls = %d, want 3 (bootstrap + 2)", f.ListCalls())
	}
}

func TestSnapshot_DirtyAfterInvalidateLists(t *testing.T) {
	ctx := context.Background()
	f := snapshotFake()
	s := NewInterfaceStore(f, NopLogger())
	if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil {
		t.Fatal(err)
	}
	s.Invalidate("Wireguard0")
	if f.ListCalls() != 1 || len(f.Posts) != 0 {
		t.Fatalf("Invalidate читает: ListCalls=%d Posts=%v", f.ListCalls(), f.Posts)
	}
	if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil {
		t.Fatal(err)
	}
	if f.ListCalls() != 2 {
		t.Fatalf("после метки ListCalls = %d, want 2", f.ListCalls())
	}
	if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil {
		t.Fatal(err)
	}
	if f.ListCalls() != 2 {
		t.Fatalf("метка не снята: ListCalls = %d, want 2", f.ListCalls())
	}
}

func TestSnapshot_ConcurrentCallersOneList(t *testing.T) {
	ctx := context.Background()
	f := snapshotFake()
	bg := &blockingGetter{Getter: f, entered: make(chan struct{}, 1)}
	s := NewInterfaceStore(bg, NopLogger())
	if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil { // bootstrap
		t.Fatal(err)
	}
	s.listedAt = time.Time{}
	bg.gate = make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil {
				errs <- err
			}
		}()
	}
	bg.waitBlocked(t)
	close(bg.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if f.ListCalls() != 2 {
		t.Fatalf("ListCalls = %d, want 2 (bootstrap + один на всех)", f.ListCalls())
	}
}

// joinLogger сигналит о присоединении к полёту (Debugf в Snapshot).
type joinLogger struct{ joined chan struct{} }

func (joinLogger) Warnf(string, ...any) {}
func (l joinLogger) Debugf(format string, _ ...any) {
	if strings.Contains(format, "joined list in flight") {
		l.joined <- struct{}{}
	}
}

func TestSnapshot_JoinOnlyFlightsAfterDirty(t *testing.T) {
	ctx := context.Background()
	f := snapshotFake()
	bg := &blockingGetter{Getter: f, entered: make(chan struct{}, 1)}
	lg := joinLogger{joined: make(chan struct{}, 4)}
	s := NewInterfaceStore(bg, lg)
	if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil { // bootstrap
		t.Fatal(err)
	}
	s.listedAt = time.Time{}
	bg.gate = make(chan struct{})

	var wg sync.WaitGroup
	run := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil {
				t.Error(err)
			}
		}()
	}
	run() // A: свой список, заблокирован
	bg.waitBlocked(t)
	run() // B: до метки — присоединяется к A
	select {
	case <-lg.joined:
	case <-time.After(time.Second):
		t.Fatal("B не присоединился к списку в полёте")
	}
	s.Invalidate("Wireguard0")
	run() // C: после метки — A начат до неё, C читает свой список
	bg.waitBlocked(t)
	close(bg.gate)
	wg.Wait()
	if f.ListCalls() != 3 {
		t.Fatalf("ListCalls = %d, want 3 (bootstrap + A + C)", f.ListCalls())
	}
	select {
	case <-lg.joined:
		t.Fatal("C присоединился к списку, начатому до метки")
	default:
	}
}

func TestSnapshot_RawFollowsTouchedRule(t *testing.T) {
	ctx := context.Background()
	f := snapshotFake()
	s := NewInterfaceStore(f, NopLogger())
	if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil { // bootstrap
		t.Fatal(err)
	}
	f.SetDetail("Wireguard0", json.RawMessage(`{"wireguard":{"peer":[{"rxbytes":2}]}}`))
	f.SetDetail("Wireguard1", json.RawMessage(`{"wireguard":{"peer":[{"rxbytes":2}]}}`))
	f.InList(func() { s.OnLayerChanged("Wireguard0", "conf", "disabled") })
	snap, err := s.Snapshot(ctx, SnapshotLive)
	if err != nil {
		t.Fatal(err)
	}
	if rec, ok := snap.Record("Wireguard0"); !ok || rec.ConfLayer != "disabled" {
		t.Fatalf("Record(Wireguard0) = %+v, %v: хук новее списка", rec, ok)
	}
	if raw, _ := snap.Raw("Wireguard0"); !strings.Contains(string(raw), `"rxbytes":1`) {
		t.Fatalf("Raw(Wireguard0) затёрт ответом, начатым до хука: %s", raw)
	}
	if raw, _ := snap.Raw("Wireguard1"); !strings.Contains(string(raw), `"rxbytes":2`) {
		t.Fatalf("Raw(Wireguard1) не обновлён: %s", raw)
	}
}

func TestSnapshot_ForgetDropsRaw(t *testing.T) {
	ctx := context.Background()
	s := NewInterfaceStore(snapshotFake(), NopLogger())
	if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil {
		t.Fatal(err)
	}
	s.Forget("Wireguard0")
	snap, err := s.Snapshot(ctx, SnapshotRecent)
	if err != nil {
		t.Fatal(err)
	}
	if raw, ok := snap.Raw("Wireguard0"); ok {
		t.Fatalf("Raw после Forget: %s", raw)
	}
	if _, ok := snap.Record("Wireguard0"); ok {
		t.Fatal("Record после Forget")
	}
}

func TestDetailsRecent_AbsentNoRCI(t *testing.T) {
	f := snapshotFake()
	s := NewInterfaceStore(f, NopLogger())
	d, err := s.DetailsRecent(context.Background(), "OpkgTun12")
	if err != nil || d != nil {
		t.Fatalf("want (nil, nil), got (%#v, %v)", d, err)
	}
	if f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("E=%d Posts=%v, want 0/none", f.E, f.Posts)
	}
}

func TestDetails_FieldsMatchGetDetails(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", ConfLayer: "running",
		State: "up", Link: "up", Connected: "yes", Uptime: 120})
	s := NewInterfaceStore(f, NopLogger())
	snap, err := s.Snapshot(ctx, SnapshotRecent)
	if err != nil {
		t.Fatal(err)
	}
	got := snap.Details("Wireguard0")
	want, err := s.GetDetails(ctx, "Wireguard0")
	if err != nil || got == nil || want == nil {
		t.Fatalf("got=%#v want=%#v err=%v", got, want, err)
	}
	if got.Uptime < 120 || want.Uptime-got.Uptime > 1 {
		t.Fatalf("Uptime: снимок %d, GetDetails %d", got.Uptime, want.Uptime)
	}
	got.Uptime = want.Uptime
	if *got != *want {
		t.Fatalf("Details = %#v, GetDetails = %#v", *got, *want)
	}
}
