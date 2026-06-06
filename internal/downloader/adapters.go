package downloader

import (
	"context"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

type deviceProxyOutboundsProvider struct {
	svc *deviceproxy.Service
}

type settingsRouteProvider struct {
	store *storage.SettingsStore
}

type singboxTunnelPortAdapter struct {
	op *singbox.Operator
}

type subscriptionPortAdapter struct {
	svc *subscription.Service
}

type singboxRuntimeAdapter struct {
	op *singbox.Operator
}

type awgStoreEgressAdapter struct {
	store *storage.AWGTunnelStore
}

func NewDeviceProxyOutboundsProvider(svc *deviceproxy.Service) OutboundsProvider {
	if svc == nil {
		return nil
	}
	return &deviceProxyOutboundsProvider{svc: svc}
}

func NewSettingsRouteProvider(store *storage.SettingsStore) RouteProvider {
	if store == nil {
		return nil
	}
	return &settingsRouteProvider{store: store}
}

func NewSingboxTunnelPortAdapter(op *singbox.Operator) TunnelListenPortLookup {
	if op == nil {
		return nil
	}
	return &singboxTunnelPortAdapter{op: op}
}

func NewSubscriptionPortAdapter(svc *subscription.Service) SubscriptionListenPortLookup {
	if svc == nil {
		return nil
	}
	return &subscriptionPortAdapter{svc: svc}
}

func NewSingboxRuntimeAdapter(op *singbox.Operator) SingboxRuntimeChecker {
	if op == nil {
		return nil
	}
	return &singboxRuntimeAdapter{op: op}
}

func NewAWGStoreEgressAdapter(store *storage.AWGTunnelStore) AWGEgressLookup {
	if store == nil {
		return nil
	}
	return &awgStoreEgressAdapter{store: store}
}

func NewSettingsBackedService(
	deviceProxySvc *deviceproxy.Service,
	singboxOp *singbox.Operator,
	subSvc *subscription.Service,
	settingsStore *storage.SettingsStore,
	awgStore *storage.AWGTunnelStore,
) *Service {
	return NewService(Deps{
		Outbounds: NewDeviceProxyOutboundsProvider(deviceProxySvc),
		TransportResolver: NewTransportResolver(TransportResolverDeps{
			Tunnels:   NewSingboxTunnelPortAdapter(singboxOp),
			Subs:      NewSubscriptionPortAdapter(subSvc),
			Singbox:   NewSingboxRuntimeAdapter(singboxOp),
			AWGEgress: NewAWGStoreEgressAdapter(awgStore),
		}),
		RouteProvider: NewSettingsRouteProvider(settingsStore),
	})
}

func (a *awgStoreEgressAdapter) DNSForTag(_ context.Context, tag string) []string {
	if a == nil || a.store == nil {
		return nil
	}
	id, system := awgTunnelIDFromTag(tag)
	if id == "" || system {
		return nil
	}
	stored, err := a.store.Get(id)
	if err != nil || stored == nil {
		return nil
	}
	return tunnel.ParseDNSList(stored.Interface.DNS)
}

func awgTunnelIDFromTag(tag string) (id string, system bool) {
	tag = strings.TrimSpace(tag)
	switch {
	case strings.HasPrefix(tag, "awg-sys-"):
		return strings.TrimPrefix(tag, "awg-sys-"), true
	case strings.HasPrefix(tag, "awg-"):
		return strings.TrimPrefix(tag, "awg-"), false
	default:
		return "", false
	}
}

func (a *deviceProxyOutboundsProvider) ListDownloadOutbounds(ctx context.Context) []Outbound {
	if a == nil || a.svc == nil {
		return nil
	}
	src := a.svc.ListOutbounds(ctx)
	out := make([]Outbound, 0, len(src))
	for _, ob := range src {
		out = append(out, Outbound{
			Tag:    ob.Tag,
			Kind:   ob.Kind,
			Label:  ob.Label,
			Detail: ob.Detail,
		})
	}
	return out
}

func (a *singboxTunnelPortAdapter) ListenPortByTag(ctx context.Context, tag string) (int, bool) {
	if a == nil || a.op == nil || strings.TrimSpace(tag) == "" {
		return 0, false
	}
	tunnels, err := a.op.ListTunnels(ctx)
	if err != nil {
		return 0, false
	}
	for _, t := range tunnels {
		if t.Tag == tag && t.ListenPort > 0 {
			return t.ListenPort, true
		}
	}
	return 0, false
}

func (a *subscriptionPortAdapter) ListenPortBySelectorTag(_ context.Context, selectorTag string) (int, bool) {
	if a == nil || a.svc == nil || strings.TrimSpace(selectorTag) == "" {
		return 0, false
	}
	for _, sub := range a.svc.List() {
		if sub.SelectorTag != selectorTag || !sub.Enabled {
			continue
		}
		if sub.ListenPort > 0 && len(sub.MemberTags) > 0 {
			return int(sub.ListenPort), true
		}
	}
	return 0, false
}

func (a *singboxRuntimeAdapter) IsRunning() bool {
	if a == nil || a.op == nil {
		return false
	}
	running, _ := a.op.IsRunningPublic()
	return running
}

func (a *singboxRuntimeAdapter) IsReady() bool {
	if a == nil || a.op == nil {
		return false
	}
	running, _ := a.op.IsRunningPublic()
	if !running {
		return false
	}
	clash := a.op.Clash()
	if clash == nil {
		return false
	}
	return clash.IsHealthy()
}

func (p *settingsRouteProvider) GetDownloadRoute(ctx context.Context) (*Route, error) {
	if p == nil || p.store == nil {
		return &Route{Tag: "direct"}, nil
	}
	st, err := p.store.Get()
	if err != nil {
		return nil, err
	}
	tag := strings.TrimSpace(st.Download.RouteTag)
	if tag == "" {
		tag = "direct"
	}
	kind := strings.TrimSpace(st.Download.RouteKind)
	if tag == "direct" {
		kind = "direct"
	}
	return &Route{Tag: tag, Kind: kind}, nil
}
