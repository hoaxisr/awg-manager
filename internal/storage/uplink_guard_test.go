package storage

import (
	"errors"
	"testing"
)

func TestAWGUplinkGuardBlocksDirectWritesAndDeepMutations(t *testing.T) {
	store, _ := newTestAWGStore(t)
	seedTunnel(t, store, &AWGTunnel{ID: "awg50", Backend: "nativewg", Name: "before", ISPInterface: "ISP1", Peer: AWGPeer{AllowedIPs: []string{"0.0.0.0/0"}}, Obfuscator: &Obfuscator{Key: "original"}})
	blocked := errors.New("uplink owns transport")
	store.SetUplinkGuard(func(old, next *AWGTunnel) error {
		if !SameUplinkTransport(old, next) {
			return blocked
		}
		return nil
	})
	for _, mut := range []func(*AWGTunnel) error{
		func(t *AWGTunnel) error { t.ISPInterface = ""; return nil },
		func(t *AWGTunnel) error { t.Peer.AllowedIPs[0] = "10.0.0.0/8"; return nil },
		func(t *AWGTunnel) error { t.Obfuscator.Key = "changed"; return nil },
	} {
		if err := store.Update("awg50", mut); !errors.Is(err, blocked) {
			t.Fatal(err)
		}
	}
	if err := store.Update("awg50", func(t *AWGTunnel) error { t.Name = "renamed"; t.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateForUplink("awg50", func(t *AWGTunnel) error { t.ISPInterface = "ISP2"; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("awg50")
	if err != nil || got.Name != "renamed" || got.ISPInterface != "ISP2" || got.Obfuscator.Key != "original" || got.Peer.AllowedIPs[0] != "0.0.0.0/0" {
		t.Fatal(got, err)
	}
	if err := store.Delete("awg50"); !errors.Is(err, blocked) {
		t.Fatal("delete bypassed guard", err)
	}
}
