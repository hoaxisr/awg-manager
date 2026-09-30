package query

import (
	"context"
	"encoding/json"
	"errors"
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
	// interfaces — единственный путь чтения по имени: пиров отсутствующего
	// в кэше интерфейса не спрашиваем (F546).
	interfaces *InterfaceStore

	store *cache.KeyedStore[string, []ndms.Peer]
}

// NewPeerStore — PeerStore поверх кэша интерфейсов ifaces (обязателен).
func NewPeerStore(log Logger, ifaces *InterfaceStore) *PeerStore {
	return NewPeerStoreWithTTL(log, ifaces, peerTTL)
}

// NewPeerStoreWithTTL — то же с заданным TTL. Чтение идёт только через ifaces
// (единый шлюз showOne), своего getter у стора нет.
func NewPeerStoreWithTTL(log Logger, ifaces *InterfaceStore, ttl time.Duration) *PeerStore {
	if ifaces == nil {
		panic("query.NewPeerStore: ifaces обязателен — чтение по имени идёт только через InterfaceStore (F546)")
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
	// Peers are NOT a standalone RCI command — there is no
	// "show interface <name> wireguard peer" command. The peer list is a
	// data sub-field of "show interface <name>" (.wireguard.peer). A direct
	// GET /show/interface/<name>/wireguard/peer happens to work (the GET
	// handler descends the response tree by URL segment), but that path is
	// not expressible as a batch-POST command — NDMS parses wireguard/peer
	// as a command continuation and answers "not found". Querying the
	// interface and reading .wireguard.peer works in both the direct-GET and
	// batch-POST transports. Verified against Keenetic RCI 2026-05-23.
	// Интерфейса нет в кэше — ноль пиров без запроса: на show interface по
	// отсутствующему имени NDMS пишет E «unable to find» в свой журнал, а
	// managed-сервер с пропавшим WireguardN опрашивается постоянно (F546).
	// ErrGone — то же «нет»: запись уже выселена, следующий опрос не спросит.
	p, ok, err := s.interfaces.Lookup(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("fetch peers %s: %w", name, err)
	}
	if !ok {
		return []ndms.Peer{}, nil
	}
	inner, err := s.interfaces.ShowRaw(ctx, p)
	if errors.Is(err, ErrGone) {
		return []ndms.Peer{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetch peers %s: %w", name, err)
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
