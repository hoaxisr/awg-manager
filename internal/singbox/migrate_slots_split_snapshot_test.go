package singbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Миграция на снимках прежней раскладки, близких к тому, что лежит на роутере:
// оба режима, оба запасных варианта (файл НЕ активного режима) и случай «оба
// слота на диске». Результат прогоняется настоящим sing-box check
// (AWGM_SINGBOX_BIN) и повторной миграцией — она обязана быть no-op.
//
// Отличие от migrate_slots_split_test.go: там разбираются отдельные ветки
// логики на минимальных конфигах, здесь — целые файлы с пользовательскими
// правилами, наборами, hostname-outbound'ами и DNS-сервером, чей резолвер
// объявлял режимный слот.
func TestMigrateSlotsSplit_RealisticSnapshot(t *testing.T) {
	base := `{
  "log": {"level": "warn"},
  "dns": {"strategy": "prefer_ipv4", "servers": [{"type":"udp","tag":"dns-bootstrap","server":"1.1.1.1"}]},
  "outbounds": [{"type":"direct","tag":"direct"}],
  "route": {"default_domain_resolver": "dns-bootstrap"},
  "experimental": {"cache_file": {"enabled": true, "path": "/opt/etc/awg-manager/cache.db"}}
}`

	legacyRouter := `{
  "inbounds": [
    {"type":"redirect","tag":"redirect-in","listen":"0.0.0.0","listen_port":51301,"tcp_fast_open":true},
    {"type":"tproxy","tag":"tproxy-in","listen":"127.0.0.1","listen_port":51270,"network":"udp","udp_fragment":true,"udp_timeout":"5m0s"}
  ],
  "outbounds": [
    {"type":"socks","tag":"proxy-a","server":"vpn.example.com","server_port":1080},
    {"type":"socks","tag":"proxy-b","server":"203.0.113.7","server_port":1080},
    {"type":"selector","tag":"vpn","outbounds":["proxy-a","proxy-b","direct"]},
    {"type":"urltest","tag":"fast","outbounds":["proxy-a","proxy-b"]}
  ],
  "dns": {
    "final": "user-doh",
    "strategy": "prefer_ipv4",
    "servers": [
      {"type":"https","tag":"user-doh","server":"dns.google","domain_resolver":{"server":"real"}},
      {"type":"udp","tag":"user-plain","server":"9.9.9.9"}
    ],
    "rules": [{"action":"route","server":"user-plain","domain_suffix":["corp.local"]}]
  },
  "route": {
    "auto_detect_interface": true,
    "final": "vpn",
    "default_domain_resolver": {"server": "real"},
    "rule_set": [
      {"tag":"geosite-ru","type":"remote","format":"binary","url":"https://example.org/ru.srs","download_detour":"direct","update_interval":"24h"}
    ],
    "rules": [
      {"action":"sniff"},
      {"action":"hijack-dns","type":"logical","mode":"or","rules":[{"protocol":"dns"},{"port":[53]}]},
      {"ip_is_private": true, "outbound": "direct"},
      {"action":"route-options","network":"udp","udp_timeout":"5m0s"},
      {"action":"route","rule_set":["geosite-ru"],"outbound":"direct"},
      {"action":"route","domain_suffix":["youtube.com"],"outbound":"vpn"}
    ]
  }
}`

	legacyFakeIP := `{
  "inbounds": [
    {"type":"tun","tag":"tun-in","interface_name":"opkgtun0","address":["172.18.0.1/30"],"mtu":1500,"auto_route":false,"stack":"gvisor"}
  ],
  "outbounds": [
    {"type":"socks","tag":"proxy-a","server":"vpn.example.com","server_port":1080,"domain_resolver":{"server":"real"}},
    {"type":"socks","tag":"proxy-b","server":"203.0.113.7","server_port":1080},
    {"type":"selector","tag":"vpn","outbounds":["proxy-a","proxy-b","direct"]}
  ],
  "dns": {
    "final": "real",
    "strategy": "prefer_ipv4",
    "servers": [
      {"type":"fakeip","tag":"fakeip","inet4_range":"198.18.0.0/15"},
      {"type":"udp","tag":"real","server":"1.1.1.1"},
      {"type":"https","tag":"user-doh","server":"dns.google","domain_resolver":{"server":"real"}}
    ],
    "rules": [
      {"action":"route","server":"fakeip","query_type":["A","AAAA"],"rule_set":["geosite-ru"]},
      {"action":"route","server":"user-doh","domain_suffix":["corp.local"]}
    ]
  },
  "route": {
    "auto_detect_interface": true,
    "final": "vpn",
    "default_domain_resolver": {"server": "real"},
    "rule_set": [
      {"tag":"geosite-ru","type":"remote","format":"binary","url":"https://example.org/ru.srs","download_detour":"direct","update_interval":"24h"}
    ],
    "rules": [
      {"action":"sniff"},
      {"action":"hijack-dns","type":"logical","mode":"or","rules":[{"protocol":"dns"},{"port":[53]}]},
      {"ip_is_private": true, "outbound": "direct"},
      {"action":"route","rule_set":["geosite-ru"],"outbound":"direct"}
    ]
  },
  "experimental": {"cache_file": {"enabled": true, "store_fakeip": true, "path": "/opt/etc/awg-manager/fakeip.db"}}
}`

	cases := []struct {
		name  string
		mode  string
		files map[string]string
	}{
		{
			name:  "tproxy",
			mode:  "tproxy",
			files: map[string]string{"20-router.json": legacyRouter},
		},
		{
			name:  "fakeip",
			mode:  "fakeip-tun",
			files: map[string]string{"21-fakeip.json": legacyFakeIP},
		},
		{
			// Запасной вариант: режим переключён в fakeip, но движок в нём ни
			// разу не поднимали — на диске только слот tproxy.
			name:  "fakeip-fallback-from-router-slot",
			mode:  "fakeip-tun",
			files: map[string]string{"20-router.json": legacyRouter},
		},
		{
			// Обратный запасной вариант: режим tproxy, на диске только fakeip.
			name:  "tproxy-fallback-from-fakeip-slot",
			mode:  "tproxy",
			files: map[string]string{"21-fakeip.json": legacyFakeIP},
		},
		{
			name:  "оба слота на диске, активен tproxy",
			mode:  "tproxy",
			files: map[string]string{"20-router.json": legacyRouter, "21-fakeip.json": legacyFakeIP},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSnapshotFile(t, filepath.Join(dir, "00-base.json"), base)
			for name, body := range tc.files {
				writeSnapshotFile(t, filepath.Join(dir, name), body)
			}
			var logs []string
			changed, err := MigrateSlotsSplitWithLog(dir, tc.mode, func(m string) { logs = append(logs, m) })
			if err != nil {
				t.Fatalf("миграция: %v", err)
			}
			if !changed {
				t.Fatalf("миграция ничего не изменила")
			}
			for _, l := range logs {
				t.Logf("журнал: %s", l)
			}
			logDirListing(t, dir, "")
			logDirListing(t, filepath.Join(dir, "disabled"), "disabled/")

			// Повторный прогон обязан быть no-op.
			changed2, err := MigrateSlotsSplitWithLog(dir, tc.mode, func(m string) { t.Logf("повтор: %s", m) })
			if err != nil {
				t.Fatalf("повторная миграция: %v", err)
			}
			if changed2 {
				t.Errorf("повторная миграция не идемпотентна")
			}
			snapshotSingboxCheck(t, dir)
		})
	}
}

func writeSnapshotFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func logDirListing(t *testing.T, dir, prefix string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, _ := e.Info()
		t.Logf("  %s%s (%d б)", prefix, e.Name(), info.Size())
	}
}

func snapshotSingboxCheck(t *testing.T, dir string) {
	t.Helper()
	bin := os.Getenv("AWGM_SINGBOX_BIN")
	if bin == "" {
		t.Log("AWGM_SINGBOX_BIN не задан — sing-box check пропущен")
		return
	}
	cmd := exec.Command(bin, "check", "-C", dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		for _, name := range []string{"21-routing.json", "20-tproxy.json", "20-fakeip.json", "20-policytun.json"} {
			if data, rerr := os.ReadFile(filepath.Join(dir, name)); rerr == nil {
				t.Logf("--- %s ---\n%s", name, data)
			}
		}
		t.Fatalf("sing-box check: %v\nstderr: %s", err, stderr.String())
	}
}
