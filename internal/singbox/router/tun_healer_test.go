package router

import (
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/heavyop"
)

// tick — один такт реконсиляции для healDetachedTun. Порог strikes требует
// нескольких тактов, поэтому счёт вызовов ведётся тактами, а не вызовами.
func tick(svc *ServiceImpl, n int) {
	for i := 0; i < n; i++ {
		svc.healDetachedTun("opkgtun0", "policy-tun-reconcile")
	}
}

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

	tick(svc, healDetachedTunStrikes)
	if sb.reloadCalls != 1 {
		t.Fatalf("ожидался один перезапуск движка, получено %d", sb.reloadCalls)
	}
}

// Окно attach: после чужого рестарта (watchdog поднял упавший движок,
// оркестратор применил конфиг) процесс уже жив, а стек к устройству ещё не
// привязан. Одиночный нулевой carrier — не улика: heal в этом окне дал бы
// ЛИШНИЙ Stop+Start, ровно тот отказ, который мы чиним.
func TestHealDetachedTun_SingleStrikeIsNotEvidence(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = sb

	carrier := false
	stubTunReadyProbe(t, func(string) bool { return carrier })

	tick(svc, 1) // такт в окне attach
	if sb.reloadCalls != 0 {
		t.Fatalf("одиночный нулевой carrier не должен лечиться, вызовов %d", sb.reloadCalls)
	}
	carrier = true // привязка появилась сама
	tick(svc, 1)
	carrier = false
	tick(svc, 1) // улика началась заново, порог ещё не набран
	if sb.reloadCalls != 0 {
		t.Fatalf("поднявшийся carrier обязан сбрасывать улику, вызовов %d", sb.reloadCalls)
	}
}

// Причина, которую перезапуск не лечит, не должна ронять соединения ВСЕХ
// слотов раз в минуту вечно: зазор удваивается до потолка.
func TestHealDetachedTun_BackoffGrowsAndResets(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = sb

	carrier := false
	stubTunReadyProbe(t, func(string) bool { return carrier })

	tick(svc, healDetachedTunStrikes)
	if sb.reloadCalls != 1 || svc.tunHealBackoff != 2*healDetachedTunInterval {
		t.Fatalf("после первой попытки ждём зазор %v, получили %v (вызовов %d)",
			2*healDetachedTunInterval, svc.tunHealBackoff, sb.reloadCalls)
	}
	// Стартовый зазор истёк, но текущий (удвоенный) — ещё нет.
	svc.lastTunHealAt = time.Now().Add(-healDetachedTunInterval - time.Second)
	tick(svc, 1)
	if sb.reloadCalls != 1 {
		t.Fatalf("backoff обязан держать паузу дольше стартового зазора, вызовов %d", sb.reloadCalls)
	}
	svc.lastTunHealAt = time.Now().Add(-3 * healDetachedTunInterval)
	tick(svc, 1)
	if sb.reloadCalls != 2 || svc.tunHealBackoff != 4*healDetachedTunInterval {
		t.Fatalf("зазор обязан удваиваться, получили %v (вызовов %d)", svc.tunHealBackoff, sb.reloadCalls)
	}

	// Привязка восстановилась — следующий РАЗРЫВ лечится быстро, а не ждёт
	// хвост прошлого backoff'а.
	carrier = true
	tick(svc, 1)
	if svc.tunHealBackoff != 0 || svc.tunDownStrikes != 0 {
		t.Fatalf("поднявшийся carrier обязан сбрасывать backoff и улику: backoff=%v strikes=%d",
			svc.tunHealBackoff, svc.tunDownStrikes)
	}
}

// Потолок: зазор не растёт бесконечно, лечение остаётся включённым для причин,
// которые уходят сами.
func TestHealDetachedTun_BackoffCapped(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = sb
	stubTunReadyProbe(t, func(string) bool { return false })

	svc.tunHealBackoff = healDetachedTunMaxInterval
	tick(svc, healDetachedTunStrikes)
	if svc.tunHealBackoff != healDetachedTunMaxInterval {
		t.Fatalf("зазор обязан упираться в потолок %v, получили %v",
			healDetachedTunMaxInterval, svc.tunHealBackoff)
	}
}

// Гейт памяти общий с оркестратором и прямым применителем конфига: Stop+Start
// параллельно с чужой валидацией на mipsel уходит в OOM. Занятый гейт —
// пропуск такта БЕЗ потери улики: лечим следующим.
func TestHealDetachedTun_SkipsWhenHeavyGateBusy(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = sb
	stubTunReadyProbe(t, func(string) bool { return false })

	heavyop.Default.Lock()
	tick(svc, healDetachedTunStrikes)
	if sb.reloadCalls != 0 {
		t.Fatalf("при занятом гейте памяти движок трогать нельзя, вызовов %d", sb.reloadCalls)
	}
	heavyop.Default.Unlock()

	tick(svc, 1)
	if sb.reloadCalls != 1 {
		t.Fatalf("улика не должна теряться из-за занятого гейта, вызовов %d", sb.reloadCalls)
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

	tick(svc, healDetachedTunStrikes+1)
	if sb.reloadCalls != 1 {
		t.Fatalf("повтор в пределах зазора не должен перезапускать движок, вызовов %d", sb.reloadCalls)
	}

	svc.lastTunHealAt = time.Now().Add(-4 * healDetachedTunInterval)
	tick(svc, 1)
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

	tick(svc, healDetachedTunStrikes+1)
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

	tick(svc, healDetachedTunStrikes+1)
	if sb.reloadCalls != 0 {
		t.Fatalf("мёртвый процесс поднимает watchdog, вызовов %d", sb.reloadCalls)
	}
}
