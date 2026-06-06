package downloader

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

const (
	localProxyHost         = "127.0.0.1"
	proxyPortProbeTimeout  = 2 * time.Second
)

// ProxyPortOpen probes whether a local sing-box mixed inbound accepts TCP.
// Nil in TransportResolverDeps uses a real dial to 127.0.0.1:<port>.
type ProxyPortOpen func(host string, port int) bool

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
	ProxyPort  int      // proxy mode: local mixed inbound port (ignored when ProxyURL set)
	ProxyURL   string   // proxy mode: full proxy URL (supports auth)
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

// SingboxRuntimeChecker reports sing-box daemon state for proxy-based routes.
type SingboxRuntimeChecker interface {
	IsRunning() bool
	// IsReady reports whether the Clash API is healthy (sing-box finished boot).
	IsReady() bool
}

// TransportResolverDeps wires lookup helpers for compositeTransportResolver.
type TransportResolverDeps struct {
	Tunnels   TunnelListenPortLookup
	Subs      SubscriptionListenPortLookup
	Singbox   SingboxRuntimeChecker
	AWGEgress AWGEgressLookup
	// SysClassNet overrides /sys/class/net for tests.
	SysClassNet string
	// ProxyPortOpen overrides the local mixed-inbound TCP probe (tests).
	ProxyPortOpen ProxyPortOpen
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
	proxyOpen ProxyPortOpen
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
		proxyOpen: d.ProxyPortOpen,
	}
}

func (r *compositeTransportResolver) singboxRunning() bool {
	return r.singbox != nil && r.singbox.IsRunning()
}

func (r *compositeTransportResolver) singboxReady() bool {
	return r.singbox != nil && r.singbox.IsReady()
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
			return TransportSpec{}, newRouteError(
				ErrCodeAWGDown,
				fmt.Sprintf("outbound %q interface %q is not present", ob.Tag, iface),
				nil,
			)
		}
		spec := TransportSpec{Mode: TransportModeBind, Interface: iface}
		if r.awgEgress != nil {
			spec.DNSServers = r.awgEgress.DNSForTag(ctx, tag)
		}
		return spec, nil

	case "singbox":
		if !r.singboxRunning() {
			return TransportSpec{}, newRouteError(
				ErrCodeSingboxNotRunning,
				fmt.Sprintf("outbound %q is unavailable: sing-box is not running", ob.Tag),
				nil,
			)
		}
		if !r.singboxReady() {
			return TransportSpec{}, newRouteError(
				ErrCodeSingboxNotReady,
				fmt.Sprintf("outbound %q is unavailable: sing-box is not ready", ob.Tag),
				nil,
			)
		}
		if r.tunnels == nil {
			return TransportSpec{}, newRouteError(
				ErrCodeRoute,
				fmt.Sprintf("outbound %q is unavailable: sing-box tunnel lookup is not configured", ob.Tag),
				nil,
			)
		}
		port, ok := r.tunnels.ListenPortByTag(ctx, ob.Tag)
		if !ok || port <= 0 {
			return TransportSpec{}, newRouteError(
				ErrCodeRoute,
				fmt.Sprintf("outbound %q is unavailable: local sing-box proxy port not found", ob.Tag),
				nil,
			)
		}
		if !r.proxyPortListening(port) {
			return TransportSpec{}, newRouteError(
				ErrCodeSingboxNotReady,
				fmt.Sprintf("outbound %q is unavailable: sing-box proxy port not listening", ob.Tag),
				nil,
			)
		}
		return TransportSpec{Mode: TransportModeProxy, ProxyPort: port}, nil

	case "subscription":
		if !r.singboxRunning() {
			return TransportSpec{}, newRouteError(
				ErrCodeSingboxNotRunning,
				fmt.Sprintf("outbound %q is unavailable: sing-box is not running", ob.Tag),
				nil,
			)
		}
		if !r.singboxReady() {
			return TransportSpec{}, newRouteError(
				ErrCodeSingboxNotReady,
				fmt.Sprintf("outbound %q is unavailable: sing-box is not ready", ob.Tag),
				nil,
			)
		}
		if r.subs == nil {
			return TransportSpec{}, newRouteError(
				ErrCodeRoute,
				fmt.Sprintf("outbound %q is unavailable: subscription lookup is not configured", ob.Tag),
				nil,
			)
		}
		port, ok := r.subs.ListenPortBySelectorTag(ctx, ob.Tag)
		if !ok || port <= 0 {
			return TransportSpec{}, newRouteError(
				ErrCodeRoute,
				fmt.Sprintf("outbound %q is unavailable: local subscription proxy port not found", ob.Tag),
				nil,
			)
		}
		if !r.proxyPortListening(port) {
			return TransportSpec{}, newRouteError(
				ErrCodeSingboxNotReady,
				fmt.Sprintf("outbound %q is unavailable: subscription proxy port not listening", ob.Tag),
				nil,
			)
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

func (r *compositeTransportResolver) proxyPortListening(port int) bool {
	if port <= 0 {
		return false
	}
	check := r.proxyOpen
	if check == nil {
		check = defaultProxyPortOpen
	}
	return check(localProxyHost, port)
}

func defaultProxyPortOpen(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), proxyPortProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
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
		if spec.ProxyURL != "" {
			cfg.ProxyURL = spec.ProxyURL
		} else if spec.ProxyPort > 0 {
			cfg.ProxyURL = fmt.Sprintf("http://%s:%d", localProxyHost, spec.ProxyPort)
		} else {
			return nil, fmt.Errorf("proxy transport requires ProxyURL or ProxyPort")
		}
	default:
		return nil, fmt.Errorf("unsupported transport mode %q", spec.Mode)
	}

	tr, err := httpclient.NewTransport(cfg)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: tr}, nil
}
