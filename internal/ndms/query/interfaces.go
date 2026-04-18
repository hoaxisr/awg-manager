package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

const (
	// interfaceListTTL is the safety-net TTL for the full list — hooks
	// (ifcreated.d / ifdestroyed.d, Plan 4) are the primary freshness
	// mechanism.
	interfaceListTTL = 30 * time.Minute
	// interfaceItemTTL is the safety-net TTL for single-name Get —
	// iflayerchanged.d / ifipchanged.d invalidate per name.
	interfaceItemTTL = 5 * time.Minute
)

// InterfaceStore caches /show/interface/ (list) and /show/interface/{name}
// (single item). Invalidation comes from NDMS hooks (Plan 4) and from
// command-after-write (Plan 3).
type InterfaceStore struct {
	getter Getter
	log    Logger

	list   *cache.TTL[struct{}, []ndms.Interface]
	listSF *cache.SingleFlight[struct{}, []ndms.Interface]

	items   *cache.TTL[string, *ndms.Interface]
	itemsSF *cache.SingleFlight[string, *ndms.Interface]

	// sys memoises ndmsID → kernel system name mappings. Kernel renames
	// are rare and only surface via NDMS events, so InvalidateAll drops
	// the memo along with the other caches.
	sysMu sync.RWMutex
	sys   map[string]string
}

// NewInterfaceStore constructs a store with production TTLs.
func NewInterfaceStore(g Getter, log Logger) *InterfaceStore {
	return NewInterfaceStoreWithTTL(g, log, interfaceListTTL, interfaceItemTTL)
}

// NewInterfaceStoreWithTTL constructs an InterfaceStore with custom TTLs.
// Used only by tests; production code uses NewInterfaceStore.
func NewInterfaceStoreWithTTL(g Getter, log Logger, listTTL, itemTTL time.Duration) *InterfaceStore {
	if log == nil {
		log = NopLogger()
	}
	return &InterfaceStore{
		getter:  g,
		log:     log,
		list:    cache.NewTTL[struct{}, []ndms.Interface](listTTL),
		listSF:  cache.NewSingleFlight[struct{}, []ndms.Interface](),
		items:   cache.NewTTL[string, *ndms.Interface](itemTTL),
		itemsSF: cache.NewSingleFlight[string, *ndms.Interface](),
		sys:     make(map[string]string),
	}
}

// List returns every interface. Uses cache; stale-ok on error.
func (s *InterfaceStore) List(ctx context.Context) ([]ndms.Interface, error) {
	if v, ok := s.list.Get(struct{}{}); ok {
		return v, nil
	}
	return s.listSF.Do(struct{}{}, func() ([]ndms.Interface, error) {
		v, err := s.fetchList(ctx)
		if err != nil {
			if stale, ok := s.list.Peek(struct{}{}); ok {
				s.log.Warnf("interface list fetch failed, serving stale cache: %v", err)
				return stale, nil
			}
			return nil, err
		}
		s.list.Set(struct{}{}, v)
		return v, nil
	})
}

// Get returns a single interface by NDMS name. Cached; stale-ok on error.
// Returns (nil, nil) when NDMS responds with an empty body (interface
// does not exist).
func (s *InterfaceStore) Get(ctx context.Context, name string) (*ndms.Interface, error) {
	if v, ok := s.items.Get(name); ok {
		return v, nil
	}
	return s.itemsSF.Do(name, func() (*ndms.Interface, error) {
		v, err := s.fetchItem(ctx, name)
		if err != nil {
			if stale, ok := s.items.Peek(name); ok {
				s.log.Warnf("interface %s fetch failed, serving stale cache: %v", name, err)
				return stale, nil
			}
			return nil, err
		}
		s.items.Set(name, v)
		return v, nil
	})
}

// GetProxy is the Proxy-typed view of Get. It always returns a non-nil
// ProxyInfo (even for absent interfaces), with Exists=false when the
// interface doesn't exist in NDMS. This matches the contract the
// singbox.ProxyManager relies on (Plan 5 migration).
func (s *InterfaceStore) GetProxy(ctx context.Context, name string) (*ndms.ProxyInfo, error) {
	iface, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if iface == nil {
		return &ndms.ProxyInfo{Name: name, Exists: false}, nil
	}
	return &ndms.ProxyInfo{
		Name:        iface.ID,
		Type:        iface.Type,
		Description: iface.Description,
		State:       iface.State,
		Link:        iface.Link,
		Up:          iface.State == "up",
		Exists:      true,
	}, nil
}

// InvalidateAll drops the list cache (the per-item cache is untouched —
// hooks for a specific name will invalidate items individually).
func (s *InterfaceStore) InvalidateAll() {
	s.list.InvalidateAll()
	s.sysMu.Lock()
	s.sys = make(map[string]string)
	s.sysMu.Unlock()
}

// Invalidate drops the cache for a single interface name. The list cache
// is left untouched — the hook that triggered this invalidation may also
// invalidate the list if the add/remove event warrants it.
func (s *InterfaceStore) Invalidate(name string) {
	s.items.Invalidate(name)
}

// --- internal helpers ---

func (s *InterfaceStore) fetchList(ctx context.Context) ([]ndms.Interface, error) {
	var raw map[string]json.RawMessage
	if err := s.getter.Get(ctx, "/show/interface/", &raw); err != nil {
		return nil, fmt.Errorf("fetch interface list: %w", err)
	}
	out := make([]ndms.Interface, 0, len(raw))
	for id, data := range raw {
		iface, err := parseInterface(id, data)
		if err != nil {
			// Tolerate a bad entry — don't fail the whole list.
			s.log.Warnf("parse interface %s: %v", id, err)
			continue
		}
		out = append(out, iface)
	}
	return out, nil
}

func (s *InterfaceStore) fetchItem(ctx context.Context, name string) (*ndms.Interface, error) {
	raw, err := s.getter.GetRaw(ctx, "/show/interface/"+name)
	if err != nil {
		return nil, fmt.Errorf("fetch interface %s: %w", name, err)
	}
	// NDMS returns HTTP 200 with an empty body when the interface doesn't exist.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var w ifaceWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("parse interface %s: %w", name, err)
	}
	if w.ID == "" && w.InterfaceName == "" {
		return nil, nil
	}
	if w.ID == "" {
		w.ID = name
	}
	iface := wireToInterface(w)
	return &iface, nil
}

// ifaceWire is the shape /show/interface/ returns per entry.
type ifaceWire struct {
	ID            string `json:"id"`
	InterfaceName string `json:"interface-name"`
	Type          string `json:"type"`
	Description   string `json:"description"`
	State         string `json:"state"`
	Link          string `json:"link"`
	Connected     string `json:"connected"`
	SecurityLevel string `json:"security-level"`
	Address       string `json:"address"`
	Mask          string `json:"mask"`
	MTU           int    `json:"mtu"`
	Uptime        int64  `json:"uptime"`
	ConfLayer     string `json:"conf-layer"`
	Priority      int    `json:"priority"`
	Summary       struct {
		Layer struct {
			IPv4 string `json:"ipv4"`
		} `json:"layer"`
	} `json:"summary"`
}

func parseInterface(id string, data json.RawMessage) (ndms.Interface, error) {
	var w ifaceWire
	if err := json.Unmarshal(data, &w); err != nil {
		return ndms.Interface{}, err
	}
	if w.ID == "" {
		w.ID = id
	}
	return wireToInterface(w), nil
}

func wireToInterface(w ifaceWire) ndms.Interface {
	return ndms.Interface{
		ID:            w.ID,
		SystemName:    w.InterfaceName,
		Type:          w.Type,
		Description:   w.Description,
		State:         w.State,
		Link:          w.Link,
		Connected:     w.Connected,
		SecurityLevel: w.SecurityLevel,
		IPv4:          w.Summary.Layer.IPv4,
		Address:       w.Address,
		Mask:          w.Mask,
		MTU:           w.MTU,
		Uptime:        w.Uptime,
		ConfLayer:     w.ConfLayer,
		Priority:      w.Priority,
	}
}

// GetDetails fetches "show interface" and parses into InterfaceDetails.
// Returns (nil, nil) when NDMS responds with empty body (interface absent).
// Does NOT cache — state.Manager reads this per tunnel-state decision,
// too hot for the list cache, and the data is timing-sensitive anyway
// (uptime advances every second). Future optimisation: add a tiny
// invalidate-on-hook cache if state decisions become hot.
func (s *InterfaceStore) GetDetails(ctx context.Context, name string) (*ndms.InterfaceDetails, error) {
	raw, err := s.getter.GetRaw(ctx, "/show/interface/"+name)
	if err != nil {
		return nil, fmt.Errorf("fetch interface %s: %w", name, err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	return parseInterfaceDetails(raw)
}

// HasIPv6Global returns true when the interface has a global IPv6 address
// configured. Uses a targeted GetRaw + JSON parse; not cached (low hit
// rate, used at WAN event time).
func (s *InterfaceStore) HasIPv6Global(ctx context.Context, name string) bool {
	raw, err := s.getter.GetRaw(ctx, "/show/interface/"+name)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return false
	}
	var probe struct {
		IPv6 struct {
			Addresses []struct {
				Global bool `json:"global"`
			} `json:"addresses"`
		} `json:"ipv6"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	for _, a := range probe.IPv6.Addresses {
		if a.Global {
			return true
		}
	}
	return false
}

// ResolveSystemName returns the kernel interface name (e.g. "nwg0") for
// an NDMS id (e.g. "Wireguard0"). Returns "" on error or unresolvable
// input. Result is memoised (kernel renames are rare and surface via
// InvalidateAll) so repeated lookups across the query layer don't each
// re-hit NDMS.
func (s *InterfaceStore) ResolveSystemName(ctx context.Context, ndmsName string) string {
	if ndmsName == "" {
		return ""
	}
	s.sysMu.RLock()
	cached, ok := s.sys[ndmsName]
	s.sysMu.RUnlock()
	if ok {
		return cached
	}
	resolved := s.fetchSystemName(ctx, ndmsName)
	if resolved != "" {
		s.sysMu.Lock()
		s.sys[ndmsName] = resolved
		s.sysMu.Unlock()
	}
	return resolved
}

func (s *InterfaceStore) fetchSystemName(ctx context.Context, ndmsName string) string {
	raw, err := s.getter.GetRaw(ctx, "/show/interface/system-name?name="+ndmsName)
	if err != nil {
		return ""
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	// NDMS may return either a bare JSON string ("nwg0") or an object
	// ({"result":"nwg0"}). Try string first, then object.
	if trimmed[0] == '"' {
		var str string
		if json.Unmarshal(trimmed, &str) == nil {
			return str
		}
	}
	if trimmed[0] == '{' {
		var resp struct {
			Result string `json:"result"`
		}
		if json.Unmarshal(trimmed, &resp) == nil {
			return resp.Result
		}
	}
	return ""
}

// detailsWire mirrors the /show/interface/{name} JSON subset needed for
// InterfaceDetails.
type detailsWire struct {
	State     string `json:"state"`
	Link      string `json:"link"`
	Connected string `json:"connected"`
	Uptime    int    `json:"uptime"`
	Summary   struct {
		Layer struct {
			Conf string `json:"conf"`
		} `json:"layer"`
	} `json:"summary"`
}

func parseInterfaceDetails(raw []byte) (*ndms.InterfaceDetails, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty input")
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("unexpected format (not JSON)")
	}
	var w detailsWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("parse interface details: %w", err)
	}
	if w.State == "" {
		return nil, fmt.Errorf("no state field")
	}
	return &ndms.InterfaceDetails{
		State:     w.State,
		Link:      w.Link,
		Connected: w.Connected == "yes",
		ConfLayer: w.Summary.Layer.Conf,
		Uptime:    w.Uptime,
	}, nil
}


// ListWAN returns public-facing WAN interfaces filtered for ISP use.
// Filters: SecurityLevel == "public", excludes VPN/tunnel interfaces by
// kernel name (opkgtun/awg/nwg/wg/wireguard/ipsec/sstp/openvpn/proxy).
// Kernel name resolved via ResolveSystemName. Returns wan.Interface records.
func (s *InterfaceStore) ListWAN(ctx context.Context) ([]wan.Interface, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]wan.Interface, 0, len(all))
	for _, iface := range all {
		if iface.SecurityLevel != "public" {
			continue
		}
		if IsNonISPInterface(iface.SystemName) {
			continue
		}
		kernelName := s.ResolveSystemName(ctx, iface.ID)
		if kernelName == "" {
			kernelName = iface.SystemName
		}
		out = append(out, wan.Interface{
			Name:     kernelName,
			ID:       iface.ID,
			Label:    wanInterfaceLabel(iface.Type, iface.SystemName, iface.Description),
			Up:       iface.State == "up" && iface.IPv4 == "running",
			Priority: iface.Priority,
		})
	}
	return out, nil
}

// ListAll returns ALL router interfaces (no security-level filter).
// Drops our own managed tunnels (opkgtun*, awgm*). Intended for the
// "choose interface" UI. Returned shape matches the legacy AllInterface.
func (s *InterfaceStore) ListAll(ctx context.Context) ([]ndms.AllInterface, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ndms.AllInterface, 0, len(all))
	for _, iface := range all {
		if iface.SystemName == "" {
			continue
		}
		if isOwnTunnel(iface.SystemName) {
			continue
		}
		kernelName := s.ResolveSystemName(ctx, iface.ID)
		if kernelName == "" {
			kernelName = iface.SystemName
		}
		out = append(out, ndms.AllInterface{
			Name:  kernelName,
			Label: allInterfaceLabel(iface.Type, iface.SystemName, iface.Description),
			Up:    iface.State == "up" && iface.IPv4 == "running",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// IsNonISPInterface returns true for VPN/tunnel interface kernel names.
// These should not be treated as WAN regardless of security-level.
// Only excludes protocols that are NEVER used by ISPs:
//   - opkgtun/awg: our own managed tunnels
//   - wireguard/nwg/wg: WireGuard (Keenetic native or third-party)
//   - ipsec/sstp/openvpn: pure VPN protocols
//   - proxy: Keenetic proxy interfaces (t2s), depend on underlying WAN
//
// NOT excluded (ISPs do use these): PPTP, L2TP, GRE, IPIP, EoIP, PPPoE, IPoE.
func IsNonISPInterface(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "opkgtun") ||
		strings.HasPrefix(n, "awg") ||
		strings.HasPrefix(n, "nwg") ||
		strings.HasPrefix(n, "wg") ||
		strings.HasPrefix(n, "wireguard") ||
		strings.HasPrefix(n, "ipsec") ||
		strings.HasPrefix(n, "sstp") ||
		strings.HasPrefix(n, "openvpn") ||
		strings.HasPrefix(n, "proxy")
}

// isOwnTunnel returns true for interfaces owned by awg-manager itself
// (kernel names: opkgtun*, awgm*). Only excludes our tunnels, not other
// VPNs (user might want to route through them).
func isOwnTunnel(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "opkgtun") || strings.HasPrefix(n, "awgm")
}

// wanInterfaceLabel builds a human-readable label for the WAN interface list.
// If NDMS has a user-set description, it's used as the label.
// Otherwise, a label is generated from the interface type.
func wanInterfaceLabel(ifaceType, kernelName, description string) string {
	if description != "" && description != kernelName {
		return description
	}
	switch ifaceType {
	case "WifiStation":
		if strings.HasPrefix(kernelName, "WifiMaster1") {
			return "Wi-Fi клиент 5 ГГц"
		}
		return "Wi-Fi клиент 2.4 ГГц"
	case "GigabitEthernet":
		return "Ethernet"
	case "FastEthernet":
		return "Ethernet"
	case "PPPoE":
		return "PPPoE"
	case "PPTP":
		return "PPTP"
	case "L2TP":
		return "L2TP"
	case "IPoE":
		return "IPoE"
	case "UsbModem", "CdcEthernet", "UsbLte", "UsbQmi":
		return "USB-модем"
	case "Vlan":
		return "VLAN"
	}
	return kernelName
}

// allInterfaceLabel generates a label for any router interface.
func allInterfaceLabel(ifaceType, kernelName, description string) string {
	if description != "" && description != kernelName {
		return description
	}
	switch ifaceType {
	case "Bridge":
		return "Bridge"
	case "Loopback":
		return "Loopback"
	case "GigabitEthernet", "FastEthernet":
		return "Ethernet"
	case "WifiStation":
		if strings.HasPrefix(kernelName, "WifiMaster1") {
			return "Wi-Fi клиент 5 ГГц"
		}
		return "Wi-Fi клиент 2.4 ГГц"
	case "WifiMaster":
		return "Wi-Fi"
	case "PPPoE":
		return "PPPoE"
	case "PPTP":
		return "PPTP"
	case "L2TP":
		return "L2TP"
	case "IPoE":
		return "IPoE"
	case "UsbModem", "CdcEthernet", "UsbLte", "UsbQmi":
		return "USB-модем"
	case "Vlan":
		return "VLAN"
	}
	return kernelName
}
