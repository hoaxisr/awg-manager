package dnsroute

import (
	"context"
	"testing"
)

func TestDomainIndex_ExactMatch(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")

	res := idx.Check("example.com")
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

	res := idx.Check("sub.example.com")
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

	res := idx.Check("a.b.c.example.com")
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

	res := idx.Check("example.com")
	if res.Removed {
		t.Fatal("child should NOT cover parent")
	}
}

func TestDomainIndex_SiblingDoesNotCover(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("a.example.com", "list_1")

	res := idx.Check("b.example.com")
	if res.Removed {
		t.Fatal("sibling should NOT cover sibling")
	}
}

func TestDomainIndex_SameListWildcard(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")

	res := idx.Check("sub.example.com")
	if !res.Removed {
		t.Fatal("same-list wildcard should remove subdomain")
	}
}

func TestDomainIndex_SameListExact(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")

	res := idx.Check("example.com")
	if !res.Removed {
		t.Fatal("same-list exact duplicate should be removed")
	}
}

func TestDomainIndex_TLDOnly(t *testing.T) {
	idx := NewDomainIndex()

	idx.Add("com", "list_1")
	res := idx.Check("com")
	if !res.Removed {
		t.Fatal("exact same-list dupe for TLD should be removed")
	}
}

func TestDomainIndex_TLDCoversAll(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("com", "list_1")

	res := idx.Check("example.com")
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

	res := idx.Check("example.com")
	if !res.Removed {
		t.Fatal("case-insensitive match should work")
	}
}

func TestDomainIndex_TrailingDot(t *testing.T) {
	idx := NewDomainIndex()
	idx.Add("example.com", "list_1")

	res := idx.Check("example.com.")
	if !res.Removed {
		t.Fatal("trailing dot should be stripped")
	}
}

func TestDomainIndex_EmptyDomain(t *testing.T) {
	idx := NewDomainIndex()

	res := idx.Check("")
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

// --- Integration tests ---

func TestServiceDedup_CreateRemovesCrossListDupes(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	resolver := &noopResolver{}
	q, c, _, _ := newTestNDMS()
	svc := NewService(store, q, c, resolver, noopLogger(), nil)

	// Create first list with example.com.
	list1, err := svc.Create(context.Background(), DomainList{
		Name:          "List A",
		ManualDomains: []string{"example.com", "google.com"},
		Routes:        []RouteTarget{{TunnelID: "t1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list1.Domains) != 2 {
		t.Fatalf("list1 domains = %d, want 2", len(list1.Domains))
	}
	if list1.LastDedupeReport != nil {
		t.Error("list1 should have no dedup report (no dupes)")
	}

	// Create second list with sub.example.com (covered by list1's example.com)
	// and google.com (exact dupe with list1).
	list2, err := svc.Create(context.Background(), DomainList{
		Name:          "List B",
		ManualDomains: []string{"sub.example.com", "google.com", "newsite.com"},
		Routes:        []RouteTarget{{TunnelID: "t1"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Only newsite.com should remain.
	if len(list2.Domains) != 1 {
		t.Fatalf("list2 domains = %v, want [newsite.com]", list2.Domains)
	}
	if list2.Domains[0] != "newsite.com" {
		t.Errorf("list2.Domains[0] = %q, want newsite.com", list2.Domains[0])
	}
	if list2.LastDedupeReport == nil {
		t.Fatal("list2 should have dedup report")
	}
	if list2.LastDedupeReport.TotalRemoved != 2 {
		t.Errorf("TotalRemoved = %d, want 2", list2.LastDedupeReport.TotalRemoved)
	}
}

// noopResolver implements InterfaceResolver for tests.
type noopResolver struct{}

func (r *noopResolver) ResolveInterface(ctx context.Context, tunnelID string) (string, error) {
	return tunnelID, nil
}

func (r *noopResolver) GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error) {
	return tunnelID, nil
}

// --- BuildIndex tests ---

func TestBuildIndex_RebuildAfterDelete(t *testing.T) {
	lists := []DomainList{
		{ID: "list_1", Domains: []string{"example.com"}},
		{ID: "list_2", Domains: []string{"google.com"}},
	}

	idx := BuildIndex(lists, "")
	res := idx.Check("example.com")
	if !res.Removed {
		t.Fatal("example.com should be claimed by list_1")
	}

	// Rebuild without list_1.
	lists = lists[1:]
	idx = BuildIndex(lists, "")

	res = idx.Check("example.com")
	if res.Removed {
		t.Fatal("after removing list_1, example.com should be available")
	}

	res = idx.Check("google.com")
	if !res.Removed {
		t.Fatal("google.com should still be claimed by list_2")
	}
}

func TestBuildIndex_ExcludeList(t *testing.T) {
	lists := []DomainList{
		{ID: "list_1", Domains: []string{"example.com"}},
		{ID: "list_2", Domains: []string{"google.com"}},
	}

	idx := BuildIndex(lists, "list_1")

	res := idx.Check("example.com")
	if res.Removed {
		t.Fatal("excluded list's domains should not be in index")
	}

	res = idx.Check("google.com")
	if !res.Removed {
		t.Fatal("list_2 domains should still be in index")
	}
}

func TestDedup_ParentWithExcludeAllowsChild(t *testing.T) {
	lists := []DomainList{
		{
			ID:       "list_google",
			Name:     "Google",
			Domains:  []string{"google.com"},
			Excludes: []string{"gemini.google.com"},
		},
	}
	idx := BuildIndex(lists, "")
	names := listNameMap(lists)

	kept, report := idx.CheckBatch([]string{"gemini.google.com"}, "list_gemini", names)

	if len(kept) != 1 || kept[0] != "gemini.google.com" {
		t.Fatalf("expected gemini.google.com to survive, got kept=%v", kept)
	}
	if report.TotalRemoved != 0 {
		t.Fatalf("expected no removals, got %d", report.TotalRemoved)
	}
}

func TestDedup_ParentWithExcludeStillCoversOtherChildren(t *testing.T) {
	lists := []DomainList{
		{
			ID:       "list_google",
			Name:     "Google",
			Domains:  []string{"google.com"},
			Excludes: []string{"gemini.google.com"},
		},
	}
	idx := BuildIndex(lists, "")
	names := listNameMap(lists)

	kept, report := idx.CheckBatch([]string{"mail.google.com"}, "list_other", names)

	if len(kept) != 0 {
		t.Fatalf("expected mail.google.com to be removed (still covered), got kept=%v", kept)
	}
	if report.WildcardDupes != 1 {
		t.Fatalf("expected 1 wildcard dup, got %d", report.WildcardDupes)
	}
}

func TestDedup_ExcludeHoleSubtree(t *testing.T) {
	lists := []DomainList{
		{
			ID:       "list_google",
			Name:     "Google",
			Domains:  []string{"google.com"},
			Excludes: []string{"gemini.google.com"},
		},
	}
	idx := BuildIndex(lists, "")
	names := listNameMap(lists)

	kept, report := idx.CheckBatch([]string{"chat.gemini.google.com"}, "list_gemini", names)

	if len(kept) != 1 {
		t.Fatalf("expected chat.gemini.google.com to survive (under hole subtree), got kept=%v", kept)
	}
	if report.TotalRemoved != 0 {
		t.Fatalf("expected no removals, got %d", report.TotalRemoved)
	}
}

func TestDedup_ExactMatchUnaffectedByExclude(t *testing.T) {
	// Even if the parent list excludes itself (degenerate config), an
	// exact match in another list still reports as a dup of the same
	// domain. Validation outside the dedupe layer catches the absurd
	// "exclude == include" config.
	lists := []DomainList{
		{
			ID:       "list_a",
			Name:     "A",
			Domains:  []string{"google.com"},
			Excludes: []string{"google.com"},
		},
	}
	idx := BuildIndex(lists, "")
	names := listNameMap(lists)

	_, report := idx.CheckBatch([]string{"google.com"}, "list_b", names)

	if report.ExactDupes != 1 {
		t.Fatalf("expected 1 exact dup, got %d", report.ExactDupes)
	}
}

func TestDedup_HoleDoesNotLeakAcrossLists(t *testing.T) {
	// List A excludes gemini.google.com. List B owns google.com without
	// excludes. List C tries to add gemini.google.com — must be removed
	// because list B still fully covers it.
	lists := []DomainList{
		{
			ID:       "list_a",
			Name:     "A",
			Domains:  []string{"google.com"},
			Excludes: []string{"gemini.google.com"},
		},
		{
			ID:      "list_b",
			Name:    "B",
			Domains: []string{"google.com"},
		},
	}
	idx := BuildIndex(lists, "")
	names := listNameMap(lists)

	kept, report := idx.CheckBatch([]string{"gemini.google.com"}, "list_c", names)

	if len(kept) != 0 {
		t.Fatalf("expected gemini.google.com removed (covered by list B), got %v", kept)
	}
	if report.WildcardDupes != 1 {
		t.Fatalf("expected 1 wildcard dup, got %d", report.WildcardDupes)
	}
}
