//go:build linux

package wdtt

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const entwareLANComment = "AWGM_WDTT_LAN"

func applyEntwareLAN(ctx context.Context, _ string, segments []string, resolver AccessManager, peerCIDRs ...string) error {
	removeEntwareLAN(ctx, "")
	if len(segments) == 0 || resolver == nil {
		return nil
	}
	cidrs, err := resolver.ResolveLANSegmentCIDRs(ctx, segments)
	if err != nil {
		return err
	}
	if len(peerCIDRs) == 0 {
		peerCIDRs = []string{wdttPeerCIDR()}
	}
	for _, peerCIDR := range peerCIDRs {
		peerCIDR = strings.TrimSpace(peerCIDR)
		if peerCIDR == "" {
			continue
		}
		for _, lanCIDR := range cidrs {
			lanCIDR = strings.TrimSpace(lanCIDR)
			if lanCIDR == "" {
				continue
			}
			if err := iptables.Run(ctx, "-I", "FORWARD", "1",
				"-s", peerCIDR, "-d", lanCIDR,
				"-m", "comment", "--comment", entwareLANComment, "-j", "ACCEPT"); err != nil {
				return fmt.Errorf("LAN forward wdtt→%s: %w", lanCIDR, err)
			}
			if err := iptables.Run(ctx, "-I", "FORWARD", "1",
				"-s", lanCIDR, "-d", peerCIDR,
				"-m", "comment", "--comment", entwareLANComment, "-j", "ACCEPT"); err != nil {
				return fmt.Errorf("LAN forward %s→wdtt: %w", lanCIDR, err)
			}
		}
	}
	return nil
}

func removeEntwareLAN(ctx context.Context, _ string) {
	out, err := iptables.RunOutput(ctx, "-S", "FORWARD")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, entwareLANComment) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" {
			continue
		}
		_ = iptables.Run(ctx, append([]string{"-D"}, fields[1:]...)...)
	}
}

func entwareLANPresent(ctx context.Context, peerCIDRs []string, lanCIDRs []string) bool {
	if len(lanCIDRs) == 0 {
		return true
	}
	if len(peerCIDRs) == 0 {
		peerCIDRs = []string{wdttPeerCIDR()}
	}
	out, err := iptables.RunOutput(ctx, "-S", "FORWARD")
	if err != nil {
		return false
	}
	want := 2 * len(lanCIDRs) * len(peerCIDRs)
	got := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, entwareLANComment) {
			continue
		}
		for _, peer := range peerCIDRs {
			if !strings.Contains(line, peer) {
				continue
			}
			for _, lan := range lanCIDRs {
				if strings.Contains(line, lan) {
					got++
					break
				}
			}
		}
	}
	return got >= want
}
