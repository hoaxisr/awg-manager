package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/pingcheck"
)

// Всё, что должно попасть на диск, обязано быть проставлено ДО вызова сервиса:
// запись сохраняет он, и хендлер после возврата её уже не трогает. Перенос
// блока дефолтов вниз оставил бы туннели без настроек пингчека.
func TestCreate_PingCheckDefaultsSetBeforeService(t *testing.T) {
	stub := &stubTunnelSvc{}
	h, _ := newTunnelsUpdateHarness(t, stub)
	h.pingCheck = &stubPingCheck{}

	postCreate(t, h, createBody(""))

	if stub.createdRecord == nil {
		t.Fatal("Create не был вызван")
	}
	if stub.createdRecord.PingCheck == nil {
		t.Fatal("дефолты пингчека обязаны быть в записи, которую получает сервис")
	}
	if stub.createdRecord.PingCheck.Target != "8.8.8.8" {
		t.Errorf("Target = %q, want 8.8.8.8", stub.createdRecord.PingCheck.Target)
	}
}

// Идентичность и отметка времени тоже проставляются до вызова.
func TestCreate_IdentitySetBeforeService(t *testing.T) {
	stub := &stubTunnelSvc{}
	h, _ := newTunnelsUpdateHarness(t, stub)

	postCreate(t, h, createBody("awg13"))

	if stub.createdRecord == nil {
		t.Fatal("Create не был вызван")
	}
	if stub.createdRecord.ID != "awg13" {
		t.Errorf("ID = %q, want awg13", stub.createdRecord.ID)
	}
	if stub.createdRecord.CreatedAt == "" {
		t.Error("CreatedAt обязан быть проставлен до сохранения")
	}
	if stub.createdRecord.Type != "awg" {
		t.Errorf("Type = %q, want awg", stub.createdRecord.Type)
	}
}

// Хендлер больше не владеет записью: при отказе сервиса он не должен ничего
// сохранять сам и обязан донести причину.
func TestCreate_HandlerDoesNotPersistOnServiceError(t *testing.T) {
	stub := &stubTunnelSvc{createErr: errors.New("NDMS отказал")}
	h, store := newTunnelsUpdateHarness(t, stub)

	rec := postCreate(t, h, createBody("awg13"))

	if rec.Code == http.StatusOK {
		t.Fatal("отказ сервиса обязан быть отказом ручки")
	}
	if _, err := store.Get("awg13"); err == nil {
		t.Error("запись сохранять некому — сервис отказал")
	}
	if body := rec.Body.String(); !strings.Contains(body, "NDMS") {
		t.Errorf("причина отказа должна дойти до клиента, got %s", body)
	}
}

// stubPingCheck — пустышка: хендлеру нужен лишь непустой сервис, чтобы
// проставить дефолты пингчека в записи.
type stubPingCheck struct{}

func (stubPingCheck) GetStatus() []pingcheck.TunnelStatus       { return nil }
func (stubPingCheck) GetLogs() []pingcheck.LogEntry             { return nil }
func (stubPingCheck) GetTunnelLogs(string) []pingcheck.LogEntry { return nil }
func (stubPingCheck) ClearLogs()                                {}
func (stubPingCheck) CheckAllNow()                              {}
func (stubPingCheck) IsEnabled() bool                           { return true }
func (stubPingCheck) StartMonitoringAllRunning()                {}
func (stubPingCheck) StopMonitoringAll()                        {}
func (stubPingCheck) Stop()                                     {}
func (stubPingCheck) StartMonitoring(string, string, ...bool)   {}
func (stubPingCheck) StopMonitoring(string)                     {}
func (stubPingCheck) GetTunnelPingStatus(string) pingcheck.TunnelPingInfo {
	return pingcheck.TunnelPingInfo{}
}
