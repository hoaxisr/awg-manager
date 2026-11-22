package tgwebproxy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	revealSecretA = "a1a1a1a1b2b2b2b2c3c3c3c3d4d4d4d4"
	revealSecretB = "e5e5e5e5f6f6f6f6a7a7a7a7b8b8b8b8"
)

func revealScopeConfig() Config {
	return Config{
		Scenario:       ScenarioDual,
		Enabled:        true,
		PublicHostname: "cdn.example.test",
		DirectHost:     "home.example.test",
		DirectPort:     8448,
		TlsDomain:      "home.example.test",
		Secret:         revealSecretA,
		Endpoints: []WebProxyEndpoint{
			{ID: "ep-cdn", Hostname: "cdn.example.test", IsCDN: true, Enabled: true, ListenPort: 8085, AdminPort: 8086},
			{ID: "ep-direct", Hostname: "direct.example.test", Enabled: true, ListenPort: 8087, AdminPort: 8088},
		},
		Users: []TelegramUser{
			{ID: "u-a", Remark: "Alice", Secret: revealSecretA, Enabled: true},
			{ID: "u-b", Remark: "Bob", Secret: revealSecretB, Enabled: false},
		},
	}
}

func assertRevealOnlyFor(t *testing.T, data RevealData, wantID, wantSecret, otherSecret string) {
	t.Helper()
	if data.UserID != wantID || data.Secret != wantSecret {
		t.Fatalf("reveal target = (%q, %q), want (%q, %q)", data.UserID, data.Secret, wantID, wantSecret)
	}
	if len(data.Users) != 1 || data.Users[0].ID != wantID || data.Users[0].Secret != wantSecret {
		t.Fatalf("reveal users = %+v, want only %q", data.Users, wantID)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal reveal: %v", err)
	}
	if strings.Contains(string(raw), otherSecret) {
		t.Errorf("reveal for %q contains another user's secret", wantID)
	}
	for _, wp := range data.WebProxies {
		if !strings.Contains(wp.TgURL, "secret="+wantSecret) {
			t.Errorf("web proxy link %q not built for %q", wp.TgURL, wantID)
		}
	}
}

func TestRevealUserSecret_ScopedToRequestedUser(t *testing.T) {
	svc, _, _ := setupTestService(t)
	svc.config = revealScopeConfig()

	a, err := svc.RevealUserSecret("u-a")
	if err != nil {
		t.Fatalf("reveal u-a: %v", err)
	}
	assertRevealOnlyFor(t, a, "u-a", revealSecretA, revealSecretB)

	// Disabled users can still be revealed explicitly (UI marks them "выключен").
	b, err := svc.RevealUserSecret("u-b")
	if err != nil {
		t.Fatalf("reveal u-b: %v", err)
	}
	assertRevealOnlyFor(t, b, "u-b", revealSecretB, revealSecretA)
}

func TestRevealUserSecret_EmptyIDResolvesToFirstUserOnly(t *testing.T) {
	svc, _, _ := setupTestService(t)
	svc.config = revealScopeConfig()

	data, err := svc.RevealSecret()
	if err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}
	assertRevealOnlyFor(t, data, "u-a", revealSecretA, revealSecretB)
}

func TestRevealUserSecret_UnknownIDDoesNotFallBack(t *testing.T) {
	svc, _, _ := setupTestService(t)
	svc.config = revealScopeConfig()

	for _, id := range []string{"u-missing", "U-A", " u-a", "default"} {
		data, err := svc.RevealUserSecret(id)
		if !errors.Is(err, ErrUserNotFound) {
			t.Errorf("RevealUserSecret(%q) err = %v, want ErrUserNotFound", id, err)
		}
		raw, _ := json.Marshal(data)
		if strings.Contains(string(raw), revealSecretA) || strings.Contains(string(raw), revealSecretB) {
			t.Errorf("RevealUserSecret(%q) returned secret material on error: %s", id, raw)
		}
	}
}

func TestRevealUserSecret_LegacySingleSecret(t *testing.T) {
	svc, _, _ := setupTestService(t)
	cfg := revealScopeConfig()
	cfg.Users = nil
	svc.config = cfg

	for _, id := range []string{"", "default"} {
		data, err := svc.RevealUserSecret(id)
		if err != nil {
			t.Fatalf("RevealUserSecret(%q): %v", id, err)
		}
		assertRevealOnlyFor(t, data, "default", revealSecretA, revealSecretB)
	}
	if _, err := svc.RevealUserSecret("u-a"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("legacy RevealUserSecret(u-a) err = %v, want ErrUserNotFound", err)
	}
}

func TestRevealUserSecret_NoSecretConfigured(t *testing.T) {
	svc, _, _ := setupTestService(t)
	cfg := revealScopeConfig()
	cfg.Users = nil
	cfg.Secret = ""
	svc.config = cfg

	if _, err := svc.RevealSecret(); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("RevealSecret without secrets err = %v, want ErrUserNotFound", err)
	}
}
