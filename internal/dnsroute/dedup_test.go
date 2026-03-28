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
