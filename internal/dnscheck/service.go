package dnscheck

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

const probeDomain = "awgm-dnscheck.test"

// DnsRouteProvider provides DNS route list statistics.
type DnsRouteProvider interface {
	ListEnabledCount(ctx context.Context) (total int, enabled int)
}

// TunnelStateProvider provides running tunnel information.
type TunnelStateProvider interface {
	RunningTunnelNames(ctx context.Context) []string
}

// ndmsClient is the subset of *transport.Client used for write paths only
// (createIPHost POSTs). Read paths flow through the cached Query stores
// below so we don't bypass the TTL/SingleFlight layer.
type ndmsClient interface {
	Post(ctx context.Context, payload any) (json.RawMessage, error)
}

// compile-time check: *transport.Client must satisfy ndmsClient
var _ ndmsClient = (*transport.Client)(nil)

// hotspotStore is the cached /show/ip/hotspot reader (see ndms/query).
type hotspotStore interface {
	List(ctx context.Context) ([]ndms.Device, error)
}

// ipHostStore is the cached /show/rc/ip/host reader with explicit
// invalidation for use after createIPHost writes.
type ipHostStore interface {
	Lookup(ctx context.Context, domain string) (string, bool)
	Invalidate()
}

// dnsProxyConfigStore answers "is encrypted DNS configured?" off cached
// /show/rc/dns-proxy bytes.
type dnsProxyConfigStore interface {
	HasEncryptedTransport(ctx context.Context) (bool, error)
}

// Service runs DNS routing diagnostic checks.
type Service struct {
	ndms           ndmsClient
	hotspot        hotspotStore
	ipHost         ipHostStore
	dnsProxyConfig dnsProxyConfigStore
	dns            DnsRouteProvider
	tunnels        TunnelStateProvider
	appLog         *logging.ScopedLogger
}

// NewService creates a new DNS check service.
func NewService(
	ndmsClient ndmsClient,
	hotspot hotspotStore,
	ipHost ipHostStore,
	dnsProxyConfig dnsProxyConfigStore,
	dns DnsRouteProvider,
	tunnels TunnelStateProvider,
	appLogger logging.AppLogger,
) *Service {
	return &Service{
		ndms:           ndmsClient,
		hotspot:        hotspot,
		ipHost:         ipHost,
		dnsProxyConfig: dnsProxyConfig,
		dns:            dns,
		tunnels:        tunnels,
		appLog:         logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubDnsCheck),
	}
}

// firstIPv4 — шов над чтением адреса интерфейса: тесты подменяют, прод зовёт
// netif.FirstIPv4.
var firstIPv4 = netif.FirstIPv4

// EnsureIPHost creates a permanent ip host entry for the probe domain.
// Called once at startup. The entry maps awgm-dnscheck.test to the router's
// br0 IP so clients can verify their DNS goes through the router.
//
// The existing entry is inspected via /show/rc/ip/host first and left alone
// when it already matches — a blind POST at every startup made NDMS log
// 'Core::Configurator: not found: "ip/host/awgm-dnscheck.test"' because
// it resolved the leaf path before creating.
func (s *Service) EnsureIPHost(ctx context.Context) {
	routerIP := firstIPv4("br0")
	if routerIP == "" {
		s.appLog.Warn("ensure-ip-host", probeDomain, "br0 has no IPv4, skipping")
		return
	}
	if current, ok := s.lookupIPHost(ctx, probeDomain); ok && current == routerIP {
		return
	}
	if err := s.createIPHost(ctx, probeDomain, routerIP); err != nil {
		s.appLog.Warn("ensure-ip-host", probeDomain, fmt.Sprintf("failed to create %s -> %s: %v", probeDomain, routerIP, err))
		return
	}
	s.appLog.Info("ensure-ip-host", probeDomain, fmt.Sprintf("created %s -> %s", probeDomain, routerIP))
}

// lookupIPHost returns the configured address for the given domain, or
// ("", false) if not present. Errors are swallowed because a missing
// entry is indistinguishable from a transient NDMS hiccup at this level —
// the caller retries via createIPHost either way.
func (s *Service) lookupIPHost(ctx context.Context, domain string) (string, bool) {
	return s.ipHost.Lookup(ctx, domain)
}

// Start runs server-side checks (tunnel, routes, policy, encryption) and returns
// the results along with client info. Check 3 (DNS probe) is left pending —
// the frontend performs it directly via fetch to the probe domain.
func (s *Service) Start(ctx context.Context, clientIP string) (*StartResponse, error) {
	s.appLog.Info("start", clientIP, "DNS check started")
	hostname := s.resolveHostname(ctx, clientIP)

	checks := []CheckResult{
		s.checkTunnel(ctx),
		s.checkRoutes(ctx),
		{ID: "dns_probe", Status: "pending", Title: "DNS-запрос к роутеру", Message: "Ожидание DNS-запроса..."},
		s.checkPolicy(ctx, clientIP),
		s.checkEncryption(ctx),
	}

	failures := 0
	for _, c := range checks {
		if c.Status == "fail" {
			failures++
		}
	}
	if failures > 0 {
		s.appLog.Warn("complete", clientIP, fmt.Sprintf("DNS check completed with %d failed checks", failures))
	} else {
		s.appLog.Info("complete", clientIP, "DNS check completed: all checks passed")
	}

	return &StartResponse{
		ClientIP: clientIP,
		Hostname: hostname,
		Checks:   checks,
	}, nil
}

// ClientContext returns the caller's LAN identity and access-policy assignment
// without running the full DNS diagnostic suite (tunnel/routes/encryption checks).
func (s *Service) ClientContext(ctx context.Context, clientIP string) (*StartResponse, error) {
	hostname := s.resolveHostname(ctx, clientIP)
	return &StartResponse{
		ClientIP: clientIP,
		Hostname: hostname,
		Checks:   []CheckResult{s.checkPolicy(ctx, clientIP)},
	}, nil
}

// checkTunnel checks that at least one tunnel is running.
func (s *Service) checkTunnel(ctx context.Context) CheckResult {
	names := s.tunnels.RunningTunnelNames(ctx)
	if len(names) == 0 {
		return CheckResult{
			ID:      "tunnel_running",
			Status:  "fail",
			Title:   "Туннель запущен",
			Message: "Ни один туннель не запущен",
			Detail:  "Запустите туннель, чтобы трафик мог маршрутизироваться",
		}
	}
	return CheckResult{
		ID:      "tunnel_running",
		Status:  "ok",
		Title:   "Туннель запущен",
		Message: fmt.Sprintf("Запущено туннелей: %d (%s)", len(names), strings.Join(names, ", ")),
	}
}

// checkRoutes checks that at least one DNS route list is enabled.
func (s *Service) checkRoutes(ctx context.Context) CheckResult {
	total, enabled := s.dns.ListEnabledCount(ctx)
	if enabled == 0 {
		return CheckResult{
			ID:      "dns_routes",
			Status:  "fail",
			Title:   "Списки DNS-маршрутизации",
			Message: "Нет активных списков DNS-маршрутизации",
			Detail:  fmt.Sprintf("Всего списков: %d, активных: 0. Включите хотя бы один список.", total),
		}
	}
	return CheckResult{
		ID:      "dns_routes",
		Status:  "ok",
		Title:   "Списки DNS-маршрутизации",
		Message: fmt.Sprintf("Активных списков: %d из %d", enabled, total),
	}
}

// checkPolicy checks if the client IP is assigned an alternative access policy.
func (s *Service) checkPolicy(ctx context.Context, clientIP string) CheckResult {
	hosts, err := s.hotspot.List(ctx)
	if err != nil {
		return CheckResult{
			ID:      "client_policy",
			Status:  "warning",
			Title:   "Политика доступа клиента",
			Message: "Не удалось получить список клиентов",
			Detail:  err.Error(),
		}
	}

	for _, h := range hosts {
		if h.IP != clientIP {
			continue
		}
		assigned := h.Access
		if assigned == "" {
			assigned = h.Policy
		}
		if assigned != "" {
			return CheckResult{
				ID:      "client_policy",
				Status:  "ok",
				Title:   "Политика доступа клиента",
				Message: fmt.Sprintf("Клиент использует политику: %s", assigned),
			}
		}
		return CheckResult{
			ID:      "client_policy",
			Status:  "warning",
			Title:   "Политика доступа клиента",
			Message: "Клиент использует политику по умолчанию",
			Detail:  "Назначьте альтернативную политику для маршрутизации трафика через туннель",
		}
	}

	return CheckResult{
		ID:      "client_policy",
		Status:  "warning",
		Title:   "Политика доступа клиента",
		Message: "Клиент не найден в списке устройств",
		Detail:  fmt.Sprintf("IP %s не найден в /show/ip/hotspot", clientIP),
	}
}

// checkEncryption checks if the DNS proxy uses encrypted DNS (DoT/DoH/TLS).
func (s *Service) checkEncryption(ctx context.Context) CheckResult {
	encrypted, err := s.dnsProxyConfig.HasEncryptedTransport(ctx)
	if err != nil {
		return CheckResult{
			ID:      "dns_encryption",
			Status:  "warning",
			Title:   "Шифрование DNS",
			Message: "Не удалось получить конфигурацию DNS-прокси",
			Detail:  err.Error(),
		}
	}
	if encrypted {
		return CheckResult{
			ID:      "dns_encryption",
			Status:  "ok",
			Title:   "Шифрование DNS",
			Message: "DNS-прокси использует зашифрованный транспорт",
		}
	}
	return CheckResult{
		ID:      "dns_encryption",
		Status:  "warning",
		Title:   "Шифрование DNS",
		Message: "Зашифрованный DNS не обнаружен",
		Detail:  "Рекомендуется включить DNS-over-TLS или DNS-over-HTTPS",
	}
}

// createIPHost creates an ip host entry via RCI.
//
// Request shape matches the CLI `ip host <domain> <address>` — domain
// and address are SIBLINGS under ip.host, NOT domain-as-key. An earlier
// version nested {ip: {host: {<domain>: {address}}}} which NDMS parsed
// as a path lookup to an existing record, producing:
//
//	Core::Configurator: not found: "ip/host/awgm-dnscheck.test"
func (s *Service) createIPHost(ctx context.Context, domain, address string) error {
	payload := map[string]interface{}{
		"ip": map[string]interface{}{
			"host": map[string]interface{}{
				"domain":  domain,
				"address": address,
			},
		},
	}
	_, err := s.ndms.Post(ctx, payload)
	if err == nil {
		s.ipHost.Invalidate()
	}
	return err
}

// resolveHostname looks up the client hostname from the hotspot list.
func (s *Service) resolveHostname(ctx context.Context, ip string) string {
	hosts, err := s.hotspot.List(ctx)
	if err != nil {
		return ip
	}
	for _, h := range hosts {
		if h.IP == ip {
			if h.Name != "" {
				return h.Name
			}
			if h.Hostname != "" {
				return h.Hostname
			}
		}
	}
	return ip
}
