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

// entwareNATPresent reports whether awg-manager WDTT iptables rules are still installed.
func entwareNATPresent(ctx context.Context, wgIface string) bool {
	natOut, err1 := iptables.RunOutput(ctx, "-t", "nat", "-S", "POSTROUTING")
	fwdOut, err2 := iptables.RunOutput(ctx, "-S", "FORWARD")
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.Contains(natOut, entwareNATComment) &&
		strings.Contains(fwdOut, entwareNATComment) &&
		strings.Contains(fwdOut, wgIface)
}

// applyEntwareNAT installs MASQUERADE + FORWARD for Entware-created wdtt0.
// Keenetic NDMS does not know wireguard-go interfaces, so RCI ip nat is a no-op.
func applyEntwareNAT(ctx context.Context, wgIface, mode, wanDev string) error {
	if mode == "none" {
		removeEntwareNAT(ctx, wgIface)
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

	cidr := DefaultWdttAddress + "/24"
	for i := 0; i < 5; i++ {
		_ = iptables.Run(ctx, "-t", "nat", "-D", "POSTROUTING",
			"-s", cidr, "-o", extIface, "-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE")
	}
	if err := iptables.Run(ctx, "-t", "nat", "-I", "POSTROUTING", "1",
		"-s", cidr, "-o", extIface, "-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE"); err != nil {
		return fmt.Errorf("MASQUERADE %s via %s: %w", cidr, extIface, err)
	}
	return nil
}

func removeEntwareNAT(ctx context.Context, wgIface string) {
	cidr := DefaultWdttAddress + "/24"
	for i := 0; i < 5; i++ {
		_ = iptables.Run(ctx, "-t", "nat", "-D", "POSTROUTING",
			"-s", cidr, "-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE")
		_ = iptables.Run(ctx, "-D", "FORWARD", "-i", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
		_ = iptables.Run(ctx, "-D", "FORWARD", "-o", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
	}
}

func setupEntwareForward(ctx context.Context, wgIface string) {
	for i := 0; i < 5; i++ {
		_ = iptables.Run(ctx, "-D", "FORWARD", "-i", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
		_ = iptables.Run(ctx, "-D", "FORWARD", "-o", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
	}
	_ = iptables.Run(ctx, "-I", "FORWARD", "1", "-i", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
	_ = iptables.Run(ctx, "-I", "FORWARD", "1", "-o", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
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
