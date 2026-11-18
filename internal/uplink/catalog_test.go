package uplink

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type catalogAdapter struct {
	Adapter
	targets []TargetInfo
	err     error
}

func (a catalogAdapter) UplinkTargets(context.Context) ([]TargetInfo, error) { return a.targets, a.err }

func TestCatalogKeepsHealthySourcesAndStableTargetIdentity(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	native := TargetInfo{Target: Target{Engine: "wireguard", Ref: "awg20"}, Label: "Native"}
	c := New(s, nil, map[string]Adapter{
		"wireguard": catalogAdapter{targets: []TargetInfo{native}},
		"broken":    catalogAdapter{err: errors.New("source unavailable")},
	})
	c.TargetCatalog = func(context.Context) ([]TargetInfo, error) {
		return []TargetInfo{
			native,
			{Target: Target{Engine: "system-wireguard", Ref: "Wireguard1"}, Label: "System", UnavailableReason: "adapter pending"},
			{Target: Target{Engine: "sing-box", Ref: "same", Kind: "subscription"}, Label: "Subscription"},
			{Target: Target{Engine: "sing-box", Ref: "same"}, Label: "Standalone"},
		}, errors.New("partial inventory")
	}
	list, err := c.Targets(context.Background())
	if err == nil || len(list) != 4 {
		t.Fatal(list, err)
	}
	if list[0].Target.Kind != "" || list[1].Target.Kind != "subscription" || list[2].UnavailableReason == "" || list[3].Target != native.Target {
		t.Fatal("identity/order/availability lost", list)
	}
	// Inventory-only entries cannot become enabled policies without an adapter.
	_, err = c.Put(context.Background(), Policy{ID: "unavailable", Enabled: true, Target: list[2].Target, Providers: []string{"one", "two"}})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal("inventory entry became executable", err)
	}
}
