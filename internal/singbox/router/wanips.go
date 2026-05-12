package router

import (
	"context"
	"sort"
	"strings"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// parseDefaultIfaces extracts all interface names appearing as `dev X`
// in `default ...` rows of `ip route show table all` output. Returns
// sorted, deduplicated list. Empty input yields empty slice.
func parseDefaultIfaces(routeOutput string) []string {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(routeOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "default ") {
			continue
		}
		// Tokens: ["default", "dev", "X", ...]
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "dev" {
				seen[fields[i+1]] = struct{}{}
				break
			}
		}
	}
	out := make([]string, 0, len(seen))
	for iface := range seen {
		out = append(out, iface)
	}
	sort.Strings(out)
	return out
}

// parseInetAddrs extracts IPv4 addresses from `ip -4 addr show dev X`
// output. Each address is normalized to "X.X.X.X/32". The peer field
// (point-to-point partner address) is deliberately ignored: it's the
// remote end's address, not ours, and bypassing it would silently
// route policy-bound traffic past TPROXY.
func parseInetAddrs(addrOutput string) []string {
	var out []string
	for _, line := range strings.Split(addrOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "inet ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// fields[1] is either "X.X.X.X" or "X.X.X.X/N".
		addr := fields[1]
		if idx := strings.IndexByte(addr, '/'); idx >= 0 {
			addr = addr[:idx]
		}
		if addr == "" {
			continue
		}
		out = append(out, addr+"/32")
	}
	return out
}

// WANIPCollector returns the router's own IP addresses on default-route
// interfaces. Used by Install to populate WAN-IP exclusions in the
// AWGM-TPROXY/AWGM-REDIRECT chains, preventing LAN-to-router-WAN-IP
// traffic from looping back into sing-box.
type WANIPCollector interface {
	Collect(ctx context.Context) ([]string, error)
}

// wanRunner abstracts `ip` command execution for tests.
type wanRunner func(ctx context.Context, args ...string) (string, error)

// wanLogger is the narrow log interface — Warn for skip-with-diagnostics,
// Info reserved for future use.
type wanLogger interface {
	Warn(msg string)
	Info(msg string)
}

type wanIPCollectorImpl struct {
	run wanRunner
	log wanLogger
}

// NewWANIPCollector returns a production collector that shells out to
// /opt/sbin/ip. Tests construct wanIPCollectorImpl directly with a fake.
func NewWANIPCollector(log wanLogger) WANIPCollector {
	return &wanIPCollectorImpl{
		run: func(ctx context.Context, args ...string) (string, error) {
			result, err := sysexec.Run(ctx, "ip", args...)
			if err != nil {
				return "", sysexec.FormatError(result, err)
			}
			return result.Stdout, nil
		},
		log: log,
	}
}

func (c *wanIPCollectorImpl) Collect(ctx context.Context) ([]string, error) {
	routeOut, err := c.run(ctx, "route", "show", "table", "all")
	if err != nil {
		return nil, err
	}
	ifaces := parseDefaultIfaces(routeOut)

	seen := make(map[string]struct{})
	for _, iface := range ifaces {
		out, err := c.run(ctx, "-4", "addr", "show", "dev", iface)
		if err != nil {
			c.log.Warn("router/wanips: ip addr show dev " + iface + ": " + err.Error())
			continue
		}
		addrs := parseInetAddrs(out)
		if len(addrs) == 0 {
			c.log.Warn("router/wanips: default route via " + iface + " but no IPv4 addresses; skipped")
			continue
		}
		for _, a := range addrs {
			seen[a] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for a := range seen {
		result = append(result, a)
	}
	sort.Strings(result)
	return result, nil
}
