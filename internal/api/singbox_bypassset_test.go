package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
)

// stubBypassSetStatus подменяет router-сервис: отдаёт заранее заданный итог
// последнего наполнения набора.
type stubBypassSetStatus struct {
	entryCount   int
	countOK      bool
	lastPopulate string
	lastError    string
	missingTags  []string
}

func (s *stubBypassSetStatus) BypassSetStatus() (int, bool, string, string, []string) {
	return s.entryCount, s.countOK, s.lastPopulate, s.lastError, s.missingTags
}

func getBypassSetStatus(t *testing.T, h *BypassSetHandler) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.GetStatus(rr, httptest.NewRequest(http.MethodGet, "/singbox/router/bypass-set/status", nil))
	return rr
}

func TestBypassSetGetStatus_ReportsServiceStatus(t *testing.T) {
	h := NewBypassSetHandler(&stubBypassSetStatus{
		entryCount:   4211,
		countOK:      true,
		lastPopulate: "2026-08-10T10:00:00Z",
		lastError:    "прошлый прогон упал",
		missingTags:  []string{"geoip:xx"},
	}, nil)

	rr := getBypassSetStatus(t, h)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		`"entryCount":4211`,
		`"entryCountOK":true`,
		`"lastPopulate":"2026-08-10T10:00:00Z"`,
		`"lastError":"прошлый прогон упал"`,
		`"missingTags":["geoip:xx"]`,
		`"installing":false`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
}

// countOK=false — размер набора снять не удалось; entryCount=0 в этом случае
// НЕ значит «набор пуст», и флаг обязан доехать до UI отдельным полем.
func TestBypassSetGetStatus_CountUnknown(t *testing.T) {
	h := NewBypassSetHandler(&stubBypassSetStatus{countOK: false}, nil)

	body := getBypassSetStatus(t, h).Body.String()
	if !strings.Contains(body, `"entryCountOK":false`) {
		t.Errorf("body missing entryCountOK:false: %s", body)
	}
	if !strings.Contains(body, `"entryCount":0`) {
		t.Errorf("body missing entryCount:0: %s", body)
	}
}

func TestBypassSetGetStatus_NilService(t *testing.T) {
	rr := getBypassSetStatus(t, NewBypassSetHandler(nil, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, `"entryCountOK":false`) {
		t.Errorf("body missing entryCountOK:false: %s", body)
	}
}

func TestBypassSetGetStatus_MethodGuard(t *testing.T) {
	h := NewBypassSetHandler(&stubBypassSetStatus{}, nil)
	rr := httptest.NewRecorder()
	h.GetStatus(rr, httptest.NewRequest(http.MethodPost, "/singbox/router/bypass-set/status", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST on status: code=%d, want 405", rr.Code)
	}
}

func TestBypassSetInstallDeps_Guards(t *testing.T) {
	h := NewBypassSetHandler(&stubBypassSetStatus{}, nil)

	rr := httptest.NewRecorder()
	h.InstallDeps(rr, httptest.NewRequest(http.MethodGet, "/singbox/router/bypass-set/install-deps", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on install-deps: code=%d, want 405", rr.Code)
	}

	post := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.InstallDeps(rr, httptest.NewRequest(http.MethodPost, "/singbox/router/bypass-set/install-deps", nil))
		return rr
	}
	if bypassset.IsIPSetAvailable() {
		// Уже установлен — сразу статус, opkg не запускается.
		rr := post()
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"available":true`) {
			t.Fatalf("install-deps with ipset present: code=%d body=%s", rr.Code, rr.Body.String())
		}
		return
	}
	// Идущая установка отвечает 409 и НЕ запускает вторую (проверка до opkg).
	h.installing.Store(true)
	defer h.installing.Store(false)
	rr = post()
	if rr.Code != http.StatusConflict {
		t.Fatalf("install-deps while installing: code=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestBypassSetInstallConntrack_Guards(t *testing.T) {
	h := NewBypassSetHandler(&stubBypassSetStatus{}, nil)

	rr := httptest.NewRecorder()
	h.InstallConntrack(rr, httptest.NewRequest(http.MethodGet, "/singbox/router/bypass-set/install-conntrack", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on install-conntrack: code=%d, want 405", rr.Code)
	}

	post := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.InstallConntrack(rr, httptest.NewRequest(http.MethodPost, "/singbox/router/bypass-set/install-conntrack", nil))
		return rr
	}
	if bypassset.IsConntrackAvailable() {
		rr := post()
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"conntrackAvailable":true`) {
			t.Fatalf("install-conntrack with conntrack present: code=%d body=%s", rr.Code, rr.Body.String())
		}
		return
	}
	h.installing.Store(true)
	defer h.installing.Store(false)
	rr = post()
	if rr.Code != http.StatusConflict {
		t.Fatalf("install-conntrack while installing: code=%d body=%s", rr.Code, rr.Body.String())
	}
}
