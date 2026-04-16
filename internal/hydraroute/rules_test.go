package hydraroute

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func setupRuleFiles(t *testing.T) (svc *Service, domainPath, ipPath string) {
	t.Helper()
	dir := t.TempDir()
	domainPath = filepath.Join(dir, "domain.conf")
	ipPath = filepath.Join(dir, "ip.list")

	origDomain := domainConfPath
	origIP := ipListPath
	domainConfPath = domainPath
	ipListPath = ipPath
	t.Cleanup(func() {
		domainConfPath = origDomain
		ipListPath = origIP
	})

	svc = &Service{}
	svc.SetStatusForTest(true)
	return svc, domainPath, ipPath
}

func TestListRules_Empty(t *testing.T) {
	svc, _, _ := setupRuleFiles(t)
	rules, _, err := svc.ListRules()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("expected empty, got %+v", rules)
	}
}

func TestCreateRule_Persists(t *testing.T) {
	svc, domainPath, _ := setupRuleFiles(t)

	created, err := svc.CreateRule(HRRule{
		Name:    "Youtube",
		Domains: []string{"youtube.com"},
		Target:  "nwg0",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if created.Name != "Youtube" {
		t.Errorf("name = %q, want Youtube", created.Name)
	}

	data, err := os.ReadFile(domainPath)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(data), []string{"## Youtube", "youtube.com/nwg0"}) {
		t.Errorf("file content missing tokens:\n%s", string(data))
	}
}

func TestCreateRule_DuplicateNameRejected(t *testing.T) {
	svc, _, _ := setupRuleFiles(t)
	_, _ = svc.CreateRule(HRRule{Name: "X", Domains: []string{"x.com"}, Target: "nwg0"})
	if _, err := svc.CreateRule(HRRule{Name: "X", Domains: []string{"x2.com"}, Target: "nwg0"}); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestCreateRule_DomainsAndSubnets(t *testing.T) {
	svc, domainPath, ipPath := setupRuleFiles(t)

	_, err := svc.CreateRule(HRRule{
		Name:    "Mixed",
		Domains: []string{"example.com"},
		Subnets: []string{"10.0.0.0/8"},
		Target:  "nwg0",
	})
	if err != nil {
		t.Fatal(err)
	}

	domains := readFileOrEmpty(t, domainPath)
	ips := readFileOrEmpty(t, ipPath)

	if !containsAll(domains, []string{"## Mixed", "example.com/nwg0"}) {
		t.Errorf("domain.conf missing entry:\n%s", domains)
	}
	if !containsAll(ips, []string{"## Mixed", "/nwg0", "10.0.0.0/8"}) {
		t.Errorf("ip.list missing entry:\n%s", ips)
	}
}

func TestListRules_RoundtripAfterCreate(t *testing.T) {
	svc, _, _ := setupRuleFiles(t)

	_, _ = svc.CreateRule(HRRule{Name: "A", Domains: []string{"a.com"}, Target: "nwg0"})
	_, _ = svc.CreateRule(HRRule{Name: "B", Domains: []string{"b.com"}, Subnets: []string{"1.2.3.0/24"}, Target: "HydraRoute"})

	rules, _, err := svc.ListRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2: %+v", len(rules), rules)
	}

	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	if rules[0].Name != "A" || !reflect.DeepEqual(rules[0].Domains, []string{"a.com"}) || rules[0].Target != "nwg0" {
		t.Errorf("rule A wrong: %+v", rules[0])
	}
	if rules[1].Name != "B" || !reflect.DeepEqual(rules[1].Subnets, []string{"1.2.3.0/24"}) || rules[1].Target != "HydraRoute" {
		t.Errorf("rule B wrong: %+v", rules[1])
	}
}

func TestUpdateRule_ReplacesByName(t *testing.T) {
	svc, _, _ := setupRuleFiles(t)

	_, _ = svc.CreateRule(HRRule{Name: "Orig", Domains: []string{"orig.com"}, Target: "nwg0"})

	_, err := svc.UpdateRule("Orig", HRRule{
		Name:    "Orig",
		Domains: []string{"new.com"},
		Target:  "nwg0",
	})
	if err != nil {
		t.Fatal(err)
	}

	rules, _, _ := svc.ListRules()
	if len(rules) != 1 || !reflect.DeepEqual(rules[0].Domains, []string{"new.com"}) {
		t.Errorf("after update: %+v", rules)
	}
}

func TestUpdateRule_AllowsRename(t *testing.T) {
	svc, _, _ := setupRuleFiles(t)

	_, _ = svc.CreateRule(HRRule{Name: "Old", Domains: []string{"a.com"}, Target: "nwg0"})

	_, err := svc.UpdateRule("Old", HRRule{
		Name:    "New",
		Domains: []string{"a.com"},
		Target:  "nwg0",
	})
	if err != nil {
		t.Fatal(err)
	}

	rules, _, _ := svc.ListRules()
	if len(rules) != 1 || rules[0].Name != "New" {
		t.Errorf("after rename: %+v", rules)
	}
}

func TestDeleteRule_RemovesEntry(t *testing.T) {
	svc, _, _ := setupRuleFiles(t)

	_, _ = svc.CreateRule(HRRule{Name: "A", Domains: []string{"a.com"}, Target: "nwg0"})
	_, _ = svc.CreateRule(HRRule{Name: "B", Domains: []string{"b.com"}, Target: "nwg0"})

	if err := svc.DeleteRule("A"); err != nil {
		t.Fatal(err)
	}

	rules, _, _ := svc.ListRules()
	if len(rules) != 1 || rules[0].Name != "B" {
		t.Errorf("after delete of A: %+v", rules)
	}
}

func TestListRules_SplitsRulesFromOversizedServiceBlock(t *testing.T) {
	svc, _, ipPath := setupRuleFiles(t)

	content := "## 2ip\n" +
		"/HydraRoute\n" +
		"geoip:RU\n" +
		"\n" +
		"##impossible to use\n" +
		"#/Too-big-geoip-tag\n" +
		"geoip:ru-blocked\n"
	if err := os.WriteFile(ipPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	rules, oversized, err := svc.ListRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Name != "2ip" {
		t.Errorf("want exactly one rule '2ip', got %+v", rules)
	}
	wantOversized := []string{"geoip:ru-blocked"}
	if !reflect.DeepEqual(oversized, wantOversized) {
		t.Errorf("oversized = %v, want %v", oversized, wantOversized)
	}
}

func TestListRules_PicksUpManualEdits(t *testing.T) {
	// Core premise: HR files are SoT. A manual edit must be visible on
	// the next ListRules call — no caching, no marker-section restriction.
	svc, domainPath, _ := setupRuleFiles(t)

	_, _ = svc.CreateRule(HRRule{Name: "Before", Domains: []string{"before.com"}, Target: "nwg0"})

	edited := "## Manual\n" +
		"manual.com/nwg0\n"
	if err := os.WriteFile(domainPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	rules, _, err := svc.ListRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Name != "Manual" {
		t.Errorf("manual edit not picked up: %+v", rules)
	}
}

// helpers
func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !stringContains(s, sub) {
			return false
		}
	}
	return true
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(data)
}
