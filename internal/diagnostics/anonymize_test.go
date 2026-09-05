package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

func TestAnonymize_MACinWANIPAddr(t *testing.T) {
	report := Report{
		WAN: WANInfo{
			IPAddr: "11: eth3: <BROADCAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP group default qlen 1000\n    link/ether a0:21:aa:be:40:58 brd ff:ff:ff:ff:ff:ff\n",
		},
	}

	anonymize(&report)

	if strings.Contains(report.WAN.IPAddr, "a0:21:aa:be:40:58") {
		t.Fatalf("real MAC still present in IPAddr:\n%s", report.WAN.IPAddr)
	}
	if !strings.Contains(report.WAN.IPAddr, "a0:21:**:**:**:58") {
		t.Fatalf("masked MAC not found in IPAddr:\n%s", report.WAN.IPAddr)
	}
	if strings.Contains(report.WAN.IPAddr, "ff:ff:ff:ff:ff:ff") {
		t.Fatalf("broadcast MAC still present in IPAddr:\n%s", report.WAN.IPAddr)
	}
	if !strings.Contains(report.WAN.IPAddr, "ff:ff:**:**:**:ff") {
		t.Fatalf("masked broadcast MAC not found in IPAddr:\n%s", report.WAN.IPAddr)
	}
}

func TestAnonymize_WGPublicKeyInNDMSRawOutput(t *testing.T) {
	raw := `{"wireguard":{"public-key":"f43p1w9IIpUvqe9m1IrUcLbVrMfgrXRgShPEu7X78Ec="}}`
	report := Report{
		Tunnels: []TunnelInfo{
			{
				Interface: IfaceInfo{
					NDMSState: "",
				},
				Connection: ConnectionInfo{
					RawOutput: raw,
				},
			},
		},
	}

	anonymize(&report)

	result := report.Tunnels[0].Connection.RawOutput
	if strings.Contains(result, "f43p1w9IIpUvqe9m1IrUcLbVrMfgrXRgShPEu7X78Ec=") {
		t.Fatalf("real WG public key still present:\n%s", result)
	}
	// maskWGKey keeps first 6 and last 6 chars, with 4 * in-between.
	// key[:6]="f43p1w", key[-6:]="X78Ec=", so expected masked = "f43p1w****X78Ec=".
	if !strings.Contains(result, "f43p1w****") {
		t.Fatalf("masked WG key prefix not found:\n%s", result)
	}
	if !strings.Contains(result, "X78Ec=") {
		t.Fatalf("masked WG key suffix not found:\n%s", result)
	}
}

func TestAnonymize_MACDedupAcrossFields(t *testing.T) {
	expectedMask := "a0:21:**:**:**:58" // maskMAC("a0:21:aa:be:40:58")

	report := Report{
		WAN: WANInfo{
			IPAddr: "link/ether a0:21:aa:be:40:58\n",
		},
		Tests: []TestResult{
			{
				Name:   "system",
				Detail: "detected mac a0:21:aa:be:40:58 on eth3",
			},
		},
	}

	anonymize(&report)

	if !strings.Contains(report.WAN.IPAddr, expectedMask) {
		t.Fatalf("expected masked MAC %q not found in WAN.IPAddr:\n%s", expectedMask, report.WAN.IPAddr)
	}
	if !strings.Contains(report.Tests[0].Detail, expectedMask) {
		t.Fatalf("expected masked MAC %q not found in Tests[0].Detail:\n%s", expectedMask, report.Tests[0].Detail)
	}
	// Both occurrences must carry the identical mask string.
	wanHasMask := strings.Contains(report.WAN.IPAddr, expectedMask)
	testHasMask := strings.Contains(report.Tests[0].Detail, expectedMask)
	if !wanHasMask || !testHasMask {
		t.Fatalf("same MAC not masked identically: wan=%v test=%v", wanHasMask, testHasMask)
	}
}

func TestAnonymize_PublicIPInTestDetail(t *testing.T) {
	report := Report{
		Tests: []TestResult{
			{
				Name:   "tunnel_connectivity",
				Detail: "IP: 95.25.93.179 (via https://ifconfig.me)",
			},
		},
	}

	anonymize(&report)

	if strings.Contains(report.Tests[0].Detail, "95.25.93.179") {
		t.Fatalf("public IP still present: %s", report.Tests[0].Detail)
	}
	if !strings.Contains(report.Tests[0].Detail, "PUBLIC-IP-") {
		t.Fatalf("public IP alias not found: %s", report.Tests[0].Detail)
	}
}

func TestAnonymize_DoesNotAliasDefaultRouteMarkers(t *testing.T) {
	report := Report{
		Tunnels: []TunnelInfo{
			{
				ConfigFile: "AllowedIPs = 0.0.0.0/0, ::/0\n",
			},
		},
		WAN: WANInfo{
			IPAddr: "inet6 fe80::a221:aaff:febe:4058/64 scope link\n",
		},
	}

	anonymize(&report)

	if !strings.Contains(report.Tunnels[0].ConfigFile, "0.0.0.0/0") {
		t.Fatalf("0.0.0.0/0 was unexpectedly anonymized: %s", report.Tunnels[0].ConfigFile)
	}
	if !strings.Contains(report.Tunnels[0].ConfigFile, "::/0") {
		t.Fatalf("::/0 was unexpectedly anonymized: %s", report.Tunnels[0].ConfigFile)
	}
	if strings.Contains(report.WAN.IPAddr, "PUBLIC-IP") {
		t.Fatalf("link-local IPv6 was corrupted by anonymizer: %s", report.WAN.IPAddr)
	}
	if !strings.Contains(report.WAN.IPAddr, "fe80::a221:aaff:febe:4058/64") {
		t.Fatalf("link-local IPv6 changed unexpectedly: %s", report.WAN.IPAddr)
	}
}

func TestAnonymize_PublicIPsInStructuredTunnelSettings(t *testing.T) {
	report := Report{
		Tunnels: []TunnelInfo{
			{
				Settings: TunnelSettings{
					DNS: "172.29.172.254, 1.0.0.1",
					PingCheckConfig: &PingCheckConfig{
						Enabled: true,
						Method:  "icmp",
						Target:  "8.8.8.8",
					},
				},
				Routes: RouteInfo{
					EndpointRoute: "95.25.93.179 via 192.168.1.1 dev eth3",
				},
			},
		},
	}

	anonymize(&report)

	out := report.Tunnels[0].Settings.DNS + " " +
		report.Tunnels[0].Settings.PingCheckConfig.Target + " " +
		report.Tunnels[0].Routes.EndpointRoute

	if strings.Contains(out, "1.0.0.1") {
		t.Fatalf("public DNS IP still present: %s", out)
	}
	if strings.Contains(out, "8.8.8.8") {
		t.Fatalf("ping target public IP still present: %s", out)
	}
	if strings.Contains(out, "95.25.93.179") {
		t.Fatalf("route public IP still present: %s", out)
	}
	if !strings.Contains(out, "PUBLIC-IP-") {
		t.Fatalf("public IP aliases not found: %s", out)
	}
	if !strings.Contains(out, "172.29.172.254") {
		t.Fatalf("private IP should be preserved: %s", out)
	}
}

// Endpoint сервера из .conf туннеля — публичный адрес пользователя-провайдера,
// он обязан уйти под алиас ENDPOINT-N, ключ пира — под PUBKEY-N. Мутант
// «registerEndpoint не регистрирует» проходил: тесты проверяли публичные IP в
// DNS/ping/route, а поле Endpoint конфига — нет.
func TestAnonymize_TunnelEndpointAndPeerKeyAliased(t *testing.T) {
	const endpointIP = "203.0.113.7"
	const peerKey = "u9F1nT0m2Y5x8Q4v7W3k6L1p0R9s2D5f8G1h4J7K0M="
	report := Report{
		Tunnels: []TunnelInfo{{
			ConfigFile: "[Interface]\nPrivateKey = [REDACTED]\nAddress = 10.8.0.2/32\n\n" +
				"[Peer]\nPublicKey = " + peerKey + "\nEndpoint = " + endpointIP + ":51820\nAllowedIPs = 0.0.0.0/0\n",
		}},
	}

	anonymize(&report)

	out := report.Tunnels[0].ConfigFile
	if strings.Contains(out, endpointIP) {
		t.Fatalf("endpoint IP остался в конфиге:\n%s", out)
	}
	if !strings.Contains(out, "Endpoint = ENDPOINT-1:51820") {
		t.Fatalf("endpoint не под алиасом ENDPOINT-1:\n%s", out)
	}
	if strings.Contains(out, peerKey) {
		t.Fatalf("ключ пира остался в конфиге:\n%s", out)
	}
	if !strings.Contains(out, "PublicKey = PUBKEY-1") {
		t.Fatalf("ключ пира не под алиасом PUBKEY-1:\n%s", out)
	}
	if !strings.Contains(out, "Address = 10.8.0.2/32") {
		t.Fatalf("приватный адрес интерфейса обязан сохраниться:\n%s", out)
	}
}

func TestAnonymize_HostnamesInJournalWarnings(t *testing.T) {
	report := Report{
		JournalWarnings: &JournalWarningsInfo{
			Levels:         []string{"error", "warn"},
			LimitPerBucket: 300,
			AWGM: JournalWarningBucket{
				Bucket: "app",
				Entries: []logging.LogEntry{
					{
						Level:   "warn",
						Group:   "system",
						Target:  "connectivitycheck.gstatic.com:443",
						Message: "connectivity check failed for connectivitycheck.gstatic.com:443",
					},
				},
			},
			Singbox: JournalWarningBucket{
				Bucket: "singbox",
				Entries: []logging.LogEntry{
					{
						Level:    "error",
						Group:    "singbox",
						Subgroup: "outbound",
						Target:   "example-vless.example.test",
						Message:  "dial tcp example-vless.example.test:443 with sni real-sni.example.test failed",
					},
				},
			},
		},
	}

	anonymize(&report)

	awgm := report.JournalWarnings.AWGM.Entries[0].Target + " " +
		report.JournalWarnings.AWGM.Entries[0].Message
	sb := report.JournalWarnings.Singbox.Entries[0].Target + " " +
		report.JournalWarnings.Singbox.Entries[0].Message
	out := awgm + " " + sb

	for _, raw := range []string{
		"connectivitycheck.gstatic.com",
		"example-vless.example.test",
		"real-sni.example.test",
	} {
		if strings.Contains(out, raw) {
			t.Fatalf("raw hostname %q still present after anonymize:\n%s", raw, out)
		}
	}

	if !strings.Contains(out, "HOST-") {
		t.Fatalf("HOST-* aliases not found after anonymize:\n%s", out)
	}
	if !strings.Contains(out, ":443") {
		t.Fatalf("port should be preserved while hostname is anonymized:\n%s", out)
	}
}

func TestAnonymize_HostnameAliasesAreStableAcrossJournalWarningBuckets(t *testing.T) {
	report := Report{
		JournalWarnings: &JournalWarningsInfo{
			Levels:         []string{"error", "warn"},
			LimitPerBucket: 300,
			AWGM: JournalWarningBucket{
				Bucket: "awgm",
				Entries: []logging.LogEntry{
					{
						Target:  "connectivitycheck.gstatic.com",
						Message: "app log mentions connectivitycheck.gstatic.com",
					},
				},
			},
			Singbox: JournalWarningBucket{
				Bucket: "singbox",
				Entries: []logging.LogEntry{
					{
						Target:  "connectivitycheck.gstatic.com:443",
						Message: "sing-box warning for connectivitycheck.gstatic.com:443",
					},
				},
			},
		},
	}

	anonymize(&report)

	oldLog := report.JournalWarnings.AWGM.Entries[0].Target + " " +
		report.JournalWarnings.AWGM.Entries[0].Message
	newLog := report.JournalWarnings.Singbox.Entries[0].Target + " " +
		report.JournalWarnings.Singbox.Entries[0].Message
	out := oldLog + " " + newLog

	if strings.Contains(out, "connectivitycheck.gstatic.com") {
		t.Fatalf("raw hostname still present:\n%s", out)
	}

	if !strings.Contains(oldLog, "HOST-1") {
		t.Fatalf("expected HOST-1 in awgm bucket, got:\n%s", oldLog)
	}
	if !strings.Contains(newLog, "HOST-1") {
		t.Fatalf("expected same HOST-1 alias in singbox bucket, got:\n%s", newLog)
	}
}

func TestAnonymize_HostnameAliasesAreCaseInsensitive(t *testing.T) {
	report := Report{
		JournalWarnings: &JournalWarningsInfo{
			Levels:         []string{"error", "warn"},
			LimitPerBucket: 300,
			Singbox: JournalWarningBucket{
				Bucket: "singbox",
				Entries: []logging.LogEntry{
					{
						Target:  "Example-VLESS.Example.Test:443",
						Message: "dial example-vless.example.test:443 failed",
					},
				},
			},
		},
	}

	anonymize(&report)

	target := report.JournalWarnings.Singbox.Entries[0].Target
	message := report.JournalWarnings.Singbox.Entries[0].Message
	out := target + " " + message

	if strings.Contains(out, "Example-VLESS.Example.Test") ||
		strings.Contains(out, "example-vless.example.test") {
		t.Fatalf("raw hostname spelling still present:\n%s", out)
	}

	if !strings.Contains(target, "HOST-1:443") {
		t.Fatalf("expected HOST-1 in target, got:\n%s", target)
	}
	if !strings.Contains(message, "HOST-1:443") {
		t.Fatalf("expected same HOST-1 alias in message, got:\n%s", message)
	}
}

func TestAnonymize_HostnamePatternDoesNotAliasVersionsOrIPsAsHosts(t *testing.T) {
	report := Report{
		JournalWarnings: &JournalWarningsInfo{
			Levels:         []string{"error", "warn"},
			LimitPerBucket: 300,
			AWGM: JournalWarningBucket{
				Bucket: "app",
				Entries: []logging.LogEntry{
					{
						Target:  "system",
						Message: "sing-box 1.12.0 failed via 8.8.8.8 from 192.168.1.1 on Wireguard0 tun_abc123",
					},
				},
			},
		},
	}

	anonymize(&report)

	out := report.JournalWarnings.AWGM.Entries[0].Target + " " +
		report.JournalWarnings.AWGM.Entries[0].Message

	if strings.Contains(out, "HOST-") {
		t.Fatalf("versions/IPs/local names must not be anonymized as HOST-*:\n%s", out)
	}
	if !strings.Contains(out, "1.12.0") {
		t.Fatalf("version should be preserved, got:\n%s", out)
	}
	if strings.Contains(out, "8.8.8.8") {
		t.Fatalf("public IP should still be anonymized by IP logic, got:\n%s", out)
	}
	if !strings.Contains(out, "PUBLIC-IP-") {
		t.Fatalf("public IP alias not found, got:\n%s", out)
	}
	if !strings.Contains(out, "192.168.1.1") {
		t.Fatalf("private IP should be preserved, got:\n%s", out)
	}
}

func TestAnonymize_HostnamesInTestsAndTunnelSettings(t *testing.T) {
	report := Report{
		Tunnels: []TunnelInfo{
			{
				ConfigFile: "Endpoint = vpn.example.test:443\n",
				Settings: TunnelSettings{
					DNS: "dns.example.test, 1.1.1.1",
					PingCheckConfig: &PingCheckConfig{
						Enabled: true,
						Method:  "uri",
						Target:  "connectivitycheck.gstatic.com",
					},
				},
				Routes: RouteInfo{
					EndpointRoute: "vpn.example.test via 192.168.1.1 dev eth3",
				},
			},
		},
		Tests: []TestResult{
			{
				Name:        "tunnel_connectivity",
				Description: "checks ifconfig.me through tunnel",
				Detail:      "request to ifconfig.me failed for vpn.example.test",
			},
		},
	}

	anonymize(&report)

	out := report.Tunnels[0].ConfigFile + " " +
		report.Tunnels[0].Settings.DNS + " " +
		report.Tunnels[0].Settings.PingCheckConfig.Target + " " +
		report.Tunnels[0].Routes.EndpointRoute + " " +
		report.Tests[0].Description + " " +
		report.Tests[0].Detail

	for _, raw := range []string{
		"vpn.example.test",
		"dns.example.test",
		"connectivitycheck.gstatic.com",
		"ifconfig.me",
	} {
		if strings.Contains(out, raw) {
			t.Fatalf("raw hostname %q still present after anonymize:\n%s", raw, out)
		}
	}

	if !strings.Contains(out, "HOST-") {
		t.Fatalf("HOST-* aliases not found:\n%s", out)
	}
	if strings.Contains(out, "1.1.1.1") {
		t.Fatalf("public DNS IP should still be anonymized:\n%s", out)
	}
	if !strings.Contains(out, "PUBLIC-IP-") {
		t.Fatalf("public IP alias not found:\n%s", out)
	}
	if !strings.Contains(out, "192.168.1.1") {
		t.Fatalf("private IP should be preserved:\n%s", out)
	}
}

func TestAnonymize_HostnamesInWANAndAWGProxyModule(t *testing.T) {
	report := Report{
		WAN: WANInfo{
			NDMSRouteTable: "route to wan-check.example.test via 95.25.93.179",
			IPRouteTable:   "default via isp-gw.example.test dev eth3",
			IPAddr:         "inet 95.25.93.179 peer isp.example.test",
		},
		AWGProxyModule: AWGProxyModule{
			RawList: "endpoint proxy-endpoint.example.test:443 via 95.25.93.179",
			DmesgLines: []string{
				"awg-proxy failed to resolve proxy-endpoint.example.test",
			},
		},
	}

	anonymize(&report)

	out := report.WAN.NDMSRouteTable + " " +
		report.WAN.IPRouteTable + " " +
		report.WAN.IPAddr + " " +
		report.AWGProxyModule.RawList + " " +
		strings.Join(report.AWGProxyModule.DmesgLines, " ")

	for _, raw := range []string{
		"wan-check.example.test",
		"isp-gw.example.test",
		"isp.example.test",
		"proxy-endpoint.example.test",
	} {
		if strings.Contains(out, raw) {
			t.Fatalf("raw hostname %q still present after anonymize:\n%s", raw, out)
		}
	}

	if !strings.Contains(out, "HOST-") {
		t.Fatalf("HOST-* aliases not found:\n%s", out)
	}
	if strings.Contains(out, "95.25.93.179") {
		t.Fatalf("public IP should still be anonymized:\n%s", out)
	}
	if !strings.Contains(out, "PUBLIC-IP-") {
		t.Fatalf("public IP alias not found:\n%s", out)
	}
}

func TestAnonymize_HostnamePatternAnonymizesMultiLabelHostsWithTechnicalSuffix(t *testing.T) {
	report := Report{
		JournalWarnings: &JournalWarningsInfo{
			Singbox: JournalWarningBucket{
				Entries: []logging.LogEntry{{
					Message: "dial proxy.example.service and api.example.json failed",
				}},
			},
		},
	}

	anonymize(&report)

	out := report.JournalWarnings.Singbox.Entries[0].Message
	if strings.Contains(out, "proxy.example.service") || strings.Contains(out, "api.example.json") {
		t.Fatalf("multi-label real hostnames with technical-looking suffix must be anonymized: %s", out)
	}
	if !strings.Contains(out, "HOST-") {
		t.Fatalf("HOST-* aliases not found: %s", out)
	}
}

func TestAnonymize_HostnamePatternPreservesTwoSegmentTechnicalNames(t *testing.T) {
	report := Report{
		JournalWarnings: &JournalWarningsInfo{
			Singbox: JournalWarningBucket{
				Entries: []logging.LogEntry{{
					Message: "sing-box.service: /etc/resolv.conf and config.json missing; see package.json and awg-manager.log",
				}},
			},
		},
	}

	anonymize(&report)

	out := report.JournalWarnings.Singbox.Entries[0].Message
	for _, raw := range []string{
		"sing-box.service",
		"resolv.conf",
		"config.json",
		"package.json",
		"awg-manager.log",
	} {
		if !strings.Contains(out, raw) {
			t.Fatalf("two-segment technical dotted name %q must be preserved, got:\n%s", raw, out)
		}
	}

	if strings.Contains(out, "HOST-") {
		t.Fatalf("two-segment technical names must not be replaced with HOST-*: %s", out)
	}
}

func TestAnonymize_SingboxConfigPreservesTechnicalFilenamesInPathsAndURLs(t *testing.T) {
	report := Report{
		SingboxConfig: &SingboxConfigInfo{
			Available: true,
			Config: map[string]any{
				"experimental": map[string]any{
					"cache_file": map[string]any{
						"enabled": true,
						"path":    "/opt/etc/awg-manager/singbox/cache.db",
					},
				},
				"route": map[string]any{
					"rule_set": []any{
						map[string]any{
							"type": "remote",
							"url":  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-youtube.srs",
						},
						map[string]any{
							"type": "remote",
							"url":  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ai-!cn.srs",
						},
					},
					"rules": []any{
						map[string]any{
							"domain_suffix": []any{
								"sensitive.example.test",
							},
						},
					},
				},
			},
		},
	}

	anonymize(&report)

	raw, err := json.Marshal(report.SingboxConfig.Config)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)

	for _, expected := range []string{
		"cache.db",
		"geosite-youtube.srs",
		"geosite-category-ai-!cn.srs",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("technical filename %q should be preserved, got:\n%s", expected, out)
		}
	}

	for _, rawHost := range []string{
		"raw.githubusercontent.com",
		"sensitive.example.test",
	} {
		if strings.Contains(out, rawHost) {
			t.Fatalf("real hostname %q should be anonymized, got:\n%s", rawHost, out)
		}
	}

	if !strings.Contains(out, "HOST-") {
		t.Fatalf("HOST-* aliases not found, got:\n%s", out)
	}
}
