//go:build linux

package listenfirewall

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const listenNetfilterHookPath = "/opt/etc/ndm/netfilter.d/62-awgm-listen-ports.sh"

// Apply inserts an INPUT accept rule for proto/port.
func Apply(ctx context.Context, port int, proto string) error {
	proto = normalizeProto(proto)
	if port <= 0 {
		return fmt.Errorf("listen firewall: invalid port %d", port)
	}
	if Present(ctx, port, proto) {
		return nil
	}
	// Без -m comment: на Keenetic xt_comment часто не загружен (#666).
	if err := iptables.Run(ctx, "-I", "INPUT", "1",
		"-p", proto, "-m", proto, "--dport", fmt.Sprintf("%d", port),
		"-j", "ACCEPT"); err != nil {
		return fmt.Errorf("INPUT accept %s/%d: %w", proto, port, err)
	}
	return nil
}

// Remove drops managed INPUT rules for proto/port.
func Remove(ctx context.Context, port int, proto string) {
	flush(ctx, port, proto)
}

// Present reports whether a managed INPUT rule exists.
func Present(ctx context.Context, port int, proto string) bool {
	proto = normalizeProto(proto)
	if port <= 0 {
		return false
	}
	return iptables.Run(ctx, "-C", "INPUT",
		"-p", proto, "-m", proto, "--dport", fmt.Sprintf("%d", port),
		"-j", "ACCEPT") == nil
}

// Reconcile ensures desired ports are open and removes stale rules.
func Reconcile(ctx context.Context, desired []PortSpec) {
	desired = MergePortSpecs(desired)
	want := make(map[string]struct{}, len(desired))
	for _, spec := range desired {
		want[portKey(spec.Port, spec.Proto)] = struct{}{}
	}
	for _, spec := range listManaged(ctx) {
		key := portKey(spec.Port, spec.Proto)
		if _, ok := want[key]; ok {
			continue
		}
		flush(ctx, spec.Port, spec.Proto)
	}
	for _, spec := range desired {
		if Present(ctx, spec.Port, spec.Proto) {
			continue
		}
		_ = Apply(ctx, spec.Port, spec.Proto)
	}
	if len(desired) == 0 {
		removeListenNetfilterHook()
		return
	}
	_ = ensureListenNetfilterHook(ctx, desired)
}

func flush(ctx context.Context, port int, proto string) {
	proto = normalizeProto(proto)
	for i := 0; i < 5; i++ {
		_ = iptables.Run(ctx, "-D", "INPUT",
			"-p", proto, "-m", proto, "--dport", fmt.Sprintf("%d", port),
			"-j", "ACCEPT")
	}
	out, err := iptables.RunOutput(ctx, "-S", "INPUT")
	if err != nil {
		return
	}
	portArg := fmt.Sprintf("--dport %d", port)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, Comment) {
			continue
		}
		if !strings.Contains(line, "-p "+proto) || !strings.Contains(line, portArg) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" {
			continue
		}
		_ = iptables.Run(ctx, append([]string{"-D"}, fields[1:]...)...)
	}
}

func listManaged(ctx context.Context) []PortSpec {
	out, err := iptables.RunOutput(ctx, "-S", "INPUT")
	if err != nil {
		return nil
	}
	var specs []PortSpec
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		proto := ""
		port := 0
		isAccept := false
		hasComment := strings.Contains(line, Comment)
		for i, f := range fields {
			if f == "-p" && i+1 < len(fields) {
				proto = fields[i+1]
			}
			if f == "--dport" && i+1 < len(fields) {
				fmt.Sscanf(fields[i+1], "%d", &port)
			}
			if f == "ACCEPT" {
				isAccept = true
			}
		}
		if !isAccept || port <= 0 || proto == "" {
			continue
		}
		if !hasComment && !strings.Contains(line, "-j ACCEPT") {
			continue
		}
		if !hasComment {
			// Only track bare ACCEPT rules we reconcile (top-of-chain dport rules).
			if !strings.Contains(line, "-m "+proto) {
				continue
			}
		}
		key := portKey(port, proto)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		specs = append(specs, PortSpec{Port: port, Proto: proto})
	}
	return specs
}

func portKey(port int, proto string) string {
	return normalizeProto(proto) + ":" + fmt.Sprintf("%d", port)
}

func normalizeProto(proto string) string {
	p := strings.ToLower(strings.TrimSpace(proto))
	if p == "" {
		return "udp"
	}
	return p
}

func listenNetfilterHookScript(specs []PortSpec) string {
	specs = MergePortSpecs(specs)
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Proto != specs[j].Proto {
			return specs[i].Proto < specs[j].Proto
		}
		return specs[i].Port < specs[j].Port
	})
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# AWG Manager: INPUT ACCEPT for proxy listen ports (survives NDMS reload).\n")
	b.WriteString("[ \"$type\" = \"ip6tables\" ] && exit 0\n")
	b.WriteString("[ \"$table\" = \"filter\" ] || exit 0\n")
	b.WriteString("IPTABLES=/opt/sbin/iptables\n")
	b.WriteString("[ -x \"$IPTABLES\" ] || IPTABLES=iptables\n")
	b.WriteString("run() { \"$IPTABLES\" -w \"$@\" 2>/dev/null || \"$IPTABLES\" \"$@\" 2>/dev/null; }\n")
	for _, spec := range specs {
		proto := normalizeProto(spec.Proto)
		fmt.Fprintf(&b, "run -C INPUT -p %s -m %s --dport %d -j ACCEPT || run -I INPUT 1 -p %s -m %s --dport %d -j ACCEPT\n",
			proto, proto, spec.Port, proto, proto, spec.Port)
	}
	b.WriteString("exit 0\n")
	return b.String()
}

func ensureListenNetfilterHook(ctx context.Context, specs []PortSpec) error {
	specs = MergePortSpecs(specs)
	if len(specs) == 0 {
		removeListenNetfilterHook()
		return nil
	}
	script := listenNetfilterHookScript(specs)
	if err := storage.AtomicWritePerm(listenNetfilterHookPath, []byte(script), 0o755); err != nil {
		return err
	}
	_, err := exec.Run(ctx, "sh", "-c", "table=filter type=iptables sh "+listenNetfilterHookPath)
	return err
}

func removeListenNetfilterHook() {
	_ = os.Remove(listenNetfilterHookPath)
}
