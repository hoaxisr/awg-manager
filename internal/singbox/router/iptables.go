package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
	sysiptables "github.com/hoaxisr/awg-manager/internal/sys/iptables"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

const (
	TPROXYPort   = 51271
	Fwmark       = 0x1
	RoutingTable = 100
	ChainName    = "AWGM-TPROXY"

	netfilterHookPath  = "/opt/etc/ndm/netfilter.d/50-awgm-tproxy.sh"
	netfilterRulesPath = "/opt/etc/awg-manager/singbox/router-netfilter.rules"
)

func kernelModuleName() string { return "xt_TPROXY" }

func buildTProxyModulePath(kernelVersion string) string {
	return filepath.Join("/lib/modules", kernelVersion, "xt_TPROXY.ko")
}

func isModuleLoaded(name string) bool {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return false
	}
	prefix := name + " "
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

var (
	netfilterOnce      sync.Once
	netfilterAvailable bool
)

func IsNetfilterAvailable() bool {
	netfilterOnce.Do(func() {
		if isModuleLoaded(kernelModuleName()) {
			netfilterAvailable = true
			return
		}
		kernel := osdetect.KernelRelease()
		if kernel == "" {
			return
		}
		_, err := os.Stat(buildTProxyModulePath(kernel))
		netfilterAvailable = err == nil
	})
	return netfilterAvailable
}

func EnsureTProxyModule(ctx context.Context) error {
	if isModuleLoaded(kernelModuleName()) {
		return nil
	}
	kernel := osdetect.KernelRelease()
	if kernel == "" {
		return ErrNetfilterComponentMissing
	}
	path := buildTProxyModulePath(kernel)
	if _, err := os.Stat(path); err != nil {
		return ErrNetfilterComponentMissing
	}
	if _, err := sysexec.Run(ctx, "insmod", path); err != nil {
		return fmt.Errorf("insmod %s: %w", path, err)
	}
	return nil
}

var (
	tproxyTargetOnce   sync.Once
	tproxyTargetResult bool
)

func IsTProxyTargetAvailable(ctx context.Context) bool {
	tproxyTargetOnce.Do(func() {
		res, err := sysexec.Run(ctx, sysiptables.Binary, "-j", "TPROXY", "--help")
		if err != nil || res == nil {
			return
		}
		tproxyTargetResult = strings.Contains(res.Stdout+res.Stderr, "TPROXY")
	})
	return tproxyTargetResult
}

type RestoreInputSpec struct {
	// PolicyMark is the NDMS-assigned mark (hex, e.g. "0xffffaaa") that
	// NDMS sets on connections from devices bound to the chosen access
	// policy. Empty means no PREROUTING jump (defensive — caller should
	// never reach Install with empty mark, but iptables doesn't panic).
	PolicyMark string
}

var bypassCIDRs = []string{
	"127.0.0.0/8",
	"169.254.0.0/16",
	"224.0.0.0/4",
	"255.255.255.255/32",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

func buildRestoreInput(spec RestoreInputSpec) string {
	var b strings.Builder
	b.WriteString("*mangle\n")
	fmt.Fprintf(&b, ":%s - [0:0]\n", ChainName)

	for _, cidr := range bypassCIDRs {
		fmt.Fprintf(&b, "-A %s -d %s -j RETURN\n", ChainName, cidr)
	}
	fmt.Fprintf(&b, "-A %s -p tcp --dport 79 -j RETURN\n", ChainName)

	fmt.Fprintf(&b, "-A %s -m mark --mark 0xff -j RETURN\n", ChainName)

	fmt.Fprintf(&b, "-A %s -p tcp -j TPROXY --on-port %d --on-ip 127.0.0.1 --tproxy-mark 0x%x\n",
		ChainName, TPROXYPort, Fwmark)
	fmt.Fprintf(&b, "-A %s -p udp -j TPROXY --on-port %d --on-ip 127.0.0.1 --tproxy-mark 0x%x\n",
		ChainName, TPROXYPort, Fwmark)

	if spec.PolicyMark != "" {
		fmt.Fprintf(&b, "-I PREROUTING 1 -m connmark --mark %s -j %s\n", spec.PolicyMark, ChainName)
	}

	b.WriteString("COMMIT\n")
	return b.String()
}

type restoreNoflushFn func(ctx context.Context, input string) error
type runFn func(ctx context.Context, args ...string) error

type persistFn func(input string) error

type IPTables struct {
	restoreNoflush restoreNoflushFn
	runIPTables    runFn
	runIP          runFn
	persistRules   persistFn
	persistHook    func() error
	cleanupHook    func()
}

func NewIPTables() *IPTables {
	return &IPTables{
		restoreNoflush: sysiptables.RestoreNoflush,
		runIPTables:    sysiptables.Run,
		runIP: func(ctx context.Context, args ...string) error {
			_, err := sysexec.Run(ctx, "ip", args...)
			return err
		},
		persistRules: writeNetfilterRulesFile,
		persistHook:  writeNetfilterHook,
		cleanupHook:  removeNetfilterHook,
	}
}

func (it *IPTables) Install(ctx context.Context, mark string) error {
	// Scrub any existing PREROUTING jumps to AWGM-TPROXY before inserting
	// the new one. iptables-restore --noflush + -I PREROUTING 1 would
	// otherwise stack a duplicate jump on every restart / mark-change /
	// re-Enable: the stale rule from the previous policy/mark survives
	// because mangle isn't flushed, and the new rule lands in front of it.
	// Idempotent: a no-op when no prior jumps exist.
	it.removeSourceHooks(ctx)

	input := buildRestoreInput(RestoreInputSpec{PolicyMark: mark})
	if it.persistRules != nil {
		if err := it.persistRules(input); err != nil {
			return fmt.Errorf("write netfilter rules: %w", err)
		}
	}
	if it.persistHook != nil {
		if err := it.persistHook(); err != nil {
			return fmt.Errorf("write netfilter hook: %w", err)
		}
	}
	if err := it.restoreNoflush(ctx, input); err != nil {
		return fmt.Errorf("iptables-restore: %w", err)
	}
	if err := it.runIP(ctx, "rule", "add", "fwmark", fmt.Sprintf("0x%x", Fwmark),
		"table", fmt.Sprintf("%d", RoutingTable)); err != nil {
		if !strings.Contains(err.Error(), "File exists") {
			return fmt.Errorf("ip rule add: %w", err)
		}
	}
	if err := it.runIP(ctx, "route", "add", "local", "0.0.0.0/0", "dev", "lo",
		"table", fmt.Sprintf("%d", RoutingTable)); err != nil {
		if !strings.Contains(err.Error(), "File exists") {
			return fmt.Errorf("ip route add: %w", err)
		}
	}
	return nil
}

func writeNetfilterRulesFile(input string) error {
	if err := os.MkdirAll(filepath.Dir(netfilterRulesPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(netfilterRulesPath, []byte(input), 0644)
}

func writeNetfilterHook() error {
	if err := os.MkdirAll(filepath.Dir(netfilterHookPath), 0755); err != nil {
		return err
	}
	script := fmt.Sprintf(`#!/bin/sh
[ "$type" = "ip6tables" ] && exit 0
[ "$table" = "mangle" ] || exit 0
[ -f %[1]q ] || exit 0
if ! /opt/sbin/iptables -w -t mangle -nL %[2]s >/dev/null 2>&1; then
  /opt/sbin/iptables-restore --noflush < %[1]q
  /opt/sbin/ip rule add fwmark 0x%[3]x table %[4]d 2>/dev/null || true
  /opt/sbin/ip route add local 0.0.0.0/0 dev lo table %[4]d 2>/dev/null || true
  logger -t awgm-tproxy "netfilter.d: restored AWGM-TPROXY chain after NDMS reload"
fi
`, netfilterRulesPath, ChainName, Fwmark, RoutingTable)
	return os.WriteFile(netfilterHookPath, []byte(script), 0755)
}

func removeNetfilterHook() {
	_ = os.Remove(netfilterHookPath)
	_ = os.Remove(netfilterRulesPath)
}

func (it *IPTables) Uninstall(ctx context.Context) error {
	if it.cleanupHook != nil {
		it.cleanupHook()
	}
	const maxUninstallPasses = 32
	for i := 0; i < maxUninstallPasses; i++ {
		if err := it.runIPTables(ctx, "-t", "mangle", "-D", "PREROUTING", "-j", ChainName); err != nil {
			break
		}
	}
	it.removeSourceHooks(ctx)
	_ = it.runIPTables(ctx, "-t", "mangle", "-F", ChainName)
	_ = it.runIPTables(ctx, "-t", "mangle", "-X", ChainName)
	_ = it.runIP(ctx, "rule", "del", "fwmark", fmt.Sprintf("0x%x", Fwmark),
		"table", fmt.Sprintf("%d", RoutingTable))
	_ = it.runIP(ctx, "route", "flush", "table", fmt.Sprintf("%d", RoutingTable))
	return nil
}

func (it *IPTables) removeSourceHooks(ctx context.Context) {
	result, err := sysexec.Run(ctx, sysiptables.Binary, "-w", "-t", "mangle", "-S", "PREROUTING")
	if err != nil || result == nil {
		return
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if !strings.Contains(line, "-j "+ChainName) {
			continue
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-A PREROUTING") {
			continue
		}
		deleteLine := strings.Replace(line, "-A PREROUTING", "-D PREROUTING", 1)
		args := append([]string{"-t", "mangle"}, strings.Fields(deleteLine)...)
		_ = it.runIPTables(ctx, args...)
	}
}

func (it *IPTables) IsInstalled(ctx context.Context) bool {
	return it.runIPTables(ctx, "-t", "mangle", "-nL", ChainName) == nil
}

