package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// proxyIsOurs decides whether an NDMS ProxyN belongs to awg-manager's sing-box
// management, so disable/orphan-cleanup removes it. Subscription composites are
// the regression case: their proxy carries the subscription *label* as the
// interface description (not a tunnel tag), so the tag/slot heuristics miss it —
// they must be recognised via their explicitly-tracked proxy index.
func TestProxyIsOurs(t *testing.T) {
	tunnelTags := map[string]bool{"vless-1": true}
	ourPortSlots := map[int]bool{3: true}
	subProxyIdx := map[int]bool{7: true}

	cases := []struct {
		name string
		idx  int
		desc string
		want bool
	}{
		{"tunnel matched by description tag", 0, "vless-1", true},
		{"tunnel matched by port slot (empty desc)", 3, "", true},
		{"subscription composite (label description)", 7, "Моя подписка", true},
		{"foreign proxy with description", 9, "some-other-app", false},
		{"foreign proxy empty desc unknown slot", 5, "", false},
	}
	for _, c := range cases {
		got := proxyIsOurs(c.idx, c.desc, tunnelTags, ourPortSlots, subProxyIdx)
		if got != c.want {
			t.Errorf("%s: proxyIsOurs(%d, %q) = %v, want %v", c.name, c.idx, c.desc, got, c.want)
		}
	}
}

// nativeProxyKernelNames must return kernel names of ONLY the proxies that are
// not ours — the KeenOS-native SOCKS proxies a user can bind a router outbound
// to (#323). Ours (by tunnel tag or by port slot) are excluded.
func TestNativeProxyKernelNames(t *testing.T) {
	proxies := []proxyEntry{
		{idx: 0, desc: "My-Socks5", kernel: "t2s0"},      // native — keep
		{idx: 1, desc: "vless-1", kernel: "t2s1"},        // ours by tunnel tag — drop
		{idx: 2, desc: "", kernel: "t2s2"},               // ours by port slot — drop
		{idx: 3, desc: "another-native", kernel: "t2s3"}, // native — keep
	}
	got := nativeProxyKernelNames(proxies,
		map[string]bool{"vless-1": true}, // tunnelTags
		map[int]bool{2: true},            // ourPortSlots
		map[int]bool{},                   // subProxyIdx
	)
	if len(got) != 2 {
		t.Fatalf("want 2 native, got %d: %v", len(got), got)
	}
	want := map[string]bool{"t2s0": true, "t2s3": true}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected native proxy %q in %v", k, got)
		}
	}
}

// F546: снятие отсутствующего ProxyN не шлёт в NDMS ничего — `interface
// ProxyN down` по отсутствующему имени создаёт запись (стенд 5.01.C.6), а
// точечный show interface пишет E «unable to find» в журнал NDMS. commands
// nil: любая команда уронила бы тест.
func TestProxyManager_RemoveProxy_AbsentSendsNothing(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{"Proxy0":{"id":"Proxy0","type":"Proxy"}}`)
	// Любой POST слоя запросов, кроме резолвера имён, — сюда (точечных
	// хелперов у FakeGetter нет, F546).
	var posts atomic.Int32
	fg.SetPostHandler(func(any) (json.RawMessage, error) {
		posts.Add(1)
		return nil, errors.New("unexpected query POST")
	})
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	pm := NewProxyManager(q, nil)

	if err := pm.RemoveProxy(context.Background(), 5, "t"); err != nil {
		t.Fatalf("RemoveProxy(5): %v", err)
	}
	if n := posts.Load(); n != 0 {
		t.Fatalf("show interface Proxy5 ушёл %d раз", n)
	}
}

// NextFreeIndex обязан считать занятыми ЧУЖИЕ ProxyN (пользовательский Proxy0 —
// не наш) и переданные reserved; иначе перезапись пользовательского прокси.
func TestProxyManager_NextFreeIndex_SkipsForeignProxyAndReserved(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{
		"Proxy0":{"id":"Proxy0","type":"Proxy","description":"user's own"},
		"Proxy1":{"id":"Proxy1","type":"Proxy","description":"awgm"},
		"Bridge0":{"id":"Bridge0","type":"Bridge"},
		"ProxyX":{"id":"ProxyX","type":"Proxy"}
	}`)
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	pm := NewProxyManager(q, nil)

	idx, err := pm.NextFreeIndex(context.Background(), map[int]bool{2: true})
	if err != nil {
		t.Fatal(err)
	}
	if idx != 3 {
		t.Fatalf("NextFreeIndex = %d, want 3 (0,1 заняты NDMS, 2 — reserved)", idx)
	}
}

// oracleProxyManager — ProxyManager на оракуле: список, чтения и команды — в f.
func oracleProxyManager(f *query.FakeNDMS) *ProxyManager {
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	c := command.NewCommands(command.Deps{
		Poster:  f,
		Queries: q,
		Save:    command.NewSaveCoordinator(f, nil, time.Hour, time.Hour, 0, q.RunningConfig),
		IsOS5:   func() bool { return true },
	})
	return NewProxyManager(q, c)
}

// withProxyComponent — прошивка с компонентом proxy (EnsureProxy без него
// отказывает до NDMS).
func withProxyComponent(t *testing.T) {
	t.Helper()
	ndmsinfo.Reset()
	t.Cleanup(ndmsinfo.Reset)
	store := query.NewSystemInfoStore(nil, nil)
	store.Adopt(ndms.Version{Components: []string{"proxy"}}, "test")
	if err := ndmsinfo.Init(context.Background(), store, time.Second); err != nil {
		t.Fatal(err)
	}
}

// ProxyN нет — ни одной команды (`interface ProxyN down` создал бы запись), E
// и фантомов нет; есть — снимается; список не прочитан — ошибка без команд.
func TestRemoveProxy_AbsentNoCommands(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Proxy0", Type: "Proxy", Description: "t"})
	pm := oracleProxyManager(f)
	if err := pm.RemoveProxy(context.Background(), 5, "t"); err != nil {
		t.Fatalf("RemoveProxy(5): %v", err)
	}
	if len(f.Posts) != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v E=%d фантомов=%d", f.Posts, f.E, f.Phantoms)
	}

	if err := pm.RemoveProxy(context.Background(), 0, "t"); err != nil || f.Has("Proxy0") {
		t.Fatalf("Proxy0 не снят: err=%v posts=%v", err, f.Posts)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d", f.E, f.Phantoms)
	}

	f = query.NewFakeNDMS(ndms.Interface{ID: "Proxy0", Type: "Proxy", Description: "t"})
	pm = oracleProxyManager(f)
	f.FailList(errors.New("RCI не ответил"))
	if err := pm.RemoveProxy(context.Background(), 0, "t"); err == nil || len(f.Posts) != 0 {
		t.Fatalf("сбой списка: err=%v posts=%v", err, f.Posts)
	}
}

// Один список на весь набор: Proxy0 поднят — ничего, Proxy1 опущен — up по
// подтверждённому, Proxy2 нет — создаётся (его подтверждение после создания —
// второе и последнее чтение списка).
func TestSyncProxies_OneListForAll(t *testing.T) {
	withProxyComponent(t)
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "Proxy0", Type: "Proxy", Description: "a", State: "up"},
		ndms.Interface{ID: "Proxy1", Type: "Proxy", Description: "b", State: "down"},
	)
	f.ExpectCreate("Proxy2")
	pm := oracleProxyManager(f)

	err := pm.SyncProxies(context.Background(), []TunnelInfo{
		{Tag: "a", ListenPort: 1080, ProxyInterface: "Proxy0"},
		{Tag: "b", ListenPort: 1081, ProxyInterface: "Proxy1"},
		{Tag: "c", ListenPort: 1082, ProxyInterface: "Proxy2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.ListCalls(); n != 2 {
		t.Fatalf("чтений списка %d, want 2 (один на набор + подтверждение созданного)", n)
	}
	if !slices.Equal(f.Created, []string{"Proxy2"}) {
		t.Fatalf("created=%v", f.Created)
	}
	for _, p := range f.Posts {
		if strings.Contains(p, "Proxy0") {
			t.Fatalf("команда по поднятому Proxy0: %s", p)
		}
	}
	if !slices.ContainsFunc(f.Posts, func(p string) bool { return strings.Contains(p, `"Proxy1":{"up":true}`) }) {
		t.Fatalf("Proxy1 не поднят: %v", f.Posts)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d posts=%v", f.E, f.Phantoms, f.Posts)
	}
}

// Список не прочитан — ошибка без команд: ни создания «на всякий случай», ни up.
func TestSyncProxies_ListErrorNoCommands(t *testing.T) {
	withProxyComponent(t)
	f := query.NewFakeNDMS(ndms.Interface{ID: "Proxy1", Type: "Proxy", State: "down"})
	pm := oracleProxyManager(f)
	f.FailList(errors.New("RCI не ответил"))
	err := pm.SyncProxies(context.Background(), []TunnelInfo{
		{Tag: "b", ListenPort: 1081, ProxyInterface: "Proxy1"},
		{Tag: "c", ListenPort: 1082, ProxyInterface: "Proxy2"},
	})
	if err == nil || len(f.Posts) != 0 {
		t.Fatalf("err=%v posts=%v", err, f.Posts)
	}
}

// F574: чужой Proxy1 создан мимо нас, хук не доставлен — память его не знает.
// Выбор по свежему списку: Proxy2. Список не прочитан — ошибка.
func TestProxyManager_NextFreeIndex_FreshList(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Proxy0", Type: "Proxy"})
	pm := oracleProxyManager(f)
	if _, err := pm.queries.Interfaces.Get(context.Background(), "Proxy0"); err != nil { // тёплая карта
		t.Fatal(err)
	}
	f.Add(ndms.Interface{ID: "Proxy1", Type: "Proxy"})
	if idx, err := pm.NextFreeIndex(context.Background(), nil); err != nil || idx != 2 {
		t.Fatalf("idx=%d err=%v, want 2", idx, err)
	}
	f.FailList(errors.New("rci down"))
	if idx, err := pm.NextFreeIndex(context.Background(), nil); err == nil {
		t.Fatalf("idx=%d: want error on list failure", idx)
	}
}

// F574 M2: CreateProxy по индексу, занятому чужим ещё не видимым ProxyN, на
// прошивке с проверяемым ответом — ours=false (откат не сносит); созданный
// этой командой — ours=true.
func TestProxyManager_CreateProxy_Ours(t *testing.T) {
	ndmsinfo.Reset()
	t.Cleanup(ndmsinfo.Reset)
	store := query.NewSystemInfoStore(nil, nil)
	store.Adopt(ndms.Version{Release: "5.01.C.6.0-0", Components: []string{"proxy"}}, "test")
	if err := ndmsinfo.Init(context.Background(), store, time.Second); err != nil {
		t.Fatal(err)
	}
	f := query.NewFakeNDMS()
	pm := oracleProxyManager(f)
	pm.queries.Interfaces.SetCreatedBackoff(time.Millisecond)
	f.HideCreated(-1)
	f.Add(ndms.Interface{ID: "Proxy0", Type: "Proxy"})
	f.HideCreated(0)
	if ours, err := pm.CreateProxy(context.Background(), 0, 1080, "t"); ours || err == nil {
		t.Fatalf("чужой: ours=%v err=%v", ours, err)
	}
	f.ExpectCreate("Proxy1")
	if ours, err := pm.CreateProxy(context.Background(), 1, 1081, "t"); !ours || err != nil {
		t.Fatalf("свой: ours=%v err=%v", ours, err)
	}
}

// withProxy501 — прошивка 5.01 (ответ на создание проверяем, R39) с
// компонентом proxy.
func withProxy501(t *testing.T) {
	t.Helper()
	ndmsinfo.Reset()
	t.Cleanup(ndmsinfo.Reset)
	store := query.NewSystemInfoStore(nil, nil)
	store.Adopt(ndms.Version{Release: "5.01.C.6.0-0", Components: []string{"proxy"}}, "test")
	if err := ndmsinfo.Init(context.Background(), store, time.Second); err != nil {
		t.Fatal(err)
	}
}

// F577 (в): на индексе из хранилища — чужой ProxyN с другим description
// (индекс пережил выключение режима): ErrProxyForeign, ни одной команды.
func TestEnsureProxy_Foreign_NoCommands(t *testing.T) {
	withProxy501(t)
	f := query.NewFakeNDMS(ndms.Interface{ID: "Proxy3", Type: "Proxy", Description: "Work", State: "up"})
	pm := oracleProxyManager(f)
	if err := pm.EnsureProxy(context.Background(), 3, 1083, "sub1", "sub1"); !errors.Is(err, ErrProxyForeign) {
		t.Fatalf("err=%v", err)
	}
	if len(f.Posts) != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v E=%d фантомов=%d", f.Posts, f.E, f.Phantoms)
	}
}

// F577 (г): переименование тега — запись наша по СТАРОМУ description,
// настраивается новым; повтор (description уже новый) тоже проходит. Записи
// нет — создание голое, затем настройки.
func TestEnsureProxy_RenameByOldDescription(t *testing.T) {
	withProxy501(t)
	f := query.NewFakeNDMS(ndms.Interface{ID: "Proxy3", Type: "Proxy", Description: "old", State: "up"})
	pm := oracleProxyManager(f)
	ctx := context.Background()
	if err := pm.EnsureProxy(ctx, 3, 1083, "new", "old"); err != nil {
		t.Fatal(err)
	}
	if _, rec, ok, err := pm.queries.Interfaces.Confirm(ctx, "Proxy3"); err != nil || !ok || rec.Description != "new" {
		t.Fatalf("rec=%+v ok=%v err=%v posts=%v", rec, ok, err, f.Posts)
	}
	if err := pm.EnsureProxy(ctx, 3, 1083, "new", "old"); err != nil {
		t.Fatalf("повтор: %v", err)
	}

	f.ExpectCreate("Proxy4")
	posts := len(f.Posts)
	if err := pm.EnsureProxy(ctx, 4, 1084, "t", "t"); err != nil {
		t.Fatal(err)
	}
	if got := f.Posts[posts:]; len(got) != 2 || got[0] != `{"interface":{"Proxy4":{}}}` || !strings.Contains(got[1], `"description":"t"`) {
		t.Fatalf("команды: %v", got)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d", f.E, f.Phantoms)
	}
}

// F577 (опасение 3): слот туннеля a занят пользовательским прокси —
// SyncProxies не поднимает его и идёт дальше: свой Proxy1 поднят. RemoveProxy
// по чужому description — ErrProxyForeign без команд.
func TestSyncProxies_ForeignSlot_SkippedContinues(t *testing.T) {
	withProxyComponent(t)
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "Proxy0", Type: "Proxy", Description: "user", State: "down"},
		ndms.Interface{ID: "Proxy1", Type: "Proxy", Description: "b", State: "down"},
	)
	pm := oracleProxyManager(f)
	err := pm.SyncProxies(context.Background(), []TunnelInfo{
		{Tag: "a", ListenPort: 1080, ProxyInterface: "Proxy0"},
		{Tag: "b", ListenPort: 1081, ProxyInterface: "Proxy1"},
	})
	if err != nil || !slices.Equal(f.Posts, []string{`{"interface":{"Proxy1":{"up":true}}}`}) {
		t.Fatalf("err=%v posts=%v", err, f.Posts)
	}
	f.Posts = nil
	if err := pm.RemoveProxy(context.Background(), 0, "a"); !errors.Is(err, ErrProxyForeign) || len(f.Posts) != 0 || !f.Has("Proxy0") {
		t.Fatalf("err=%v posts=%v", err, f.Posts)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d", f.E, f.Phantoms)
	}
}
