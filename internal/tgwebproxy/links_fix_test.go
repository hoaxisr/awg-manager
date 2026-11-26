package tgwebproxy

import "testing"

func TestBuildTgLinks_EmptyTlsDomainUsesEngineDomain(t *testing.T) {
	sec := "00112233445566778899aabbccddeeff"
	rev, err := BuildTgLinks(Config{
		Scenario:       ScenarioDual,
		PublicHostname: "tg.example.com",
		DirectHost:     "203.0.113.10",
		DirectPort:     8443,
		Secret:         sec,
	}, sec)
	if err != nil {
		t.Fatalf("BuildTgLinks: %v", err)
	}
	want := "ee" + sec + "79612e7275" // hex("ya.ru")
	if rev.MtproxySecret != want {
		t.Fatalf("MtproxySecret = %q, want %q", rev.MtproxySecret, want)
	}
	if rev.MtproxyURL != "tg://proxy?server=203.0.113.10&port=8443&secret="+want {
		t.Fatalf("MtproxyURL = %q", rev.MtproxyURL)
	}
}

func TestBuildTgLinks_CdnHTTPHasNoMTProtoFields(t *testing.T) {
	sec := "00112233445566778899aabbccddeeff"
	rev, err := BuildTgLinks(Config{
		Scenario:       ScenarioCDNHTTP,
		PublicHostname: "tg.example.com, tg2.example.com",
		DirectHost:     "stale.example.com",
		DirectPort:     8443,
		TlsDomain:      "www.google.com",
		Secret:         sec,
	}, sec)
	if err != nil {
		t.Fatalf("BuildTgLinks: %v", err)
	}
	if rev.MtproxySecret != "" || rev.MtproxyURL != "" || rev.MtproxyTmeURL != "" || rev.DirectHost != "" {
		t.Fatalf("cdn_http leaked MTProto fields: %+v", rev)
	}
	for _, u := range rev.Users {
		if u.MtproxySecret != "" || u.MtproxyURL != "" {
			t.Fatalf("cdn_http user leaked MTProto fields: %+v", u)
		}
	}
	if rev.TgURL != "tg://webproxy?server=tg.example.com&secret="+sec {
		t.Fatalf("TgURL = %q, want the first domain only", rev.TgURL)
	}
}

func TestBuildTgLinks_SkipsDisabledFirstUser(t *testing.T) {
	off := "00112233445566778899aabbccddeeff"
	on := "ffeeddccbbaa99887766554433221100"
	cfg := Config{
		Scenario:   ScenarioDirectFakeTLS,
		DirectHost: "203.0.113.10",
		DirectPort: 8443,
		TlsDomain:  "ya.ru",
		Secret:     off,
		Users: []TelegramUser{
			{ID: "default", Secret: off, Enabled: false},
			{ID: "u2", Secret: on, Enabled: true},
		},
	}
	rev, err := BuildTgLinks(cfg, "")
	if err != nil {
		t.Fatalf("BuildTgLinks: %v", err)
	}
	if rev.Secret != on {
		t.Fatalf("Secret = %q, want the enabled user's", rev.Secret)
	}
	u, err := resolveRevealTarget(cfg, "")
	if err != nil || u.ID != "u2" {
		t.Fatalf("resolveRevealTarget = %+v, %v; want u2", u, err)
	}
}

func TestRevealDirectHost_HiddenInCdnHTTP(t *testing.T) {
	if got := revealDirectHost(Config{Scenario: ScenarioCDNHTTP, DirectHost: "x"}); got != "" {
		t.Fatalf("cdn_http: %q", got)
	}
	if got := revealDirectHost(Config{Scenario: ScenarioDual, DirectHost: "x"}); got != "x" {
		t.Fatalf("dual: %q", got)
	}
}
