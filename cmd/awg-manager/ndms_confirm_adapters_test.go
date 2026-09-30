package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// rcGetter — оракул плюс пути сверх его модели (running-config для ACL).
type rcGetter struct {
	*ndmsquery.FakeNDMS
	raw map[string]string
}

func (g rcGetter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if b, ok := g.raw[path]; ok {
		return []byte(b), nil
	}
	return g.FakeNDMS.GetRaw(ctx, path)
}

func (g rcGetter) Get(ctx context.Context, path string, dst any) error {
	if b, ok := g.raw[path]; ok {
		return json.Unmarshal([]byte(b), dst)
	}
	return g.FakeNDMS.Get(ctx, path, dst)
}

// confirmAdapters — все адаптеры подтверждения на одном оракуле: список,
// точечные чтения и команды идут в f.
type confirmAdapters struct {
	opkg   confirmingOpkgTun
	proxy  proxyNDMSCommands
	defrt  confirmingDefaultRoute
	nat    confirmingSegmentNAT
	static *routerStaticRouteAdapter
	permit confirmingPermitter
}

func newConfirmAdapters(t *testing.T, f *ndmsquery.FakeNDMS) confirmAdapters {
	t.Helper()
	// Создание OpkgTun проверяет /sys/class/net (F569) — не хоста, а свой.
	old := netdev.SysClassNet
	netdev.SysClassNet = t.TempDir()
	t.Cleanup(func() { netdev.SysClassNet = old })
	q := ndmsquery.NewQueries(ndmsquery.Deps{
		Getter: rcGetter{FakeNDMS: f, raw: map[string]string{"/show/running-config": `{"message":[]}`}},
		Logger: ndmsquery.NopLogger(),
		IsOS5:  func() bool { return true },
	})
	cmds := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  f,
		Queries: q,
		Save:    ndmscommand.NewSaveCoordinator(f, nil, time.Hour, time.Hour, 0, q.RunningConfig),
		IsOS5:   func() bool { return true },
	})
	opkg := confirmingOpkgTun{cmds.Interfaces, q.Interfaces}
	return confirmAdapters{
		opkg:   opkg,
		proxy:  proxyNDMSCommands{confirmingOpkgTun: opkg, routes: cmds.Routes},
		defrt:  confirmingDefaultRoute{cmds.Routes, q.Interfaces},
		nat:    confirmingSegmentNAT{cmds.NAT, q.Interfaces},
		static: &routerStaticRouteAdapter{routes: cmds.Routes, ifaces: q.Interfaces},
		permit: confirmingPermitter{cmds.Policies, q.Interfaces},
	}
}

// oracleClean — ни E, ни фантомов.
func oracleClean(t *testing.T, f *ndmsquery.FakeNDMS) {
	t.Helper()
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d, want 0/0; posts=%v", f.E, f.Phantoms, f.Posts)
	}
}

// teardowns — снос/опускание всех адаптеров по OpkgTun5 (сегмент и WAN —
// Bridge1/PPPoE0).
func teardowns(a confirmAdapters) map[string]func(context.Context) error {
	route := router.StaticRouteSpec{Interface: "OpkgTun5", Network: "198.18.0.0", Mask: "255.254.0.0"}
	return map[string]func(context.Context) error{
		"DeleteOpkgTun":          func(ctx context.Context) error { return a.opkg.DeleteOpkgTun(ctx, "OpkgTun5") },
		"InterfaceDown":          func(ctx context.Context) error { return a.opkg.InterfaceDown(ctx, "OpkgTun5") },
		"ClearAddress":           func(ctx context.Context) error { return a.opkg.ClearAddress(ctx, "OpkgTun5") },
		"ClearIPv6Address":       func(ctx context.Context) error { return a.opkg.ClearIPv6Address(ctx, "OpkgTun5") },
		"ClearIPGlobal":          func(ctx context.Context) error { return a.opkg.ClearIPGlobal(ctx, "OpkgTun5") },
		"RemovePermitAllACL":     func(ctx context.Context) error { return a.opkg.RemovePermitAllACL(ctx, "OpkgTun5") },
		"RemovePermitAllACLv6":   func(ctx context.Context) error { return a.opkg.RemovePermitAllACLv6(ctx, "OpkgTun5") },
		"RemoveDefaultRoute":     func(ctx context.Context) error { return a.defrt.RemoveDefaultRoute(ctx, "OpkgTun5") },
		"RemoveIPv6DefaultRoute": func(ctx context.Context) error { return a.defrt.RemoveIPv6DefaultRoute(ctx, "OpkgTun5") },
		"RemoveSegmentNAT":       func(ctx context.Context) error { return a.nat.RemoveSegmentNAT(ctx, "Bridge1") },
		"RemoveStaticNAT":        func(ctx context.Context) error { return a.nat.RemoveStaticNAT(ctx, "Bridge1", "PPPoE0") },
		"RemoveStaticRoute":      func(ctx context.Context) error { return a.static.RemoveStaticRoute(ctx, route) },
	}
}

// sets — настройка существующего у всех адаптеров.
func sets(a confirmAdapters) map[string]func(context.Context) error {
	route := router.StaticRouteSpec{Interface: "OpkgTun5", Network: "198.18.0.0", Mask: "255.254.0.0"}
	return map[string]func(context.Context) error{
		"SetIPGlobal": func(ctx context.Context) error { return a.opkg.SetIPGlobal(ctx, "OpkgTun5") },
		"SetAddress": func(ctx context.Context) error {
			return a.opkg.SetAddress(ctx, "OpkgTun5", "172.18.0.1", "255.255.255.252")
		},
		"SetIPv6Address":              func(ctx context.Context) error { return a.opkg.SetIPv6Address(ctx, "OpkgTun5", "fdfe::1/126") },
		"SetMTU":                      func(ctx context.Context) error { return a.opkg.SetMTU(ctx, "OpkgTun5", 1500) },
		"InterfaceUp":                 func(ctx context.Context) error { return a.opkg.InterfaceUp(ctx, "OpkgTun5") },
		"SetDescription":              func(ctx context.Context) error { return a.opkg.SetDescription(ctx, "OpkgTun5", "d") },
		"SetSecurityLevel":            func(ctx context.Context) error { return a.opkg.SetSecurityLevel(ctx, "OpkgTun5", "public") },
		"SetPermitAllACL":             func(ctx context.Context) error { return a.opkg.SetPermitAllACL(ctx, "OpkgTun5") },
		"SetPermitAllACLv6":           func(ctx context.Context) error { return a.opkg.SetPermitAllACLv6(ctx, "OpkgTun5") },
		"SetDefaultRoute":             func(ctx context.Context) error { return a.defrt.SetDefaultRoute(ctx, "OpkgTun5") },
		"SetIPv6DefaultRoute":         func(ctx context.Context) error { return a.defrt.SetIPv6DefaultRoute(ctx, "OpkgTun5") },
		"EnsureDefaultRouteCandidacy": func(ctx context.Context) error { return a.proxy.EnsureDefaultRouteCandidacy(ctx, "OpkgTun5") },
		"AddStaticRoute":              func(ctx context.Context) error { return a.static.AddStaticRoute(ctx, route) },
		"PermitInterface":             func(ctx context.Context) error { return a.permit.PermitInterface(ctx, "Policy0", "OpkgTun5", 1) },
	}
}

// Интерфейса нет: снос и опускание — nil без единой команды (`interface X …`
// по отсутствующему создаёт X, ссылка на него пишет E в журнал ndm).
func TestConfirmingOpkgTun_TeardownAbsentIsNil(t *testing.T) {
	f := ndmsquery.NewFakeNDMS() // ни OpkgTun5, ни Bridge1/PPPoE0
	for name, call := range teardowns(newConfirmAdapters(t, f)) {
		if err := call(context.Background()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды по отсутствующему: %v", f.Posts)
	}
	oracleClean(t, f)
}

// Static NAT: снятие при отсутствии ЛЮБОГО из двух — nil без команды.
func TestConfirmingSegmentNAT_RemoveStaticOneAbsentIsNil(t *testing.T) {
	// Bridge0, а не Bridge1: имя Bridge1 таблица ndms.KernelName не знает
	// (R32), и резолвер вслед за списком спросил бы его — чтение, не команда,
	// но len(Posts) его посчитал бы.
	f := ndmsquery.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"}) // PPPoE0 нет
	if err := newConfirmAdapters(t, f).nat.RemoveStaticNAT(context.Background(), "Bridge0", "PPPoE0"); err != nil {
		t.Fatal(err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команда по отсутствующему WAN: %v", f.Posts)
	}
	oracleClean(t, f)
}

// Интерфейса нет: настройка — ошибка с именем, без команды.
func TestConfirmingOpkgTun_SetAbsentIsError(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	for name, call := range sets(newConfirmAdapters(t, f)) {
		err := call(context.Background())
		if !errors.Is(err, router.ErrIfaceAbsent) || !strings.Contains(err.Error(), "OpkgTun5") {
			t.Errorf("%s: %v, want ошибку с именем", name, err)
		}
	}
	// Static NAT: WAN есть, сегмента нет — ошибка с именем сегмента.
	f.Add(ndms.Interface{ID: "PPPoE0", Type: "PPPoE"})
	a := newConfirmAdapters(t, f)
	if err := a.nat.SetStaticNAT(context.Background(), "Bridge1", "PPPoE0"); err == nil || !strings.Contains(err.Error(), "Bridge1") {
		t.Errorf("SetStaticNAT: %v", err)
	}
	if err := a.nat.SetSegmentNAT(context.Background(), "Bridge1"); err == nil || !strings.Contains(err.Error(), "Bridge1") {
		t.Errorf("SetSegmentNAT: %v", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды по отсутствующему: %v", f.Posts)
	}
	oracleClean(t, f)
}

// Static NAT: сегмент есть, WAN нет — ошибка с именем WAN, но НЕ
// ErrIfaceAbsent: router.segmentGone принял бы её за снятый сегмент (F563).
func TestConfirmingSegmentNAT_SetStaticWANAbsentIsNotSegmentGone(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"}) // PPPoE0 нет
	err := newConfirmAdapters(t, f).nat.SetStaticNAT(context.Background(), "Bridge0", "PPPoE0")
	if !errors.Is(err, router.ErrWANAbsent) || errors.Is(err, router.ErrIfaceAbsent) || !strings.Contains(err.Error(), "PPPoE0") {
		t.Fatalf("SetStaticNAT: %v, want ErrWANAbsent с именем WAN без ErrIfaceAbsent", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команда по отсутствующему WAN: %v", f.Posts)
	}
	oracleClean(t, f)
}

// Список не прочитан: и настройка, и снос — ошибка без команды (решение 4).
func TestConfirmingAdapters_ListErrorNoCommand(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(
		ndms.Interface{ID: "OpkgTun5", Type: "OpkgTun"},
		ndms.Interface{ID: "Bridge1", Type: "Bridge"},
		ndms.Interface{ID: "PPPoE0", Type: "PPPoE"},
	)
	a := newConfirmAdapters(t, f)
	errRCI := errors.New("RCI не ответил")
	f.FailList(errRCI)
	calls := teardowns(a)
	for k, v := range sets(a) {
		calls[k] = v
	}
	calls["SetStaticNAT"] = func(ctx context.Context) error { return a.nat.SetStaticNAT(ctx, "Bridge1", "PPPoE0") }
	for name, call := range calls {
		if err := call(context.Background()); !errors.Is(err, errRCI) {
			t.Errorf("%s: %v, want ошибку списка", name, err)
		}
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды без подтверждения: %v", f.Posts)
	}
}

// F569: живое opkgtunN — создание отказывает без RCI, текст называет
// устройство; E, C и фантомов нет.
func TestConfirmingOpkgTun_CreateDevicePresent_NoRCI(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	f.ExpectCreate("OpkgTun5")
	a := newConfirmAdapters(t, f)
	if err := os.Mkdir(filepath.Join(netdev.SysClassNet, "opkgtun5"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := a.opkg.CreateOpkgTunWithSecurityLevel(context.Background(), "OpkgTun5", "awgm policy-tun", "public")
	if !errors.Is(err, netdev.ErrPresent) || !strings.Contains(err.Error(), "opkgtun5") {
		t.Fatalf("err = %v", err)
	}
	if len(f.Posts) != 0 || f.ListCalls() != 0 || f.C != 0 || f.Has("OpkgTun5") {
		t.Fatalf("posts=%v lists=%d C=%d", f.Posts, f.ListCalls(), f.C)
	}
	oracleClean(t, f)
}

// Интерфейс есть: снос уходит и снимает запись; E и фантомов нет.
func TestConfirmingOpkgTun_PresentDeletes(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(ndms.Interface{ID: "OpkgTun5", Type: "OpkgTun"})
	if err := newConfirmAdapters(t, f).opkg.DeleteOpkgTun(context.Background(), "OpkgTun5"); err != nil {
		t.Fatal(err)
	}
	if f.Has("OpkgTun5") {
		t.Fatalf("запись не снята: %v", f.Posts)
	}
	oracleClean(t, f)
}

// Стоимость: подтверждение на каждый шаг — один полный список на вызов.
// Последовательности повторяют прод-потоки дословно:
//   - policy-tun: policytun_enable.go (create → ip global → ACL → адрес → v6 →
//     ACLv6 → MTU → up) и парковка дефолта v4+v6;
//   - старт прокси-роли (wdtt): ndmsres create (+MTU), адрес, up, ip global,
//     ACL, кандидатура default route, permit в одной политике.
func TestConfirmingAdapters_ProvisionListReads(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(a confirmAdapters) error
		want int
	}{
		{"policy-tun", func(a confirmAdapters) error {
			steps := []func() error{
				func() error {
					return a.opkg.CreateOpkgTunWithSecurityLevel(ctx, "OpkgTun5", "awgm policy-tun", "public")
				},
				func() error { return a.opkg.SetIPGlobal(ctx, "OpkgTun5") },
				func() error { return a.opkg.SetPermitAllACL(ctx, "OpkgTun5") },
				func() error { return a.opkg.SetAddress(ctx, "OpkgTun5", "172.18.0.1", "255.255.255.252") },
				func() error { return a.opkg.SetIPv6Address(ctx, "OpkgTun5", "fdfe::1/126") },
				func() error { return a.opkg.SetPermitAllACLv6(ctx, "OpkgTun5") },
				func() error { return a.opkg.SetMTU(ctx, "OpkgTun5", 1500) },
				func() error { return a.opkg.InterfaceUp(ctx, "OpkgTun5") },
				func() error { return a.defrt.SetDefaultRoute(ctx, "OpkgTun5") },
				func() error { return a.defrt.SetIPv6DefaultRoute(ctx, "OpkgTun5") },
			}
			for _, s := range steps {
				if err := s(); err != nil {
					return err
				}
			}
			return nil
		}, 10},
		{"proxy-role", func(a confirmAdapters) error {
			steps := []func() error{
				func() error {
					return a.proxy.CreateOpkgTunWithSecurityLevel(ctx, "OpkgTun5", "AWGM WDTT Raw Client: x", "public")
				},
				func() error { return a.proxy.SetMTU(ctx, "OpkgTun5", 1400) },
				func() error { return a.proxy.SetAddress(ctx, "OpkgTun5", "10.9.0.2", "255.255.255.0") },
				func() error { return a.proxy.InterfaceUp(ctx, "OpkgTun5") },
				func() error { return a.proxy.SetIPGlobal(ctx, "OpkgTun5") },
				func() error { return a.proxy.SetPermitAllACL(ctx, "OpkgTun5") },
				func() error { return a.proxy.EnsureDefaultRouteCandidacy(ctx, "OpkgTun5") },
				func() error { return a.permit.PermitInterface(ctx, "Policy0", "OpkgTun5", 1) },
			}
			for _, s := range steps {
				if err := s(); err != nil {
					return err
				}
			}
			return nil
		}, 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := ndmsquery.NewFakeNDMS()
			f.ExpectCreate("OpkgTun5")
			a := newConfirmAdapters(t, f)
			if err := c.run(a); err != nil {
				t.Fatal(err)
			}
			if got := f.ListCalls(); got != c.want {
				t.Fatalf("чтений списка %d, want %d", got, c.want)
			}
			oracleClean(t, f)
		})
	}
}

// Серия CIDR-маршрутов через ForInterface: одно чтение списка на 100
// маршрутов, E и фантомов нет (F546, R25).
func TestStaticRoutesForInterface_OneListFor100(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(ndms.Interface{ID: "OpkgTun5", Type: "OpkgTun"})
	a := newConfirmAdapters(t, f)
	ctx := context.Background()
	rt, ok, err := a.static.ForInterface(ctx, "OpkgTun5")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	for i := 0; i < 100; i++ {
		if err := rt.AddStaticRoute(ctx, router.StaticRouteSpec{Interface: "OpkgTun5", Network: fmt.Sprintf("149.%d.0.0", i), Mask: "255.255.0.0"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.ListCalls(); n != 1 || len(f.Posts) != 100 {
		t.Fatalf("списков %d, команд %d; want 1/100", n, len(f.Posts))
	}
	oracleClean(t, f)
	// Спека на другой интерфейс через это подтверждение не уходит.
	if err := rt.AddStaticRoute(ctx, router.StaticRouteSpec{Interface: "OpkgTun6", Network: "149.200.0.0", Mask: "255.255.0.0"}); err == nil || len(f.Posts) != 100 {
		t.Fatalf("чужой интерфейс: err=%v posts=%d", err, len(f.Posts))
	}
}

// Нет — ok=false без ошибки; список не прочитан — ошибка; команд нет.
func TestStaticRoutesForInterface_AbsentOrListError(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	a := newConfirmAdapters(t, f)
	if rt, ok, err := a.static.ForInterface(context.Background(), "OpkgTun5"); rt != nil || ok || err != nil {
		t.Fatalf("absent: rt=%v ok=%v err=%v", rt, ok, err)
	}
	f.FailList(errors.New("RCI не ответил"))
	if _, ok, err := a.static.ForInterface(context.Background(), "OpkgTun5"); ok || err == nil {
		t.Fatalf("list error: ok=%v err=%v", ok, err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("posts=%v", f.Posts)
	}
	oracleClean(t, f)
}
