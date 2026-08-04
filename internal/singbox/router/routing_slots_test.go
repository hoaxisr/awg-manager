package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakeOrch — тонкая обёртка над НАСТОЯЩИМ оркестратором (тем же двойником, что
// используют соседние тесты пакета): добавляет чтение флага слота по имени и
// проверку, в каком каталоге лежит файл слота.
type fakeOrch struct {
	*orchestrator.Orchestrator
	dir       string
	filenames map[orchestrator.Slot]string
}

// newFakeOrch поднимает оркестратор в t.TempDir() со всем каталогом слотов —
// applyRoutingSlots трогает четыре из них, а незарегистрированный слот
// оркестратор отвергает. Файлы слотов маршрутизации кладутся на диск: без них
// SetEnabled ограничился бы правкой карты enabled, и перенос active/ ↔
// disabled/ (то, ради чего слоты и переключают) тесты бы не задели.
func newFakeOrch(t *testing.T) *fakeOrch {
	t.Helper()
	dir := t.TempDir()
	o := orchestrator.New(dir, nil)
	filenames := make(map[orchestrator.Slot]string)
	for _, meta := range orchestrator.KnownSlots() {
		if err := o.Register(meta); err != nil {
			t.Fatalf("register %s: %v", meta.Slot, err)
		}
		filenames[meta.Slot] = meta.Filename
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	f := &fakeOrch{Orchestrator: o, dir: dir, filenames: filenames}
	for _, slot := range append(modeSlots(), orchestrator.SlotRouting) {
		if err := o.SaveSilent(slot, []byte(`{"outbounds":[]}`)); err != nil {
			t.Fatalf("seed %s: %v", slot, err)
		}
	}
	return f
}

// fileActive: файл слота лежит в config.d/ (виден sing-box), а не в disabled/.
func (f *fakeOrch) fileActive(t *testing.T, slot orchestrator.Slot) bool {
	t.Helper()
	name := f.filenames[slot]
	if name == "" {
		t.Fatalf("нет имени файла для слота %s", slot)
	}
	active := fileExistsForTest(filepath.Join(f.dir, name))
	parked := fileExistsForTest(filepath.Join(f.dir, "disabled", name))
	if active == parked {
		t.Fatalf("слот %s: файл должен лежать ровно в одном каталоге (active=%v, disabled=%v)", slot, active, parked)
	}
	return active
}

func fileExistsForTest(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
			// Флаг без переноса файла — половина работы: sing-box читает
			// каталог, а не карту в памяти.
			if got := orch.fileActive(t, s); got != (s == tc.want) {
				t.Errorf("%s: файл слота %s active=%v, ожидалось %v", tc.mode, s, got, s == tc.want)
			}
		}
		if !orch.enabled(orchestrator.SlotRouting) {
			t.Errorf("%s: общий слот обязан быть включён вместе с режимным", tc.mode)
		}
		if !orch.fileActive(t, orchestrator.SlotRouting) {
			t.Errorf("%s: файл общего слота обязан лежать в config.d/", tc.mode)
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
		if orch.fileActive(t, s) {
			t.Errorf("файл слота %s остался в config.d/ после выключения движка", s)
		}
	}
}

// failingToggler гасит/зажигает слоты, но на заданном слоте всегда падает.
type failingToggler struct {
	failOn orchestrator.Slot
	seen   map[orchestrator.Slot]bool
}

func (f *failingToggler) SetEnabled(slot orchestrator.Slot, enabled bool) error {
	if f.seen == nil {
		f.seen = map[orchestrator.Slot]bool{}
	}
	if slot == f.failOn {
		return errors.New("инъекция: SetEnabled упал")
	}
	f.seen[slot] = enabled
	return nil
}

// Выключение движка — best-effort: сбой на ОДНОМ слоте не должен оставлять
// остальные нетронутыми. Обрыв по первой ошибке был опаснее всего именно
// здесь: вызывающие пути выключения только логируют warning и идут сносить
// tun/OpkgTun, так что уцелевший режимный слот оставил бы в конфиге инбаунд на
// удалённый интерфейс.
func TestApplyRoutingSlotsDisableBestEffort(t *testing.T) {
	all := append(modeSlots(), orchestrator.SlotRouting)
	// Падение проверяется на КАЖДОМ слоте: иначе тест был бы завязан на порядок
	// обхода и пропустил бы обрыв, случившийся на последнем из слотов.
	for _, failOn := range all {
		tg := &failingToggler{failOn: failOn}

		if err := applyRoutingSlots(tg, "policy-tun", false); err == nil {
			t.Fatalf("сбой на %s обязан вернуться ошибкой", failOn)
		}
		for _, slot := range all {
			if slot == failOn {
				continue
			}
			on, tried := tg.seen[slot]
			if !tried {
				t.Errorf("сбой на %s: слот %s не получил попытки переключения", failOn, slot)
				continue
			}
			if on {
				t.Errorf("сбой на %s: слот %s остался включённым", failOn, slot)
			}
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

// Интеграционный ассерт на tproxy-пути enableLocked: включение движка в режиме
// tproxy обязано промотировать ИМЕННО SlotTProxy. Без него ошибка в имени
// режима молчит — ModeSlot на неизвестном значении отдаёт SlotTProxy, и
// подмена константы (например на fakeip-tun) зажгла бы чужой слот незаметно.
func TestEnableTProxy_PromotesTProxySlot(t *testing.T) {
	svc, dir := newQoSSlotTestService(t, "vpn")
	ensureDisabledDir(t, dir)
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotRouting, false); err != nil {
		t.Fatalf("park routing slot: %v", err)
	}
	svc.deps.Settings = newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
	})
	svc.deps.Singbox = &fakeSingbox{dir: dir, isRunningFn: func() (bool, int) { return true, 1234 }}
	stubListeningProbe(t, func() bool { return true })
	svc.deps.Policies = &fakeAccessPolicyProvider{}
	svc.deps.IPTables = newStubIPTables(func(_ context.Context, _ string) error { return nil })
	svc.deps.WANIPCollector = &fakeWANIPCollector{}
	svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	svc.deps.XtDscpProbe = func(context.Context) bool { return true }

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	if !slotEnabled(t, svc, orchestrator.SlotTProxy) {
		t.Error("SlotTProxy обязан быть включён после Enable в режиме tproxy")
	}
	if !slotEnabled(t, svc, orchestrator.SlotRouting) {
		t.Error("SlotRouting обязан быть включён после Enable в режиме tproxy")
	}
	for _, other := range []orchestrator.Slot{orchestrator.SlotFakeIP, orchestrator.SlotPolicyTun} {
		if slotEnabled(t, svc, other) {
			t.Errorf("слот %s обязан быть выключен в режиме tproxy", other)
		}
	}
}
