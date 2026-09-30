package query

import (
	"context"
	"errors"
	"testing"
	"time"
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

// wgList — список интерфейсов для кэша: PeerStore читает только те, что в нём есть.
const wgList = `{
	"Wireguard0": {"id":"Wireguard0","type":"Wireguard"},
	"Wireguard1": {"id":"Wireguard1","type":"Wireguard"},
	"Wireguard2": {"id":"Wireguard2","type":"Wireguard"}
}`

func newTestPeerStore(fg *FakeGetter, ttl time.Duration) *PeerStore {
	fg.SetJSON(ifaceListPath, wgList)
	return NewPeerStoreWithTTL(NopLogger(), NewInterfaceStore(fg, NopLogger()), ttl)
}

func TestPeerStore_GetPeers_ParsesInterfacePeerField(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0", sampleInterfaceJSON)

	s := newTestPeerStore(fg, peerTTL)

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

	s := newTestPeerStore(fg, peerTTL)

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
	s := newTestPeerStore(fg, peerTTL)

	_, _ = s.GetPeers(context.Background(), "Wireguard0")
	_, _ = s.GetPeers(context.Background(), "Wireguard0")
	if got := fg.PostInterfaceCalls("Wireguard0"); got != 1 {
		t.Errorf("calls: want 1 (cache hit), got %d", got)
	}
}

func TestPeerStore_GetPeers_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0", sampleInterfaceJSON)
	s := newTestPeerStore(fg, 20*time.Millisecond)

	if _, err := s.GetPeers(context.Background(), "Wireguard0"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	fg.SetPostInterfaceError("Wireguard0", errors.New("ndms down"))

	peers, err := s.GetPeers(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("stale-ok: want no error, got %v", err)
	}
	if len(peers) != 1 {
		t.Errorf("stale peers len: want 1, got %d", len(peers))
	}
}

func TestPeerStore_GetPeers_GoneIsTreatedAsEmpty(t *testing.T) {
	// Кэш знает Wireguard1, NDMS — уже нет (потерян ifdestroyed): конверт
	// `unable to find` — «пиров нет», не ошибка, чтобы метрики не сыпали
	// предупреждениями.
	fg := newFakeGetter()
	fg.SetPostInterface("Wireguard1", `{"show":{"interface":{"status":[{"status":"error","code":"6553619","message":"unable to find"}]}}}`)

	s := newTestPeerStore(fg, peerTTL)

	peers, err := s.GetPeers(context.Background(), "Wireguard1")
	if err != nil {
		t.Fatalf("unable-to-find must not surface as error, got %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("unable-to-find must map to empty peers, got %d", len(peers))
	}

	// Прочие ошибки — наружу.
	fg.SetPostInterfaceError("Wireguard2", errors.New("ndms timeout"))
	if _, err := s.GetPeers(context.Background(), "Wireguard2"); err == nil {
		t.Error("transport error must surface")
	}
}

func TestPeerStore_InvalidateSingleAffectsOnlyThatName(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0", sampleInterfaceJSON)
	fg.SetJSON("/show/interface/Wireguard1", sampleInterfaceJSON)
	s := newTestPeerStore(fg, peerTTL)

	_, _ = s.GetPeers(context.Background(), "Wireguard0")
	_, _ = s.GetPeers(context.Background(), "Wireguard1")

	s.Invalidate("Wireguard0")
	_, _ = s.GetPeers(context.Background(), "Wireguard0")
	_, _ = s.GetPeers(context.Background(), "Wireguard1")

	if got := fg.PostInterfaceCalls("Wireguard0"); got != 2 {
		t.Errorf("Wireguard0: want 2, got %d", got)
	}
	if got := fg.PostInterfaceCalls("Wireguard1"); got != 1 {
		t.Errorf("Wireguard1: want 1, got %d", got)
	}
}

func TestNewPeerStore_NilIfacesPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil ifaces must panic")
		}
	}()
	NewPeerStore(NopLogger(), nil)
}
