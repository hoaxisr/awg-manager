package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Полный путь F576/R45 на оракуле NDMS: провижинер ниже — зеркало
// cmd/awg-manager confirmingOpkgTun (подтверждение свежим списком на каждый
// вызов, создание только при netdev.Free), а устройства ядра — каталог,
// подставленный в netdev.SysClassNet. Запись NDMS живёт с устройством:
// создание его заводит, снос убирает; внешнее снятие записи устройство
// оставляет (его держит sing-box).

type rcOracle struct{ *query.FakeNDMS }

func (g rcOracle) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if path == "/show/running-config" {
		return []byte(`{"message":[]}`), nil
	}
	return g.FakeNDMS.GetRaw(ctx, path)
}

func (g rcOracle) Get(ctx context.Context, path string, dst any) error {
	if path == "/show/running-config" {
		return json.Unmarshal([]byte(`{"message":[]}`), dst)
	}
	return g.FakeNDMS.Get(ctx, path, dst)
}

type oracleOpkg struct {
	f      *query.FakeNDMS
	cmds   *command.Commands
	ifaces *query.InterfaceStore
	sys    string
}

func newOracleOpkg(t *testing.T, f *query.FakeNDMS) *oracleOpkg {
	t.Helper()
	sys := t.TempDir()
	old := netdev.SysClassNet
	netdev.SysClassNet = sys
	t.Cleanup(func() { netdev.SysClassNet = old })
	q := query.NewQueries(query.Deps{Getter: rcOracle{f}, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	cmds := command.NewCommands(command.Deps{
		Poster:  f,
		Queries: q,
		Save:    command.NewSaveCoordinator(f, nil, time.Hour, time.Hour, 0, q.RunningConfig),
		IsOS5:   func() bool { return true },
	})
	return &oracleOpkg{f: f, cmds: cmds, ifaces: q.Interfaces, sys: sys}
}

func (o *oracleOpkg) setDevice(kernel string, present bool) {
	p := filepath.Join(o.sys, kernel)
	if present {
		_ = os.MkdirAll(p, 0o755)
	} else {
		_ = os.RemoveAll(p)
	}
	o.f.SetNetdev(kernel, present)
}

func (o *oracleOpkg) set(ctx context.Context, name string, do func(query.Confirmed) error) error {
	c, _, ok, err := o.ifaces.Confirm(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("интерфейса %s %w", name, ErrIfaceAbsent)
	}
	return do(c)
}

func (o *oracleOpkg) teardown(ctx context.Context, name string, do func(query.Confirmed) error) error {
	c, _, ok, err := o.ifaces.Confirm(ctx, name)
	if err != nil || !ok {
		return err
	}
	return do(c)
}

func (o *oracleOpkg) CreateOpkgTunWithSecurityLevel(ctx context.Context, name, desc, level string) error {
	kernel, _ := ndms.KernelName(name)
	free, err := netdev.Absent(kernel)
	if err != nil {
		return fmt.Errorf("create opkgtun %s: %w", name, err)
	}
	if _, err := o.cmds.Interfaces.CreateOpkgTunWithSecurityLevel(ctx, name, desc, level, free); err != nil {
		return err
	}
	o.setDevice(kernel, true)
	return nil
}

func (o *oracleOpkg) OpkgTunRecord(ctx context.Context, name string) (string, bool, error) {
	_, rec, ok, err := o.ifaces.Confirm(ctx, name)
	if err != nil || !ok {
		return "", false, err
	}
	return rec.Description, true, nil
}

func (o *oracleOpkg) DeleteOpkgTun(ctx context.Context, name string) error {
	return o.teardown(ctx, name, func(c query.Confirmed) error {
		if err := o.cmds.Interfaces.DeleteOpkgTun(ctx, c); err != nil {
			return err
		}
		kernel, _ := ndms.KernelName(name)
		o.setDevice(kernel, false)
		return nil
	})
}

func (o *oracleOpkg) SetSecurityLevel(ctx context.Context, name, level string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.SetSecurityLevel(ctx, c, level) })
}
func (o *oracleOpkg) SetIPGlobal(ctx context.Context, name string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.SetIPGlobal(ctx, c) })
}
func (o *oracleOpkg) SetAddress(ctx context.Context, name, addr, mask string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.SetAddress(ctx, c, addr, mask) })
}
func (o *oracleOpkg) ClearAddress(ctx context.Context, name string) error {
	return o.teardown(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.ClearAddress(ctx, c) })
}
func (o *oracleOpkg) SetIPv6Address(ctx context.Context, name, addr string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.SetIPv6Address(ctx, c, addr) })
}
func (o *oracleOpkg) ClearIPv6Address(ctx context.Context, name string) error {
	return o.teardown(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.ClearIPv6Address(ctx, c) })
}
func (o *oracleOpkg) SetMTU(ctx context.Context, name string, mtu int) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.SetMTU(ctx, c, mtu) })
}
func (o *oracleOpkg) InterfaceUp(ctx context.Context, name string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.InterfaceUp(ctx, c) })
}
func (o *oracleOpkg) InterfaceDown(ctx context.Context, name string) error {
	return o.teardown(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.InterfaceDown(ctx, c) })
}
func (o *oracleOpkg) SetPermitAllACL(ctx context.Context, name string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.SetPermitAllACL(ctx, c) })
}
func (o *oracleOpkg) RemovePermitAllACL(ctx context.Context, name string) error {
	return o.teardown(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.RemovePermitAllACL(ctx, c) })
}
func (o *oracleOpkg) SetPermitAllACLv6(ctx context.Context, name string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.SetPermitAllACLv6(ctx, c) })
}
func (o *oracleOpkg) RemovePermitAllACLv6(ctx context.Context, name string) error {
	return o.teardown(ctx, name, func(c query.Confirmed) error { return o.cmds.Interfaces.RemovePermitAllACLv6(ctx, c) })
}

// DefaultRouteProvider по тем же правилам подтверждения.
func (o *oracleOpkg) SetDefaultRoute(ctx context.Context, name string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.Routes.SetDefaultRoute(ctx, c) })
}
func (o *oracleOpkg) RemoveDefaultRoute(ctx context.Context, name string) error {
	return o.teardown(ctx, name, func(c query.Confirmed) error { return o.cmds.Routes.RemoveDefaultRoute(ctx, c) })
}
func (o *oracleOpkg) SetIPv6DefaultRoute(ctx context.Context, name string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.Routes.SetIPv6DefaultRoute(ctx, c) })
}
func (o *oracleOpkg) RemoveIPv6DefaultRoute(ctx context.Context, name string) error {
	return o.teardown(ctx, name, func(c query.Confirmed) error { return o.cmds.Routes.RemoveIPv6DefaultRoute(ctx, c) })
}

// LiveOpkgTunIndices — устройства ядра из подставленного /sys/class/net.
func (o *oracleOpkg) LiveOpkgTunIndices(context.Context) (map[int]bool, error) {
	ents, err := os.ReadDir(o.sys)
	if err != nil {
		return nil, err
	}
	live := map[int]bool{}
	for _, e := range ents {
		if n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "opkgtun")); err == nil && strings.HasPrefix(e.Name(), "opkgtun") {
			live[n] = true
		}
	}
	return live, nil
}

// scan — Deps.OpkgTunScan, как opkgTunScanner в cmd/awg-manager.
func (o *oracleOpkg) scan(ctx context.Context, description string) ([]string, error) {
	all, err := o.ifaces.List(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, i := range all {
		if strings.HasPrefix(i.ID, "OpkgTun") && i.Description == description {
			ids = append(ids, i.ID)
		}
	}
	return ids, nil
}

// records — занятость записями NDMS (ndmsHolders в cmd/awg-manager).
func (o *oracleOpkg) records() opkgtun.Source {
	return opkgtun.Source{Name: "записи NDMS", Read: func(ctx context.Context) (opkgtun.Taken, error) {
		all, err := o.ifaces.List(ctx)
		if err != nil {
			return nil, err
		}
		out := opkgtun.Taken{}
		for _, i := range all {
			if n, err := strconv.Atoi(strings.TrimPrefix(i.ID, "OpkgTun")); err == nil && strings.HasPrefix(i.ID, "OpkgTun") {
				out[n] = opkgtun.AnonHolder("запись NDMS " + i.ID)
			}
		}
		return out, nil
	}}
}

// externalRemove — `no interface X` из веб-интерфейса: записи нет, хук
// ifdestroyed доставлен, устройство осталось за sing-box.
func (o *oracleOpkg) externalRemove(name string) {
	o.f.Remove(name)
	o.ifaces.OnDestroyed(name)
}

func (o *oracleOpkg) clean(t *testing.T) {
	t.Helper()
	if o.f.E != 0 || o.f.C != 0 || o.f.Phantoms != 0 {
		t.Fatalf("E=%d C=%d фантомов=%d, want 0; posts=%v", o.f.E, o.f.C, o.f.Phantoms, o.f.Posts)
	}
}

func wireOracle(t *testing.T, h *policyTunEnableHarness) *oracleOpkg {
	t.Helper()
	o := newOracleOpkg(t, query.NewFakeNDMS())
	h.svc.deps.OpkgTun = o
	h.svc.deps.DefaultRoute = o
	h.svc.deps.OpkgTunIndices = o
	h.svc.deps.OpkgTunScan = o.scan
	h.svc.deps.OpkgTunPool = testOpkgTunPool(h.svc, o.records())
	old := fakeIPLinkPresent
	fakeIPLinkPresent = func(_ context.Context, iface string) bool {
		_, err := os.Stat(filepath.Join(o.sys, iface))
		return err == nil
	}
	t.Cleanup(func() { fakeIPLinkPresent = old })
	return o
}

func setRouterEnabled(t *testing.T, h *policyTunEnableHarness) storage.SingboxRouterSettings {
	t.Helper()
	if err := h.store.Update(func(cur *storage.Settings) error { cur.SingboxRouter.Enabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	all, err := h.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return all.SingboxRouter
}

func policyTunInboundIface(t *testing.T, h *policyTunEnableHarness) string {
	t.Helper()
	cfg, err := h.svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range cfg.Inbounds {
		if in.Tag == "tun-in" {
			return in.InterfaceName
		}
	}
	return ""
}

// R45: включение → выключение (запись удержана) → включение. Второе
// включение запись не создаёт (её устройство живо — Create отказал бы), а
// настраивает удержанную; ни E, ни C, ни фантомов.
func TestF576_PolicyTunReenableReusesHeldRecord(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	o := wireOracle(t, h)
	o.f.ExpectCreate("OpkgTun0")
	ctx := context.Background()

	if err := h.svc.Enable(ctx); err != nil {
		t.Fatalf("Enable #1: %v", err)
	}
	if err := h.svc.Disable(ctx); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if !o.f.Has("OpkgTun0") {
		t.Fatal("выключение обязано удержать запись")
	}
	if _, err := netdev.Absent("opkgtun0"); !errors.Is(err, netdev.ErrPresent) {
		t.Fatalf("у удержанной записи живое устройство, Absent: %v", err)
	}
	sb := h.svc.deps.Singbox.(*fakeSingbox)
	starts, reloads := sb.startCalls, sb.reloadCalls
	mark := len(o.f.Posts)

	if err := h.svc.Enable(ctx); err != nil {
		t.Fatalf("Enable #2: %v", err)
	}
	level := false
	for _, p := range o.f.Posts[mark:] {
		level = level || strings.Contains(p, `{"OpkgTun0":{"security-level":{"public":true}}}`)
	}
	if !level {
		t.Fatalf("удержанная запись не настроена (security-level): %v", o.f.Posts[mark:])
	}
	if len(o.f.Created) != 1 {
		t.Fatalf("запись создана %d раз, want 1: %v", len(o.f.Created), o.f.Created)
	}
	if st := h.loadPolicyTun(t); st == nil || !st.Provisioned || st.Index != 0 {
		t.Fatalf("persist = %+v, want provisioned 0", st)
	}
	if got := policyTunInboundIface(t, h); got != "opkgtun0" {
		t.Fatalf("tun-in = %q, want opkgtun0", got)
	}
	if sb.startCalls != starts || sb.reloadCalls != reloads {
		t.Fatalf("sing-box перезапускался напрямую: start %d→%d reload %d→%d", starts, sb.startCalls, reloads, sb.reloadCalls)
	}
	o.clean(t)
}

// Внешнее снятие записи при живом устройстве: тик поднимает режим на другом
// номере без ошибки, и по старому имени не уходит ни одной команды.
func TestF576_PolicyTunExternalRemovalMovesIndex(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	o := wireOracle(t, h)
	o.f.ExpectCreate("OpkgTun0", "OpkgTun1")
	ctx := context.Background()

	if err := h.svc.Enable(ctx); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	sr := setRouterEnabled(t, h)
	o.externalRemove("OpkgTun0")
	mark := len(o.f.Posts)

	if err := h.svc.reconcilePolicyTun(ctx, sr); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if st := h.loadPolicyTun(t); st == nil || !st.Provisioned || st.Index != 1 {
		t.Fatalf("persist = %+v, want provisioned 1", st)
	}
	if got := policyTunInboundIface(t, h); got != "opkgtun1" {
		t.Fatalf("tun-in = %q, want opkgtun1", got)
	}
	for _, p := range o.f.Posts[mark:] {
		if strings.Contains(p, `"OpkgTun0"`) || strings.Contains(p, "OpkgTun0 ") {
			t.Fatalf("команда по снятому OpkgTun0: %s", p)
		}
	}
	o.clean(t)
}

// Heal «tun-инбаунд пропал» идёт тем же включением: удержанную живую запись
// он настраивает, а не создаёт.
func TestF576_PolicyTunInboundHealReusesRecord(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	o := wireOracle(t, h)
	o.f.ExpectCreate("OpkgTun0")
	ctx := context.Background()

	if err := h.svc.Enable(ctx); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	sr := setRouterEnabled(t, h)
	cfg, err := h.svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Inbounds = filterPolicyTunInbound(cfg.Inbounds)
	if err := h.svc.persistConfigDirect(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	if err := h.svc.reconcilePolicyTun(ctx, sr); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := policyTunInboundIface(t, h); got != "opkgtun0" {
		t.Fatalf("tun-in = %q, want opkgtun0 вернуться", got)
	}
	if len(o.f.Created) != 1 {
		t.Fatalf("запись создана %d раз, want 1", len(o.f.Created))
	}
	if st := h.loadPolicyTun(t); st == nil || !st.Provisioned || st.Index != 0 {
		t.Fatalf("persist = %+v, want provisioned 0", st)
	}
	o.clean(t)
}

// Запись есть, но не наша — отказ без единой команды.
func TestF576_ProvisionForeignRecordRefusesWithoutCommands(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun3", Type: "OpkgTun", Description: "чужой"})
	o := newOracleOpkg(t, f)
	svc := newTestService(t, Deps{OpkgTun: o})

	_, err := svc.provisionOpkgTun(context.Background(), "OpkgTun3", policyTunDescription, "public")
	if err == nil || !strings.Contains(err.Error(), "чужой") {
		t.Fatalf("err = %v, want отказ с чужим description", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды по чужой записи: %v", f.Posts)
	}
	o.clean(t)
}

// fakeip делит provisionOpkgTun: его выключение запись сносит, поэтому
// повторное включение — снова создание F569, без E/C/фантомов.
func TestF576_FakeIPReenableRecreates(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	o := newOracleOpkg(t, query.NewFakeNDMS())
	h.svc.deps.OpkgTun = o
	h.svc.deps.OpkgTunIndices = o
	h.svc.deps.OpkgTunScan = o.scan
	h.svc.deps.OpkgTunPool = testOpkgTunPool(h.svc, o.records())
	old := fakeIPLinkPresent
	fakeIPLinkPresent = func(_ context.Context, iface string) bool {
		_, err := os.Stat(filepath.Join(o.sys, iface))
		return err == nil
	}
	t.Cleanup(func() { fakeIPLinkPresent = old })
	oldDrain := fakeIPScheduleDrain
	fakeIPScheduleDrain = func(removeReject func()) { removeReject() }
	t.Cleanup(func() { fakeIPScheduleDrain = oldDrain })
	o.f.ExpectCreate("OpkgTun0")
	ctx := context.Background()

	if err := h.svc.Enable(ctx); err != nil {
		t.Fatalf("Enable #1: %v", err)
	}
	if err := h.svc.Disable(ctx); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if o.f.Has("OpkgTun0") {
		t.Fatal("выключение fakeip запись сносит")
	}
	o.f.ExpectCreate("OpkgTun0") // повторное создание того же имени — намеренное
	if err := h.svc.Enable(ctx); err != nil {
		t.Fatalf("Enable #2: %v", err)
	}
	if len(o.f.Created) != 2 {
		t.Fatalf("создания = %v, want 2", o.f.Created)
	}
	if st := h.loadFakeIP(t); st == nil || !st.Provisioned {
		t.Fatalf("persist = %+v, want provisioned", st)
	}
	o.clean(t)
}
