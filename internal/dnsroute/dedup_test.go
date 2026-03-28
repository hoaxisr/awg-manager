package dnsroute

import (
	"testing"
)

func TestDomainIndex_ExactMatch(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")

	res := idx.Check("example.com", "list_2")
	if !res.Removed {
		t.Fatal("expected domain to be removed")
	}
	if res.Reason != "exact" {
		t.Errorf("reason = %q, want exact", res.Reason)
	}
	if res.CoveredBy != "example.com" {
		t.Errorf("coveredBy = %q, want example.com", res.CoveredBy)
	}
	if res.OwnerListID != "list_1" {
		t.Errorf("ownerListID = %q, want list_1", res.OwnerListID)
	}
}

func TestDomainIndex_WildcardParentCovers(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")

	res := idx.Check("sub.example.com", "list_2")
	if !res.Removed {
		t.Fatal("expected subdomain to be removed")
	}
	if res.Reason != "wildcard" {
		t.Errorf("reason = %q, want wildcard", res.Reason)
	}
	if res.CoveredBy != "example.com" {
		t.Errorf("coveredBy = %q, want example.com", res.CoveredBy)
	}
}

func TestDomainIndex_DeepNesting(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")

	res := idx.Check("a.b.c.example.com", "list_2")
	if !res.Removed {
		t.Fatal("expected deeply nested subdomain to be removed")
	}
	if res.Reason != "wildcard" {
		t.Errorf("reason = %q, want wildcard", res.Reason)
	}
}

func TestDomainIndex_ChildDoesNotCoverParent(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("sub.example.com", "list_1")

	res := idx.Check("example.com", "list_2")
	if res.Removed {
		t.Fatal("child should NOT cover parent")
	}
}

func TestDomainIndex_SiblingDoesNotCover(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("a.example.com", "list_1")

	res := idx.Check("b.example.com", "list_2")
	if res.Removed {
		t.Fatal("sibling should NOT cover sibling")
	}
}

func TestDomainIndex_SameListWildcard(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")

	res := idx.Check("sub.example.com", "list_1")
	if !res.Removed {
		t.Fatal("same-list wildcard should remove subdomain")
	}
}

func TestDomainIndex_SameListExact(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")

	res := idx.Check("example.com", "list_1")
	if !res.Removed {
		t.Fatal("same-list exact duplicate should be removed")
	}
}

func TestDomainIndex_TLDOnly(t *testing.T) {
	idx := NewDomainIndex()

	idx.Add("com", "list_1")
	res := idx.Check("com", "list_1")
	if !res.Removed {
		t.Fatal("exact same-list dupe for TLD should be removed")
	}
}

func TestDomainIndex_TLDCoversAll(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("com", "list_1")

	res := idx.Check("example.com", "list_2")
	if !res.Removed {
		t.Fatal("TLD should cover all domains under it")
	}
	if res.CoveredBy != "com" {
		t.Errorf("coveredBy = %q, want com", res.CoveredBy)
	}
}

func TestDomainIndex_CaseInsensitive(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("Example.COM", "list_1")

	res := idx.Check("example.com", "list_2")
	if !res.Removed {
		t.Fatal("case-insensitive match should work")
	}
}

func TestDomainIndex_TrailingDot(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")

	res := idx.Check("example.com.", "list_2")
	if !res.Removed {
		t.Fatal("trailing dot should be stripped")
	}
}

func TestDomainIndex_EmptyDomain(t *testing.T) {
	idx := NewDomainIndex()

	res := idx.Check("", "list_1")
	if res.Removed {
		t.Fatal("empty domain should not be marked as removed")
	}
}

func TestCheckBatch_MixedResults(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")
	idx.Add("google.com", "list_2")
	names := map[string]string{"list_1": "VPN Sites", "list_2": "CDN Routes"}
	domains := []string{"sub.example.com", "newsite.com", "google.com", "test.org"}
	kept, report := idx.CheckBatch(domains, "list_3", names)
	if len(kept) != 2 {
		t.Fatalf("kept = %d, want 2: %v", len(kept), kept)
	}
	if kept[0] != "newsite.com" || kept[1] != "test.org" {
		t.Errorf("kept = %v, want [newsite.com, test.org]", kept)
	}
	if report.TotalInput != 4 {
		t.Errorf("TotalInput = %d, want 4", report.TotalInput)
	}
	if report.TotalKept != 2 {
		t.Errorf("TotalKept = %d, want 2", report.TotalKept)
	}
	if report.TotalRemoved != 2 {
		t.Errorf("TotalRemoved = %d, want 2", report.TotalRemoved)
	}
	if report.ExactDupes != 1 {
		t.Errorf("ExactDupes = %d, want 1", report.ExactDupes)
	}
	if report.WildcardDupes != 1 {
		t.Errorf("WildcardDupes = %d, want 1", report.WildcardDupes)
	}
	if len(report.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(report.Items))
	}
}

func TestCheckBatch_InternalDedup(t *testing.T) {
	idx := NewDomainIndex()
	names := map[string]string{}
	domains := []string{"example.com", "sub.example.com", "other.example.com"}
	kept, report := idx.CheckBatch(domains, "list_1", names)
	if len(kept) != 1 {
		t.Fatalf("kept = %d, want 1: %v", len(kept), kept)
	}
	if kept[0] != "example.com" {
		t.Errorf("kept[0] = %q, want example.com", kept[0])
	}
	if report.TotalRemoved != 2 {
		t.Errorf("TotalRemoved = %d, want 2", report.TotalRemoved)
	}
}

func TestCheckBatch_InternalExactDedup(t *testing.T) {
	idx := NewDomainIndex()
	names := map[string]string{}
	domains := []string{"example.com", "Example.COM", "other.com"}
	kept, report := idx.CheckBatch(domains, "list_1", names)
	if len(kept) != 2 {
		t.Fatalf("kept = %d, want 2: %v", len(kept), kept)
	}
	if report.TotalRemoved != 1 {
		t.Errorf("TotalRemoved = %d, want 1", report.TotalRemoved)
	}
	if report.ExactDupes != 1 {
		t.Errorf("ExactDupes = %d, want 1", report.ExactDupes)
	}
}

func TestCheckBatch_ReportConsistency(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("a.com", "list_1")
	names := map[string]string{"list_1": "List A"}
	domains := []string{"x.com", "a.com", "sub.a.com", "y.com", "y.com"}
	_, report := idx.CheckBatch(domains, "list_2", names)
	if report.TotalInput != 5 {
		t.Errorf("TotalInput = %d, want 5", report.TotalInput)
	}
	if report.TotalKept+report.TotalRemoved != report.TotalInput {
		t.Errorf("TotalKept(%d) + TotalRemoved(%d) != TotalInput(%d)", report.TotalKept, report.TotalRemoved, report.TotalInput)
	}
	if report.ExactDupes+report.WildcardDupes != report.TotalRemoved {
		t.Errorf("ExactDupes(%d) + WildcardDupes(%d) != TotalRemoved(%d)", report.ExactDupes, report.WildcardDupes, report.TotalRemoved)
	}
}

func TestCheckBatch_EmptyInput(t *testing.T) {
	idx := NewDomainIndex()
	names := map[string]string{}
	kept, report := idx.CheckBatch(nil, "list_1", names)
	if len(kept) != 0 {
		t.Errorf("kept should be empty, got %v", kept)
	}
	if report.TotalInput != 0 {
		t.Errorf("TotalInput = %d, want 0", report.TotalInput)
	}
}

func TestCheckBatch_AllFiltered(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")
	names := map[string]string{"list_1": "Main"}
	domains := []string{"example.com", "sub.example.com"}
	kept, report := idx.CheckBatch(domains, "list_2", names)
	if len(kept) != 0 {
		t.Errorf("all should be filtered, got %v", kept)
	}
	if report.TotalRemoved != 2 {
		t.Errorf("TotalRemoved = %d, want 2", report.TotalRemoved)
	}
}
