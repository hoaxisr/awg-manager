//go:build linux

package wdtt

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const entwareNATComment = "AWGM_WDTT"

// entwareNATPresentForServer reports whether all planned NAT/FORWARD rules exist.
func entwareNATPresentForServer(ctx context.Context, cfg ServerConfig, wanDev string) bool {
	plans := cfg.serverEntwareNATPlans()
	if len(plans) == 0 {
		return true
	}
	natOut, err1 := iptables.RunOutput(ctx, "-t", "nat", "-S", "POSTROUTING")
	fwdOut, err2 := iptables.RunOutput(ctx, "-S", "FORWARD")
	if err1 != nil || err2 != nil {
		return false
	}
	if !strings.Contains(fwdOut, entwareNATComment) {
		return false
	}
	for _, iface := range cfg.serverEntwareNATIfaces() {
		if !strings.Contains(fwdOut, iface) {
			return false
		}
	}
	for _, cidr := range cfg.serverEntwarePeerCIDRs() {
		if !strings.Contains(natOut, cidr) {
			return false
		}
	}
	if wanDev == "" {
		dev, err := defaultWANDev(ctx)
		if err != nil {
			return strings.Contains(natOut, entwareNATComment)
		}
		wanDev = dev
	}
	return masqueradeOutDev(natOut) == wanDev
}

// entwareNATPresent kept for tests; checks single iface/CIDR only.
func entwareNATPresent(ctx context.Context, wgIface, wanDev string) bool {
	cfg := ServerConfig{RelayMode: ConnModeWG, WgIface: wgIface}
	if wgIface == DefaultRawServerIface {
		cfg = ServerConfig{RelayMode: ConnModeRaw}
	}
	return entwareNATPresentForServer(ctx, cfg, wanDev)
}

// masqueradeOutDev returns the `-o <dev>` of the AWGM_WDTT MASQUERADE rule.
func masqueradeOutDev(natOut string) string {
	for _, line := range strings.Split(natOut, "\n") {
		if !strings.Contains(line, entwareNATComment) {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "-o" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}

func applyEntwareNATForServer(ctx context.Context, cfg ServerConfig, mode, wanDev string) error {
	plans := cfg.serverEntwareNATPlans()
	if len(plans) == 0 {
		return nil
	}
	if mode == "none" {
		removeEntwareNATForServer(ctx, cfg)
		return nil
	}
	extIface := strings.TrimSpace(wanDev)
	if extIface == "" || mode == "full" {
		var err error
		extIface, err = defaultWANDev(ctx)
		if err != nil {
			return err
		}
	}
	_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644)

	setupEntwareForward(ctx, cfg.serverEntwareNATIfaces()...)
	setupEntwareMSSClamp(ctx, cfg.serverEntwarePeerCIDRs()...)

	flushEntwareMasquerade(ctx)
	for _, cidr := range cfg.serverEntwarePeerCIDRs() {
		if err := iptables.Run(ctx, "-t", "nat", "-I", "POSTROUTING", "1",
			"-s", cidr, "-o", extIface, "-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE"); err != nil {
			return fmt.Errorf("MASQUERADE %s via %s: %w", cidr, extIface, err)
		}
	}
	return nil
}

// applyEntwareNAT installs MASQUERADE + FORWARD for a single iface/CIDR (legacy).
func applyEntwareNAT(ctx context.Context, wgIface, mode, wanDev, peerCIDR string) error {
	cfg := ServerConfig{RelayMode: ConnModeWG, WgIface: wgIface}
	if wgIface == DefaultRawServerIface {
		cfg = ServerConfig{RelayMode: ConnModeRaw}
	}
	if peerCIDR != "" && peerCIDR != cfg.serverPeerCIDR() && peerCIDR != wdttPeerCIDR() {
		// Caller passed explicit CIDR; fall back to single-plan apply.
		if mode == "none" {
			removeEntwareNATIfaces(ctx, wgIface)
			return nil
		}
		extIface := strings.TrimSpace(wanDev)
		if extIface == "" || mode == "full" {
			var err error
			extIface, err = defaultWANDev(ctx)
			if err != nil {
				return err
			}
		}
		_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644)
		setupEntwareForward(ctx, wgIface)
		setupEntwareMSSClamp(ctx, peerCIDR)
		flushEntwareMasquerade(ctx)
		if err := iptables.Run(ctx, "-t", "nat", "-I", "POSTROUTING", "1",
			"-s", peerCIDR, "-o", extIface, "-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE"); err != nil {
			return fmt.Errorf("MASQUERADE %s via %s: %w", peerCIDR, extIface, err)
		}
		return nil
	}
	return applyEntwareNATForServer(ctx, cfg, mode, wanDev)
}

func removeEntwareNATForServer(ctx context.Context, cfg ServerConfig) {
	removeEntwareNATIfaces(ctx, cfg.serverEntwareNATIfaces()...)
}

func removeEntwareNAT(ctx context.Context, wgIface string) {
	removeEntwareNATIfaces(ctx, wgIface)
}

func removeEntwareNATIfaces(ctx context.Context, ifaces ...string) {
	flushEntwareMasquerade(ctx)
	removeEntwareMSSClamp(ctx)
	for _, iface := range ifaces {
		iface = strings.TrimSpace(iface)
		if iface == "" {
			continue
		}
		for i := 0; i < 5; i++ {
			_ = iptables.Run(ctx, "-D", "FORWARD", "-i", iface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
			_ = iptables.Run(ctx, "-D", "FORWARD", "-o", iface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
		}
	}
}

// flushEntwareMasquerade удаляет все AWGM_WDTT MASQUERADE-правила реплеем
// вывода -S: iptables -D требует точного совпадения спеки, а -o меняется при
// смене WAN — удаление по фиксированной спеке молча промахивается.
func flushEntwareMasquerade(ctx context.Context) {
	out, err := iptables.RunOutput(ctx, "-t", "nat", "-S", "POSTROUTING")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, entwareNATComment) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" {
			continue
		}
		_ = iptables.Run(ctx, append([]string{"-t", "nat", "-D"}, fields[1:]...)...)
	}
}

func setupEntwareForward(ctx context.Context, ifaces ...string) {
	for _, wgIface := range ifaces {
		wgIface = strings.TrimSpace(wgIface)
		if wgIface == "" {
			continue
		}
		for i := 0; i < 5; i++ {
			_ = iptables.Run(ctx, "-D", "FORWARD", "-i", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
			_ = iptables.Run(ctx, "-D", "FORWARD", "-o", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
		}
		_ = iptables.Run(ctx, "-I", "FORWARD", "1", "-i", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
		_ = iptables.Run(ctx, "-I", "FORWARD", "1", "-o", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
	}
}

const entwareMSSChain = "awgm_wdtt_mangle"

func entwareMSSPresentAll(ctx context.Context, peerCIDRs []string) bool {
	if len(peerCIDRs) == 0 {
		return true
	}
	out, err := iptables.RunOutput(ctx, "-t", "mangle", "-S", entwareMSSChain)
	if err != nil {
		return false
	}
	if !strings.Contains(out, "TCPMSS") {
		return false
	}
	for _, cidr := range peerCIDRs {
		if cidr != "" && !strings.Contains(out, cidr) {
			return false
		}
	}
	return true
}

// entwareMSSPresent reports whether TCPMSS clamp for peerCIDR is installed.
func entwareMSSPresent(ctx context.Context, peerCIDR string) bool {
	if peerCIDR == "" {
		return true
	}
	return entwareMSSPresentAll(ctx, []string{peerCIDR})
}

func setupEntwareMSSClamp(ctx context.Context, peerCIDRs ...string) {
	var cidrs []string
	for _, c := range peerCIDRs {
		c = strings.TrimSpace(c)
		if c != "" {
			cidrs = append(cidrs, c)
		}
	}
	if len(cidrs) == 0 {
		return
	}
	_ = iptables.Run(ctx, "-t", "mangle", "-N", entwareMSSChain)
	_ = iptables.Run(ctx, "-t", "mangle", "-F", entwareMSSChain)
	for _, peerCIDR := range cidrs {
		for _, spec := range []string{"-s", "-d"} {
			// Без -m comment: на Keenetic xt_comment часто не загружен (#666),
			// правило молча не ставится — ручной fix пользователей идёт без comment.
			_ = iptables.Run(ctx, "-t", "mangle", "-A", entwareMSSChain,
				spec, peerCIDR, "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN",
				"-j", "TCPMSS", "--clamp-mss-to-pmtu")
		}
	}
	for i := 0; i < 3; i++ {
		_ = iptables.Run(ctx, "-t", "mangle", "-D", "FORWARD", "-j", entwareMSSChain)
	}
	_ = iptables.Run(ctx, "-t", "mangle", "-I", "FORWARD", "1", "-j", entwareMSSChain)
}

func removeEntwareMSSClamp(ctx context.Context) {
	for i := 0; i < 3; i++ {
		_ = iptables.Run(ctx, "-t", "mangle", "-D", "FORWARD", "-j", entwareMSSChain)
	}
	_ = iptables.Run(ctx, "-t", "mangle", "-F", entwareMSSChain)
	_ = iptables.Run(ctx, "-t", "mangle", "-X", entwareMSSChain)
}

func defaultWANDev(ctx context.Context) (string, error) {
	res, err := exec.Run(ctx, "/opt/sbin/ip", "route", "show", "default")
	if err != nil {
		return "", fmt.Errorf("default route: %w", err)
	}
	fields := strings.Fields(strings.TrimSpace(res.Stdout))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("default route: no dev in %q", strings.TrimSpace(res.Stdout))
}
