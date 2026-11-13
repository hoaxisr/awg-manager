package awgm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientEnvelopeAndBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing API key")
		}
		if r.URL.Path != "/api/singbox/router/status" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]string{"final": "direct"}})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.RouterStatus(context.Background())
	if err != nil || status.Final != "direct" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestClientPreservesAWGMError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": true, "code": "NO_DRAFT", "message": "no draft to apply"})
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "")
	err := client.get(context.Background(), "/api/test", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "NO_DRAFT" || apiErr.Status != http.StatusConflict {
		t.Fatalf("error=%v", err)
	}
}
