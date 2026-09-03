package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(h http.Handler, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNewHandler_NoKeyIsOpen(t *testing.T) {
	rec := post(newHandler(""), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "list_tunnels") {
		t.Fatalf("tools/list body = %s", rec.Body.String())
	}
}

func TestNewHandler_KeyEnforced(t *testing.T) {
	h := newHandler("awgm_devkey")
	if rec := post(h, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := post(h, "awgm_devkey"); rec.Code != http.StatusOK {
		t.Fatalf("good key: %d %s", rec.Code, rec.Body.String())
	}
}
