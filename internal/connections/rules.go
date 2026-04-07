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

// parseGroupNameToListID extracts the list ID from an awg-manager-managed
// object-group name. Returns ("", false) for non-awg group names or names
// without a numeric list segment.
//
// Format produced by dnsroute.buildGroupName:
//
//	AWG_<num>_<sanitized_name>_<chunk>
//
// where num is the digit suffix from a "list_<num>" ID. Examples:
//
//	"AWG_6_youtube_1"             -> "list_6"
//	"AWG_2_instagram_facebook_w_1" -> "list_2"
func parseGroupNameToListID(groupName string) (string, bool) {
	const prefix = "AWG_"
	if !strings.HasPrefix(groupName, prefix) {
		return "", false
	}
	rest := groupName[len(prefix):]
	if rest == "" {
		return "", false
	}
	// First segment up to the next "_" must be all digits.
	idx := strings.IndexByte(rest, '_')
	var num string
	if idx < 0 {
		num = rest
	} else {
		num = rest[:idx]
	}
	if num == "" {
		return "", false
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return "list_" + num, true
}

// buildIPRuleMap walks the parsed runtime groups and produces an IP -> []RuleHit
// lookup table. Non-AWG groups are skipped. The lister is consulted once to
// resolve list IDs to display names; lister failure is non-fatal — hits are
// returned with empty ListName.
func buildIPRuleMap(ctx context.Context, groups []runtimeGroup, lister DNSListLister) map[string][]RuleHit {
	// Resolve list ID -> name once. lister may be nil or may fail; both
	// are non-fatal for the resulting map.
	names := make(map[string]string)
	if lister != nil {
		if lists, err := lister.List(ctx); err == nil {
			for _, l := range lists {
				names[l.ID] = l.Name
			}
		}
	}

	out := make(map[string][]RuleHit)
	for _, g := range groups {
		listID, ok := parseGroupNameToListID(g.Name)
		if !ok {
			continue
		}
		listName := names[listID]
		for _, e := range g.Entries {
			for _, ip := range e.IPs {
				out[ip] = append(out[ip], RuleHit{
					ListID:   listID,
					ListName: listName,
					FQDN:     e.FQDN,
					Pattern:  e.Parent,
				})
			}
		}
	}
	return out
}
