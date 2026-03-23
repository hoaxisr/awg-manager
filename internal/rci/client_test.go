package rci

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(handler http.Handler) (*Client, *httptest.Server) {
	srv := httptest.NewServer(handler)
	c := &Client{
		http:    srv.Client(),
		baseURL: srv.URL,
	}
	return c, srv
}

func TestGet_DecodesJSON(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/show/version" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"release":"4.03"}`))
	}))
	defer srv.Close()

	var dst struct{ Release string }
	if err := c.Get(context.Background(), "/show/version", &dst); err != nil {
		t.Fatal(err)
	}
	if dst.Release != "4.03" {
		t.Errorf("release = %q", dst.Release)
	}
}

func TestGetRaw_ReturnsBytes(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"raw": true}`))
	}))
	defer srv.Close()

	data, err := c.GetRaw(context.Background(), "/show/interface/Wireguard0")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"raw": true}` {
		t.Errorf("got %q", string(data))
	}
}

func TestPost_SendsJSON(t *testing.T) {
	var received []byte
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		received, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	payload := map[string]any{"ip": map[string]any{"route": map[string]any{"default": true}}}
	_, err := c.Post(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	json.Unmarshal(received, &parsed)
	if parsed["ip"] == nil {
		t.Error("expected ip key in payload")
	}
}

func TestPostBatch_SendsArray(t *testing.T) {
	var received []byte
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.Write([]byte(`[{}, {}]`))
	}))
	defer srv.Close()

	cmds := []any{
		map[string]any{"interface": map[string]any{"name": "Wireguard0", "up": true}},
		map[string]any{"system": map[string]any{"configuration": map[string]any{"save": map[string]any{}}}},
	}
	results, err := c.PostBatch(context.Background(), cmds)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	var parsed []any
	json.Unmarshal(received, &parsed)
	if len(parsed) != 2 {
		t.Errorf("expected 2-element array, got %d", len(parsed))
	}
}

func TestGet_Non200_ReturnsError(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	var dst any
	err := c.Get(context.Background(), "/show/version", &dst)
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
}

func TestPost_RCIError_ReturnsError(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"error","message":"address conflict"}`))
	}))
	defer srv.Close()

	_, err := c.Post(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for RCI error response")
	}
}
