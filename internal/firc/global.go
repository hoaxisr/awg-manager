package firc

import (
	"fmt"
	"slices"
	"strings"
)

const globalGroupID = "c100d002"

// The IPv4 complement of local, private, shared, multicast and reserved ranges.
// Match numeric destinations as well as destinations obtained via DNS.
const globalCIDRs = `1.0.0.0/8 2.0.0.0/7 4.0.0.0/6 8.0.0.0/7 11.0.0.0/8 12.0.0.0/6 16.0.0.0/4 32.0.0.0/3 64.0.0.0/3 96.0.0.0/6 100.0.0.0/10 100.128.0.0/9 101.0.0.0/8 102.0.0.0/7 104.0.0.0/5 112.0.0.0/5 120.0.0.0/6 124.0.0.0/7 126.0.0.0/8 128.0.0.0/3 160.0.0.0/5 168.0.0.0/8 169.0.0.0/9 169.128.0.0/10 169.192.0.0/11 169.224.0.0/12 169.240.0.0/13 169.248.0.0/14 169.252.0.0/15 169.255.0.0/16 170.0.0.0/7 172.0.0.0/12 172.32.0.0/11 172.64.0.0/10 172.128.0.0/9 173.0.0.0/8 174.0.0.0/7 176.0.0.0/4 192.0.0.0/9 192.128.0.0/11 192.160.0.0/13 192.169.0.0/16 192.170.0.0/15 192.172.0.0/14 192.176.0.0/12 192.192.0.0/10 193.0.0.0/8 194.0.0.0/7 196.0.0.0/6 200.0.0.0/5 208.0.0.0/4 2000::/3`

func globalGroup(ext ExtConfig) (Group, bool) {
	kind, target, ok := strings.Cut(ext.GlobalTarget, ":")
	if !ok || target == "" {
		return Group{}, false
	}
	var interfaces []string
	switch kind {
	case "iface":
		interfaces = []string{target}
	case "pg":
		for _, pg := range ext.ProxyGroups {
			if pg.ID == target {
				interfaces = slices.Clone(pg.Interfaces)
				break
			}
		}
	}
	if len(interfaces) == 0 {
		return Group{}, false
	}
	g := Group{ID: globalGroupID, Name: "Global", Enable: true, Interface: GroupInterface(interfaces)}
	for i, cidr := range strings.Fields(globalCIDRs) {
		typ := "subnet"
		if strings.Contains(cidr, ":") {
			typ = "subnet6"
		}
		g.Rules = append(g.Rules, Rule{ID: fmt.Sprintf("c2%06x", i+1), Type: typ, Rule: cidr, Enable: true})
	}
	return g, true
}
