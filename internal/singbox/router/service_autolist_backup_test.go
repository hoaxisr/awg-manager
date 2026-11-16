package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// backupTestService — сервис с каталогом данных из SettingsStore (в тестах он
// всегда temp-каталог), как боевая проводка: снимок лежит рядом с settings.json.
func backupTestService(t *testing.T) *ServiceImpl {
	t.Helper()
	svc := newTeardownTestService(t)
	// Каталог данных панели уже temp-каталог (newTestSettingsStore) — путь
	// снимка внутрь него и проверяем.
	return svc
}

func backupFilePath(t *testing.T, svc *ServiceImpl) string {
	t.Helper()
	path, err := svc.autolistBackupPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// readBackupFile читает файл снимка с диска (или t.Fatal, если его нет).
func readBackupFile(t *testing.T, svc *ServiceImpl) AutolistBackup {
	t.Helper()
	raw, err := os.ReadFile(backupFilePath(t, svc))
	if err != nil {
		t.Fatalf("снимок не найден: %v", err)
	}
	var snap AutolistBackup
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("снимок повреждён: %v", err)
	}
	return snap
}

func TestCaptureSnapshot_WritesRuleRuleSetOutbound(t *testing.T) {
	svc := backupTestService(t)
	addAutolistObjects(t, svc, false)
	if err := svc.CaptureAutolistSnapshot(context.Background()); err != nil {
		t.Fatalf("CaptureAutolistSnapshot: %v", err)
	}
	snap := readBackupFile(t, svc)
	if snap.Version != 1 {
		t.Errorf("want version 1, got %d", snap.Version)
	}
	if snap.Rule == nil || !isAutolistOwnerRule(*snap.Rule) {
		t.Errorf("owner-правило в снимке: %+v", snap.Rule)
	}
	if snap.RuleSet == nil || snap.RuleSet.Tag != autolistRuleSetTag || snap.RuleSet.Type != "inline" || len(snap.RuleSet.Rules) == 0 {
		t.Errorf("rule-set в снимке: %+v", snap.RuleSet)
	}
	if snap.Outbound == nil || snap.Outbound.Tag != autolistTargetOutbound || snap.Outbound.BindInterface != "eth3" {
		t.Errorf("outbound в снимке: %+v", snap.Outbound)
	}
	if snap.CapturedAt.IsZero() {
		t.Error("capturedAt обязателен")
	}
	// Объекты при снятии снимка не удаляются.
	assertTeardownState(t, svc, false)
}

func TestCaptureSnapshot_EmptyConfig_RemovesStaleFile(t *testing.T) {
	svc := backupTestService(t)
	// Устаревший снимок от прежнего сноса.
	if err := writeAutolistBackup(backupFilePath(t, svc), &AutolistBackup{Version: 1, CapturedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CaptureAutolistSnapshot(context.Background()); err != nil {
		t.Fatalf("CaptureAutolistSnapshot on empty config: %v", err)
	}
	if _, err := os.Stat(backupFilePath(t, svc)); !os.IsNotExist(err) {
		t.Fatalf("устаревший снимок должен быть убран, stat: %v", err)
	}
}

func TestTeardown_WritesSnapshot_ThenRestoreRecreatesAll(t *testing.T) {
	svc := backupTestService(t)
	addAutolistObjects(t, svc, false)
	res, err := svc.TeardownAutolistObjects(context.Background())
	if err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if !res.BackupSaved {
		t.Fatal("снимок должен сохраниться")
	}
	assertTeardownState(t, svc, true)

	exists, capturedAt, err := svc.AutolistBackupInfo(context.Background())
	if err != nil || !exists {
		t.Fatalf("AutolistBackupInfo: exists=%v err=%v", exists, err)
	}
	if capturedAt.IsZero() {
		t.Error("capturedAt обязателен")
	}

	if err := svc.RestoreAutolistObjects(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	assertTeardownState(t, svc, false)
	if svc.StagingStatus(context.Background()).HasDraft {
		t.Error("после restore не должно оставаться неприменённого черновика")
	}
	// Снимок использован — файл убран, больше не предлагать.
	if exists, _, err := svc.AutolistBackupInfo(context.Background()); err != nil || exists {
		t.Fatalf("снимок должен быть удалён после restore: exists=%v err=%v", exists, err)
	}
}

func TestRestore_NoBackupFile_Errors(t *testing.T) {
	svc := backupTestService(t)
	err := svc.RestoreAutolistObjects(context.Background())
	if err == nil || !strings.Contains(err.Error(), "не найден") {
		t.Fatalf("want «не найден», got %v", err)
	}
}

func TestRestore_AllObjectsExist_Refuses(t *testing.T) {
	svc := backupTestService(t)
	addAutolistObjects(t, svc, false)
	if err := svc.CaptureAutolistSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Объекты живы: сателлит пересоздал их сам — восстановление не требуется.
	err := svc.RestoreAutolistObjects(context.Background())
	if err == nil || !strings.Contains(err.Error(), "уже существуют") {
		t.Fatalf("want «уже существуют», got %v", err)
	}
	assertTeardownState(t, svc, false)
	// Снимок при отказе сохраняется — пользователь решает сам.
	if _, err := os.Stat(backupFilePath(t, svc)); err != nil {
		t.Fatalf("снимок должен остаться: %v", err)
	}
}

func TestRestore_PartialExistence_RecreatesMissing(t *testing.T) {
	svc := backupTestService(t)
	addAutolistObjects(t, svc, false)
	if _, err := svc.TeardownAutolistObjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Сателлит запускался после сноса и создал ТОЛЬКО rule-set.
	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   autolistRuleSetTag,
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"example.com"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreAutolistObjects(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// Все три должны быть на месте: недостающие восстановлены, набор не задвоен.
	rules, err := svc.ListRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, r := range rules {
		if isAutolistOwnerRule(r) {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("want 1 owner rule, got %d", owners)
	}
	assertTeardownState(t, svc, false)
}

func TestRestore_BindInterfaceNotBindable_Refuses(t *testing.T) {
	svc := backupTestService(t)
	addAutolistObjects(t, svc, false)
	if _, err := svc.TeardownAutolistObjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Интерфейс привязки пропал из каталога bindable (VPN выключен).
	svc.deps.BindableInterfaces = fakeBindable{list: []WANInterfaceInfo{{Name: "ppp9", Up: true}}}
	err := svc.RestoreAutolistObjects(context.Background())
	if err == nil || !strings.Contains(err.Error(), "eth3") {
		t.Fatalf("want отказ по eth3, got %v", err)
	}
	// Отказ до мутаций: объекты не восстановлены, снимок жив.
	assertTeardownState(t, svc, true)
}

func TestRestore_ReadbackProvesAllThree(t *testing.T) {
	svc := backupTestService(t)
	addAutolistObjects(t, svc, false)
	if _, err := svc.TeardownAutolistObjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreAutolistObjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	rules, err := svc.ListRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sets, err := svc.ListRuleSets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	outbounds, err := svc.ListCompositeOutbounds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	owner, set, out := false, false, false
	for _, r := range rules {
		if isAutolistOwnerRule(r) {
			owner = true
		}
	}
	for _, rs := range sets {
		if rs.Tag == autolistRuleSetTag {
			set = true
		}
	}
	for _, ob := range outbounds {
		if ob.Tag == autolistTargetOutbound {
			out = true
		}
	}
	if !owner || !set || !out {
		t.Fatalf("readback: owner=%v set=%v outbound=%v", owner, set, out)
	}
}

func TestDeleteAutolistBackup_RemovesFile(t *testing.T) {
	svc := backupTestService(t)
	addAutolistObjects(t, svc, false)
	if err := svc.CaptureAutolistSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteAutolistBackup(context.Background()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(backupFilePath(t, svc)); !os.IsNotExist(err) {
		t.Fatalf("файл снимка должен быть удалён, stat: %v", err)
	}
	// Отсутствующий снимок — не ошибка (идемпотентность).
	if err := svc.DeleteAutolistBackup(context.Background()); err != nil {
		t.Fatalf("delete on missing file: %v", err)
	}
}

func TestAutolistBackupInfo_AbsentFile_IsClean(t *testing.T) {
	svc := backupTestService(t)
	exists, capturedAt, err := svc.AutolistBackupInfo(context.Background())
	if err != nil {
		t.Fatalf("AutolistBackupInfo: %v", err)
	}
	if exists || !capturedAt.IsZero() {
		t.Fatalf("want clean info, got exists=%v capturedAt=%v", exists, capturedAt)
	}
}

func TestAutolistBackupPath_InDataDir(t *testing.T) {
	svc := backupTestService(t)
	path := backupFilePath(t, svc)
	dataDir := filepath.Dir(svc.deps.Settings.DataDir() + string(os.PathSeparator) + "settings.json")
	if filepath.Dir(path) != filepath.Dir(filepath.Join(dataDir, autolistBackupFile)) {
		t.Fatalf("снимок обязан лежать в каталоге данных панели: %s", path)
	}
	if filepath.Base(path) != autolistBackupFile {
		t.Fatalf("want base %q, got %q", autolistBackupFile, filepath.Base(path))
	}
}
