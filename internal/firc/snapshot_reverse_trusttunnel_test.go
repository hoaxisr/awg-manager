package firc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreSavedSingboxFiles_AllowsTrustTunnelAndXHTTP(t *testing.T) {
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

	// Save the pre-FIRC sing-box return bundle and an auto-pre-migrate snapshot (covering disabled/20-router.json + client-routes.json),
	// then write FIRC tunnels.yaml with tt:// link.
	if err := op.SavePreFircSingboxConfig(sbDir, true); err != nil {
		t.Fatalf("SavePreFircSingboxConfig: %v", err)
	}
	if _, err := op.CreateSystemSnapshot(sbDir, "pre-migrate", "auto-pre-migrate", "sing-box"); err != nil {
		t.Fatalf("CreateSystemSnapshot: %v", err)
	}
	// Simulate absent saved-singbox-config.json (forcing auto-pre-migrate snapshot fallback with disabled/20-router.json).
	_ = os.Remove(op.savedSingboxConfigPath())
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

	st := op.GetSavedSingboxStatus(sbDir)
	if !st.Available || st.VerificationStatus != "verified" {
		t.Fatalf("expected verified saved sing-box status, got %+v", st)
	}

	res, err := op.RestoreSavedSingboxFiles(ctx, sbDir, true, "firc")
	if err != nil {
		t.Fatalf("RestoreSavedSingboxFiles failed: %v", err)
	}
	if res.RestoredFiles == 0 || res.SnapshotCreated == nil {
		t.Fatalf("expected restored files and protective snapshot, got %+v", res)
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

	// Also verify reverse link conversion preserves tt:// -> trusttunnel.
	conv, err := convertFircLinkToSingboxOutbound(
		"tt://u1:p1@lt2.trutun.online:443?record_fragment=1&sni=lt2.trutun.online#LAT",
		"lat",
		"LAT",
		0,
	)
	if err != nil {
		t.Fatalf("convertFircLinkToSingboxOutbound(tt): %v", err)
	}
	if conv["type"] != "trusttunnel" || conv["server"] != "lt2.trutun.online" {
		t.Fatalf("unexpected converted trusttunnel outbound: %+v", conv)
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

func TestReverseMigrate_RemoteRuleSetsIncludeHTTPClientAndMergePreservesRouterFields(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	sbDir := filepath.Join(tmp, "singbox", "config.d")
	disabledDir := filepath.Join(sbDir, "disabled")
	fircDir := filepath.Join(tmp, "firc")
	if err := os.MkdirAll(disabledDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fircDir, 0755); err != nil {
		t.Fatal(err)
	}

	existingDisabledRouter := `{
  "inbounds": [{"type": "redirect", "tag": "redirect-in", "listen": "0.0.0.0", "listen_port": 51272}],
  "dns": {"final": "dns-direct", "servers": [{"tag": "dns-direct", "type": "udp", "server": "77.88.8.8"}]},
  "http_clients": [{"tag": "rs-download", "detour": "direct"}],
  "route": {
    "default_http_client": "rs-download",
    "final": "direct",
    "auto_detect_interface": true,
    "rule_set": [{"tag": "geosite-existing", "type": "remote", "format": "binary", "url": "https://example.com/existing.srs"}],
    "rules": [{"rule_set": ["geosite-existing"], "outbound": "LAT"}]
  }
}`
	if err := os.WriteFile(filepath.Join(disabledDir, "20-router.json"), []byte(existingDisabledRouter), 0600); err != nil {
		t.Fatal(err)
	}

	op := NewOperator(Options{ConfigDir: fircDir})
	op.SetSingboxValidator(func(_ context.Context, _ string) error { return nil })

	if err := op.SaveTunnelsFile([]Tunnel{{
		ID:          "lat",
		Description: "LAT",
		Device:      "tunvless0",
		Enable:      true,
		Sources: []TunnelSource{{
			ID:   "s1",
			Kind: "link",
			Name: "LAT",
			Link: "tt://u1:p1@lt2.trutun.online:443?sni=lt2.trutun.online#LAT",
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := op.SaveGroupsFile([]Group{{
		ID:        "g-youtube",
		Name:      "YouTube",
		Interface: []string{"tunvless0"},
		Enable:    true,
		List:      &GroupList{URL: "https://example.com/youtube.srs"},
	}}); err != nil {
		t.Fatal(err)
	}

	// Merge mode must preserve inbounds, dns, final, auto_detect_interface, default_http_client, and clean detour:direct.
	if _, err := op.ReverseMigrateToSingbox(ctx, sbDir, &ReverseMigrationOptions{Mode: "merge"}, "firc"); err != nil {
		t.Fatalf("ReverseMigrateToSingbox(merge): %v", err)
	}
	mergedRaw, err := os.ReadFile(filepath.Join(sbDir, "20-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	var merged map[string]any
	if err := json.Unmarshal(mergedRaw, &merged); err != nil {
		t.Fatal(err)
	}
	if _, ok := merged["inbounds"]; !ok {
		t.Fatalf("merge dropped inbounds: %s", string(mergedRaw))
	}
	if _, ok := merged["dns"]; !ok {
		t.Fatalf("merge dropped dns: %s", string(mergedRaw))
	}
	clients, _ := merged["http_clients"].([]any)
	if len(clients) == 0 {
		t.Fatalf("merge missing http_clients: %s", string(mergedRaw))
	}
	firstClient, _ := clients[0].(map[string]any)
	if _, hasDetour := firstClient["detour"]; hasDetour {
		t.Fatalf("merge retained illegal empty direct detour: %+v", firstClient)
	}
	rt, _ := merged["route"].(map[string]any)
	if rt["default_http_client"] != "rs-download" || rt["final"] != "direct" || rt["auto_detect_interface"] != true {
		t.Fatalf("merge lost route metadata: %+v", rt)
	}

	// Replace mode with a remote rule-set group must still emit http_clients and default_http_client.
	if _, err := op.ReverseMigrateToSingbox(ctx, sbDir, &ReverseMigrationOptions{Mode: "replace"}, "firc"); err != nil {
		t.Fatalf("ReverseMigrateToSingbox(replace): %v", err)
	}
	replacedRaw, err := os.ReadFile(filepath.Join(sbDir, "20-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	var replaced map[string]any
	if err := json.Unmarshal(replacedRaw, &replaced); err != nil {
		t.Fatal(err)
	}
	if _, ok := replaced["http_clients"]; !ok {
		t.Fatalf("replace with remote rule_set missing http_clients: %s", string(replacedRaw))
	}
	rtRep, _ := replaced["route"].(map[string]any)
	if rtRep["default_http_client"] != "rs-download" {
		t.Fatalf("replace with remote rule_set missing default_http_client: %+v", rtRep)
	}
}

func TestReverseMigrate_GeneratesAndPreservesPerTunnelMixedInbounds(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	sbDir := filepath.Join(tmp, "singbox", "config.d")
	disabledDir := filepath.Join(sbDir, "disabled")
	fircDir := filepath.Join(tmp, "firc")
	if err := os.MkdirAll(disabledDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fircDir, 0755); err != nil {
		t.Fatal(err)
	}

	existing10 := `{
  "inbounds": [
    {"listen": "127.0.0.1", "listen_port": 1081, "tag": "LAT-in", "type": "mixed"}
  ],
  "outbounds": [
    {"tag": "LAT", "type": "trusttunnel", "server": "lt2.trutun.online", "server_port": 443}
  ],
  "route": {
    "rules": [{"inbound": "LAT-in", "outbound": "LAT"}]
  }
}`
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(existing10), 0600); err != nil {
		t.Fatal(err)
	}

	op := NewOperator(Options{ConfigDir: fircDir})
	op.SetSingboxValidator(func(_ context.Context, _ string) error { return nil })
	if err := op.SaveTunnelsFile([]Tunnel{
		{
			ID:          "lat",
			Description: "LAT",
			Device:      "tunvless0",
			Enable:      true,
			Sources: []TunnelSource{{
				ID:   "s1",
				Kind: "link",
				Name: "LAT",
				Link: "tt://u1:p1@lt2.trutun.online:443?sni=lt2.trutun.online#LAT",
			}},
		},
		{
			ID:          "fin",
			Description: "FIN",
			Device:      "tunvless1",
			Enable:      true,
			Sources: []TunnelSource{{
				ID:   "s2",
				Kind: "link",
				Name: "FIN",
				Link: "tt://u2:p2@fi.trutun.online:443?sni=fi.trutun.online#FIN",
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Merge keeps LAT-in on 1081 and allocates 1080 for FIN-in.
	if _, err := op.ReverseMigrateToSingbox(ctx, sbDir, &ReverseMigrationOptions{Mode: "merge"}, "firc"); err != nil {
		t.Fatalf("ReverseMigrateToSingbox(merge): %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(sbDir, "10-tunnels.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Inbounds []struct {
			Tag        string `json:"tag"`
			Type       string `json:"type"`
			Listen     string `json:"listen"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
		Route struct {
			Rules []struct {
				Inbound  string `json:"inbound"`
				Outbound string `json:"outbound"`
			} `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	gotPorts := map[string]int{}
	for _, ib := range doc.Inbounds {
		if ib.Type != "mixed" || ib.Listen != "127.0.0.1" {
			t.Fatalf("unexpected inbound shape: %+v", ib)
		}
		gotPorts[ib.Tag] = ib.ListenPort
	}
	if gotPorts["LAT-in"] != 1081 || gotPorts["FIN-in"] != 1080 {
		t.Fatalf("unexpected merge inbound ports: %+v (raw=%s)", gotPorts, string(raw))
	}
	if len(doc.Route.Rules) != 2 {
		t.Fatalf("expected 2 inbound route rules, got %+v", doc.Route.Rules)
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
