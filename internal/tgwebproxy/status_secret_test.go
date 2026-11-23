package tgwebproxy

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// statusLeakFixture returns a realistic multi-user, multi-endpoint configuration
// with a legacy secret and direct Fake-TLS host. Every secret-derived value that
// may only be returned by the reveal endpoint is listed in forbidden.
func statusLeakFixture() (cfg Config, forbidden []string) {
	const (
		mainSecret   = "0123456789abcdef0123456789abcdef"
		userA        = "a1a1a1a1b2b2b2b2c3c3c3c3d4d4d4d4"
		userB        = "e5e5e5e5f6f6f6f6a7a7a7a7b8b8b8b8"
		legacySecret = "fedcba9876543210fedcba9876543210"
	)
	cfg = Config{
		Scenario:        ScenarioDual,
		Enabled:         true,
		ListenPort:      8085,
		AdminPort:       8086,
		PublicHostname:  "cdn.example.test",
		DirectHost:      "home.example.test",
		DirectPort:      8448,
		TlsDomain:       "home.example.test",
		Secret:          mainSecret,
		LegacySecret:    legacySecret,
		LegacyExpiresAt: "2099-01-01T00:00:00Z",
		Endpoints: []WebProxyEndpoint{
			{ID: "ep-cdn", Hostname: "cdn.example.test", Description: "CDN", IsCDN: true, Enabled: true, ListenPort: 8085, AdminPort: 8086},
			{ID: "ep-direct", Hostname: "direct.example.test", Description: "Direct", Enabled: true, ListenPort: 8087, AdminPort: 8088},
			{ID: "ep-off", Hostname: "off.example.test", Enabled: false, ListenPort: 8089, AdminPort: 8090},
		},
		Users: []TelegramUser{
			{ID: "u-a", Remark: "Alice", Secret: userA, Enabled: true},
			{ID: "u-b", Remark: "Bob", Secret: userB, Enabled: false},
		},
	}

	hosts := []string{"cdn.example.test", "direct.example.test", "off.example.test"}
	for _, sec := range []string{mainSecret, userA, userB, legacySecret} {
		forbidden = append(forbidden, sec, strings.ToUpper(sec))
		forbidden = append(forbidden, "ee"+sec+hex.EncodeToString([]byte(cfg.TlsDomain)))
		for _, h := range hosts {
			forbidden = append(forbidden, DeriveBridgeToken(h, sec), DeriveFileBridgeToken(h, sec))
		}
	}
	return cfg, forbidden
}

func assertNoSecretMaterial(t *testing.T, label, payload string, forbidden []string) {
	t.Helper()
	for _, f := range forbidden {
		if f != "" && strings.Contains(payload, f) {
			t.Errorf("%s: secret-derived value %q present in JSON", label, f)
		}
	}
	for _, marker := range []string{"tg://", "t.me/", "bridge=", "?b="} {
		if strings.Contains(payload, marker) {
			t.Errorf("%s: credential link marker %q present in JSON", label, marker)
		}
	}
}

// TestGetStatus_DoesNotExposeWebProxySecrets is the regression test for the
// status endpoint embedding tg://webproxy?...&secret=..., t.me links and HMAC
// bridge tokens into Status.WebProxies. Those must only be returned by the
// CSRF-protected, rate-limited, no-store reveal endpoint.
func TestGetStatus_DoesNotExposeWebProxySecrets(t *testing.T) {
	svc, _, _ := setupTestService(t)
	cfg, forbidden := statusLeakFixture()
	svc.config = cfg

	st := svc.GetStatus()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	assertNoSecretMaterial(t, "Status", string(raw), forbidden)

	// The UI still needs endpoint identity in the status payload.
	got := map[string]string{}
	for _, wp := range st.WebProxies {
		got[wp.ID] = wp.Hostname
	}
	want := map[string]string{"ep-cdn": "cdn.example.test", "ep-direct": "direct.example.test"}
	if len(got) != len(want) {
		t.Fatalf("status web_proxies = %v, want %v", got, want)
	}
	for id, host := range want {
		if got[id] != host {
			t.Errorf("status web_proxies[%s] = %q, want %q", id, got[id], host)
		}
	}
	for _, wp := range st.WebProxies {
		if wp.ID == "ep-cdn" && (!wp.IsCDN || wp.Description != "CDN") {
			t.Errorf("status web_proxies lost endpoint metadata: %+v", wp)
		}
	}

	// Masked fields are still populated.
	if st.SecretMasked == "" || st.LegacySecretMasked == "" {
		t.Errorf("masked secrets missing: %q / %q", st.SecretMasked, st.LegacySecretMasked)
	}
	if len(st.Users) != 2 || st.Users[0].SecretMasked == "" {
		t.Errorf("public users missing masks: %+v", st.Users)
	}
}

// TestGetStatus_LegacySingleSecretNoLeak covers configurations migrated from
// the single-secret schema (no Users, no Endpoints, only PublicHostname).
func TestGetStatus_LegacySingleSecretNoLeak(t *testing.T) {
	svc, _, _ := setupTestService(t)
	cfg, forbidden := statusLeakFixture()
	cfg.Users = nil
	cfg.Endpoints = nil
	svc.config = cfg

	st := svc.GetStatus()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	assertNoSecretMaterial(t, "Status(legacy)", string(raw), forbidden)
	if len(st.WebProxies) != 1 || st.WebProxies[0].Hostname != "cdn.example.test" {
		t.Errorf("legacy status web_proxies = %+v, want single cdn.example.test entry", st.WebProxies)
	}
}

// TestRevealUserSecret_StillReturnsWebProxyLinks guards that removing links
// from status did not break the dedicated reveal path used by the UI.
func TestRevealUserSecret_StillReturnsWebProxyLinks(t *testing.T) {
	svc, _, _ := setupTestService(t)
	cfg, _ := statusLeakFixture()
	svc.config = cfg

	data, err := svc.RevealUserSecret("u-a")
	if err != nil {
		t.Fatalf("RevealUserSecret: %v", err)
	}
	userA := cfg.Users[0].Secret
	if len(data.WebProxies) != 2 {
		t.Fatalf("reveal web_proxies len = %d, want 2", len(data.WebProxies))
	}
	for _, wp := range data.WebProxies {
		if !strings.Contains(wp.TgURL, "tg://webproxy?server="+wp.Hostname+"&secret="+userA) {
			t.Errorf("reveal tg_url = %q, want secret of u-a", wp.TgURL)
		}
		if wp.BridgeURL != DeriveBridgeURL(wp.Hostname, userA) {
			t.Errorf("reveal bridge_url = %q, want derived from u-a", wp.BridgeURL)
		}
	}
	if !strings.Contains(data.MtproxyURL, "secret=ee"+userA) {
		t.Errorf("reveal mtproxy_url = %q, want ee-prefixed u-a secret", data.MtproxyURL)
	}
}

// F819: маска показывает только последние 4 символа секрета.
func TestMaskSecret_OnlyTail(t *testing.T) {
	got := maskSecret("00112233445566778899aabbccddeeff")
	if got != "********eeff" {
		t.Fatalf("maskSecret = %q, want ********eeff", got)
	}
	if maskSecret("short") != "********" || maskSecret("") != "" {
		t.Fatal("short/empty secrets must not leak characters")
	}
}
