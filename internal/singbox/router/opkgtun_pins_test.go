package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func pinsOf(indices ...int) func(context.Context) (map[int]bool, error) {
	m := make(map[int]bool, len(indices))
	for _, i := range indices {
		m[i] = true
	}
	return func(context.Context) (map[int]bool, error) { return m, nil }
}

// Номер, занятый записью туннеля, которую ещё ни разу не включали, живым
// интерфейсом не выглядит: OpkgTun для kernel-туннеля создаётся только первым
// стартом. Аллокатор режимов роутера обязан такой номер пропускать — иначе он
// заберёт его себе, а включение туннеля усыновит чужой интерфейс.
func TestPolicyTunEnable_SkipsForeignPin(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true, 1: true, 2: true}}
	h.svc.deps.OpkgTunPins = pinsOf(3)

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if h.log.has("Create:OpkgTun3:public") {
		t.Errorf("номер 3 занят чужим пином и не должен выдаваться: %v", h.log.calls)
	}
	if !h.log.has("Create:OpkgTun4:public") {
		t.Errorf("ожидался следующий свободный номер 4, получено %v", h.log.calls)
	}
}

// Собственный удержанный индекс приходит из записи владения, а не из пинов:
// подмешать его в занятость значит перепинить себя и оборвать permit.
func TestPolicyTunEnable_OwnHoldSurvivesPins(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	h.svc.deps.OpkgTunPins = pinsOf(7)
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if st := h.loadPolicyTun(t); st == nil || st.Index != 3 {
		t.Errorf("удержанный свой индекс обязан пережить чужие пины, got %+v", st)
	}
}

// Недосчёт занятых номеров — единственное направление, дающее коллизию.
func TestPolicyTunEnable_FailsClosedOnPinsError(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	h.svc.deps.OpkgTunPins = func(context.Context) (map[int]bool, error) {
		return nil, errors.New("хранилище недоступно")
	}

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("сбой поставщика пинов обязан останавливать включение")
	}
}
