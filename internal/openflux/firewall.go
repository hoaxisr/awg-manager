package openflux

import (
	"context"
	"fmt"
	"strconv"
)

// iptablesRunner executes a single iptables invocation. firewallRun is nil on
// platforms without iptables; tests substitute an in-memory fake chain.
type iptablesRunner func(ctx context.Context, args ...string) error

const openfluxFirewallComment = "AWGM_OPENFLUX_LISTEN"

const maxFirewallDeletes = 16

func openfluxRuleArgs(port int, tagged bool) []string {
	args := []string{"-p", "tcp", "-m", "tcp", "--dport", strconv.Itoa(port)}
	if tagged {
		args = append(args, "-m", "comment", "--comment", openfluxFirewallComment)
	}
	return append(args, "-j", "ACCEPT")
}

// applyListenFirewall opens tcp/port in INPUT using an owned rule comment when supported.
// untaggedOwned reports whether this call inserted an untagged fallback rule (without xt_comment)
// so only that caller may delete it later. Pre-existing untagged rules are reused without
// claiming ownership.
func applyListenFirewall(ctx context.Context, port int) (untaggedOwned bool, err error) {
	run := firewallRun
	if run == nil {
		return false, nil
	}
	if port <= 0 {
		return false, fmt.Errorf("openflux firewall: недопустимый порт %d", port)
	}
	for _, tagged := range []bool{true, false} {
		if run(ctx, append([]string{"-C", "INPUT"}, openfluxRuleArgs(port, tagged)...)...) == nil {
			return false, nil
		}
	}
	if run(ctx, append([]string{"-I", "INPUT", "1"}, openfluxRuleArgs(port, true)...)...) == nil {
		return false, nil
	}
	if err := run(ctx, append([]string{"-I", "INPUT", "1"}, openfluxRuleArgs(port, false)...)...); err != nil {
		return false, fmt.Errorf("openflux INPUT accept tcp/%d: %w", port, err)
	}
	return true, nil
}

// removeListenFirewall deletes only OpenFlux-owned INPUT rules for port.
// Pre-existing untagged rules installed by external services or users are preserved.
func removeListenFirewall(ctx context.Context, port int, untaggedOwned bool) error {
	run := firewallRun
	if run == nil || port <= 0 {
		return nil
	}
	for i := 0; i < maxFirewallDeletes; i++ {
		if run(ctx, append([]string{"-D", "INPUT"}, openfluxRuleArgs(port, true)...)...) != nil {
			break
		}
	}
	if untaggedOwned {
		if err := run(ctx, append([]string{"-D", "INPUT"}, openfluxRuleArgs(port, false)...)...); err != nil {
			return fmt.Errorf("openflux remove INPUT rule tcp/%d: %w", port, err)
		}
	}
	return nil
}
