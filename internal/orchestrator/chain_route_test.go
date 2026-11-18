package orchestrator

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F806/#1233: «подключаться через туннель» у kernel-туннеля. Маршрут до
// endpoint обязан лечь в интерфейс родителя, а не в его WAN — иначе выбор
// сохраняется, но трафик идёт мимо родителя. ActiveWAN при этом остаётся WAN
// родителя: по нему ребёнок реагирует на падение канала.
func TestResumeKernel_ChainRoutesThroughParentIface(t *testing.T) {
	fake := &fakeKernelOp{}
	o := newResumeOrch(t, fake, &storage.AWGTunnel{ID: "awg10", Name: "amst", ActiveWAN: "eth3"})
	if err := o.store.Create(&storage.AWGTunnel{
		ID:           "awg11",
		Name:         "riga",
		ISPInterface: "tunnel:awg10",
		Peer:         storage.AWGPeer{Endpoint: "203.0.113.7:51820"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := o.executeResumeKernel(context.Background(), Action{Type: ActionResumeKernel, Tunnel: "awg11"}); err != nil {
		t.Fatal(err)
	}

	calls := fake.routeCalls()
	if len(calls) != 1 {
		t.Fatalf("вызовов SetupEndpointRoute %d, ожидали 1", len(calls))
	}
	if calls[0].kernelDevice != "opkgtun10" || calls[0].ispName != "tunnel:awg10" {
		t.Errorf("маршрут мимо родителя: %+v", calls[0])
	}
	child, _ := o.store.Get("awg11")
	if child.ActiveWAN != "eth3" {
		t.Errorf("ActiveWAN = %q, ожидали WAN родителя eth3", child.ActiveWAN)
	}
}

// Родитель NativeWG: интерфейс ядра — nwgN по индексу, а не opkgtunN по ID.
func TestRouteDevice_NativeWGParent(t *testing.T) {
	o := newResumeOrch(t, &fakeKernelOp{}, &storage.AWGTunnel{ID: "awg10", Backend: "nativewg", NWGIndex: 3})
	dev, isp, err := RouteDevice(o.store, "tunnel:awg10", "eth3")
	if err != nil || dev != "nwg3" || isp != "tunnel:awg10" {
		t.Fatalf("RouteDevice = %q, %q, %v", dev, isp, err)
	}
}

// Маршрут ребёнка вида `dev opkgtunN` ядро сносит вместе с линком родителя.
// Поднялся родитель — маршруты работающих kernel-детей ставятся заново;
// иначе ребёнок лежит на unreachable до своего следующего старта. Неработающему
// и nativewg-ребёнку (у того `connect via` в NDMS) маршрут не ставится.
func TestExecuteActions_ParentUpRefreshesChildRoutes(t *testing.T) {
	fake := &fakeKernelOp{}
	o := newResumeOrch(t, fake, &storage.AWGTunnel{ID: "awg10", Name: "amst", ISPInterface: "eth3", ActiveWAN: "eth3"})
	children := []struct {
		stored  *storage.AWGTunnel
		running bool
	}{
		{&storage.AWGTunnel{ID: "awg11", Name: "on", ISPInterface: "tunnel:awg10", Peer: storage.AWGPeer{Endpoint: "203.0.113.7:51820"}}, true},
		{&storage.AWGTunnel{ID: "awg12", Name: "down", Enabled: true, ISPInterface: "tunnel:awg10", Peer: storage.AWGPeer{Endpoint: "203.0.113.8:51820"}}, false},
		{&storage.AWGTunnel{ID: "awg13", Name: "other", ISPInterface: "eth3", Peer: storage.AWGPeer{Endpoint: "203.0.113.9:51820"}}, true},
		{&storage.AWGTunnel{ID: "awg14", Name: "nwg", Backend: "nativewg", ISPInterface: "tunnel:awg10", Peer: storage.AWGPeer{Endpoint: "203.0.113.10:51820"}}, true},
	}
	for _, c := range children {
		if err := o.store.Create(c.stored); err != nil {
			t.Fatal(err)
		}
		st := tunnelStateFromStored(c.stored)
		if st.Backend == "" {
			st.Backend = "kernel"
		}
		st.Running = c.running
		o.state.tunnels[c.stored.ID] = st
	}

	if err := o.executeActions(context.Background(), []Action{{Type: ActionResumeKernel, Tunnel: "awg10"}}); err != nil {
		t.Fatal(err)
	}

	var child []endpointRouteCall
	for _, c := range fake.routeCalls() {
		if c.tunnelID != "awg10" {
			child = append(child, c)
		}
	}
	if len(child) != 1 || child[0].tunnelID != "awg11" || child[0].kernelDevice != "opkgtun10" {
		t.Fatalf("маршруты детей: %+v", child)
	}
}

// Ребёнок, поднятый раньше родителя (порядок бута случаен): WAN родителя
// неизвестен, но старт не отказывает — маршрут встанет с подъёмом родителя.
func TestReconcileKernel_ChainRoutesThroughParentIface(t *testing.T) {
	fake := &fakeKernelOp{}
	o := newResumeOrch(t, fake, &storage.AWGTunnel{ID: "awg10", Name: "amst"})
	o.stateMgr = &fakeStateMgr{running: map[string]bool{}}
	if err := o.store.Create(&storage.AWGTunnel{ID: "awg11", Name: "riga", ISPInterface: "tunnel:awg10",
		Peer: storage.AWGPeer{Endpoint: "203.0.113.7:51820"}}); err != nil {
		t.Fatal(err)
	}
	if err := o.executeReconcileKernel(context.Background(), Action{Type: ActionReconcileKernel, Tunnel: "awg11"}); err != nil {
		t.Fatalf("старт ребёнка при лежащем родителе отказал: %v", err)
	}
	if fake.reconcileCfg.KernelDevice != "opkgtun10" || fake.reconcileCfg.ISPInterface != "tunnel:awg10" {
		t.Errorf("Reconcile получил %q/%q", fake.reconcileCfg.KernelDevice, fake.reconcileCfg.ISPInterface)
	}
}
