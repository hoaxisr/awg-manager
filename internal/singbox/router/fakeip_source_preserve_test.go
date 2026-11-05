package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// #1160: при `ip nat` сегмента sing-box видит вместо клиента адрес tun, и
// source_ip_cidr не срабатывает. Source-preserve работает и в fakeip.

func armFakeIPSourcePreserve(t *testing.T, h *fakeIPEnableHarness, on bool, segs []string) *fakeNATState {
	t.Helper()
	if err := h.store.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.PolicyTunSourcePreserve = on
		cur.SingboxRouter.PolicyTunNATSegments = segs
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	state := &fakeNATState{nat: []query.NATEntry{{Interface: "Home"}}}
	h.svc.deps.NATState = state
	h.svc.deps.SegmentNAT = &recSegmentNAT{log: h.log, state: state}
	h.svc.deps.DefaultGateway = &fakeGateway{name: "PPPoE0"}
	return state
}

func enableFakeIPLive(t *testing.T, h *fakeIPEnableHarness) {
	t.Helper()
	h.svc.deps.IPTables = errProbeIPTables()
	if err := h.svc.enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
}

func reconcileFakeIP(t *testing.T, h *fakeIPEnableHarness) {
	t.Helper()
	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func fakeIPNATRecords(t *testing.T, h *fakeIPEnableHarness) []storage.PolicyTunNATSegment {
	t.Helper()
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return natSegmentsOf(all.OpkgTun)
}

// Галка применяется на тике и переживает следующий: реап не принимает записи
// работающего fakeip за миграционный мусор.
func TestFakeIPReconcile_AppliesSourcePreserveAndKeepsIt(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	armFakeIPSourcePreserve(t, h, true, []string{"Home"})
	enableFakeIPLive(t, h)
	h.log.calls = nil

	reconcileFakeIP(t, h)
	if !h.log.has("RemoveSegmentNAT:Home") || !h.log.has("SetStaticNAT:Home:PPPoE0") {
		t.Fatalf("source-preserve не применён: %v", h.log.calls)
	}
	segs := fakeIPNATRecords(t, h)
	if len(segs) != 1 || segs[0].Name != "Home" || segs[0].PriorMode != natModeDynamic {
		t.Fatalf("записи = %+v", segs)
	}

	h.log.calls = nil
	reconcileFakeIP(t, h)
	if h.log.has("SetSegmentNAT:Home") || h.log.has("RemoveStaticNAT:Home:PPPoE0") {
		t.Fatalf("второй тик снял применённое: %v", h.log.calls)
	}
	if len(fakeIPNATRecords(t, h)) != 1 {
		t.Fatalf("записи потеряны: %+v", fakeIPNATRecords(t, h))
	}
}

func TestFakeIPReconcile_RestoresRevoked(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	armFakeIPSourcePreserve(t, h, true, []string{"Home"})
	enableFakeIPLive(t, h)
	reconcileFakeIP(t, h)
	if err := h.store.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.PolicyTunSourcePreserve = false
		cur.SingboxRouter.PolicyTunNATSegments = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.log.calls = nil

	reconcileFakeIP(t, h)
	mustOrderCalls(t, h.log, "RemoveStaticNAT:Home:PPPoE0", "SetSegmentNAT:Home")
	if segs := fakeIPNATRecords(t, h); len(segs) != 0 {
		t.Fatalf("записи не сняты: %+v", segs)
	}
}

func TestFakeIPDisable_RestoresSegmentNAT(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	armFakeIPSourcePreserve(t, h, true, []string{"Home"})
	enableFakeIPLive(t, h)
	reconcileFakeIP(t, h)
	h.log.calls = nil

	if err := h.svc.disable(context.Background()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !h.log.has("SetSegmentNAT:Home") {
		t.Fatalf("сегмент не возвращён: %v", h.log.calls)
	}
	all, _ := h.store.Load()
	if all.OpkgTun != nil {
		t.Fatalf("запись осталась: %+v", all.OpkgTun)
	}
}

// Отказ восстановления: запись — единственный след прежнего NAT, стирать её нельзя.
func TestFakeIPDisable_RestoreFailureKeepsRecords(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	state := armFakeIPSourcePreserve(t, h, true, []string{"Home"})
	enableFakeIPLive(t, h)
	reconcileFakeIP(t, h)
	h.svc.deps.SegmentNAT = &recSegmentNAT{log: h.log, state: state, failAt: "SetSegmentNAT"}

	if err := h.svc.disable(context.Background()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if segs := fakeIPNATRecords(t, h); len(segs) != 1 {
		t.Fatalf("записи потеряны: %+v", segs)
	}
}

func TestReleaseFakeIPTunForRemoval_RestoresSegmentNAT(t *testing.T) {
	stubLinkAbsent(t)
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: stateFakeIPTun})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 1,
		PolicyTun: &storage.OpkgTunPolicyData{NATSegments: []storage.PolicyTunNATSegment{{Name: "Home", PriorMode: natModeDynamic}}}}); err != nil {
		t.Fatal(err)
	}
	log := &callLog{}
	state := &fakeNATState{}
	if err := ReleaseFakeIPTunForRemoval(context.Background(), Deps{
		SwapGate:   &netdev.SwapGate{},
		Settings:   store,
		OpkgTun:    &recOpkgTun{log: log},
		SegmentNAT: &recSegmentNAT{log: log, state: state},
		NATState:   state,
	}); err != nil {
		t.Fatalf("ReleaseFakeIPTunForRemoval: %v", err)
	}
	if !log.has("SetSegmentNAT:Home") {
		t.Fatalf("сегмент не возвращён: %v", log.calls)
	}
}

// Статус говорит о ПРИМЕНЁННОМ и в fakeip: на нём держится предупреждение UI.
func TestGetStatus_FakeIPSourcePreserveIsApplied(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	armFakeIPSourcePreserve(t, h, true, []string{"Home"})
	enableFakeIPLive(t, h)

	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.PolicyTunSourcePreserve == nil || *st.PolicyTunSourcePreserve {
		t.Fatalf("до применения = %v, want false", st.PolicyTunSourcePreserve)
	}
	reconcileFakeIP(t, h)
	st, err = h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.PolicyTunSourcePreserve == nil || !*st.PolicyTunSourcePreserve {
		t.Fatalf("после применения = %v, want true", st.PolicyTunSourcePreserve)
	}
}

// F775: при isolate-private NDMS не пускает private→private, и LAN не доходит
// до tun. Tun, созданный прежней версией, переводится на public на тике.
func TestFakeIPReconcile_HealsPrivateTun(t *testing.T) {
	rc := func(level string) []string {
		return []string{"interface OpkgTun0", "    security-level " + level, "!"}
	}
	for _, tc := range []struct {
		name  string
		lines []string
		err   error
		want  bool
	}{
		{"прежняя версия", rc("private"), nil, true},
		{"уже public", rc("public"), nil, false},
		{"блока нет", nil, nil, false},
		{"конфиг не прочитан", nil, errors.New("rc down"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeIPEnableHarness(t, "")
			enableFakeIPLive(t, h)
			rc := &fakeRunningConfig{lines: tc.lines, err: tc.err}
			h.svc.deps.RunningConfig = rc
			h.log.calls = nil
			reconcileFakeIP(t, h)
			// Свежее чтение стоит ~89 тиков ndm: на устоявшемся тике его быть не должно.
			if !tc.want && rc.fetched != 0 {
				t.Errorf("fetched = %d, want 0", rc.fetched)
			}
			if got := h.log.has("SetSecurityLevel:OpkgTun0:public"); got != tc.want {
				t.Fatalf("SetSecurityLevel = %v, want %v: %v", got, tc.want, h.log.calls)
			}
		})
	}
}
