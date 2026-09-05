package router

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// RT15: лечение отцепившегося tun обязано ехать из ТИКА реконсиляции.
//
// Сам healDetachedTun покрыт (tun_healer_test.go), но только прямыми
// вызовами — то есть покрыто поведение хелпера, а не то, что его кто-то
// зовёт. Удаление обеих строк проводки из reconcileFakeIPTun и
// reconcilePolicyTun проходило по пакету зелёным, а это ровно семейство
// «запущен, но не работает, чинит рестарт»: другого лечения у состояния
// «процесс жив, стек не привязан» нет.
//
// Тики отсчитываем по расписанию хелпера (firstHealTick), а не по числу 2 —
// расписание уже вынесено в переменную ради этого.
func TestReconcileFakeIPTun_TickDrivesTunHealer(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	sb := h.svc.deps.Singbox.(*fakeSingbox)
	sb.reloadCalls = 0
	// Стек отцепился: интерфейс на месте, движок жив, carrier нулевой.
	stubTunReadyProbe(t, func(string) bool { return false })

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	for i := 0; i < firstHealTick; i++ {
		if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
			t.Fatalf("тик %d: %v", i, err)
		}
	}
	if sb.reloadCalls != 1 {
		t.Fatalf("тик не позвал лечение tun: перезапусков %d, ждали 1", sb.reloadCalls)
	}
}

func TestReconcilePolicyTun_TickDrivesTunHealer(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	sb := h.svc.deps.Singbox.(*fakeSingbox)
	sb.reloadCalls = 0
	stubTunReadyProbe(t, func(string) bool { return false })
	if err := h.svc.deps.Orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	for i := 0; i < firstHealTick; i++ {
		if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
			t.Fatalf("тик %d: %v", i, err)
		}
	}
	if sb.reloadCalls != 1 {
		t.Fatalf("тик не позвал лечение tun: перезапусков %d, ждали 1", sb.reloadCalls)
	}
}
