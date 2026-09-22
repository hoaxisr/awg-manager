package dnscheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

const probeDomain = "awgm-dnscheck.test"

// probeTeardownDelay — сколько запись `ip host` живёт после запуска проверки.
// Запись нужна только на время пробы: постоянная переживала удаление пакета и
// подменяла PTR LAN-адреса роутера (#942). У пробы во фронте таймаут 3 с,
// остальное — запас на медленный RCI.
var probeTeardownDelay = 15 * time.Second

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

	teardownMu    sync.Mutex
	teardownTimer *time.Timer
	teardownGen   uint64
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

// NewProbeHost builds a Service for the probe-entry paths alone (startup sweep,
// uninstall): RemoveProbeHost needs nothing but NDMS, the ip host cache and the
// logger. The diagnostic checks are not usable on such an instance.
func NewProbeHost(ndmsClient ndmsClient, ipHost ipHostStore, appLogger logging.AppLogger) *Service {
	return &Service{
		ndms:   ndmsClient,
		ipHost: ipHost,
		appLog: logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubDnsCheck),
	}
}

// firstIPv4 — шов над чтением адреса интерфейса: тесты подменяют, прод зовёт
// netif.FirstIPv4.
var firstIPv4 = netif.FirstIPv4

// ensureIPHost creates the ip host entry for the probe domain. The entry maps
// awgm-dnscheck.test to the router's br0 IP so clients can verify their DNS
// goes through the router. It lives only for the duration of a check — see
// armProbeHost.
//
// The existing entry is inspected via /show/rc/ip/host first and left alone
// when it already matches — a blind POST at every startup made NDMS log
// 'Core::Configurator: not found: "ip/host/awgm-dnscheck.test"' because
// it resolved the leaf path before creating.
//
// The error is returned, not just logged: without the entry the probe cannot
// succeed, and its failure would otherwise read as "client uses external DNS".
func (s *Service) ensureIPHost(ctx context.Context) error {
	routerIP := firstIPv4("br0")
	if routerIP == "" {
		s.appLog.Warn("ensure-ip-host", probeDomain, "br0 has no IPv4, skipping")
		return errors.New("у br0 нет адреса IPv4")
	}
	if current, ok := s.lookupIPHost(ctx, probeDomain); ok && current == routerIP {
		return nil
	}
	if err := s.createIPHost(ctx, probeDomain, routerIP); err != nil {
		s.appLog.Warn("ensure-ip-host", probeDomain, fmt.Sprintf("failed to create %s -> %s: %v", probeDomain, routerIP, err))
		return err
	}
	s.appLog.Info("ensure-ip-host", probeDomain, fmt.Sprintf("created %s -> %s", probeDomain, routerIP))
	return nil
}

// lookupIPHost returns the configured address for the given domain, or
// ("", false) if not present. Errors are swallowed because a missing
// entry is indistinguishable from a transient NDMS hiccup at this level —
// the caller retries via createIPHost either way.
func (s *Service) lookupIPHost(ctx context.Context, domain string) (string, bool) {
	return s.ipHost.Lookup(ctx, domain)
}

// armProbeHost creates the probe entry and schedules its removal. Called at the
// END of Start, synchronously: the frontend fires its probe only after Start
// answers, so probeTeardownDelay has to be counted from that answer, not from
// the first server-side check.
//
// Teardown is a timer rather than a hook on the probe endpoint because the very
// case the check diagnoses — client DNS bypassing the router — means the probe
// never arrives. A repeated Start re-arms the timer.
//
// Creation and teardown share teardownMu, and the callback re-checks the
// generation under it. Stop() alone is not enough: it neither waits for a
// callback already inside RemoveProbeHost nor reports it in time, so a Start
// landing in that window would skip creation (the entry is still there) and
// then have it deleted under the running check.
func (s *Service) armProbeHost(ctx context.Context) error {
	s.teardownMu.Lock()
	defer s.teardownMu.Unlock()

	if s.teardownTimer != nil {
		s.teardownTimer.Stop()
	}
	s.teardownGen++
	gen := s.teardownGen

	if err := s.ensureIPHost(ctx); err != nil {
		return err
	}

	s.teardownTimer = time.AfterFunc(probeTeardownDelay, func() {
		// Своя ctx: request-scoped отменится, как только Start ответит клиенту.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		s.teardownMu.Lock()
		defer s.teardownMu.Unlock()
		if gen != s.teardownGen {
			return // пока ждали лок, проверку запустили заново
		}
		s.RemoveProbeHost(ctx)
	})
	return nil
}

// armProbeCheck arms the probe entry and returns the dns_probe row: pending
// when the entry is in place (the frontend fills the row in after its fetch),
// warning when arming failed — a probe without the entry always fails, and that
// failure would read as "the client uses external DNS".
func (s *Service) armProbeCheck(ctx context.Context) CheckResult {
	if err := s.armProbeHost(ctx); err != nil {
		return CheckResult{
			ID:      "dns_probe",
			Status:  "warning",
			Title:   "DNS-запрос к роутеру",
			Message: "Не удалось подготовить запись для проверки",
			Detail:  err.Error(),
		}
	}
	return CheckResult{
		ID:      "dns_probe",
		Status:  "pending",
		Title:   "DNS-запрос к роутеру",
		Message: "Ожидание DNS-запроса...",
	}
}

// RemoveProbeHost drops the probe entry if it is there. Exported for the
// uninstall path (cleanup.Service), which additionally persists the NDMS
// configuration — without that save the entry survives package removal in the
// startup config (#942).
//
// The lookup gate mirrors EnsureIPHost: deleting an absent record is an error
// for NDMS, not a no-op, and shows up in the router log as
// 'E Dns::Manager: no such record: "awgm-dnscheck.test", address .'
func (s *Service) RemoveProbeHost(ctx context.Context) error {
	if _, ok := s.lookupIPHost(ctx, probeDomain); !ok {
		return nil
	}
	if err := s.deleteIPHost(ctx, probeDomain); err != nil {
		s.appLog.Warn("remove-ip-host", probeDomain, fmt.Sprintf("failed to remove %s: %v", probeDomain, err))
		return err
	}
	s.appLog.Info("remove-ip-host", probeDomain, fmt.Sprintf("removed %s", probeDomain))
	return nil
}

// Start runs server-side checks (tunnel, routes, policy, encryption) and returns
// the results along with client info. Check 3 (DNS probe) is left pending —
// the frontend performs it directly via fetch to the probe domain.
func (s *Service) Start(ctx context.Context, clientIP string) (*StartResponse, error) {
	s.appLog.Info("start", clientIP, "DNS check started")
	hostname := s.resolveHostname(ctx, clientIP)

	tunnelCheck := s.checkTunnel(ctx)
	routesCheck := s.checkRoutes(ctx)
	policyCheck := s.checkPolicy(ctx, clientIP)
	encryptionCheck := s.checkEncryption(ctx)
	// Последней: окно жизни записи отсчитывается от ответа клиенту, а не от
	// начала серверных проверок — на холодных кэшах те стоят секунды.
	probeCheck := s.armProbeCheck(ctx)

	checks := []CheckResult{tunnelCheck, routesCheck, probeCheck, policyCheck, encryptionCheck}

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

// deleteIPHost removes an ip host entry via RCI.
//
// Address is deliberately omitted — that is the `no ip host <domain>` form,
// which clears EVERY address of the domain ("cleared "<domain>" records."),
// so a LAN address change between checks cannot leave an orphan behind.
// Verified on the 5.01.C.3.0-1 stand.
func (s *Service) deleteIPHost(ctx context.Context, domain string) error {
	payload := map[string]interface{}{
		"ip": map[string]interface{}{
			"host": map[string]interface{}{
				"domain": domain,
				"no":     true,
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
