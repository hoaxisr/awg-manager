package dnsroute

import (
	"strings"
)

// CheckResult is the outcome of checking a single domain against the index.
type CheckResult struct {
	Removed     bool
	Reason      string // "exact" or "wildcard"
	CoveredBy   string // the domain that covers this one
	OwnerListID string // list ID that owns the covering domain
}

// trieNode is a node in the reverse-label trie.
type trieNode struct {
	children    map[string]*trieNode
	ownerListID string // non-empty if a domain is registered at this node
	domain      string // the full domain registered here (for CoveredBy reporting)
}

// DomainIndex is a reverse-label trie for efficient domain deduplication.
// Domains are stored by splitting labels and reversing: "sub.example.com" -> ["com", "example", "sub"].
// This allows parent domains to cover all subdomains during traversal.
type DomainIndex struct {
	root *trieNode
}

// NewDomainIndex creates an empty DomainIndex.
func NewDomainIndex() *DomainIndex {
	return &DomainIndex{
		root: &trieNode{children: make(map[string]*trieNode)},
	}
}

// normalizeDomain lowercases, trims whitespace and trailing dots.
func normalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimRight(d, ".")
	return d
}

// splitLabels splits a domain into reversed labels: "sub.example.com" -> ["com", "example", "sub"].
func splitLabels(domain string) []string {
	parts := strings.Split(domain, ".")
	// Reverse in place.
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return parts
}

// Add registers a domain as owned by the given list.
// If the node already has an owner, the first owner wins.
func (idx *DomainIndex) Add(domain string, listID string) {
	domain = normalizeDomain(domain)
	if domain == "" {
		return
	}

	labels := splitLabels(domain)
	node := idx.root
	for _, label := range labels {
		child, ok := node.children[label]
		if !ok {
			child = &trieNode{children: make(map[string]*trieNode)}
			node.children[label] = child
		}
		node = child
	}
	// First owner wins.
	if node.ownerListID == "" {
		node.ownerListID = listID
		node.domain = domain
	}
}

// Check tests whether a domain is covered by an existing entry in the index.
// A domain is covered if:
//   - An exact match exists (any list, including same list) -> reason "exact"
//   - A parent domain exists (any ancestor in the label hierarchy) -> reason "wildcard"
func (idx *DomainIndex) Check(domain string, currentListID string) CheckResult {
	domain = normalizeDomain(domain)
	if domain == "" {
		return CheckResult{}
	}

	labels := splitLabels(domain)
	node := idx.root
	for i, label := range labels {
		// Check if current node has an owner (parent domain covers this one).
		if node.ownerListID != "" && i > 0 {
			return CheckResult{
				Removed:     true,
				Reason:      "wildcard",
				CoveredBy:   node.domain,
				OwnerListID: node.ownerListID,
			}
		}

		child, ok := node.children[label]
		if !ok {
			// No match at all -- domain is not covered.
			return CheckResult{}
		}
		node = child
	}

	// We traversed all labels. Check the final node.
	if node.ownerListID != "" {
		return CheckResult{
			Removed:     true,
			Reason:      "exact",
			CoveredBy:   node.domain,
			OwnerListID: node.ownerListID,
		}
	}

	return CheckResult{}
}

// BuildIndex builds a DomainIndex from all existing lists, optionally excluding one list.
func BuildIndex(lists []DomainList, excludeListID string) *DomainIndex {
	idx := NewDomainIndex()
	for i := range lists {
		if lists[i].ID == excludeListID {
			continue
		}
		for _, d := range lists[i].Domains {
			idx.Add(d, lists[i].ID)
		}
	}
	return idx
}

// listNameMap builds a map of listID → listName from all lists.
func listNameMap(lists []DomainList) map[string]string {
	m := make(map[string]string, len(lists))
	for i := range lists {
		m[lists[i].ID] = lists[i].Name
	}
	return m
}

// CheckBatch checks a batch of domains against the index and returns
// the kept domains and a deduplication report.
func (idx *DomainIndex) CheckBatch(domains []string, currentListID string, listNames map[string]string) ([]string, DedupeReport) {
	report := DedupeReport{TotalInput: len(domains)}
	if len(domains) == 0 {
		return nil, report
	}

	work := NewDomainIndex()
	var kept []string

	for _, raw := range domains {
		d := normalizeDomain(raw)
		if d == "" {
			continue
		}

		// 1. Check against existing index (cross-list).
		if res := idx.Check(d, currentListID); res.Removed {
			reason := res.Reason
			report.TotalRemoved++
			if reason == "exact" {
				report.ExactDupes++
			} else {
				report.WildcardDupes++
			}
			report.Items = append(report.Items, DedupeItem{
				Domain:    d,
				Reason:    reason,
				CoveredBy: res.CoveredBy,
				ListID:    res.OwnerListID,
				ListName:  listNames[res.OwnerListID],
			})
			continue
		}

		// 2. Check against working index (internal batch dedup).
		if res := work.Check(d, currentListID); res.Removed {
			reason := res.Reason
			report.TotalRemoved++
			if reason == "exact" {
				report.ExactDupes++
			} else {
				report.WildcardDupes++
			}
			report.Items = append(report.Items, DedupeItem{
				Domain:    d,
				Reason:    reason,
				CoveredBy: res.CoveredBy,
				ListID:    currentListID,
				ListName:  listNames[currentListID],
			})
			continue
		}

		// 3. Not a dupe — keep it and add to working index.
		work.Add(d, currentListID)
		kept = append(kept, d)
	}

	report.TotalKept = len(kept)
	return kept, report
}
