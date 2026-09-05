package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = cp; return nil }); err != nil {
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

// 4) Отказ применения места хранения cache.db к 00-base.json не персистит
// настройку: иначе стор говорил бы «tmp», пока база на флеше (issue #842).
func TestUpdateSettings_CacheLocationApplyFailureSavesNothing(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	h.seedState(t, stateOff, false)
	h.svc.deps.ApplyCacheFileLocation = func(string) error { return errors.New("injected") }

	sr := storage.SingboxRouterSettings{WANAutoDetect: true, CacheFileLocation: "tmp"}
	if err := h.svc.UpdateSettings(ctx, sr); err == nil {
		t.Fatal("UpdateSettings: ожидалась ошибка применения")
	}
	saved, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.SingboxRouter.CacheFileLocation != "" {
		t.Fatalf("настройка сохранена при отказе применения: %q", saved.SingboxRouter.CacheFileLocation)
	}
}

// 5) Отказ ПЕРСИСТА после успешного применения места cache.db откатывает применение:
// шов вызывается второй раз с прежним значением. Отказ персиста — settings.json заменён
// непустым каталогом (AtomicWrite: rename tmp поверх каталога отказывает и под root).
func TestUpdateSettings_PersistFailureRollsBackCacheLocation(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	// Не stateOff: с "off" UpdateSettings отказывает в хвосте на invalid routingMode
	// (service_settings.go:208) — уже ПОСЛЕ персиста, и подготовка упала бы.
	h.seedState(t, stateTProxy, false)
	sr, err := h.svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sr.CacheFileLocation = "flash"
	if err := h.svc.UpdateSettings(ctx, sr); err != nil {
		t.Fatalf("подготовка прежнего значения: %v", err)
	}
	var applied []string
	h.svc.deps.ApplyCacheFileLocation = func(loc string) error { applied = append(applied, loc); return nil }

	path := filepath.Join(h.store.DataDir(), "settings.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "busy"), 0o755); err != nil {
		t.Fatal(err)
	}

	sr.CacheFileLocation = "tmp"
	if err := h.svc.UpdateSettings(ctx, sr); err == nil {
		t.Fatal("UpdateSettings: ожидался отказ персиста")
	}
	if want := []string{"tmp", "flash"}; !slices.Equal(applied, want) {
		t.Fatalf("вызовы шва = %v, ждали %v (применение, затем откат)", applied, want)
	}
}
