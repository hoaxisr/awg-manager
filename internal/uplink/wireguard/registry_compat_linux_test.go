//go:build linux

package wireguard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestManagedRegistryVersionsPreservePayloadScope(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4, 5} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			dir := t.TempDir()
			c := configFixture(t)
			c.BindingFile = filepath.Base((&Managed{deps: ManagedDeps{Directory: dir}}).bindingPath(c.Ref))
			if version >= 3 {
				c.AllowedIPs = []string{"0.0.0.0/0", "::/0"}
				c.LocalPublicKey = c.Peer
			}
			raw, err := json.Marshal(registry{Version: version, Profiles: map[string]Config{c.Ref: c}})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "uplink-wireguard.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			deps := ManagedDeps{Directory: dir, Lookup: func(context.Context, string) (ManagedTarget, error) { return ManagedTarget{}, uplink.ErrUnsupported }, List: func(context.Context) ([]ManagedTarget, error) { return nil, nil }, Providers: func() []uplink.Provider { return nil }, WithLock: func(_ context.Context, _ string, fn func() error) error { return fn() }, ApplyBinding: func(context.Context, string, string, string) error { return nil }}
			m, err := NewManaged(deps)
			if version == 5 {
				if err == nil {
					t.Fatal("unknown registry accepted")
				}
				return
			}
			if err != nil || !m.OwnsTarget(c.Ref) {
				t.Fatal(m, err)
			}
			loaded := m.profiles[c.Ref]
			if !filepath.IsAbs(loaded.BindingFile) || filepath.Dir(loaded.BindingFile) != filepath.Join(dir, "uplink-wireguard-bindings") {
				t.Fatal("restored path not rebased", loaded.BindingFile)
			}
			if version == 3 && (payloadPrefixes(loaded.AllowedIPs) != "0.0.0.0/0 ::/0" || loaded.LocalPublicKey != c.Peer) {
				t.Fatal("payload identity lost", loaded)
			}
		})
	}
}
