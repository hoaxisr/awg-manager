package deviceproxy

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

func TestService_GetConfig_ReturnsDefault(t *testing.T) {
	s := newTestService(t)
	got := s.GetConfig()
	if got.Enabled {
		t.Fatalf("default should not be enabled")
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	return NewService(Deps{Store: store})
}

func TestService_ValidateConfig_PortConflict(t *testing.T) {
	s := newTestService(t)

	bad := Config{Enabled: true, ListenAll: true, Port: 1080}
	s.withTunnelInboundPorts([]int{1080, 1081}) // helper

	err := s.ValidateConfig(bad)
	if err == nil {
		t.Fatalf("expected port conflict error")
	}
}

func TestService_ValidateConfig_EmptyAuthUsername(t *testing.T) {
	s := newTestService(t)
	bad := Config{
		Enabled: true, ListenAll: true, Port: 1099,
		Auth: AuthSpec{Enabled: true, Username: "", Password: "p"},
	}
	err := s.ValidateConfig(bad)
	if err == nil {
		t.Fatalf("expected empty-username error")
	}
}

func TestService_SaveConfig_AppliesToSingbox(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms})

	cfg := Config{
		Enabled:          true,
		ListenAll:        false,
		ListenInterface:  "Bridge0",
		Port:             1099,
		SelectedOutbound: "direct",
	}
	if err := s.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if sb.lastSpec == nil {
		t.Fatalf("singbox spec was not applied")
	}
	if sb.lastSpec.ListenAddr != "10.10.10.1" {
		t.Fatalf("listen resolved to %q, want 10.10.10.1", sb.lastSpec.ListenAddr)
	}
	if sb.lastSpec.SelectedTag != "direct" {
		t.Fatalf("selected = %q", sb.lastSpec.SelectedTag)
	}

	// Persisted to storage
	got := store.Get()
	if got != cfg {
		t.Fatalf("stored != saved:\n got=%#v\nwant=%#v", got, cfg)
	}
}

type fakeSingboxOperator struct {
	running             bool
	tags                []string
	tunnelInfos         []TunnelOutboundInfo
	lastSpec            *ExternalSpec
	lastSpecNR          *ExternalSpec // ApplyDeviceProxyNoReload call
	lastSelector        string
	lastMember          string
	runtimeActive       string // what GetSelectorActive returns
	lastInstanceSpecs   []ExternalInstanceSpec // last ApplyDeviceProxyInstances call payload
	applyInstancesCalls int                    // number of ApplyDeviceProxyInstances invocations
	applyInstancesErr   error                  // error to return from ApplyDeviceProxyInstances (nil = succeed)
}

func (f *fakeSingboxOperator) ApplyDeviceProxy(_ context.Context, spec ExternalSpec) error {
	f.lastSpec = &spec
	return nil
}
func (f *fakeSingboxOperator) ApplyDeviceProxyNoReload(_ context.Context, spec ExternalSpec) error {
	f.lastSpecNR = &spec
	return nil
}
func (f *fakeSingboxOperator) TunnelTags() []string { return f.tags }
func (f *fakeSingboxOperator) TunnelOutbounds() []TunnelOutboundInfo {
	return f.tunnelInfos
}
func (f *fakeSingboxOperator) IsRunning() bool      { return f.running }
func (f *fakeSingboxOperator) SetSelectorDefault(_ context.Context, selector, member string) error {
	f.lastSelector, f.lastMember = selector, member
	return nil
}
func (f *fakeSingboxOperator) GetSelectorActive(_ context.Context, _ string) (string, error) {
	if !f.running {
		return "", fmt.Errorf("not running")
	}
	return f.runtimeActive, nil
}

func (f *fakeSingboxOperator) ApplyDeviceProxyInstances(_ context.Context, specs []ExternalInstanceSpec) error {
	f.applyInstancesCalls++
	if f.applyInstancesErr != nil {
		return f.applyInstancesErr
	}
	f.lastInstanceSpecs = append([]ExternalInstanceSpec(nil), specs...)
	return nil
}

type fakeNDMSQuery struct{ addr string }

func (f *fakeNDMSQuery) GetInterfaceAddress(_ context.Context, _ string) (string, error) {
	return f.addr, nil
}

func TestService_SelectRuntimeOutbound_OnlyClashAPI(t *testing.T) {
	sb := &fakeSingboxOperator{running: true, tags: []string{"VLESS-RU"}}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	_ = store.Save(Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"})

	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms})

	if err := s.SelectRuntimeOutbound(context.Background(), "VLESS-RU"); err != nil {
		t.Fatalf("SelectRuntimeOutbound: %v", err)
	}
	if sb.lastSelector != "device-proxy-selector" || sb.lastMember != "VLESS-RU" {
		t.Fatalf("selector switch not called with expected args: %+v", sb)
	}
	// Storage must NOT be mutated — the switch is ephemeral.
	if got := store.Get().SelectedOutbound; got != "direct" {
		t.Fatalf("storage was written: SelectedOutbound=%q, want 'direct'", got)
	}
	// ApplyDeviceProxy must NOT have been called.
	if sb.lastSpec != nil {
		t.Fatalf("ApplyDeviceProxy called but shouldn't have been: %+v", sb.lastSpec)
	}
	if sb.lastSpecNR != nil {
		t.Fatalf("ApplyDeviceProxyNoReload called but shouldn't have been: %+v", sb.lastSpecNR)
	}
}

func TestService_SelectRuntimeOutbound_UnknownTag(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	_ = store.Save(Config{Enabled: true, ListenAll: true, Port: 1099})
	s := NewService(Deps{Store: store, Singbox: sb})

	err := s.SelectRuntimeOutbound(context.Background(), "nope")
	if err == nil || !errors.Is(err, ErrOutboundUnavailable) {
		t.Fatalf("got %v, want ErrOutboundUnavailable", err)
	}
}

// fakeAWGOutboundsCatalog is a test double for AWGOutboundsCatalog.
type fakeAWGOutboundsCatalog struct {
	tags []AWGTagInfo
	err  error
}

func (f *fakeAWGOutboundsCatalog) ListTags(_ context.Context) ([]AWGTagInfo, error) {
	return f.tags, f.err
}

type fakeSubscriptionOutboundsCatalog struct {
	items []SubscriptionOutboundInfo
}

func (f *fakeSubscriptionOutboundsCatalog) ListDeviceProxyOutbounds() []SubscriptionOutboundInfo {
	return append([]SubscriptionOutboundInfo(nil), f.items...)
}

func TestService_ListOutbounds_IncludesSystemTunnels(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	awgCatalog := &fakeAWGOutboundsCatalog{
		tags: []AWGTagInfo{
			{Tag: "awg-sys-Wireguard0", Label: "My VPN", Kind: "system", Iface: "nwg0"},
		},
	}
	s := NewService(Deps{Store: store, AWGOutbounds: awgCatalog})

	out := s.ListOutbounds(context.Background())

	found := false
	for _, ob := range out {
		if ob.Tag == "awg-sys-Wireguard0" {
			found = true
			if ob.Kind != "awg" {
				t.Fatalf("expected kind=awg, got %q", ob.Kind)
			}
			if ob.Label != "My VPN" {
				t.Fatalf("expected label=My VPN, got %q", ob.Label)
			}
			if ob.Detail != "nwg0" {
				t.Fatalf("expected detail=nwg0, got %q", ob.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("awg-sys-Wireguard0 not found in outbounds: %+v", out)
	}
}

func TestService_ListOutbounds_IncludesSingboxTunnelDetail(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	sb := &fakeSingboxOperator{
		tags: []string{"vless-1"},
		tunnelInfos: []TunnelOutboundInfo{
			{Tag: "vless-1", Protocol: "vless", Server: "example.com", Port: 443},
		},
	}
	s := NewService(Deps{Store: store, Singbox: sb})

	out := s.ListOutbounds(context.Background())
	for _, ob := range out {
		if ob.Tag != "vless-1" {
			continue
		}
		if ob.Detail != "VLESS · example.com:443" {
			t.Fatalf("unexpected detail: %q", ob.Detail)
		}
		return
	}
	t.Fatalf("vless-1 not found in outbounds: %+v", out)
}

func TestService_ListOutbounds_ClassifiesSubscriptionOutbounds(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	sub := &fakeSubscriptionOutboundsCatalog{
		items: []SubscriptionOutboundInfo{
			{Tag: "subscription-test-outbound", Label: "Subscription Test Route"},
		},
	}
	s := NewService(Deps{Store: store, SubscriptionOutbounds: sub})

	out := s.ListOutbounds(context.Background())
	for _, ob := range out {
		if ob.Tag != "subscription-test-outbound" {
			continue
		}
		if ob.Kind != "subscription" {
			t.Fatalf("expected kind=subscription, got %q", ob.Kind)
		}
		return
	}
	t.Fatalf("subscription-test-outbound not found in outbounds: %+v", out)
}

func TestService_SaveConfig_AppliesToSingbox_SystemTunnels(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	awgCatalog := &fakeAWGOutboundsCatalog{
		tags: []AWGTagInfo{
			{Tag: "awg-sys-Wireguard0", Label: "My VPN", Kind: "system", Iface: "nwg0"},
		},
	}
	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms, AWGOutbounds: awgCatalog})

	cfg := Config{
		Enabled:          true,
		ListenAll:        false,
		ListenInterface:  "Bridge0",
		Port:             1099,
		SelectedOutbound: "awg-sys-Wireguard0",
	}
	if err := s.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if sb.lastSpec == nil {
		t.Fatalf("singbox spec was not applied")
	}

	found := false
	for _, tag := range sb.lastSpec.AWGTags {
		if tag == "awg-sys-Wireguard0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("awg-sys-Wireguard0 not found in spec AWGTags: %+v", sb.lastSpec.AWGTags)
	}
}

func TestService_GetRuntimeState_Alive(t *testing.T) {
	sb := &fakeSingboxOperator{running: true, runtimeActive: "VLESS-RU"}
	store := NewStore(filepath.Join(t.TempDir(), "d.json"))
	_ = store.Save(Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"})
	s := NewService(Deps{Store: store, Singbox: sb})

	got := s.GetRuntimeState(context.Background())
	if !got.Alive || got.ActiveTag != "VLESS-RU" || got.DefaultTag != "direct" {
		t.Fatalf("runtime = %#v", got)
	}
}

func TestService_GetRuntimeState_Dead(t *testing.T) {
	sb := &fakeSingboxOperator{running: false}
	store := NewStore(filepath.Join(t.TempDir(), "d.json"))
	_ = store.Save(Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"})
	s := NewService(Deps{Store: store, Singbox: sb})

	got := s.GetRuntimeState(context.Background())
	if got.Alive || got.ActiveTag != "" {
		t.Fatalf("runtime = %#v, want Alive=false ActiveTag=''", got)
	}
	if got.DefaultTag != "direct" {
		t.Fatalf("DefaultTag = %q, want 'direct'", got.DefaultTag)
	}
}

func TestService_SaveConfig_DefaultOnly_SkipsReload(t *testing.T) {
	sb := &fakeSingboxOperator{running: true, tags: []string{"VLESS-RU"}}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "d.json"))
	start := Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"}
	_ = store.Save(start)

	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms})

	next := start
	next.SelectedOutbound = "VLESS-RU"
	if err := s.SaveConfig(context.Background(), next); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if sb.lastSpec != nil {
		t.Fatalf("ApplyDeviceProxy (reload path) was called but shouldn't have been")
	}
	if sb.lastSpecNR == nil || sb.lastSpecNR.SelectedTag != "VLESS-RU" {
		t.Fatalf("no-reload spec missing or wrong: %+v", sb.lastSpecNR)
	}
}

func TestService_SaveConfig_DefaultOnly_SingboxDown_Reloads(t *testing.T) {
	// When sing-box is NOT running, the no-reload path must not be taken
	// even if only SelectedOutbound changed — there's no live selector to
	// preserve, and the full-apply path includes the cold-start safety net.
	sb := &fakeSingboxOperator{running: false, tags: []string{"VLESS-RU"}}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "d.json"))
	start := Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"}
	_ = store.Save(start)

	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms})

	next := start
	next.SelectedOutbound = "VLESS-RU"
	if err := s.SaveConfig(context.Background(), next); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if sb.lastSpec == nil {
		t.Fatalf("ApplyDeviceProxy (reload path) was NOT called when sing-box is down")
	}
	if sb.lastSpecNR != nil {
		t.Fatalf("no-reload path taken incorrectly when sing-box is down")
	}
}

func TestService_SaveConfig_PortChange_Reloads(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "d.json"))
	start := Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"}
	_ = store.Save(start)

	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms})

	next := start
	next.Port = 1100
	if err := s.SaveConfig(context.Background(), next); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if sb.lastSpec == nil {
		t.Fatalf("ApplyDeviceProxy (reload path) was NOT called")
	}
	if sb.lastSpecNR != nil {
		t.Fatalf("no-reload path was incorrectly used for a port change")
	}
}

func TestService_SaveConfig_EnableToggle_Reloads(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "d.json"))
	// Start disabled.
	_ = store.Save(Config{Enabled: false, ListenAll: true, Port: 1099})
	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms})

	// Enable with a selected outbound — not a "default only" change because
	// Enabled flipped from false to true.
	next := Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"}
	if err := s.SaveConfig(context.Background(), next); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if sb.lastSpec == nil {
		t.Fatalf("Enable-toggle must go through the reload path")
	}
	if sb.lastSpecNR != nil {
		t.Fatalf("no-reload path taken incorrectly for Enable toggle")
	}
}

func TestService_SaveInstance_FailureRollbackReappliesSingbox(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	s := NewService(Deps{Store: store, Singbox: sb})

	// Save initial valid instance A — succeeds, first apply.
	instA := Instance{
		ID: "a", Name: "A",
		Enabled: true, ListenAll: true, Port: 1099,
		SelectedOutbound: "direct",
	}
	if err := s.SaveInstance(context.Background(), instA); err != nil {
		t.Fatalf("save A: %v", err)
	}
	firstApplyCount := sb.applyInstancesCalls
	if firstApplyCount != 1 {
		t.Fatalf("expected 1 apply after save A, got %d", firstApplyCount)
	}

	// Save instance B but make sing-box adapter fail on next apply.
	sb.applyInstancesErr = errors.New("simulated singbox apply failure")
	instB := Instance{
		ID: "b", Name: "B",
		Enabled: true, ListenAll: true, Port: 1100,
		SelectedOutbound: "direct",
	}
	err := s.SaveInstance(context.Background(), instB)
	if err == nil {
		t.Fatalf("expected save B to fail because apply errored")
	}

	// After failure: applyInstancesCalls should be 3:
	//   call #1 = initial save A
	//   call #2 = failed save B (during apply)
	//   call #3 = rollback re-apply with restored snapshot
	if sb.applyInstancesCalls != 3 {
		t.Errorf("expected 3 apply calls (initial + failed save + rollback reapply), got %d", sb.applyInstancesCalls)
	}

	// Storage should contain A and the default instance (B rolled back).
	snap := store.Snapshot()
	if len(snap.Instances) != 2 {
		t.Errorf("expected 2 instances (default + a), got %d: %v", len(snap.Instances), snap.Instances)
	}
	hasA := false
	hasDefault := false
	for _, inst := range snap.Instances {
		if inst.ID == "a" {
			hasA = true
		}
		if inst.ID == "default" {
			hasDefault = true
		}
	}
	if !hasA || !hasDefault {
		t.Errorf("storage should contain 'a' and 'default' after rollback, got %v", snap.Instances)
	}

	// Verify B was deleted from storage (should not appear in instances).
	hasB := false
	for _, inst := range snap.Instances {
		if inst.ID == "b" {
			hasB = true
		}
	}
	if hasB {
		t.Errorf("instance B should not be in storage after rollback, but found it")
	}

	// Reapply must use the rolled-back snapshot (without B).
	// Note: the reapply just happened above with applyInstancesErr still set,
	// so lastInstanceSpecs is NOT populated for it (the fake returns error
	// and skips the populate). To verify the reapply payload, clear the
	// error and trigger another save that succeeds — its lastInstanceSpecs
	// will reflect the (now-rolled-back) storage state.
	sb.applyInstancesErr = nil
	if err := s.SaveInstance(context.Background(), instA); err != nil {
		t.Fatalf("re-save A after clearing error: %v", err)
	}
	// Final payload should contain default and a (but not b).
	if len(sb.lastInstanceSpecs) != 2 {
		t.Errorf("expected 2 instance specs in final apply (default + a), got %d: %v", len(sb.lastInstanceSpecs), sb.lastInstanceSpecs)
	}
	hasFinalA := false
	hasFinalDefault := false
	for _, spec := range sb.lastInstanceSpecs {
		if spec.ID == "a" {
			hasFinalA = true
		}
		if spec.ID == "default" {
			hasFinalDefault = true
		}
	}
	if !hasFinalA || !hasFinalDefault {
		t.Errorf("final apply should contain 'default' and 'a' specs, got %v", sb.lastInstanceSpecs)
	}
}

func TestService_Reconcile_MissingTargetDisables(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	_ = store.Save(Config{
		Enabled:          true,
		ListenAll:        true,
		Port:             1099,
		SelectedOutbound: "awg-ghost", // tunnel that no longer exists
	})

	bus := events.NewBus()
	_, evCh, unsub := bus.Subscribe()
	defer unsub()

	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms, Bus: bus})
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := store.Get()
	if got.Enabled {
		t.Fatalf("expected disabled, got %#v", got)
	}
	if got.SelectedOutbound != "" {
		t.Fatalf("expected cleared SelectedOutbound, got %q", got.SelectedOutbound)
	}

	// Drain events, check that missing-target was published.
	sawMissing := false
	// Non-blocking read loop.
	for {
		select {
		case ev := <-evCh:
			if ev.Type == "deviceproxy:missing-target" {
				sawMissing = true
			}
		default:
			if !sawMissing {
				t.Fatalf("missing-target event was not published")
			}
			return
		}
	}
}

func TestService_SaveInstance_PortCollisionAcrossInstances(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	s := NewService(Deps{Store: store, Singbox: sb})

	// Save instance A on port 1099 (enabled).
	instA := Instance{
		ID: "a", Name: "A",
		Enabled: true, ListenAll: true, Port: 1099,
		SelectedOutbound: "direct",
	}
	if err := s.SaveInstance(context.Background(), instA); err != nil {
		t.Fatalf("save A: %v", err)
	}

	// Try to save instance B with same port (enabled) → should fail.
	instB := Instance{
		ID: "b", Name: "B",
		Enabled: true, ListenAll: true, Port: 1099,
		SelectedOutbound: "direct",
	}
	err := s.SaveInstance(context.Background(), instB)
	if err == nil {
		t.Fatalf("expected port-collision error for B, got nil")
	}
	if !contains(err.Error(), "port 1099") || !contains(err.Error(), "another") {
		t.Errorf("error should mention port and another instance, got: %v", err)
	}

	// Disabled B with same port → should succeed (disabled instances don't collide).
	instBDisabled := instB
	instBDisabled.Enabled = false
	if err := s.SaveInstance(context.Background(), instBDisabled); err != nil {
		t.Fatalf("save disabled B: %v", err)
	}

	// Re-saving A with same port (self) → should succeed (excluded by ID).
	if err := s.SaveInstance(context.Background(), instA); err != nil {
		t.Fatalf("re-save A: %v", err)
	}
}

func TestService_DeleteInstance_Default_PersistsWhenApplyFails(t *testing.T) {
	sb := &fakeSingboxOperator{running: true, applyInstancesErr: errors.New("simulated apply failure")}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	s := NewService(Deps{Store: store, Singbox: sb})

	if applied, err := s.DeleteInstance(context.Background(), "default"); err != nil {
		t.Fatalf("delete default: %v", err)
	} else if applied {
		t.Fatalf("expected apply to fail in this test")
	}

	snap := store.Snapshot()
	if len(snap.Instances) != 0 {
		t.Fatalf("expected empty snapshot after deleting default, got %#v", snap.Instances)
	}
	if sb.applyInstancesCalls != 1 {
		t.Fatalf("expected one apply attempt, got %d", sb.applyInstancesCalls)
	}
}

// contains is a tiny helper for substring assertions.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

type fakeRouterOutboundsCatalog struct {
	items []RouterOutboundInfo
}

func (f *fakeRouterOutboundsCatalog) ListDeviceProxyRouterOutbounds() []RouterOutboundInfo {
	return append([]RouterOutboundInfo(nil), f.items...)
}

func TestService_ListOutbounds_IncludesRouterOutbounds(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	cat := &fakeRouterOutboundsCatalog{items: []RouterOutboundInfo{
		{Tag: "grp-eu", Label: "grp-eu", Detail: "selector · 3"},
	}}
	s := NewService(Deps{Store: store})
	s.SetRouterOutbounds(cat)

	out := s.ListOutbounds(context.Background())

	var found *Outbound
	for i := range out {
		if out[i].Tag == "grp-eu" {
			found = &out[i]
		}
	}
	if found == nil {
		t.Fatalf("router outbound grp-eu not listed")
	}
	if found.Kind != "router" {
		t.Fatalf("expected kind=router, got %q", found.Kind)
	}
	if found.Detail != "selector · 3" {
		t.Fatalf("expected detail passthrough, got %q", found.Detail)
	}
}

func TestService_BuildSpec_RouterOutboundsBecomeSelectorMembers(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	cat := &fakeRouterOutboundsCatalog{items: []RouterOutboundInfo{
		{Tag: "grp-eu", Label: "grp-eu", Detail: "selector · 3"},
	}}
	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms})
	s.SetRouterOutbounds(cat)

	spec, err := s.buildSpec(context.Background(), Config{Enabled: true, ListenAll: true, Port: 1099})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	found := false
	for _, tag := range spec.SBTags {
		if tag == "grp-eu" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected grp-eu in SBTags, got %v", spec.SBTags)
	}
}

func TestService_SubscribeBus_RouterOutboundsTriggersReconcile(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	_ = store.Save(Config{
		Enabled:          true,
		ListenAll:        true,
		Port:             1099,
		SelectedOutbound: "router-ghost", // нет ни в одном каталоге
	})
	bus := events.NewBus()
	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms, Bus: bus})
	unsub := s.SubscribeBus(context.Background())
	defer unsub()

	bus.Publish("singbox-router:outbounds", nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !store.Get().Enabled {
			return // reconcile отработал и отключил висячий инстанс
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("reconcile не сработал на событие singbox-router:outbounds")
}
