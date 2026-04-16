package hydraroute

import (
	"strings"
	"testing"
)

func TestGenerateDomainConf_Basic(t *testing.T) {
	lists := []ManagedEntry{
		{
			ListName: "Telegram",
			Domains:  []string{"t.me", "telegram.org"},
			Iface:    "Wireguard0",
		},
	}
	got := GenerateDomainConf(lists)

	mustContain(t, got, "## Telegram")
	mustContain(t, got, "t.me,telegram.org/Wireguard0")

	if strings.Contains(got, "list:") {
		t.Errorf("marker must not contain legacy 'list:' prefix; got:\n%s", got)
	}
	if strings.Contains(got, "{") {
		t.Errorf("marker must not contain legacy {uuid}; got:\n%s", got)
	}
}

func TestGenerateDomainConf_GeoSiteTags(t *testing.T) {
	lists := []ManagedEntry{
		{
			ListName: "Google",
			Domains:  []string{"google.com", "geosite:GOOGLE"},
			Iface:    "Wireguard1",
		},
	}
	got := GenerateDomainConf(lists)

	mustContain(t, got, "## Google")
	mustContain(t, got, "google.com,geosite:GOOGLE/Wireguard1")
}

func TestGenerateDomainConf_Empty(t *testing.T) {
	got := GenerateDomainConf(nil)
	if got != "" {
		t.Errorf("expected empty output, got: %q", got)
	}
}

func TestGenerateIPList_Basic(t *testing.T) {
	lists := []ManagedEntry{
		{
			ListName: "Telegram",
			Subnets:  []string{"91.108.4.0/22", "149.154.160.0/20"},
			Iface:    "Wireguard0",
		},
	}
	got := GenerateIPList(lists)

	mustContain(t, got, "## Telegram")
	mustContain(t, got, "/Wireguard0")
	mustContain(t, got, "91.108.4.0/22")
	mustContain(t, got, "149.154.160.0/20")
}

func TestGenerateIPList_GeoIPTag(t *testing.T) {
	lists := []ManagedEntry{
		{
			ListName: "Russia",
			Subnets:  []string{"5.8.0.0/21", "geoip:RU"},
			Iface:    "Wireguard2",
		},
	}
	got := GenerateIPList(lists)

	mustContain(t, got, "## Russia")
	mustContain(t, got, "/Wireguard2")
	mustContain(t, got, "5.8.0.0/21")
	mustContain(t, got, "geoip:RU")
}

func mustContain(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected output to contain %q\nfull output:\n%s", substr, s)
	}
}
