package firewall

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	iptablesCmd  = "/opt/sbin/iptables"
	restoreCmd   = "/opt/sbin/iptables-restore"
	maxRetries   = 3
	retryBaseWait = time.Second
)

// ManagerImpl is the iptables firewall manager implementation.
type ManagerImpl struct {
	mssClamp    bool // Add TCP MSS clamping rules (kernel backend)
	ndmsManaged bool // NDMS manages filter/nat rules (OS5 OpkgTun)
	appLog      *logging.ScopedLogger
}

// New creates a new firewall manager.
// mssClamp enables TCP MSS clamping rules in mangle table (for kernel backend).
// ndmsManaged skips filter/nat rules — NDMS manages them for OpkgTun interfaces (OS5).
func New(mssClamp, ndmsManaged bool, appLogger logging.AppLogger) *ManagerImpl {
	return &ManagerImpl{
		mssClamp:    mssClamp,
		ndmsManaged: ndmsManaged,
		appLog:      logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubFirewall),
	}
}

// AddRules adds iptables rules for a tunnel interface.
// When ndmsManaged=true (OS5), only adds MSS clamping — NDMS handles filter/nat.
// Otherwise uses iptables-restore --noflush for atomic rule addition.
func (m *ManagerImpl) AddRules(ctx context.Context, iface string) error {
	// Remove existing rules first to prevent duplicates (idempotent).
	m.RemoveRules(ctx, iface)

	input := m.buildRestoreInput(iface)
	if input == "" {
		return nil
	}

	m.appLog.Full("add-rules", iface, fmt.Sprintf("Adding FORWARD rules for %s", iface))
	m.appLog.Debug("add-rules", iface, fmt.Sprintf("iptables-restore input: ndmsManaged=%v mssClamp=%v", m.ndmsManaged, m.mssClamp))

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * retryBaseWait)
		}
		_, err := exec.RunWithOptions(ctx, restoreCmd, []string{"--noflush"}, exec.Options{
			Stdin: strings.NewReader(input),
		})
		if err == nil {
			return nil
		}
		lastErr = err
	}
	m.appLog.Warn("add-rules", iface, fmt.Sprintf("iptables-restore failed: %v", lastErr))
	return fmt.Errorf("iptables-restore --noflush for %s: %w", iface, lastErr)
}

// RemoveRules removes iptables rules for a tunnel interface.
// When ndmsManaged=true (OS5), only removes MSS clamping — NDMS handles filter/nat.
func (m *ManagerImpl) RemoveRules(ctx context.Context, iface string) error {
	m.appLog.Full("remove-rules", iface, fmt.Sprintf("Removing rules for %s", iface))

	// Remove MSS clamping (ignore errors — may not exist)
	m.removeMSSClamp(ctx, iface)

	if !m.ndmsManaged {
		rules := StandardRules(iface)
		// Remove in reverse order
		for i := len(rules) - 1; i >= 0; i-- {
			if err := m.deleteRule(ctx, rules[i]); err != nil {
				m.appLog.Warn("remove-rules", iface, fmt.Sprintf("Failed to delete rule %s/%s: %v", rules[i].Table, rules[i].Chain, err))
			}
		}
	}

	return nil
}

// HasRules checks if rules exist for an interface.
func (m *ManagerImpl) HasRules(ctx context.Context, iface string) bool {
	args := []string{"-w", "-C", "INPUT", "-i", iface, "-j", "ACCEPT"}
	_, err := exec.Run(ctx, iptablesCmd, args...)
	return err == nil
}

// buildRestoreInput generates iptables-restore format input for all rules.
// Returns empty string when ndmsManaged and no MSS clamp needed.
func (m *ManagerImpl) buildRestoreInput(iface string) string {
	var b strings.Builder

	if !m.ndmsManaged {
		// filter table
		b.WriteString("*filter\n")
		b.WriteString(fmt.Sprintf("-A INPUT -i %s -j ACCEPT\n", iface))
		b.WriteString(fmt.Sprintf("-A OUTPUT -o %s -j ACCEPT\n", iface))
		b.WriteString(fmt.Sprintf("-A FORWARD -i %s -j ACCEPT\n", iface))
		b.WriteString(fmt.Sprintf("-A FORWARD -o %s -j ACCEPT\n", iface))
		b.WriteString("COMMIT\n")

		// nat table
		b.WriteString("*nat\n")
		b.WriteString(fmt.Sprintf("-A POSTROUTING -o %s -j MASQUERADE\n", iface))
		b.WriteString("COMMIT\n")
	}

	// mangle table (MSS clamping, kernel backend only)
	if m.mssClamp {
		b.WriteString("*mangle\n")
		b.WriteString(fmt.Sprintf("-I FORWARD 1 -o %s -p tcp -m tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu\n", iface))
		b.WriteString("COMMIT\n")
	}

	return b.String()
}

// removeMSSClamp removes the TCP MSS clamping rule (ignore errors).
func (m *ManagerImpl) removeMSSClamp(ctx context.Context, iface string) {
	exec.Run(ctx, iptablesCmd,
		"-w", "-t", "mangle", "-D", "FORWARD",
		"-o", iface, "-p", "tcp", "-m", "tcp",
		"--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu")
}

// deleteRule removes a single iptables rule.
func (m *ManagerImpl) deleteRule(ctx context.Context, rule Rule) error {
	args := m.buildRuleArgs("-D", rule)
	result, err := exec.Run(ctx, iptablesCmd, args...)
	if err != nil {
		return fmt.Errorf("iptables -D %s -i/o %s: %w", rule.Chain, rule.Interface, exec.FormatError(result, err))
	}
	return nil
}

// buildRuleArgs builds iptables arguments for a rule.
func (m *ManagerImpl) buildRuleArgs(action string, rule Rule) []string {
	args := []string{"-w"}

	// Table selection (filter is default)
	if rule.Table != "filter" {
		args = append(args, "-t", rule.Table)
	}

	args = append(args, action, rule.Chain)
	args = append(args, rule.Direction, rule.Interface)
	args = append(args, "-j", rule.Target)

	return args
}

// Ensure ManagerImpl implements Manager interface.
var _ Manager = (*ManagerImpl)(nil)
