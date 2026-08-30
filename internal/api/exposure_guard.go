package api

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// exposureCheckInterval is how often the router config is re-read. The
// underlying stores cache for 30-60s, so the cost is two localhost GETs
// per tick.
const exposureCheckInterval = 60 * time.Minute

// exposureRetryInterval is used after a failed read instead of the full
// hour. The daemon routinely starts before NDMS is up, so the very first
// check after a router reboot fails — waiting an hour to retry would leave
// that window unchecked.
const exposureRetryInterval = 5 * time.Minute

// Narrow store interfaces — fake-friendly, no *ndmsquery.Queries in tests.
type staticNATLister interface {
	List(ctx context.Context) ([]ndmsquery.StaticNATEntry, error)
}

type httpProxyLister interface {
	List(ctx context.Context) ([]ndmsquery.HTTPProxyEntry, error)
}

type interfaceLookup interface {
	Get(ctx context.Context, name string) (*ndms.Interface, error)
	List(ctx context.Context) ([]ndms.Interface, error)
}

// ExposureGuard turns authentication on when the router config shows the
// manager's HTTP port reachable from the internet without any password.
//
// Two independent paths expose us, either one is enough:
//
//   - a TCP port forward (`ip static`) from a public interface onto our
//     port — note "this Keenetic" in the router UI writes to-address
//     127.0.0.1, which reaches our always-on loopback listener regardless
//     of which interfaces the HTTP server binds to;
//   - an `ip http proxy` publication with security-level public and
//     auth off.
//
// `auth: true` on the proxy entry silences ONLY the second path: a port
// forward lands on our socket directly, bypassing the router's HTTP
// server, and no authentication exists there.
//
// A bind on 0.0.0.0 is deliberately NOT a signal — the router's firewall
// denies inbound by default, so listening everywhere opens nothing.
//
// Fail-open: when NDMS is unreachable the flag is left alone. The daemon
// routinely starts before NDMS is up, and flipping the flag on every
// router reboot would be noise, not protection.
type ExposureGuard struct {
	store   *storage.SettingsStore
	static  staticNATLister
	proxies httpProxyLister
	ifaces  interfaceLookup
	log     *logging.ScopedLogger
	bus     *events.Bus
}

func NewExposureGuard(store *storage.SettingsStore, static staticNATLister, proxies httpProxyLister, ifaces interfaceLookup, appLogger logging.AppLogger) *ExposureGuard {
	return &ExposureGuard{
		store:   store,
		static:  static,
		proxies: proxies,
		ifaces:  ifaces,
		log:     logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubSettings),
	}
}

// SetEventBus sets the event bus for SSE publishing.
func (g *ExposureGuard) SetEventBus(bus *events.Bus) { g.bus = bus }

// Start re-checks until ctx ends: every exposureCheckInterval normally,
// every exposureRetryInterval after a check that could not read the
// router config.
func (g *ExposureGuard) Start(ctx context.Context) {
	go func() {
		for {
			delay := exposureCheckInterval
			if !g.check(ctx) {
				delay = exposureRetryInterval
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}()
}

// Check enables authentication if the manager is exposed without it.
// Safe to call from anywhere; does nothing when auth is already on.
func (g *ExposureGuard) Check(ctx context.Context) { g.check(ctx) }

// check reports whether the state could be determined. False means the
// settings or the router config were unreadable — the caller should come
// back sooner than the regular interval.
func (g *ExposureGuard) check(ctx context.Context) bool {
	settings, err := g.store.Get()
	if err != nil {
		g.log.Warn("auth", "", "Exposure check skipped: "+err.Error())
		return false
	}
	if settings.AuthEnabled {
		return true
	}
	reason, err := g.detect(ctx, strconv.Itoa(settings.Server.Port))
	if err != nil {
		g.log.Warn("auth", "", "Exposure check skipped: "+err.Error())
		return false
	}
	if reason == "" {
		return true
	}
	changed, err := g.store.SetAuthEnabled(true)
	if err != nil {
		g.log.Error("auth", "", "Failed to enable authentication after exposure: "+err.Error())
		return false
	}
	if !changed {
		return true
	}
	g.log.Warn("auth", "", "Authentication enabled automatically: "+reason)
	g.bus.PublishInvalidated(events.ResourceSettings, "exposure-guard")
	return true
}

// detect returns a human-readable reason when the manager is exposed
// without authentication, or "" when it is not. An error means the router
// config could not be read — the caller must treat that as "unknown", not
// as "not exposed".
func (g *ExposureGuard) detect(ctx context.Context, port string) (string, error) {
	entries, err := g.static.List(ctx)
	if err != nil {
		return "", fmt.Errorf("read ip static: %w", err)
	}
	for _, e := range entries {
		if !strings.EqualFold(e.Protocol, "tcp") || e.TargetPort() != port {
			continue
		}
		ours, err := g.addressIsOurs(ctx, e.ToAddress)
		if err != nil {
			return "", err
		}
		if !ours {
			continue
		}
		public, err := g.interfaceIsPublic(ctx, e.Interface)
		if err != nil {
			return "", err
		}
		if !public {
			continue
		}
		return fmt.Sprintf("port forward %s:%s -> %s:%s", e.Interface, e.Port, e.ToAddress, e.TargetPort()), nil
	}

	proxies, err := g.proxies.List(ctx)
	if err != nil {
		return "", fmt.Errorf("read ip http: %w", err)
	}
	for _, p := range proxies {
		if p.UpstreamPort == port && p.Public && !p.Auth {
			return fmt.Sprintf("ip http proxy %q publishes port %s without auth", p.Name, port), nil
		}
	}
	return "", nil
}

// addressIsOurs reports whether the forward lands on this router: any
// loopback address (what the router UI writes for "this Keenetic") or an
// IPv4 configured on one of its interfaces.
func (g *ExposureGuard) addressIsOurs(ctx context.Context, addr string) (bool, error) {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false, nil
	}
	if ip.IsLoopback() {
		return true, nil
	}
	list, err := g.ifaces.List(ctx)
	if err != nil {
		return false, fmt.Errorf("list interfaces: %w", err)
	}
	for _, iface := range list {
		if iface.Address != "" && ip.Equal(net.ParseIP(iface.Address)) {
			return true, nil
		}
	}
	return false, nil
}

func (g *ExposureGuard) interfaceIsPublic(ctx context.Context, name string) (bool, error) {
	iface, err := g.ifaces.Get(ctx, name)
	if err != nil {
		return false, fmt.Errorf("get interface %s: %w", name, err)
	}
	return iface != nil && iface.SecurityLevel == "public", nil
}
