package router

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/heavyop"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// healerHarness — движок жив, слот активен, carrier управляется через carrier.
// Возвращает функцию одного такта реконсиляции: healDetachedTun считает такты,
// поэтому в тестах их и отсчитываем.
func healerHarness(t *testing.T) (svc *ServiceImpl, sb *fakeSingbox, carrier *bool, tick func(n int)) {
	t.Helper()
	svc, _ = newOrchedTestService(t)
	sb = newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = sb
	// Режим включён: healer перечитывает намерение перед самым перезапуском.
	all, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	all.SingboxRouter.Enabled = true
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
		t.Fatalf("SetEnabled SlotRouter: %v", err)
	}
	down := false
	carrier = &down
	stubTunReadyProbe(t, func(string) bool { return *carrier })
	tick = func(n int) {
		for i := 0; i < n; i++ {
			svc.healDetachedTun("opkgtun0", "policy-tun-reconcile", orchestrator.SlotRouter)
		}
	}
	return svc, sb, carrier, tick
}

// firstHealTick / lastHealTick — границы расписания попыток, чтобы тесты не
// зашивали конкретные числа.
var firstHealTick = healDetachedTunAttempts[0]
var lastHealTick = healDetachedTunAttempts[len(healDetachedTunAttempts)-1]

// Движок жив, но стек отцепился от tun (carrier=0): дефолт клиентов уже
// припаркован на интерфейс, трафик уходит в никуда, а режим числится
// включённым. Единственное лечение — перезапуск движка; до healDetachedTun это
// состояние не ловил никто.
func TestHealDetachedTun_RestartsWhenCarrierDown(t *testing.T) {
	_, sb, _, tick := healerHarness(t)

	tick(firstHealTick)
	if sb.reloadCalls != 1 {
		t.Fatalf("ожидался один перезапуск движка, получено %d", sb.reloadCalls)
	}
}

// Окно attach: после чужого рестарта (watchdog поднял упавший движок,
// оркестратор применил конфиг) процесс уже жив, а стек к устройству ещё не
// привязан. Первый такт — не улика: heal в этом окне дал бы ЛИШНИЙ Stop+Start.
func TestHealDetachedTun_FirstTickIsNotEvidence(t *testing.T) {
	_, sb, carrier, tick := healerHarness(t)

	tick(1)
	if sb.reloadCalls != 0 {
		t.Fatalf("одиночный нулевой carrier не должен лечиться, вызовов %d", sb.reloadCalls)
	}
	*carrier = true // привязка появилась сама
	tick(1)
	*carrier = false
	tick(1) // улика началась заново, порог ещё не набран
	if sb.reloadCalls != 0 {
		t.Fatalf("поднявшийся carrier обязан сбрасывать улику, вызовов %d", sb.reloadCalls)
	}
}

// Причина, которую перезапуск не лечит, не должна ронять соединения ВСЕХ
// слотов вечно: после последней попытки healer молчит, пока carrier не
// поднимется сам.
func TestHealDetachedTun_GivesUpAfterLastAttempt(t *testing.T) {
	_, sb, carrier, tick := healerHarness(t)

	tick(lastHealTick)
	if sb.reloadCalls != len(healDetachedTunAttempts) {
		t.Fatalf("ожидалось %d попыток, получено %d", len(healDetachedTunAttempts), sb.reloadCalls)
	}
	tick(lastHealTick * 4) // сколько бы тиков ни прошло — больше не лечим
	if sb.reloadCalls != len(healDetachedTunAttempts) {
		t.Fatalf("после исчерпания попыток движок трогать нельзя, вызовов %d", sb.reloadCalls)
	}

	// Carrier поднялся — счётчик обнулён, следующий РАЗРЫВ лечится с начала.
	*carrier = true
	tick(1)
	*carrier = false
	tick(firstHealTick)
	if sb.reloadCalls != len(healDetachedTunAttempts)+1 {
		t.Fatalf("после восстановления carrier лечение обязано начаться заново, вызовов %d", sb.reloadCalls)
	}
}

// Запаркованный слот — нулевой carrier закономерен (в merged-конфиге нет
// tun-инбаунда). Гейт обязан держаться КОДОМ: возврат слота, который идёт выше
// по тику, может и провалиться.
func TestHealDetachedTun_NoopWhenSlotParked(t *testing.T) {
	svc, sb, _, tick := healerHarness(t)
	if err := svc.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}

	tick(lastHealTick * 2)
	if sb.reloadCalls != 0 {
		t.Fatalf("при запаркованном слоте перезапуск ничего не чинит, вызовов %d", sb.reloadCalls)
	}
}

// Живой carrier — не наш случай: движок трогать нельзя.
func TestHealDetachedTun_NoopWhenCarrierUp(t *testing.T) {
	_, sb, carrier, tick := healerHarness(t)
	*carrier = true

	tick(lastHealTick * 2)
	if sb.reloadCalls != 0 {
		t.Fatalf("при живом carrier перезапуск не нужен, вызовов %d", sb.reloadCalls)
	}
}

// Мёртвый процесс — забота watchdog'а: healer не должен с ним конкурировать.
func TestHealDetachedTun_NoopWhenProcessDead(t *testing.T) {
	_, sb, _, tick := healerHarness(t)
	sb.isRunningFn = func() (bool, int) { return false, 0 }

	tick(lastHealTick * 2)
	if sb.reloadCalls != 0 {
		t.Fatalf("мёртвый процесс поднимает watchdog, вызовов %d", sb.reloadCalls)
	}
}

// Гейт памяти общий с оркестратором: Stop+Start параллельно с чужой валидацией
// на mipsel уходит в OOM. Занятый гейт — пропуск такта БЕЗ потери попытки.
func TestHealDetachedTun_SkipsWhenHeavyGateBusy(t *testing.T) {
	_, sb, _, tick := healerHarness(t)

	heavyop.Default.Lock()
	busyReleased := false
	t.Cleanup(func() {
		if !busyReleased {
			heavyop.Default.Unlock()
		}
	})
	tick(lastHealTick * 2)
	if sb.reloadCalls != 0 {
		t.Fatalf("при занятом гейте памяти движок трогать нельзя, вызовов %d", sb.reloadCalls)
	}
	heavyop.Default.Unlock()
	busyReleased = true

	tick(1)
	if sb.reloadCalls != 1 {
		t.Fatalf("попытка не должна сгорать из-за занятого гейта, вызовов %d", sb.reloadCalls)
	}
}

// Disable ходит мимо transitionMu, поэтому режим может выключиться, пока
// healer копит такты и берёт гейт памяти. Поднять движок в выключенном режиме
// хуже, чем пропустить такт: отменять воскрешение пришлось бы пользователю.
func TestHealDetachedTun_NoopWhenRouterDisabledMeanwhile(t *testing.T) {
	svc, sb, _, tick := healerHarness(t)

	// Набираем такты при включённом режиме, но до порога.
	tick(firstHealTick - 1)
	// Режим выключили — ровно та гонка, что открыта Disable мимо transitionMu.
	all, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	all.SingboxRouter.Enabled = false
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatal(err)
	}

	tick(lastHealTick * 2)
	if sb.reloadCalls != 0 {
		t.Errorf("в выключенном режиме движок трогать нельзя, вызовов %d", sb.reloadCalls)
	}
}
