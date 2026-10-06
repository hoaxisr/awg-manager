package adaptiverouting

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type mockTunnelProvider struct {
	tunnels []TunnelInfo
}

func TestStore_DesiredDoesNotBecomeApplied(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.UpdateSettings(func(s *Settings) error {
		s.Enabled = true
		s.PrimaryEgress = EgressRef{Kind: EgressKindMihomoGroup, ResourceID: "draft", Engine: EngineMihomo}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied := store.GetApplied(); applied != nil {
		t.Fatalf("draft unexpectedly became applied: %+v", applied)
	}

	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.GetSettings().Enabled {
		t.Fatal("desired draft was not persisted")
	}
	if applied := reloaded.GetApplied(); applied != nil {
		t.Fatalf("reloaded draft unexpectedly became applied: %+v", applied)
	}
}

func TestStore_CommitAppliedPersistsConfigAndStateTogether(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	settings := DefaultSettings()
	settings.Enabled = true
	settings.PrimaryEgress = EgressRef{Kind: EgressKindKernelTunnel, ResourceID: "nwg1", Engine: EngineSystem}
	resolved := ResolvedEgress{Ref: settings.PrimaryEgress, DisplayName: "Home", Interface: "nwg1", Available: true}
	state := OperationalState{AppliedGeneration: "gen-test", RoutingOwner: RoutingOwnerSusanin, ActiveEgress: &resolved, Status: "running", LastReconcile: now}
	if err := store.CommitApplied(AppliedConfig{Generation: "gen-test", Settings: settings, Egress: resolved, Committed: now}, state); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	applied := reloaded.GetApplied()
	if applied == nil || applied.Generation != "gen-test" || applied.Settings.PrimaryEgress.ResourceID != "nwg1" {
		t.Fatalf("unexpected applied config: %+v", applied)
	}
	if got := reloaded.GetState(); got.Status != "running" || got.RoutingOwner != RoutingOwnerSusanin {
		t.Fatalf("unexpected runtime state: %+v", got)
	}
}

func TestStore_LegacyStateDoesNotAuthorizeBootRestore(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{"appliedGeneration":"old","routingOwner":"susanin","status":"running"}`)
	if err := os.WriteFile(filepath.Join(dir, "susanin_state.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if applied := store.GetApplied(); applied != nil {
		t.Fatalf("legacy state must not be considered restorable: %+v", applied)
	}
}

func (m *mockTunnelProvider) ListTunnels() []TunnelInfo {
	return m.tunnels
}

func TestStore_SettingsAndState(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	settings := store.GetSettings()
	if settings.RoutingTableID != 105 {
		t.Fatalf("expected routing table 105, got %d", settings.RoutingTableID)
	}
	if settings.FwmarkMask != "0x30000000" {
		t.Fatalf("expected mask 0x30000000, got %s", settings.FwmarkMask)
	}

	// Update settings
	updated, err := store.UpdateSettings(func(s *Settings) error {
		s.Enabled = true
		s.PrimaryEgress = EgressRef{
			Kind:       EgressKindMihomoGroup,
			ResourceID: "grp-123",
			Engine:     EngineMihomo,
		}
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if !updated.Enabled || updated.PrimaryEgress.ResourceID != "grp-123" {
		t.Fatalf("unexpected settings: %+v", updated)
	}

	// Reload store from disk to verify persistence
	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reload NewStore: %v", err)
	}
	if !reloaded.GetSettings().Enabled || reloaded.GetSettings().PrimaryEgress.ResourceID != "grp-123" {
		t.Fatalf("reloaded settings mismatch: %+v", reloaded.GetSettings())
	}
}

type mockMihomoProvider struct {
	groups []MihomoGroupInfo
}

func (m *mockMihomoProvider) ListProxies() []MihomoProxyInfo { return nil }
func (m *mockMihomoProvider) ListSubscriptions() []MihomoSubscriptionInfo { return nil }
func (m *mockMihomoProvider) ListGroups() []MihomoGroupInfo { return m.groups }

func TestCatalog_Resolve(t *testing.T) {
	nativeStore := &mockMihomoProvider{
		groups: []MihomoGroupInfo{
			{
				ID:      "grp-fastest",
				Name:    "FastestProxy",
				Enabled: true,
				Proxies: []string{"DIRECT"},
			},
		},
	}
	grpID := "grp-fastest"

	tp := &mockTunnelProvider{
		tunnels: []TunnelInfo{
			{ID: "awg-home", Name: "Home AWG", Interface: "awg10", Active: true, Kind: "awg"},
		},
	}

	catalog := NewCatalog(nativeStore, tp)
	egresses, err := catalog.ListEgresses(context.Background())
	if err != nil {
		t.Fatalf("ListEgresses: %v", err)
	}
	if len(egresses) < 2 {
		t.Fatalf("expected at least 2 egresses, got %d", len(egresses))
	}

	// Resolve group by ID
	resolvedGroup, err := catalog.Resolve(context.Background(), EgressRef{
		Kind:       EgressKindMihomoGroup,
		ResourceID: grpID,
		Engine:     EngineMihomo,
	})
	if err != nil {
		t.Fatalf("Resolve group: %v", err)
	}
	if resolvedGroup.DisplayName != "FastestProxy" || resolvedGroup.Interface != "awgsus0" {
		t.Fatalf("unexpected resolved group: %+v", resolvedGroup)
	}

	// Resolve tunnel
	resolvedTunnel, err := catalog.Resolve(context.Background(), EgressRef{
		Kind:       EgressKindKernelTunnel,
		ResourceID: "awg-home",
		Engine:     EngineSystem,
	})
	if err != nil {
		t.Fatalf("Resolve tunnel: %v", err)
	}
	if resolvedTunnel.Interface != "awg10" || !resolvedTunnel.Available {
		t.Fatalf("unexpected resolved tunnel: %+v", resolvedTunnel)
	}
}

func TestReferenceChecker(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.UpdateSettings(func(s *Settings) error {
		s.Enabled = true
		s.PrimaryEgress = EgressRef{
			Kind:       EgressKindMihomoGroup,
			ResourceID: "g-alpha",
			Engine:     EngineMihomo,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	checker := NewReferenceChecker(store)

	inUse, reason := checker.CheckResourceInUse("group", "g-alpha", "Group Alpha")
	if !inUse {
		t.Fatal("expected g-alpha to be in use")
	}
	if reason == "" {
		t.Fatal("expected reason message")
	}

	inUseOther, _ := checker.CheckResourceInUse("group", "g-other", "Other")
	if inUseOther {
		t.Fatal("expected g-other to NOT be in use")
	}
}

func TestStore_ExternalFileReload(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	st := store.GetSettings()
	if len(st.AlwaysEntries) != 0 {
		t.Fatalf("expected empty always entries, got %v", st.AlwaysEntries)
	}

	// External write to susanin.json
	time.Sleep(10 * time.Millisecond) // ensure mtime advances
	externalJSON := []byte(`{
		"enabled": true,
		"alwaysEntries": ["external.domain.com", "sub.domain.org"]
	}`)
	if err := os.WriteFile(filepath.Join(dir, "susanin.json"), externalJSON, 0644); err != nil {
		t.Fatal(err)
	}

	updated := store.GetSettings()
	if len(updated.AlwaysEntries) != 2 || updated.AlwaysEntries[0] != "external.domain.com" {
		t.Fatalf("expected reloaded always entries, got %v", updated.AlwaysEntries)
	}
}

