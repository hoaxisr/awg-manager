package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// These tests cover the direct-save path that replaced SaveDraft for
// system-driven config writes (Enable / Disable legacy / healTProxyInbound).
// The bug they regress against: on every router reboot a `pending/20-router.json`
// appeared because the boot-time Reconcile→Enable cycle staged its
// idempotently-regenerated config as if it were a user edit, leaving the
// UI banner "Несохранённые правки" stuck until the user clicked Apply.
//
// The fix splits persistConfig (still staged) from persistConfigDirect
// (direct write to active, with byte-equal short-circuit). Boot recovery
// goes through persistConfigDirect → no pending → no banner.

func TestPersistConfigDirect_NoOpWhenActiveMatches(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	// Active file pre-exists with what marshalling NewEmptyConfig would
	// produce — Bootstrap below sees it and marks the slot enabled.
	cfg := NewEmptyConfig()
	bytesNow, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	activePath := filepath.Join(dir, "21-routing.json")
	if err := os.WriteFile(activePath, bytesNow, 0644); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	// Re-bootstrap so the orchestrator picks up the active file.
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Capture mtime to verify atomic rewrite did NOT happen.
	before, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat active: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // separate possible mtime windows

	if err := svc.persistConfigDirect(context.Background(), cfg); err != nil {
		t.Fatalf("persistConfigDirect: %v", err)
	}

	after, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat active after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("active should not be re-written when bytes match (before=%v after=%v)", before.ModTime(), after.ModTime())
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "21-routing.json")); !os.IsNotExist(err) {
		t.Errorf("pending must not exist after byte-equal direct save: %v", err)
	}
}

func TestPersistConfigDirect_WritesActiveWhenDifferent(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	// Seed active with stale bytes (different from what marshalling our
	// cfg below will produce). Bootstrap marks the slot enabled.
	activePath := filepath.Join(dir, "21-routing.json")
	if err := os.WriteFile(activePath, []byte(`{"stale": true}`), 0644); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	cfg := NewEmptyConfig()
	cfg.Route.Rules = append(cfg.Route.Rules, Rule{Action: "route", Outbound: "direct"})

	if err := svc.persistConfigDirect(context.Background(), cfg); err != nil {
		t.Fatalf("persistConfigDirect: %v", err)
	}

	got, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	want, _ := json.MarshalIndent(cfg, "", "  ")
	if string(got) != string(want) {
		t.Errorf("active not overwritten with new bytes\nwant: %s\ngot:  %s", want, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "21-routing.json")); !os.IsNotExist(err) {
		t.Errorf("pending must not exist after direct save: %v", err)
	}
}

func TestPersistConfigDirect_WritesActiveWhenAbsent(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	// No active file. Bootstrap sees nothing → enabled=false; explicit
	// SetEnabled flips it to true so orch.Save targets activePath.
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := svc.deps.Orch.SetEnabled(orchestrator.SlotRouting, true); err != nil {
		t.Fatalf("SetEnabled true: %v", err)
	}

	cfg := NewEmptyConfig()
	cfg.Route.Rules = append(cfg.Route.Rules, Rule{Action: "route", Outbound: "direct"})

	if err := svc.persistConfigDirect(context.Background(), cfg); err != nil {
		t.Fatalf("persistConfigDirect: %v", err)
	}

	activePath := filepath.Join(dir, "21-routing.json")
	got, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	want, _ := json.MarshalIndent(cfg, "", "  ")
	if string(got) != string(want) {
		t.Errorf("active not created with expected bytes\nwant: %s\ngot:  %s", want, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "21-routing.json")); !os.IsNotExist(err) {
		t.Errorf("pending must not exist after direct save: %v", err)
	}
}

// Regression: changing the UDP timeout on a running engine goes through
// Reconcile→reconcileInstalled→syncModeSlot. The old heal returned early
// whenever a tproxy-in inbound was present, so a changed udpTimeout was never
// written to the config (UI showed "1 час" while the file kept "3m0s").
// #554: the system route-options rule must be brought to spec by the SAME
// heal — it used to be regenerated only by Enable, so a changed timeout
// stayed stale in the rule until the engine was toggled off/on.
//
// Носители таймаута уехали в режимный слот (20-tproxy.json) вместе со всем
// захватом трафика — лечится теперь он.
func TestSyncModeSlot_AppliesChangedUDPTimeout(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	modePath := seedTProxySlot(t, svc, dir, buildTProxySlot(TProxyParams{}))

	if _, err := svc.syncModeSlot(tproxySettings("1h0m0s")); err != nil {
		t.Fatalf("syncModeSlot: %v", err)
	}

	got := readRouterConfigFile(t, modePath)
	var found bool
	for _, in := range got.Inbounds {
		if in.Tag == "tproxy-in" {
			found = true
			if in.UDPTimeout != "1h0m0s" {
				t.Errorf("inbound udp_timeout not applied: want 1h0m0s, got %q", in.UDPTimeout)
			}
		}
	}
	if !found {
		t.Fatal("tproxy-in inbound missing after heal")
	}
	var ruleFound bool
	for _, r := range got.Route.Rules {
		if isSystemUDPTimeoutRule(r) {
			ruleFound = true
			if r.UDPTimeout != "1h0m0s" {
				t.Errorf("route-options udp_timeout not applied (#554): want 1h0m0s, got %q", r.UDPTimeout)
			}
		}
	}
	if !ruleFound {
		t.Fatal("system route-options rule missing after heal")
	}
}

// Самолечение пишет РЕЖИМНЫЙ слот и не трогает ни активный общий конфиг, ни
// пользовательский черновик над ним: черновик — про правила и наборы, а
// материализация его в active мимо ApplyDraft ровно тот баг, от которого
// защищался прежний тест.
func TestSyncModeSlot_LeavesRoutingDraftAlone(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	seedTProxySlot(t, svc, dir, buildTProxySlot(TProxyParams{}))

	active := NewEmptyConfig()
	activePath := filepath.Join(dir, "21-routing.json")
	activeBytes, _ := json.MarshalIndent(active, "", "  ")
	if err := os.WriteFile(activePath, activeBytes, 0644); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	draft := NewEmptyConfig()
	draft.Route.Rules = append(draft.Route.Rules, Rule{Action: "route", Outbound: "draft-marker", Domain: []string{"draft.example"}})
	draftBytes, _ := json.MarshalIndent(draft, "", "  ")
	if err := os.MkdirAll(filepath.Join(dir, "pending"), 0755); err != nil {
		t.Fatalf("mkdir pending: %v", err)
	}
	pendingPath := filepath.Join(dir, "pending", "21-routing.json")
	if err := os.WriteFile(pendingPath, draftBytes, 0644); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if _, err := svc.syncModeSlot(tproxySettings("1h0m0s")); err != nil {
		t.Fatalf("syncModeSlot: %v", err)
	}

	raw, _ := os.ReadFile(activePath)
	if string(raw) != string(activeBytes) {
		t.Fatalf("общий слот переписан самолечением режимного:\n%s", raw)
	}
	pendingRaw, err := os.ReadFile(pendingPath)
	if err != nil || !strings.Contains(string(pendingRaw), "draft-marker") {
		t.Errorf("pending draft must survive heal untouched (err=%v)", err)
	}
}

// #554: the rule can drift alone (the inbound is already at spec — e.g. a
// pre-fix build applied the inbound but never the rule). The heal must still
// rewrite the config.
func TestSyncModeSlot_HealsRuleWhenOnlyRuleDrifted(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	drifted := buildTProxySlot(TProxyParams{UDPTimeout: "1h0m0s"})
	// Rule deliberately absent — the drifted-carrier case.
	kept := drifted.Route.Rules[:0]
	for _, r := range drifted.Route.Rules {
		if !isSystemUDPTimeoutRule(r) {
			kept = append(kept, r)
		}
	}
	drifted.Route.Rules = kept
	modePath := seedTProxySlot(t, svc, dir, drifted)

	if _, err := svc.syncModeSlot(tproxySettings("1h0m0s")); err != nil {
		t.Fatalf("syncModeSlot: %v", err)
	}

	got := readRouterConfigFile(t, modePath)
	for _, r := range got.Route.Rules {
		if isSystemUDPTimeoutRule(r) && r.UDPTimeout == "1h0m0s" {
			return
		}
	}
	t.Fatal("missing route-options rule was not healed")
}

// The cheap steady-state guard: when the slot already matches, heal must
// not rewrite the file (no spurious SIGHUP every reconcile tick).
func TestSyncModeSlot_NoOpWhenConverged(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	modePath := seedTProxySlot(t, svc, dir, buildTProxySlot(TProxyParams{UDPTimeout: "1h0m0s"}))

	before, err := os.Stat(modePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	changed, err := svc.syncModeSlot(tproxySettings("1h0m0s"))
	if err != nil {
		t.Fatalf("syncModeSlot: %v", err)
	}
	if changed {
		t.Error("сошедшийся слот не должен перезаписываться")
	}

	after, err := os.Stat(modePath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("active rewritten despite matching timeout (before=%v after=%v)", before.ModTime(), after.ModTime())
	}
}

// tproxySettings — минимальные настройки режима tproxy с заданным таймаутом.
func tproxySettings(udpTimeout string) storage.SingboxRouterSettings {
	return storage.SingboxRouterSettings{RoutingMode: stateTProxy, UDPTimeout: udpTimeout}
}

// seedTProxySlot кладёт cfg в активный файл режимного слота и переподнимает
// оркестратор, чтобы слот считался включённым. Возвращает путь файла.
func seedTProxySlot(t *testing.T, svc *ServiceImpl, dir string, cfg *RouterConfig) string {
	t.Helper()
	seed, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	path := filepath.Join(dir, "20-tproxy.json")
	if err := os.WriteFile(path, seed, 0644); err != nil {
		t.Fatalf("seed mode slot: %v", err)
	}
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return path
}

func readRouterConfigFile(t *testing.T, path string) RouterConfig {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cfg RouterConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return cfg
}

func TestWaitForSingbox_ReturnsWhenRunning(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	stubListeningProbe(t, func() bool { return true })

	calls := 0
	svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) {
		calls++
		return calls >= 3, 1234 // false, false, true
	}

	start := time.Now()
	if err := svc.waitForSingbox(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("waitForSingbox: %v", err)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 polls, got %d", calls)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waitForSingbox took unexpectedly long: %v", elapsed)
	}
}

func TestWaitForSingbox_TimesOutWhenNeverRunning(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	// Default fakeSingbox.IsRunning returns (false, 0) — perfect for this case.

	start := time.Now()
	err := svc.waitForSingbox(context.Background(), 250*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("waitForSingbox returned too early: %v", elapsed)
	}
	if elapsed > 1*time.Second {
		t.Errorf("waitForSingbox returned too late: %v", elapsed)
	}
}

// Припаркованный слот. Orch.Save пишет такой слот в disabled/, а сравнение
// шло с активным путём — файла там нет, и запись каждый раз считалась
// изменением: лишний Save и debounced reload на каждом тике reconcile (а на
// тике reconcile за ним ещё и ожидание применения). Второй persist того же
// содержимого обязан отчитаться «не менялось».
func TestPersistSlotChanged_ParkedSlotSecondWriteIsNoOp(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	slot := orchestrator.SlotTProxy // режимный слот припаркован: движок выключен
	if st, ok := svc.slotSnapshot(slot); !ok || st.Enabled {
		t.Fatalf("предпосылка теста: слот %s должен быть припаркован (%+v)", slot, st)
	}

	cfg := NewEmptyConfig()
	cfg.Route.Rules = append(cfg.Route.Rules, Rule{Action: "route", Outbound: "direct", Domain: []string{"a.example"}})

	changed, err := svc.persistSlotChanged(slot, cfg, false)
	if err != nil {
		t.Fatalf("первая запись: %v", err)
	}
	if !changed {
		t.Fatal("первая запись припаркованного слота обязана считаться изменением")
	}
	parked := filepath.Join(dir, "disabled", "20-tproxy.json")
	if _, err := os.Stat(parked); err != nil {
		t.Fatalf("файл припаркованного слота не появился: %v", err)
	}

	changed, err = svc.persistSlotChanged(slot, cfg, false)
	if err != nil {
		t.Fatalf("повторная запись: %v", err)
	}
	if changed {
		t.Error("повторная запись того же содержимого не должна считаться изменением — иначе reload дёргается на каждом тике")
	}

	// Включённый слот сравнивается с активным файлом, как и раньше: его
	// пропажа обязана лечиться перезаписью.
	if err := svc.deps.Orch.SetEnabled(slot, true); err != nil {
		t.Fatalf("включить слот: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "20-tproxy.json")); err != nil {
		t.Fatalf("убрать активный файл: %v", err)
	}
	changed, err = svc.persistSlotChanged(slot, cfg, false)
	if err != nil {
		t.Fatalf("запись после пропажи активного файла: %v", err)
	}
	if !changed {
		t.Error("пропавший активный файл включённого слота обязан быть перезаписан")
	}
	if _, err := os.Stat(filepath.Join(dir, "20-tproxy.json")); err != nil {
		t.Errorf("активный файл не восстановлен: %v", err)
	}
}
