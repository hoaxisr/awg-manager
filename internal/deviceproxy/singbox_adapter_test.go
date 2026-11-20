package deviceproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

func TestSingboxAdapter_SetShouldWriteSkipsWhenFalse(t *testing.T) {
	cfgDir := filepath.Join(t.TempDir(), "singbox", "config.d")
	orch := orchestrator.New(cfgDir, nil)
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotDeviceProxy, Filename: "12-deviceproxy.json"}); err != nil {
		t.Fatal(err)
	}

	adapter := NewSingboxAdapter(nil)
	adapter.SetOrch(orch)
	allowed := false
	adapter.SetShouldWrite(func() bool { return allowed })

	ctx := context.Background()
	spec := ExternalSpec{
		Enabled:     true,
		ListenAddr:  "0.0.0.0",
		Port:        1099,
		SelectedTag: "direct",
	}
	if err := adapter.ApplyDeviceProxy(ctx, spec); err != nil {
		t.Fatalf("ApplyDeviceProxy: %v", err)
	}
	if err := adapter.ApplyDeviceProxyNoReload(ctx, spec); err != nil {
		t.Fatalf("ApplyDeviceProxyNoReload: %v", err)
	}
	if err := adapter.ApplyDeviceProxyInstances(ctx, []ExternalInstanceSpec{{
		ID:          "dp-1",
		Enabled:     spec.Enabled,
		ListenAddr:  spec.ListenAddr,
		Port:        spec.Port,
		SelectedTag: spec.SelectedTag,
	}}); err != nil {
		t.Fatalf("ApplyDeviceProxyInstances: %v", err)
	}
	if _, err := os.Stat(cfgDir); !os.IsNotExist(err) {
		t.Fatalf("expected %s not to be created when shouldWrite=false (stat err=%v)", cfgDir, err)
	}

	allowed = true
	if err := adapter.ApplyDeviceProxy(ctx, spec); err != nil {
		t.Fatalf("ApplyDeviceProxy after re-enable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "12-deviceproxy.json")); err != nil {
		t.Fatalf("expected 12-deviceproxy.json written when shouldWrite=true: %v", err)
	}
}
