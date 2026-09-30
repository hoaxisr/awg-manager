package query

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

// peerTTL is short because the MetricsPoller refreshes on its own
// interval (~10s). This TTL mostly serves fast-back-to-back reads.
// От него зависит допуск дедупа серверов в поллере метрик
// (metrics.handshakeJitter): его поднимать вместе с этим значением.
const peerTTL = 8 * time.Second

// PeerStore caches the .wireguard.peer list of /show/interface/{name} —
// the per-interface peer metrics. Per-interface key.
type PeerStore struct {
	log Logger
	// interfaces — источник записей: пиры читаются из снимка полного списка,
	// по имени NDMS не спрашивают (F546).
	interfaces *InterfaceStore

	store *cache.KeyedStore[string, []ndms.Peer]
}

// NewPeerStore — PeerStore поверх кэша интерфейсов ifaces (обязателен).
func NewPeerStore(log Logger, ifaces *InterfaceStore) *PeerStore {
	return NewPeerStoreWithTTL(log, ifaces, peerTTL)
}

// NewPeerStoreWithTTL — то же с заданным TTL. Чтение идёт только через ifaces
// (снимок списка), своего getter у стора нет.
func NewPeerStoreWithTTL(log Logger, ifaces *InterfaceStore, ttl time.Duration) *PeerStore {
	if ifaces == nil {
		panic("query.NewPeerStore: ifaces обязателен — пиры читаются из снимка InterfaceStore (F546)")
	}
	if log == nil {
		log = NopLogger()
	}
	s := &PeerStore{log: log, interfaces: ifaces}
	s.store = cache.NewKeyedStore(ttl, log, "peers", s.fetch)
	return s
}

// GetPeers returns the peer list for a wireguard interface.
func (s *PeerStore) GetPeers(ctx context.Context, name string) ([]ndms.Peer, error) {
	return s.store.Get(ctx, name)
}

// Invalidate drops cache for a single interface. Called by events.Dispatcher.
func (s *PeerStore) Invalidate(name string) { s.store.Invalidate(name) }

// InvalidateAll drops every cached entry (daemon reconfigure).
func (s *PeerStore) InvalidateAll() { s.store.InvalidateAll() }

// peerWire mirrors the JSON shape of one element of the
// .wireguard.peer array inside /show/interface/{name}.
type peerWire struct {
	PublicKey               string `json:"public-key"`
	Description             string `json:"description"`
	LocalPort               int    `json:"local-port"`
	RemotePort              int    `json:"remote-port"`
	Via                     string `json:"via"`
	LocalEndpointAddress    string `json:"local-endpoint-address"`
	RemoteEndpointAddress   string `json:"remote-endpoint-address"`
	RxBytes                 int64  `json:"rxbytes"`
	TxBytes                 int64  `json:"txbytes"`
	LastHandshakeSecondsAgo int64  `json:"last-handshake"`
	Online                  bool   `json:"online"`
	Enabled                 bool   `json:"enabled"`
	Fwmark                  int64  `json:"fwmark"`
}

func (s *PeerStore) fetch(ctx context.Context, name string) ([]ndms.Peer, error) {
	// Peers are NOT a standalone RCI command — the peer list is a data
	// sub-field of the interface record (.wireguard.peer). Берём его из
	// снимка полного списка (не старше SnapshotRecent): поля записи списка те
	// же, что у `show interface <name>`, а по имени NDMS не спрашивают —
	// запрос, долетевший между снятием записи и хуком ifdestroyed, пишет E
	// «unable to find» (F546). Параллельные GetPeers тика делят один список.
	// Записи в снимке нет — ноль пиров; список не прочитан — ошибка.
	snap, err := s.interfaces.Snapshot(ctx, SnapshotRecent)
	if err != nil {
		return nil, fmt.Errorf("fetch peers %s: %w", name, err)
	}
	inner, ok := snap.Raw(name)
	if !ok {
		return []ndms.Peer{}, nil
	}
	var wrap struct {
		Wireguard struct {
			Peer []peerWire `json:"peer"`
		} `json:"wireguard"`
	}
	if len(inner) > 0 {
		if err := json.Unmarshal(inner, &wrap); err != nil {
			return nil, fmt.Errorf("fetch peers %s: %w", name, err)
		}
	}
	wire := wrap.Wireguard.Peer
	out := make([]ndms.Peer, 0, len(wire))
	for _, w := range wire {
		out = append(out, ndms.Peer{
			PublicKey:               w.PublicKey,
			Description:             w.Description,
			LocalPort:               w.LocalPort,
			RemotePort:              w.RemotePort,
			Via:                     w.Via,
			LocalEndpointAddress:    w.LocalEndpointAddress,
			RemoteEndpointAddress:   w.RemoteEndpointAddress,
			RxBytes:                 w.RxBytes,
			TxBytes:                 w.TxBytes,
			LastHandshakeSecondsAgo: w.LastHandshakeSecondsAgo,
			Online:                  w.Online,
			Enabled:                 w.Enabled,
			Fwmark:                  w.Fwmark,
		})
	}
	return out, nil
}
