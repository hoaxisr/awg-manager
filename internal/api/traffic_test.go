package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/traffic"
)

func TestTrafficHandler_RejectsUnsupportedPeriods(t *testing.T) {
	h := &TunnelsHandler{}
	h.SetTrafficHistory(traffic.New())

	cases := []string{"15m", "2h", "48h", "7d", "30d", "bogus"}
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

	for _, p := range []string{"5m", "10m", "30m", "1h", "3h", "6h", "12h", "24h"} {
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

func TestTrafficHandler_AcceptsEmojiID(t *testing.T) {
	h := &TunnelsHandler{}
	h.SetTrafficHistory(traffic.New())
	v := url.Values{}
	v.Set("id", "🇷🇺 Russia [*CIDR] YA")
	v.Set("period", "1h")
	req := httptest.NewRequest(http.MethodGet, "/api/tunnels/traffic?"+v.Encode(), nil)
	rr := httptest.NewRecorder()
	h.Traffic(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("emoji id: want 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestTrafficHandler_AcceptsSpaceID(t *testing.T) {
	h := &TunnelsHandler{}
	h.SetTrafficHistory(traffic.New())
	v := url.Values{}
	v.Set("id", "AWG Test")
	v.Set("period", "1h")
	req := httptest.NewRequest(http.MethodGet, "/api/tunnels/traffic?"+v.Encode(), nil)
	rr := httptest.NewRecorder()
	h.Traffic(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("space id: want 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestTrafficHandler_RejectsOversizedID(t *testing.T) {
	h := &TunnelsHandler{}
	h.SetTrafficHistory(traffic.New())
	v := url.Values{}
	v.Set("id", strings.Repeat("a", 257))
	v.Set("period", "1h")
	req := httptest.NewRequest(http.MethodGet, "/api/tunnels/traffic?"+v.Encode(), nil)
	rr := httptest.NewRecorder()
	h.Traffic(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("oversized id: want 400, got %d", rr.Code)
	}
}

func TestTrafficHandler_RejectsControlCharID(t *testing.T) {
	h := &TunnelsHandler{}
	h.SetTrafficHistory(traffic.New())
	v := url.Values{}
	v.Set("id", "foo\x00bar")
	v.Set("period", "1h")
	req := httptest.NewRequest(http.MethodGet, "/api/tunnels/traffic?"+v.Encode(), nil)
	rr := httptest.NewRecorder()
	h.Traffic(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("control-char id: want 400, got %d", rr.Code)
	}
}

func TestTrafficHandler_AcceptsExactly256ByteID(t *testing.T) {
	h := &TunnelsHandler{}
	h.SetTrafficHistory(traffic.New())
	v := url.Values{}
	v.Set("id", strings.Repeat("a", 256))
	v.Set("period", "1h")
	req := httptest.NewRequest(http.MethodGet, "/api/tunnels/traffic?"+v.Encode(), nil)
	rr := httptest.NewRecorder()
	h.Traffic(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("256-byte id (boundary): want 200, got %d", rr.Code)
	}
}
