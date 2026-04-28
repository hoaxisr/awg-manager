package query

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

const (
	// wgServerListTTL — safety-net TTL for the full list; hooks invalidate
	// on interface create/destroy.
	wgServerListTTL = 30 * time.Minute
	// wgServerItemTTL — per-name runtime snapshot TTL.
	wgServerItemTTL = 5 * time.Minute
	// wgServerRCTTL — RC-side config rarely changes.
	wgServerRCTTL = 10 * time.Minute

	// noHandshakeMarker: RCI sentinel for "no handshake ever".
	noHandshakeMarker = int64(math.MaxInt32) // 2147483647

	builtInVPNServerDescription = "Wireguard VPN Server"
)

// --- wire types (private) ----------------------------------------------------

// rciInterfaceInfo mirrors the subset of /show/interface/<name> fields we need.
type rciInterfaceInfo struct {
	State         string `json:"state"`
	Link          string `json:"link"`
	Connected     string `json:"connected"`
	InterfaceName string `json:"interface-name"`
	Type          string `json:"type"`
	Description   string `json:"description"`
	Address       string `json:"address"`
	Mask          string `json:"mask"`
}

// rciWireguardDetail is the runtime shape of /show/interface/<name> for a
// WireGuard interface, adding the nested "wireguard" object.
type rciWireguardDetail struct {
	rciInterfaceInfo
	MTU       int   `json:"mtu"`
	Uptime    int64 `json:"uptime"`
	Wireguard *struct {
		PublicKey  string             `json:"public-key"`
		ListenPort int                `json:"listen-port"`
		Peer       []rciWireguardPeer `json:"peer"`
	} `json:"wireguard"`
}

type rciWireguardPeer struct {
	PublicKey             string `json:"public-key"`
	Description           string `json:"description"`
	RemoteEndpointAddress string `json:"remote-endpoint-address"`
	RemotePort            int    `json:"remote-port"`
	Via                   string `json:"via"`
	RxBytes               int64  `json:"rxbytes"`
	TxBytes               int64  `json:"txbytes"`
	LastHandshake         int64  `json:"last-handshake"`
	Online                bool   `json:"online"`
	Enabled               bool   `json:"enabled"`
}

// rciRCInterface is the static config shape of /show/rc/interface/<name>.
type rciRCInterface struct {
	Description string `json:"description"`
	IP          *struct {
		Address *struct {
			Address string `json:"address"`
			Mask    string `json:"mask"`
		} `json:"address"`
		MTU string `json:"mtu"`
	} `json:"ip"`
	Wireguard *struct {
		ListenPort *struct {
			Port int `json:"port"`
		} `json:"listen-port"`
		Peer []rciRCPeer `json:"peer"`
	} `json:"wireguard"`
}

type rciRCPeer struct {
	Key          string `json:"key"`
	Comment      string `json:"comment"`
	PresharedKey string `json:"preshared-key"`
	AllowIPs     []struct {
		Address string `json:"address"`
		Mask    string `json:"mask"`
	} `json:"allow-ips"`
}

// --- store -------------------------------------------------------------------

// WGServerStore caches WG-server views derived from /show/interface/ and
// /show/rc/interface/<name>. Invalidation comes from NDMS hooks and
// command-after-write callers.
type WGServerStore struct {
	*cache.ListStore[[]ndms.WireguardServer]

	getter     Getter
	log        Logger
	interfaces *InterfaceStore // for ResolveSystemName (memoised)

	// per-name server snapshot (runtime only).
	items   *cache.TTL[string, *ndms.WireguardServer]
	itemsSF *cache.SingleFlight[string, *ndms.WireguardServer]

	// per-name RC config.
	rc   *cache.TTL[string, *ndms.WireguardServerConfig]
	rcSF *cache.SingleFlight[string, *ndms.WireguardServerConfig]

	// ASC params (raw JSON, per-name, encoded for the caller-selected shape).
	asc   *cache.TTL[string, json.RawMessage]
	ascSF *cache.SingleFlight[string, json.RawMessage]
}

// NewWGServerStore constructs the store with production TTLs. Takes
// InterfaceStore so kernel-name resolution shares a single memo across
// the query layer.
func NewWGServerStore(g Getter, log Logger, ifaces *InterfaceStore) *WGServerStore {
	return NewWGServerStoreWithTTL(g, log, ifaces, wgServerListTTL, wgServerItemTTL, wgServerRCTTL)
}

// NewWGServerStoreWithTTL is the test-friendly constructor.
func NewWGServerStoreWithTTL(g Getter, log Logger, ifaces *InterfaceStore, listTTL, itemTTL, rcTTL time.Duration) *WGServerStore {
	if log == nil {
		log = NopLogger()
	}
	s := &WGServerStore{
		getter:     g,
		log:        log,
		interfaces: ifaces,
		items:      cache.NewTTL[string, *ndms.WireguardServer](itemTTL),
		itemsSF:    cache.NewSingleFlight[string, *ndms.WireguardServer](),
		rc:         cache.NewTTL[string, *ndms.WireguardServerConfig](rcTTL),
		rcSF:       cache.NewSingleFlight[string, *ndms.WireguardServerConfig](),
		asc:        cache.NewTTL[string, json.RawMessage](rcTTL),
		ascSF:      cache.NewSingleFlight[string, json.RawMessage](),
	}
	s.ListStore = cache.NewListStore(listTTL, log, "wg server list", s.fetchAll)
	return s
}

// Get returns a single WG server's runtime snapshot.
func (s *WGServerStore) Get(ctx context.Context, name string) (*ndms.WireguardServer, error) {
	if v, ok := s.items.Get(name); ok {
		return v, nil
	}
	return s.itemsSF.Do(name, func() (*ndms.WireguardServer, error) {
		v, err := s.fetchItem(ctx, name)
		if err != nil {
			if stale, ok := s.items.Peek(name); ok {
				s.log.Warnf("wg server %s fetch failed, serving stale cache: %v", name, err)
				return stale, nil
			}
			return nil, err
		}
		s.items.Set(name, v)
		return v, nil
	})
}

// GetConfig returns the merged (runtime + RC) WG server config.
func (s *WGServerStore) GetConfig(ctx context.Context, name string) (*ndms.WireguardServerConfig, error) {
	if v, ok := s.rc.Get(name); ok {
		return v, nil
	}
	return s.rcSF.Do(name, func() (*ndms.WireguardServerConfig, error) {
		v, err := s.fetchConfig(ctx, name)
		if err != nil {
			if stale, ok := s.rc.Peek(name); ok {
				s.log.Warnf("wg server config %s fetch failed, serving stale cache: %v", name, err)
				return stale, nil
			}
			return nil, err
		}
		s.rc.Set(name, v)
		return v, nil
	})
}

// FindFreeIndex returns the next free WireguardN slot in [1,99].
func (s *WGServerStore) FindFreeIndex(ctx context.Context) (int, error) {
	var raw map[string]json.RawMessage
	if err := s.getter.Get(ctx, "/show/interface/", &raw); err != nil {
		return 0, fmt.Errorf("list interfaces: %w", err)
	}
	used := make(map[int]bool)
	for name := range raw {
		if strings.HasPrefix(name, "Wireguard") {
			if n, err := strconv.Atoi(strings.TrimPrefix(name, "Wireguard")); err == nil {
				used[n] = true
			}
		}
	}
	for i := 1; i < 100; i++ {
		if !used[i] {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no free Wireguard index found")
}

// GetASCParams returns the AWG obfuscation params for name. If extended is
// true, fields are encoded as the 16-field ASCParamsExtended shape, else as
// the 9-field ASCParams shape. The caller is responsible for the firmware
// gate (e.g. osdetect.AtLeast(5, 1)).
func (s *WGServerStore) GetASCParams(ctx context.Context, name string, extended bool) (json.RawMessage, error) {
	key := name + ":"
	if extended {
		key += "ext"
	} else {
		key += "base"
	}
	if v, ok := s.asc.Get(key); ok {
		return v, nil
	}
	return s.ascSF.Do(key, func() (json.RawMessage, error) {
		v, err := s.fetchASC(ctx, name, extended)
		if err != nil {
			if stale, ok := s.asc.Peek(key); ok {
				s.log.Warnf("wg asc %s fetch failed, serving stale cache: %v", name, err)
				return stale, nil
			}
			return nil, err
		}
		s.asc.Set(key, v)
		return v, nil
	})
}

// ListSystemTunnels returns all system WG tunnels (excluding the built-in VPN server).
func (s *WGServerStore) ListSystemTunnels(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	var raw map[string]json.RawMessage
	if err := s.getter.Get(ctx, "/show/interface/", &raw); err != nil {
		return nil, fmt.Errorf("list system wireguard: %w", err)
	}
	var tunnels []ndms.SystemWireguardTunnel
	for id, data := range raw {
		var typeCheck struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(data, &typeCheck); err != nil {
			continue
		}
		if !strings.EqualFold(typeCheck.Type, "Wireguard") {
			continue
		}
		if typeCheck.Description == builtInVPNServerDescription {
			continue
		}
		var detail rciWireguardDetail
		if err := json.Unmarshal(data, &detail); err != nil {
			continue
		}
		if detail.ID() == "" {
			detail.InterfaceName = id
		}
		t := rciToSystemTunnel(detail)
		t.ID = id
		t.InterfaceName = s.resolveSystemName(ctx, id)
		tunnels = append(tunnels, t)
	}
	sort.Slice(tunnels, func(i, j int) bool { return tunnels[i].ID < tunnels[j].ID })
	return tunnels, nil
}

// GetSystemTunnel returns a single system-tunnel view.
func (s *WGServerStore) GetSystemTunnel(ctx context.Context, name string) (*ndms.SystemWireguardTunnel, error) {
	var detail rciWireguardDetail
	if err := s.getter.Get(ctx, "/show/interface/"+name, &detail); err != nil {
		return nil, fmt.Errorf("get system wireguard %s: %w", name, err)
	}
	t := rciToSystemTunnel(detail)
	t.ID = name
	t.InterfaceName = s.resolveSystemName(ctx, name)
	return &t, nil
}

// Invalidate drops caches for a single server name (runtime, rc, asc)
// AND the aggregate list cache — otherwise GetAll would keep returning
// a stale peer list after a per-server mutation until the list TTL
// expires.
func (s *WGServerStore) Invalidate(name string) {
	s.items.Invalidate(name)
	s.rc.Invalidate(name)
	s.asc.Invalidate(name + ":ext")
	s.asc.Invalidate(name + ":base")
	s.ListStore.InvalidateAll()
}

// InvalidateAll drops every cached entry across all keyspaces. Kernel
// system-name memo is owned by InterfaceStore — callers that need a
// full hot-plug reset should invalidate both stores. Shadows the
// promoted ListStore.InvalidateAll so the per-name keyed caches are
// reset alongside the list cache.
func (s *WGServerStore) InvalidateAll() {
	s.ListStore.InvalidateAll()
	s.items.InvalidateAll()
	s.rc.InvalidateAll()
	s.asc.InvalidateAll()
}

// --- fetchers ---------------------------------------------------------------

func (s *WGServerStore) fetchAll(ctx context.Context) ([]ndms.WireguardServer, error) {
	var raw map[string]json.RawMessage
	if err := s.getter.Get(ctx, "/show/interface/", &raw); err != nil {
		return nil, fmt.Errorf("list wireguard servers: %w", err)
	}
	var servers []ndms.WireguardServer
	for id, data := range raw {
		var typeCheck struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &typeCheck); err != nil {
			continue
		}
		if !strings.EqualFold(typeCheck.Type, "Wireguard") {
			continue
		}
		var detail rciWireguardDetail
		if err := json.Unmarshal(data, &detail); err != nil {
			continue
		}
		srv := rciToWireguardServer(detail)
		srv.ID = id
		srv.InterfaceName = s.resolveSystemName(ctx, id)
		servers = append(servers, srv)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].ID < servers[j].ID })

	// Enrich peers with AllowedIPs from RC in parallel. Transport-layer
	// semaphore bounds concurrency; we only coordinate completion.
	if len(servers) > 0 {
		var wg sync.WaitGroup
		type rcResult struct {
			idx int
			rc  rciRCInterface
			err error
		}
		results := make(chan rcResult, len(servers))
		for i := range servers {
			wg.Add(1)
			go func(idx int, name string) {
				defer wg.Done()
				var rc rciRCInterface
				err := s.getter.Get(ctx, "/show/rc/interface/"+name, &rc)
				results <- rcResult{idx: idx, rc: rc, err: err}
			}(i, servers[i].ID)
		}
		go func() { wg.Wait(); close(results) }()
		for r := range results {
			if r.err != nil || r.rc.Wireguard == nil {
				continue
			}
			allowedByKey := make(map[string][]string)
			for _, rp := range r.rc.Wireguard.Peer {
				var ips []string
				for _, a := range rp.AllowIPs {
					if a.Mask != "" {
						ips = append(ips, a.Address+"/"+a.Mask)
					} else {
						ips = append(ips, a.Address)
					}
				}
				allowedByKey[rp.Key] = ips
			}
			for j := range servers[r.idx].Peers {
				if ips, ok := allowedByKey[servers[r.idx].Peers[j].PublicKey]; ok {
					servers[r.idx].Peers[j].AllowedIPs = ips
				}
			}
		}
	}
	return servers, nil
}

func (s *WGServerStore) fetchItem(ctx context.Context, name string) (*ndms.WireguardServer, error) {
	var detail rciWireguardDetail
	if err := s.getter.Get(ctx, "/show/interface/"+name, &detail); err != nil {
		return nil, fmt.Errorf("get wireguard server %s: %w", name, err)
	}
	srv := rciToWireguardServer(detail)
	srv.ID = name
	srv.InterfaceName = s.resolveSystemName(ctx, name)
	return &srv, nil
}

func (s *WGServerStore) fetchConfig(ctx context.Context, name string) (*ndms.WireguardServerConfig, error) {
	// Runtime for public key.
	var detail rciWireguardDetail
	if err := s.getter.Get(ctx, "/show/interface/"+name, &detail); err != nil {
		return nil, fmt.Errorf("get wireguard server %s: %w", name, err)
	}
	var publicKey string
	if detail.Wireguard != nil {
		publicKey = detail.Wireguard.PublicKey
	}
	// Static config for peer details.
	var rc rciRCInterface
	if err := s.getter.Get(ctx, "/show/rc/interface/"+name, &rc); err != nil {
		return nil, fmt.Errorf("get wireguard server config %s: %w", name, err)
	}
	cfg := rciRCToServerConfig(rc, publicKey)
	return &cfg, nil
}

func (s *WGServerStore) fetchASC(ctx context.Context, name string, extended bool) (json.RawMessage, error) {
	var raw map[string]string
	path := "/show/rc/interface/" + name + "/wireguard/asc"
	if err := s.getter.Get(ctx, path, &raw); err != nil {
		return nil, fmt.Errorf("get ASC params %s: %w", name, err)
	}
	if extended {
		params := struct {
			Jc   int    `json:"jc"`
			Jmin int    `json:"jmin"`
			Jmax int    `json:"jmax"`
			S1   int    `json:"s1"`
			S2   int    `json:"s2"`
			H1   string `json:"h1"`
			H2   string `json:"h2"`
			H3   string `json:"h3"`
			H4   string `json:"h4"`
			S3   int    `json:"s3"`
			S4   int    `json:"s4"`
			I1   string `json:"i1"`
			I2   string `json:"i2"`
			I3   string `json:"i3"`
			I4   string `json:"i4"`
			I5   string `json:"i5"`
		}{
			Jc: atoiSafe(raw["jc"]), Jmin: atoiSafe(raw["jmin"]), Jmax: atoiSafe(raw["jmax"]),
			S1: atoiSafe(raw["s1"]), S2: atoiSafe(raw["s2"]),
			H1: raw["h1"], H2: raw["h2"], H3: raw["h3"], H4: raw["h4"],
			S3: atoiSafe(raw["s3"]), S4: atoiSafe(raw["s4"]),
			I1: raw["i1"], I2: raw["i2"], I3: raw["i3"], I4: raw["i4"], I5: raw["i5"],
		}
		return json.Marshal(params)
	}
	params := struct {
		Jc   int    `json:"jc"`
		Jmin int    `json:"jmin"`
		Jmax int    `json:"jmax"`
		S1   int    `json:"s1"`
		S2   int    `json:"s2"`
		H1   string `json:"h1"`
		H2   string `json:"h2"`
		H3   string `json:"h3"`
		H4   string `json:"h4"`
	}{
		Jc: atoiSafe(raw["jc"]), Jmin: atoiSafe(raw["jmin"]), Jmax: atoiSafe(raw["jmax"]),
		S1: atoiSafe(raw["s1"]), S2: atoiSafe(raw["s2"]),
		H1: raw["h1"], H2: raw["h2"], H3: raw["h3"], H4: raw["h4"],
	}
	return json.Marshal(params)
}

// resolveSystemName delegates to InterfaceStore so kernel-name resolution
// is memoised in one place. Preserves legacy fallback: if resolution
// fails or is empty, return the NDMS id unchanged.
func (s *WGServerStore) resolveSystemName(ctx context.Context, ndmsID string) string {
	if s.interfaces == nil {
		return ndmsID
	}
	if name := s.interfaces.ResolveSystemName(ctx, ndmsID); name != "" {
		return name
	}
	return ndmsID
}

// --- converters --------------------------------------------------------------

// ID returns the interface identifier from rciWireguardDetail. Present so that
// callers can detect empty decodes without reaching into the embedded struct.
func (d rciWireguardDetail) ID() string { return d.InterfaceName }

func formatPeerEndpoint(p rciWireguardPeer) string {
	if p.RemoteEndpointAddress == "" && p.RemotePort == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", p.RemoteEndpointAddress, p.RemotePort)
}

// FormatHandshakeSecondsAgo converts RCI last-handshake (seconds ago) to
// RFC3339 or "". Sentinels: <= 0 or >= MaxInt32 indicate "never".
func FormatHandshakeSecondsAgo(secsAgo int64) string {
	if secsAgo <= 0 || secsAgo >= noHandshakeMarker {
		return ""
	}
	return time.Now().Add(-time.Duration(secsAgo) * time.Second).Format(time.RFC3339)
}

func rciToSystemTunnel(iface rciWireguardDetail) ndms.SystemWireguardTunnel {
	t := ndms.SystemWireguardTunnel{
		ID:          iface.InterfaceName,
		Description: iface.Description,
		Status:      iface.State,
		Connected:   iface.Connected == "yes",
		MTU:         iface.MTU,
		Address:     iface.Address,
		Mask:        iface.Mask,
		Uptime:      iface.Uptime,
	}
	if iface.Wireguard != nil && len(iface.Wireguard.Peer) > 0 {
		peer := iface.Wireguard.Peer[0]
		t.Peer = &ndms.WireguardPeerInfo{
			PublicKey:     peer.PublicKey,
			Endpoint:      formatPeerEndpoint(peer),
			Via:           peer.Via,
			RxBytes:       peer.RxBytes,
			TxBytes:       peer.TxBytes,
			LastHandshake: FormatHandshakeSecondsAgo(peer.LastHandshake),
			Online:        peer.Online,
		}
	}
	return t
}

func rciToWireguardServer(iface rciWireguardDetail) ndms.WireguardServer {
	server := ndms.WireguardServer{
		ID:          iface.InterfaceName,
		Description: iface.Description,
		Status:      iface.State,
		Connected:   iface.Connected == "yes",
		MTU:         iface.MTU,
		Address:     iface.Address,
		Mask:        iface.Mask,
	}
	if iface.Wireguard != nil {
		server.PublicKey = iface.Wireguard.PublicKey
		server.ListenPort = iface.Wireguard.ListenPort
		for _, p := range iface.Wireguard.Peer {
			server.Peers = append(server.Peers, ndms.WireguardServerPeer{
				PublicKey:     p.PublicKey,
				Description:   p.Description,
				Endpoint:      formatPeerEndpoint(p),
				RxBytes:       p.RxBytes,
				TxBytes:       p.TxBytes,
				LastHandshake: FormatHandshakeSecondsAgo(p.LastHandshake),
				Online:        p.Online,
				Enabled:       p.Enabled,
			})
		}
	}
	return server
}

func rciRCToServerConfig(rc rciRCInterface, publicKey string) ndms.WireguardServerConfig {
	cfg := ndms.WireguardServerConfig{PublicKey: publicKey}
	if rc.IP != nil {
		if rc.IP.Address != nil {
			cfg.Address = rc.IP.Address.Address
		}
		if rc.IP.MTU != "" {
			fmt.Sscanf(rc.IP.MTU, "%d", &cfg.MTU)
		}
	}
	if rc.Wireguard != nil {
		if rc.Wireguard.ListenPort != nil {
			cfg.ListenPort = rc.Wireguard.ListenPort.Port
		}
		for _, p := range rc.Wireguard.Peer {
			peer := ndms.WireguardServerPeerConfig{
				PublicKey:    p.Key,
				Description:  p.Comment,
				PresharedKey: p.PresharedKey,
			}
			for _, aip := range p.AllowIPs {
				ones := ipMaskToPrefix(aip.Mask)
				if ones < 0 {
					continue
				}
				peer.AllowedIPs = append(peer.AllowedIPs, fmt.Sprintf("%s/%d", aip.Address, ones))
				if ones == 32 && peer.Address == "" {
					peer.Address = aip.Address
				}
			}
			cfg.Peers = append(cfg.Peers, peer)
		}
	}
	return cfg
}

// ipMaskToPrefix converts a dotted-quad mask (e.g. "255.255.255.255") to a
// prefix length. Returns -1 on parse failure.
func ipMaskToPrefix(mask string) int {
	ip := net.ParseIP(mask)
	if ip == nil {
		return -1
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return -1
	}
	ones, _ := net.IPMask(ip4).Size()
	return ones
}

func atoiSafe(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}
