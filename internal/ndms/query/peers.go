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

// PeerStore caches /show/interface/{name}/wireguard/peer — the narrow
// endpoint used for metrics. Per-interface key.
type PeerStore struct {
	getter Getter
	log    Logger

	cache    *cache.TTL[string, []ndms.Peer]
	inFlight *cache.SingleFlight[string, []ndms.Peer]
}

func NewPeerStore(g Getter, log Logger) *PeerStore {
	return NewPeerStoreWithTTL(g, log, peerTTL)
}

func NewPeerStoreWithTTL(g Getter, log Logger, ttl time.Duration) *PeerStore {
	if log == nil {
		log = NopLogger()
	}
	return &PeerStore{
		getter:   g,
		log:      log,
		cache:    cache.NewTTL[string, []ndms.Peer](ttl),
		inFlight: cache.NewSingleFlight[string, []ndms.Peer](),
	}
}

// GetPeers returns the peer list for a wireguard interface.
func (s *PeerStore) GetPeers(ctx context.Context, name string) ([]ndms.Peer, error) {
	if v, ok := s.cache.Get(name); ok {
		return v, nil
	}
	return s.inFlight.Do(name, func() ([]ndms.Peer, error) {
		v, err := s.fetch(ctx, name)
		if err != nil {
			if stale, ok := s.cache.Peek(name); ok {
				s.log.Warnf("peers %s fetch failed, serving stale cache: %v", name, err)
				return stale, nil
			}
			return nil, err
		}
		s.cache.Set(name, v)
		return v, nil
	})
}

// Invalidate drops cache for a single interface. Called by events.Dispatcher.
func (s *PeerStore) Invalidate(name string) { s.cache.Invalidate(name) }

// InvalidateAll drops every cached entry (daemon reconfigure).
func (s *PeerStore) InvalidateAll() { s.cache.InvalidateAll() }

// peerWire mirrors the JSON shape of one element from
// /show/interface/{name}/wireguard/peer.
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
	var wire []peerWire
	path := "/show/interface/" + name + "/wireguard/peer"
	if err := s.getter.Get(ctx, path, &wire); err != nil {
		// NDMS responds 404 when the interface has no peers configured
		// (typical right after creating a server before any clients
		// have been added). That's a legitimate "empty" state, not a
		// failure — treat it as zero peers so the poller doesn't log
		// warnings on every tick.
		var httpErr *transport.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return []ndms.Peer{}, nil
		}
		return nil, fmt.Errorf("fetch peers %s: %w", name, err)
	}
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
