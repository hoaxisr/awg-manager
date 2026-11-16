package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/autolistdetect"
)

// newAutolistStatusHandler — Status-хендлер с подменённой проверкой снимка.
func newAutolistStatusHandler(t *testing.T, probe func(context.Context) (bool, time.Time, error)) *AutolistHandler {
	t.Helper()
	h := NewAutolistHandler(autolistdetect.NewService())
	h.SetLogger(nil) // nil-safe ScopedLogger: Warn молчит
	h.SetHasRemovedBackupProbe(probe)
	return h
}

func getAutolistStatus(t *testing.T, h *AutolistHandler) autolistdetect.Status {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/autolist/status", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Data struct {
			AutolistStatus *autolistdetect.Status `json:"autolistStatus"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, rr.Body.String())
	}
	if env.Data.AutolistStatus == nil {
		t.Fatal("autolistStatus отсутствует в ответе")
	}
	return *env.Data.AutolistStatus
}

func TestAutolistStatus_HasRemovedBackup_ReachesJSON(t *testing.T) {
	h := newAutolistStatusHandler(t, func(context.Context) (bool, time.Time, error) {
		return true, time.Now(), nil
	})
	st := getAutolistStatus(t, h)
	if !st.HasRemovedBackup {
		t.Error("hasRemovedBackup=true должен доехать до JSON-ответа /autolist/status")
	}
}

func TestAutolistStatus_NoSnapshot_FlagFalse(t *testing.T) {
	h := newAutolistStatusHandler(t, func(context.Context) (bool, time.Time, error) {
		return false, time.Time{}, nil
	})
	st := getAutolistStatus(t, h)
	if st.HasRemovedBackup {
		t.Error("без снимка флаг должен остаться false")
	}
}

func TestAutolistStatus_ProbeError_FlagFalse_StatusStill200(t *testing.T) {
	h := newAutolistStatusHandler(t, func(context.Context) (bool, time.Time, error) {
		return false, time.Time{}, errors.New("data dir unavailable")
	})
	// Отказ проверки снимка не валит статус: 200 + флаг false.
	st := getAutolistStatus(t, h)
	if st.HasRemovedBackup {
		t.Error("при ошибке проверки флаг должен остаться false")
	}
}

func TestAutolistStatus_NilProbe_FlagFalse(t *testing.T) {
	h := NewAutolistHandler(autolistdetect.NewService())
	st := getAutolistStatus(t, h)
	if st.HasRemovedBackup {
		t.Error("без подключённой проверки флаг должен остаться false")
	}
}
