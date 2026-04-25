package dnsroute

import "net"

// cidrCovers returns true if network a fully contains network b.
func cidrCovers(a, b *net.IPNet) bool {
	aOnes, aBits := a.Mask.Size()
	bOnes, bBits := b.Mask.Size()
	if aBits != bBits {
		return false
	}
	return aOnes <= bOnes && a.Contains(b.IP)
}

type parsedSubnet struct {
	raw      string
	net      *net.IPNet
	listID   string
	listName string
	// holes carries the owner list's ExcludeSubnets — pre-parsed so we
	// don't repeat work per candidate. A candidate that falls into any
	// hole is NOT covered by this list, even if cidrCovers reports true.
	holes []*net.IPNet
}

// candidateInHoles reports whether `c` falls inside any of `holes`,
// matching either an exact CIDR or a parent CIDR (subtree semantics).
func candidateInHoles(c *net.IPNet, holes []*net.IPNet) bool {
	for _, h := range holes {
		if cidrCovers(h, c) {
			return true
		}
	}
	return false
}

func dedupSubnets(input []string, currentListID string, existingLists []DomainList) ([]string, DedupeReport) {
	report := DedupeReport{TotalInput: len(input)}
	if len(input) == 0 {
		return nil, report
	}

	var existing []parsedSubnet
	for i := range existingLists {
		if existingLists[i].ID == currentListID {
			continue
		}
		var holes []*net.IPNet
		for _, h := range existingLists[i].ExcludeSubnets {
			if _, hn, err := net.ParseCIDR(h); err == nil {
				holes = append(holes, hn)
			}
		}
		for _, s := range existingLists[i].Subnets {
			_, n, err := net.ParseCIDR(s)
			if err != nil {
				continue
			}
			existing = append(existing, parsedSubnet{
				raw:      n.String(),
				net:      n,
				listID:   existingLists[i].ID,
				listName: existingLists[i].Name,
				holes:    holes,
			})
		}
	}

	var kept []string
	var keptParsed []parsedSubnet

	for _, raw := range input {
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			report.TotalInput--
			continue
		}
		normalized := n.String()
		removed := false

		for _, ex := range existing {
			if ex.raw == normalized {
				report.TotalRemoved++
				report.ExactDupes++
				report.Items = append(report.Items, DedupeItem{Domain: normalized, Reason: "exact", CoveredBy: ex.raw, ListID: ex.listID, ListName: ex.listName})
				removed = true
				break
			}
			if cidrCovers(ex.net, n) {
				// Hole carve-out: candidate inside any of the owner's
				// ExcludeSubnets means the owner does NOT cover it.
				if candidateInHoles(n, ex.holes) {
					continue
				}
				report.TotalRemoved++
				report.WildcardDupes++
				report.Items = append(report.Items, DedupeItem{Domain: normalized, Reason: "subnet_covered", CoveredBy: ex.raw, ListID: ex.listID, ListName: ex.listName})
				removed = true
				break
			}
		}
		if removed {
			continue
		}

		for _, k := range keptParsed {
			if k.raw == normalized {
				report.TotalRemoved++
				report.ExactDupes++
				report.Items = append(report.Items, DedupeItem{Domain: normalized, Reason: "exact", CoveredBy: k.raw, ListID: currentListID})
				removed = true
				break
			}
			if cidrCovers(k.net, n) {
				report.TotalRemoved++
				report.WildcardDupes++
				report.Items = append(report.Items, DedupeItem{Domain: normalized, Reason: "subnet_covered", CoveredBy: k.raw, ListID: currentListID})
				removed = true
				break
			}
		}
		if removed {
			continue
		}

		kept = append(kept, normalized)
		keptParsed = append(keptParsed, parsedSubnet{raw: normalized, net: n, listID: currentListID})
	}

	report.TotalKept = len(kept)
	return kept, report
}
