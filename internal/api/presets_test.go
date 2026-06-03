package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/presets"
)

func newTestPresetsHandler(t *testing.T) *PresetsHandler {
	return NewPresetsHandler(presets.NewCatalog(presets.NewStore(t.TempDir())))
}

func TestPresetsHandlerListGET(t *testing.T) {
	h := newTestPresetsHandler(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/presets", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			Presets []presets.Preset `json:"presets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !env.Success || len(env.Data.Presets) < 50 {
		t.Fatalf("want success + full catalog (>=50), got %d", len(env.Data.Presets))
	}
}

func TestPresetsHandlerRejectsNonGET(t *testing.T) {
	h := newTestPresetsHandler(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodPost, "/api/presets", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
