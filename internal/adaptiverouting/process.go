package adaptiverouting

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

type ProcessManager struct {
	configDir  string
	binaryPath string
	mu         sync.Mutex
	cmd        *exec.Cmd
}

func NewProcessManager(configDir string, binaryPath string) *ProcessManager {
	return &ProcessManager{
		configDir:  configDir,
		binaryPath: binaryPath,
	}
}

func (p *ProcessManager) ConfigPath() string {
	return filepath.Join(p.configDir, "susanin.conf")
}

func (p *ProcessManager) PidPath() string {
	return filepath.Join(p.configDir, "susanin.pid")
}

func (p *ProcessManager) AlwaysPath() string {
	return filepath.Join(p.configDir, "vpn_always.txt")
}

func (p *ProcessManager) NeverPath() string {
	return filepath.Join(p.configDir, "vpn_never.txt")
}

// GenerateConfig renders susanin.conf from settings and active egress.
func (p *ProcessManager) GenerateConfigFile(
	settings Settings,
	egressDev string,
	lanInterfaces []string,
	lanSubnets []string,
	policyMark string,
) ([]byte, error) {
	if egressDev == "" {
		egressDev = "awgsus0"
	}
	if len(lanInterfaces) == 0 {
		lanInterfaces = []string{"br0"}
	}
	if len(lanSubnets) == 0 {
		lanSubnets = []string{"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12"}
	}

	table := settings.RoutingTableID
	if table <= 0 {
		table = DefaultTableID
	}
	markTest := settings.FwmarkTest
	if markTest == "" {
		markTest = DefaultFwmarkTest
	}
	markOk := settings.FwmarkOk
	if markOk == "" {
		markOk = DefaultFwmarkOk
	}
	markMask := settings.FwmarkMask
	if markMask == "" {
		markMask = DefaultFwmarkMask
	}

	fastSec := settings.Detection.FastIntervalSeconds
	if fastSec <= 0 {
		fastSec = 1
	}
	softSec := settings.Detection.SoftIntervalSeconds
	if softSec <= 0 {
		softSec = 1
	}
	judgeSec := settings.Detection.JudgeIntervalSeconds
	if judgeSec <= 0 {
		judgeSec = 1
	}
	healthSec := settings.Detection.HealthIntervalSeconds
	if healthSec <= 0 {
		healthSec = 5
	}
	synRetries := settings.Detection.TcpSynRetries
	if synRetries <= 0 {
		synRetries = 2
	}
	maxEntries := settings.Persistence.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 4096
	}

	var sb strings.Builder
	sb.WriteString("# Managed by AWG Manager - DO NOT EDIT MANUALLY\n")
	sb.WriteString(fmt.Sprintf("egress_interface=%s\n", egressDev))
	if egressDev == "awgsus0" {
		sb.WriteString("egress_address=198.18.0.1\n")
	} else if iface, err := net.InterfaceByName(egressDev); err == nil {
		if addrs, err := iface.Addrs(); err == nil {
			for _, addr := range addrs {
				if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
					sb.WriteString(fmt.Sprintf("egress_address=%s\n", ipNet.IP.String()))
					break
				}
			}
		}
	}
	sb.WriteString(fmt.Sprintf("lan_interfaces=%s\n", strings.Join(lanInterfaces, ",")))
	sb.WriteString(fmt.Sprintf("lan_subnets=%s\n", strings.Join(lanSubnets, ",")))
	sb.WriteString(fmt.Sprintf("routing_table=%d\n", table))
	sb.WriteString(fmt.Sprintf("mark_test=%s\n", markTest))
	sb.WriteString(fmt.Sprintf("mark_ok=%s\n", markOk))
	sb.WriteString(fmt.Sprintf("mark_mask=%s\n", markMask))
	sb.WriteString(fmt.Sprintf("ip_rule_priority_start=%d\n", settings.RulePriorityOk))
	if strings.TrimSpace(policyMark) != "" {
		sb.WriteString(fmt.Sprintf("policy_mark=%s\n", strings.TrimSpace(policyMark)))
	}
	sb.WriteString(fmt.Sprintf("fast_interval=%ds\n", fastSec))
	sb.WriteString(fmt.Sprintf("soft_interval=%ds\n", softSec))
	sb.WriteString(fmt.Sprintf("judge_interval=%ds\n", judgeSec))
	sb.WriteString(fmt.Sprintf("health_interval=%ds\n", healthSec))
	sb.WriteString(fmt.Sprintf("fast_syn_min_op=%d\n", synRetries))
	sb.WriteString(fmt.Sprintf("ok_ttl=%d\n", settings.Persistence.OkTTLSeconds))
	sb.WriteString(fmt.Sprintf("ok_max_entries=%d\n", maxEntries))
	sb.WriteString("ok_evict_misses=3\n")
	sb.WriteString("test_ttl=1m\n")
	sb.WriteString("cooldown_ttl=5m\n")
	sb.WriteString("watch_ttl=2\n")
	sb.WriteString("watch_retry_below=1\n")
	sb.WriteString("promo_per_min=120\n")
	lateBytes := settings.Detection.LateStallBytes
	if lateBytes <= 0 {
		lateBytes = 16384
	}
	sb.WriteString(fmt.Sprintf("late_stall_bytes=%d\n", lateBytes))
	sb.WriteString("health_probe=1.1.1.1,8.8.8.8\n")
	sb.WriteString(fmt.Sprintf("vpn_always_file=%s\n", p.AlwaysPath()))
	sb.WriteString(fmt.Sprintf("vpn_never_file=%s\n", p.NeverPath()))
	if settings.DNS.Enabled && len(settings.DNS.Servers) > 0 {
		// Use the last configured DNS server for vpn_always_dns resolution.
		// This allows users to place the most reliable resolver last in the list.
		// Fallback to first if only one is present.
		alwaysDNS := ""
		for _, s := range settings.DNS.Servers {
			s = strings.TrimSpace(s)
			if s != "" {
				alwaysDNS = s
			}
		}
		if alwaysDNS != "" {
			sb.WriteString(fmt.Sprintf("vpn_always_dns=%s\n", alwaysDNS))
		}
	}
	sb.WriteString("log_level=info\n")
	sb.WriteString("disk_mode=soft\n")
	sb.WriteString("soft_state_interval=12h\n")

	return []byte(sb.String()), nil
}

// WriteConfigFiles updates susanin.conf, vpn_always.txt and vpn_never.txt.
func (p *ProcessManager) WriteConfigFiles(
	settings Settings,
	egressDev string,
	lanInterfaces []string,
	lanSubnets []string,
	policyMark string,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := os.MkdirAll(p.configDir, 0755); err != nil {
		return fmt.Errorf("mkdir config dir %s: %w", p.configDir, err)
	}

	confBytes, err := p.GenerateConfigFile(settings, egressDev, lanInterfaces, lanSubnets, policyMark)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p.ConfigPath(), confBytes, 0644); err != nil {
		return fmt.Errorf("write %s: %w", p.ConfigPath(), err)
	}

	// Ensure tools/datapath.sh is up to date with EnhancedDatapathScript
	for _, td := range []string{filepath.Join(filepath.Dir(p.configDir), "tools"), ManagedSusaninToolsDir} {
		if fi, err := os.Stat(td); err == nil && fi.IsDir() {
			target := filepath.Join(td, "datapath.sh")
			tmpFile := target + ".tmp"
			if err := os.WriteFile(tmpFile, []byte(EnhancedDatapathScript), 0755); err == nil {
				_ = os.Rename(tmpFile, target)
			} else {
				_ = os.WriteFile(target, []byte(EnhancedDatapathScript), 0755)
			}
		}
	}


	// Write always entries
	var alwaysContent strings.Builder
	for _, entry := range settings.AlwaysEntries {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			alwaysContent.WriteString(entry + "\n")
		}
	}
	if err := os.WriteFile(p.AlwaysPath(), []byte(alwaysContent.String()), 0644); err != nil {
		return fmt.Errorf("write %s: %w", p.AlwaysPath(), err)
	}

	// Mirror to /opt/susanin/etc if needed
	if filepath.Clean(p.configDir) != filepath.Clean(ManagedSusaninEtcDir) {
		if fi, err := os.Stat(ManagedSusaninEtcDir); err == nil && fi.IsDir() {
			_ = os.WriteFile(filepath.Join(ManagedSusaninEtcDir, "susanin.conf"), confBytes, 0644)
			_ = os.WriteFile(filepath.Join(ManagedSusaninEtcDir, "vpn_always.txt"), []byte(alwaysContent.String()), 0644)
		}
	}

	// Write never entries
	var neverContent strings.Builder
	for _, entry := range settings.NeverEntries {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			neverContent.WriteString(entry + "\n")
		}
	}
	if err := os.WriteFile(p.NeverPath(), []byte(neverContent.String()), 0644); err != nil {
		return fmt.Errorf("write %s: %w", p.NeverPath(), err)
	}

	return nil
}

// Start launches susanin-agent in the background.
func (p *ProcessManager) Start(ctx context.Context, binPath string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if binPath != "" {
		p.binaryPath = binPath
	}
	if p.binaryPath == "" {
		return fmt.Errorf("susanin-agent binary path not configured")
	}

	// Always make sure any stale/orphan daemon is terminated before launching a new one
	for _, pid := range p.managedPIDsLocked() {
		if p.cmd != nil && p.cmd.Process != nil && p.cmd.Process.Pid == pid {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil && proc != nil {
			_ = proc.Signal(syscall.SIGTERM)
			time.Sleep(100 * time.Millisecond)
			if err := proc.Signal(syscall.Signal(0)); err == nil {
				_ = proc.Kill()
			}
		}
	}

	if running, pid := p.isRunningLocked(); running {
		_ = pid
		return nil // already running
	}

	cmd := exec.Command(p.binaryPath, "run")
	cmd.Dir = p.configDir
	logPath := filepath.Join(ManagedSusaninVarDir, "agent.log")
	if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	cmd.Env = append(os.Environ(), "SUSANIN_CONF="+p.ConfigPath(), "SUSANIN_LOG="+logPath)
	// Detach process group
	childproc.SetProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start susanin-agent daemon: %w", err)
	}

	p.cmd = cmd
	// Write PID file
	if cmd.Process != nil && cmd.Process.Pid > 0 {
		_ = os.WriteFile(p.PidPath(), []byte(strconv.Itoa(cmd.Process.Pid)), 0644)
	}

	// Reap the child when it exits. Without Wait the process may remain a
	// zombie, Signal(0) keeps succeeding, and the stale PID file makes the UI
	// report Susanin as running after a crash.
	go p.wait(cmd)

	return nil
}

func (p *ProcessManager) managedPIDsLocked() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		parts := bytes.Split(bytes.TrimRight(cmdline, "\x00"), []byte{0})
		if len(parts) == 0 {
			continue
		}
		binBase := filepath.Base(string(parts[0]))
		isSusanin := strings.Contains(binBase, "susanin-agent")
		if !isSusanin {
			if exe, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe")); err == nil {
				if strings.Contains(filepath.Base(exe), "susanin-agent") {
					isSusanin = true
				}
			}
		}
		if !isSusanin {
			continue
		}
		hasRun := false
		for _, arg := range parts[1:] {
			if string(arg) == "run" {
				hasRun = true
				break
			}
		}
		if hasRun || len(parts) == 1 {
			pids = append(pids, pid)
		}
	}
	return pids
}

func (p *ProcessManager) wait(cmd *exec.Cmd) {
	_ = cmd.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != cmd {
		return
	}
	p.cmd = nil
	_ = os.Remove(p.PidPath())
}

// Stop terminates the running susanin-agent.
func (p *ProcessManager) Stop(ctx context.Context) error {
	p.mu.Lock()
	pids := p.managedPIDsLocked()
	if len(pids) == 0 {
		if running, pid := p.isRunningLocked(); running && pid > 0 {
			pids = append(pids, pid)
		}
	}
	p.cmd = nil
	pidPath := p.PidPath()
	p.mu.Unlock()

	_ = os.Remove(pidPath)

	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil || proc == nil {
			continue
		}
		_ = proc.Signal(syscall.SIGTERM)
		for i := 0; i < 30; i++ {
			select {
			case <-ctx.Done():
				_ = proc.Kill()
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			if err := proc.Signal(syscall.Signal(0)); err != nil {
				break
			}
			if i == 29 {
				_ = proc.Kill()
			}
		}
	}

	return nil
}

// CleanupOrphans discovers and terminates any stray susanin-agent daemons.
func (p *ProcessManager) CleanupOrphans(ctx context.Context) error {
	p.mu.Lock()
	var orphanPids []int
	for _, pid := range p.managedPIDsLocked() {
		if p.cmd != nil && p.cmd.Process != nil && p.cmd.Process.Pid == pid {
			continue
		}
		orphanPids = append(orphanPids, pid)
	}
	p.mu.Unlock()

	for _, pid := range orphanPids {
		if proc, err := os.FindProcess(pid); err == nil && proc != nil {
			_ = proc.Signal(syscall.SIGTERM)
			for i := 0; i < 15; i++ {
				select {
				case <-ctx.Done():
					_ = proc.Kill()
					return ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
				if err := proc.Signal(syscall.Signal(0)); err != nil {
					break
				}
				if i == 14 {
					_ = proc.Kill()
				}
			}
		}
	}
	return nil
}

// IsRunning reports whether susanin-agent daemon is currently alive.
func (p *ProcessManager) IsRunning() (bool, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isRunningLocked()
}

func (p *ProcessManager) isRunningLocked() (bool, int) {
	if p.cmd != nil && p.cmd.Process != nil {
		pid := p.cmd.Process.Pid
		if pid > 0 {
			if err := p.cmd.Process.Signal(syscall.Signal(0)); err == nil {
				return true, pid
			}
		}
	}

	// Check pid file
	data, err := os.ReadFile(p.PidPath())
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false, 0
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, 0
	}
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		// On Linux, verify /proc/<pid>/cmdline contains susanin
		if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
			if strings.Contains(string(cmdline), "susanin") {
				return true, pid
			}
			return false, 0
		}
		return true, pid
	}

	return false, 0
}

// GetStatusOutput invokes `susanin-agent status` to retrieve runtime details.
func (p *ProcessManager) GetStatusOutput(ctx context.Context) (string, error) {
	if p.binaryPath == "" {
		return "", fmt.Errorf("binary path empty")
	}
	cmd := exec.CommandContext(ctx, p.binaryPath, "status")
	cmd.Dir = p.configDir
	cmd.Env = append(os.Environ(), "SUSANIN_CONF="+p.ConfigPath())
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("susanin-agent status: %w", err)
	}
	return out.String(), nil
}
