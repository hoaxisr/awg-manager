//go:build linux

package wdtt

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

func applyRawServerPolicyMark(ctx context.Context, mark string) error {
	mark = strings.TrimSpace(mark)
	if mark == "" {
		removeRawServerPolicyMark(ctx)
		return nil
	}
	iface := DefaultRawServerIface
	if rawServerMarkRulePresent(ctx, iface, mark) && rawServerConnmarkRulePresent(ctx, iface) {
		return nil
	}
	// Итоговый порядок в цепочке: MARK (поз.1), CONNMARK (поз.2) — иначе
	// save-mark пишет ноль (PR #697, F3). Вставка обратная (оба -I 1):
	// CONNMARK первым, MARK вторым. При любой частичной пропаже — пересборка
	// обоих: одиночная довставка инвертирует порядок.
	removeRawServerPolicyMarkRules(ctx, iface)
	if err := ensureRawServerPolicyConnmarkRule(ctx, iface); err != nil {
		return err
	}
	if err := ensureRawServerPolicyMarkRule(ctx, iface, mark); err != nil {
		removeRawServerPolicyMark(ctx)
		return err
	}
	return nil
}

func ensureRawServerPolicyMarkRule(ctx context.Context, iface, mark string) error {
	if rawServerMarkRulePresent(ctx, iface, mark) {
		return nil
	}
	// Без -m comment намеренно: правило адресовано НАШЕМУ интерфейсу
	// (wdttraw0) — имя интерфейса и есть признак владения, как у FORWARD accept
	// и цепочки awgm_wdtt_mangle. Снос ниже реплеит -S и удаляет по напечатанной
	// спеке, поэтому обе формы (с меткой и без) он снимает одинаково.
	if err := iptables.Run(ctx, "-t", "mangle", "-I", "PREROUTING", "1",
		"-i", iface, "-j", "MARK", "--set-xmark", mark+"/0xffffffff"); err != nil {
		return fmt.Errorf("MARK %s on %s: %w", mark, iface, err)
	}
	return nil
}

func ensureRawServerPolicyConnmarkRule(ctx context.Context, iface string) error {
	if rawServerConnmarkRulePresent(ctx, iface) {
		return nil
	}
	if err := iptables.Run(ctx, "-t", "mangle", "-I", "PREROUTING", "1",
		"-i", iface, "-j", "CONNMARK",
		"--save-mark", "--nfmask", "0xffffffff", "--ctmask", "0xffffffff"); err != nil {
		return fmt.Errorf("CONNMARK on %s: %w", iface, err)
	}
	return nil
}

func rawServerMarkRulePresent(ctx context.Context, iface, mark string) bool {
	return iptables.Run(ctx, "-t", "mangle", "-C", "PREROUTING",
		"-i", iface, "-j", "MARK", "--set-xmark", mark+"/0xffffffff") == nil
}

func rawServerConnmarkRulePresent(ctx context.Context, iface string) bool {
	return iptables.Run(ctx, "-t", "mangle", "-C", "PREROUTING",
		"-i", iface, "-j", "CONNMARK",
		"--save-mark", "--nfmask", "0xffffffff", "--ctmask", "0xffffffff") == nil
}

func removeRawServerPolicyMark(ctx context.Context) {
	removeRawServerPolicyMarkRules(ctx, DefaultRawServerIface)
}

func removeRawServerPolicyMarkRules(ctx context.Context, iface string) {
	for pass := 0; pass < 8; pass++ {
		out, err := iptables.RunOutput(ctx, "-t", "mangle", "-S", "PREROUTING")
		if err != nil {
			return
		}
		var deleted bool
		for _, line := range strings.Split(out, "\n") {
			if !strings.Contains(line, "-i "+iface) {
				continue
			}
			if !strings.Contains(line, "-j MARK") && !strings.Contains(line, "-j CONNMARK") &&
				!strings.Contains(line, "AWGM-WDTT-POLICY") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[0] != "-A" {
				continue
			}
			if iptables.Run(ctx, append([]string{"-t", "mangle", "-D"}, fields[1:]...)...) == nil {
				deleted = true
				break
			}
		}
		if !deleted {
			return
		}
	}
}

func rawServerPolicyMarkPresent(ctx context.Context, mark string) bool {
	mark = strings.TrimSpace(mark)
	iface := DefaultRawServerIface
	if mark == "" {
		out, err := iptables.RunOutput(ctx, "-t", "mangle", "-S", "PREROUTING")
		if err != nil {
			return true
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "-i "+iface) &&
				(strings.Contains(line, "-j MARK") || strings.Contains(line, "-j CONNMARK")) {
				return false
			}
		}
		return true
	}
	return rawServerMarkRulePresent(ctx, iface, mark) &&
		rawServerConnmarkRulePresent(ctx, iface)
}
