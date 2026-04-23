package ops

import (
	"context"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// === Mock implementations ===
//
// Operators consume NDMS via *query.Queries and *command.Commands directly
// (see operator_os5.go). Tests that don't exercise NDMS paths simply pass
// nil for both — methods that touch NDMS will panic, which is exactly the
// signal we want for tests that claim to be NDMS-free. Tests that DO need
// NDMS behaviour build real Queries/Commands with a FakeGetter (see the
// NewFakeGetter helper in internal/ndms/query).

// MockWGClient for WireGuard operations.
type MockWGClient struct {
	setConfError error
	showError    error
	hasPeer      bool

	SetConfCalls []struct{ Iface, Path string }
}

func (m *MockWGClient) SetConf(ctx context.Context, iface, confPath string) error {
	m.SetConfCalls = append(m.SetConfCalls, struct{ Iface, Path string }{iface, confPath})
	return m.setConfError
}

func (m *MockWGClient) Show(ctx context.Context, iface string) (*wg.ShowResult, error) {
	if m.showError != nil {
		return nil, m.showError
	}
	return &wg.ShowResult{HasPeer: m.hasPeer}, nil
}

func (m *MockWGClient) RemovePeer(ctx context.Context, iface, publicKey string) error {
	return nil
}

func (m *MockWGClient) GetPeerPublicKey(ctx context.Context, iface string) (string, error) {
	if m.hasPeer {
		return "mock-key", nil
	}
	return "", nil
}

// MockBackend for kernel backend operations.
type MockBackend struct {
	running      bool
	pid          int
	startError   error
	stopError    error
	waitReadyErr error

	StartCalls []string
	StopCalls  []string
}

func (m *MockBackend) Type() backend.Type {
	return backend.TypeKernel
}

func (m *MockBackend) Start(ctx context.Context, ifaceName string) error {
	m.StartCalls = append(m.StartCalls, ifaceName)
	if m.startError == nil {
		m.running = true
		m.pid = 12345
	}
	return m.startError
}

func (m *MockBackend) Stop(ctx context.Context, ifaceName string) error {
	m.StopCalls = append(m.StopCalls, ifaceName)
	m.running = false
	m.pid = 0
	return m.stopError
}

func (m *MockBackend) IsRunning(ctx context.Context, ifaceName string) (bool, int) {
	return m.running, m.pid
}

func (m *MockBackend) WaitReady(ctx context.Context, ifaceName string, timeout time.Duration) error {
	return m.waitReadyErr
}

// MockFirewall for firewall operations.
type MockFirewall struct {
	addError    error
	removeError error
	hasRules    bool

	AddCalls    []string
	RemoveCalls []string
}

func (m *MockFirewall) AddRules(ctx context.Context, iface string) error {
	m.AddCalls = append(m.AddCalls, iface)
	return m.addError
}

func (m *MockFirewall) RemoveRules(ctx context.Context, iface string) error {
	m.RemoveCalls = append(m.RemoveCalls, iface)
	return m.removeError
}

func (m *MockFirewall) HasRules(ctx context.Context, iface string) bool {
	return m.hasRules
}

// === Test helpers ===

// mockIPRun is a no-op ip command runner for tests.
func mockIPRun(_ context.Context, _ string, _ ...string) (*exec.Result, error) {
	return &exec.Result{}, nil
}

// ipRunRecorder records ip command calls for assertion.
type ipRunRecorder struct {
	Calls []string
}

func (r *ipRunRecorder) run(_ context.Context, name string, args ...string) (*exec.Result, error) {
	r.Calls = append(r.Calls, name+" "+strings.Join(args, " "))
	return &exec.Result{}, nil
}
