//go:build linux

package wdtt

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const rawServerPolicyComment = "AWGM-WDTT-POLICY"

func applyRawServerPolicyMark(ctx context.Context, mark string) error {
	mark = strings.TrimSpace(mark)
	if mark == "" {
		removeRawServerPolicyMark(ctx)
		return nil
	}
	removeRawServerPolicyMark(ctx)
	iface := DefaultRawServerIface
	if err := iptables.Run(ctx, "-t", "mangle", "-A", "PREROUTING",
		"-i", iface, "-m", "comment", "--comment", rawServerPolicyComment,
		"-j", "MARK", "--set-xmark", mark+"/0xffffffff"); err != nil {
		return fmt.Errorf("MARK %s on %s: %w", mark, iface, err)
	}
	if err := iptables.Run(ctx, "-t", "mangle", "-A", "PREROUTING",
		"-i", iface, "-m", "comment", "--comment", rawServerPolicyComment,
		"-j", "CONNMARK", "--save-mark", "--nfmask", "0xffffffff", "--ctmask", "0xffffffff"); err != nil {
		removeRawServerPolicyMark(ctx)
		return fmt.Errorf("CONNMARK on %s: %w", iface, err)
	}
	return nil
}

func removeRawServerPolicyMark(ctx context.Context) {
	out, err := iptables.RunOutput(ctx, "-t", "mangle", "-S", "PREROUTING")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, rawServerPolicyComment) {
			continue
		}
		if !strings.Contains(line, "-i "+DefaultRawServerIface) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" {
			continue
		}
		_ = iptables.Run(ctx, append([]string{"-t", "mangle", "-D"}, fields[1:]...)...)
	}
}

func rawServerPolicyMarkPresent(ctx context.Context, mark string) bool {
	out, err := iptables.RunOutput(ctx, "-t", "mangle", "-S", "PREROUTING")
	if err != nil {
		return false
	}
	mark = strings.TrimSpace(mark)
	hasMark := mark == ""
	hasConn := mark == ""
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, rawServerPolicyComment) || !strings.Contains(line, "-i "+DefaultRawServerIface) {
			continue
		}
		if strings.Contains(line, "-j MARK") {
			if mark == "" || strings.Contains(line, mark) {
				hasMark = true
			}
		}
		if strings.Contains(line, "-j CONNMARK") {
			hasConn = true
		}
	}
	if mark == "" {
		return !hasMark && !hasConn
	}
	return hasMark && hasConn
}
