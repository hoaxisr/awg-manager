package firc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreSnapshot_AllowsTrustTunnelAndXHTTP(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	sbDir := filepath.Join(tmp, "singbox", "config.d")
	fircDir := filepath.Join(tmp, "firc")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fircDir, 0755); err != nil {
		t.Fatal(err)
	}

	tunnelsJSON := `{
  "outbounds": [
    {"tag": "direct", "type": "direct"},
    {
      "tag": "🇱🇻 LAT (Латвия) (Premium)",
      "type": "trusttunnel",
      "server": "lt2.trutun.online",
      "server_port": 443,
      "username": "u1",
      "password": "p1",
      "quic": false,
      "tls": {
        "enabled": true,
        "fragment": true,
        "server_name": "lt2.trutun.online"
      }
    },
    {
      "tag": "vless-ws-node",
      "type": "vless",
      "server": "example.com",
      "server_port": 443,
      "uuid": "11111111-1111-1111-1111-111111111111",
      "transport": {
        "type": "ws",
        "path": "/ws"
      }
    }
  ]
}`
	disabledDir := filepath.Join(sbDir, "disabled")
	if err := os.MkdirAll(disabledDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(tunnelsJSON), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "20-router.json"), []byte(`{"route":{"rules":[]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	routerDisabledJSON := `{"http_clients":[{"tag":"rs-download"}],"route":{"default_http_client":"rs-download","final":"direct","rules":[{"ip_cidr":["10.0.0.0/8"],"action":"hijack-dns"}]}}`
	if err := os.WriteFile(filepath.Join(disabledDir, "20-router.json"), []byte(routerDisabledJSON), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "settings.json"), []byte(`{"singboxRouter":{"routingEngine":"sing-box","interceptFamily":"ipv4_only"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "client-routes.json"), []byte(`{"routes":[],"tables":{}}`), 0600); err != nil {
		t.Fatal(err)
	}

	op := NewOperator(Options{ConfigDir: fircDir})
	op.SetSingboxValidator(func(_ context.Context, _ string) error { return nil })

	// Снимок до правок (с disabled/20-router.json и client-routes.json), затем
	// FIRC tunnels.yaml со ссылкой tt://.
	snap, err := op.CreateSystemSnapshot(sbDir, "pre-migrate", "auto-pre-migrate", "sing-box")
	if err != nil {
		t.Fatalf("CreateSystemSnapshot: %v", err)
	}
	if err := op.SaveTunnelsFile([]Tunnel{{
		ID:          "lat",
		Description: "LAT",
		Device:      "tunvless0",
		Enable:      true,
		Sources: []TunnelSource{{
			ID:   "08b59030",
			Kind: "link",
			Name: "🇱🇻 LAT (Латвия) (Premium)",
			Link: "tt://u1:p1@lt2.trutun.online:443?record_fragment=1&sni=lt2.trutun.online#%F0%9F%87%B1%F0%9F%87%BB%20LAT%20%28%D0%9B%D0%B0%D1%82%D0%B2%D0%B8%D1%8F%29%20%28Premium%29",
		}},
	}}); err != nil {
		t.Fatalf("SaveTunnelsFile: %v", err)
	}
	if err := op.SaveGroupsFile([]Group{}); err != nil {
		t.Fatalf("SaveGroupsFile: %v", err)
	}

	// Simulate modified/stripped active sing-box config before return.
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := op.RestoreSystemSnapshot(ctx, snap.ID, sbDir); err != nil {
		t.Fatalf("RestoreSystemSnapshot: %v", err)
	}

	restoredRaw, err := os.ReadFile(filepath.Join(sbDir, "10-tunnels.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restoredRaw), `"type": "trusttunnel"`) {
		t.Fatalf("expected restored 10-tunnels.json to contain trusttunnel outbound, got %s", string(restoredRaw))
	}
	restoredDisabled, err := os.ReadFile(filepath.Join(disabledDir, "20-router.json"))
	if err != nil {
		t.Fatalf("expected restored disabled/20-router.json: %v", err)
	}
	if !strings.Contains(string(restoredDisabled), `"http_clients"`) || !strings.Contains(string(restoredDisabled), `"default_http_client"`) {
		t.Fatalf("expected restored disabled/20-router.json to preserve http_clients/default_http_client, got %s", string(restoredDisabled))
	}
}

func TestMigrationResult_SerializesFalseBooleansExplicitly(t *testing.T) {
	res := MigrationResult{
		Tunnels:        []Tunnel{},
		Groups:         []Group{},
		ConfigMigrated: true,
		FircStarted:    false,
		EngineSwitched: false,
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(raw)
	for _, want := range []string{`"configMigrated":true`, `"fircStarted":false`, `"engineSwitched":false`} {
		if !strings.Contains(s, want) {
			t.Fatalf("expected %s in %s", want, s)
		}
	}
}

func TestValidateSingboxOutboundSemantics_AllowsXHTTP(t *testing.T) {
	ob := map[string]any{
		"tag":         "sub-xhttp",
		"type":        "vless",
		"server":      "example.com",
		"server_port": float64(443),
		"uuid":        "11111111-2222-3333-4444-555555555555",
		"transport": map[string]any{
			"type":            "xhttp",
			"host":            "dogs.abvphelp.cc",
			"mode":            "auto",
			"path":            "/xhttp",
			"x_padding_bytes": "100-1000",
			"no_grpc_header":  true,
		},
	}
	if _, err := validateSingboxOutboundSemantics(ob); err != nil {
		t.Fatalf("expected xhttp outbound to validate, got: %v", err)
	}
}

func TestValidateSubscriptionsJSONSemantics_AllowsUplinkBinding(t *testing.T) {
	data := []byte(`[{
		"id": "sub1",
		"label": "ABV",
		"url": "https://example.com/sub.json",
		"enabled": true,
		"bindInterface": "eth3",
		"uplinkBinding": {"device": "eth3", "ipv4": "192.168.90.3", "dnsServers": ["192.168.90.1:53"]}
	}]`)
	if _, err := validateSubscriptionsJSONSemantics(data); err != nil {
		t.Fatalf("expected uplinkBinding to validate in subscriptions.json snapshot, got: %v", err)
	}
}
