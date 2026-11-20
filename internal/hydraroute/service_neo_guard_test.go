package hydraroute

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScheduleRestart_SkipsWhenNotInstalled verifies that the central
// scheduleRestart no longer schedules a fork/exec when HR Neo binary is
// absent. Это central guard: одной защитой покрываем все 5 callsite'ов
// (rules-write/config-heal/config-write/policy-order/geo-sync).
//
// Bug repro: при отсутствующем /opt/bin/neo сборка планировала рестарт,
// AfterFunc через 2s делал fork/exec → лог "neo restart failed:
// no such file or directory". См. systematic-debugging session
// 2026-05-23.
func TestScheduleRestart_SkipsWhenNotInstalled(t *testing.T) {
	svc := &Service{}
	svc.SetStatusForTest(false) // not installed
	svc.scheduleRestart("test-reason")
	if svc.restartTimer != nil {
		t.Errorf("restartTimer установлен при !Installed — fork/exec будет вызван")
		svc.restartTimer.Stop()
	}
}

// TestScheduleRestart_SchedulesWhenInstalled regression-guard: при
// Installed=true прежнее поведение сохраняется.
func TestScheduleRestart_SchedulesWhenInstalled(t *testing.T) {
	svc := &Service{}
	svc.SetStatusForTest(true)
	svc.scheduleRestart("test-reason")
	if svc.restartTimer == nil {
		t.Fatalf("restartTimer должен быть установлен при Installed=true")
	}
	// Останавливаем чтобы AfterFunc не дёрнул реальный fork/exec в тестах.
	svc.restartTimer.Stop()
}

// TestSyncGeoFilesToConfig_NoOpWhenNotInstalled проверяет что при
// !Installed файл hrneo.conf не пишется и restart не планируется.
// Если neo удалили после awg-manager — мы не должны обновлять его
// конфиг "на будущее" (решено в session 2026-05-23).
func TestSyncGeoFilesToConfig_NoOpWhenNotInstalled(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "hrneo.conf")
	origPath := hrConfPath
	hrConfPath = confPath
	t.Cleanup(func() { hrConfPath = origPath })

	svc := &Service{}
	svc.SetStatusForTest(false)
	svc.SetGeoDataStore(&GeoDataStore{}) // ненулевой, чтобы пройти первую защиту

	if err := svc.SyncGeoFilesToConfig(); err != nil {
		t.Fatalf("SyncGeoFilesToConfig вернул ошибку при !Installed: %v", err)
	}
	if _, err := os.Stat(confPath); err == nil {
		t.Errorf("hrneo.conf создан при !Installed — не должен")
	}
	if svc.restartTimer != nil {
		t.Errorf("restartTimer установлен — restart будет вызван")
		svc.restartTimer.Stop()
	}
}

// TestHealInvalidRuntimeConfig_NoRestartWhenNotInstalled закрывает
// логический баг service.go:58 — раньше проверял `!Running`, что при
// !Installed (Running=false автоматом) приводило к планированию
// рестарта. Должно: только при Installed && !Running.
func TestHealInvalidRuntimeConfig_NoRestartWhenNotInstalled(t *testing.T) {
	svc := &Service{}
	svc.SetStatusForTest(false) // not installed → Running=false тоже

	// HealInvalidRuntimeConfig вызывает HealInvalidRuntimeConfig() (package
	// func), который ReadConfig'ом не найдёт файл — changed=false, выйдет
	// без побочек. Этого нам и достаточно: если бы он вошёл в горячую ветку,
	// то старый код бы planиrovaл restart. Тест ловит regression и для
	// fixed-logic-but-future-changes case.
	svc.HealInvalidRuntimeConfig()
	if svc.restartTimer != nil {
		t.Errorf("HealInvalidRuntimeConfig планирует restart при !Installed")
		svc.restartTimer.Stop()
	}
}

func TestNeoControlCommandPrefersNeo(t *testing.T) {
	dir := t.TempDir()
	oldNeo, oldInit := neoCommand, hrneoInit
	t.Cleanup(func() { neoCommand, hrneoInit = oldNeo, oldInit })
	neoCommand = filepath.Join(dir, "neo")
	hrneoInit = filepath.Join(dir, "S99hrneo")
	if err := os.WriteFile(neoCommand, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hrneoInit, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := neoControlCommand(); got != neoCommand {
		t.Fatalf("expected %s, got %s", neoCommand, got)
	}
}

func TestNeoControlCommandFallsBackToInit(t *testing.T) {
	dir := t.TempDir()
	oldNeo, oldInit := neoCommand, hrneoInit
	t.Cleanup(func() { neoCommand, hrneoInit = oldNeo, oldInit })
	neoCommand = filepath.Join(dir, "missing-neo")
	hrneoInit = filepath.Join(dir, "S99hrneo")
	if err := os.WriteFile(hrneoInit, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := neoControlCommand(); got != hrneoInit {
		t.Fatalf("expected fallback %s, got %s", hrneoInit, got)
	}
}

func TestNeoControlCommandKeepsNeoWhenNothingExists(t *testing.T) {
	dir := t.TempDir()
	oldNeo, oldInit := neoCommand, hrneoInit
	t.Cleanup(func() { neoCommand, hrneoInit = oldNeo, oldInit })
	neoCommand = filepath.Join(dir, "missing-neo")
	hrneoInit = filepath.Join(dir, "missing-init")
	if got := neoControlCommand(); got != neoCommand {
		t.Fatalf("expected %s, got %s", neoCommand, got)
	}
}
