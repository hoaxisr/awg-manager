package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// После успешного ApplyStaging роутер обязан примирить dns.strategy базового
// слота: без этого выбор пользователя затеняется 00-base.json до перезапуска
// демона (мерж скаляров first-file-wins).
func TestApplyStaging_CallsReconcileBaseDNSStrategy(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	calls := 0
	svc.deps.ReconcileBaseOwnedScalars = func() error { calls++; return nil }

	_ = svc.deps.Orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)
	cfg := NewEmptyConfig()
	cfg.Route.Final = "direct"
	if err := svc.persistConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ApplyStaging(context.Background())
	if err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging: err=%v res=%s", err, res.Error())
	}
	if calls != 1 {
		t.Fatalf("ReconcileBaseDNSStrategy вызовов = %d, want 1", calls)
	}
}

// Неудачное применение не должно трогать base: слот не изменился, примирять
// нечего.
func TestApplyStaging_NoReconcileWhenApplyFails(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	calls := 0
	svc.deps.ReconcileBaseOwnedScalars = func() error { calls++; return nil }

	// Черновик ссылается на неизвестный outbound → ApplyDraft не Ok.
	cfg := NewEmptyConfig()
	cfg.Route.Final = "no-such-outbound"
	if err := svc.persistConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ApplyStaging(context.Background())
	if err == nil && res.Ok() {
		t.Fatal("предусловие: ApplyStaging обязан провалиться на неизвестном outbound'е")
	}
	if calls != 0 {
		t.Fatalf("на провале примирение звать нельзя, вызовов = %d", calls)
	}
}

// Ошибка примирения — best-effort: успешное применение она не валит.
func TestApplyStaging_ReconcileErrorDoesNotFailApply(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.ReconcileBaseOwnedScalars = func() error { return errors.New("boom") }

	_ = svc.deps.Orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)
	cfg := NewEmptyConfig()
	cfg.Route.Final = "direct"
	if err := svc.persistConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ApplyStaging(context.Background())
	if err != nil || !res.Ok() {
		t.Fatalf("ошибка примирения не должна валить ApplyStaging: err=%v res=%s", err, res.Error())
	}
}

// nil-dep (тесты, старая обвязка) не паникует.
func TestApplyStaging_NilReconcileDepDoesNotPanic(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	_ = svc.deps.Orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)
	cfg := NewEmptyConfig()
	cfg.Route.Final = "direct"
	if err := svc.persistConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if res, err := svc.ApplyStaging(context.Background()); err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging: err=%v res=%s", err, res.Error())
	}
}

// Fakeip-путь пишет слот 21 напрямую, мимо staging — примирение обязано
// звучать и здесь (до фикса затенение в fakeip-режиме было вечным).
func TestFakeIPSetDNSGlobals_CallsReconcileBaseDNSStrategy(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	seedFakeIPLocked(t, svc) // overlay кладёт "real"-сервер, без него SetDNSGlobals не примет final
	calls := 0
	svc.deps.ReconcileBaseOwnedScalars = func() error { calls++; return nil }

	if err := svc.FakeIPSetDNSGlobals(context.Background(), "real", "ipv4_only"); err != nil {
		t.Fatalf("FakeIPSetDNSGlobals: %v", err)
	}
	if calls != 1 {
		t.Fatalf("ReconcileBaseDNSStrategy вызовов = %d, want 1", calls)
	}
}
