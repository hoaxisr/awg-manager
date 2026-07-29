//go:build linux

package listenfirewall

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

// Apply inserts an INPUT accept rule for proto/port.
func Apply(ctx context.Context, port int, proto string) error {
	proto = normalizeProto(proto)
	if port <= 0 {
		return fmt.Errorf("listen firewall: invalid port %d", port)
	}
	flush(ctx, port, proto)
	if err := iptables.Run(ctx, "-I", "INPUT", "1",
		"-p", proto, "-m", proto, "--dport", fmt.Sprintf("%d", port),
		"-m", "comment", "--comment", Comment,
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
	out, err := iptables.RunOutput(ctx, "-S", "INPUT")
	if err != nil {
		return false
	}
	portArg := fmt.Sprintf("--dport %d", port)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, Comment) {
			continue
		}
		if !strings.Contains(line, "-p "+proto) {
			continue
		}
		if strings.Contains(line, portArg) {
			return true
		}
	}
	return false
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
}

func flush(ctx context.Context, port int, proto string) {
	proto = normalizeProto(proto)
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
		if !strings.Contains(line, Comment) {
			continue
		}
		fields := strings.Fields(line)
		proto := ""
		port := 0
		for i, f := range fields {
			if f == "-p" && i+1 < len(fields) {
				proto = fields[i+1]
			}
			if f == "--dport" && i+1 < len(fields) {
				fmt.Sscanf(fields[i+1], "%d", &port)
			}
		}
		if port <= 0 || proto == "" {
			continue
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
