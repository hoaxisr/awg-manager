// Package tunnel contains legacy snapshot tests for architecture v2 migration.
// These tests record the command sequences executed by the current lifecycle code,
// allowing us to verify that the new architecture produces equivalent external behavior.
package tunnel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// CommandRecorder records all executed commands for verification.
type CommandRecorder struct {
	mu       sync.Mutex
	commands []RecordedCommand
}

// RecordedCommand represents a single executed command.
type RecordedCommand struct {
	Binary string
	Args   []string
	Time   time.Time
}

func (r *CommandRecorder) Record(binary string, args ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, RecordedCommand{
		Binary: binary,
		Args:   args,
		Time:   time.Now(),
	})
}

func (r *CommandRecorder) Commands() []RecordedCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RecordedCommand{}, r.commands...)
}

func (r *CommandRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = nil
}

func (r *CommandRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var sb strings.Builder
	for i, cmd := range r.commands {
		sb.WriteString(fmt.Sprintf("%d: %s %s\n", i+1, cmd.Binary, strings.Join(cmd.Args, " ")))
	}
	return sb.String()
}

// ExpectedStartOS5FullPath contains the expected command sequence for full start on OS 5.x.
// This is the "golden" snapshot that the new architecture must match.
var ExpectedStartOS5FullPath = []string{
	// Phase 1: Prepare OpkgTun
	`ndmc -c "interface OpkgTun0"`,
	`ndmc -c "interface OpkgTun0 description test-tunnel"`,
	`ndmc -c "interface OpkgTun0 security-level public"`,
	`ndmc -c "interface OpkgTun0 ip global auto"`,

	// Phase 2: Start amneziawg-go (recorded separately - process spawn)
	// /opt/sbin/amneziawg-go opkgtun0

	// Phase 2.5: Apply WireGuard config
	`/opt/sbin/awg setconf opkgtun0 /opt/etc/awg-manager/awg0.conf`,

	// Phase 3: Configure NDMS
	`ndmc -c "interface OpkgTun0 ip address 10.0.0.1 255.255.255.255"`,
	`ndmc -c "interface OpkgTun0 ip mtu 1420"`,
	`ndmc -c "interface OpkgTun0 ip tcp adjust-mss pmtu"`,
	`ndmc -c "interface OpkgTun0 up"`,
	`ndmc -c "system configuration save"`,

	// Phase 4: Set default route
	`ndmc -c "ip route default OpkgTun0"`,
	`ndmc -c "system configuration save"`,

	// Phase 5: iptables
	`iptables -w -A INPUT -i OpkgTun0 -j ACCEPT`,
	`iptables -w -A OUTPUT -o OpkgTun0 -j ACCEPT`,
	`iptables -w -A FORWARD -i OpkgTun0 -j ACCEPT`,
	`iptables -w -A FORWARD -o OpkgTun0 -j ACCEPT`,
}

// ExpectedStopOS5 contains the expected command sequence for stop on OS 5.x.
var ExpectedStopOS5 = []string{
	// Remove iptables
	`iptables -w -D FORWARD -o OpkgTun0 -j ACCEPT`,
	`iptables -w -D FORWARD -i OpkgTun0 -j ACCEPT`,
	`iptables -w -D OUTPUT -o OpkgTun0 -j ACCEPT`,
	`iptables -w -D INPUT -i OpkgTun0 -j ACCEPT`,

	// Remove default route
	`ndmc -c "no ip route default OpkgTun0"`,
	`ndmc -c "system configuration save"`,

	// Bring interface down
	`ndmc -c "interface OpkgTun0 down"`,

	// Remove peer (get key first)
	`/opt/sbin/awg show opkgtun0`,
	`/opt/sbin/awg set opkgtun0 peer <pubkey> remove`,

	// NOTE: Process is NOT killed in current implementation (workaround)
}

// ExpectedDeleteOS5 contains the expected command sequence for delete on OS 5.x.
var ExpectedDeleteOS5 = []string{
	// First: Stop (all commands from ExpectedStopOS5)
	// Then: Kill process and remove NDMS binding

	// Kill process (via PID file)
	// SIGTERM then SIGKILL

	// Remove from NDMS
	`ndmc -c "no ip route default OpkgTun0"`,
	`ndmc -c "no interface OpkgTun0"`,
	`ndmc -c "system configuration save"`,
}

// TestDocumentCurrentBehavior is a documentation test that outputs
// the expected command sequences for reference.
func TestDocumentCurrentBehavior(t *testing.T) {
	t.Log("=== OS 5.x Start (Full Path) ===")
	for i, cmd := range ExpectedStartOS5FullPath {
		t.Logf("%2d: %s", i+1, cmd)
	}

	t.Log("\n=== OS 5.x Stop ===")
	for i, cmd := range ExpectedStopOS5 {
		t.Logf("%2d: %s", i+1, cmd)
	}

	t.Log("\n=== OS 5.x Delete ===")
	for i, cmd := range ExpectedDeleteOS5 {
		t.Logf("%2d: %s", i+1, cmd)
	}
}

// TestCurrentArchitectureProblems documents the known issues.
func TestCurrentArchitectureProblems(t *testing.T) {
	problems := []struct {
		ID          string
		Description string
		Location    string
		Impact      string
	}{
		{
			ID:          "BUG-001",
			Description: "Two InterfaceExists functions with different semantics",
			Location:    "awg/status.go:38 vs tunnel/readiness.go:73",
			Impact:      "Desynchronized state detection causes 'already running' and 'exit 122' errors",
		},
		{
			ID:          "BUG-002",
			Description: "Process not killed on Stop (OS5)",
			Location:    "tunnel/lifecycle.go:594",
			Impact:      "Zombie processes, PID file desync, 'already running' on next Start",
		},
		{
			ID:          "BUG-003",
			Description: "Missing NAT/MASQUERADE rule",
			Location:    "tunnel/iptables.go",
			Impact:      "LAN clients cannot use tunnel as gateway",
		},
		{
			ID:          "BUG-004",
			Description: "Resume path doesn't check interface state",
			Location:    "tunnel/lifecycle.go:319",
			Impact:      "'interface up: exit 122' when interface already UP",
		},
		{
			ID:          "BUG-005",
			Description: "Full start path doesn't check process state",
			Location:    "tunnel/lifecycle.go:396",
			Impact:      "'already running' when interfaceAlive=false but process is zombie",
		},
		{
			ID:          "BUG-006",
			Description: "Stop ignores errors silently",
			Location:    "tunnel/lifecycle.go:583",
			Impact:      "No visibility into cleanup failures",
		},
	}

	for _, p := range problems {
		t.Logf("%s: %s\n  Location: %s\n  Impact: %s\n",
			p.ID, p.Description, p.Location, p.Impact)
	}
}

// MockExecRunner is a test helper that records commands instead of executing them.
type MockExecRunner struct {
	recorder  *CommandRecorder
	responses map[string]string // command -> stdout response
}

func NewMockExecRunner() *MockExecRunner {
	return &MockExecRunner{
		recorder:  &CommandRecorder{},
		responses: make(map[string]string),
	}
}

func (m *MockExecRunner) Run(_ context.Context, binary string, args ...string) (string, error) {
	m.recorder.Record(binary, args...)

	key := binary + " " + strings.Join(args, " ")
	if resp, ok := m.responses[key]; ok {
		return resp, nil
	}
	return "", nil
}

func (m *MockExecRunner) SetResponse(command, response string) {
	m.responses[command] = response
}

func (m *MockExecRunner) Commands() []RecordedCommand {
	return m.recorder.Commands()
}

// StateSnapshot captures the expected state after an operation.
type StateSnapshot struct {
	OpkgTunExists    bool
	InterfaceUp      bool
	ProcessRunning   bool
	HasPeer          bool
	IptablesRulesSet bool
	DefaultRouteSet  bool
}

// ExpectedStateAfterStart is the expected state after a successful Start.
var ExpectedStateAfterStart = StateSnapshot{
	OpkgTunExists:    true,
	InterfaceUp:      true,
	ProcessRunning:   true,
	HasPeer:          true,
	IptablesRulesSet: true,
	DefaultRouteSet:  true,
}

// ExpectedStateAfterStop is the expected state after Stop.
// NOTE: In current implementation, ProcessRunning is TRUE (bug/workaround).
var ExpectedStateAfterStop = StateSnapshot{
	OpkgTunExists:    true,  // Preserved for NDMS binding
	InterfaceUp:      false, // Brought down
	ProcessRunning:   true,  // BUG: Not killed in current implementation
	HasPeer:          false, // Removed to mark as "stopped"
	IptablesRulesSet: false, // Removed
	DefaultRouteSet:  false, // Removed
}

// ExpectedStateAfterStopFixed is what Stop SHOULD produce in v2.
var ExpectedStateAfterStopFixed = StateSnapshot{
	OpkgTunExists:    true,  // Preserved for NDMS binding
	InterfaceUp:      false, // Brought down
	ProcessRunning:   false, // FIXED: Process killed
	HasPeer:          false, // N/A - process dead
	IptablesRulesSet: false, // Removed
	DefaultRouteSet:  false, // Removed
}

// ExpectedStateAfterDelete is the expected state after Delete.
var ExpectedStateAfterDelete = StateSnapshot{
	OpkgTunExists:    false, // Removed from NDMS
	InterfaceUp:      false, // N/A
	ProcessRunning:   false, // Killed
	HasPeer:          false, // N/A
	IptablesRulesSet: false, // Removed
	DefaultRouteSet:  false, // Removed
}

func TestStateTransitions(t *testing.T) {
	t.Log("State transitions documentation:")
	t.Log("")
	t.Log("Initial (NotCreated) -> Start -> Running")
	t.Log("  OpkgTun: ❌ -> ✓")
	t.Log("  Interface: ❌ -> ✓ (UP)")
	t.Log("  Process: ❌ -> ✓")
	t.Log("  Peer: ❌ -> ✓")
	t.Log("")
	t.Log("Running -> Stop -> Stopped (CURRENT - buggy)")
	t.Log("  OpkgTun: ✓ -> ✓")
	t.Log("  Interface: ✓ (UP) -> ✓ (DOWN)")
	t.Log("  Process: ✓ -> ✓ (BUG: not killed)")
	t.Log("  Peer: ✓ -> ❌")
	t.Log("")
	t.Log("Running -> Stop -> Stopped (FIXED)")
	t.Log("  OpkgTun: ✓ -> ✓")
	t.Log("  Interface: ✓ (UP) -> ❌ (process dead)")
	t.Log("  Process: ✓ -> ❌")
	t.Log("  Peer: ✓ -> ❌")
	t.Log("")
	t.Log("Stopped -> Start -> Running")
	t.Log("  (Same as Initial -> Start, OpkgTun already exists)")
	t.Log("")
	t.Log("Any -> Delete -> NotCreated")
	t.Log("  OpkgTun: * -> ❌")
	t.Log("  Interface: * -> ❌")
	t.Log("  Process: * -> ❌")
	t.Log("  Peer: * -> ❌")
}
