package cdn

import (
	"testing"
)

func TestListProfiles(t *testing.T) {
	profiles := ListProfiles()
	if len(profiles) == 0 {
		t.Fatalf("expected non-empty profile list")
	}

	foundDirect := false
	foundGet := false
	for _, p := range profiles {
		if p.ID == ProfileDirectID {
			foundDirect = true
		}
		if p.ID == ProfileCDNGetID {
			foundGet = true
		}
	}
	if !foundDirect || !foundGet {
		t.Errorf("expected both direct and cdn_get profiles present")
	}
}

func TestListProfilesForServer(t *testing.T) {
	xrayProfiles := ListProfilesForServer("xray")
	for _, p := range xrayProfiles {
		if p.ID == ProfileDirectID {
			t.Errorf("direct profile must not be included for VLESS wizard")
		}
		if p.ID == ProfileCDNWSID {
			t.Errorf("cdn_ws (WebSocket) profile must not be included for VLESS wizard")
		}
	}
	if len(xrayProfiles) != len(ListProfiles())-2 {
		t.Errorf("expected 2 fewer profiles for VLESS, got %d", len(xrayProfiles))
	}
}

func TestGetProfile(t *testing.T) {
	p, ok := GetProfile(ProfileCDNGetID)
	if !ok {
		t.Fatalf("expected profile cdn_get to be found")
	}
	if p.ID != ProfileCDNGetID {
		t.Errorf("expected ID %s, got %s", ProfileCDNGetID, p.ID)
	}

	_, ok = GetProfile("nonexistent")
	if ok {
		t.Fatalf("expected nonexistent profile to return false")
	}
}

func TestGenerateDNSRecords(t *testing.T) {
	// IPv4 with CDN
	records, err := GenerateDNSRecords(ProfileCDNGetID, "cdn.example.com", "1.2.3.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Type != "A" || records[0].Name != "cdn.example.com" || records[0].Content != "1.2.3.4" || !records[0].Proxied {
		t.Errorf("unexpected record: %+v", records[0])
	}

	// IPv6 with Direct
	records, err = GenerateDNSRecords(ProfileDirectID, "direct.example.com", "2001:db8::1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if records[0].Type != "AAAA" || records[0].Proxied {
		t.Errorf("expected unproxied AAAA, got %+v", records[0])
	}

	// Hostname CNAME
	records, err = GenerateDNSRecords(ProfileCDNWSID, "ws.example.com", "origin.example.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if records[0].Type != "CNAME" || records[0].Content != "origin.example.net" || !records[0].Proxied {
		t.Errorf("expected proxied CNAME, got %+v", records[0])
	}

	// Invalid profile
	_, err = GenerateDNSRecords("unknown", "foo.com", "1.2.3.4")
	if err == nil {
		t.Errorf("expected error for unknown profile")
	}

	// Empty inputs
	_, err = GenerateDNSRecords(ProfileCDNGetID, "", "1.2.3.4")
	if err == nil {
		t.Errorf("expected error for empty domain")
	}
	_, err = GenerateDNSRecords(ProfileCDNGetID, "foo.com", "")
	if err == nil {
		t.Errorf("expected error for empty origin")
	}
}
