package firc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/fpclient"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "check" {
		if os.Getenv("AWGM_TEST_SB_CHECK_PASS") == "1" {
			os.Exit(0)
		}
		_, _ = os.Stderr.WriteString("FATAL[0000] decode config: invalid sing-box candidate configuration\n")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestMigrateFromSingboxConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	sbDir := filepath.Join(tmpDir, "singbox", "config.d")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sbDir: %v", err)
	}

	// 1. Write 15-awg.json
	awgJSON := `{
  "outbounds": [
    {
      "type": "direct",
      "tag": "awg-nwg0",
      "bind_interface": "nwg0"
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(sbDir, "15-awg.json"), []byte(awgJSON), 0644); err != nil {
		t.Fatalf("write 15-awg.json: %v", err)
	}

	// 2. Write 10-tunnels.json with vless, trusttunnel, trojan, shadowsocks, naive, socks
	tunnelsJSON := `{
  "outbounds": [
    {
      "type": "vless",
      "tag": "finland-vless",
      "server": "fi.example.com",
      "server_port": 443,
      "uuid": "11111111-2222-3333-4444-555555555555",
      "tls": {
        "enabled": true,
        "server_name": "fi.example.com"
      }
    },
    {
      "type": "trusttunnel",
      "tag": "nl-tt",
      "server": "nl.example.com",
      "server_port": 443,
      "username": "alice",
      "password": "secret",
      "tls": {
        "enabled": true,
        "server_name": "nl.example.com",
        "record_fragment": true
      }
    },
    {
      "type": "trojan",
      "tag": "de-trojan",
      "server": "de.example.com",
      "server_port": 443,
      "password": "trojan-secret",
      "tls": {
        "enabled": true,
        "server_name": "de.example.com"
      }
    },
    {
      "type": "shadowsocks",
      "tag": "ss-2022",
      "server": "ss.example.com",
      "server_port": 8388,
      "method": "2022-blake3-aes-128-gcm",
      "password": "MTIzNDU2Nzg5MDEyMzQ1Ng=="
    },
    {
      "type": "naive",
      "tag": "us-naive",
      "server": "us.example.com",
      "server_port": 443,
      "username": "u",
      "password": "p",
      "tls": {
        "enabled": true,
        "server_name": "us.example.com"
      }
    },
    {
      "type": "socks",
      "tag": "local-socks",
      "server": "127.0.0.1",
      "server_port": 1080
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(tunnelsJSON), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}

	// 3. Write 20-router.json
	routerJSON := `{
  "outbounds": [
    {
      "type": "selector",
      "tag": "proxy-group",
      "default": "finland-vless",
      "outbounds": ["finland-vless", "nl-tt"]
    }
  ],
  "route": {
    "rule_set": [
      {
        "tag": "geosite-youtube",
        "type": "remote",
        "format": "binary",
        "url": "https://example.com/srs/youtube.srs"
      }
    ],
    "rules": [
      {
        "action": "hijack-dns",
        "port": [53]
      },
      {
        "ip_is_private": true,
        "outbound": "direct"
      },
      {
        "domain_suffix": ["openai.com", ".anthropic.com"],
        "rule_set": ["geosite-youtube"],
        "outbound": "proxy-group"
      },
      {
        "ip_cidr": ["1.1.1.0/24"],
        "outbound": "awg-nwg0"
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(sbDir, "20-router.json"), []byte(routerJSON), 0644); err != nil {
		t.Fatalf("write 20-router.json: %v", err)
	}

	fircDir := filepath.Join(tmpDir, "firc")
	op := NewOperator(Options{
		ConfigDir: fircDir,
	})

	res, err := op.MigrateFromSingboxWithOptions(context.Background(), sbDir, nil)
	if err != nil {
		t.Fatalf("MigrateFromSingbox failed: %v", err)
	}

	if len(res.Tunnels) != 6 {
		t.Fatalf("expected 6 migrated tunnels, got %d (skipped: %v)", len(res.Tunnels), res.Skipped)
	}
	if res.Tunnels[0].Device != "tunvless0" || !strings.HasPrefix(res.Tunnels[0].Sources[0].Link, "vless://") {
		t.Errorf("unexpected tunnel[0]: %+v", res.Tunnels[0])
	}
	// First enabled tunnel also received port 1099 local SOCKS5 inbound
	if len(res.Tunnels[0].Inbounds) != 1 || res.Tunnels[0].Inbounds[0].Port != 1099 {
		t.Errorf("expected tunnel[0] to have port 1099 inbound, got %+v", res.Tunnels[0].Inbounds)
	}
	if res.Tunnels[1].Device != "tunvless1" || !strings.HasPrefix(res.Tunnels[1].Sources[0].Link, "tt://") {
		t.Errorf("unexpected tunnel[1]: %+v", res.Tunnels[1])
	}
	if !strings.HasPrefix(res.Tunnels[2].Sources[0].Link, "trojan://") {
		t.Errorf("unexpected tunnel[2]: %+v", res.Tunnels[2])
	}
	if !strings.HasPrefix(res.Tunnels[3].Sources[0].Link, "ss://") {
		t.Errorf("unexpected tunnel[3]: %+v", res.Tunnels[3])
	}
	if !strings.HasPrefix(res.Tunnels[4].Sources[0].Link, "naive+https://") {
		t.Errorf("unexpected tunnel[4]: %+v", res.Tunnels[4])
	}
	if !strings.HasPrefix(res.Tunnels[5].Sources[0].Link, "socks5://") {
		t.Errorf("unexpected tunnel[5]: %+v", res.Tunnels[5])
	}

	if len(res.Groups) != 2 {
		t.Fatalf("expected 2 migrated groups, got %d: %+v", len(res.Groups), res.Groups)
	}
	if len(res.Groups[0].Interface) != 1 || res.Groups[0].Interface[0] != "tunvless0" {
		t.Errorf("expected group[0] interface tunvless0, got %v", res.Groups[0].Interface)
	}
	if len(res.Groups[0].Rules) != 2 || res.Groups[0].Rules[0].Rule != "openai.com" || res.Groups[0].Rules[1].Rule != "anthropic.com" {
		t.Errorf("unexpected group[0] rules: %+v", res.Groups[0].Rules)
	}
	if res.Groups[0].List == nil || res.Groups[0].List.URL != "https://example.com/srs/youtube.srs" {
		t.Errorf("unexpected group[0] list: %+v", res.Groups[0].List)
	}
	if len(res.Groups[1].Interface) != 1 || res.Groups[1].Interface[0] != "nwg0" || len(res.Groups[1].Rules) != 1 || res.Groups[1].Rules[0].Rule != "1.1.1.0/24" {
		t.Errorf("unexpected group[1]: %+v", res.Groups[1])
	}

	loadedTunnels, err := op.LoadTunnelsFile()
	if err != nil || len(loadedTunnels) != 6 {
		t.Fatalf("LoadTunnelsFile failed: err=%v len=%d", err, len(loadedTunnels))
	}
	loadedGroups, err := op.LoadGroupsFile()
	if err != nil || len(loadedGroups) != 2 {
		t.Fatalf("LoadGroupsFile failed: err=%v len=%d", err, len(loadedGroups))
	}
}

func TestMigrateFullParity_Hy2AnytlsVmessMieru_Subscriptions_HappCrypt_MultiRuleSets(t *testing.T) {
	tmpDir := t.TempDir()
	sbDir := filepath.Join(tmpDir, "singbox", "config.d")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sbDir: %v", err)
	}

	// Generate 3 RSA keys so we can test happ://crypt3/ decryption via happ_keys.json
	var b64Keys []string
	var privKey3 *rsa.PrivateKey
	for i := 0; i < 3; i++ {
		pk, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("rsa.GenerateKey: %v", err)
		}
		if i == 2 {
			privKey3 = pk
		}
		der := x509.MarshalPKCS1PrivateKey(pk)
		b64Keys = append(b64Keys, base64.StdEncoding.EncodeToString(der))
	}
	keysJSON, _ := json.Marshal(b64Keys)
	if err := os.WriteFile(filepath.Join(tmpDir, "happ_keys.json"), keysJSON, 0600); err != nil {
		t.Fatalf("write happ_keys.json: %v", err)
	}

	// Encrypt a subscription URL with key #3 (crypt3/)
	plainSubURL := "https://sub.example.com/api/v1/client/subscribe?token=secret123"
	cipherBytes, err := rsa.EncryptPKCS1v15(rand.Reader, &privKey3.PublicKey, []byte(plainSubURL))
	if err != nil {
		t.Fatalf("rsa.EncryptPKCS1v15: %v", err)
	}
	happCrypt3URL := "happ://crypt3/" + base64.StdEncoding.EncodeToString(cipherBytes)

	// Write subscriptions.json in parent dataDir (tmpDir)
	subsJSON := `[
  {
    "id": "87f242c6-019d-4d88-90b5-123456789abc",
    "label": "Happ Основная",
    "url": "` + happCrypt3URL + `",
    "refreshHours": 6,
    "enabled": true,
    "headers": [
      {"name": "User-Agent", "value": "Happ/4.6.2/ios/1700000000"},
      {"name": "X-HWID", "value": "deadbeefcafe1234"}
    ],
    "filterInclude": "NL|DE",
    "filterExclude": "(?i)(RU|LTE)",
    "selectorTag": "sub-87f242c6",
    "memberTags": ["sub-87f242c6-node1", "sub-87f242c6-node2"],
    "activeMember": "sub-87f242c6-node2",
    "members": [
      {"tag": "sub-87f242c6-node1", "label": "Germany-1"},
      {"tag": "sub-87f242c6-node2", "label": "Netherlands-1"}
    ],
    "excludedMembers": [
      {"tag": "sub-87f242c6-node3", "label": "Slow-Node"}
    ]
  }
]`
	if err := os.WriteFile(filepath.Join(tmpDir, "subscriptions.json"), []byte(subsJSON), 0644); err != nil {
		t.Fatalf("write subscriptions.json: %v", err)
	}

	// Write 10-tunnels.json with vmess, hysteria2, anytls, mieru, trusttunnel (quic=true)
	tunnelsJSON := `{
  "outbounds": [
    {
      "type": "hysteria2",
      "tag": "hy2-fast",
      "server": "hy2.example.com",
      "server_port": 8443,
      "server_ports": ["20000:30000"],
      "password": "hy2password",
      "up_mbps": 100,
      "down_mbps": 200,
      "obfs": {
        "type": "salamander",
        "password": "salapass"
      },
      "tls": {
        "enabled": true,
        "server_name": "hy2.example.com"
      }
    },
    {
      "type": "anytls",
      "tag": "anytls-de",
      "server": "any.example.com",
      "server_port": 443,
      "password": "anysecret",
      "tls": {
        "enabled": true,
        "server_name": "any.example.com",
        "alpn": ["h2", "http/1.1"]
      }
    },
    {
      "type": "vmess",
      "tag": "vmess-ws",
      "server": "vmess.example.com",
      "server_port": 443,
      "uuid": "11111111-2222-3333-4444-555555555555",
      "security": "auto",
      "tls": {
        "enabled": true,
        "server_name": "vmess.example.com"
      },
      "transport": {
        "type": "ws",
        "path": "/vmess"
      }
    },
    {
      "type": "trusttunnel",
      "tag": "tt-quic",
      "server": "tt.example.com",
      "server_port": 443,
      "username": "u",
      "password": "p",
      "quic": true,
      "tls": {
        "enabled": true,
        "server_name": "tt.example.com"
      }
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(tunnelsJSON), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}

	// Write 20-router.json with MULTIPLE rule_sets targeting the same subscription outbound
	routerJSON := `{
  "route": {
    "rule_set": [
      {
        "tag": "geosite-telegram",
        "type": "remote",
        "url": "https://example.com/srs/telegram.srs"
      },
      {
        "tag": "geosite-category-ai-!cn",
        "type": "remote",
        "url": "https://example.com/srs/ai.srs"
      },
      {
        "tag": "geosite-google-gemini",
        "type": "remote",
        "url": "https://example.com/srs/gemini.srs"
      }
    ],
    "rules": [
      {
        "rule_set": ["geosite-telegram"],
        "outbound": "sub-87f242c6"
      },
      {
        "rule_set": ["geosite-category-ai-!cn"],
        "outbound": "sub-87f242c6"
      },
      {
        "rule_set": ["geosite-google-gemini"],
        "outbound": "sub-87f242c6"
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(sbDir, "20-router.json"), []byte(routerJSON), 0644); err != nil {
		t.Fatalf("write 20-router.json: %v", err)
	}

	// Write client-routes.json in tmpDir
	clientRoutesJSON := `{
  "routes": [
    {
      "id": "cr1",
      "clientIp": "192.168.1.55",
      "tunnelId": "hy2-fast",
      "enabled": true
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(tmpDir, "client-routes.json"), []byte(clientRoutesJSON), 0644); err != nil {
		t.Fatalf("write client-routes.json: %v", err)
	}

	res, err := MigrateFromSingboxConfigDir(sbDir)
	if err != nil {
		t.Fatalf("MigrateFromSingboxConfigDir failed: %v", err)
	}

	// 4 standalone tunnels + 1 subscription tunnel = 5 tunnels
	if len(res.Tunnels) != 5 {
		t.Fatalf("expected 5 tunnels, got %d (skipped: %v)", len(res.Tunnels), res.Skipped)
	}
	if !strings.HasPrefix(res.Tunnels[0].Sources[0].Link, "hysteria2://") {
		t.Errorf("expected hysteria2:// link, got %s", res.Tunnels[0].Sources[0].Link)
	}
	if !strings.HasPrefix(res.Tunnels[1].Sources[0].Link, "anytls://") {
		t.Errorf("expected anytls:// link, got %s", res.Tunnels[1].Sources[0].Link)
	}
	if !strings.HasPrefix(res.Tunnels[2].Sources[0].Link, "vmess://") {
		t.Errorf("expected vmess:// link, got %s", res.Tunnels[2].Sources[0].Link)
	}
	if !strings.Contains(res.Tunnels[3].Sources[0].Link, "quic=1") {
		t.Errorf("expected tt:// link with quic=1, got %s", res.Tunnels[3].Sources[0].Link)
	}

	subTun := res.Tunnels[4]
	if len(subTun.Sources) != 1 || subTun.Sources[0].Kind != "subscription" {
		t.Fatalf("expected subscription source in tunnel[4], got %+v", subTun.Sources)
	}
	if subTun.Sources[0].URL != plainSubURL {
		t.Errorf("expected decrypted happ://crypt3 URL %q, got %q", plainSubURL, subTun.Sources[0].URL)
	}
	if subTun.Sources[0].Headers["X-HWID"] != "deadbeefcafe1234" {
		t.Errorf("expected X-HWID header preserved, got %+v", subTun.Sources[0].Headers)
	}
	if subTun.Filter != "NL|DE" {
		t.Errorf("expected Filter='NL|DE', got %q", subTun.Filter)
	}
	if subTun.FilterExclude != "(?i)(RU|LTE)" {
		t.Errorf("expected FilterExclude='(?i)(RU|LTE)', got %q", subTun.FilterExclude)
	}
	if len(subTun.Order) != 2 || !strings.HasSuffix(subTun.Order[0], ":Netherlands-1") {
		t.Errorf("expected active member Netherlands-1 first in Order, got %v", subTun.Order)
	}
	if len(subTun.Exclude) != 1 || !strings.HasSuffix(subTun.Exclude[0], ":Slow-Node") {
		t.Errorf("expected Slow-Node in Exclude, got %v", subTun.Exclude)
	}

	// 3 rule_set groups + 1 client-routes group = 4 groups
	if res.RuleSetsMigrated != 3 {
		t.Errorf("expected 3 ruleSetsMigrated, got %d", res.RuleSetsMigrated)
	}
	if res.ClientRoutesMigrated != 1 {
		t.Errorf("expected 1 clientRoutesMigrated, got %d", res.ClientRoutesMigrated)
	}
	if len(res.Groups) != 4 {
		t.Fatalf("expected 4 groups (3 rule_sets + 1 client_route), got %d: %+v", len(res.Groups), res.Groups)
	}
	for i := 0; i < 3; i++ {
		if res.Groups[i].List == nil || res.Groups[i].Interface[0] != subTun.Device {
			t.Errorf("expected group[%d] to route to %s with non-nil List, got %+v", i, subTun.Device, res.Groups[i])
		}
	}
	if len(res.Groups[3].Devices.Allow) != 1 || res.Groups[3].Devices.Allow[0] != "192.168.1.55" {
		t.Errorf("expected client route group[3] for 192.168.1.55, got %+v", res.Groups[3])
	}
}

func TestParseInlineSourcesForFirc_AllFormats(t *testing.T) {
	// 1. Wrapper URL (clash://install-config?url=...) + GitHub blob -> raw
	src1, err := ParseInlineSourcesForFirc(
		"clash://install-config?url=https%3A%2F%2Fgithub.com%2Fuser%2Frepo%2Fblob%2Fmain%2Fsub.txt",
		"GitHub Подписка",
		"",
	)
	if err != nil || len(src1) != 1 || src1[0].Kind != "subscription" {
		t.Fatalf("expected subscription source from clash:// + github blob, got err=%v src=%+v", err, src1)
	}
	if src1[0].URL != "https://raw.githubusercontent.com/user/repo/main/sub.txt" {
		t.Errorf("expected rewritten raw GitHub URL, got %q", src1[0].URL)
	}

	// 2. Double-Base64 encoded multi-protocol links (hy2:// + anytls://)
	rawLinks := "hy2://pass123@hy2.example.com:443?sni=hy2.example.com&obfs=salamander&obfs-password=sala#Hy2Node\nanytls://secret@any.example.com:443?sni=any.example.com#AnyNode\n"
	b64Once := base64.StdEncoding.EncodeToString([]byte(rawLinks))
	b64Twice := base64.StdEncoding.EncodeToString([]byte(b64Once))
	src2, err := ParseInlineSourcesForFirc(b64Twice, "DoubleB64", "")
	if err != nil || len(src2) != 2 {
		t.Fatalf("expected 2 link sources from double-base64, got err=%v src=%+v", err, src2)
	}
	if !strings.HasPrefix(src2[0].Link, "hysteria2://") || !strings.HasPrefix(src2[1].Link, "anytls://") {
		t.Errorf("unexpected links from double-base64: %+v", src2)
	}

	// 3. AdGuard TrustTunnel TOML [endpoint]
	tomlInput := `
[endpoint]
hostname = "tt.example.com"
addresses = ["1.2.3.4:443"]
username = "alice"
password = "bob"
upstream_protocol = "http3"
`
	src3, err := ParseInlineSourcesForFirc(tomlInput, "AdGuard TT", "")
	if err != nil || len(src3) != 1 || !strings.HasPrefix(src3[0].Link, "tt://") || !strings.Contains(src3[0].Link, "quic=1") {
		t.Fatalf("expected tt:// link with quic=1 from TrustTunnel TOML, got err=%v src=%+v", err, src3)
	}
}

func TestMigrateFakeIPAndSelectiveOptions(t *testing.T) {
	tmpDir := t.TempDir()
	cfgDir := filepath.Join(tmpDir, "config.d")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("mkdir config.d: %v", err)
	}

	// Write 10-tunnels.json with 2 tunnels
	tunnelsJSON := `{
  "outbounds": [
    {
      "type": "vless",
      "tag": "vless-nl",
      "server": "nl.example.com",
      "server_port": 443,
      "uuid": "11111111-2222-3333-4444-555555555555",
      "tls": {"enabled": true, "server_name": "nl.example.com"}
    },
    {
      "type": "hysteria2",
      "tag": "hy2-de",
      "server": "de.example.com",
      "server_port": 443,
      "password": "secret",
      "tls": {"enabled": true, "server_name": "de.example.com"}
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(cfgDir, "10-tunnels.json"), []byte(tunnelsJSON), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}

	// Write 21-fakeip.json (FakeIP-TUN slot) with inline rule_set, dns.rules (server: fakeip) + route.final, and route.rules
	fakeipJSON := `{
  "inbounds": [{"type": "tun", "tag": "tun-in", "interface_name": "opkgtun0"}],
  "dns": {
    "servers": [
      {"tag": "fakeip", "type": "fakeip", "inet4_range": "198.18.0.0/15"},
      {"tag": "real", "type": "udp", "server": "1.1.1.1"}
    ],
    "rules": [
      {"action": "route", "server": "fakeip", "query_type": ["A", "AAAA"]},
      {"action": "route", "server": "fakeip", "domain_suffix": ["claude.ai", "anthropic.com"]}
    ],
    "final": "real"
  },
  "route": {
    "rule_set": [
      {
        "tag": "inline-openai",
        "type": "inline",
        "rules": [
          {"domain_suffix": ["openai.com", "chatgpt.com"], "ip_cidr": ["104.18.0.0/20"]}
        ]
      }
    ],
    "rules": [
      {"action": "hijack-dns"},
      {"ip_is_private": true, "outbound": "direct"},
      {"rule_set": ["inline-openai"], "outbound": "hy2-de"}
    ],
    "final": "vless-nl"
  }
}`
	if err := os.WriteFile(filepath.Join(cfgDir, "21-fakeip.json"), []byte(fakeipJSON), 0644); err != nil {
		t.Fatalf("write 21-fakeip.json: %v", err)
	}

	res, err := MigrateFromSingboxConfigDirWithOptions(cfgDir, &MigrationOptions{
		RouterSource: "fakeip",
	})
	if err != nil {
		t.Fatalf("MigrateFromSingboxConfigDirWithOptions: %v", err)
	}
	if res.FakeIP == nil || !res.FakeIP.Detected {
		t.Fatalf("expected FakeIP metadata to be detected, got %+v", res.FakeIP)
	}
	if res.FakeIP.Inet4Range != "198.18.0.0/15" || res.FakeIP.RealDNS != "1.1.1.1" || res.FakeIP.RouteFinal != "vless-nl" {
		t.Errorf("unexpected FakeIP metadata: %+v", res.FakeIP)
	}
	if len(res.Tunnels) != 2 {
		t.Fatalf("expected 2 tunnels, got %d", len(res.Tunnels))
	}
	if len(res.Groups) != 2 {
		t.Fatalf("expected 2 groups (hy2-de from inline rule_set + vless-nl from fakeip dns.rules + route.final), got %d: %+v", len(res.Groups), res.Groups)
	}

	// Test selective filtering by SelectedTunnelIDs and SelectedGroupIDs
	onlyFirstTunnel := res.Tunnels[0].ID
	onlySecondGroup := res.Groups[1].ID
	selRes, err := MigrateFromSingboxConfigDirWithOptions(cfgDir, &MigrationOptions{
		RouterSource:      "fakeip",
		SelectedTunnelIDs: []string{onlyFirstTunnel},
		SelectedGroupIDs:  []string{onlySecondGroup},
	})
	if err != nil {
		t.Fatalf("selective MigrateFromSingboxConfigDirWithOptions: %v", err)
	}
	if len(selRes.Tunnels) != 1 || selRes.Tunnels[0].ID != onlyFirstTunnel {
		t.Errorf("expected only tunnel %q, got %+v", onlyFirstTunnel, selRes.Tunnels)
	}
	if len(selRes.Groups) != 1 || selRes.Groups[0].ID != onlySecondGroup {
		t.Errorf("expected only group %q, got %+v", onlySecondGroup, selRes.Groups)
	}
}

// Возврат после импорта — только снимок auto-pre-migrate: он обязан вернуть
// байт в байт и config.d sing-box, и файлы AWGM Route, какими они были до импорта.
func TestImportThenSnapshotRestoreReturnsBothEngines(t *testing.T) {
	tmpDir := t.TempDir()
	awgmDir := filepath.Join(tmpDir, "awg-manager")
	sbDir := filepath.Join(awgmDir, "config.d")
	fircDir := filepath.Join(tmpDir, "firc")
	for _, d := range []string{sbDir, fircDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	origTunnels := `{"outbounds":[{"type":"vless","tag":"orig-vless","server":"1.2.3.4","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(origTunnels), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(awgmDir, "settings.json"), []byte(`{"singboxRouter":{"routingEngine":"sing-box"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	op := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
	op.SetSingboxValidator(func(_ context.Context, _ string) error { return nil })
	preTunnels := []Tunnel{{ID: "before", Device: "tunvless0", Enable: true, Sources: []TunnelSource{{ID: "0a0b0c0d", Kind: "link", Name: "B",
		Link: "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@before.example.com:443?security=tls&sni=before.example.com#B"}}}}
	if err := op.SaveTunnelsFile(preTunnels); err != nil {
		t.Fatal(err)
	}
	preGroups := []Group{{ID: "a1b2c3d4", Name: "Before", Interface: []string{"tunvless0"}, Enable: true,
		Rules: []Rule{{ID: "r1", Type: "namespace", Rule: "before.example.com", Enable: true}}}}
	if err := op.SaveGroupsFile(preGroups); err != nil {
		t.Fatal(err)
	}
	preGroupsYAML, _ := os.ReadFile(op.GroupsPath())

	res, err := op.MigrateFromSingboxWithOptions(t.Context(), sbDir, &MigrationOptions{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.SnapshotCreated == nil || res.SnapshotCreated.ID == "" {
		t.Fatal("импорт обязан снимать auto-pre-migrate")
	}
	afterImport, _ := os.ReadFile(op.GroupsPath())
	if strings.Contains(string(afterImport), "before.example.com") {
		t.Fatalf("импорт заменяет группы, а прежняя осталась:\n%s", afterImport)
	}

	// Пользователь правит обе стороны после импорта.
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(`{"outbounds":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := op.SaveGroupsFile([]Group{{ID: "e5f6a7b8", Name: "After", Interface: []string{"tunvless0"}, Enable: true}}); err != nil {
		t.Fatal(err)
	}

	if _, err := op.RestoreSystemSnapshot(t.Context(), res.SnapshotCreated.ID, sbDir); err != nil {
		t.Fatalf("RestoreSystemSnapshot: %v", err)
	}
	gotSB, _ := os.ReadFile(filepath.Join(sbDir, "10-tunnels.json"))
	if string(gotSB) != origTunnels {
		t.Errorf("10-tunnels.json не вернулся байт в байт:\n%s", gotSB)
	}
	gotGroups, _ := os.ReadFile(op.GroupsPath())
	if string(gotGroups) != string(preGroupsYAML) {
		t.Errorf("groups.yaml не вернулся к состоянию до импорта:\n%s\nwant:\n%s", gotGroups, preGroupsYAML)
	}
}

// Повторный импорт того же sing-box даёт тот же результат, а после смены выхода
// в sing-box от прежнего выхода не остаётся ни группы, ни туннеля: слияние
// оставляло их, и обратная конвертация потом воскрешала давно заменённый выход.
func TestReimportIsIdempotentAndDropsReplacedOutbound(t *testing.T) {
	tmpDir := t.TempDir()
	awgmDir := filepath.Join(tmpDir, "awg-manager")
	sbDir := filepath.Join(awgmDir, "config.d")
	fircDir := filepath.Join(tmpDir, "firc")
	for _, d := range []string{sbDir, fircDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	tunnels := `{"outbounds":[{"type":"vless","tag":"vless-a","server":"1.2.3.4","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`
	router := `{"route":{"rules":[{"domain_suffix":["example.com"],"outbound":"vless-a"}],"final":"direct"}}`
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(tunnels), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "20-router.json"), []byte(router), 0644); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})

	read := func() (string, string) {
		tu, _ := os.ReadFile(op.TunnelsPath())
		gr, _ := os.ReadFile(op.GroupsPath())
		return string(tu), string(gr)
	}
	first, err := op.MigrateFromSingboxWithOptions(t.Context(), sbDir, &MigrationOptions{})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if len(first.Tunnels) == 0 || len(first.Groups) == 0 {
		t.Fatalf("кандидатов нет — проверять нечего: %+v", first)
	}
	t1, g1 := read()
	if _, err := op.MigrateFromSingboxWithOptions(t.Context(), sbDir, &MigrationOptions{}); err != nil {
		t.Fatalf("second import: %v", err)
	}
	t2, g2 := read()
	if t1 != t2 {
		t.Errorf("tunnels.yaml изменился на повторном импорте:\n%s\n---\n%s", t1, t2)
	}
	if g1 != g2 {
		t.Errorf("groups.yaml изменился на повторном импорте:\n%s\n---\n%s", g1, g2)
	}

	// Выход переименован в sing-box: vless-a → vless-b.
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(strings.ReplaceAll(tunnels, "vless-a", "vless-b")), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "20-router.json"), []byte(strings.ReplaceAll(router, "vless-a", "vless-b")), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := op.MigrateFromSingboxWithOptions(t.Context(), sbDir, &MigrationOptions{}); err != nil {
		t.Fatalf("import after rename: %v", err)
	}
	t3, g3 := read()
	if strings.Contains(t3, "vless-a") || strings.Contains(g3, "vless-a") {
		t.Errorf("после смены выхода остался прежний vless-a:\ntunnels:\n%s\ngroups:\n%s", t3, g3)
	}
	if !strings.Contains(t3, "vless-b") || !strings.Contains(g3, "vless-b") {
		t.Errorf("новый выход vless-b не импортирован:\ntunnels:\n%s\ngroups:\n%s", t3, g3)
	}
}

func TestSnapshotRestorePreflightValidationAndNonDestructiveReject(t *testing.T) {
	tmpDir := t.TempDir()
	awgmDir := filepath.Join(tmpDir, "awg-manager")
	sbDir := filepath.Join(awgmDir, "config.d")
	fircDir := filepath.Join(tmpDir, "firc")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sbDir: %v", err)
	}
	if err := os.MkdirAll(fircDir, 0755); err != nil {
		t.Fatalf("mkdir fircDir: %v", err)
	}

	origTunnels := `{"outbounds":[{"type":"vless","tag":"keep-me","server":"1.2.3.4","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(origTunnels), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}

	op := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
	if err := os.MkdirAll(op.snapshotsDir(), 0700); err != nil {
		t.Fatalf("mkdir snapshotsDir: %v", err)
	}

	// 1. Empty JSON "{}" snapshot must be rejected BEFORE deleting 10-tunnels.json
	if err := os.WriteFile(filepath.Join(op.snapshotsDir(), "empty-snap.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("write empty-snap.json: %v", err)
	}
	if _, err := op.RestoreSystemSnapshot(t.Context(), "empty-snap", sbDir); err == nil {
		t.Fatalf("expected RestoreSystemSnapshot to reject empty `{}` snapshot")
	}
	if data, err := os.ReadFile(filepath.Join(sbDir, "10-tunnels.json")); err != nil || string(data) != origTunnels {
		t.Fatalf("10-tunnels.json was modified or deleted on failed empty snapshot restore: err=%v data=%q", err, string(data))
	}

	// 2. Snapshot with path traversal `../evil.json` must be rejected before touching disk
	traversalSnap := `{
  "id": "traversal-snap",
  "createdAt": 1700000000,
  "activeEngine": "sing-box",
  "files": [
    {"scope": "singbox", "relPath": "10-tunnels.json", "mode": 420, "dataB64": "` + base64.StdEncoding.EncodeToString([]byte(origTunnels)) + `"},
    {"scope": "singbox", "relPath": "../evil.json", "mode": 420, "dataB64": "` + base64.StdEncoding.EncodeToString([]byte(`{}`)) + `"}
  ]
}`
	if err := os.WriteFile(filepath.Join(op.snapshotsDir(), "traversal-snap.json"), []byte(traversalSnap), 0600); err != nil {
		t.Fatalf("write traversal-snap.json: %v", err)
	}
	if _, err := op.RestoreSystemSnapshot(t.Context(), "traversal-snap", sbDir); err == nil {
		t.Fatalf("expected RestoreSystemSnapshot to reject path traversal relPath")
	}
	if _, err := os.Stat(filepath.Join(awgmDir, "evil.json")); !os.IsNotExist(err) {
		t.Fatalf("path traversal file should not have been created")
	}

	// 3. Snapshot with invalid base64 or corrupt JSON in second entry must not delete 10-tunnels.json
	corruptSnap := `{
  "id": "corrupt-snap",
  "createdAt": 1700000000,
  "activeEngine": "sing-box",
  "files": [
    {"scope": "singbox", "relPath": "10-tunnels.json", "mode": 420, "dataB64": "` + base64.StdEncoding.EncodeToString([]byte(`{"outbounds":[]}`)) + `"},
    {"scope": "singbox", "relPath": "20-router.json", "mode": 420, "dataB64": "!!!not-base64!!!"}
  ]
}`
	if err := os.WriteFile(filepath.Join(op.snapshotsDir(), "corrupt-snap.json"), []byte(corruptSnap), 0600); err != nil {
		t.Fatalf("write corrupt-snap.json: %v", err)
	}
	if _, err := op.RestoreSystemSnapshot(t.Context(), "corrupt-snap", sbDir); err == nil {
		t.Fatalf("expected RestoreSystemSnapshot to reject corrupt base64 entry")
	}
	if data, err := os.ReadFile(filepath.Join(sbDir, "10-tunnels.json")); err != nil || string(data) != origTunnels {
		t.Fatalf("10-tunnels.json was modified on failed corrupt snapshot restore: err=%v data=%q", err, string(data))
	}
}

func TestFakeIPPersistenceWhenStoppedAndAutoSnapshotFailClosed(t *testing.T) {
	tmpDir := t.TempDir()
	awgmDir := filepath.Join(tmpDir, "awg-manager")
	sbDir := filepath.Join(awgmDir, "config.d")
	fircDir := filepath.Join(tmpDir, "firc")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sbDir: %v", err)
	}
	if err := os.MkdirAll(fircDir, 0755); err != nil {
		t.Fatalf("mkdir fircDir: %v", err)
	}

	op := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})

	// 1. When singboxConfigDir and fircDir have no files at all, CreateSystemSnapshot fails,
	// so MigrateFromSingboxWithOptions (snapshot is mandatory) must abort fail-closed.
	if _, err := op.MigrateFromSingboxWithOptions(t.Context(), sbDir, &MigrationOptions{}); err == nil {
		t.Fatalf("expected MigrateFromSingboxWithOptions to abort when auto-snapshot fails on empty source")
	}

	// 2. Write 21-fakeip.json and verify FakeIP settings are persisted to firc.conf even when fircd is stopped.
	fakeipJSON := `{
  "dns": {
    "servers": [
      {"tag": "fakeip", "type": "fakeip", "inet4_range": "198.18.0.0/15"},
      {"tag": "real", "type": "udp", "server": "9.9.9.9"}
    ]
  },
  "route": {"rules": []}
}`
	if err := os.WriteFile(filepath.Join(sbDir, "21-fakeip.json"), []byte(fakeipJSON), 0644); err != nil {
		t.Fatalf("write 21-fakeip.json: %v", err)
	}
	incFakeIP := true
	res, err := op.MigrateFromSingboxWithOptions(t.Context(), sbDir, &MigrationOptions{
		RouterSource:        "fakeip",
		IncludeFakeIPConfig: &incFakeIP,
	})
	if err != nil {
		t.Fatalf("MigrateFromSingboxWithOptions with FakeIP: %v", err)
	}
	if res.FakeIP == nil || !res.FakeIP.Applied {
		t.Fatalf("expected FakeIP.Applied=true after persisting to firc.conf, got %+v", res.FakeIP)
	}
	confBytes, err := os.ReadFile(op.ConfigPath())
	if err != nil {
		t.Fatalf("read firc.conf: %v", err)
	}
	if !strings.Contains(string(confBytes), "198.18.0.0/15") || !strings.Contains(string(confBytes), "9.9.9.9") {
		t.Fatalf("expected firc.conf to contain persisted FakeIP v4Pool and upstream DNS 9.9.9.9, got:\n%s", string(confBytes))
	}
}

func TestKernelTCPFalsePreservedAndServerRoutingConcurrentWithKeenDNS(t *testing.T) {
	tmpDir := t.TempDir()
	op := NewOperator(Options{ConfigDir: tmpDir, InboundPort: 1099})

	// 1. F4: Verify explicit kernelTcp: false is preserved through JSON unmarshal and ApplyTunnels
	var tunFalse Tunnel
	if err := json.Unmarshal([]byte(`{"id":"t1","device":"tunvless0","enable":true,"advanced":{"kernelTcp":false},"sources":[{"id":"s1","kind":"link","link":"http://127.0.0.1:1099"}]}`), &tunFalse); err != nil {
		t.Fatalf("unmarshal tunFalse: %v", err)
	}
	if tunFalse.Advanced.KernelTCP != false || !tunFalse.Advanced.KernelTCPSet {
		t.Fatalf("expected KernelTCP=false and KernelTCPSet=true, got %+v", tunFalse.Advanced)
	}
	if _, err := op.ApplyTunnels(t.Context(), []Tunnel{tunFalse}, false); err != nil {
		t.Fatalf("ApplyTunnels: %v", err)
	}
	loaded, err := op.LoadTunnelsFile()
	if err != nil || len(loaded) != 1 || loaded[0].Advanced.KernelTCP != false {
		t.Fatalf("expected loaded tunnel to preserve KernelTCP=false, got err=%v loaded=%+v", err, loaded)
	}

	// Verify omitted kernelTcp defaults to true after ApplyTunnels (and when advanced:{} is present)
	var tunDefault Tunnel
	if err := json.Unmarshal([]byte(`{"id":"t2","device":"tunvless1","enable":true,"sources":[{"id":"s1","kind":"link","link":"http://127.0.0.1:1099"}]}`), &tunDefault); err != nil {
		t.Fatalf("unmarshal tunDefault: %v", err)
	}
	if _, err := op.ApplyTunnels(t.Context(), []Tunnel{tunFalse, tunDefault}, false); err != nil {
		t.Fatalf("ApplyTunnels both: %v", err)
	}
	loadedBoth, err := op.LoadTunnelsFile()
	if err != nil || len(loadedBoth) != 2 {
		t.Fatalf("LoadTunnelsFile both: err=%v len=%d", err, len(loadedBoth))
	}
	if loadedBoth[0].Advanced.KernelTCP != false {
		t.Fatalf("expected explicit kernelTcp:false preserved on t1, got true")
	}
	if !loadedBoth[1].Advanced.KernelTCP {
		t.Fatalf("expected omitted kernelTcp on t2 to default to true")
	}

	// 2. Priority #4: Verify CommitServerRouting does not clobber concurrent KeenDNSCloud edit
	if _, err := op.PrepareServerRouting(t.Context(), "tx-sr-1", "vless_xhttp_server", true); err != nil {
		t.Fatalf("PrepareServerRouting: %v", err)
	}
	// Concurrent KeenDNS edit between Prepare and Commit
	if _, err := op.ApplyExtConfig(t.Context(), ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:    true,
			Interfaces: []string{"tunvless0"},
		},
	}); err != nil {
		t.Fatalf("ApplyExtConfig KeenDNS: %v", err)
	}
	if err := op.CommitServerRouting(t.Context(), "tx-sr-1"); err != nil {
		t.Fatalf("CommitServerRouting: %v", err)
	}
	finalExt := op.LoadExtConfig()
	if !finalExt.KeenDNSCloud.Enabled || len(finalExt.KeenDNSCloud.Interfaces) != 1 || finalExt.KeenDNSCloud.Interfaces[0] != "tunvless0" {
		t.Fatalf("expected concurrent KeenDNSCloud update to be preserved after CommitServerRouting, got %+v", finalExt)
	}
	if !finalExt.ServerRouting.VlessXhttpServer {
		t.Fatalf("expected ServerRouting.VlessXhttpServer=true after CommitServerRouting, got %+v", finalExt.ServerRouting)
	}
}

func TestApplyGroupsNetfilterFailureRollsBackWithoutFircdFileOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	op := NewOperator(Options{ConfigDir: tmpDir, InboundPort: 1099})

	initialGroups := []Group{
		{
			ID:        "11112222",
			Name:      "InitialGroup",
			Interface: []string{"tunvless0"},
			Enable:    true,
			Rules:     []Rule{{ID: "r1", Type: "domain", Rule: "initial.example.com", Enable: true}},
		},
	}
	if err := op.SaveGroupsFile(initialGroups); err != nil {
		t.Fatalf("SaveGroupsFile initial: %v", err)
	}
	origBytes, err := os.ReadFile(op.GroupsPath())
	if err != nil {
		t.Fatalf("read initial groups.yaml: %v", err)
	}

	var putCalls atomic.Int32
	var lastGroupsName atomic.Value
	lastGroupsName.Store("InitialGroup")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/system" || r.URL.Path == "/api/v1/system/netfilter"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dirty":false}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/groups"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": initialGroups})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/groups":
			putCalls.Add(1)
			var req struct {
				Groups []Group `json:"groups"`
				Save   bool    `json:"save"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if len(req.Groups) > 0 {
				lastGroupsName.Store(req.Groups[0].Name)
			}
			// Simulate fircd overwriting groups.yaml on disk whenever save=true is sent
			if req.Save {
				_ = os.WriteFile(op.GroupsPath(), []byte("OVERWRITTEN_BY_FIRCD_SAVE_TRUE\n"), 0644)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": req.Groups})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))

	// Enable KeenDNS so ReconcileExtNetfilterStrict invokes the iptables runner
	if err := op.SaveExtConfig(ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:    true,
			Interfaces: []string{"tunvless0"},
		},
	}); err != nil {
		t.Fatalf("SaveExtConfig: %v", err)
	}

	// Inject iptables failure during the forward reconcile (putCalls == 1),
	// and succeed during the rollback reconcile (putCalls == 2).
	SetExtIptablesRunner(func(_ context.Context, _ ...string) ([]byte, error) {
		if putCalls.Load() == 1 {
			return []byte("iptables: mock failure"), errors.New("exit status 1")
		}
		return nil, nil
	})
	defer SetExtIptablesRunner(nil)

	newGroups := []Group{
		{
			ID:        "11112222",
			Name:      "MutatedGroup",
			Interface: []string{"tunvless0"},
			Enable:    true,
			Rules:     []Rule{{ID: "r1", Type: "domain", Rule: "mutated.example.com", Enable: true}},
		},
	}
	if _, err := op.ApplyGroups(t.Context(), newGroups); err == nil {
		t.Fatalf("expected ApplyGroups to fail when strict netfilter reconcile fails")
	}

	// Verify runtime was rolled back to InitialGroup and groups.yaml was restored byte-for-byte
	if got := lastGroupsName.Load().(string); got != "InitialGroup" {
		t.Fatalf("expected runtime groups rolled back to InitialGroup, got %q", got)
	}
	afterBytes, err := os.ReadFile(op.GroupsPath())
	if err != nil || string(afterBytes) != string(origBytes) {
		t.Fatalf("expected groups.yaml restored byte-for-byte after rollback, got:\n%s", string(afterBytes))
	}
	if op.IsRecoveryRequired() {
		t.Fatalf("expected recovery_required=false when rollback succeeded, got reason=%q", op.RecoveryReason())
	}
}

func TestR3PreflightSchemaSemanticsAndCandidateCheckBeforeMutation(t *testing.T) {
	tmpDir := t.TempDir()
	awgmDir := filepath.Join(tmpDir, "awg-manager")
	sbDir := filepath.Join(awgmDir, "config.d")
	fircDir := filepath.Join(tmpDir, "firc")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sbDir: %v", err)
	}
	if err := os.MkdirAll(fircDir, 0755); err != nil {
		t.Fatalf("mkdir fircDir: %v", err)
	}

	origTunnels := `{"outbounds":[{"type":"vless","tag":"working-vless","server":"working.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`
	origRouter := `{"route":{"rules":[{"domain":["working.example.com"],"outbound":"working-vless"}],"final":"direct"}}`
	origSettings := `{"singboxRouter":{"routingEngine":"sing-box"}}`
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(origTunnels), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "20-router.json"), []byte(origRouter), 0644); err != nil {
		t.Fatalf("write 20-router.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(awgmDir, "settings.json"), []byte(origSettings), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	op := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
	if err := os.MkdirAll(op.snapshotsDir(), 0700); err != nil {
		t.Fatalf("mkdir snapshotsDir: %v", err)
	}

	writeSnap := func(id, activeEngine string, files []SnapshotFileEntry) {
		t.Helper()
		snap := SystemSnapshot{
			SnapshotMeta: SnapshotMeta{
				ID:           id,
				CreatedAt:    1700000000,
				ActiveEngine: activeEngine,
				FilesCount:   len(files),
			},
			Files: files,
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			t.Fatalf("marshal snap %s: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(op.snapshotsDir(), id+".json"), raw, 0600); err != nil {
			t.Fatalf("write snap %s: %v", id, err)
		}
	}

	b64 := func(s string) string {
		return base64.StdEncoding.EncodeToString([]byte(s))
	}

	assertWorkingFilesUntouched := func(caseName string) {
		t.Helper()
		gotT, err := os.ReadFile(filepath.Join(sbDir, "10-tunnels.json"))
		if err != nil || string(gotT) != origTunnels {
			t.Fatalf("[%s] working 10-tunnels.json was modified or deleted: err=%v got=%q", caseName, err, string(gotT))
		}
		gotR, err := os.ReadFile(filepath.Join(sbDir, "20-router.json"))
		if err != nil || string(gotR) != origRouter {
			t.Fatalf("[%s] working 20-router.json was modified or deleted: err=%v got=%q", caseName, err, string(gotR))
		}
		gotS, err := os.ReadFile(filepath.Join(awgmDir, "settings.json"))
		if err != nil || string(gotS) != origSettings {
			t.Fatalf("[%s] working settings.json was modified or deleted: err=%v got=%q", caseName, err, string(gotS))
		}
	}

	invalidCases := []struct {
		name         string
		activeEngine string
		files        []SnapshotFileEntry
	}{
		{
			name:         "empty-mandatory-file",
			activeEngine: "sing-box",
			files: []SnapshotFileEntry{
				{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64("   \n\t ")},
			},
		},
		{
			name:         "empty-json-object-in-settings",
			activeEngine: "sing-box",
			files: []SnapshotFileEntry{
				{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64(origTunnels)},
				{Scope: "awgm", RelPath: "settings.json", Mode: 0644, DataB64: b64(`{}`)},
			},
		},
		{
			name:         "unknown-field-in-settings-json",
			activeEngine: "sing-box",
			files: []SnapshotFileEntry{
				{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64(origTunnels)},
				{Scope: "awgm", RelPath: "settings.json", Mode: 0644, DataB64: b64(`{"unknownEvilField":true}`)},
			},
		},
		{
			name:         "invalid-routing-engine-in-settings-json",
			activeEngine: "sing-box",
			files: []SnapshotFileEntry{
				{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64(origTunnels)},
				{Scope: "awgm", RelPath: "settings.json", Mode: 0644, DataB64: b64(`{"singboxRouter":{"routingEngine":"nonexistent-engine"}}`)},
			},
		},
		{
			name:         "missing-mandatory-10-tunnels-when-router-present",
			activeEngine: "sing-box",
			files: []SnapshotFileEntry{
				{Scope: "singbox", RelPath: "20-router.json", Mode: 0644, DataB64: b64(`{"route":{"rules":[],"final":"direct"}}`)},
			},
		},
		{
			name:         "router-references-nonexistent-outbound",
			activeEngine: "sing-box",
			files: []SnapshotFileEntry{
				{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64(origTunnels)},
				{Scope: "singbox", RelPath: "20-router.json", Mode: 0644, DataB64: b64(`{"route":{"rules":[{"domain":["example.com"],"outbound":"ghost-outbound"}],"final":"direct"}}`)},
			},
		},
		{
			name:         "client-routes-references-nonexistent-outbound",
			activeEngine: "sing-box",
			files: []SnapshotFileEntry{
				{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64(origTunnels)},
				{Scope: "awgm", RelPath: "client-routes.json", Mode: 0644, DataB64: b64(`[{"ip":"192.168.1.50","outbound_tag":"ghost-outbound"}]`)},
			},
		},
		{
			name:         "incompatible-singbox-transport-xhttp",
			activeEngine: "sing-box",
			files: []SnapshotFileEntry{
				{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64(`{"outbounds":[{"type":"vless","tag":"bad-tr","server":"1.2.3.4","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555","transport":{"type":"xhttp"}}]}`)},
			},
		},
		{
			name:         "incompatible-singbox-transport-on-wireguard",
			activeEngine: "sing-box",
			files: []SnapshotFileEntry{
				{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64(`{"outbounds":[{"type":"wireguard","tag":"bad-wg","server":"1.2.3.4","server_port":51820,"private_key":"AAAA","transport":{"type":"ws"}}]}`)},
			},
		},
		{
			name:         "syntactically-valid-yaml-but-invalid-firc-schema",
			activeEngine: "firc",
			files: []SnapshotFileEntry{
				{Scope: "firc", RelPath: "tunnels.yaml", Mode: 0600, DataB64: b64("foo: bar\n")},
			},
		},
		{
			name:         "firc-groups-references-missing-tunnel-device",
			activeEngine: "firc",
			files: []SnapshotFileEntry{
				{Scope: "firc", RelPath: "tunnels.yaml", Mode: 0600, DataB64: b64("tunnels:\n  - id: t1\n    device: tunvless0\n    enable: true\n    sources:\n      - id: s1\n        kind: link\n        link: vless://11111111-2222-3333-4444-555555555555@example.com:443?type=tcp&security=tls#n1\n")},
				{Scope: "firc", RelPath: "groups.yaml", Mode: 0644, DataB64: b64("groups:\n  - id: g1\n    name: G1\n    interface:\n      - tunvless99\n    enable: true\n    rules:\n      - id: r1\n        type: domain\n        rule: example.com\n        enable: true\n")},
			},
		},
	}

	for _, tc := range invalidCases {
		writeSnap(tc.name, tc.activeEngine, tc.files)
		if _, err := op.RestoreSystemSnapshot(t.Context(), tc.name, sbDir); err == nil {
			t.Fatalf("[%s] expected RestoreSystemSnapshot to reject invalid snapshot in preflight", tc.name)
		}
		assertWorkingFilesUntouched(tc.name)
	}

	// Test staged Sing-box candidate check (SetSingboxValidator / sing-box check) rejection BEFORE modifying working files
	var validatorCalledWithStagedFile atomic.Bool
	op.SetSingboxValidator(func(_ context.Context, stagedDir string) error {
		b, err := os.ReadFile(filepath.Join(stagedDir, "10-tunnels.json"))
		if err == nil && strings.Contains(string(b), "candidate-vless") {
			validatorCalledWithStagedFile.Store(true)
		}
		return errors.New("sing-box check: FATAL[0000] mock candidate configuration rejected by sing-box check")
	})
	defer op.SetSingboxValidator(nil)

	candTunnels := `{"outbounds":[{"type":"vless","tag":"candidate-vless","server":"cand.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`
	writeSnap("candidate-check-fail", "sing-box", []SnapshotFileEntry{
		{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64(candTunnels)},
	})
	if _, err := op.RestoreSystemSnapshot(t.Context(), "candidate-check-fail", sbDir); err == nil {
		t.Fatalf("expected RestoreSystemSnapshot to fail when staged sing-box candidate check fails")
	}
	if !validatorCalledWithStagedFile.Load() {
		t.Fatalf("expected SingboxValidator to be invoked on staged directory containing candidate-vless")
	}
	assertWorkingFilesUntouched("candidate-check-fail")
}

// TestSubprocessCrashWorker is executed in a separate child OS process by
// TestR1DurableJournalSubprocessCrashAndStartupRecovery and TestR1CommitBoundaryFaultInjectionAndSubprocessRecovery
// to simulate hard process death (os.Exit(42)) at specific file stages or commit-boundary faults.
func TestSubprocessCrashWorker(t *testing.T) {
	crashOp := os.Getenv("AWGM_TEST_CRASH_OP")
	if crashOp == "" {
		t.Skip("subprocess worker only runs when AWGM_TEST_CRASH_OP is set")
	}
	crashStage := os.Getenv("AWGM_TEST_CRASH_STAGE")
	crashStep, _ := strconv.Atoi(os.Getenv("AWGM_TEST_CRASH_STEP"))
	commitFaultMode := os.Getenv("AWGM_TEST_COMMIT_FAULT_MODE")
	sbDir := os.Getenv("AWGM_TEST_SB_DIR")
	fircDir := os.Getenv("AWGM_TEST_FIRC_DIR")
	snapID := os.Getenv("AWGM_TEST_SNAP_ID")

	op := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
	op.SetSingboxValidator(func(_ context.Context, _ string) error { return nil })

	switch commitFaultMode {
	case "commit_dir_sync_error_then_exit":
		var failedCommittedSync atomic.Bool
		op.SetDirSyncHook(func(dir string) error {
			jBytes, err := os.ReadFile(filepath.Join(dir, "migration-journal.json"))
			if err == nil && strings.Contains(string(jBytes), `"stage": "committed"`) && !failedCommittedSync.Swap(true) {
				return errors.New("injected syncParentDir failure on Stage=committed after rename")
			}
			return nil
		})
	case "commit_dir_sync_hard_crash":
		op.SetDirSyncHook(func(dir string) error {
			jBytes, err := os.ReadFile(filepath.Join(dir, "migration-journal.json"))
			if err == nil && strings.Contains(string(jBytes), `"stage": "committed"`) {
				os.Exit(42)
			}
			return nil
		})
	case "commit_readback_corrupt_then_exit":
		var corruptedCommitted atomic.Bool
		op.SetDirSyncHook(func(dir string) error {
			jPath := filepath.Join(dir, "migration-journal.json")
			jBytes, err := os.ReadFile(jPath)
			if err == nil && strings.Contains(string(jBytes), `"stage": "committed"`) && !corruptedCommitted.Swap(true) {
				// Mutate on-disk bytes after os.Rename so real os.ReadFile readback verification fails
				_ = os.WriteFile(jPath, append(jBytes, ' '), 0600)
			}
			return nil
		})
	case "post_remove_dir_sync_error_then_exit":
		var sawCommitted atomic.Bool
		var failedPostRemoveSync atomic.Bool
		op.SetDirSyncHook(func(dir string) error {
			jPath := filepath.Join(dir, "migration-journal.json")
			jBytes, err := os.ReadFile(jPath)
			if err == nil && strings.Contains(string(jBytes), `"stage": "committed"`) {
				sawCommitted.Store(true)
				return nil
			}
			if sawCommitted.Load() && os.IsNotExist(err) && !failedPostRemoveSync.Swap(true) {
				return errors.New("injected syncParentDir failure after committed journal remove")
			}
			return nil
		})
	}

	op.SetMigrationStepHook(func(operation, stage string, stepIndex int, _ string) error {
		if operation == crashOp && stage == crashStage && stepIndex == crashStep {
			os.Exit(42)
		}
		return nil
	})

	switch crashOp {
	case "snapshot_restore":
		_, _ = op.RestoreSystemSnapshot(context.Background(), snapID, sbDir)
	case "forward_migrate":
		_, _ = op.MigrateFromSingboxWithOptions(context.Background(), sbDir, &MigrationOptions{})
	}
	t.Fatalf("expected subprocess to exit(42) at op=%s stage=%s step=%d mode=%s, but operation returned normally", crashOp, crashStage, crashStep, commitFaultMode)
}

func TestR1DurableJournalSubprocessCrashAndStartupRecovery(t *testing.T) {
	runSubprocessCrash := func(t *testing.T, crashOp, crashStage string, crashStep int, sbDir, fircDir, snapID string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocessCrashWorker$")
		cmd.Env = append(os.Environ(),
			"AWGM_TEST_CRASH_OP="+crashOp,
			"AWGM_TEST_CRASH_STAGE="+crashStage,
			"AWGM_TEST_CRASH_STEP="+strconv.Itoa(crashStep),
			"AWGM_TEST_SB_DIR="+sbDir,
			"AWGM_TEST_FIRC_DIR="+fircDir,
			"AWGM_TEST_SNAP_ID="+snapID,
		)
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 42 {
			t.Fatalf("expected child process to exit with code 42 for %s/%s/%d, got err=%v out=%s", crashOp, crashStage, crashStep, err, string(out))
		}
	}

	// Sub-test A: Crash during RestoreSystemSnapshot at removing_stale (step 0) and writing_files (step 0 and step 1)
	for _, crashPoint := range []struct {
		stage string
		step  int
	}{
		{stage: "removing_stale", step: 0},
		{stage: "writing_files", step: 0},
		{stage: "writing_files", step: 1},
	} {
		tmpDir := t.TempDir()
		awgmDir := filepath.Join(tmpDir, "awg-manager")
		sbDir := filepath.Join(awgmDir, "config.d")
		fircDir := filepath.Join(tmpDir, "firc")
		_ = os.MkdirAll(sbDir, 0755)
		_ = os.MkdirAll(fircDir, 0755)

		// First create a valid target snapshot that we will later restore
		snapTunnels := `{"outbounds":[{"type":"vless","tag":"snap-vless","server":"snap.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`
		snapRouter := `{"route":{"rules":[{"domain":["snap.example.com"],"outbound":"snap-vless"}],"final":"direct"}}`
		_ = os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(snapTunnels), 0644)
		_ = os.WriteFile(filepath.Join(sbDir, "20-router.json"), []byte(snapRouter), 0644)
		_ = os.WriteFile(filepath.Join(awgmDir, "settings.json"), []byte(`{"singboxRouter":{"routingEngine":"sing-box"}}`), 0644)

		initOp := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
		snapMeta, err := initOp.CreateSystemSnapshot(sbDir, "Target Snap", "manual", "sing-box")
		if err != nil {
			t.Fatalf("CreateSystemSnapshot: %v", err)
		}

		// Now mutate disk state before the restore (including a stale 99-extra.json that wasn't in the snapshot,
		// and removing 20-router.json so restoring the snapshot would create 20-router.json as a new file!).
		preCrashTunnels := `{"outbounds":[{"type":"vless","tag":"pre-crash-vless","server":"precrash.example.com","server_port":443,"uuid":"22222222-2222-3333-4444-555555555555"}]}`
		preCrashStale := `{"outbounds":[{"type":"direct","tag":"stale-direct"}]}`
		_ = os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(preCrashTunnels), 0644)
		_ = os.WriteFile(filepath.Join(sbDir, "99-extra.json"), []byte(preCrashStale), 0644)
		_ = os.Remove(filepath.Join(sbDir, "20-router.json"))

		// Spawn subprocess that crashes right after crashPoint
		runSubprocessCrash(t, "snapshot_restore", crashPoint.stage, crashPoint.step, sbDir, fircDir, snapMeta.ID)

		// Verify durable journal exists on disk in uncommitted stage right after crash
		jBytes, jErr := os.ReadFile(filepath.Join(fircDir, "migration-journal.json"))
		if jErr != nil || !strings.Contains(string(jBytes), crashPoint.stage) {
			t.Fatalf("expected uncommitted migration-journal.json at stage %s after crash, err=%v data=%s", crashPoint.stage, jErr, string(jBytes))
		}

		// Start a new Operator (simulating restart after process crash / power loss)
		restartedOp := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})

		// 1. Verify startup recovery automatically rolled back all files to exact pre-crash state:
		//    - 10-tunnels.json restored to preCrashTunnels
		//    - 99-extra.json (which was deleted at removing_stale step 0) restored to preCrashStale
		//    - 20-router.json (which did NOT exist before restore) removed!
		gotTunnels, err := os.ReadFile(filepath.Join(sbDir, "10-tunnels.json"))
		if err != nil || string(gotTunnels) != preCrashTunnels {
			t.Fatalf("[%s:%d] expected 10-tunnels.json rolled back on startup to preCrashTunnels, got err=%v data=%s", crashPoint.stage, crashPoint.step, err, string(gotTunnels))
		}
		gotStale, err := os.ReadFile(filepath.Join(sbDir, "99-extra.json"))
		if err != nil || string(gotStale) != preCrashStale {
			t.Fatalf("[%s:%d] expected deleted 99-extra.json restored on startup, got err=%v data=%s", crashPoint.stage, crashPoint.step, err, string(gotStale))
		}
		if _, stErr := os.Stat(filepath.Join(sbDir, "20-router.json")); !os.IsNotExist(stErr) {
			t.Fatalf("[%s:%d] expected newly created 20-router.json (absent before restore) to be removed on startup rollback", crashPoint.stage, crashPoint.step)
		}

		// 2. Verify ordinary mutations are blocked until recovery is explicitly resolved
		if !restartedOp.IsRecoveryRequired() {
			t.Fatalf("[%s:%d] expected restartedOp.IsRecoveryRequired()=true after crash recovery", crashPoint.stage, crashPoint.step)
		}
		if _, mErr := restartedOp.ApplyTunnels(t.Context(), []Tunnel{}, false); !errors.Is(mErr, ErrRecoveryRequired) {
			t.Fatalf("[%s:%d] expected ApplyTunnels blocked by ErrRecoveryRequired, got %v", crashPoint.stage, crashPoint.step, mErr)
		}
		if _, mErr := restartedOp.RestoreSystemSnapshot(t.Context(), snapMeta.ID, sbDir); !errors.Is(mErr, ErrRecoveryRequired) {
			t.Fatalf("[%s:%d] expected RestoreSystemSnapshot blocked by ErrRecoveryRequired before resolution, got %v", crashPoint.stage, crashPoint.step, mErr)
		}

		// 3. Resolve recovery via ClearRecovery and verify journal is cleaned up
		if cErr := restartedOp.ClearRecovery(); cErr != nil {
			t.Fatalf("[%s:%d] ClearRecovery: %v", crashPoint.stage, crashPoint.step, cErr)
		}
		if _, stErr := os.Stat(restartedOp.MigrationJournalPath()); !os.IsNotExist(stErr) {
			t.Fatalf("[%s:%d] expected migration-journal.json removed after ClearRecovery", crashPoint.stage, crashPoint.step)
		}
	}

	// Sub-test D: Crash during MigrateFromSingboxWithOptions (forward_migrate) at writing_files step 0
	{
		tmpDir := t.TempDir()
		awgmDir := filepath.Join(tmpDir, "awg-manager")
		sbDir := filepath.Join(awgmDir, "config.d")
		fircDir := filepath.Join(tmpDir, "firc")
		_ = os.MkdirAll(sbDir, 0755)
		_ = os.MkdirAll(fircDir, 0755)

		sb10 := `{"outbounds":[{"type":"vless","tag":"fwd-sb","server":"fwd.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`
		_ = os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(sb10), 0644)

		initOp := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
		origTunnels := []Tunnel{
			{
				ID: "orig-firc", Device: "tunvless0", Enable: true,
				Sources: []TunnelSource{{ID: "s1", Kind: "link", Name: "Orig", Link: "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@orig-firc.example.com:443?security=tls&sni=orig-firc.example.com#Orig"}},
			},
		}
		_ = initOp.SaveTunnelsFile(origTunnels)
		// Note: groups.yaml does NOT exist before forward_migrate!

		runSubprocessCrash(t, "forward_migrate", "writing_files", 0, sbDir, fircDir, "")

		restartedOp := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
		restoredTunnels, err := restartedOp.LoadTunnelsFile()
		if err != nil || len(restoredTunnels) != 1 || restoredTunnels[0].ID != "orig-firc" {
			t.Fatalf("expected tunnels.yaml rolled back to orig-firc after forward_migrate crash, got err=%v tunnels=%+v", err, restoredTunnels)
		}
		if _, err := os.Stat(filepath.Join(fircDir, "groups.yaml")); !os.IsNotExist(err) {
			t.Fatalf("expected absent groups.yaml to remain absent after forward_migrate crash rollback")
		}
		if !restartedOp.IsRecoveryRequired() {
			t.Fatalf("expected IsRecoveryRequired()=true after forward_migrate crash recovery")
		}
		_ = restartedOp.ClearRecovery()
	}
}

func TestV1SingboxCandidateValidatorFailClosed(t *testing.T) {
	tmpDir := t.TempDir()
	awgmDir := filepath.Join(tmpDir, "awg-manager")
	sbDir := filepath.Join(awgmDir, "config.d")
	fircDir := filepath.Join(tmpDir, "firc")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sbDir: %v", err)
	}
	if err := os.MkdirAll(fircDir, 0755); err != nil {
		t.Fatalf("mkdir fircDir: %v", err)
	}

	orig10 := `{"outbounds":[{"type":"vless","tag":"working-vless","server":"working.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(orig10), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(awgmDir, "settings.json"), []byte(`{"singboxRouter":{"routingEngine":"firc"}}`), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	op := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
	// Snapshot while validator is mocked as valid
	op.SetSingboxValidator(func(_ context.Context, _ string) error { return nil })
	snapMeta, err := op.CreateSystemSnapshot(sbDir, "Valid Snap", "manual", "sing-box")
	if err != nil {
		t.Fatalf("CreateSystemSnapshot: %v", err)
	}
	if err := op.SaveTunnelsFile([]Tunnel{
		{
			ID: "t1", Device: "tunvless0", Enable: true,
			Sources: []TunnelSource{{ID: "s1", Kind: "link", Name: "N1", Link: "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@rev.example.com:443?security=tls&sni=rev.example.com#N1"}},
		},
	}); err != nil {
		t.Fatalf("SaveTunnelsFile: %v", err)
	}
	op.SetSingboxValidator(nil)

	// Mutate working 10-tunnels.json so we can verify it is NEVER modified when restore fails closed
	live10 := `{"outbounds":[{"type":"vless","tag":"live-untouched","server":"live.example.com","server_port":443,"uuid":"22222222-2222-3333-4444-555555555555"}]}`
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(live10), 0644); err != nil {
		t.Fatalf("write live 10-tunnels.json: %v", err)
	}

	nonExecPath := filepath.Join(tmpDir, "sing-box-nonexec")
	if err := os.WriteFile(nonExecPath, []byte("not an executable binary"), 0644); err != nil {
		t.Fatalf("write nonExecPath: %v", err)
	}

	unavailableCases := []struct {
		name  string
		sbBin string
	}{
		{name: "empty_binary_path", sbBin: ""},
		{name: "missing_binary", sbBin: filepath.Join(tmpDir, "does-not-exist-sing-box")},
		{name: "non_executable_binary", sbBin: nonExecPath},
	}

	for _, tc := range unavailableCases {
		op.SetSingboxBinary(tc.sbBin)

		// Restore MUST fail-closed with ErrSingboxValidatorUnavailable
		if _, err := op.RestoreSystemSnapshot(t.Context(), snapMeta.ID, sbDir); !errors.Is(err, ErrSingboxValidatorUnavailable) {
			t.Fatalf("[%s] expected RestoreSystemSnapshot to fail closed with ErrSingboxValidatorUnavailable, got %v", tc.name, err)
		}

		// Verify working 10-tunnels.json was not touched
		got10, rErr := os.ReadFile(filepath.Join(sbDir, "10-tunnels.json"))
		if rErr != nil || string(got10) != live10 {
			t.Fatalf("[%s] working 10-tunnels.json was modified: err=%v got=%s", tc.name, rErr, string(got10))
		}
	}

	// 3. Test real binary execution checker failure (using os.Args[0], the compiled test binary itself, which is executable and exits non-zero on `check -C <tmpDir>`)
	op.SetSingboxBinary(os.Args[0])
	if _, err := op.RestoreSystemSnapshot(t.Context(), snapMeta.ID, sbDir); err == nil || errors.Is(err, ErrSingboxValidatorUnavailable) {
		t.Fatalf("expected RestoreSystemSnapshot to fail with sing-box check execution error (not unavailable), got %v", err)
	}
	got10, rErr := os.ReadFile(filepath.Join(sbDir, "10-tunnels.json"))
	if rErr != nil || string(got10) != live10 {
		t.Fatalf("working 10-tunnels.json was modified after checker failure: err=%v got=%s", rErr, string(got10))
	}
}

func TestV2UnifiedStagingManifestExcludesDeletedBaseAndFailsOnCopyError(t *testing.T) {
	tmpDir := t.TempDir()
	awgmDir := filepath.Join(tmpDir, "awg-manager")
	sbDir := filepath.Join(awgmDir, "config.d")
	fircDir := filepath.Join(tmpDir, "firc")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sbDir: %v", err)
	}
	if err := os.MkdirAll(fircDir, 0755); err != nil {
		t.Fatalf("mkdir fircDir: %v", err)
	}

	// Sub-test A: 00-base.json exists on disk in singboxConfigDir with outbound "base-only-outbound",
	// and is ABSENT from the snapshot. Because RestoreSystemSnapshot clears stale root files
	// (which deletes 00-base.json on apply), the staged directory
	// passed to the checker MUST NOT contain 00-base.json, and validOutboundTags MUST NOT contain "base-only-outbound".
	diskBase := `{"log":{"level":"info"},"outbounds":[{"type":"direct","tag":"base-only-outbound"}]}`
	orig10 := `{"outbounds":[{"type":"vless","tag":"snap-vless","server":"snap.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`
	if err := os.WriteFile(filepath.Join(sbDir, "00-base.json"), []byte(diskBase), 0644); err != nil {
		t.Fatalf("write 00-base.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(orig10), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(awgmDir, "settings.json"), []byte(`{"singboxRouter":{"routingEngine":"sing-box"}}`), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	op := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
	if err := os.MkdirAll(op.snapshotsDir(), 0700); err != nil {
		t.Fatalf("mkdir snapshotsDir: %v", err)
	}

	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	// 1. Snapshot without 00-base.json whose 20-router.json references "base-only-outbound" must be rejected
	// because 00-base.json will be deleted on apply!
	snapRefDeletedBase := SystemSnapshot{
		SnapshotMeta: SnapshotMeta{ID: "ref-deleted-base", CreatedAt: 1700000000, ActiveEngine: "sing-box", FilesCount: 2},
		Files: []SnapshotFileEntry{
			{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64(orig10)},
			{Scope: "singbox", RelPath: "20-router.json", Mode: 0644, DataB64: b64(`{"route":{"rules":[{"domain":["example.com"],"outbound":"base-only-outbound"}],"final":"direct"}}`)},
		},
	}
	rawRefSnap, _ := json.Marshal(snapRefDeletedBase)
	_ = os.WriteFile(filepath.Join(op.snapshotsDir(), "ref-deleted-base.json"), rawRefSnap, 0600)

	op.SetSingboxValidator(func(_ context.Context, _ string) error { return nil })
	if _, err := op.RestoreSystemSnapshot(t.Context(), "ref-deleted-base", sbDir); err == nil {
		t.Fatalf("expected RestoreSystemSnapshot to reject reference to outbound in 00-base.json when 00-base.json is absent from snapshot")
	}
	// Verify 00-base.json on disk is still untouched
	if gotBase, err := os.ReadFile(filepath.Join(sbDir, "00-base.json")); err != nil || string(gotBase) != diskBase {
		t.Fatalf("expected 00-base.json untouched after rejected restore, got err=%v data=%s", err, string(gotBase))
	}

	// 2. Snapshot without 00-base.json and valid 10-tunnels.json: verify the checker sees the exact future file set
	// (only 10-tunnels.json, WITHOUT 00-base.json), and after apply 00-base.json is indeed removed so staged == applied!
	var stagedFilesSeen []string
	op.SetSingboxValidator(func(_ context.Context, stagedDir string) error {
		ents, err := os.ReadDir(stagedDir)
		if err != nil {
			return err
		}
		stagedFilesSeen = nil
		for _, e := range ents {
			stagedFilesSeen = append(stagedFilesSeen, e.Name())
		}
		return nil
	})

	snapNoBase := SystemSnapshot{
		SnapshotMeta: SnapshotMeta{ID: "no-base-snap", CreatedAt: 1700000001, ActiveEngine: "sing-box", FilesCount: 1},
		Files: []SnapshotFileEntry{
			{Scope: "singbox", RelPath: "10-tunnels.json", Mode: 0644, DataB64: b64(orig10)},
		},
	}
	rawNoBase, _ := json.Marshal(snapNoBase)
	_ = os.WriteFile(filepath.Join(op.snapshotsDir(), "no-base-snap.json"), rawNoBase, 0600)

	if _, err := op.RestoreSystemSnapshot(t.Context(), "no-base-snap", sbDir); err != nil {
		t.Fatalf("RestoreSystemSnapshot no-base-snap: %v", err)
	}
	if len(stagedFilesSeen) != 1 || stagedFilesSeen[0] != "10-tunnels.json" {
		t.Fatalf("expected checker to see exact future file set [10-tunnels.json] without 00-base.json, got %v", stagedFilesSeen)
	}
	// Compare with actual applied root .json files in sbDir
	appliedEnts, _ := os.ReadDir(sbDir)
	var appliedFiles []string
	for _, e := range appliedEnts {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			appliedFiles = append(appliedFiles, e.Name())
		}
	}
	if !slices.Equal(stagedFilesSeen, appliedFiles) {
		t.Fatalf("staged file set %v does not match final applied file set %v", stagedFilesSeen, appliedFiles)
	}

	// Sub-test B: Error during staging of candidate files aborts BEFORE any working file is modified
	_ = os.WriteFile(filepath.Join(sbDir, "00-base.json"), []byte(diskBase), 0644)
	working10 := `{"outbounds":[{"type":"direct","tag":"working-direct"}]}`
	_ = os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(working10), 0644)

	op.SetSingboxStageCopyHook(func(relPath string) error {
		if relPath == "10-tunnels.json" {
			return errors.New("simulated I/O failure copying 10-tunnels.json into staging")
		}
		return nil
	})
	defer op.SetSingboxStageCopyHook(nil)

	if _, err := op.RestoreSystemSnapshot(t.Context(), "no-base-snap", sbDir); err == nil {
		t.Fatalf("expected RestoreSystemSnapshot to abort when staging copy of 10-tunnels.json fails")
	}
	got10, _ := os.ReadFile(filepath.Join(sbDir, "10-tunnels.json"))
	if string(got10) != working10 {
		t.Fatalf("working 10-tunnels.json was modified despite staging copy error: %s", string(got10))
	}
	if gotBase, _ := os.ReadFile(filepath.Join(sbDir, "00-base.json")); string(gotBase) != diskBase {
		t.Fatalf("00-base.json was removed despite staging copy error: %s", gotBase)
	}
}

func TestC1CommitBoundaryPostRenameSyncReadbackAndCleanupFaults(t *testing.T) {
	TestR1CommitBoundaryFaultInjectionAndSubprocessRecovery(t)
}

func TestR1CommitBoundaryFaultInjectionAndSubprocessRecovery(t *testing.T) {
	const (
		snap10       = `{"outbounds":[{"type":"vless","tag":"snap-vless","server":"snap.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`
		snap20       = `{"route":{"rules":[{"domain":["snap.example.com"],"outbound":"snap-vless"}],"final":"direct"}}`
		snapSubs     = `[{"id":"sub-snap","label":"snap-vless","url":"https://snap.example.com/sub","enabled":true}]`
		snapSettings = `{"singboxRouter":{"routingEngine":"sing-box"}}`
		snapFircTun  = "configVersion: 0.7.0\ntunnels:\n  - id: snap-tun\n    device: tunvless0\n    enable: true\n    sources:\n      - id: s1\n        kind: link\n        name: SnapNode\n        link: vless://11111111-2222-3333-4444-555555555555@snap.example.com:443?security=tls&sni=snap.example.com#SnapNode\n"
		snapFircGrp  = "configVersion: 0.7.0\ngroups:\n  - id: 11223344\n    name: SnapGrp\n    interface:\n      - tunvless0\n    enable: true\n    rules:\n      - id: r1\n        type: domain\n        rule: snap.example.com\n        enable: true\n"

		work10       = `{"outbounds":[{"type":"vless","tag":"work-vless","server":"work.example.com","server_port":443,"uuid":"22222222-2222-3333-4444-555555555555"}]}`
		workStale99  = `{"outbounds":[{"type":"direct","tag":"stale-99"}]}`
		workSubs     = `[{"id":"sub-work","label":"work-vless","url":"https://work.example.com/sub","enabled":true}]`
		workSettings = `{"singboxRouter":{"routingEngine":"firc"}}`
		workFircTun  = "configVersion: 0.7.0\ntunnels:\n  - id: work-tun\n    device: tunvless0\n    enable: true\n    sources:\n      - id: s1\n        kind: link\n        name: WorkNode\n        link: vless://22222222-2222-3333-4444-555555555555@work.example.com:443?security=tls&sni=work.example.com#WorkNode\n"
		workFircGrp  = "configVersion: 0.7.0\ngroups:\n  - id: 55667788\n    name: WorkGrp\n    interface:\n      - tunvless0\n    enable: true\n    rules:\n      - id: r1\n        type: domain\n        rule: work.example.com\n        enable: true\n"
	)

	setupMultiFileEnv := func(t *testing.T) (sbDir, awgmDir, fircDir, snapID string, op *Operator) {
		t.Helper()
		tmpDir := t.TempDir()
		awgmDir = filepath.Join(tmpDir, "awg-manager")
		sbDir = filepath.Join(awgmDir, "config.d")
		fircDir = filepath.Join(tmpDir, "firc")
		_ = os.MkdirAll(sbDir, 0755)
		_ = os.MkdirAll(fircDir, 0755)

		_ = os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(snap10), 0644)
		_ = os.WriteFile(filepath.Join(sbDir, "20-router.json"), []byte(snap20), 0644)
		_ = os.WriteFile(filepath.Join(awgmDir, "subscriptions.json"), []byte(snapSubs), 0644)
		_ = os.WriteFile(filepath.Join(awgmDir, "settings.json"), []byte(snapSettings), 0644)
		_ = os.WriteFile(filepath.Join(fircDir, "tunnels.yaml"), []byte(snapFircTun), 0600)
		_ = os.WriteFile(filepath.Join(fircDir, "groups.yaml"), []byte(snapFircGrp), 0644)

		op = NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
		op.SetSingboxValidator(func(_ context.Context, _ string) error { return nil })
		snapMeta, err := op.CreateSystemSnapshot(sbDir, "MultiFile Snap", "manual", "sing-box")
		if err != nil {
			t.Fatalf("CreateSystemSnapshot: %v", err)
		}

		// Mutate all working files before restore:
		// - overwrite 10-tunnels.json, subscriptions.json, settings.json, tunnels.yaml, groups.yaml
		// - remove 20-router.json (so restore recreates it)
		// - add 99-stale.json (so restore deletes it)
		_ = os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(work10), 0644)
		_ = os.Remove(filepath.Join(sbDir, "20-router.json"))
		_ = os.WriteFile(filepath.Join(sbDir, "99-stale.json"), []byte(workStale99), 0644)
		_ = os.WriteFile(filepath.Join(awgmDir, "subscriptions.json"), []byte(workSubs), 0644)
		_ = os.WriteFile(filepath.Join(awgmDir, "settings.json"), []byte(workSettings), 0644)
		_ = os.WriteFile(filepath.Join(fircDir, "tunnels.yaml"), []byte(workFircTun), 0600)
		_ = os.WriteFile(filepath.Join(fircDir, "groups.yaml"), []byte(workFircGrp), 0644)

		return sbDir, awgmDir, fircDir, snapMeta.ID, op
	}

	assertEntireFileSet := func(t *testing.T, label, sbDir, awgmDir, fircDir string, wantCommitted bool) {
		t.Helper()
		want10, wantSubs, wantSet, wantFT, wantFG := work10, workSubs, workSettings, workFircTun, workFircGrp
		if wantCommitted {
			want10, wantSubs, wantSet, wantFT, wantFG = snap10, snapSubs, snapSettings, snapFircTun, snapFircGrp
		}
		for _, chk := range []struct {
			name string
			path string
			want string
		}{
			{"10-tunnels.json", filepath.Join(sbDir, "10-tunnels.json"), want10},
			{"subscriptions.json", filepath.Join(awgmDir, "subscriptions.json"), wantSubs},
			{"settings.json", filepath.Join(awgmDir, "settings.json"), wantSet},
			{"tunnels.yaml", filepath.Join(fircDir, "tunnels.yaml"), wantFT},
			{"groups.yaml", filepath.Join(fircDir, "groups.yaml"), wantFG},
		} {
			got, err := os.ReadFile(chk.path)
			if err != nil || string(got) != chk.want {
				t.Fatalf("[%s] %s integrity mismatch (wantCommitted=%v): err=%v got=%q want=%q",
					label, chk.name, wantCommitted, err, string(got), chk.want)
			}
		}
		if wantCommitted {
			got20, err := os.ReadFile(filepath.Join(sbDir, "20-router.json"))
			if err != nil || string(got20) != snap20 {
				t.Fatalf("[%s] expected committed 20-router.json==snap20, got err=%v data=%q", label, err, string(got20))
			}
			if _, err := os.Stat(filepath.Join(sbDir, "99-stale.json")); !os.IsNotExist(err) {
				t.Fatalf("[%s] expected stale 99-stale.json to remain deleted in committed state, stat err=%v", label, err)
			}
		} else {
			if _, err := os.Stat(filepath.Join(sbDir, "20-router.json")); !os.IsNotExist(err) {
				t.Fatalf("[%s] expected newly-created 20-router.json to be removed on rollback, stat err=%v", label, err)
			}
			gotStale, err := os.ReadFile(filepath.Join(sbDir, "99-stale.json"))
			if err != nil || string(gotStale) != workStale99 {
				t.Fatalf("[%s] expected deleted 99-stale.json restored on rollback, got err=%v data=%q", label, err, string(gotStale))
			}
		}
	}

	assertRecoveryAndSubsequentRestore := func(t *testing.T, label string, op *Operator, sbDir, awgmDir, fircDir, snapID string) {
		t.Helper()
		if !op.IsRecoveryRequired() {
			t.Fatalf("[%s] expected IsRecoveryRequired()=true before recovery resolution", label)
		}
		if _, err := op.ApplyTunnels(t.Context(), []Tunnel{}, false); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("[%s] expected ApplyTunnels blocked by ErrRecoveryRequired, got %v", label, err)
		}
		if _, err := op.RestoreSystemSnapshot(t.Context(), snapID, sbDir); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("[%s] expected RestoreSystemSnapshot blocked by ErrRecoveryRequired, got %v", label, err)
		}
		if err := op.ClearRecovery(); err != nil {
			t.Fatalf("[%s] ClearRecovery failed: %v", label, err)
		}
		if op.IsRecoveryRequired() {
			t.Fatalf("[%s] expected IsRecoveryRequired()=false after ClearRecovery", label)
		}
		if _, err := os.Stat(op.MigrationJournalPath()); !os.IsNotExist(err) {
			t.Fatalf("[%s] expected migration-journal.json removed after ClearRecovery", label)
		}
		assertEntireFileSet(t, label+"-after-ClearRecovery", sbDir, awgmDir, fircDir, true)
		op.SetSingboxValidator(func(_ context.Context, _ string) error { return nil })
		if _, err := op.RestoreSystemSnapshot(t.Context(), snapID, sbDir); err != nil {
			t.Fatalf("[%s] expected subsequent RestoreSystemSnapshot to succeed after ClearRecovery, got %v", label, err)
		}
	}

	// 1. Pre-publication fault BEFORE os.Rename of Stage="committed" (BeforeCommitMarkerWrite)
	//    -> Stage="committed" is never published; entire file set MUST be rolled back to working state.
	{
		sbDir, awgmDir, fircDir, snapID, op := setupMultiFileEnv(t)
		op.SetMigrationJournalCommitFaultHooks(MigrationJournalCommitFaultHooks{
			BeforeCommitMarkerWrite: func(_ *MigrationJournal) error {
				return errors.New("injected pre-rename fault before Stage=committed")
			},
		})
		if _, err := op.RestoreSystemSnapshot(t.Context(), snapID, sbDir); err == nil {
			t.Fatalf("expected RestoreSystemSnapshot to fail when BeforeCommitMarkerWrite fails")
		}
		op.SetMigrationJournalCommitFaultHooks(MigrationJournalCommitFaultHooks{})

		assertEntireFileSet(t, "pre-publication-fault", sbDir, awgmDir, fircDir, false)
		if op.IsRecoveryRequired() {
			t.Fatalf("expected IsRecoveryRequired()=false after clean pre-publication rollback, got %s", op.RecoveryReason())
		}
	}

	// 2. C1 Acceptance: syncParentDir failure specifically on writing Stage="committed" (AFTER os.Rename),
	//    followed by abnormal termination (os.Exit(42)) and startup of a new Operator process.
	//    Tests both:
	//      (a) syncParentDir returning an error on Stage="committed" followed by os.Exit(42)
	//      (b) hard crash os.Exit(42) inside syncParentDir on Stage="committed"
	for _, subMode := range []struct {
		mode  string
		stage string
	}{
		{mode: "commit_dir_sync_error_then_exit", stage: "post_commit_error"},
		{mode: "commit_dir_sync_hard_crash", stage: "none"},
	} {
		sbDir, awgmDir, fircDir, snapID, _ := setupMultiFileEnv(t)

		cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocessCrashWorker$")
		cmd.Env = append(os.Environ(),
			"AWGM_TEST_CRASH_OP=snapshot_restore",
			"AWGM_TEST_CRASH_STAGE="+subMode.stage,
			"AWGM_TEST_CRASH_STEP=0",
			"AWGM_TEST_COMMIT_FAULT_MODE="+subMode.mode,
			"AWGM_TEST_SB_DIR="+sbDir,
			"AWGM_TEST_FIRC_DIR="+fircDir,
			"AWGM_TEST_SNAP_ID="+snapID,
		)
		out, cErr := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(cErr, &exitErr) || exitErr.ExitCode() != 42 {
			t.Fatalf("[%s] expected child process to exit(42), got err=%v out=%s", subMode.mode, cErr, string(out))
		}

		// Verify entire file set on disk immediately after subprocess death (zero partial rollback!)
		assertEntireFileSet(t, subMode.mode+"-before-restart", sbDir, awgmDir, fircDir, true)

		// Start new Operator process -> must preserve entire committed file set, enforce RecoveryRequired,
		// and allow subsequent recovery via ClearRecovery().
		restartedOp := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
		assertEntireFileSet(t, subMode.mode+"-after-restart", sbDir, awgmDir, fircDir, true)
		assertRecoveryAndSubsequentRestore(t, subMode.mode, restartedOp, sbDir, awgmDir, fircDir, snapID)
	}

	// 3. C1 Acceptance: Readback failure AFTER publication (os.Rename of Stage="committed"):
	//    - 3a: In-process CommitMarkerReadback hook error + NewOperator restart + ClearRecovery
	//    - 3b: Subprocess real os.ReadFile byte-mismatch corruption after os.Rename + os.Exit(42) + NewOperator + ClearRecovery
	{
		sbDir, awgmDir, fircDir, snapID, op := setupMultiFileEnv(t)
		op.SetMigrationJournalCommitFaultHooks(MigrationJournalCommitFaultHooks{
			CommitMarkerReadback: func(_ string, _ []byte) ([]byte, error) {
				return nil, errors.New("injected readback I/O error after Stage=committed rename")
			},
		})
		_, err := op.RestoreSystemSnapshot(t.Context(), snapID, sbDir)
		if !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("expected ErrRecoveryRequired on post-rename CommitMarkerReadback failure, got %v", err)
		}
		op.SetMigrationJournalCommitFaultHooks(MigrationJournalCommitFaultHooks{})

		// Working files MUST NOT be rolled back!
		assertEntireFileSet(t, "readback-hook-inproc", sbDir, awgmDir, fircDir, true)
		if !op.IsRecoveryRequired() {
			t.Fatalf("expected op.IsRecoveryRequired()=true after post-rename readback failure")
		}

		restartedOp := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
		assertEntireFileSet(t, "readback-hook-restart", sbDir, awgmDir, fircDir, true)
		assertRecoveryAndSubsequentRestore(t, "readback-hook-restart", restartedOp, sbDir, awgmDir, fircDir, snapID)
	}
	{
		sbDir, awgmDir, fircDir, snapID, _ := setupMultiFileEnv(t)
		cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocessCrashWorker$")
		cmd.Env = append(os.Environ(),
			"AWGM_TEST_CRASH_OP=snapshot_restore",
			"AWGM_TEST_CRASH_STAGE=post_commit_error",
			"AWGM_TEST_CRASH_STEP=0",
			"AWGM_TEST_COMMIT_FAULT_MODE=commit_readback_corrupt_then_exit",
			"AWGM_TEST_SB_DIR="+sbDir,
			"AWGM_TEST_FIRC_DIR="+fircDir,
			"AWGM_TEST_SNAP_ID="+snapID,
		)
		out, cErr := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(cErr, &exitErr) || exitErr.ExitCode() != 42 {
			t.Fatalf("expected child process to exit(42) on commit_readback_corrupt_then_exit, got err=%v out=%s", cErr, string(out))
		}
		restartedOp := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
		assertEntireFileSet(t, "readback-corrupt-subproc", sbDir, awgmDir, fircDir, true)
		assertRecoveryAndSubsequentRestore(t, "readback-corrupt-subproc", restartedOp, sbDir, awgmDir, fircDir, snapID)
	}

	// 4. C1 Acceptance: Post-commit journal removal & directory sync failures:
	//    - 4a: BeforeJournalRemove failure
	//    - 4b: AfterJournalRemove failure
	//    - 4c: Real SetDirSyncHook failure on post-remove directory sync (in-process + restart)
	//    - 4d: Subprocess post-remove directory sync failure + os.Exit(42) + NewOperator
	//    - 4e: Subprocess crash at commit_marker_written (before os.Remove) + NewOperator
	for _, tc := range []struct {
		name  string
		setup func(op *Operator)
	}{
		{
			name: "BeforeJournalRemove",
			setup: func(op *Operator) {
				op.SetMigrationJournalCommitFaultHooks(MigrationJournalCommitFaultHooks{
					BeforeJournalRemove: func(_ string) error {
						return errors.New("injected unlink failure before journal remove")
					},
				})
			},
		},
		{
			name: "AfterJournalRemove",
			setup: func(op *Operator) {
				op.SetMigrationJournalCommitFaultHooks(MigrationJournalCommitFaultHooks{
					AfterJournalRemove: func(_ string) error {
						return errors.New("injected fault after journal remove")
					},
				})
			},
		},
		{
			name: "PostRemoveRealDirSyncHook",
			setup: func(op *Operator) {
				var sawCommitted atomic.Bool
				var failedPostRemove atomic.Bool
				op.SetDirSyncHook(func(dir string) error {
					jPath := filepath.Join(dir, "migration-journal.json")
					jBytes, err := os.ReadFile(jPath)
					if err == nil && strings.Contains(string(jBytes), `"stage": "committed"`) {
						sawCommitted.Store(true)
						return nil
					}
					if sawCommitted.Load() && os.IsNotExist(err) && !failedPostRemove.Swap(true) {
						return errors.New("injected real dirSyncHook failure after committed journal remove")
					}
					return nil
				})
			},
		},
	} {
		sbDir, awgmDir, fircDir, snapID, op := setupMultiFileEnv(t)
		tc.setup(op)
		_, err := op.RestoreSystemSnapshot(t.Context(), snapID, sbDir)
		if !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("[%s] expected ErrRecoveryRequired, got %v", tc.name, err)
		}
		op.SetMigrationJournalCommitFaultHooks(MigrationJournalCommitFaultHooks{})
		op.SetDirSyncHook(nil)

		assertEntireFileSet(t, tc.name+"-inproc", sbDir, awgmDir, fircDir, true)
		if !op.IsRecoveryRequired() {
			t.Fatalf("[%s] expected op.IsRecoveryRequired()=true", tc.name)
		}

		restartedOp := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
		assertEntireFileSet(t, tc.name+"-restart", sbDir, awgmDir, fircDir, true)
		assertRecoveryAndSubsequentRestore(t, tc.name+"-restart", restartedOp, sbDir, awgmDir, fircDir, snapID)
	}

	// 4d & 4e: Subprocess crash on post-remove dir sync failure and at commit_marker_written
	for _, subTC := range []struct {
		name  string
		mode  string
		stage string
	}{
		{name: "subproc-post-remove-dir-sync", mode: "post_remove_dir_sync_error_then_exit", stage: "post_commit_error"},
		{name: "subproc-commit-marker-written", mode: "", stage: "commit_marker_written"},
	} {
		sbDir, awgmDir, fircDir, snapID, _ := setupMultiFileEnv(t)
		cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocessCrashWorker$")
		cmd.Env = append(os.Environ(),
			"AWGM_TEST_CRASH_OP=snapshot_restore",
			"AWGM_TEST_CRASH_STAGE="+subTC.stage,
			"AWGM_TEST_CRASH_STEP=0",
			"AWGM_TEST_COMMIT_FAULT_MODE="+subTC.mode,
			"AWGM_TEST_SB_DIR="+sbDir,
			"AWGM_TEST_FIRC_DIR="+fircDir,
			"AWGM_TEST_SNAP_ID="+snapID,
		)
		out, cErr := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(cErr, &exitErr) || exitErr.ExitCode() != 42 {
			t.Fatalf("[%s] expected child process to exit(42), got err=%v out=%s", subTC.name, cErr, string(out))
		}
		restartedOp := NewOperator(Options{ConfigDir: fircDir, InboundPort: 1099})
		assertEntireFileSet(t, subTC.name+"-restart", sbDir, awgmDir, fircDir, true)
		assertRecoveryAndSubsequentRestore(t, subTC.name+"-restart", restartedOp, sbDir, awgmDir, fircDir, snapID)
	}
}

func TestFircFiniteProtoNormalizer(t *testing.T) {
	fpclient.SetStorePath(filepath.Join(t.TempDir(), "fp-clients.json"))

	const (
		uuidStr   = "01234567-89ab-4def-8123-456789abcdef"
		fpID      = "fp_test_firc_1"
		secretHex = "a1b2c3d4e5f60718293a4b5c6d7e8f90112233445566778899aabbccddeeff00"
	)

	vlessFP := "vless://" + uuidStr + "@cdn.example.com:443?encryption=none&security=tls&sni=cdn.example.com&type=xhttp&host=cdn.example.com&path=%2Fcdn-bridge&mode=finiteproto&fp_v=2&fp_id=" + fpID + "&fp_secret=" + secretHex + "&fp_method=GET#CDN-FP-Node"
	directFP := "fp://" + fpID + ":" + secretHex + "@cdn2.example.com:443?v=2&path=%2Fcdn-bridge&host=cdn2.example.com&sni=cdn2.example.com&fp_method=HEAD#Direct-FP-Node"

	if !IsDirectFircProxyLink(vlessFP) {
		t.Fatalf("expected IsDirectFircProxyLink=true for FiniteProto vless:// link")
	}
	if !IsDirectFircProxyLink(directFP) {
		t.Fatalf("expected IsDirectFircProxyLink=true for fp:// link")
	}

	// 1. ParseInlineSourcesForFirc on single vless:// FiniteProto v2 link
	sources, err := ParseInlineSourcesForFirc(vlessFP, "Fallback", "")
	if err != nil {
		t.Fatalf("ParseInlineSourcesForFirc(vlessFP) error: %v", err)
	}
	if len(sources) != 1 || sources[0].Kind != "link" || !strings.HasPrefix(sources[0].Link, "vless://") || !strings.Contains(sources[0].Link, "mode=finiteproto") || !strings.HasSuffix(sources[0].Link, "#CDN-FP-Node") {
		t.Fatalf("unexpected normalized source for vlessFP: %+v", sources)
	}

	// 2. ParseInlineSourcesForFirc on multi-line body with both vlessFP and directFP
	multi := vlessFP + "\n" + directFP
	multiSources, err := ParseInlineSourcesForFirc(multi, "Multi", "")
	if err != nil {
		t.Fatalf("ParseInlineSourcesForFirc(multi) error: %v", err)
	}
	if len(multiSources) != 2 {
		t.Fatalf("expected 2 sources from multi-line FP input, got %d: %+v", len(multiSources), multiSources)
	}
	for i, s := range multiSources {
		if !strings.HasPrefix(s.Link, "vless://") || !strings.Contains(s.Link, "mode=finiteproto") {
			t.Fatalf("multiSources[%d] expected native vless://...mode=finiteproto, got %q", i, s.Link)
		}
	}

	// 3. NormalizeTunnelSources on a Tunnel with a raw FiniteProto link
	tun := Tunnel{
		ID:     "tun-fp",
		Device: "tunvless0",
		Enable: true,
		Sources: []TunnelSource{
			{
				Kind: "link",
				Name: "My FP",
				Link: directFP,
			},
		},
	}
	if err := NormalizeTunnelSources(&tun, ""); err != nil {
		t.Fatalf("NormalizeTunnelSources error: %v", err)
	}
	if len(tun.Sources) != 1 || !strings.HasPrefix(tun.Sources[0].Link, "vless://") || !strings.Contains(tun.Sources[0].Link, "mode=finiteproto") {
		t.Fatalf("expected NormalizeTunnelSources to retain native vless://...mode=finiteproto, got %+v", tun.Sources)
	}
}

func TestMigrateFromSingboxRollsBackOnGroupSyncFailure(t *testing.T) {
	mockSrv, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tmpDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:   filepath.Join(tmpDir, "firc-cfg"),
		FircdBinary: "/bin/true",
		TunvlessBin: "/bin/true",
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	op.SetLifecycleHooks(func(context.Context) error { return nil }, func(context.Context) error { return nil })

	ctx := context.Background()
	if _, err := op.ApplyTunnels(ctx, []Tunnel{{ID: "initial-tun", Device: "firc0", Enable: true}}, true); err != nil {
		t.Fatalf("seed ApplyTunnels: %v", err)
	}
	if _, err := op.ApplyGroups(ctx, []Group{{ID: "initial-grp", Name: "Init", Interface: GroupInterface{"firc0"}, Enable: true}}); err != nil {
		t.Fatalf("seed ApplyGroups: %v", err)
	}

	sbDir := filepath.Join(tmpDir, "sb-config.d")
	_ = os.MkdirAll(sbDir, 0755)
	_ = os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(`{"outbounds":[{"type":"socks","tag":"migrated-socks","server":"1.2.3.4","server_port":1080}]}`), 0644)
	_ = os.WriteFile(filepath.Join(sbDir, "20-router.json"), []byte(`{"route":{"rules":[{"domain":["example.org"],"outbound":"migrated-socks"}]}}`), 0644)

	mockSrv.mu.Lock()
	mockSrv.failGroupsOnce = true
	mockSrv.mu.Unlock()

	if _, err := op.MigrateFromSingboxWithOptions(ctx, sbDir, nil); err == nil {
		t.Fatal("expected MigrateFromSingbox to fail when SyncGroups fails")
	}
	diskTunnelsAfterFail, _ := op.LoadTunnelsFile()
	if len(diskTunnelsAfterFail) != 1 || diskTunnelsAfterFail[0].ID != "initial-tun" {
		t.Fatalf("expected tunnels.yaml to roll back after migration failure, got: %+v", diskTunnelsAfterFail)
	}
	mockSrv.mu.Lock()
	if len(mockSrv.tunnels) != 1 || mockSrv.tunnels[0].ID != "initial-tun" {
		mTuns := mockSrv.tunnels
		mockSrv.mu.Unlock()
		t.Fatalf("expected runtime tunnels in fircd to roll back to initial-tun, got: %+v", mTuns)
	}
	if len(mockSrv.groups) != 1 || mockSrv.groups[0].ID != "initial-grp" {
		mGrps := mockSrv.groups
		mockSrv.mu.Unlock()
		t.Fatalf("expected runtime groups in fircd to roll back to initial-grp, got: %+v", mGrps)
	}
	mockSrv.mu.Unlock()
}

func TestMigrateFromSingboxFailsClosedOnLiveSnapshotFailure(t *testing.T) {
	mockSrv, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tmpDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:   filepath.Join(tmpDir, "firc-cfg"),
		FircdBinary: "/bin/true",
		TunvlessBin: "/bin/true",
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	ctx := context.Background()
	if _, err := op.ApplyTunnels(ctx, []Tunnel{{ID: "orig-tun", Device: "firc0", Enable: true}}, true); err != nil {
		t.Fatalf("seed ApplyTunnels: %v", err)
	}

	mockSrv.mu.Lock()
	mockSrv.failGetTunnels = true
	mockSrv.failGetGroups = true
	beforePutTunnels := mockSrv.putTunnelsCount
	beforePutGroups := mockSrv.putGroupsCount
	mockSrv.mu.Unlock()

	sbDir := filepath.Join(tmpDir, "sb-p11")
	_ = os.MkdirAll(sbDir, 0o755)
	_ = os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(`{"outbounds":[{"type":"socks","tag":"s1","server":"1.2.3.4","server_port":1080}]}`), 0o644)
	if _, err := op.MigrateFromSingboxWithOptions(ctx, sbDir, nil); err == nil {
		t.Fatal("P1.1 regression: expected MigrateFromSingbox to fail closed when live GetTunnels snapshot fails")
	}

	mockSrv.mu.Lock()
	afterPutTunnels := mockSrv.putTunnelsCount
	afterPutGroups := mockSrv.putGroupsCount
	mockSrv.mu.Unlock()

	if afterPutTunnels != beforePutTunnels || afterPutGroups != beforePutGroups {
		t.Fatalf("P1.1 regression: PUT was called despite failed live snapshot")
	}
}

func TestValidateSettingsJSONSemantics_AllowsSingboxStoppedByEngine(t *testing.T) {
	payload := []byte(`{"schemaVersion":1,"singboxStoppedByEngine":true,"singboxManuallyStopped":false,"singboxRouter":{"routingEngine":"firc","routingMode":"tproxy"}}`)
	if err := validateSettingsJSONSemantics(payload); err != nil {
		t.Fatalf("validateSettingsJSONSemantics returned error: %v", err)
	}
}

func TestValidateFircConfYAML_SupportsFirc070SchemaAndRejectsInvalid(t *testing.T) {
	firc070 := []byte(`configVersion: 0.7.0
app:
  httpWeb:
    enabled: true
    host:
      address: "[::]"
      port: 666
  dnsProxy:
    host:
      address: "[::]"
      port: 3553
    upstream:
      address: 127.0.0.1
      port: 53
    disableRemap53: false
    disableDropAAAA: false
    disableStats: false
    maxIdleConns: 10
    maxConcurrent: 100
    timeout: 5s
  netfilter:
    iptables:
      chainPrefix: FIRC_
    disableIPv4: false
    disableIPv6: false
    startMarkTableIndex: 1718186595
  addressPool:
    v4:
      pool: 198.18.0.0/15
      chunk: 24
    v6:
      pool: ""
      chunk: 64
    ttlClamp: 5m0s
    maxNames: 65536
  link:
    - br0
  update:
    check: false
`)
	if err := validateFircConfYAML(firc070); err != nil {
		t.Fatalf("expected firc 0.7.0 firc.conf to validate cleanly, got: %v", err)
	}
	if err := validateFircConfYAML([]byte("configVersion: 0.7.0\nfoo: bar\n")); err == nil {
		t.Fatal("expected missing app section to fail")
	}
	if err := validateFircConfYAML([]byte("app:\n  addressPool:\n    v4:\n      pool: not-a-cidr\n")); err == nil {
		t.Fatal("expected invalid v4 pool CIDR to fail")
	}
}

func TestValidateFircGroupsYAML_SupportsFirc070LastUpdateAndOverrides(t *testing.T) {
	groups070 := []byte(`configVersion: 0.7.0
groups:
- id: d663876a
  name: Example
  interface: nwg0
  enable: false
  resolve:
    tunnel: false
  rules:
  - id: 6f34ee91
    type: wildcard
    rule: '*wildcard.example.com'
    enable: true
  - id: 00ae5f7c
    type: regex
    rule: ^.*.regex.example.com$
    enable: true
  - id: 6120dc8a
    type: domain
    rule: domain.example.com
    enable: true
  - id: b9751782
    type: namespace
    rule: namespace.example.com
    enable: true
  - id: deadbeef
    type: subnet6
    rule: 2001:db8::/32
    enable: true
- id: 38ff351a
  name: lat-premium
  interface: tunvless0
  enable: true
  resolve:
    tunnel: false
  rules: []
  list:
    url: https://repo.hoaxisr.ru/rulesets/srs/grok.srs
    interval: 86400
    last_update: 1794918216
    overrides: []
`)
	groups, err := validateFircGroupsYAML(groups070, nil)
	if err != nil {
		t.Fatalf("expected firc 0.7.0 groups.yaml to validate cleanly, got: %v", err)
	}
	if len(groups) != 2 || groups[1].List == nil || groups[1].List.LastUpdate != 1794918216 {
		t.Fatalf("unexpected decoded GroupList: %+v", groups)
	}
}
