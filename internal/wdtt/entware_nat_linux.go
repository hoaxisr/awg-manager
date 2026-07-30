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

// entwareNATPresent reports whether awg-manager WDTT iptables rules are still
// installed AND the MASQUERADE still exits through wanDev (empty wanDev →
// current default-route dev): после WAN-failover правило с комментом живо, но
// смотрит в мёртвый интерфейс — считаем его отсутствующим, чтобы reconcile
// переустановил.
func entwareNATPresent(ctx context.Context, wgIface, wanDev string) bool {
	natOut, err1 := iptables.RunOutput(ctx, "-t", "nat", "-S", "POSTROUTING")
	fwdOut, err2 := iptables.RunOutput(ctx, "-S", "FORWARD")
	if err1 != nil || err2 != nil {
		return false
	}
	if !strings.Contains(fwdOut, entwareNATComment) || !strings.Contains(fwdOut, wgIface) {
		return false
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
	flushEntwareMasquerade(ctx)
	if err := iptables.Run(ctx, "-t", "nat", "-I", "POSTROUTING", "1",
		"-s", cidr, "-o", extIface, "-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE"); err != nil {
		return fmt.Errorf("MASQUERADE %s via %s: %w", cidr, extIface, err)
	}
	return nil
}

func removeEntwareNAT(ctx context.Context, wgIface string) {
	flushEntwareMasquerade(ctx)
	for i := 0; i < 5; i++ {
		_ = iptables.Run(ctx, "-D", "FORWARD", "-i", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
		_ = iptables.Run(ctx, "-D", "FORWARD", "-o", wgIface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT")
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
