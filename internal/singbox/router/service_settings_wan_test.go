package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

func routeWANOf(t *testing.T, path string) (autoDetect any, iface any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Route map[string]any `json:"route"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Route["auto_detect_interface"], doc.Route["default_interface"]
}

// F703: выбор WAN при включённом маршрутизаторе доезжает до 20-router.json
// сразу, а не после выключения/включения режима (EnsureRouteWAN звучал только
// на Enable). Стенд 21.10: PUT eth3 → в слоте по-прежнему auto_detect.
func TestUpdateSettings_WANAppliedToActiveRouterSlot(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	h.seedState(t, stateOff, false)
	if err := h.svc.SwitchRoutingMode(ctx, "tproxy"); err != nil {
		t.Fatalf("switch to tproxy: %v", err)
	}
	slot := filepath.Join(h.dir, "20-router.json")
	if ad, _ := routeWANOf(t, slot); ad != true {
		t.Fatalf("precondition: auto_detect_interface = %v, want true", ad)
	}

	sr, err := h.svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sr.WANAutoDetect, sr.WANInterface = false, "ppp0"
	_ = h.svc.UpdateSettings(ctx, sr) // ошибка Reconcile не важна — ассерт по слоту

	ad, iface := routeWANOf(t, slot)
	if ad != nil || iface != "ppp0" {
		t.Errorf("route WAN = auto_detect %v, default_interface %v; want pinned ppp0", ad, iface)
	}

	sr.WANAutoDetect, sr.WANInterface = true, ""
	_ = h.svc.UpdateSettings(ctx, sr)
	if ad, iface := routeWANOf(t, slot); ad != true || iface != nil {
		t.Errorf("route WAN back to auto = %v / %v", ad, iface)
	}
}

// Черновик — снимок слота на момент первой правки: применение устаревшего
// снимка вернуло бы прежний WAN мимо настроек. Выбор WAN — поле настроек, не
// пользовательское, и при ApplyStaging переносится из применённого слота.
func TestApplyStaging_KeepsRouteWANFromApplied(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	h.seedState(t, stateOff, false)
	if err := h.svc.SwitchRoutingMode(ctx, "tproxy"); err != nil {
		t.Fatalf("switch to tproxy: %v", err)
	}
	applied, err := h.svc.deps.Orch.LoadApplied(orchestrator.SlotRouter)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.deps.Orch.SaveDraft(orchestrator.SlotRouter, applied); err != nil {
		t.Fatal(err)
	}

	sr, err := h.svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sr.WANAutoDetect, sr.WANInterface = false, "ppp0"
	_ = h.svc.UpdateSettings(ctx, sr)

	if _, err := h.svc.ApplyStaging(ctx); err != nil {
		t.Fatalf("ApplyStaging: %v", err)
	}
	ad, iface := routeWANOf(t, filepath.Join(h.dir, "20-router.json"))
	if ad != nil || iface != "ppp0" {
		t.Errorf("after ApplyStaging route WAN = %v / %v; want pinned ppp0", ad, iface)
	}
}
