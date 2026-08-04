package router

import (
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// fakeOrch — тонкая обёртка над НАСТОЯЩИМ оркестратором (тем же двойником, что
// используют соседние тесты пакета): добавляет чтение флага слота по имени.
type fakeOrch struct {
	*orchestrator.Orchestrator
}

// newFakeOrch поднимает оркестратор в t.TempDir() со всем каталогом слотов —
// applyRoutingSlots трогает четыре из них, а незарегистрированный слот
// оркестратор отвергает.
func newFakeOrch(t *testing.T) *fakeOrch {
	t.Helper()
	o := orchestrator.New(t.TempDir(), nil)
	for _, meta := range orchestrator.KnownSlots() {
		if err := o.Register(meta); err != nil {
			t.Fatalf("register %s: %v", meta.Slot, err)
		}
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return &fakeOrch{o}
}

// registerExtraModeSlots дорегистрирует режимные слоты, которых нет в
// харнессах, собранных до появления трёх режимов: applyRoutingSlots трогает ВСЕ
// режимные слоты, а незарегистрированный оркестратор отвергает. Слоты, которые
// харнесс уже зарегистрировал сам (со своим именем файла), пропускаются.
func registerExtraModeSlots(t *testing.T, orch *orchestrator.Orchestrator) {
	t.Helper()
	for _, meta := range orchestrator.KnownSlots() {
		if _, ok := modeBySlot[meta.Slot]; !ok {
			continue
		}
		if err := orch.Register(meta); err != nil && !errors.Is(err, orchestrator.ErrSlotAlreadyRegistered) {
			t.Fatalf("register %s: %v", meta.Slot, err)
		}
	}
}

func (f *fakeOrch) enabled(slot orchestrator.Slot) bool {
	for _, st := range f.Snapshot() {
		if st.Slot == slot {
			return st.Enabled
		}
	}
	return false
}

// ModeSlot — единственный источник соответствия «режим → слот захвата».
// Значения режимов закрыты NormalizeSingboxRouterSettings; всё неизвестное
// (включая пустую строку) нормализуется в tproxy.
func TestModeSlot(t *testing.T) {
	cases := []struct {
		mode string
		want orchestrator.Slot
	}{
		{"tproxy", orchestrator.SlotTProxy},
		{"fakeip-tun", orchestrator.SlotFakeIP},
		{"policy-tun", orchestrator.SlotPolicyTun},
		{"", orchestrator.SlotTProxy},
		{"нет-такого-режима", orchestrator.SlotTProxy},
	}
	for _, tc := range cases {
		if got := ModeSlot(tc.mode); got != tc.want {
			t.Errorf("ModeSlot(%q) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

// Переключение режима обязано оставить включённым РОВНО один режимный слот
// плюс общий. Лишний включённый режимный слот означает два inbound'а и два
// hijack-dns в merged-конфиге — sing-box падает или молча ловит трафик не тем
// перехватчиком.
func TestApplyRoutingSlotsExclusive(t *testing.T) {
	cases := []struct {
		mode string
		want orchestrator.Slot
	}{
		{"tproxy", orchestrator.SlotTProxy},
		{"policy-tun", orchestrator.SlotPolicyTun},
		{"fakeip-tun", orchestrator.SlotFakeIP},
	}
	all := []orchestrator.Slot{
		orchestrator.SlotTProxy, orchestrator.SlotPolicyTun, orchestrator.SlotFakeIP,
	}

	for _, tc := range cases {
		orch := newFakeOrch(t)
		if err := applyRoutingSlots(orch, tc.mode, true); err != nil {
			t.Fatalf("%s: %v", tc.mode, err)
		}
		for _, s := range all {
			if got := orch.enabled(s); got != (s == tc.want) {
				t.Errorf("%s: слот %s enabled=%v, ожидалось %v", tc.mode, s, got, s == tc.want)
			}
		}
		if !orch.enabled(orchestrator.SlotRouting) {
			t.Errorf("%s: общий слот обязан быть включён вместе с режимным", tc.mode)
		}
	}
}

// Выключение движка гасит и режимный, и общий слот — файлы уезжают в disabled/,
// содержимое не теряется.
func TestApplyRoutingSlotsDisableAll(t *testing.T) {
	orch := newFakeOrch(t)
	if err := applyRoutingSlots(orch, "fakeip-tun", true); err != nil {
		t.Fatal(err)
	}
	if err := applyRoutingSlots(orch, "fakeip-tun", false); err != nil {
		t.Fatal(err)
	}
	for _, s := range []orchestrator.Slot{
		orchestrator.SlotTProxy, orchestrator.SlotPolicyTun,
		orchestrator.SlotFakeIP, orchestrator.SlotRouting,
	} {
		if orch.enabled(s) {
			t.Errorf("слот %s остался включённым после выключения движка", s)
		}
	}
}

// Смена режима на живом движке: прежний режимный слот обязан погаснуть, а не
// остаться вторым перехватчиком рядом с новым.
func TestApplyRoutingSlotsSwitchMode(t *testing.T) {
	orch := newFakeOrch(t)
	if err := applyRoutingSlots(orch, "tproxy", true); err != nil {
		t.Fatal(err)
	}
	if err := applyRoutingSlots(orch, "policy-tun", true); err != nil {
		t.Fatal(err)
	}
	if orch.enabled(orchestrator.SlotTProxy) {
		t.Error("слот tproxy остался включённым после перехода в policy-tun")
	}
	if !orch.enabled(orchestrator.SlotPolicyTun) {
		t.Error("слот policytun обязан быть включён после перехода в policy-tun")
	}
	if !orch.enabled(orchestrator.SlotRouting) {
		t.Error("общий слот обязан пережить смену режима включённым")
	}
}
