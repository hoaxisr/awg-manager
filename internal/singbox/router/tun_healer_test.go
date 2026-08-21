package router

import (
	"testing"
	"time"
)

// Движок жив, но стек отцепился от tun (carrier=0): дефолт клиентов уже
// припаркован на интерфейс, трафик уходит в никуда, а режим числится
// включённым. Единственное лечение — перезапуск движка; до healDetachedTun это
// состояние не ловил никто.
func TestHealDetachedTun_RestartsWhenCarrierDown(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = sb
	stubTunReadyProbe(t, func(string) bool { return false })

	svc.healDetachedTun("opkgtun0", "policy-tun-reconcile")
	if sb.reloadCalls != 1 {
		t.Fatalf("ожидался один перезапуск движка, получено %d", sb.reloadCalls)
	}
}

// Тик реконсиляции идёт часто: без зазора интерфейс, который не поднимается по
// другой причине, вогнал бы движок в цикл перезапусков.
func TestHealDetachedTun_ThrottlesRepeats(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = sb
	stubTunReadyProbe(t, func(string) bool { return false })

	svc.healDetachedTun("opkgtun0", "policy-tun-reconcile")
	svc.healDetachedTun("opkgtun0", "policy-tun-reconcile")
	if sb.reloadCalls != 1 {
		t.Fatalf("повтор в пределах зазора не должен перезапускать движок, вызовов %d", sb.reloadCalls)
	}

	svc.lastTunHealAt = time.Now().Add(-2 * healDetachedTunInterval)
	svc.healDetachedTun("opkgtun0", "policy-tun-reconcile")
	if sb.reloadCalls != 2 {
		t.Fatalf("после зазора лечение обязано повториться, вызовов %d", sb.reloadCalls)
	}
}

// Живой carrier — не наш случай: движок трогать нельзя.
func TestHealDetachedTun_NoopWhenCarrierUp(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = sb
	stubTunReadyProbe(t, func(string) bool { return true })

	svc.healDetachedTun("opkgtun0", "policy-tun-reconcile")
	if sb.reloadCalls != 0 {
		t.Fatalf("при живом carrier перезапуск не нужен, вызовов %d", sb.reloadCalls)
	}
}

// Мёртвый процесс — забота watchdog'а: healer не должен с ним конкурировать.
func TestHealDetachedTun_NoopWhenProcessDead(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return false, 0 }
	svc.deps.Singbox = sb
	stubTunReadyProbe(t, func(string) bool { return false })

	svc.healDetachedTun("opkgtun0", "policy-tun-reconcile")
	if sb.reloadCalls != 0 {
		t.Fatalf("мёртвый процесс поднимает watchdog, вызовов %d", sb.reloadCalls)
	}
}
