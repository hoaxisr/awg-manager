package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

const httpProxyTTL = 60 * time.Second

// HTTPProxyEntry is one `ip http proxy <name>` publication — the router's
// own HTTP server reverse-proxying an opkg application, optionally over
// KeenDNS. Verified shape (live router 2026-08-20):
//
//	"proxy": {"awgm": {
//	   "upstream": {"proto":"http","upstream":"192.168.0.1","port":"2222"},
//	   "domain": {"ndns": true},
//	   "security-level": {"public": true},
//	   "auth": true}}
//
// Auth means the ROUTER holds authentication in front of the upstream.
type HTTPProxyEntry struct {
	Name         string
	UpstreamPort string
	Public       bool
	Auth         bool
}

// HTTPProxyStore caches /show/rc/ip/http.
type HTTPProxyStore struct {
	*cache.ListStore[[]HTTPProxyEntry]
	getter Getter
}

func NewHTTPProxyStore(g Getter, log Logger) *HTTPProxyStore {
	s := &HTTPProxyStore{getter: g}
	s.ListStore = cache.NewListStore(httpProxyTTL, log, "ip-http-proxy", s.fetch)
	return s
}

func (s *HTTPProxyStore) fetch(ctx context.Context) ([]HTTPProxyEntry, error) {
	raw, err := s.getter.GetRaw(ctx, "/show/rc/ip/http")
	if err != nil {
		// 404 = subsystem absent on this firmware, not a failure.
		var httpErr *transport.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("fetch ip http: %w", err)
	}
	return parseHTTPProxies(raw)
}

func parseHTTPProxies(raw []byte) ([]HTTPProxyEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v struct {
		Proxy map[string]struct {
			Upstream struct {
				Port string `json:"port"`
			} `json:"upstream"`
			SecurityLevel struct {
				Public bool `json:"public"`
			} `json:"security-level"`
			Auth bool `json:"auth"`
		} `json:"proxy"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("decode ip http: %w", err)
	}
	entries := make([]HTTPProxyEntry, 0, len(v.Proxy))
	for name, p := range v.Proxy {
		entries = append(entries, HTTPProxyEntry{
			Name:         name,
			UpstreamPort: p.Upstream.Port,
			Public:       p.SecurityLevel.Public,
			Auth:         p.Auth,
		})
	}
	return entries, nil
}
