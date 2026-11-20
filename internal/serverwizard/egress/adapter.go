package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/routing"
)

type EgressKind string

const (
	EgressKindDirect         EgressKind = "direct"
	EgressKindInterface      EgressKind = "interface"
	EgressKindSocks          EgressKind = "socks"
	EgressKindRouterOutbound EgressKind = "router_outbound"
)

const (
	EgressStateAvailable   = "available"
	EgressStateDegraded    = "degraded"
	EgressStateUnavailable = "unavailable"
	EgressStateUnsupported = "unsupported"
)

type EgressOption struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Iface             string     `json:"iface,omitempty"`
	Kind              EgressKind `json:"kind"`
	Owner             string     `json:"owner"`
	SupportedServices []string   `json:"supported_services,omitempty"`
	State             string     `json:"state,omitempty"`
	UnavailableReason string     `json:"unavailable_reason,omitempty"`
	Current           bool       `json:"current,omitempty"`
	Available         bool       `json:"available"`
	DegradedMsg       string     `json:"degraded_msg,omitempty"`
	SupportsTCP       bool       `json:"supports_tcp"`
	SupportsUDP       bool       `json:"supports_udp"`
	Generation        uint64     `json:"generation"`
}

type TunnelCatalog interface {
	ListAll(ctx context.Context) []routing.TunnelEntry
}

// KernelIfaceResolver resolves a catalog tunnel ID (e.g. "system:Wireguard1")
// to its OS kernel interface name (e.g. "nwg1"). Optional extension on TunnelCatalog.
type KernelIfaceResolver interface {
	GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error)
}

func (a *Adapter) hasKernelResolver() bool {
	if a == nil || a.catalog == nil {
		return false
	}
	_, ok := a.catalog.(KernelIfaceResolver)
	return ok
}

func (a *Adapter) resolveKernelIface(ctx context.Context, t routing.TunnelEntry) (string, error) {
	raw := strings.TrimSpace(t.Iface)
	if strings.EqualFold(strings.TrimSpace(t.Type), "firc") {
		return raw, nil
	}
	if kr, ok := a.catalog.(KernelIfaceResolver); ok && t.ID != "" {
		kname, err := kr.GetKernelIfaceName(ctx, t.ID)
		kname = strings.TrimSpace(kname)
		if err == nil && kname != "" {
			return kname, nil
		}
		if strings.EqualFold(strings.TrimSpace(t.Type), "system") {
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("kernel interface not resolved for %s", t.ID)
		}
	}
	return raw, nil
}

// Resolver resolves an egress request for a server kind into concrete outbound parameters.
type Resolver interface {
	Resolve(ctx context.Context, req ResolveRequest) (ResolvedEgress, error)
}

type Adapter struct {
	catalog           TunnelCatalog
	generation        atomic.Uint64
	socksCacheMu      sync.Mutex
	socksCachedAt     time.Time
	socksAvailable    bool
	dialTimeout       func(network, address string, timeout time.Duration) (net.Conn, error)
	ifaceLinkUp       func(iface string) (up bool, checked bool)
	fircMu            sync.RWMutex
	fircStateProvider func(ctx context.Context) (available bool, degradedMsg string)
	fircTunnels       func(ctx context.Context) []routing.TunnelEntry
}

func NewAdapter(catalog TunnelCatalog) *Adapter {
	a := &Adapter{
		catalog:     catalog,
		dialTimeout: net.DialTimeout,
	}
	a.generation.Store(1)
	return a
}

// SetFircStateProvider configures how the adapter checks FIRC availability.
func (a *Adapter) SetFircStateProvider(fn func(ctx context.Context) (available bool, degradedMsg string)) {
	a.fircMu.Lock()
	defer a.fircMu.Unlock()
	a.fircStateProvider = fn
}

// SetFircTunnels configures an optional provider of FIRC egress tunnels.
func (a *Adapter) SetFircTunnels(fn func(ctx context.Context) []routing.TunnelEntry) {
	a.fircMu.Lock()
	defer a.fircMu.Unlock()
	a.fircTunnels = fn
}

func (a *Adapter) tunnelEntries(ctx context.Context) []routing.TunnelEntry {
	a.fircMu.RLock()
	fircProvider := a.fircTunnels
	a.fircMu.RUnlock()

	var fircEntries []routing.TunnelEntry
	if fircProvider != nil {
		fircEntries = fircProvider(ctx)
	}

	var catalogEntries []routing.TunnelEntry
	if a.catalog != nil {
		catalogEntries = a.catalog.ListAll(ctx)
	}

	if len(fircEntries) == 0 {
		return catalogEntries
	}
	if len(catalogEntries) == 0 {
		return fircEntries
	}

	merged := make([]routing.TunnelEntry, 0, len(fircEntries)+len(catalogEntries))
	merged = append(merged, fircEntries...)
	merged = append(merged, catalogEntries...)
	return merged
}

// SetIfaceLinkUpProbe overrides kernel link state probing (used by tests).
func (a *Adapter) SetIfaceLinkUpProbe(fn func(iface string) (up bool, checked bool)) {
	a.ifaceLinkUp = fn
}

func (a *Adapter) checkKernelLinkUp(ifaceName string) (up bool, checked bool) {
	ifaceName = strings.TrimSpace(ifaceName)
	if ifaceName == "" {
		return false, false
	}
	if a.ifaceLinkUp != nil {
		return a.ifaceLinkUp(ifaceName)
	}
	if !a.hasKernelResolver() {
		return false, false
	}
	ifi, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return false, false
	}
	return (ifi.Flags & net.FlagUp) != 0, true
}

func (a *Adapter) checkFircAvailable(ctx context.Context) (bool, string) {
	a.fircMu.RLock()
	fn := a.fircStateProvider
	a.fircMu.RUnlock()
	if fn == nil {
		return false, "Провайдер состояния FIRC не подключён"
	}
	return fn(ctx)
}

// IsLegacyEgress returns true if the given egress ID or mode refers to the deprecated Mihomo SOCKS (:1099) path.
func IsLegacyEgress(idOrMode string) bool {
	return idOrMode == "mihomo:1099" || idOrMode == "socks"
}

// LegacyEgressLabel returns the user-facing status string for deprecated egress configs.
func LegacyEgressLabel() string {
	return "Устаревший выход — требуется выбор"
}

// SetDialTimeout allows customizing the dialer function for tests.
func (a *Adapter) SetDialTimeout(dialFn func(network, address string, timeout time.Duration) (net.Conn, error)) {
	a.socksCacheMu.Lock()
	defer a.socksCacheMu.Unlock()
	a.dialTimeout = dialFn
	a.socksCachedAt = time.Time{}
}

func (a *Adapter) checkSocksAvailable() bool {
	a.socksCacheMu.Lock()
	defer a.socksCacheMu.Unlock()

	if !a.socksCachedAt.IsZero() && time.Since(a.socksCachedAt) < 2*time.Second {
		return a.socksAvailable
	}

	dialFn := a.dialTimeout
	if dialFn == nil {
		dialFn = net.DialTimeout
	}

	conn, err := dialFn("tcp", "127.0.0.1:1099", 200*time.Millisecond)
	a.socksCachedAt = time.Now()
	if err == nil {
		_ = conn.Close()
		a.socksAvailable = true
	} else {
		a.socksAvailable = false
	}
	return a.socksAvailable
}

// BumpGeneration increments the generation counter when routing state changes.
func (a *Adapter) BumpGeneration() uint64 {
	return a.generation.Add(1)
}

// ListOptions aggregates all system, FIRC policy, and tunnel egress possibilities.
func (a *Adapter) ListOptions(ctx context.Context) []EgressOption {
	return a.ListOptionsForServer(ctx, "")
}

// ListOptionsForServer returns selectable egress options for a specific server kind.
// Legacy mihomo:1099 is never offered as a selectable option for new configurations.
func (a *Adapter) ListOptionsForServer(ctx context.Context, serverKind string) []EgressOption {
	return a.ListOptionsForServerWithCurrent(ctx, serverKind)
}

func isInternalServiceEntry(t routing.TunnelEntry) bool {
	if t.Server {
		return true
	}
	typ := strings.ToLower(strings.TrimSpace(t.Type))
	if typ == "wan" || typ == "direct" || typ == "block" || typ == "dns" || typ == "selector" || typ == "urltest" || typ == "fallback" || typ == "mihomo" {
		return true
	}
	id := strings.ToLower(strings.TrimSpace(t.ID))
	if id == "" || id == "direct" || id == "block" || id == "reject" || id == "dns" || id == "dns-out" || id == "global" || id == "auto" || id == "mihomo:1099" || id == "firc:router" || id == "router" {
		return true
	}
	if strings.HasPrefix(id, "wan:") || strings.HasPrefix(id, "dns:") || strings.HasPrefix(id, "selector:") || strings.HasPrefix(id, "internal:") {
		return true
	}
	return false
}

// ListOptionsForServerWithCurrent returns selectable egress options for a specific server kind,
// preserving any currently configured egress IDs even if they are unavailable, degraded, or legacy.
func (a *Adapter) ListOptionsForServerWithCurrent(ctx context.Context, serverKind string, currentEgressIDs ...string) []EgressOption {
	gen := a.generation.Load()
	options := make([]EgressOption, 0)

	currentSet := make(map[string]bool)
	for _, cid := range currentEgressIDs {
		trimmed := strings.TrimSpace(cid)
		if trimmed != "" {
			currentSet[trimmed] = true
		}
	}

	// 1. Direct WAN
	options = append(options, EgressOption{
		ID:                "direct",
		Name:              "Напрямую в интернет",
		Iface:             "direct",
		Kind:              EgressKindDirect,
		Owner:             "system",
		SupportedServices: []string{"xray", "tgwebproxy"},
		State:             EgressStateAvailable,
		Current:           currentSet["direct"],
		Available:         true,
		SupportsTCP:       true,
		SupportsUDP:       true,
		Generation:        gen,
	})

	// 2. FIRC Routing Rules (supported for VLESS XHTTP server, not for standalone tgwebproxy)
	if serverKind != "tgwebproxy" {
		fircOk, fircDegraded := a.checkFircAvailable(ctx)
		if !fircOk && fircDegraded == "" {
			fircDegraded = "Маршрутизация FIRC недоступна"
		}
		state := EgressStateAvailable
		if !fircOk {
			state = EgressStateUnavailable
		}
		isCurrent := currentSet["firc:router"] || currentSet["router"]
		if !fircOk && isCurrent {
			fircDegraded = fmt.Sprintf("Текущий выход недоступен — выберите другой или восстановите маршрутизацию (%s)", fircDegraded)
		}
		options = append(options, EgressOption{
			ID:                "firc:router",
			Name:              "По правилам роутера",
			Iface:             "router",
			Kind:              EgressKindRouterOutbound,
			Owner:             "firc",
			SupportedServices: []string{"xray"},
			State:             state,
			UnavailableReason: fircDegraded,
			Current:           isCurrent,
			Available:         fircOk,
			DegradedMsg:       fircDegraded,
			SupportsTCP:       true,
			SupportsUDP:       true,
			Generation:        gen,
		})
	}

	seenIDs := map[string]bool{
		"direct":      true,
		"firc:router": true,
		"router":      true,
	}
	seenIfaces := make(map[string]bool)

	// 3. Tunnels from FIRC and Catalog
	if tunnels := a.tunnelEntries(ctx); len(tunnels) != 0 {
		for _, t := range tunnels {
			rawIface := strings.TrimSpace(t.Iface)
			kernelIface, kerr := a.resolveKernelIface(ctx, t)
			if kerr == nil && kernelIface != "" {
				t.Iface = kernelIface
			} else if strings.EqualFold(strings.TrimSpace(t.Type), "system") && a.hasKernelResolver() {
				t.Iface = ""
				if t.Available {
					t.Available = false
					if t.Warning == "" {
						t.Warning = "нет интерфейса ядра"
					}
				}
			}
			sysShort := ""
			if strings.EqualFold(strings.TrimSpace(t.Type), "system") {
				sysShort = strings.TrimPrefix(t.ID, "system:")
			}
			isCurrent := currentSet[t.ID] ||
				(rawIface != "" && currentSet[rawIface]) ||
				(t.Iface != "" && currentSet[t.Iface]) ||
				(sysShort != "" && currentSet[sysShort])
			if isInternalServiceEntry(t) && !isCurrent {
				continue
			}
			if seenIDs[t.ID] || (t.Iface != "" && seenIfaces[t.Iface]) {
				continue
			}

			state := EgressStateAvailable
			degradedMsg := ""
			avail := t.Available
			if isInternalServiceEntry(t) {
				avail = false
				state = EgressStateUnsupported
				degradedMsg = "Текущий выход недоступен — серверный или служебный интерфейс не может использоваться для выхода в интернет"
			} else if !t.Available {
				state = EgressStateDegraded
				if isCurrent {
					degradedMsg = fmt.Sprintf("Текущий выход недоступен — выберите другой или восстановите туннель (статус: %s)", t.Status)
				} else {
					degradedMsg = fmt.Sprintf("Туннель недоступен (статус: %s)", t.Status)
				}
			} else if linkUp, checked := a.checkKernelLinkUp(t.Iface); checked && !linkUp {
				avail = false
				state = EgressStateDegraded
				if isCurrent {
					degradedMsg = fmt.Sprintf("Текущий выход недоступен — интерфейс %s остановлен (DOWN)", t.Iface)
				} else {
					degradedMsg = fmt.Sprintf("Интерфейс %s остановлен (DOWN)", t.Iface)
				}
			}

			name := strings.TrimSpace(t.Name)
			if name == "" {
				name = t.ID
			}

			seenIDs[t.ID] = true
			if t.Iface != "" {
				seenIfaces[t.Iface] = true
			}

			options = append(options, EgressOption{
				ID:                t.ID,
				Name:              name,
				Iface:             t.Iface,
				Kind:              EgressKindInterface,
				Owner:             t.Type,
				SupportedServices: []string{"xray", "tgwebproxy"},
				State:             state,
				UnavailableReason: degradedMsg,
				Current:           isCurrent,
				Available:         avail,
				DegradedMsg:       degradedMsg,
				SupportsTCP:       true,
				SupportsUDP:       true,
				Generation:        gen,
			})
		}
	}

	// 4. Preserve any currently configured egress not present in catalog (unavailable / deleted / legacy)
	for _, cid := range currentEgressIDs {
		cid = strings.TrimSpace(cid)
		if cid == "" || seenIDs[cid] || seenIfaces[cid] {
			continue
		}
		seenIDs[cid] = true
		if IsLegacyEgress(cid) {
			reason := "Текущий выход недоступен — устаревший режим прокси больше не поддерживается, выберите другой выход"
			options = append(options, EgressOption{
				ID:                cid,
				Name:              LegacyEgressLabel(),
				Iface:             cid,
				Kind:              EgressKindSocks,
				Owner:             "legacy",
				SupportedServices: []string{},
				State:             EgressStateUnsupported,
				UnavailableReason: reason,
				Current:           true,
				Available:         false,
				DegradedMsg:       reason,
				SupportsTCP:       false,
				SupportsUDP:       false,
				Generation:        gen,
			})
			continue
		}
		reason := "Текущий выход недоступен — выберите другой или восстановите туннель"
		options = append(options, EgressOption{
			ID:                cid,
			Name:              cid,
			Iface:             cid,
			Kind:              EgressKindInterface,
			Owner:             "missing",
			SupportedServices: []string{"xray", "tgwebproxy"},
			State:             EgressStateUnavailable,
			UnavailableReason: reason,
			Current:           true,
			Available:         false,
			DegradedMsg:       reason,
			SupportsTCP:       true,
			SupportsUDP:       true,
			Generation:        gen,
		})
	}

	// Sort deterministically: direct first, router_outbound second, then by Name, tie-breaking by ID
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].Kind != options[j].Kind {
			if options[i].Kind == EgressKindDirect {
				return true
			}
			if options[j].Kind == EgressKindDirect {
				return false
			}
			if options[i].Kind == EgressKindRouterOutbound {
				return true
			}
			if options[j].Kind == EgressKindRouterOutbound {
				return false
			}
		}
		if options[i].Name != options[j].Name {
			return options[i].Name < options[j].Name
		}
		return options[i].ID < options[j].ID
	})

	return options
}

// FindOption returns an egress option by ID or kernel interface name.
func (a *Adapter) FindOption(ctx context.Context, id string) (EgressOption, bool) {
	id = strings.TrimSpace(id)
	for _, opt := range a.ListOptions(ctx) {
		if opt.ID == id || opt.ID == "system:"+id || (opt.Iface != "" && opt.Iface == id) {
			return opt, true
		}
	}
	return EgressOption{}, false
}

// EgressSummary creates a deterministic summary string for state fingerprinting.
func (a *Adapter) EgressSummary(ctx context.Context) string {
	opts := a.ListOptions(ctx)
	sort.SliceStable(opts, func(i, j int) bool {
		if opts[i].ID != opts[j].ID {
			return opts[i].ID < opts[j].ID
		}
		return opts[i].Iface < opts[j].Iface
	})
	summary := fmt.Sprintf("gen:%d", a.generation.Load())
	for _, o := range opts {
		summary += fmt.Sprintf("|%s:%t:%s", o.ID, o.Available, o.DegradedMsg)
	}
	return summary
}

// ResolveRequest represents a typed egress resolution request.
type ResolveRequest struct {
	ServerKind string `json:"server_kind"` // "xray", "tgwebproxy", or "both"
	EgressID   string `json:"egress_id"`   // "direct", "firc:router", "nwg1", etc.
}

// ResolvedEgress holds normalized outbound mode and target parameters.
type ResolvedEgress struct {
	Mode      string `json:"mode"`       // "direct", "router", "interface"
	Interface string `json:"interface"`  // e.g. "nwg1"
	SocksPort int    `json:"socks_port"` // legacy compatibility only
}

// Resolve resolves an egress request for a target server kind into concrete outbound parameters.
func (a *Adapter) Resolve(ctx context.Context, req ResolveRequest) (ResolvedEgress, error) {
	serverKind := req.ServerKind
	egressID := req.EgressID
	if egressID == "" || egressID == "direct" {
		return ResolvedEgress{Mode: "direct"}, nil
	}

	if egressID == "firc:router" || egressID == "router" {
		if serverKind == "tgwebproxy" {
			return ResolvedEgress{}, errors.New("Telegram Proxy does not support FIRC policy routing; choose direct or a tunnel interface")
		}
		fircOk, fircDegraded := a.checkFircAvailable(ctx)
		if !fircOk {
			if fircDegraded == "" {
				fircDegraded = "FIRC routing is not available"
			}
			return ResolvedEgress{}, fmt.Errorf("egress firc:router is unavailable: %s", fircDegraded)
		}
		return ResolvedEgress{Mode: "router"}, nil
	}

	if IsLegacyEgress(egressID) {
		return ResolvedEgress{}, fmt.Errorf("%s (%s): выберите прямой выход (WAN), конкретный туннель или правила маршрутизации (FIRC)", LegacyEgressLabel(), egressID)
	}

	if tunnels := a.tunnelEntries(ctx); len(tunnels) != 0 {
		for _, t := range tunnels {
			rawIface := strings.TrimSpace(t.Iface)
			kernelIface, kerr := a.resolveKernelIface(ctx, t)
			matchesSystemRaw := strings.EqualFold(strings.TrimSpace(t.Type), "system") && strings.TrimPrefix(t.ID, "system:") == egressID
			if t.ID == egressID || rawIface == egressID || (kernelIface != "" && kernelIface == egressID) || matchesSystemRaw {
				if kerr == nil && kernelIface != "" {
					t.Iface = kernelIface
				} else if strings.EqualFold(strings.TrimSpace(t.Type), "system") && a.hasKernelResolver() {
					return ResolvedEgress{}, fmt.Errorf("selected tunnel %s has no resolvable kernel interface (%v)", t.ID, kerr)
				}
				if isInternalServiceEntry(t) {
					return ResolvedEgress{}, fmt.Errorf("selected interface %s is a server or internal service interface and cannot be used as egress", egressID)
				}
				if !t.Available {
					return ResolvedEgress{}, fmt.Errorf("selected tunnel interface %s is degraded (%s)", t.Iface, t.Status)
				}
				if t.Iface == "" {
					return ResolvedEgress{}, fmt.Errorf("selected tunnel %s has no network interface", t.ID)
				}
				if linkUp, checked := a.checkKernelLinkUp(t.Iface); checked && !linkUp {
					return ResolvedEgress{}, fmt.Errorf("selected tunnel interface %s is down (DOWN)", t.Iface)
				}
				return ResolvedEgress{
					Mode:      "interface",
					Interface: t.Iface,
				}, nil
			}
		}
	}

	return ResolvedEgress{
		Mode:      "interface",
		Interface: egressID,
	}, nil
}
