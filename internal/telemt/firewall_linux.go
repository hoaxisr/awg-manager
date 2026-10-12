//go:build linux

package telemt

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const telemtFirewallComment = "AWGM_TELEMT_LISTEN"

func telemtRuleArgs(port int, tagged bool) []string {
	args := []string{"-p", "tcp", "-m", "tcp", "--dport", strconv.Itoa(port)}
	if tagged {
		args = append(args, "-m", "comment", "--comment", telemtFirewallComment)
	}
	return append(args, "-j", "ACCEPT")
}

func applyFirewall(ctx context.Context, port int) error {
	if port <= 0 {
		return fmt.Errorf("telemt firewall: invalid port %d", port)
	}
	// Check if already present with tag
	if err := iptables.Run(ctx, append([]string{"-C", "INPUT"}, telemtRuleArgs(port, true)...)...); err == nil {
		return nil
	}
	// Insert at top of INPUT with comment
	if err := iptables.Run(ctx, append([]string{"-I", "INPUT", "1"}, telemtRuleArgs(port, true)...)...); err == nil {
		return nil
	}
	// Fallback without comment if xt_comment not supported
	if err := iptables.Run(ctx, append([]string{"-I", "INPUT", "1"}, telemtRuleArgs(port, false)...)...); err != nil {
		return fmt.Errorf("telemt INPUT accept tcp/%d: %w", port, err)
	}
	return nil
}

func removeFirewall(ctx context.Context, port int) {
	if port <= 0 {
		return
	}
	// Remove all instances of tagged rule
	for {
		if err := iptables.Run(ctx, append([]string{"-D", "INPUT"}, telemtRuleArgs(port, true)...)...); err != nil {
			break
		}
	}
	// Also attempt remove untagged rule if present
	_ = iptables.Run(ctx, append([]string{"-D", "INPUT"}, telemtRuleArgs(port, false)...)...)
}
