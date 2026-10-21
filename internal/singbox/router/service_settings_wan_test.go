package router

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
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
	updateSettingsKeepGoing(t, h.svc, sr)

	ad, iface := routeWANOf(t, slot)
	if ad != nil || iface != "ppp0" {
		t.Errorf("route WAN = auto_detect %v, default_interface %v; want pinned ppp0", ad, iface)
	}

	sr.WANAutoDetect, sr.WANInterface = true, ""
	updateSettingsKeepGoing(t, h.svc, sr)
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
	updateSettingsKeepGoing(t, h.svc, sr)

	if _, err := h.svc.ApplyStaging(ctx); err != nil {
		t.Fatalf("ApplyStaging: %v", err)
	}
	ad, iface := routeWANOf(t, filepath.Join(h.dir, "20-router.json"))
	if ad != nil || iface != "ppp0" {
		t.Errorf("after ApplyStaging route WAN = %v / %v; want pinned ppp0", ad, iface)
	}
}

func hasSystemSniff(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, r := range doc.Route.Rules {
		if r["action"] == "sniff" && len(r) == 1 {
			return true
		}
	}
	return false
}

// F704: переключатель сниффера при включённом маршрутизаторе доезжает до
// 20-router.json сразу (EnsureSystemRules звучал только на Enable).
// Стенд 21.10: PUT snifferEnabled:false → правило sniff осталось.
func TestUpdateSettings_SnifferAppliedToActiveRouterSlot(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	h.seedState(t, stateOff, false)
	if err := h.svc.SwitchRoutingMode(ctx, "tproxy"); err != nil {
		t.Fatalf("switch to tproxy: %v", err)
	}
	slot := filepath.Join(h.dir, "20-router.json")
	sr, err := h.svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sr.SnifferEnabled = true
	updateSettingsKeepGoing(t, h.svc, sr)
	if !hasSystemSniff(t, slot) {
		t.Fatal("sniff rule not added by snifferEnabled=true")
	}

	sr.SnifferEnabled = false
	updateSettingsKeepGoing(t, h.svc, sr)
	if hasSystemSniff(t, slot) {
		t.Error("sniff rule survived snifferEnabled=false")
	}
	sr.SnifferEnabled = true
	updateSettingsKeepGoing(t, h.svc, sr)
	if !hasSystemSniff(t, slot) {
		t.Error("sniff rule not restored by snifferEnabled=true")
	}
}

// Черновик, снятый при включённом сниффере, при применении не возвращает
// sniff после выключения сниффера в настройках.
func TestApplyStaging_KeepsSnifferFromSettings(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	h.seedState(t, stateOff, false)
	if err := h.svc.SwitchRoutingMode(ctx, "tproxy"); err != nil {
		t.Fatalf("switch to tproxy: %v", err)
	}
	sr, err := h.svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sr.SnifferEnabled = true
	updateSettingsKeepGoing(t, h.svc, sr)
	applied, err := h.svc.deps.Orch.LoadApplied(orchestrator.SlotRouter)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.deps.Orch.SaveDraft(orchestrator.SlotRouter, applied); err != nil {
		t.Fatal(err)
	}
	sr.SnifferEnabled = false
	updateSettingsKeepGoing(t, h.svc, sr)

	if _, err := h.svc.ApplyStaging(ctx); err != nil {
		t.Fatalf("ApplyStaging: %v", err)
	}
	if hasSystemSniff(t, filepath.Join(h.dir, "20-router.json")) {
		t.Error("stale draft brought sniff back")
	}
}

// ApplyStaging пишет тот же 20-router.json, что UpdateSettings
// (reapplyRouterSettings) и смена режима: без общего transitionMu перенос
// WAN/сниффера в черновик и запись применённого слота гонялись бы.
func TestApplyStaging_RejectedDuringTransition(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateOff, false)
	h.svc.transitionMu.Lock()
	defer h.svc.transitionMu.Unlock()
	if _, err := h.svc.ApplyStaging(context.Background()); !errors.Is(err, ErrTransitionInProgress) {
		t.Fatalf("ApplyStaging under transitionMu: err = %v, want ErrTransitionInProgress", err)
	}
}

// Долечивание: выбор WAN, сохранённый до версии с мгновенным применением
// (или в обход UpdateSettings), доезжает до слота на проходе Reconcile — без
// пересохранения настроек пользователем.
func TestReconcile_HealsStoredWANIntoRouterSlot(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	h.seedState(t, stateOff, false)
	if err := h.svc.SwitchRoutingMode(ctx, "tproxy"); err != nil {
		t.Fatalf("switch to tproxy: %v", err)
	}
	if err := h.svc.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.WANAutoDetect, cur.SingboxRouter.WANInterface = false, "ppp0"
		cur.SingboxRouter.RoutingMode = "" // legacy-запись: пусто = tproxy
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = h.svc.Reconcile(ctx)
	if ad, iface := routeWANOf(t, filepath.Join(h.dir, "20-router.json")); ad != nil || iface != "ppp0" {
		t.Errorf("after Reconcile route WAN = %v / %v; want pinned ppp0", ad, iface)
	}
}

// updateSettingsKeepGoing — ошибку Reconcile обвязки игнорируем (ассерт по
// слоту), но не отказ транзакцией: с ним ассерт падал бы с невнятной причиной.
func updateSettingsKeepGoing(t *testing.T, s *ServiceImpl, sr storage.SingboxRouterSettings) {
	t.Helper()
	if err := s.UpdateSettings(context.Background(), sr); errors.Is(err, ErrTransitionInProgress) {
		t.Fatalf("UpdateSettings: %v", err)
	}
}
