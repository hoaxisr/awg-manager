package query

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

// peerTTL is short because the MetricsPoller refreshes on its own
// interval (~10s). This TTL mostly serves fast-back-to-back reads.
const peerTTL = 8 * time.Second

// PeerStore caches the .wireguard.peer list of /show/interface/{name} —
// the per-interface peer metrics. Per-interface key.
type PeerStore struct {
	getter Getter
	log    Logger

	store *cache.KeyedStore[string, []ndms.Peer]
}

func NewPeerStore(g Getter, log Logger) *PeerStore {
	return NewPeerStoreWithTTL(g, log, peerTTL)
}

func NewPeerStoreWithTTL(g Getter, log Logger, ttl time.Duration) *PeerStore {
	if log == nil {
		log = NopLogger()
	}
	s := &PeerStore{getter: g, log: log}
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
	var wrap struct {
		Wireguard struct {
			Peer []peerWire `json:"peer"`
		} `json:"wireguard"`
	}
	path := "/show/interface/" + name
	if err := s.getter.Get(ctx, path, &wrap); err != nil {
		// 404 means the interface itself doesn't exist (e.g. torn down) —
		// treat as zero peers so the poller doesn't log warnings on every
		// tick. A live interface with no peers returns an empty
		// .wireguard.peer instead.
		var httpErr *transport.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return []ndms.Peer{}, nil
		}
		return nil, fmt.Errorf("fetch peers %s: %w", name, err)
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
