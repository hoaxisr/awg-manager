package rci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestImportWireguardConfig(t *testing.T) {
	confData := []byte("[Interface]\nPrivateKey = AAAA=\nAddress = 10.0.0.2/32\n")
	expectedB64 := base64.StdEncoding.EncodeToString(confData)

	var receivedPayload map[string]any

	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedPayload)

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"interface":{"wireguard":{"import":{"created":"Wireguard1","intersects":"","status":[{"status":"message","message":"imported"}]}}}}`))
	}))
	defer srv.Close()

	name, err := c.ImportWireguardConfig(context.Background(), confData, "tunnel.conf")
	if err != nil {
		t.Fatalf("ImportWireguardConfig: %v", err)
	}
	if name != "Wireguard1" {
		t.Errorf("name = %q, want Wireguard1", name)
	}

	// Verify payload structure.
	iface := receivedPayload["interface"].(map[string]any)
	wg := iface["wireguard"].(map[string]any)
	if wg["import"] != expectedB64 {
		t.Errorf("import = %q, want %q", wg["import"], expectedB64)
	}
	if wg["name"] != "" {
		t.Errorf("name = %q, want empty", wg["name"])
	}
	if wg["filename"] != "tunnel.conf" {
		t.Errorf("filename = %q, want tunnel.conf", wg["filename"])
	}
}

func TestImportWireguardConfig_NoName(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"interface":{"wireguard":{"import":{"created":"","intersects":""}}}}`))
	}))
	defer srv.Close()

	_, err := c.ImportWireguardConfig(context.Background(), []byte("test"), "test.conf")
	if err == nil {
		t.Fatal("expected error for empty created")
	}
}
