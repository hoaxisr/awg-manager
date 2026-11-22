package telemt

import (
	"context"
	"fmt"
	"strconv"
)

// iptablesRunner runs a single iptables invocation. firewallRun is nil on
// platforms without iptables; tests substitute an in-memory chain.
type iptablesRunner func(ctx context.Context, args ...string) error

const telemtFirewallComment = "AWGM_TELEMT_LISTEN"

func telemtRuleArgs(port int, tagged bool) []string {
	args := []string{"-p", "tcp", "-m", "tcp", "--dport", strconv.Itoa(port)}
	if tagged {
		args = append(args, "-m", "comment", "--comment", telemtFirewallComment)
	}
	return append(args, "-j", "ACCEPT")
}

// maxFirewallDeletes bounds duplicate-removal loops so a misbehaving iptables
// can never hang Stop/Close.
const maxFirewallDeletes = 16

// applyFirewall opens tcp/port in INPUT. Idempotent: an existing rule of
// either form (tagged, or an identical untagged rule) is reused, not duplicated.
//
// untaggedOwned reports that this call inserted the untagged fallback (no
// xt_comment). That form is indistinguishable from a user's own rule, so only
// the caller that inserted it may later remove it via removeFirewall.
func applyFirewall(ctx context.Context, port int) (untaggedOwned bool, err error) {
	run := firewallRun
	if run == nil {
		return false, nil
	}
	if port <= 0 {
		return false, fmt.Errorf("telemt firewall: invalid port %d", port)
	}
	for _, tagged := range []bool{true, false} {
		if run(ctx, append([]string{"-C", "INPUT"}, telemtRuleArgs(port, tagged)...)...) == nil {
			return false, nil
		}
	}
	if run(ctx, append([]string{"-I", "INPUT", "1"}, telemtRuleArgs(port, true)...)...) == nil {
		return false, nil
	}
	// Fallback without comment for firmware lacking xt_comment: the port must
	// open, at the cost of an unmarked rule (same trade-off as listenfirewall).
	if err := run(ctx, append([]string{"-I", "INPUT", "1"}, telemtRuleArgs(port, false)...)...); err != nil {
		return false, fmt.Errorf("telemt INPUT accept tcp/%d: %w", port, err)
	}
	return true, nil
}

// removeFirewall deletes telemt's tagged rules for port and, only when
// untaggedOwned, the single untagged rule this service inserted itself.
// Untagged rules it did not insert (user hooks, other components, older
// awg-manager runs) are left in place.
func removeFirewall(ctx context.Context, port int, untaggedOwned bool) {
	run := firewallRun
	if run == nil || port <= 0 {
		return
	}
	for i := 0; i < maxFirewallDeletes; i++ {
		if run(ctx, append([]string{"-D", "INPUT"}, telemtRuleArgs(port, true)...)...) != nil {
			break
		}
	}
	if untaggedOwned {
		_ = run(ctx, append([]string{"-D", "INPUT"}, telemtRuleArgs(port, false)...)...)
	}
}
