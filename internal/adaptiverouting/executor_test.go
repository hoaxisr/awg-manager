package adaptiverouting

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

type mockMihomoProviderForExecTest struct {
	groups []MihomoGroupInfo
}

func (m *mockMihomoProviderForExecTest) ListProxies() []MihomoProxyInfo { return nil }
func (m *mockMihomoProviderForExecTest) ListSubscriptions() []MihomoSubscriptionInfo {
	return nil
}
func (m *mockMihomoProviderForExecTest) ListGroups() []MihomoGroupInfo { return m.groups }

func TestSystemExecutor(t *testing.T) {
	exec := NewSystemExecutor()
	if exec.InterfaceName() != "" {
		t.Fatalf("expected empty interface initially")
	}

	egressWG := ResolvedEgress{
		Interface:   "Wireguard2",
		DisplayName: "WireGuard Server",
	}
	if err := exec.Prepare(context.Background(), egressWG); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if got := exec.InterfaceName(); got != "nwg2" {
		t.Fatalf("expected nwg2, got %s", got)
	}

	if err := exec.Commit(context.Background()); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	if err := exec.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	if err := exec.TearDown(context.Background()); err != nil {
		t.Fatalf("teardown failed: %v", err)
	}
	if got := exec.InterfaceName(); got != "" {
		t.Fatalf("expected empty interface after teardown, got %s", got)
	}

	egressOpkg := ResolvedEgress{
		Interface:   "OpkgTun1",
		DisplayName: "OpkgTun 1",
	}
	if err := exec.Prepare(context.Background(), egressOpkg); err != nil {
		t.Fatalf("prepare OpkgTun failed: %v", err)
	}
	if got := exec.InterfaceName(); got != "opkgtun1" {
		t.Fatalf("expected opkgtun1, got %s", got)
	}
}

func TestMihomoExecutor_TypedNilProviderDoesNotPanic(t *testing.T) {
	var typedNil *mockMihomoProviderForExecTest
	exec := NewMihomoExecutor(typedNil)

	egress := ResolvedEgress{
		Ref: EgressRef{
			Kind:       EgressKindMihomoGroup,
			ResourceID: "group-1",
			Engine:     EngineMihomo,
		},
		DisplayName: "Group 1",
	}

	if err := exec.Prepare(context.Background(), egress); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	if exec.AdaptiveConfig() != nil {
		t.Fatalf("expected nil AdaptiveConfig before commit")
	}
}

func TestMihomoExecutor_CommitWithInterface(t *testing.T) {
	oldIfaceByName := ifaceByName
	defer func() { ifaceByName = oldIfaceByName }()

	ifaceByName = func(name string) (*net.Interface, error) {
		if name == TunInterfaceName {
			return &net.Interface{Name: TunInterfaceName}, nil
		}
		return nil, errors.New("not found")
	}

	reloaded := false
	exec := NewMihomoExecutor(nil)
	exec.SetReloadFunc(func(ctx context.Context) error {
		reloaded = true
		return nil
	})

	egress := ResolvedEgress{
		Ref: EgressRef{
			Kind:       EgressKindMihomoProxy,
			ResourceID: "proxy-1",
			Engine:     EngineMihomo,
		},
		DisplayName: "Proxy 1",
	}

	if err := exec.Prepare(context.Background(), egress); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	if err := exec.Commit(context.Background()); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	if !reloaded {
		t.Fatalf("expected reloadFunc to be called")
	}

	if got := exec.InterfaceName(); got != TunInterfaceName {
		t.Fatalf("expected interface %s, got %s", TunInterfaceName, got)
	}

	cfg := exec.AdaptiveConfig()
	if cfg == nil || !cfg.Enabled || cfg.SelectedGroup != "Proxy 1" || cfg.Device != TunInterfaceName {
		t.Fatalf("unexpected adaptive config: %+v", cfg)
	}

	if err := exec.TearDown(context.Background()); err != nil {
		t.Fatalf("teardown failed: %v", err)
	}
	if exec.InterfaceName() != "" {
		t.Fatalf("expected empty interface after teardown")
	}
}

func TestMihomoExecutor_CommitMissingInterfaceRollsBack(t *testing.T) {
	oldIfaceByName := ifaceByName
	defer func() { ifaceByName = oldIfaceByName }()

	ifaceByName = func(name string) (*net.Interface, error) {
		return nil, errors.New("interface not found")
	}

	exec := NewMihomoExecutor(nil)
	egress := ResolvedEgress{
		DisplayName: "Proxy Test",
	}
	_ = exec.Prepare(context.Background(), egress)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := exec.Commit(ctx)
	if err == nil {
		t.Fatalf("expected Commit to fail when interface is missing")
	}

	if exec.InterfaceName() != "" {
		t.Fatalf("expected interface name to be empty after failure")
	}
}

func TestSingboxExecutor_CommitAndReload(t *testing.T) {
	oldIfaceByName := ifaceByName
	defer func() { ifaceByName = oldIfaceByName }()

	ifaceByName = func(name string) (*net.Interface, error) {
		if name == TunInterfaceName {
			return &net.Interface{Name: TunInterfaceName}, nil
		}
		return nil, errors.New("not found")
	}

	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	_ = orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true})
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap orchestrator failed: %v", err)
	}
	baseJSON := []byte(`{"outbounds": [{"type": "direct", "tag": "outbound-sg-1"}]}`)
	if err := orch.Save(orchestrator.SlotBase, baseJSON); err != nil {
		t.Fatalf("save base slot failed: %v", err)
	}

	exec := NewSingboxExecutor(orch)
	egress := ResolvedEgress{
		Ref: EgressRef{
			Kind:       EgressKindSingboxOutbound,
			ResourceID: "outbound-sg-1",
			Engine:     EngineSingbox,
		},
		DisplayName: "Singapore Outbound",
	}

	if err := exec.Prepare(context.Background(), egress); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	if err := exec.Commit(context.Background()); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	if got := exec.InterfaceName(); got != TunInterfaceName {
		t.Fatalf("expected %s, got %s", TunInterfaceName, got)
	}

	// Verify slot is enabled
	if !isSlotEnabled(orch, orchestrator.SlotAdaptiveEgress) {
		t.Fatalf("expected slot %s to be enabled", orchestrator.SlotAdaptiveEgress)
	}

	// Teardown disables slot
	if err := exec.TearDown(context.Background()); err != nil {
		t.Fatalf("teardown failed: %v", err)
	}
	if isSlotEnabled(orch, orchestrator.SlotAdaptiveEgress) {
		t.Fatalf("expected slot %s to be disabled after teardown", orchestrator.SlotAdaptiveEgress)
	}
}

func TestSingboxExecutor_CommitFailureDisablesSlot(t *testing.T) {
	oldIfaceByName := ifaceByName
	defer func() { ifaceByName = oldIfaceByName }()

	ifaceByName = func(name string) (*net.Interface, error) {
		return nil, errors.New("not found")
	}

	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	_ = orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true})
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap orchestrator failed: %v", err)
	}
	baseJSON := []byte(`{"outbounds": [{"type": "direct", "tag": "bad-egress"}]}`)
	if err := orch.Save(orchestrator.SlotBase, baseJSON); err != nil {
		t.Fatalf("save base slot failed: %v", err)
	}

	exec := NewSingboxExecutor(orch)
	egress := ResolvedEgress{
		Ref: EgressRef{ResourceID: "bad-egress"},
	}
	_ = exec.Prepare(context.Background(), egress)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := exec.Commit(ctx)
	if err == nil {
		t.Fatalf("expected Commit to fail on missing interface")
	}

	// Slot must be rolled back (disabled)
	if isSlotEnabled(orch, orchestrator.SlotAdaptiveEgress) {
		t.Fatalf("expected slot to be disabled after failed commit")
	}
}

func isSlotEnabled(orch *orchestrator.Orchestrator, slot orchestrator.Slot) bool {
	for _, st := range orch.Snapshot() {
		if st.Slot == slot {
			return st.Enabled
		}
	}
	return false
}
