package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/traffic"
)

func TestTrafficHandler_RejectsUnsupportedPeriods(t *testing.T) {
	h := &TunnelsHandler{}
	h.SetTrafficHistory(traffic.New())

	cases := []string{"3h", "7d", "30d", "bogus"}
	for _, p := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/tunnels/traffic?id=awg0&period="+p, nil)
		rr := httptest.NewRecorder()
		h.Traffic(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("period=%q: want 400, got %d", p, rr.Code)
		}
	}
}

func TestTrafficHandler_AcceptsValidPeriods(t *testing.T) {
	h := &TunnelsHandler{}
	h.SetTrafficHistory(traffic.New())

	for _, p := range []string{"1h", "24h"} {
		req := httptest.NewRequest(http.MethodGet, "/api/tunnels/traffic?id=awg0&period="+p, nil)
		rr := httptest.NewRecorder()
		h.Traffic(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("period=%q: want 200, got %d", p, rr.Code)
		}
	}
}

func TestTrafficHandler_MissingID(t *testing.T) {
	h := &TunnelsHandler{}
	h.SetTrafficHistory(traffic.New())
	req := httptest.NewRequest(http.MethodGet, "/api/tunnels/traffic?period=1h", nil)
	rr := httptest.NewRecorder()
	h.Traffic(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for missing id, got %d", rr.Code)
	}
}

func TestTrafficHandler_WrongMethod(t *testing.T) {
	h := &TunnelsHandler{}
	h.SetTrafficHistory(traffic.New())
	req := httptest.NewRequest(http.MethodPost, "/api/tunnels/traffic?id=awg0&period=1h", nil)
	rr := httptest.NewRecorder()
	h.Traffic(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405 for POST, got %d", rr.Code)
	}
}
