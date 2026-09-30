package query

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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

// ctxGetter держит первый запрос списка до gate и отвечает ошибкой ctx, если
// ctx вызывающего к тому моменту отменён.
type ctxGetter struct {
	Getter
	gate    chan struct{}
	entered chan struct{}
}

func (g *ctxGetter) Get(ctx context.Context, path string, dst any) error {
	if path == ifaceListPath && g.gate != nil {
		select {
		case g.entered <- struct{}{}:
			<-g.gate
		default:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return g.Getter.Get(ctx, path, dst)
}

// Ведущий — HTTP-запрос панели, вкладку закрыли: его ctx отменён. Живой
// присоединившийся не наследует «context canceled» — читает свой список.
func TestSnapshot_JoinerSurvivesLeaderCancel(t *testing.T) {
	f := snapshotFake()
	g := &ctxGetter{Getter: f, entered: make(chan struct{})}
	lg := joinLogger{joined: make(chan struct{}, 4)}
	s := NewInterfaceStore(g, lg)
	if _, err := s.Snapshot(context.Background(), SnapshotRecent); err != nil { // bootstrap
		t.Fatal(err)
	}
	s.listedAt = time.Time{}
	g.gate = make(chan struct{})

	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() { _, err := s.Snapshot(leaderCtx, SnapshotRecent); leaderErr <- err }()
	<-g.entered
	joinerErr := make(chan error, 1)
	go func() { _, err := s.Snapshot(context.Background(), SnapshotRecent); joinerErr <- err }()
	select {
	case <-lg.joined:
	case <-time.After(time.Second):
		t.Fatal("не присоединился")
	}
	cancel()
	close(g.gate)
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("ведущий: %v, want context.Canceled", err)
	}
	if err := <-joinerErr; err != nil {
		t.Fatalf("присоединившийся унаследовал отмену ведущего: %v", err)
	}
}

// Отмена ведущего при нескольких присоединившихся: они не читают по списку
// каждый — один начинает новый полёт, остальные присоединяются к нему (или
// берут уже свежий снимок). 5 присоединившихся → 1 новый список.
func TestSnapshot_JoinersAfterLeaderCancel_OneList(t *testing.T) {
	f := snapshotFake()
	g := &ctxGetter{Getter: f, entered: make(chan struct{})}
	lg := joinLogger{joined: make(chan struct{}, 64)}
	s := NewInterfaceStore(g, lg)
	if _, err := s.Snapshot(context.Background(), SnapshotRecent); err != nil { // bootstrap
		t.Fatal(err)
	}
	s.listedAt = time.Time{}
	g.gate = make(chan struct{})

	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() { _, err := s.Snapshot(leaderCtx, SnapshotRecent); leaderErr <- err }()
	<-g.entered
	const joiners = 5
	errs := make(chan error, joiners)
	for i := 0; i < joiners; i++ {
		go func() { _, err := s.Snapshot(context.Background(), SnapshotRecent); errs <- err }()
	}
	for i := 0; i < joiners; i++ {
		select {
		case <-lg.joined:
		case <-time.After(time.Second):
			t.Fatalf("присоединились %d из %d", i, joiners)
		}
	}
	before := f.ListCalls()
	cancel()
	close(g.gate)
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("ведущий: %v, want context.Canceled", err)
	}
	for i := 0; i < joiners; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("присоединившийся: %v", err)
		}
	}
	if n := f.ListCalls() - before; n != 1 {
		t.Fatalf("новых списков %d, want 1", n)
	}
}

// Метка «грязно» (наша запись) — чтение памяти берёт свежий список: поля,
// которых хуки не несут, после нашей команды не старые.
func TestList_DirtyReadsFreshFields(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun", Description: "old", Mask: "255.255.255.255", MTU: 1420})
	s := NewInterfaceStore(f, NopLogger())
	if _, err := s.List(ctx); err != nil {
		t.Fatal(err)
	}
	f.Add(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun", Description: "new", Mask: "255.255.255.0", MTU: 1380}) // наша правка, хука нет
	s.Invalidate("OpkgTun10")
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v %v", list, err)
	}
	if got := list[0]; got.Description != "new" || got.Mask != "255.255.255.0" || got.MTU != 1380 {
		t.Fatalf("после метки List старый: %+v", got)
	}
	if got, _ := s.Get(ctx, "OpkgTun10"); got == nil || got.Description != "new" {
		t.Fatalf("Get: %+v", got)
	}
}

// Ответы списков пришли не по порядку: начатый раньше уже применённого не
// затирает более свежие данные.
func TestRefreshList_OlderResponseNotApplied(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun", Description: "old"})
	s := NewInterfaceStore(f, NopLogger())
	if _, err := s.List(ctx); err != nil {
		t.Fatal(err)
	}
	gate, entered := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	f.InList(func() {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-gate
		}
	})
	done := make(chan struct{})
	go func() { s.InvalidateAll(); close(done) }() // A: ответ «old» задержан
	<-entered
	f.Add(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun", Description: "new"})
	s.InvalidateAll() // B: начат позже, применён первым
	close(gate)
	<-done
	if got, _ := s.Get(ctx, "OpkgTun10"); got == nil || got.Description != "new" {
		t.Fatalf("старый ответ затёр свежий: %+v", got)
	}
}

// R36: зрителей нет — два фоновых поллера (шаг 60 с, сдвиг 30 с) принимают
// снимок до SnapshotBackground и за 3 минуты делят ≤ 4 списка (без допуска —
// по списку на тик, 7). Со зрителем (без допуска в ctx) — как прежде.
// Время виртуальное: возраст памяти задаётся сдвигом listedAt.
func TestSnapshot_BackgroundPollersShareList(t *testing.T) {
	for _, tc := range []struct {
		name   string
		viewer bool
		max    int
		min    int
	}{{"без зрителя", false, 4, 1}, {"со зрителем", true, 7, 7}} {
		t.Run(tc.name, func(t *testing.T) {
			f := snapshotFake()
			s := NewInterfaceStore(f, NopLogger())
			if _, err := s.Snapshot(context.Background(), SnapshotRecent); err != nil {
				t.Fatal(err)
			}
			before := f.ListCalls()
			lastList := -1000 * time.Second // виртуальное время последнего списка
			for now := time.Duration(0); now <= 180*time.Second; now += 30 * time.Second {
				s.mu.Lock()
				s.listedAt = time.Now().Add(-(now - lastList))
				s.mu.Unlock()
				ctx := context.Background()
				if !tc.viewer {
					ctx = WithSnapshotBackground(ctx)
				}
				n := f.ListCalls()
				if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil {
					t.Fatal(err)
				}
				if f.ListCalls() > n {
					lastList = now
				}
			}
			got := f.ListCalls() - before
			if got > tc.max || got < tc.min {
				t.Fatalf("списков %d, want %d..%d", got, tc.min, tc.max)
			}
		})
	}
}

// Метка «грязно» (наша запись) сильнее фонового допуска: свежий список.
func TestSnapshot_BackgroundDirtyReadsList(t *testing.T) {
	f := snapshotFake()
	s := NewInterfaceStore(f, NopLogger())
	ctx := WithSnapshotBackground(context.Background())
	if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil {
		t.Fatal(err)
	}
	before := f.ListCalls()
	if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil || f.ListCalls() != before {
		t.Fatalf("свежая память: списков +%d, %v", f.ListCalls()-before, err)
	}
	s.Invalidate("Wireguard0")
	if _, err := s.Snapshot(ctx, SnapshotRecent); err != nil || f.ListCalls() != before+1 {
		t.Fatalf("после метки: списков +%d, %v; want +1", f.ListCalls()-before, err)
	}
	// SnapshotLive допуском не расширяется.
	if _, err := s.Snapshot(ctx, SnapshotLive); err != nil || f.ListCalls() != before+2 {
		t.Fatalf("Live: списков +%d, want +2", f.ListCalls()-before)
	}
}
