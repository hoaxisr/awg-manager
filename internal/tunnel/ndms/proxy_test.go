package ndms

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/rci"
)

// newProxyTestClient creates a ClientImpl backed by an httptest.Server.
// The handler receives POST /rci/ requests and can return arbitrary JSON.
func newProxyTestClient(t *testing.T, handler http.HandlerFunc) (*ClientImpl, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &ClientImpl{rci: rci.NewWithURL(srv.URL)}, srv
}

func TestBuildProxyCreatePayload(t *testing.T) {
	payload := buildProxyCreatePayload("Proxy0", "Germany VLESS", "127.0.0.1", 1080, true)

	// Re-marshal to test via JSON round-trip (same way rci.Post sends it).
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}

	iface := got["interface"].(map[string]any)
	p0 := iface["Proxy0"].(map[string]any)
	if p0["description"] != "Germany VLESS" {
		t.Errorf("description = %q, want %q", p0["description"], "Germany VLESS")
	}
	if p0["up"] != true {
		t.Error("expected up=true")
	}
	proxy := p0["proxy"].(map[string]any)
	protoObj := proxy["protocol"].(map[string]any)
	if protoObj["proto"] != "socks5" {
		t.Errorf("protocol.proto = %q, want socks5", protoObj["proto"])
	}
	if proxy["socks5-udp"] != true {
		t.Error("expected socks5-udp=true")
	}
	up := proxy["upstream"].(map[string]any)
	if up["host"] != "127.0.0.1" {
		t.Errorf("upstream.host = %v", up["host"])
	}
	if up["port"] != "1080" {
		t.Errorf("upstream.port = %v (type %T)", up["port"], up["port"])
	}
	ipObj, ok := p0["ip"].(map[string]any)
	if !ok {
		t.Fatalf("missing ip object: %+v", p0)
	}
	globalObj, ok := ipObj["global"].(map[string]any)
	if !ok {
		t.Fatalf("missing ip.global: %+v", ipObj)
	}
	if globalObj["auto"] != true {
		t.Errorf("ip.global.auto: %v want true", globalObj["auto"])
	}
}

func TestBuildProxyCreatePayload_NoUDP(t *testing.T) {
	payload := buildProxyCreatePayload("Proxy1", "US Proxy", "10.0.0.1", 2080, false)
	b, _ := json.Marshal(payload)
	var got map[string]any
	json.Unmarshal(b, &got)
	proxy := got["interface"].(map[string]any)["Proxy1"].(map[string]any)["proxy"].(map[string]any)
	if _, ok := proxy["socks5-udp"]; ok {
		t.Error("socks5-udp should be absent when socks5UDP=false")
	}
}

func TestCreateProxy(t *testing.T) {
	var receivedBody []byte
	c, _ := newProxyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	})

	ctx := context.Background()
	if err := c.CreateProxy(ctx, "Proxy0", "Germany VLESS", "127.0.0.1", 1080, true); err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(receivedBody, &parsed); err != nil {
		t.Fatalf("invalid JSON sent: %v", err)
	}
	iface := parsed["interface"].(map[string]any)
	if _, ok := iface["Proxy0"]; !ok {
		t.Error("expected Proxy0 key in interface payload")
	}
}

func TestDeleteProxy(t *testing.T) {
	var receivedBody []byte
	c, _ := newProxyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	})

	if err := c.DeleteProxy(context.Background(), "Proxy0"); err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	json.Unmarshal(receivedBody, &parsed)
	iface := parsed["interface"].(map[string]any)
	p0 := iface["Proxy0"].(map[string]any)
	if p0["no"] != true {
		t.Error("expected no=true in delete payload")
	}
}

func TestProxyUp(t *testing.T) {
	var receivedBody []byte
	c, _ := newProxyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	})

	if err := c.ProxyUp(context.Background(), "Proxy0"); err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	json.Unmarshal(receivedBody, &parsed)
	iface := parsed["interface"].(map[string]any)
	p0 := iface["Proxy0"].(map[string]any)
	if p0["up"] != true {
		t.Error("expected up=true in ProxyUp payload")
	}
}

func TestProxyDown(t *testing.T) {
	var receivedBody []byte
	c, _ := newProxyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	})

	if err := c.ProxyDown(context.Background(), "Proxy0"); err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	json.Unmarshal(receivedBody, &parsed)
	iface := parsed["interface"].(map[string]any)
	p0 := iface["Proxy0"].(map[string]any)
	if p0["down"] != true {
		t.Error("expected down=true in ProxyDown payload")
	}
}

func TestShowProxy_Exists(t *testing.T) {
	// Flat JSON returned by GET /show/interface/Proxy0 when the interface exists.
	resp := `{
		"id": "Proxy0",
		"interface-name": "Proxy0",
		"type": "Proxy",
		"description": "Germany VLESS",
		"state": "up",
		"link": "up"
	}`
	c, _ := newProxyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/show/interface/Proxy0" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(resp))
	})

	info, err := c.ShowProxy(context.Background(), "Proxy0")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Exists {
		t.Error("expected Exists=true")
	}
	if info.Name != "Proxy0" {
		t.Errorf("Name = %q", info.Name)
	}
	if info.Type != "Proxy" {
		t.Errorf("Type = %q, want Proxy", info.Type)
	}
	if info.Description != "Germany VLESS" {
		t.Errorf("Description = %q", info.Description)
	}
	if info.State != "up" {
		t.Errorf("State = %q, want up", info.State)
	}
	if info.Link != "up" {
		t.Errorf("Link = %q, want up", info.Link)
	}
	if !info.Up {
		t.Error("expected Up=true")
	}
}

func TestShowProxy_NotExists(t *testing.T) {
	// NDMS returns 200 with empty body when the interface does not exist.
	c, _ := newProxyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/show/interface/Proxy99" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		// Empty body — interface absent.
		w.WriteHeader(http.StatusOK)
	})

	info, err := c.ShowProxy(context.Background(), "Proxy99")
	if err != nil {
		t.Fatal(err)
	}
	if info.Exists {
		t.Error("expected Exists=false for missing interface")
	}
	if info.Name != "Proxy99" {
		t.Errorf("Name = %q", info.Name)
	}
}
