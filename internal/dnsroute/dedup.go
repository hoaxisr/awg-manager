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
	// excludeOwners marks this node as an "exclude hole" for the listed
	// owner lists: when traversing through a parent owned by ownerID, if
	// any descendant on the path (including this node) has
	// excludeOwners[ownerID] == true, the parent does NOT cover the
	// requested domain.
	excludeOwners map[string]bool
}

// pending tracks an ancestor owner whose coverage hasn't yet been
// refuted by an exclude-hole on the traversal path.
type pending struct {
	ownerListID string
	domain      string
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

// normalizeDomain lowercases, trims whitespace, trailing dots,
// and leading dots (.ru → ru).
func normalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimRight(d, ".")
	d = strings.TrimLeft(d, ".")
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

// AddExcludeHole marks the node at `domain` as an exclude-hole for
// `ownerListID`. Walking through this node (or its descendants) suppresses
// coverage from a parent owned by ownerListID, letting another list claim
// the same subtree.
func (idx *DomainIndex) AddExcludeHole(domain string, ownerListID string) {
	domain = normalizeDomain(domain)
	if domain == "" || ownerListID == "" {
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
	if node.excludeOwners == nil {
		node.excludeOwners = make(map[string]bool)
	}
	node.excludeOwners[ownerListID] = true
}

// Check tests whether a domain is covered by an existing entry.
//
// A domain is covered if some ancestor has ownerListID == "L" AND no
// exclude-hole for "L" appears between that ancestor and the requested
// domain (inclusive on both ends).
//
// Holes refute wildcard coverage only. An exact match (full label
// traversal lands on a node with ownerListID) always reports as a dup,
// regardless of holes — it's a precise hit, not coverage.
//
// When multiple cross-list ancestors all cover the query (none refuted),
// the deepest surviving ancestor wins in CoveredBy. This is more
// informative than the pre-excludes algorithm (which short-circuited on
// the shallowest), and matches user expectation that the closest
// covering rule should be cited.
func (idx *DomainIndex) Check(domain string) CheckResult {
	domain = normalizeDomain(domain)
	if domain == "" {
		return CheckResult{}
	}

	labels := splitLabels(domain)
	node := idx.root
	var stack []pending

	for i, label := range labels {
		// Before descending, check whether the current node provides
		// new coverage from above (parent context, i.e. i > 0).
		if i > 0 && node.ownerListID != "" {
			stack = append(stack, pending{ownerListID: node.ownerListID, domain: node.domain})
		}

		// Holes at THIS node refute pending owners that match.
		if node.excludeOwners != nil {
			stack = filterPending(stack, node.excludeOwners)
		}

		child, ok := node.children[label]
		if !ok {
			// Path stops short — coverage exists iff a pending owner
			// survived holes along the way.
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				return CheckResult{
					Removed:     true,
					Reason:      "wildcard",
					CoveredBy:   top.domain,
					OwnerListID: top.ownerListID,
				}
			}
			return CheckResult{}
		}
		node = child
	}

	// Final node — exact match always wins, holes don't apply.
	if node.ownerListID != "" {
		return CheckResult{
			Removed:     true,
			Reason:      "exact",
			CoveredBy:   node.domain,
			OwnerListID: node.ownerListID,
		}
	}

	// Holes at the final node also refute pending owners.
	if node.excludeOwners != nil {
		stack = filterPending(stack, node.excludeOwners)
	}

	if len(stack) > 0 {
		top := stack[len(stack)-1]
		return CheckResult{
			Removed:     true,
			Reason:      "wildcard",
			CoveredBy:   top.domain,
			OwnerListID: top.ownerListID,
		}
	}
	return CheckResult{}
}

// filterPending keeps only those pending owners NOT marked as a hole
// in `holes`. Order is preserved.
func filterPending(stack []pending, holes map[string]bool) []pending {
	out := stack[:0]
	for _, p := range stack {
		if !holes[p.ownerListID] {
			out = append(out, p)
		}
	}
	return out
}

// BuildIndex builds a DomainIndex from all existing lists, optionally
// excluding one list. Each list's Excludes register as exclude-holes
// owned by that list, so a parent domain only covers subdomains that
// don't fall under the list's own excludes.
func BuildIndex(lists []DomainList, excludeListID string) *DomainIndex {
	idx := NewDomainIndex()
	for i := range lists {
		if lists[i].ID == excludeListID {
			continue
		}
		for _, d := range lists[i].Domains {
			idx.Add(d, lists[i].ID)
		}
		for _, e := range lists[i].Excludes {
			idx.AddExcludeHole(e, lists[i].ID)
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
		if res := idx.Check(d); res.Removed {
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
		if res := work.Check(d); res.Removed {
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
