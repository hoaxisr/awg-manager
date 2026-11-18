package vlessserver

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAndMigrateLegacy_ValidXHTTP_PreservesFilesAndWritesBackup(t *testing.T) {
	dataDir := t.TempDir()
	xrayDir := filepath.Join(dataDir, "xray")
	secretsDir := filepath.Join(xrayDir, "secrets")
	profilesDir := filepath.Join(xrayDir, "profiles", "prod")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatal(err)
	}

	secretFile := filepath.Join(secretsDir, "token.key")
	secretBytes := []byte("keep-this-secret-intact")
	if err := os.WriteFile(secretFile, secretBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	profileFile := filepath.Join(profilesDir, "metadata.json")
	profileBytes := []byte(`{"id":"prod"}`)
	if err := os.WriteFile(profileFile, profileBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	legacyJSON := []byte(`{
  "inbounds": [
    {
      "tag": "vless-in",
      "listen": "0.0.0.0",
      "port": 8443,
      "protocol": "vless",
      "settings": {
        "clients": [
          {"id": "41b7ef34-6b19-48f3-8d95-82cf4474e109", "email": "ivan-phone"}
        ]
      },
      "streamSettings": {
        "network": "xhttp",
        "security": "none",
        "xhttpSettings": {
          "host": "cdn.example.org",
          "path": "/secret-bridge/",
          "mode": "packet-up",
          "uplinkHTTPMethod": "POST"
        }
      }
    },
    {
      "tag": "timeweb-in",
      "listen": "127.0.0.1",
      "port": 9007,
      "protocol": "vless",
      "settings": {
        "clients": [
          {"id": "41b7ef34-6b19-48f3-8d95-82cf4474e109", "email": "ivan-phone"}
        ]
      },
      "streamSettings": {
        "network": "xhttp",
        "xhttpSettings": {
          "path": "/secret-bridge/"
        }
      }
    }
  ],
  "outbounds": [
    {"protocol": "freedom"}
  ]
}`)
	cfgPath := filepath.Join(xrayDir, "config.json")
	if err := os.WriteFile(cfgPath, legacyJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	disc, err := DiscoverLegacy(cfgPath, "")
	if err != nil {
		t.Fatalf("DiscoverLegacy: %v", err)
	}
	if !disc.Found || disc.Topology != TopologyA {
		t.Fatalf("unexpected discovery result: %+v", disc)
	}
	if disc.PublicDomain != "cdn.example.org" || disc.Path != "/secret-bridge/" || disc.UplinkMethod != "POST" {
		t.Fatalf("unexpected extracted fields: %+v", disc)
	}
	if disc.OutboundMode != "direct" || disc.OutboundSocksPort != 0 {
		t.Fatalf("unexpected discovered egress: mode=%q socks_port=%d", disc.OutboundMode, disc.OutboundSocksPort)
	}
	if len(disc.Clients) != 1 || disc.Clients[0].ID != "41b7ef34-6b19-48f3-8d95-82cf4474e109" || disc.Clients[0].Remark != "ivan-phone" {
		t.Fatalf("unexpected clients: %+v", disc.Clients)
	}

	migrated, err := MigrateLegacy(context.Background(), disc, dataDir, 9008)
	if err != nil {
		t.Fatalf("MigrateLegacy: %v", err)
	}
	if migrated.ListenPort != 9008 || migrated.DispatcherPort != 8443 || len(migrated.Clients) != 1 {
		t.Fatalf("unexpected migrated config: %+v", migrated)
	}
	if migrated.OutboundMode != "direct" || migrated.OutboundSocksPort != 0 {
		t.Fatalf("expected direct egress preserved, got mode=%q socks_port=%d", migrated.OutboundMode, migrated.OutboundSocksPort)
	}

	// Idempotent second call with identical source checksum
	migrated2, err := MigrateLegacy(context.Background(), disc, dataDir, 9008)
	if err != nil {
		t.Fatalf("second idempotent MigrateLegacy failed: %v", err)
	}
	if migrated2.PublicDomain != "cdn.example.org" || migrated2.OutboundMode != "direct" {
		t.Fatalf("unexpected second migrated config: domain=%q mode=%q", migrated2.PublicDomain, migrated2.OutboundMode)
	}

	// Verify backup written and zero legacy files/secrets/profiles deleted or altered
	bakBytes, err := os.ReadFile(filepath.Join(xrayDir, "config.json.bak"))
	if err != nil || !bytes.Equal(bakBytes, legacyJSON) {
		t.Fatalf("expected intact config.json.bak copy, err=%v", err)
	}
	origBytes, err := os.ReadFile(cfgPath)
	if err != nil || !bytes.Equal(origBytes, legacyJSON) {
		t.Fatalf("expected original config.json untouched, err=%v", err)
	}
	gotSecret, err := os.ReadFile(secretFile)
	if err != nil || !bytes.Equal(gotSecret, secretBytes) {
		t.Fatalf("secret file modified or deleted: err=%v", err)
	}
	gotProfile, err := os.ReadFile(profileFile)
	if err != nil || !bytes.Equal(gotProfile, profileBytes) {
		t.Fatalf("profile file modified or deleted: err=%v", err)
	}
}

func TestAutoImportOnNewService_WhenSettingsMissing(t *testing.T) {
	dataDir := t.TempDir()
	xrayDir := filepath.Join(dataDir, "xray")
	if err := os.MkdirAll(xrayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyJSON := []byte(`{
  "inbounds": [{
    "listen": "127.0.0.1",
    "port": 9008,
    "protocol": "vless",
    "settings": {"clients": [{"id": "11111111-2222-4333-8444-555555555555", "email": "dacha"}]},
    "streamSettings": {"network": "xhttp", "xhttpSettings": {"host": "home.keenetic.pro", "path": "/vless"}}
  }],
  "outbounds": [{"protocol": "freedom"}]
}`)
	if err := os.WriteFile(filepath.Join(xrayDir, "config.json"), legacyJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	svc := New(dataDir, nil)
	if svc.IsRecoveryRequired() {
		_, reason, _ := svc.RecoveryInfo()
		t.Fatalf("unexpected recoveryRequired on valid legacy config: %s", reason)
	}
	cfg := svc.GetConfig()
	if cfg.PublicDomain != "home.keenetic.pro" || len(cfg.Clients) != 1 || cfg.Clients[0].ID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("auto-import config mismatch: %+v", cfg)
	}
	if cfg.OutboundMode != "direct" || cfg.OutboundSocksPort != 0 {
		t.Fatalf("expected auto-imported freedom outbound to remain direct (port 0), got mode=%q port=%d", cfg.OutboundMode, cfg.OutboundSocksPort)
	}

	// Reload persisted xray-server-settings.json and verify direct egress does not revert to socks:1099
	reloaded := New(dataDir, nil).GetConfig()
	if reloaded.OutboundMode != "direct" || reloaded.OutboundSocksPort != 0 {
		t.Fatalf("expected reloaded settings to preserve direct egress, got mode=%q port=%d", reloaded.OutboundMode, reloaded.OutboundSocksPort)
	}
}

func TestDiscoverAndMigrateLegacy_SupportedEgressAndPlainIngress(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantTopo  LegacyTopology
		wantMode  string
		wantPort  int
		wantIface string
	}{
		{
			name: "plain_xhttp_behind_external_ingress_with_freedom",
			raw: `{
				"inbounds": [{
					"listen": "127.0.0.1", "port": 9008, "protocol": "vless",
					"settings": {"clients": [{"id": "00000000-0000-4000-8000-000000000001"}]},
					"streamSettings": {
						"network": "xhttp",
						"security": "",
						"xhttpSettings": {"host": "edge.cdn.example.com", "path": "/bridge/"}
					}
				}],
				"outbounds": [{"protocol": "freedom", "settings": {"domainStrategy": "UseIPv4"}}]
			}`,
			wantTopo: TopologyB,
			wantMode: "direct",
			wantPort: 0,
		},
		{
			name: "direct_protocol_alias",
			raw: `{
				"inbounds": [{
					"listen": "0.0.0.0", "port": 8080, "protocol": "vless",
					"settings": {"clients": [{"id": "00000000-0000-4000-8000-000000000002"}]},
					"streamSettings": {"network": "xhttp", "security": "none", "xhttpSettings": {"path": "/p/"}}
				}],
				"outbounds": [{"protocol": "direct"}]
			}`,
			wantTopo: TopologyA,
			wantMode: "direct",
			wantPort: 0,
		},
		{
			name: "freedom_with_sockopt_interface",
			raw: `{
				"inbounds": [{
					"listen": "127.0.0.1", "port": 9008, "protocol": "vless",
					"settings": {"clients": [{"id": "00000000-0000-4000-8000-000000000003"}]},
					"streamSettings": {"network": "xhttp", "security": "none", "xhttpSettings": {"path": "/if/"}}
				}],
				"outbounds": [{
					"protocol": "freedom",
					"streamSettings": {"sockopt": {"interface": "nwg1"}}
				}]
			}`,
			wantTopo:  TopologyB,
			wantMode:  "interface",
			wantPort:  0,
			wantIface: "nwg1",
		},
		{
			name: "supported_local_socks5_servers_array",
			raw: `{
				"inbounds": [{
					"listen": "127.0.0.1", "port": 9008, "protocol": "vless",
					"settings": {"clients": [{"id": "00000000-0000-4000-8000-000000000004"}]},
					"streamSettings": {"network": "xhttp", "security": "none", "xhttpSettings": {"path": "/s5/"}}
				}],
				"outbounds": [{
					"protocol": "socks",
					"settings": {"servers": [{"address": "127.0.0.1", "port": 10808}]}
				}]
			}`,
			wantTopo: TopologyB,
			wantMode: "socks",
			wantPort: 10808,
		},
		{
			name: "supported_localhost_socks5_flat_settings",
			raw: `{
				"inbounds": [{
					"listen": "127.0.0.1", "port": 9008, "protocol": "vless",
					"settings": {"clients": [{"id": "00000000-0000-4000-8000-000000000005"}]},
					"streamSettings": {"network": "xhttp", "security": "none", "xhttpSettings": {"path": "/s5b/"}}
				}],
				"outbounds": [{
					"protocol": "socks",
					"settings": {"address": "localhost", "port": 1099}
				}]
			}`,
			wantTopo: TopologyB,
			wantMode: "socks",
			wantPort: 1099,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			cfgPath := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(cfgPath, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			disc, err := DiscoverLegacy(cfgPath, "")
			if err != nil {
				t.Fatalf("DiscoverLegacy: %v", err)
			}
			if disc.Topology != tc.wantTopo {
				t.Fatalf("Topology = %q, want %q (conflict=%q)", disc.Topology, tc.wantTopo, disc.ConflictReason)
			}
			if disc.OutboundMode != tc.wantMode || disc.OutboundSocksPort != tc.wantPort || disc.OutboundInterface != tc.wantIface {
				t.Fatalf("discovered egress = (%q, %d, %q), want (%q, %d, %q)",
					disc.OutboundMode, disc.OutboundSocksPort, disc.OutboundInterface,
					tc.wantMode, tc.wantPort, tc.wantIface)
			}
			got, err := MigrateLegacy(context.Background(), disc, dataDir, 9008)
			if err != nil {
				t.Fatalf("MigrateLegacy: %v", err)
			}
			if got.OutboundMode != tc.wantMode || got.OutboundSocksPort != tc.wantPort || got.OutboundInterface != tc.wantIface {
				t.Fatalf("migrated egress = (%q, %d, %q), want (%q, %d, %q)",
					got.OutboundMode, got.OutboundSocksPort, got.OutboundInterface,
					tc.wantMode, tc.wantPort, tc.wantIface)
			}
		})
	}
}

func TestDiscoverAndMigrateLegacy_UnsupportedConfigsFailExplainablyAndPreserveData(t *testing.T) {
	cases := []struct {
		name           string
		raw            string
		wantReasonPart string
	}{
		{
			name:           "corrupt_json",
			raw:            `{"inbounds": [ broken`,
			wantReasonPart: "malformed legacy JSON",
		},
		{
			name: "vmess_protocol",
			raw: `{
				"inbounds": [{"listen":"0.0.0.0","port":443,"protocol":"vmess","streamSettings":{"network":"ws"}}],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "unsupported legacy protocol",
		},
		{
			name: "trojan_protocol",
			raw: `{
				"inbounds": [{"listen":"0.0.0.0","port":443,"protocol":"trojan","streamSettings":{"network":"tcp"}}],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "unsupported legacy protocol",
		},
		{
			name: "shadowsocks_protocol",
			raw: `{
				"inbounds": [{"listen":"0.0.0.0","port":8388,"protocol":"shadowsocks"}],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "unsupported legacy protocol",
		},
		{
			name: "vless_reality",
			raw: `{
				"inbounds": [{
					"listen":"0.0.0.0","port":443,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"xhttp","security":"reality","realitySettings":{"dest":"yahoo.com:443"}}
				}],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "reality",
		},
		{
			name: "vless_inbound_tls_with_certificates",
			raw: `{
				"inbounds": [{
					"listen":"0.0.0.0","port":443,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{
						"network":"xhttp",
						"security":"tls",
						"tlsSettings":{"certificates":[{"certificateFile":"/opt/etc/xray/cert.pem","keyFile":"/opt/etc/xray/key.pem"}]},
						"xhttpSettings":{"host":"cdn.example.org","path":"/test/"}
					}
				}],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "tls termination",
		},
		{
			name: "vless_inbound_tlssettings_without_security_field",
			raw: `{
				"inbounds": [{
					"listen":"0.0.0.0","port":443,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{
						"network":"xhttp",
						"security":"none",
						"tlsSettings":{"certificates":[{"certificateFile":"/cert.pem","keyFile":"/key.pem"}]},
						"xhttpSettings":{"path":"/test/"}
					}
				}],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "tls termination",
		},
		{
			name: "vless_unknown_security",
			raw: `{
				"inbounds": [{
					"listen":"127.0.0.1","port":9008,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"xhttp","security":"mtls-custom","xhttpSettings":{"path":"/test/"}}
				}],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "unsupported legacy stream security",
		},
		{
			name: "vless_ws",
			raw: `{
				"inbounds": [{
					"listen":"127.0.0.1","port":9008,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"ws"}
				}],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "unsupported legacy transport \"ws\"",
		},
		{
			name: "vless_grpc",
			raw: `{
				"inbounds": [{
					"listen":"127.0.0.1","port":9008,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"grpc"}
				}],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "unsupported legacy transport \"grpc\"",
		},
		{
			name: "vless_tcp_vision",
			raw: `{
				"inbounds": [{
					"listen":"0.0.0.0","port":443,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109","flow":"xtls-rprx-vision"}]},
					"streamSettings":{"network":"xhttp"}
				}],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "flow",
		},
		{
			name: "ambiguous_multi_inbound",
			raw: `{
				"inbounds": [
					{"tag":"in-1","listen":"127.0.0.1","port":9008,"protocol":"vless","streamSettings":{"network":"xhttp"}},
					{"tag":"in-2","listen":"127.0.0.1","port":9010,"protocol":"vless","streamSettings":{"network":"xhttp"}}
				],
				"outbounds": [{"protocol":"freedom"}]
			}`,
			wantReasonPart: "multiple VLESS inbounds",
		},
		{
			name: "multiple_outbounds_with_routing",
			raw: `{
				"inbounds": [{
					"listen":"127.0.0.1","port":9008,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"xhttp","security":"none","xhttpSettings":{"path":"/test/"}}
				}],
				"outbounds": [
					{"tag":"direct","protocol":"freedom"},
					{"tag":"proxy","protocol":"socks","settings":{"servers":[{"address":"127.0.0.1","port":1099}]}}
				],
				"routing": {
					"rules": [{"type":"field","domain":["geosite:category-ads"],"outboundTag":"proxy"}]
				}
			}`,
			wantReasonPart: "routing",
		},
		{
			name: "multiple_outbounds_without_routing",
			raw: `{
				"inbounds": [{
					"listen":"127.0.0.1","port":9008,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"xhttp","security":"none","xhttpSettings":{"path":"/test/"}}
				}],
				"outbounds": [
					{"tag":"direct","protocol":"freedom"},
					{"tag":"block","protocol":"blackhole"}
				]
			}`,
			wantReasonPart: "multiple legacy outbounds",
		},
		{
			name: "unsupported_outbound_protocol_wireguard",
			raw: `{
				"inbounds": [{
					"listen":"127.0.0.1","port":9008,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"xhttp","security":"none","xhttpSettings":{"path":"/test/"}}
				}],
				"outbounds": [{"protocol":"wireguard","settings":{"secretKey":"abc"}}]
			}`,
			wantReasonPart: "unsupported legacy outbound protocol",
		},
		{
			name: "unsupported_remote_socks_outbound",
			raw: `{
				"inbounds": [{
					"listen":"127.0.0.1","port":9008,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"xhttp","security":"none","xhttpSettings":{"path":"/test/"}}
				}],
				"outbounds": [{"protocol":"socks","settings":{"servers":[{"address":"10.0.0.2","port":1080}]}}]
			}`,
			wantReasonPart: "non-loopback legacy socks address",
		},
		{
			name: "unsupported_authenticated_socks_outbound",
			raw: `{
				"inbounds": [{
					"listen":"127.0.0.1","port":9008,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"xhttp","security":"none","xhttpSettings":{"path":"/test/"}}
				}],
				"outbounds": [{"protocol":"socks","settings":{"servers":[{"address":"127.0.0.1","port":1080,"users":[{"user":"u","pass":"p"}]}]}}]
			}`,
			wantReasonPart: "authenticated legacy socks",
		},
		{
			name: "insufficient_data_missing_outbounds",
			raw: `{
				"inbounds": [{
					"listen":"127.0.0.1","port":9008,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"xhttp","security":"none","xhttpSettings":{"path":"/test/"}}
				}]
			}`,
			wantReasonPart: "missing or empty outbounds",
		},
		{
			name: "insufficient_data_socks_without_port",
			raw: `{
				"inbounds": [{
					"listen":"127.0.0.1","port":9008,"protocol":"vless",
					"settings":{"clients":[{"id":"41b7ef34-6b19-48f3-8d95-82cf4474e109"}]},
					"streamSettings":{"network":"xhttp","security":"none","xhttpSettings":{"path":"/test/"}}
				}],
				"outbounds": [{"protocol":"socks","settings":{}}]
			}`,
			wantReasonPart: "missing server address and port",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			xrayDir := filepath.Join(dataDir, "xray")
			if err := os.MkdirAll(xrayDir, 0o700); err != nil {
				t.Fatal(err)
			}
			cfgPath := filepath.Join(xrayDir, "config.json")
			rawBytes := []byte(tc.raw)
			if err := os.WriteFile(cfgPath, rawBytes, 0o600); err != nil {
				t.Fatal(err)
			}

			disc, err := DiscoverLegacy(cfgPath, "")
			if err != nil {
				t.Fatalf("DiscoverLegacy error: %v", err)
			}
			if disc.Topology != TopologyC {
				t.Fatalf("Topology = %q, want %q", disc.Topology, TopologyC)
			}
			if !strings.Contains(strings.ToLower(disc.ConflictReason), strings.ToLower(tc.wantReasonPart)) {
				t.Fatalf("ConflictReason = %q, want substring %q", disc.ConflictReason, tc.wantReasonPart)
			}

			if _, err := MigrateLegacy(context.Background(), disc, dataDir, 9008); !errors.Is(err, ErrMigrationConflict) {
				t.Fatalf("MigrateLegacy error = %v, want ErrMigrationConflict", err)
			}
			markerPath := filepath.Join(xrayDir, "migration-marker.json")
			settingsPath := filepath.Join(xrayDir, "xray-server-settings.json")
			if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
				t.Fatalf("expected no migration-marker.json after refused MigrateLegacy, stat err=%v", err)
			}
			if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
				t.Fatalf("expected no xray-server-settings.json after refused MigrateLegacy, stat err=%v", err)
			}
			afterDirectRefusal, err := os.ReadFile(cfgPath)
			if err != nil || !bytes.Equal(afterDirectRefusal, rawBytes) {
				t.Fatalf("legacy config.json altered after refused MigrateLegacy")
			}

			// Service startup on unsupported legacy config marks RecoveryRequired, writes no marker/settings, and keeps original intact
			svc := New(dataDir, nil)
			req, reason, _ := svc.RecoveryInfo()
			if !req || !strings.Contains(strings.ToLower(reason), strings.ToLower(tc.wantReasonPart)) {
				t.Fatalf("expected RecoveryRequired with %q, got req=%v reason=%q", tc.wantReasonPart, req, reason)
			}
			if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
				t.Fatalf("expected no migration-marker.json after Service startup refusal, stat err=%v", err)
			}
			if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
				t.Fatalf("expected no xray-server-settings.json after Service startup refusal, stat err=%v", err)
			}
			afterBytes, err := os.ReadFile(cfgPath)
			if err != nil || !bytes.Equal(afterBytes, rawBytes) {
				t.Fatalf("legacy config.json was altered or deleted!")
			}
			bakBytes, err := os.ReadFile(filepath.Join(xrayDir, "config.json.bak"))
			if err != nil || !bytes.Equal(bakBytes, rawBytes) {
				t.Fatalf("expected non-destructive config.json.bak backup")
			}
		})
	}
}
