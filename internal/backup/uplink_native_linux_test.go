//go:build linux

package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/wireguard"
)

func TestNativeUplinkProfileSurvivesExportRestore(t *testing.T) {
	uplinkProfileRoundTrip(t, "")
}
func TestSystemUplinkProfileSurvivesExportRestore(t *testing.T) {
	uplinkProfileRoundTrip(t, "uplink-system-wireguard")
}
func TestKernelUplinkProfileSurvivesExportRestore(t *testing.T) {
	uplinkProfileRoundTrip(t, "uplink-kernel-amneziawg")
}
func uplinkProfileRoundTrip(t *testing.T, subdir string) {
	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, subdir)
	if err := os.WriteFile(filepath.Join(sourceRoot, "settings.json"), []byte(`{"version":32}`), 0600); err != nil {
		t.Fatal(err)
	}
	ref := "awg50"
	bindingName := "6177673530.json"
	if err := os.MkdirAll(filepath.Join(source, "uplink-wireguard-bindings"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{"settings.json": `{"version":32}`, filepath.Join("uplink-wireguard-bindings", bindingName): `{"version":1,"device":"wan2","source":"10.44.2.1","gateway":"10.44.2.2"}`} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := wireguard.Config{Ref: ref, Interface: "nwg0", Peer: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), BindingFile: bindingName, Endpoint: netip.MustParseAddrPort("203.0.113.9:51820"), Mark: 0x4151, Table: 151, Priority: 1, TunnelDNS: []string{"10.77.0.2"}}
	if subdir != "" {
		config.AllowedIPs = []string{"0.0.0.0/0", "::/0"}
	}
	if subdir == "uplink-kernel-amneziawg" {
		config.KernelPhase = "preparing"
		config.LocalPublicKey = config.Peer
		config.Mark = 0x41570001
	}
	raw, err := json.Marshal(map[string]any{"version": 4, "profiles": map[string]wireguard.Config{ref: config}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "uplink-wireguard.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err = Export(sourceRoot, "native-uplink", &archive); err != nil {
		t.Fatal(err)
	}
	targetRoot := t.TempDir()
	if err = Restore(targetRoot, &archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetRoot, subdir)
	deps := wireguard.ManagedDeps{Directory: target, Lookup: func(context.Context, string) (wireguard.ManagedTarget, error) {
		return wireguard.ManagedTarget{}, uplink.ErrUnsupported
	}, List: func(context.Context) ([]wireguard.ManagedTarget, error) { return nil, nil }, Providers: func() []uplink.Provider { return nil }, WithLock: func(ctx context.Context, id string, work func() error) error { return work() }, ApplyBinding: func(context.Context, string, string, string) error { return nil }}
	manager, err := wireguard.NewManaged(deps)
	if err != nil || !manager.OwnsTarget(ref) {
		t.Fatal(manager, err)
	}
	if subdir == "uplink-kernel-amneziawg" {
		deps.Directory = targetRoot
		kernel, e := wireguard.NewKernel(wireguard.KernelDeps{ManagedDeps: deps, NativeMarks: func(context.Context) (map[string][]uint32, error) { return nil, nil }, StartProviders: func(string) []string { return []string{"isp1", "isp2"} }, RestoreRoute: func(context.Context, string) error { return nil }})
		if e != nil || !kernel.OwnsTarget(ref) {
			t.Fatal("kernel ownership journal lost during restore", e)
		}
	}
	if data, err := os.ReadFile(filepath.Join(target, "uplink-wireguard-bindings", bindingName)); err != nil || !bytes.Contains(data, []byte(`"device":"wan2"`)) {
		t.Fatal(string(data), err)
	}
}
