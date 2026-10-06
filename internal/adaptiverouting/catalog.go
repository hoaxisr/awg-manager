package adaptiverouting

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type MihomoNativeProvider interface {
	ListProxies() []MihomoProxyInfo
	ListSubscriptions() []MihomoSubscriptionInfo
	ListGroups() []MihomoGroupInfo
}

type MihomoProxyInfo struct {
	ID      string
	Name    string
	Enabled bool
}

type MihomoSubscriptionInfo struct {
	ID      string
	Name    string
	Enabled bool
}

type MihomoGroupInfo struct {
	ID         string
	Name       string
	Enabled    bool
	Proxies    []string
	Use        []string
	DisableUDP bool
}

type TunnelProvider interface {
	ListTunnels() []TunnelInfo
}

type TunnelInfo struct {
	ID        string
	Name      string
	Interface string
	Active    bool
	Kind      string // "awg" | "native" | "wdtt" | "freeturn"
}

type SingboxProvider interface {
	ListSubscriptions() []SingboxSubscriptionInfo
	ListOutbounds() []SingboxOutboundInfo
}

type SingboxSubscriptionInfo struct {
	ID          string
	Label       string
	Tag         string
	Enabled     bool
	MemberCount int
	Mode        string
}

type SingboxOutboundInfo struct {
	Tag       string
	Type      string
	Interface string
	Enabled   bool
	Label     string
}

type Catalog struct {
	nativeStore     MihomoNativeProvider
	tunnelProvider  TunnelProvider
	singboxProvider SingboxProvider
}

func NewCatalog(nativeStore MihomoNativeProvider, tunnelProvider TunnelProvider, sbProviders ...SingboxProvider) *Catalog {
	var sb SingboxProvider
	if len(sbProviders) > 0 {
		sb = sbProviders[0]
	}
	return &Catalog{
		nativeStore:     nativeStore,
		tunnelProvider:  tunnelProvider,
		singboxProvider: sb,
	}
}

func (c *Catalog) ListEgresses(ctx context.Context) ([]ResolvedEgress, error) {
	var results []ResolvedEgress

	// 1. Kernel Tunnels
	if c.tunnelProvider != nil {
		for _, t := range c.tunnelProvider.ListTunnels() {
			if strings.HasPrefix(t.Name, "awg-manager:") || strings.HasPrefix(t.ID, "awg-manager:") ||
				strings.HasPrefix(t.Interface, "Proxy") || strings.HasPrefix(t.ID, "wan:") {
				continue
			}
			iface := resolveKernelDev(t.Interface)
			available := t.Active && iface != ""
			reason := ""
			if !available {
				reason = "Туннель выключен или интерфейс не поднят"
			}
			results = append(results, ResolvedEgress{
				Ref: EgressRef{
					Kind:       EgressKindKernelTunnel,
					ResourceID: t.ID,
					Engine:     EngineSystem,
				},
				DisplayName: t.Name,
				Interface:   iface,
				Capabilities: Capabilities{
					TCP:  true,
					UDP:  true,
					ICMP: true,
					IPv4: true,
				},
				Available:         available,
				UnavailableReason: reason,
			})
		}
	}

	// 2. Mihomo Native Resources
	if c.nativeStore != nil {
		// Proxies
		for _, p := range c.nativeStore.ListProxies() {
			available := p.Enabled
			reason := ""
			if !available {
				reason = "Прокси-узел отключен"
			}
			results = append(results, ResolvedEgress{
				Ref: EgressRef{
					Kind:       EgressKindMihomoProxy,
					ResourceID: p.ID,
					Engine:     EngineMihomo,
				},
				DisplayName: p.Name,
				Interface:   "awgsus0",
				Capabilities: Capabilities{
					TCP:  true,
					UDP:  true,
					IPv4: true,
				},
				Available:         available,
				UnavailableReason: reason,
			})
		}

		// Subscriptions
		for _, s := range c.nativeStore.ListSubscriptions() {
			available := s.Enabled
			reason := ""
			if !available {
				reason = "Подписка отключена"
			}
			results = append(results, ResolvedEgress{
				Ref: EgressRef{
					Kind:       EgressKindMihomoSubscription,
					ResourceID: s.ID,
					Engine:     EngineMihomo,
				},
				DisplayName: s.Name,
				Interface:   "awgsus0",
				Capabilities: Capabilities{
					TCP:  true,
					UDP:  true,
					IPv4: true,
				},
				Available:         available,
				UnavailableReason: reason,
			})
		}

		// Groups
		for _, g := range c.nativeStore.ListGroups() {
			available := g.Enabled
			reason := ""
			if !available {
				reason = "Группа отключена"
			} else if len(g.Proxies) == 0 && len(g.Use) == 0 {
				available = false
				reason = "В группе нет доступных узлов"
			}
			results = append(results, ResolvedEgress{
				Ref: EgressRef{
					Kind:       EgressKindMihomoGroup,
					ResourceID: g.ID,
					Engine:     EngineMihomo,
				},
				DisplayName: g.Name,
				Interface:   "awgsus0",
				Capabilities: Capabilities{
					TCP:  true,
					UDP:  !g.DisableUDP,
					IPv4: true,
				},
				Available:         available,
				UnavailableReason: reason,
			})
		}
	}

	// 3. Sing-box Resources
	if c.singboxProvider != nil {
		// Subscriptions
		for _, s := range c.singboxProvider.ListSubscriptions() {
			available := s.Enabled && s.MemberCount > 0
			reason := ""
			if !available {
				if !s.Enabled {
					reason = "Подписка sing-box отключена"
				} else {
					reason = "В подписке sing-box нет активных узлов"
				}
			}
			tag := s.Tag
			if tag == "" {
				tag = s.ID
			}
			results = append(results, ResolvedEgress{
				Ref: EgressRef{
					Kind:       EgressKindSingboxSubscription,
					ResourceID: tag,
					Engine:     EngineSingbox,
				},
				DisplayName: s.Label,
				Interface:   "awgsus0",
				Capabilities: Capabilities{
					TCP:  true,
					UDP:  true,
					IPv4: true,
				},
				Available:         available,
				UnavailableReason: reason,
			})
		}

		// Outbounds
		for _, o := range c.singboxProvider.ListOutbounds() {
			name := o.Label
			if name == "" {
				name = o.Tag
			}
			results = append(results, ResolvedEgress{
				Ref: EgressRef{
					Kind:       EgressKindSingboxOutbound,
					ResourceID: o.Tag,
					Engine:     EngineSingbox,
				},
				DisplayName: name,
				Interface:   "awgsus0",
				Capabilities: Capabilities{
					TCP:  true,
					UDP:  true,
					IPv4: true,
				},
				Available:         o.Enabled,
				UnavailableReason: "",
			})
		}
	}

	// Deterministic sorting so UI never jumps between polls:
	// 1. Kind rank: kernel-tunnel -> mihomo-proxy -> mihomo-group -> mihomo-subscription -> singbox-subscription -> singbox-outbound
	// 2. DisplayName natural comparison
	// 3. Interface and ResourceID as tiebreaker
	kindRank := func(kind EgressKind) int {
		switch kind {
		case EgressKindKernelTunnel:
			return 1
		case EgressKindMihomoProxy:
			return 2
		case EgressKindMihomoGroup:
			return 3
		case EgressKindMihomoSubscription:
			return 4
		case EgressKindSingboxSubscription:
			return 5
		case EgressKindSingboxOutbound:
			return 6
		default:
			return 99
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		rI := kindRank(results[i].Ref.Kind)
		rJ := kindRank(results[j].Ref.Kind)
		if rI != rJ {
			return rI < rJ
		}
		nameI := strings.ToLower(results[i].DisplayName)
		nameJ := strings.ToLower(results[j].DisplayName)
		if nameI != nameJ {
			return nameI < nameJ
		}
		if results[i].Interface != results[j].Interface {
			return results[i].Interface < results[j].Interface
		}
		return results[i].Ref.ResourceID < results[j].Ref.ResourceID
	})

	return results, nil
}

func (c *Catalog) Resolve(ctx context.Context, ref EgressRef) (ResolvedEgress, error) {
	all, err := c.ListEgresses(ctx)
	if err != nil {
		return ResolvedEgress{}, err
	}

	for _, eg := range all {
		kindMatch := ref.Kind == "" || eg.Ref.Kind == ref.Kind
		engineMatch := ref.Engine == "" || eg.Ref.Engine == ref.Engine
		if kindMatch && engineMatch {
			if eg.Ref.ResourceID == ref.ResourceID || strings.EqualFold(eg.DisplayName, ref.ResourceID) {
				return eg, nil
			}
		}
	}

	return ResolvedEgress{}, fmt.Errorf("egress target %s/%s not found", ref.Kind, ref.ResourceID)
}
