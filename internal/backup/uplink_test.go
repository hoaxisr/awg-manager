package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestUplinkPolicySurvivesExportRestore(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"version":32}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := uplink.OpenStore(filepath.Join(source, "uplink-policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := store.Put(uplink.Policy{ID: "stable-policy", Target: uplink.Target{Engine: "sing-box", Ref: "vpn"}, Providers: []string{"isp1", "isp2"}, Enabled: true, Failback: "automatic", RecoveryThreshold: 2})
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := Export(source, "uplink-test", &archive); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := Restore(target, &archive); err != nil {
		t.Fatal(err)
	}
	restored, err := uplink.OpenStore(filepath.Join(target, "uplink-policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.List()) != 1 || !reflect.DeepEqual(restored.List()[0], policy) {
		t.Fatal(restored.List())
	}
}
