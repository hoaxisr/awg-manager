package query

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// F546: одиночный `show interface <name>` по отсутствующему имени NDMS
// пишет E «unable to find» в своём журнале. Читатели по имени обязаны
// отсекать имя, которого нет в кэше интерфейсов, не спрашивая NDMS. Оракул —
// FakeNDMS: E считает любое точечное чтение отсутствующего, какой бы формой
// оно ни ушло.

func absentIfaceQueries(t *testing.T) (*Queries, *FakeNDMS) {
	t.Helper()
	f := NewFakeNDMS( // Wireguard7 в NDMS нет
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"},
		ndms.Interface{ID: "Bridge0", Type: "Bridge", SystemName: "br0"},
	)
	return NewQueries(Deps{Getter: f, Logger: NopLogger()}), f
}

func TestPeers_AbsentInterface_NoQuery(t *testing.T) {
	q, f := absentIfaceQueries(t)
	peers, err := q.Peers.GetPeers(context.Background(), "Wireguard7")
	if err != nil || len(peers) != 0 {
		t.Fatalf("want (0 peers, nil), got (%v, %v)", peers, err)
	}
	if f.E != 0 || len(f.Posts) != 0 || f.ListCalls() != 1 {
		t.Fatalf("E=%d Posts=%v ListCalls=%d, want 0/none/1 (снимок)", f.E, f.Posts, f.ListCalls())
	}
}

func TestWGServers_AbsentInterface_NoQuery(t *testing.T) {
	q, f := absentIfaceQueries(t)
	ctx := context.Background()
	_, _ = q.WGServers.GetSystemTunnel(ctx, "Wireguard7")
	if _, err := q.WGServers.Get(ctx, "Wireguard7"); err == nil {
		t.Error("Get отсутствующего сервера обязан вернуть ошибку, как прежний 404 rc")
	}
	_, _ = q.WGServers.GetConfig(ctx, "Wireguard7")
	_, _ = q.WGServers.PeersRCFresh(ctx, "Wireguard7")
	_, _ = q.WGServers.GetASCParams(ctx, "Wireguard7", true)
	_, _ = q.WGServers.ASC3Fields(ctx, "Wireguard7")
	_, _ = q.WGServers.ASCParamsFresh(ctx, "Wireguard7", true)
	if f.E != 0 {
		t.Fatalf("E = %d, want 0", f.E)
	}
}

func TestResolveSystemName_AbsentInterface_NoResolver(t *testing.T) {
	q, f := absentIfaceQueries(t)
	if got := q.Interfaces.ResolveSystemName(context.Background(), "Proxy2"); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
	_ = q.Interfaces.SystemNames(context.Background(), []string{"Proxy2", "OpkgTun12"})
	if f.E != 0 {
		t.Fatalf("E = %d, want 0", f.E)
	}
}

// Свежий список не затирает соседа, обновлённого хуком, пока шёл запрос
// (seq-гард applyListLocked).
func TestInterfaceStore_SnapshotLive_KeepsHookedNeighbour(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	bg := &blockingGetter{Getter: fg, entered: make(chan struct{}, 1)}
	s := NewInterfaceStore(bg, NopLogger())
	ctx := context.Background()
	_, _ = s.Get(ctx, "Wireguard0") // bootstrap
	bg.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := s.Snapshot(ctx, SnapshotLive); done <- err }()
	bg.waitBlocked(t)
	s.OnLayerChanged("Wireguard0", "conf", "disabled") // хук, пока список в полёте
	close(bg.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "Wireguard0")
	if got == nil || got.ConfLayer != "disabled" {
		t.Fatalf("сосед затёрт снимком списка: %#v", got)
	}
}

// Опрос статистики managed-сервера-сироты (панель открыта — каждые ~5 с) не
// должен читать весь список на каждом вызове: ошибка отсутствия в KeyedStore
// не кэшируется, поэтому отсечка — только по кэшу.
func TestWGServers_AbsentInterface_NoListPerPoll(t *testing.T) {
	q, f := absentIfaceQueries(t)
	ctx := context.Background()
	_, _ = q.WGServers.Get(ctx, "Wireguard7") // бутстрап кэша
	before := f.ListCalls()
	for i := 0; i < 3; i++ {
		_, _ = q.WGServers.Get(ctx, "Wireguard7")
		_, _ = q.WGServers.GetConfig(ctx, "Wireguard7")
	}
	if n := f.ListCalls() - before; n != 0 {
		t.Fatalf("опрос сироты прочитал список %d раз", n)
	}
	if f.E != 0 {
		t.Fatalf("E = %d, want 0", f.E)
	}
}

// F546 S1: при внешнем сносе NDMS шлёт iflayerchanged РАНЬШЕ ifdestroyed —
// хук слоя сбрасывает список серверов, а кэш интерфейсов ещё держит имя.
// Перечитывание списка серверов не должно спрашивать снятый интерфейс по
// имени: не больше одного чтения полного списка, E == 0. Снимок моложе
// SnapshotRecent ещё может держать снятый — rc берётся из дерева, не по имени.
func TestWGServers_RefreshAfterExternalRemoval_NoPointRead(t *testing.T) {
	f := NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"},
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"},
	)
	q := NewQueries(Deps{Getter: f, Logger: NopLogger()})
	ctx := context.Background()
	if _, err := q.WGServers.List(ctx); err != nil {
		t.Fatalf("prime: %v", err)
	}

	f.Remove("Wireguard1") // ifdestroyed в очереди, не доставлен
	q.WGServers.InvalidateAll()
	before := f.ListCalls()
	servers, err := q.WGServers.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if f.E != 0 {
		t.Fatalf("E = %d, want 0", f.E)
	}
	if n := f.ListCalls() - before; n > 1 {
		t.Fatalf("чтений списка %d, want ≤ 1", n)
	}
	if len(servers) == 0 || servers[0].ID != "Wireguard0" {
		t.Fatalf("servers = %+v, want Wireguard0", servers)
	}
}
