package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

const httpProxyTTL = 60 * time.Second

// HTTPProxyEntry is one `ip http proxy <name>` publication — the router's
// own HTTP server reverse-proxying an opkg application, optionally over
// KeenDNS. Verified shape (live router 2026-08-20 & 2026-11-03):
//
//	"proxy": {"awgm": {
//	   "upstream": {"proto":"http","upstream":"192.168.0.1","port":"2222"},
//	   "domain": {"ndns": true},
//	   "ssl": {"redirect": true},
//	   "security-level": {"public": true},
//	   "preserve-host": true,
//	   "auth": true}}
//
// Auth means the ROUTER holds authentication in front of the upstream.
type HTTPProxyEntry struct {
	Name          string `json:"name"`
	UpstreamProto string `json:"upstream_proto,omitempty"`
	UpstreamHost  string `json:"upstream_host,omitempty"`
	UpstreamPort  string `json:"upstream_port,omitempty"`
	DomainNDNS    bool   `json:"domain_ndns,omitempty"`
	StaticDomain  string `json:"static_domain,omitempty"`
	SSLRedirect   bool   `json:"ssl_redirect,omitempty"`
	Public        bool   `json:"public,omitempty"`
	Private       bool   `json:"private,omitempty"`
	Auth          bool   `json:"auth,omitempty"`
	PreserveHost  bool   `json:"preserve_host,omitempty"`
	RawJSON       string `json:"raw_json,omitempty"`
}

// HTTPProxyRuntimeEntry represents one active entry from `/show/ip/http/proxy`.
type HTTPProxyRuntimeEntry struct {
	Name     string `json:"name"`
	FQDN     string `json:"fqdn"`
	Proto    string `json:"proto"`
	Upstream string `json:"upstream"`
	Host     string `json:"host"`
	Allow    string `json:"allow"`
	NDNS     bool   `json:"ndns"`
}

// HTTPProxyConfigSnapshot holds top-level HTTP server status and all configured proxy entries from `/show/rc/ip/http`.
type HTTPProxyConfigSnapshot struct {
	SSLEnabled    bool             `json:"ssl_enabled"`
	SSLPort       string           `json:"ssl_port,omitempty"`
	PublicEnabled bool             `json:"public_enabled"`
	Entries       []HTTPProxyEntry `json:"entries"`
}

// HTTPProxyStore caches /show/rc/ip/http and provides uncached reads for transactional KeenDNS operations.
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
	snap, err := s.FetchConfigUncached(ctx)
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return nil, nil
	}
	return snap.Entries, nil
}

// FetchConfigUncached reads `/show/rc/ip/http` directly from NDMS without cache.
func (s *HTTPProxyStore) FetchConfigUncached(ctx context.Context) (*HTTPProxyConfigSnapshot, error) {
	if s == nil || s.getter == nil {
		return nil, errors.New("http proxy store not initialized")
	}
	raw, err := s.getter.GetRaw(ctx, "/show/rc/ip/http")
	if err != nil {
		// 404 = subsystem absent on this firmware, not a failure.
		var httpErr *transport.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("fetch ip http: %w", err)
	}
	return parseHTTPProxySnapshot(raw)
}

// FetchRuntimeUncached reads `/show/ip/http/proxy` directly from NDMS without cache.
func (s *HTTPProxyStore) FetchRuntimeUncached(ctx context.Context) (map[string]HTTPProxyRuntimeEntry, error) {
	if s == nil || s.getter == nil {
		return nil, errors.New("http proxy store not initialized")
	}
	raw, err := s.getter.GetRaw(ctx, "/show/ip/http/proxy")
	if err != nil {
		var httpErr *transport.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("fetch runtime ip http proxy: %w", err)
	}
	return parseHTTPRuntimeProxies(raw)
}

func parseHTTPProxies(raw []byte) ([]HTTPProxyEntry, error) {
	snap, err := parseHTTPProxySnapshot(raw)
	if err != nil || snap == nil {
		return nil, err
	}
	return snap.Entries, nil
}

func parseAuthField(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var obj struct {
		No bool `json:"no"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return !obj.No
	}
	return false
}

func parseHTTPProxySnapshot(raw []byte) (*HTTPProxyConfigSnapshot, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v struct {
		SecurityLevel struct {
			Public bool `json:"public"`
		} `json:"security-level"`
		SSL struct {
			Enable bool   `json:"enable"`
			Port   string `json:"port"`
		} `json:"ssl"`
		Proxy map[string]json.RawMessage `json:"proxy"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("decode ip http: %w", err)
	}

	type rawProxyItem struct {
		Upstream struct {
			Proto    string `json:"proto"`
			Upstream string `json:"upstream"`
			Port     string `json:"port"`
		} `json:"upstream"`
		Domain struct {
			NDNS   bool   `json:"ndns"`
			Static string `json:"static"`
		} `json:"domain"`
		SSL struct {
			Redirect bool `json:"redirect"`
		} `json:"ssl"`
		SecurityLevel struct {
			Public  bool `json:"public"`
			Private bool `json:"private"`
		} `json:"security-level"`
		PreserveHost bool            `json:"preserve-host"`
		Auth         json.RawMessage `json:"auth"`
	}

	names := make([]string, 0, len(v.Proxy))
	for name := range v.Proxy {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]HTTPProxyEntry, 0, len(v.Proxy))
	for _, name := range names {
		rawItem := v.Proxy[name]
		var p rawProxyItem
		if err := json.Unmarshal(rawItem, &p); err != nil {
			return nil, fmt.Errorf("decode ip http proxy %s: %w", name, err)
		}
		entries = append(entries, HTTPProxyEntry{
			Name:          name,
			UpstreamProto: strings.ToLower(strings.TrimSpace(p.Upstream.Proto)),
			UpstreamHost:  strings.TrimSpace(p.Upstream.Upstream),
			UpstreamPort:  strings.TrimSpace(p.Upstream.Port),
			DomainNDNS:    p.Domain.NDNS,
			StaticDomain:  strings.TrimSpace(p.Domain.Static),
			SSLRedirect:   p.SSL.Redirect,
			Public:        p.SecurityLevel.Public,
			Private:       p.SecurityLevel.Private,
			Auth:          parseAuthField(p.Auth),
			PreserveHost:  p.PreserveHost,
			RawJSON:       strings.TrimSpace(string(rawItem)),
		})
	}
	return &HTTPProxyConfigSnapshot{
		SSLEnabled:    v.SSL.Enable,
		SSLPort:       v.SSL.Port,
		PublicEnabled: v.SecurityLevel.Public,
		Entries:       entries,
	}, nil
}

func parseHTTPRuntimeProxies(raw []byte) (map[string]HTTPProxyRuntimeEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var m map[string]struct {
		FQDN     string `json:"fqdn"`
		Proto    string `json:"proto"`
		Upstream string `json:"upstream"`
		Host     string `json:"host"`
		Allow    string `json:"allow"`
		NDNS     bool   `json:"ndns"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode runtime ip http proxy: %w", err)
	}
	out := make(map[string]HTTPProxyRuntimeEntry, len(m))
	for name, item := range m {
		out[name] = HTTPProxyRuntimeEntry{
			Name:     name,
			FQDN:     strings.TrimSpace(item.FQDN),
			Proto:    strings.TrimSpace(item.Proto),
			Upstream: strings.TrimSpace(item.Upstream),
			Host:     strings.TrimSpace(item.Host),
			Allow:    strings.TrimSpace(item.Allow),
			NDNS:     item.NDNS,
		}
	}
	return out, nil
}
