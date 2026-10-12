package telemt

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const (
	telemtFirewallComment   = "AWGM_TELEMT_LISTEN"
	telemtNetfilterHookPath = "/opt/etc/ndm/netfilter.d/63-awgm-telemt.sh"
)

func telemtRuleArgs(iface string, port int) []string {
	args := []string{"-i", iface, "-p", "tcp", "-m", "tcp", "--dport", strconv.Itoa(port), "-m", "comment", "--comment", telemtFirewallComment, "-j", "ACCEPT"}
	return args
}

// applyFirewall inserts an INPUT accept rule for tcp/port on iface with comment,
// and ensures the netfilter.d hook script is written to survive WAN reloads.
func applyFirewall(ctx context.Context, iface string, port int) error {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return ErrInterfaceRequired
	}
	if port <= 0 {
		return fmt.Errorf("%w: %d", ErrInvalidPort, port)
	}

	// Check if already present with tag
	if err := iptables.Run(ctx, append([]string{"-C", "INPUT"}, telemtRuleArgs(iface, port)...)...); err != nil {
		// Insert at top of INPUT with comment
		if err := iptables.Run(ctx, append([]string{"-I", "INPUT", "1"}, telemtRuleArgs(iface, port)...)...); err != nil {
			return fmt.Errorf("telemt INPUT accept -i %s tcp/%d: %w", iface, port, err)
		}
	}

	return ensureTelemtNetfilterHook(ctx, iface, port)
}

// removeFirewall drops only our tagged INPUT rules for tcp/port, and removes the netfilter.d hook.
// If port <= 0, all telemt tagged rules are removed.
func removeFirewall(ctx context.Context, iface string, port int) {
	removeTelemtNetfilterHook()
	out, err := iptables.RunOutput(ctx, "-S", "INPUT")
	if err != nil {
		if iface != "" && port > 0 {
			for {
				if err := iptables.Run(ctx, append([]string{"-D", "INPUT"}, telemtRuleArgs(iface, port)...)...); err != nil {
					break
				}
			}
		}
		return
	}
	var portArg string
	if port > 0 {
		portArg = fmt.Sprintf("--dport %d", port)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, telemtFirewallComment) {
			continue
		}
		if !strings.Contains(line, "-p tcp") {
			continue
		}
		if portArg != "" && !strings.Contains(line, portArg) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" {
			continue
		}
		_ = iptables.Run(ctx, append([]string{"-D"}, fields[1:]...)...)
	}
}

func telemtNetfilterHookScript(iface string, port int) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# AWG Manager: INPUT ACCEPT for telemt MTProxy listen port (survives NDMS reload).\n")
	b.WriteString("[ \"$type\" = \"ip6tables\" ] && exit 0\n")
	b.WriteString("[ \"$table\" = \"filter\" ] || exit 0\n")
	b.WriteString("IPTABLES=/opt/sbin/iptables\n")
	b.WriteString("[ -x \"$IPTABLES\" ] || IPTABLES=iptables\n")
	b.WriteString("run() { \"$IPTABLES\" -w \"$@\" 2>/dev/null || \"$IPTABLES\" \"$@\" 2>/dev/null; }\n")
	args := strings.Join(telemtRuleArgs(iface, port), " ")
	fmt.Fprintf(&b, "run -C INPUT %s || run -I INPUT 1 %s\n", args, args)
	b.WriteString("exit 0\n")
	return b.String()
}

func ensureTelemtNetfilterHook(ctx context.Context, iface string, port int) error {
	script := telemtNetfilterHookScript(iface, port)
	if err := storage.AtomicWritePerm(telemtNetfilterHookPath, []byte(script), 0o755); err != nil {
		return err
	}
	_, err := exec.Run(ctx, "sh", "-c", "table=filter type=iptables sh "+telemtNetfilterHookPath)
	return err
}

func removeTelemtNetfilterHook() {
	_ = os.Remove(telemtNetfilterHookPath)
}
