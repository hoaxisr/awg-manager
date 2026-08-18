package router

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Зомби-путь: hold policy-tun (запись без Provisioned, интерфейс жив) +
// включение fakeip. Раньше fakeip enable чужой персист игнорировал — запись
// policy-tun продолжала указывать на живой интерфейс до реапа. С единой
// записью enable обязан СНАЧАЛА освободить чужое владение (restore NAT →
// teardown), затем перезаписать запись. После enable записи режима policy-tun
// не существует — по построению (одна запись).
func TestFakeIPEnable_ReleasesForeignPolicyTunOwnership(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{2: true}}
	natState := &fakeNATState{static: []query.StaticNATEntry{{Interface: "Guest", ToInterface: "OpkgTun2"}}}
	h.svc.deps.NATState = natState
	h.svc.deps.SegmentNAT = &recSegmentNAT{log: h.log, state: natState}
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode:      storage.OpkgTunModePolicyTun,
		Index:     2,
		PolicyTun: &storage.OpkgTunPolicyData{NATSegments: []storage.PolicyTunNATSegment{{Name: "Guest", PriorMode: "dynamic"}}},
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(fakeip): %v", err)
	}

	if !h.log.has("Delete:OpkgTun2") {
		t.Errorf("чужое владение обязано быть освобождено (teardown OpkgTun2): %v", h.log.calls)
	}
	if !h.log.has("SetSegmentNAT:Guest") {
		t.Errorf("записанный NAT сегмента обязан восстанавливаться: %v", h.log.calls)
	}
	all, err := h.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if all.OpkgTun == nil || all.OpkgTun.Mode != storage.OpkgTunModeFakeIP {
		t.Fatalf("итоговая запись = %+v, want Mode=fakeip-tun", all.OpkgTun)
	}
}

// «Одно чтение одной записи»: хелпер отвечает только за свой режим, hold
// (Provisioned=false) — валидное владение policy-tun (Р3).
func TestOpkgTunOwned_SingleRead(t *testing.T) {
	mk := func(st *storage.OpkgTunState) *storage.Settings {
		return &storage.Settings{OpkgTun: st}
	}
	if _, ok := opkgTunOwned(mk(nil), stateFakeIPTun); ok {
		t.Fatal("nil record must own nothing")
	}
	fk := &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 1}
	if st, ok := opkgTunOwned(mk(fk), stateFakeIPTun); !ok || st.Index != 1 {
		t.Fatal("fakeip record must answer for fakeip")
	}
	if _, ok := opkgTunOwned(mk(fk), statePolicyTun); ok {
		t.Fatal("fakeip record must not answer for policy-tun")
	}
	hold := &storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 2}
	if st, ok := opkgTunOwned(mk(hold), statePolicyTun); !ok || st.Provisioned {
		t.Fatalf("hold must be owned by policy-tun: %+v", st)
	}
}

// Р2: fakeip уже был провижинен (запись есть, интерфейс умер), re-provision
// упал после персиста нового индекса → откат обязан вернуть ПРЕЖНЮЮ запись, а
// не nil. С nil протухший ресурс прежнего провижининга терял персист — реапу
// оставался только description-скан, а детектор сброса fakeip-кэша терял
// prev-диапазоны.
func TestFakeIPEnable_RollbackRestoresPreviousRecord(t *testing.T) {
	h := newFakeIPEnableHarness(t, "SetAddress")
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 1,
		FakeIP: &storage.OpkgTunFakeIPData{Inet4Range: "198.18.0.0/15"},
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("Enable must fail (injected SetAddress)")
	}

	st := h.loadFakeIP(t)
	if st == nil || !st.Provisioned || st.Index != 1 ||
		st.FakeIP == nil || st.FakeIP.Inet4Range != "198.18.0.0/15" {
		t.Fatalf("запись после отката = %+v, want прежняя {fakeip, provisioned, index 1}", st)
	}
}

// СТРАХОВКА (зелёный до и после): первый enable (записи не было) откатывается
// в nil — прежнее поведение, частный случай restore-prev.
func TestFakeIPEnable_RollbackClearsRecordOnFirstEnable(t *testing.T) {
	h := newFakeIPEnableHarness(t, "SetAddress")

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("Enable must fail (injected SetAddress)")
	}

	if st := h.loadFakeIP(t); st != nil {
		t.Fatalf("запись после отката = %+v, want nil", st)
	}
}
