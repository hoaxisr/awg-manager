package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

// sampleInterfaceJSON mirrors the unwrapped /show/interface/<name> response:
// the interface object, with the peer list nested under .wireguard.peer.
// There is no standalone /wireguard/peer command — peers are a sub-field.
const sampleInterfaceJSON = `{
	"type": "Wireguard",
	"wireguard": {
		"peer": [
			{
				"public-key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
				"description": "warp",
				"local-port": 43185,
				"remote-port": 4500,
				"via": "PPPoE0",
				"local-endpoint-address": "198.51.100.207",
				"remote-endpoint-address": "162.159.192.1",
				"rxbytes": 1422,
				"txbytes": 11078,
				"last-handshake": 3,
				"online": true,
				"enabled": true,
				"fwmark": 268434092
			}
		]
	}
}`

func TestPeerStore_GetPeers_ParsesInterfacePeerField(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0", sampleInterfaceJSON)

	s := NewPeerStore(fg, NopLogger())

	peers, err := s.GetPeers(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("GetPeers: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("peers len: want 1, got %d", len(peers))
	}
	p := peers[0]
	if p.PublicKey != "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=" {
		t.Errorf("PublicKey: %s", p.PublicKey)
	}
	if p.RxBytes != 1422 || p.TxBytes != 11078 {
		t.Errorf("rx/tx: rx=%d tx=%d", p.RxBytes, p.TxBytes)
	}
	if p.LastHandshakeSecondsAgo != 3 {
		t.Errorf("LastHandshakeSecondsAgo: %d", p.LastHandshakeSecondsAgo)
	}
	if !p.Online || !p.Enabled {
		t.Errorf("flags: online=%v enabled=%v", p.Online, p.Enabled)
	}
}

// A live interface with no peers returns an interface object whose
// .wireguard.peer is absent/empty — that must map to zero peers, not error.
func TestPeerStore_GetPeers_NoPeerFieldIsEmpty(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0", `{"type":"Wireguard","wireguard":{}}`)

	s := NewPeerStore(fg, NopLogger())

	peers, err := s.GetPeers(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("GetPeers: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("no-peer interface must map to empty, got %d", len(peers))
	}
}

func TestPeerStore_GetPeers_CacheHitSkipsFetch(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0", sampleInterfaceJSON)
	s := NewPeerStore(fg, NopLogger())

	_, _ = s.GetPeers(context.Background(), "Wireguard0")
	_, _ = s.GetPeers(context.Background(), "Wireguard0")
	if got := fg.Calls("/show/interface/Wireguard0"); got != 1 {
		t.Errorf("calls: want 1 (cache hit), got %d", got)
	}
}

func TestPeerStore_GetPeers_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0", sampleInterfaceJSON)
	s := NewPeerStoreWithTTL(fg, NopLogger(), 20*time.Millisecond)

	if _, err := s.GetPeers(context.Background(), "Wireguard0"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	fg.SetError("/show/interface/Wireguard0", errors.New("ndms down"))

	peers, err := s.GetPeers(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("stale-ok: want no error, got %v", err)
	}
	if len(peers) != 1 {
		t.Errorf("stale peers len: want 1, got %d", len(peers))
	}
}

func TestPeerStore_GetPeers_404IsTreatedAsEmpty(t *testing.T) {
	// NDMS responds 404 when the interface itself doesn't exist (e.g. torn
	// down). That's "no peers", not a real error — translate to empty slice
	// so metrics don't spam warnings.
	fg := newFakeGetter()
	fg.SetError("/show/interface/Wireguard1",
		&transport.HTTPError{Method: "GET", Path: "/show/interface/Wireguard1", Status: 404})

	s := NewPeerStore(fg, NopLogger())

	peers, err := s.GetPeers(context.Background(), "Wireguard1")
	if err != nil {
		t.Fatalf("404 must not surface as error, got %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("404 must map to empty peers, got %d", len(peers))
	}

	// Non-404 errors still surface.
	fg.SetError("/show/interface/Wireguard2", errors.New("ndms timeout"))
	if _, err := s.GetPeers(context.Background(), "Wireguard2"); err == nil {
		t.Error("non-404 error must surface")
	}
}

func TestPeerStore_InvalidateSingleAffectsOnlyThatName(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0", sampleInterfaceJSON)
	fg.SetJSON("/show/interface/Wireguard1", sampleInterfaceJSON)
	s := NewPeerStore(fg, NopLogger())

	_, _ = s.GetPeers(context.Background(), "Wireguard0")
	_, _ = s.GetPeers(context.Background(), "Wireguard1")

	s.Invalidate("Wireguard0")
	_, _ = s.GetPeers(context.Background(), "Wireguard0")
	_, _ = s.GetPeers(context.Background(), "Wireguard1")

	if got := fg.Calls("/show/interface/Wireguard0"); got != 2 {
		t.Errorf("Wireguard0: want 2, got %d", got)
	}
	if got := fg.Calls("/show/interface/Wireguard1"); got != 1 {
		t.Errorf("Wireguard1: want 1, got %d", got)
	}
}
