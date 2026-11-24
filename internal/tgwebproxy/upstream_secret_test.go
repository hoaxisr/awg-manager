package tgwebproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

const socksTestPassword = "S0cks-P@ss-4821-unique"

func socksUpstreamConfig() Config {
	return Config{
		Upstreams: []UpstreamSpec{
			{ID: "up-1", Type: UpstreamTypeDirect, Weight: 5, Enabled: true},
			{ID: "up-socks", Type: UpstreamTypeSocks5, Address: "10.0.0.2:1080", Username: "tguser", Password: socksTestPassword, Weight: 1, Enabled: true},
		},
	}
}

func storedUpstream(t *testing.T, svc *Service, id string) UpstreamSpec {
	t.Helper()
	svc.mu.Lock()
	defer svc.mu.Unlock()
	for _, u := range svc.config.Upstreams {
		if u.ID == id {
			return u
		}
	}
	t.Fatalf("upstream %s not stored: %+v", id, svc.config.Upstreams)
	return UpstreamSpec{}
}

// roundTripPublicConfig mimics the UI: read PublicConfig JSON, edit, PUT back.
func roundTripPublicConfig(t *testing.T, svc *Service, edit func(*Config)) {
	t.Helper()
	raw, err := json.Marshal(svc.GetConfig())
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(&cfg)
	}
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("UpdateConfig round-trip: %v", err)
	}
}

func TestPublicViews_DoNotExposeSocks5Password(t *testing.T) {
	svc, _, _ := setupTestService(t)
	if err := svc.UpdateConfig(socksUpstreamConfig()); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	for label, view := range map[string]any{"PublicConfig": svc.GetConfig(), "Status": svc.GetStatus()} {
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		js := string(raw)
		if strings.Contains(js, socksTestPassword) {
			t.Errorf("%s JSON exposes the SOCKS5 password: %s", label, js)
		}
		if strings.Contains(js, `"password"`) {
			t.Errorf("%s JSON has a password field: %s", label, js)
		}
		if !strings.Contains(js, `"password_set":true`) || !strings.Contains(js, "10.0.0.2:1080") || !strings.Contains(js, "tguser") {
			t.Errorf("%s JSON lost upstream metadata needed by the editor: %s", label, js)
		}
	}
}

func TestUpdateConfig_PreservesRedactedSocks5Password(t *testing.T) {
	svc, _, _ := setupTestService(t)
	if err := svc.UpdateConfig(socksUpstreamConfig()); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	// Unrelated edit through the redacted public view (password comes back empty).
	roundTripPublicConfig(t, svc, func(c *Config) { c.Upstreams[1].Weight = 3 })

	u := storedUpstream(t, svc, "up-socks")
	if u.Password != socksTestPassword || u.Weight != 3 {
		t.Fatalf("stored upstream after round-trip = %+v, want password kept and weight 3", u)
	}
	var runtimePass string
	for _, eu := range svc.GetInternalConfig().EnabledUpstreams() {
		if eu.ID == "up-socks" {
			runtimePass = eu.Password
		}
	}
	if runtimePass != socksTestPassword {
		t.Fatalf("runtime upstreams lost the SOCKS5 password after round-trip")
	}
}

// A redacted password must never be re-sent to a different proxy target:
// otherwise editing only the address would hand the stored credential to an
// attacker-controlled SOCKS5 server.
func TestUpdateConfig_DoesNotReuseSocks5PasswordForNewTarget(t *testing.T) {
	cases := map[string]func(*Config){
		"address changed":  func(c *Config) { c.Upstreams[1].Address = "203.0.113.9:1080" },
		"username changed": func(c *Config) { c.Upstreams[1].Username = "other" },
		"id changed":       func(c *Config) { c.Upstreams[1].ID = "up-new" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			if err := svc.UpdateConfig(socksUpstreamConfig()); err != nil {
				t.Fatalf("UpdateConfig: %v", err)
			}
			roundTripPublicConfig(t, svc, edit)
			svc.mu.Lock()
			defer svc.mu.Unlock()
			for _, u := range svc.config.Upstreams {
				if u.Password == socksTestPassword {
					t.Fatalf("stored password reused for changed upstream %+v", u)
				}
			}
		})
	}
}

func TestUpdateConfig_ExplicitSocks5PasswordReplaces(t *testing.T) {
	svc, _, _ := setupTestService(t)
	if err := svc.UpdateConfig(socksUpstreamConfig()); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	roundTripPublicConfig(t, svc, func(c *Config) { c.Upstreams[1].Password = "new-pass" })
	if u := storedUpstream(t, svc, "up-socks"); u.Password != "new-pass" {
		t.Fatalf("explicit password not applied: %+v", u)
	}
}
