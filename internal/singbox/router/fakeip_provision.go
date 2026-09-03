package router

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// StaticRouteSpec mirrors internal/ndms/command.StaticRouteSpec so the router
// stays decoupled from concrete ndms command types (DIP), consistent with the
// other consumer-owned router interfaces. The cmd/awg-manager adapter translates
// field-for-field.
type StaticRouteSpec struct {
	Interface string
	Host      string
	Network   string
	Mask      string
	Reject    bool
	Comment   string
	// V6 selects the IPv6 route form (bare network+interface, no mask/host/
	// reject/comment). Mirrors ndmscommand.StaticRouteSpec.V6.
	V6 bool
}

// OpkgTunProvisioner manages the fakeip-tun kernel interface lifecycle via NDMS.
type OpkgTunProvisioner interface {
	CreateOpkgTunWithSecurityLevel(ctx context.Context, name, description, securityLevel string) error
	SetIPGlobal(ctx context.Context, name string) error
	DeleteOpkgTun(ctx context.Context, name string) error
	SetAddress(ctx context.Context, name, address, mask string) error
	ClearAddress(ctx context.Context, name string) error
	SetIPv6Address(ctx context.Context, name, address string) error
	ClearIPv6Address(ctx context.Context, name string) error
	SetMTU(ctx context.Context, name string, mtu int) error
	InterfaceUp(ctx context.Context, name string) error
	InterfaceDown(ctx context.Context, name string) error
	// SetPermitAllACL / RemovePermitAllACL — NDMS-native разрешение трафика в
	// интерфейс: permit-all access-list `_WEBADMIN_<name>` + `ip access-group
	// … in` + auto-delete (как галка доступа в веб-морде). Без него firewall
	// NDMS (isolate-private и т.п.) режет LAN→tun форвард и DNS на tun-адрес.
	SetPermitAllACL(ctx context.Context, name string) error
	RemovePermitAllACL(ctx context.Context, name string) error
	// SetPermitAllACLv6 / RemovePermitAllACLv6 — то же для IPv6: у NDMS под v6
	// ОТДЕЛЬНОЕ пространство списков (`ipv6 access-list` + `ipv6 access-group`),
	// и v4-разрешение его не покрывает. Ставится только когда у интерфейса есть
	// v6-адрес — на интерфейсе без v6 разрешать нечего.
	SetPermitAllACLv6(ctx context.Context, name string) error
	RemovePermitAllACLv6(ctx context.Context, name string) error
}

// StaticRouteProvider manages NDMS auto static routes for the fakeip pool + reject route.
type StaticRouteProvider interface {
	AddStaticRoute(ctx context.Context, route StaticRouteSpec) error
	RemoveStaticRoute(ctx context.Context, route StaticRouteSpec) error
}

// DefaultRouteProvider manages the NDMS default route (v4 + v6) — policy-tun
// парковка дефолта на tun-интерфейс и снятие при выключении.
type DefaultRouteProvider interface {
	SetDefaultRoute(ctx context.Context, name string) error
	RemoveDefaultRoute(ctx context.Context, name string) error
	SetIPv6DefaultRoute(ctx context.Context, name string) error
	RemoveIPv6DefaultRoute(ctx context.Context, name string) error
}

// SegmentNATProvider manages segment NAT (`ip nat`) и Static NAT (`ip static`)
// для policy-tun сегментов.
type SegmentNATProvider interface {
	SetSegmentNAT(ctx context.Context, seg string) error
	RemoveSegmentNAT(ctx context.Context, seg string) error
	SetStaticNAT(ctx context.Context, seg, wan string) error
	RemoveStaticNAT(ctx context.Context, seg, wan string) error
}

// RunningConfigReader читает строки /show/running-config. TTL-кэша 60 мин
// хватает всему остальному, но policy-tun-reconcile обязан звать InvalidateAll
// перед чтением: дрейф permit/route, внесённый пользователем мимо нас, иначе
// невидим до часа.
type RunningConfigReader interface {
	Lines(ctx context.Context) ([]string, error)
	InvalidateAll()
}

// NATStateReader — структурированное состояние NAT (вместо текстового парсинга
// running-config): /show/rc/ip/nat + /show/rc/ip/static.
type NATStateReader interface {
	ListNAT(ctx context.Context) ([]query.NATEntry, error)
	ListStaticNAT(ctx context.Context) ([]query.StaticNATEntry, error)
}

// FakeIPTunParams holds the static fakeip-tun provisioning knobs not derivable
// at runtime. Defaults are spec §3.3/3.4/3.6 values; wired in cmd/awg-manager.
// (RealServer + cache path are sourced by the lifecycle layer in Slice 1D.)
type FakeIPTunParams struct {
	Inet4Range string // fakeip v4 pool (default "198.18.0.0/15", per sing-box docs)
	Inet6Range string // fakeip v6 pool (default "fc00::/18", per sing-box docs; empty disables v6)
	TunAddr4   string // tun gw /30 CIDR (default "172.18.0.1/30"); client DNS = other /30 host
	TunAddr6   string // tun gw /126 CIDR (default "fdfe:dcba:9876::1/126"; empty disables v6)
	MTU        int    // tun MTU (default 1500)
	// RealServer is the true upstream resolver the fakeip config's "real" DNS
	// server forwards to (proxy-endpoint hostnames + non-fakeip queries).
	// Default "1.1.1.1"; user-overridable via settings.FakeIPRealServer
	// (resolveFakeIPParams).
	RealServer string
	// CachePath is the sing-box experimental.cache_file path (store_fakeip).
	// Not a Deps input: fakeIPParamsWithCache fills it from Deps.CacheDBPath
	// (the operator's effective path, issue #842) on the way to the overlay spec.
	CachePath string
}

// DefaultFakeIPTunParams returns the spec-default fakeip-tun provisioning knobs
// (spec §3.3 fakeip pools, §3.4 tun gw addresses + MTU).
// Single source of truth for the wiring site in cmd/awg-manager and tests.
func DefaultFakeIPTunParams() FakeIPTunParams {
	return FakeIPTunParams{
		Inet4Range: "198.18.0.0/15",
		Inet6Range: "fc00::/18",
		TunAddr4:   "172.18.0.1/30",
		TunAddr6:   "fdfe:dcba:9876::1/126",
		MTU:        1500,
		RealServer: "1.1.1.1", // default upstream; user-overridable via FakeIPRealServer
		// CachePath left empty — fakeIPParamsWithCache takes Deps.CacheDBPath.
	}
}

// resolveFakeIPParams — вариант для сервиса: адрес bootstrap берётся из
// живых настроек, а не из снимка времени сборки зависимостей.
func (s *ServiceImpl) resolveFakeIPParams(sr storage.SingboxRouterSettings) FakeIPTunParams {
	return resolveFakeIPParamsWith(s.deps.FakeIPTun, sr, s.bootstrapDNS())
}

// fakeIPParamsWithCache — параметры для overlay и спеки: плюс эффективный
// путь cache.db у оператора (issue #842). Отдельно от resolveFakeIPParams,
// который зовётся с тика планировщика ради адресов и пулов и не должен
// ради них читать 00-base.json с флеша.
func (s *ServiceImpl) fakeIPParamsWithCache(sr storage.SingboxRouterSettings) FakeIPTunParams {
	p := s.resolveFakeIPParams(sr)
	if s.deps.CacheDBPath != nil {
		p.CachePath = s.deps.CacheDBPath()
	}
	return p
}

// bootstrapDNS читает общий адрес bootstrap-резолвера; пусто, когда стор не
// подключён (тестовые wiring'и) или настройка не задана.
func (s *ServiceImpl) bootstrapDNS() string {
	if s.deps.Settings == nil {
		return ""
	}
	return s.deps.Settings.GetSingboxBootstrapDNS()
}

// resolveFakeIPParamsWith overlays the user-editable engine settings (pool4/6,
// MTU, real upstream) from sr onto the wired static params base, returning
// the effective FakeIPTunParams. Single source of truth shared by
// enableFakeIPTun and the fakeip config overlay so the live tun/cache/pool
// can never diverge from what the user is editing. Mirrors the merge formerly
// inlined in enableFakeIPTun.
// bootstrapDNS — общий адрес bootstrap-резолвера sing-box
// (Settings.SingboxBootstrapDNS). Он подставляется в "real", когда своя
// настройка FakeIP не задана: в режиме fakeip-tun резолвером доменных
// адресов владеет слот fakeip, и без этого общая настройка bootstrap в
// этом режиме не влияла бы ни на что (issue #770).
func resolveFakeIPParamsWith(base FakeIPTunParams, sr storage.SingboxRouterSettings, bootstrapDNS string) FakeIPTunParams {
	p := base
	if bootstrapDNS != "" {
		p.RealServer = bootstrapDNS
	}
	if sr.FakeIPPool4 != "" {
		p.Inet4Range = sr.FakeIPPool4
	}
	p.Inet6Range = sr.FakeIPPool6
	if sr.FakeIPPool6 == "" {
		p.TunAddr6 = ""
	}
	if sr.FakeIPMTU != 0 {
		p.MTU = sr.FakeIPMTU
	}
	if sr.FakeIPRealServer != "" {
		p.RealServer = sr.FakeIPRealServer
	}
	return p
}
