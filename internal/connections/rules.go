package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/dnsroute"
)

// RuleHit is a single attribution of a destination IP to a DNS-route rule.
// One IP may have multiple hits when it resolves under more than one list
// (e.g. CDN IPs shared between YouTube and a hosting list).
type RuleHit struct {
	ListID   string `json:"listId"`
	ListName string `json:"listName,omitempty"`
	FQDN     string `json:"fqdn,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
}

// DNSListLister is the minimal interface connections needs to resolve a list
// ID into a human-readable name. The existing api.DNSRouteService satisfies
// it structurally — no adapter required.
type DNSListLister interface {
	List(ctx context.Context) ([]dnsroute.DomainList, error)
}

// runtimeGroup is a parsed object-group from /show/object-group/fqdn.
type runtimeGroup struct {
	Name    string
	Entries []runtimeEntry
}

// runtimeEntry is one resolved hostname inside a group.
type runtimeEntry struct {
	FQDN   string
	Parent string
	IPs    []string
}

// parseObjectGroupRuntime decodes the NDMS /show/object-group/fqdn response.
// IPv4 and IPv6 addresses are merged into a single IPs slice per entry; the
// caller does not care about the family at the lookup stage.
func parseObjectGroupRuntime(r io.Reader) ([]runtimeGroup, error) {
	var raw struct {
		Group []struct {
			GroupName string `json:"group-name"`
			Entry     []struct {
				FQDN   string `json:"fqdn"`
				Parent string `json:"parent"`
				IPv4   []struct {
					Address string `json:"address"`
				} `json:"ipv4"`
				IPv6 []struct {
					Address string `json:"address"`
				} `json:"ipv6"`
			} `json:"entry"`
		} `json:"group"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode object-group runtime: %w", err)
	}

	groups := make([]runtimeGroup, 0, len(raw.Group))
	for _, g := range raw.Group {
		rg := runtimeGroup{Name: g.GroupName}
		for _, e := range g.Entry {
			ips := make([]string, 0, len(e.IPv4)+len(e.IPv6))
			for _, a := range e.IPv4 {
				if a.Address != "" {
					ips = append(ips, a.Address)
				}
			}
			for _, a := range e.IPv6 {
				if a.Address != "" {
					ips = append(ips, a.Address)
				}
			}
			rg.Entries = append(rg.Entries, runtimeEntry{
				FQDN:   e.FQDN,
				Parent: e.Parent,
				IPs:    ips,
			})
		}
		groups = append(groups, rg)
	}
	return groups, nil
}

// parseGroupNameToSlug extracts the slug segment from an awg-manager-managed
// object-group name of the form "<slug>_p<N>". Returns ("", false) for names
// that don't match the expected shape (e.g. user-created NDMS groups).
//
// Examples:
//
//	"youtube_p1"              -> "youtube"
//	"instagram_facebook_w_p3" -> "instagram_facebook_w"
//	"1_p1"                    -> "1"   (fallback slug = numeric list ID)
func parseGroupNameToSlug(groupName string) (string, bool) {
	if !dnsroute.IsAWGManagedName(groupName) {
		return "", false
	}
	idx := strings.LastIndex(groupName, "_p")
	if idx <= 0 {
		return "", false
	}
	return groupName[:idx], true
}

// buildIPRuleMap walks the parsed runtime groups and produces an IP -> []RuleHit
// lookup table. Non-AWG groups are skipped. The lister is consulted once to
// reverse-map a slug (sanitized list name) back to its list ID and display
// name; lister failure is non-fatal — unresolved groups are simply skipped.
//
// Slug collisions (two lists sanitizing to the same slug) resolve to whichever
// list lands in the map last. This is a deliberate trade-off accepted when the
// group-name format was shortened from "AWG_<num>_<slug>_pN" to "AWG_<slug>_pN".
func buildIPRuleMap(ctx context.Context, groups []runtimeGroup, lister DNSListLister) map[string][]RuleHit {
	type listRef struct{ id, name string }
	slugs := make(map[string]listRef)
	if lister != nil {
		if lists, err := lister.List(ctx); err == nil {
			for _, l := range lists {
				slugs[dnsroute.GroupSlug(l.ID, l.Name)] = listRef{id: l.ID, name: l.Name}
			}
		}
	}

	out := make(map[string][]RuleHit)
	for _, g := range groups {
		slug, ok := parseGroupNameToSlug(g.Name)
		if !ok {
			continue
		}
		ref, known := slugs[slug]
		if !known {
			continue
		}
		for _, e := range g.Entries {
			for _, ip := range e.IPs {
				out[ip] = append(out[ip], RuleHit{
					ListID:   ref.id,
					ListName: ref.name,
					FQDN:     e.FQDN,
					Pattern:  e.Parent,
				})
			}
		}
	}
	return out
}
