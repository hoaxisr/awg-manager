package downloader

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

const localProxyHost = "127.0.0.1"

// TransportMode selects how an HTTP client reaches the destination.
type TransportMode string

const (
	TransportModeDirect TransportMode = "direct"
	// TransportModeBind sends traffic via SO_BINDTODEVICE on a kernel iface.
	// Used for AWG / NativeWG tunnels — no sing-box involvement.
	TransportModeBind TransportMode = "bind"
	// TransportModeProxy routes through an existing sing-box mixed inbound on
	// localhost. Used for sing-box phase-1 tunnels and subscription composites.
	// Do NOT bind to t2sN/ProxyN kernel ifaces: those are TUN/NDMS plumbing,
	// not valid egress for raw TCP from userspace.
	TransportModeProxy TransportMode = "proxy"
)

// TransportSpec is the resolved egress for one download route.
type TransportSpec struct {
	Mode       TransportMode
	Interface  string   // bind mode: kernel iface name
	DNSServers []string // bind mode: tunnel DNS for hostname resolution
	ProxyPort  int      // proxy mode: local mixed inbound port
}

// AWGEgressLookup resolves tunnel DNS for an AWG outbound tag.
type AWGEgressLookup interface {
	DNSForTag(ctx context.Context, tag string) []string
}

// TunnelListenPortLookup resolves a sing-box tunnel tag to its local mixed
// inbound listen port (1080+).
type TunnelListenPortLookup interface {
	ListenPortByTag(ctx context.Context, tag string) (int, bool)
}

// SubscriptionListenPortLookup resolves a subscription selector tag to its
// local mixed inbound listen port (11000+).
type SubscriptionListenPortLookup interface {
	ListenPortBySelectorTag(ctx context.Context, selectorTag string) (int, bool)
}

// SingboxRuntimeChecker reports whether sing-box is running. Proxy-based
// routes require a live process listening on localhost.
type SingboxRuntimeChecker interface {
	IsRunning() bool
}

// TransportResolverDeps wires lookup helpers for compositeTransportResolver.
type TransportResolverDeps struct {
	Tunnels   TunnelListenPortLookup
	Subs      SubscriptionListenPortLookup
	Singbox   SingboxRuntimeChecker
	AWGEgress AWGEgressLookup
	// SysClassNet overrides /sys/class/net for tests.
	SysClassNet string
}

// TransportResolver maps a catalog outbound to a concrete transport.
type TransportResolver interface {
	Resolve(ctx context.Context, ob Outbound) (TransportSpec, error)
	IsAvailable(ctx context.Context, ob Outbound) bool
}

type compositeTransportResolver struct {
	tunnels   TunnelListenPortLookup
	subs      SubscriptionListenPortLookup
	singbox   SingboxRuntimeChecker
	awgEgress AWGEgressLookup
	sysNet    string
}

func NewTransportResolver(d TransportResolverDeps) TransportResolver {
	sysNet := strings.TrimSpace(d.SysClassNet)
	if sysNet == "" {
		sysNet = "/sys/class/net"
	}
	return &compositeTransportResolver{
		tunnels:   d.Tunnels,
		subs:      d.Subs,
		singbox:   d.Singbox,
		awgEgress: d.AWGEgress,
		sysNet:    sysNet,
	}
}

func (r *compositeTransportResolver) singboxRunning() bool {
	return r.singbox != nil && r.singbox.IsRunning()
}

func (r *compositeTransportResolver) Resolve(ctx context.Context, ob Outbound) (TransportSpec, error) {
	kind := strings.TrimSpace(ob.Kind)
	tag := strings.TrimSpace(ob.Tag)
	if tag == "" || tag == "direct" || kind == "" || kind == "direct" {
		return TransportSpec{Mode: TransportModeDirect}, nil
	}

	switch kind {
	case "awg":
		iface := strings.TrimSpace(ob.Detail)
		if iface == "" {
			return TransportSpec{}, fmt.Errorf("outbound %q has no kernel interface", ob.Tag)
		}
		if !r.ifaceExists(iface) {
			return TransportSpec{}, fmt.Errorf("outbound %q interface %q is not present", ob.Tag, iface)
		}
		spec := TransportSpec{Mode: TransportModeBind, Interface: iface}
		if r.awgEgress != nil {
			spec.DNSServers = r.awgEgress.DNSForTag(ctx, tag)
		}
		return spec, nil

	case "singbox":
		if !r.singboxRunning() {
			return TransportSpec{}, fmt.Errorf("outbound %q is unavailable: sing-box is not running", ob.Tag)
		}
		if r.tunnels == nil {
			return TransportSpec{}, fmt.Errorf("outbound %q is unavailable: sing-box tunnel lookup is not configured", ob.Tag)
		}
		port, ok := r.tunnels.ListenPortByTag(ctx, ob.Tag)
		if !ok || port <= 0 {
			return TransportSpec{}, fmt.Errorf("outbound %q is unavailable: local sing-box proxy port not found", ob.Tag)
		}
		return TransportSpec{Mode: TransportModeProxy, ProxyPort: port}, nil

	case "subscription":
		if !r.singboxRunning() {
			return TransportSpec{}, fmt.Errorf("outbound %q is unavailable: sing-box is not running", ob.Tag)
		}
		if r.subs == nil {
			return TransportSpec{}, fmt.Errorf("outbound %q is unavailable: subscription lookup is not configured", ob.Tag)
		}
		port, ok := r.subs.ListenPortBySelectorTag(ctx, ob.Tag)
		if !ok || port <= 0 {
			return TransportSpec{}, fmt.Errorf("outbound %q is unavailable: local subscription proxy port not found", ob.Tag)
		}
		return TransportSpec{Mode: TransportModeProxy, ProxyPort: port}, nil

	default:
		return TransportSpec{}, fmt.Errorf("outbound %q kind %q is not supported for download transport", ob.Tag, ob.Kind)
	}
}

func (r *compositeTransportResolver) IsAvailable(ctx context.Context, ob Outbound) bool {
	spec, err := r.Resolve(ctx, ob)
	if err != nil {
		return false
	}
	_ = spec
	return true
}

func (r *compositeTransportResolver) ifaceExists(name string) bool {
	if name == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(r.sysNet, name))
	return err == nil
}

func newHTTPClientFromSpec(spec TransportSpec) (*http.Client, error) {
	cfg := httpclient.TransportConfig{}
	switch spec.Mode {
	case TransportModeDirect:
		// zero cfg → default route
	case TransportModeBind:
		cfg.Interface = spec.Interface
		cfg.DNSServers = spec.DNSServers
	case TransportModeProxy:
		if spec.ProxyPort <= 0 {
			return nil, fmt.Errorf("proxy transport requires ProxyPort")
		}
		cfg.ProxyURL = fmt.Sprintf("http://%s:%d", localProxyHost, spec.ProxyPort)
	default:
		return nil, fmt.Errorf("unsupported transport mode %q", spec.Mode)
	}

	// Прокси окружения наследуется СОЗНАТЕЛЬНО (Proxy оставлен нулевым,
	// httpclient.ProxyInheritEnv): у режима «через прокси» адрес уже
	// стоит в cfg.ProxyURL и он сильнее окружения, а у прямого режима
	// наследование прокси владельца роутера — желаемое поведение загрузчика.
	tr, err := httpclient.NewTransport(cfg)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: tr}, nil
}
