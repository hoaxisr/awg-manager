package query

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
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

// newTestPeerStore — PeerStore над оракулом с Wireguard0..2; пиры задаёт тест
// через SetDetail. Карта интерфейсов возвращается, чтобы метить её грязной
// (Invalidate): тогда каждое чтение снимка — ровно один список, и ListCalls
// считает походы PeerStore в NDMS.
func newTestPeerStore(ttl time.Duration) (*PeerStore, *FakeNDMS, *InterfaceStore) {
	f := NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard2", Type: "Wireguard"})
	ifs := NewInterfaceStore(f, NopLogger())
	return NewPeerStoreWithTTL(NopLogger(), ifs, ttl), f, ifs
}

func TestPeerStore_FetchFromSnapshot(t *testing.T) {
	s, f, _ := newTestPeerStore(peerTTL)
	f.SetDetail("Wireguard0", json.RawMessage(sampleInterfaceJSON))

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
	if len(f.Posts) != 0 || f.E != 0 {
		t.Fatalf("Posts=%v E=%d, want none/0: пиры — из списка, не по имени", f.Posts, f.E)
	}
}

// A live interface with no peers returns an interface object whose
// .wireguard.peer is absent/empty — that must map to zero peers, not error.
func TestPeerStore_GetPeers_NoPeerFieldIsEmpty(t *testing.T) {
	s, f, _ := newTestPeerStore(peerTTL)
	f.SetDetail("Wireguard0", json.RawMessage(`{"type":"Wireguard","wireguard":{}}`))

	peers, err := s.GetPeers(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("GetPeers: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("no-peer interface must map to empty, got %d", len(peers))
	}
}

func TestPeerStore_GetPeers_CacheHitSkipsFetch(t *testing.T) {
	s, f, ifs := newTestPeerStore(peerTTL)
	f.SetDetail("Wireguard0", json.RawMessage(sampleInterfaceJSON))

	_, _ = s.GetPeers(context.Background(), "Wireguard0")
	lists := f.ListCalls()
	ifs.Invalidate("Wireguard0") // промах кэша пиров стал бы списком
	_, _ = s.GetPeers(context.Background(), "Wireguard0")
	if got := f.ListCalls() - lists; got != 0 {
		t.Errorf("списков на попадание в кэш: %d, want 0", got)
	}
}

func TestPeerStore_GetPeers_ServesStaleOnError(t *testing.T) {
	s, f, ifs := newTestPeerStore(20 * time.Millisecond)
	f.SetDetail("Wireguard0", json.RawMessage(sampleInterfaceJSON))

	if _, err := s.GetPeers(context.Background(), "Wireguard0"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	ifs.Invalidate("Wireguard0")
	f.FailList(errors.New("ndms down"))

	peers, err := s.GetPeers(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("stale-ok: want no error, got %v", err)
	}
	if len(peers) != 1 {
		t.Errorf("stale peers len: want 1, got %d", len(peers))
	}
}

// Записи нет в снимке — «пиров нет», не ошибка и без запроса по имени; список
// не прочитан — ошибка наружу (решение 4).
func TestPeerStore_GetPeers_NotInSnapshotIsEmpty(t *testing.T) {
	s, f, _ := newTestPeerStore(peerTTL)

	peers, err := s.GetPeers(context.Background(), "Wireguard7")
	if err != nil || len(peers) != 0 {
		t.Fatalf("нет в снимке: want (0 peers, nil), got (%v, %v)", peers, err)
	}
	if f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("E=%d Posts=%v, want 0/none", f.E, f.Posts)
	}

	s2, f2, _ := newTestPeerStore(peerTTL)
	f2.FailList(errors.New("ndms timeout"))
	if _, err := s2.GetPeers(context.Background(), "Wireguard2"); err == nil {
		t.Error("list error must surface")
	}
}

// Снят снаружи, ifdestroyed не доставлен: пиры — из снимка (прежние) или
// пусто, но ни одной E и ни одного POST.
func TestPeerStore_RemovedNoHook_NoRCI(t *testing.T) {
	s, f, ifs := newTestPeerStore(time.Millisecond)
	f.SetDetail("Wireguard1", json.RawMessage(sampleInterfaceJSON))
	if _, err := s.GetPeers(context.Background(), "Wireguard1"); err != nil {
		t.Fatal(err)
	}
	f.Remove("Wireguard1") // хук не доставлен
	time.Sleep(2 * time.Millisecond)
	if _, err := s.GetPeers(context.Background(), "Wireguard1"); err != nil {
		t.Fatal(err)
	}
	ifs.Invalidate("Wireguard1") // и через свежий список
	time.Sleep(2 * time.Millisecond)
	peers, err := s.GetPeers(context.Background(), "Wireguard1")
	if err != nil || len(peers) != 0 {
		t.Fatalf("после свежего списка: want (0 peers, nil), got (%v, %v)", peers, err)
	}
	if f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("E=%d Posts=%v, want 0/none", f.E, f.Posts)
	}
}

func TestPeerStore_InvalidateSingleAffectsOnlyThatName(t *testing.T) {
	s, f, ifs := newTestPeerStore(peerTTL)
	f.SetDetail("Wireguard0", json.RawMessage(sampleInterfaceJSON))
	f.SetDetail("Wireguard1", json.RawMessage(sampleInterfaceJSON))
	ctx := context.Background()

	_, _ = s.GetPeers(ctx, "Wireguard0")
	_, _ = s.GetPeers(ctx, "Wireguard1")

	s.Invalidate("Wireguard0")
	lists := f.ListCalls()
	ifs.Invalidate("Wireguard0")
	_, _ = s.GetPeers(ctx, "Wireguard0")
	if got := f.ListCalls() - lists; got != 1 {
		t.Errorf("Wireguard0 после Invalidate: списков %d, want 1", got)
	}
	lists = f.ListCalls()
	ifs.Invalidate("Wireguard1")
	_, _ = s.GetPeers(ctx, "Wireguard1")
	if got := f.ListCalls() - lists; got != 0 {
		t.Errorf("Wireguard1 не сброшен: списков %d, want 0", got)
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
