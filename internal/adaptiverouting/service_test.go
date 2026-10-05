package adaptiverouting

import (
	"strings"
	"testing"
)

func TestParseSusaninLogLine(t *testing.T) {
	line1 := "14:22:10 INFO: [AUTO-SUSANIN: CONFIRMED] 142.251.155.6:443"
	ev1 := parseSusaninLogLine(line1)
	if ev1.Action != "CONFIRMED" {
		t.Fatalf("expected CONFIRMED, got %s", ev1.Action)
	}
	if ev1.Target != "142.251.155.6:443" {
		t.Fatalf("expected target 142.251.155.6:443, got %s", ev1.Target)
	}
	if ev1.ResourceTitle == "" {
		t.Fatalf("expected non-empty ResourceTitle for 142.251.* (Google/YouTube), got empty")
	}
	t.Logf("Line 1 parsed: action=%s, target=%s, title=%s, org=%s, cc=%s",
		ev1.Action, ev1.Target, ev1.ResourceTitle, ev1.ResourceOrg, ev1.ResourceCC)

	line2 := "14:22:15 WARN: [AUTO-SUSANIN: TCP-STALL] 104.21.50.1:443"
	ev2 := parseSusaninLogLine(line2)
	if ev2.Action != "STALL" {
		t.Fatalf("expected STALL, got %s", ev2.Action)
	}
	if ev2.ResourceTitle != "Cloudflare CDN" {
		t.Fatalf("expected Cloudflare CDN, got %s", ev2.ResourceTitle)
	}

	line3 := "14:22:20 INFO: [AUTO-SUSANIN: QUIC] 172.67.180.2:443"
	ev3 := parseSusaninLogLine(line3)
	if ev3.Action != "QUIC" {
		t.Fatalf("expected QUIC, got %s", ev3.Action)
	}
}

func TestReleaseBinaries_PinsComplete(t *testing.T) {
	requiredArches := []string{"aarch64", "mips", "mipsel", "armv7", "x86_64"}
	for _, arch := range requiredArches {
		spec, ok := ReleaseBinaries[arch]
		if !ok {
			t.Errorf("missing ReleaseBinaries for arch %q", arch)
			continue
		}
		if spec.Version == "" {
			t.Errorf("empty version for %q", arch)
		}
		if spec.SHA256 == "" {
			t.Errorf("empty SHA256 for %q", arch)
		}
		if spec.Size <= 0 {
			t.Errorf("invalid size for %q: %d", arch, spec.Size)
		}
		if spec.URL == "" {
			t.Errorf("missing URL for %q", arch)
		}
	}
}

func TestFindDomainKnowledge_ExactOrSuffixOnly(t *testing.T) {
	exact := FindDomainKnowledge("x.com", "")
	if exact == nil || exact.Title != "X (Twitter)" {
		t.Fatalf("expected X (Twitter) for x.com, got %v", exact)
	}

	sub := FindDomainKnowledge("api.x.com", "")
	if sub == nil || sub.Title != "X (Twitter)" {
		t.Fatalf("expected X (Twitter) for api.x.com, got %v", sub)
	}

	fox := FindDomainKnowledge("fox.com", "")
	if fox != nil && fox.Title == "X (Twitter)" {
		t.Fatalf("fox.com falsely matched X (Twitter)")
	}

	notion := FindDomainKnowledge("notion.so", "")
	if notion == nil || notion.Title != "Notion" {
		t.Fatalf("expected Notion for notion.so, got %v", notion)
	}

	fakeNotion := FindDomainKnowledge("badnotion.solutions", "")
	if fakeNotion != nil && fakeNotion.Title == "Notion" {
		t.Fatalf("badnotion.solutions falsely matched Notion")
	}
}

func TestService_ApplyRejectsInvalidRoutingTableID(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	catalog := NewCatalog(nil, nil, nil)
	checker := NewReferenceChecker(store)
	svc := NewService(dir, catalog, checker, store)

	settings := DefaultSettings()
	settings.RoutingTableID = 254 // Reserved table 'main'

	_, err = svc.Apply(t.Context(), settings)
	if err == nil {
		t.Fatal("expected Apply to fail when RoutingTableID is 254")
	}
	if !strings.Contains(err.Error(), "таблицы маршрутизации") {
		t.Fatalf("unexpected error message: %v", err)
	}

	settings.RoutingTableID = 50 // Below 100
	_, err = svc.Apply(t.Context(), settings)
	if err == nil {
		t.Fatal("expected Apply to fail when RoutingTableID is < 100")
	}
}

func TestProcessManager_GenerateConfigFileRejectsInvalidRoutingTableID(t *testing.T) {
	pm := NewProcessManager(t.TempDir(), "")
	settings := DefaultSettings()
	settings.RoutingTableID = 255 // Reserved table 'local'

	_, err := pm.GenerateConfigFile(settings, "awgsus0", nil, nil, "")
	if err == nil {
		t.Fatal("expected GenerateConfigFile to fail when table is 255")
	}
}

func TestService_SetLanInterfacesUpdatesIPs(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)
	catalog := NewCatalog(nil, nil, nil)
	checker := NewReferenceChecker(store)
	svc := NewService(dir, catalog, checker, store)

	// Explicit override prevents overwriting
	svc.SetRouterIPs([]string{"10.0.0.1"})
	svc.SetLanInterfaces([]string{"lo"})
	ips := svc.currentRouterIPsLocked()
	if len(ips) != 1 || ips[0] != "10.0.0.1" {
		t.Fatalf("expected custom router IP 10.0.0.1 to be preserved, got %v", ips)
	}
}

