package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// 1) PUT с чужим режимом не переключает режим (красный до фикса:
// персист становится fakeip-tun, tproxy-ресурсы остаются жить).
func TestUpdateSettings_PreservesRoutingModeAndEnabled(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	// DeviceMode "all" — tproxy без политики (см. seedState).
	h.seedState(t, stateOff, false)
	if err := h.svc.SwitchRoutingMode(ctx, "tproxy"); err != nil {
		t.Fatalf("switch to tproxy: %v", err)
	}
	sr, err := h.svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	sr.RoutingMode = "fakeip-tun" // зловредный/устаревший клиент
	sr.Enabled = false
	_ = h.svc.UpdateSettings(ctx, sr) // ошибка Reconcile не важна — ассерт по персисту

	saved, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.SingboxRouter.RoutingMode != "tproxy" {
		t.Fatalf("RoutingMode сменился через PUT: %q", saved.SingboxRouter.RoutingMode)
	}
	if !saved.SingboxRouter.Enabled {
		t.Fatal("Enabled сброшен через PUT")
	}
}

// 2) Тело без mode (нормализация дефолтит в tproxy) не роняет
// персистированный fakeip-tun (красный до фикса: mode → tproxy,
// Enabled → false).
func TestUpdateSettings_EmptyModeKeepsPersisted(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	// Персист как после persistMode(fakeip-tun, true) — пишем стором напрямую.
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cp := *all
	cp.SingboxRouter.RoutingMode = "fakeip-tun"
	cp.SingboxRouter.Enabled = true
	if err := h.store.Save(&cp); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	_ = h.svc.UpdateSettings(ctx, storage.SingboxRouterSettings{WANAutoDetect: true})

	saved, _ := h.store.Load()
	if saved.SingboxRouter.RoutingMode != "fakeip-tun" || !saved.SingboxRouter.Enabled {
		t.Fatalf("персист затёрт: mode=%q enabled=%v",
			saved.SingboxRouter.RoutingMode, saved.SingboxRouter.Enabled)
	}
}

// 3) PUT во время живого перехода отвергается и ничего не сохраняет
// (красный до фикса: err == nil и настройки сохранены — Reconcile внутри
// UpdateSettings молча пропустил тик по TryLock, а Save уже прошёл).
func TestUpdateSettings_RejectedDuringTransition(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	sr, err := h.svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	sr.BypassExtraPorts = "8443 TCP"

	h.svc.transitionMu.Lock() // имитация SwitchRoutingMode в полёте
	err = h.svc.UpdateSettings(ctx, sr)
	h.svc.transitionMu.Unlock()

	if !errors.Is(err, ErrTransitionInProgress) {
		t.Fatalf("ожидали ErrTransitionInProgress, получили %v", err)
	}
	saved, _ := h.store.Load()
	if saved.SingboxRouter.BypassExtraPorts == "8443 TCP" {
		t.Fatal("настройки сохранены поверх живого перехода")
	}
}
