package dnsroute

import (
	"net"
	"testing"
)

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("bad CIDR: " + s)
	}
	return n
}

// --- cidrCovers tests ---

func TestCovers_BasicContainment(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		expect bool
	}{
		{"parent covers child", "10.0.0.0/8", "10.1.2.0/24", true},
		{"same net", "10.0.0.0/24", "10.0.0.0/24", true},
		{"different /24s", "10.0.0.0/24", "10.0.1.0/24", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cidrCovers(mustParseCIDR(tt.a), mustParseCIDR(tt.b))
			if got != tt.expect {
				t.Errorf("cidrCovers(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.expect)
			}
		})
	}
}

func TestCovers_Boundaries(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		expect bool
	}{
		{"last addr in /24", "10.0.0.0/24", "10.0.0.255/32", true},
		{"first outside /24", "10.0.0.0/24", "10.0.1.0/32", false},
		{"/0 covers everything", "0.0.0.0/0", "192.168.1.0/24", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cidrCovers(mustParseCIDR(tt.a), mustParseCIDR(tt.b))
			if got != tt.expect {
				t.Errorf("cidrCovers(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.expect)
			}
		})
	}
}

func TestCovers_SameMask(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		expect bool
	}{
		{"exact same", "192.168.1.0/24", "192.168.1.0/24", true},
		{"different nets same mask", "192.168.1.0/24", "192.168.2.0/24", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cidrCovers(mustParseCIDR(tt.a), mustParseCIDR(tt.b))
			if got != tt.expect {
				t.Errorf("cidrCovers(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.expect)
			}
		})
	}
}

func TestCovers_HostRoutes(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		expect bool
	}{
		{"/24 covers /32", "10.0.0.0/24", "10.0.0.5/32", true},
		{"/32 not covers /24", "10.0.0.5/32", "10.0.0.0/24", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cidrCovers(mustParseCIDR(tt.a), mustParseCIDR(tt.b))
			if got != tt.expect {
				t.Errorf("cidrCovers(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.expect)
			}
		})
	}
}

func TestCovers_Normalization(t *testing.T) {
	// 10.0.0.5/8 normalizes to 10.0.0.0/8
	got := cidrCovers(mustParseCIDR("10.0.0.5/8"), mustParseCIDR("10.1.0.0/16"))
	if !got {
		t.Error("10.0.0.5/8 (normalized to 10.0.0.0/8) should cover 10.1.0.0/16")
	}
}

func TestCovers_IPv6(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		expect bool
	}{
		{"fd00::/64 covers /128", "fd00::/64", "fd00::1/128", true},
		{"/128 not covers /64", "fd00::1/128", "fd00::/64", false},
		{"different v6 nets", "fd00::/64", "fd01::/64", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cidrCovers(mustParseCIDR(tt.a), mustParseCIDR(tt.b))
			if got != tt.expect {
				t.Errorf("cidrCovers(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.expect)
			}
		})
	}
}

// --- dedupSubnets tests ---

func TestDedupSubnets_ExactDupe(t *testing.T) {
	existing := []DomainList{
		{ID: "list1", Name: "List 1", Subnets: []string{"10.0.0.0/24"}},
	}
	kept, report := dedupSubnets([]string{"10.0.0.0/24"}, "list2", existing)
	if len(kept) != 0 {
		t.Errorf("expected 0 kept, got %d: %v", len(kept), kept)
	}
	if report.ExactDupes != 1 {
		t.Errorf("expected 1 exact dupe, got %d", report.ExactDupes)
	}
	if len(report.Items) != 1 || report.Items[0].Reason != "exact" {
		t.Errorf("expected exact reason, got %v", report.Items)
	}
}

func TestDedupSubnets_CoveredByParent(t *testing.T) {
	existing := []DomainList{
		{ID: "list1", Name: "List 1", Subnets: []string{"10.0.0.0/16"}},
	}
	kept, report := dedupSubnets([]string{"10.0.1.0/24"}, "list2", existing)
	if len(kept) != 0 {
		t.Errorf("expected 0 kept, got %d: %v", len(kept), kept)
	}
	if report.WildcardDupes != 1 {
		t.Errorf("expected 1 wildcard dupe, got %d", report.WildcardDupes)
	}
	if len(report.Items) != 1 || report.Items[0].Reason != "subnet_covered" {
		t.Errorf("expected subnet_covered reason, got %v", report.Items)
	}
}

func TestDedupSubnets_InternalDedup(t *testing.T) {
	// Within the same batch: 10.0.0.0/16 covers 10.0.1.0/24 and 10.0.2.0/24
	input := []string{"10.0.0.0/16", "10.0.1.0/24", "10.0.2.0/24"}
	kept, report := dedupSubnets(input, "list1", nil)
	if len(kept) != 1 {
		t.Errorf("expected 1 kept, got %d: %v", len(kept), kept)
	}
	if kept[0] != "10.0.0.0/16" {
		t.Errorf("expected 10.0.0.0/16 kept, got %s", kept[0])
	}
	if report.TotalRemoved != 2 {
		t.Errorf("expected 2 removed, got %d", report.TotalRemoved)
	}
}

func TestDedupSubnets_InvalidCIDR(t *testing.T) {
	kept, report := dedupSubnets([]string{"not-a-cidr", "10.0.0.0/24"}, "list1", nil)
	if len(kept) != 1 {
		t.Errorf("expected 1 kept, got %d: %v", len(kept), kept)
	}
	// Invalid CIDR is silently skipped — not counted in TotalInput or TotalRemoved.
	if report.TotalInput != 1 {
		t.Errorf("expected TotalInput=1 (invalid skipped), got %d", report.TotalInput)
	}
	if report.TotalRemoved != 0 {
		t.Errorf("expected 0 removed, got %d", report.TotalRemoved)
	}
}

func TestDedupSubnets_IPv6(t *testing.T) {
	existing := []DomainList{
		{ID: "list1", Name: "List 1", Subnets: []string{"fd00::/48"}},
	}
	kept, report := dedupSubnets([]string{"fd00::/64"}, "list2", existing)
	if len(kept) != 0 {
		t.Errorf("expected 0 kept, got %d: %v", len(kept), kept)
	}
	if report.TotalRemoved != 1 {
		t.Errorf("expected 1 removed, got %d", report.TotalRemoved)
	}
}

func TestDedupSubnets_ParentWithExcludeAllowsChild(t *testing.T) {
	existing := []DomainList{
		{
			ID:             "list_a",
			Name:           "A",
			Subnets:        []string{"10.0.0.0/16"},
			ExcludeSubnets: []string{"10.0.0.0/24"},
		},
	}

	kept, report := dedupSubnets([]string{"10.0.0.0/24"}, "list_b", existing)

	if len(kept) != 1 || kept[0] != "10.0.0.0/24" {
		t.Fatalf("expected 10.0.0.0/24 to survive, got kept=%v", kept)
	}
	if report.TotalRemoved != 0 {
		t.Fatalf("expected no removals, got %d", report.TotalRemoved)
	}
}

func TestDedupSubnets_ExcludeSubtree(t *testing.T) {
	existing := []DomainList{
		{
			ID:             "list_a",
			Name:           "A",
			Subnets:        []string{"10.0.0.0/8"},
			ExcludeSubnets: []string{"10.0.0.0/16"},
		},
	}

	kept, report := dedupSubnets([]string{"10.0.5.0/24"}, "list_b", existing)

	if len(kept) != 1 {
		t.Fatalf("expected 10.0.5.0/24 to survive (under exclude subtree), got %v", kept)
	}
	if report.TotalRemoved != 0 {
		t.Fatalf("expected no removals, got %d", report.TotalRemoved)
	}
}
