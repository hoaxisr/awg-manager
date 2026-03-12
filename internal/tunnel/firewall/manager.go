// Package firewall provides an interface for iptables firewall management.
package firewall

import "context"

// Manager is the interface for firewall rule management.
type Manager interface {
	// AddRules adds all required iptables rules for a tunnel interface.
	// Rules added:
	//   - INPUT -i <iface> -j ACCEPT
	//   - OUTPUT -o <iface> -j ACCEPT
	//   - FORWARD -i <iface> -j ACCEPT
	//   - FORWARD -o <iface> -j ACCEPT
	//   - nat POSTROUTING -o <iface> -j MASQUERADE (NEW in v2!)
	AddRules(ctx context.Context, iface string) error

	// RemoveRules removes all iptables rules for a tunnel interface.
	RemoveRules(ctx context.Context, iface string) error

	// HasRules checks if rules exist for an interface.
	HasRules(ctx context.Context, iface string) bool
}

// Rule represents an iptables rule.
type Rule struct {
	Table     string // "filter" or "nat"
	Chain     string // "INPUT", "OUTPUT", "FORWARD", "POSTROUTING"
	Interface string // Interface name
	Direction string // "-i" (input) or "-o" (output)
	Target    string // "ACCEPT" or "MASQUERADE"
}

// StandardRules returns the list of standard rules for a tunnel interface.
// This includes the NAT rule that was missing in v1.
func StandardRules(iface string) []Rule {
	return []Rule{
		// Filter table - traffic acceptance
		{Table: "filter", Chain: "INPUT", Interface: iface, Direction: "-i", Target: "ACCEPT"},
		{Table: "filter", Chain: "OUTPUT", Interface: iface, Direction: "-o", Target: "ACCEPT"},
		{Table: "filter", Chain: "FORWARD", Interface: iface, Direction: "-i", Target: "ACCEPT"},
		{Table: "filter", Chain: "FORWARD", Interface: iface, Direction: "-o", Target: "ACCEPT"},
		// NAT table - masquerade for LAN clients (NEW in v2!)
		{Table: "nat", Chain: "POSTROUTING", Interface: iface, Direction: "-o", Target: "MASQUERADE"},
	}
}
