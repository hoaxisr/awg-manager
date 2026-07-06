package singbox

// Временный смоук-генератор для проверки миграции sing-box 1.14
// (download_detour → http_client, явный default HTTP-клиент). Пишет три
// config.d-каталога реальными генераторами/патчерами проекта:
//
//	after/    — свежие генераторы (freshBaseConfig + router.SaveConfig)
//	before/   — эмуляция старой установки (download_detour, без http_clients)
//	migrated/ — копия before/ после boot-патчеров NewOperator
//
// Запускается только с SMOKE_OUT=<dir> (обычные прогоны skip).
// НЕ коммитить вместе с фичей — вспомогательный харнесс.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

func TestSmokeGenerateHTTPClientConfigs(t *testing.T) {
	out := os.Getenv("SMOKE_OUT")
	if out == "" {
		t.Skip("SMOKE_OUT not set")
	}
	rsBase := os.Getenv("SMOKE_RS_BASE")
	if rsBase == "" {
		rsBase = "http://127.0.0.1:18099"
	}

	origCache := defaultCacheDBPath
	defaultCacheDBPath = filepath.Join(out, "cache.db")
	defer func() { defaultCacheDBPath = origCache }()

	afterDir := filepath.Join(out, "after")
	beforeDir := filepath.Join(out, "before")
	migratedDir := filepath.Join(out, "migrated")
	for _, d := range []string{afterDir, beforeDir, migratedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// --- after/: реальные генераторы ---
	if err := writeJSONFile(filepath.Join(afterDir, "00-base.json"), freshBaseConfigWithLogLevel("info")); err != nil {
		t.Fatal(err)
	}
	cfg := router.NewEmptyConfig()
	// Каноничный tproxy-in (см. ensureTProxyInbound в service.go).
	cfg.Inbounds = []router.Inbound{{
		Type: "tproxy", Tag: "tproxy-in", Listen: "127.0.0.1",
		ListenPort: 51280, Network: "udp", UDPFragment: true, UDPTimeout: "3m0s",
	}}
	// «Туннельный» outbound в стиле awg-direct: direct + bind_interface —
	// непустой direct, валиден как http_client.detour (см. empty-direct
	// check в sing-box) и умеет ходить на 127.0.0.1 через lo.
	cfg.Outbounds = []router.Outbound{{Type: "direct", Tag: "vpn", BindInterface: "lo"}}
	for _, rs := range []router.RuleSet{
		{Tag: "rs-detour", Type: "remote", Format: "binary", URL: rsBase + "/a.srs", UpdateInterval: "24h", HTTPClient: &router.RuleSetHTTPClient{Detour: "vpn"}},
		{Tag: "rs-auto", Type: "remote", Format: "binary", URL: rsBase + "/b.srs", UpdateInterval: "24h"},
		// detour "direct" нормализуется в default-клиент ещё в AddRuleSet.
		{Tag: "rs-direct", Type: "remote", Format: "binary", URL: rsBase + "/c.srs", UpdateInterval: "24h", HTTPClient: &router.RuleSetHTTPClient{Detour: "direct"}},
	} {
		if err := cfg.AddRuleSet(rs); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []router.Rule{
		{RuleSet: []string{"rs-detour"}, Action: "route", Outbound: "vpn"},
		{RuleSet: []string{"rs-auto"}, Action: "route", Outbound: "direct"},
		{RuleSet: []string{"rs-direct"}, Action: "route", Outbound: "direct"},
	} {
		if err := cfg.AddRule(r); err != nil {
			t.Fatal(err)
		}
	}
	cfg.EnsureSystemRules(true)
	if err := cfg.SetRouteFinal("direct"); err != nil {
		t.Fatal(err)
	}
	// RouterConfig.AddRuleSet сам не нормализует http_client (это делают
	// CRUD-методы сервиса и RuleSet.UnmarshalJSON) — прогоняем конфиг через
	// JSON-цикл, как на реальном пути API → decodeBody → UnmarshalJSON:
	// detour "direct" у rs-direct должен схлопнуться в default-клиент.
	rawCfg, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	normalized := router.NewEmptyConfig()
	if err := json.Unmarshal(rawCfg, normalized); err != nil {
		t.Fatal(err)
	}
	if err := router.SaveConfig(filepath.Join(afterDir, "20-router.json"), normalized); err != nil {
		t.Fatal(err)
	}

	// --- before/: эмуляция старой установки ---
	base := freshBaseConfigWithLogLevel("info")
	delete(base, "http_clients")
	if err := writeJSONFile(filepath.Join(beforeDir, "00-base.json"), base); err != nil {
		t.Fatal(err)
	}
	legacyRouter := `{
  "inbounds": [
    {"type": "tproxy", "tag": "tproxy-in", "listen": "127.0.0.1", "listen_port": 51280, "network": "udp", "udp_fragment": true, "udp_timeout": "3m0s"}
  ],
  "outbounds": [
    {"type": "direct", "tag": "vpn", "bind_interface": "lo"}
  ],
  "route": {
    "final": "direct",
    "rule_set": [
      {"tag": "rs-detour", "type": "remote", "format": "binary", "url": "` + rsBase + `/a.srs", "update_interval": "24h", "download_detour": "vpn"},
      {"tag": "rs-auto", "type": "remote", "format": "binary", "url": "` + rsBase + `/b.srs", "update_interval": "24h"},
      {"tag": "rs-direct", "type": "remote", "format": "binary", "url": "` + rsBase + `/c.srs", "update_interval": "24h", "download_detour": "direct"}
    ],
    "rules": [
      {"action": "sniff"},
      {"rule_set": ["rs-detour"], "action": "route", "outbound": "vpn"},
      {"rule_set": ["rs-auto"], "action": "route", "outbound": "direct"},
      {"rule_set": ["rs-direct"], "action": "route", "outbound": "direct"}
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(beforeDir, "20-router.json"), []byte(legacyRouter), 0o644); err != nil {
		t.Fatal(err)
	}

	// --- migrated/: before/ + boot-патчеры (как NewOperator) ---
	for _, name := range []string{"00-base.json", "20-router.json"} {
		data, err := os.ReadFile(filepath.Join(beforeDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(migratedDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ensureBaseConfigWithLogLevel(migratedDir, "info")
	for _, slotName := range []string{"20-router.json", "21-fakeip.json"} {
		for _, sub := range []string{"", "pending", "disabled"} {
			patchSlotRuleSetsDownloadDetour(filepath.Join(migratedDir, sub, slotName))
		}
	}
	t.Logf("smoke config dirs written under %s", out)
}
