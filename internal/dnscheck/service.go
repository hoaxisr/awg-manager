package dnscheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
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

// ndmsClient is the subset of ndms.Client used by this service.
type ndmsClient interface {
	RCIPost(ctx context.Context, payload interface{}) (json.RawMessage, error)
	RCIGet(ctx context.Context, path string) (json.RawMessage, error)
}

// compile-time check: ndms.Client must satisfy ndmsClient
var _ ndmsClient = (ndms.Client)(nil)

// Service runs DNS routing diagnostic checks.
type Service struct {
	ndms    ndmsClient
	dns     DnsRouteProvider
	tunnels TunnelStateProvider
	log     *logger.Logger
}

// NewService creates a new DNS check service.
func NewService(ndmsClient ndms.Client, dns DnsRouteProvider, tunnels TunnelStateProvider, log *logger.Logger) *Service {
	return &Service{
		ndms:    ndmsClient,
		dns:     dns,
		tunnels: tunnels,
		log:     log,
	}
}

// EnsureIPHost creates a permanent ip host entry for the probe domain.
// Called once at startup. The entry maps awgm-dnscheck.test to the router's
// br0 IP so clients can verify their DNS goes through the router.
//
// The existing entry is inspected via /show/rc/ip/host first and left alone
// when it already matches — a blind POST at every startup made NDMS log
// 'Core::Configurator: not found: "ip/host/awgm-dnscheck.test"' because
// it resolved the leaf path before creating.
func (s *Service) EnsureIPHost(ctx context.Context) {
	routerIP := getBr0IP()
	if routerIP == "" {
		s.log.Warnf("dnscheck: br0 has no IPv4, skipping ip host setup")
		return
	}
	if current, ok := s.lookupIPHost(ctx, probeDomain); ok && current == routerIP {
		return
	}
	if err := s.createIPHost(ctx, probeDomain, routerIP); err != nil {
		s.log.Warnf("dnscheck: failed to create ip host %s -> %s: %v", probeDomain, routerIP, err)
		return
	}
	s.log.Infof("dnscheck: ip host %s -> %s", probeDomain, routerIP)
}

// lookupIPHost returns the configured address for the given domain, or
// ("", false) if not present. Errors are swallowed because a missing
// entry is indistinguishable from a transient NDMS hiccup at this level —
// the caller retries via createIPHost either way.
func (s *Service) lookupIPHost(ctx context.Context, domain string) (string, bool) {
	raw, err := s.ndms.RCIGet(ctx, "/show/rc/ip/host")
	if err != nil {
		return "", false
	}
	var entries []struct {
		Domain  string `json:"domain"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.Domain == domain {
			return e.Address, true
		}
	}
	return "", false
}

// Start runs server-side checks (tunnel, routes, policy, encryption) and returns
// the results along with client info. Check 3 (DNS probe) is left pending —
// the frontend performs it directly via fetch to the probe domain.
func (s *Service) Start(ctx context.Context, clientIP string) (*StartResponse, error) {
	hostname := s.resolveHostname(ctx, clientIP)

	checks := []CheckResult{
		s.checkTunnel(ctx),
		s.checkRoutes(ctx),
		{ID: "dns_probe", Status: "pending", Title: "DNS-запрос к роутеру", Message: "Ожидание DNS-запроса..."},
		s.checkPolicy(ctx, clientIP),
		s.checkEncryption(ctx),
	}

	return &StartResponse{
		ClientIP: clientIP,
		Hostname: hostname,
		Checks:   checks,
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
	raw, err := s.ndms.RCIGet(ctx, "/show/ip/hotspot")
	if err != nil {
		return CheckResult{
			ID:      "client_policy",
			Status:  "warning",
			Title:   "Политика доступа клиента",
			Message: "Не удалось получить список клиентов",
			Detail:  err.Error(),
		}
	}

	var hotspot struct {
		Host []struct {
			IP     string `json:"ip"`
			Access string `json:"access"`
			Name   string `json:"name"`
		} `json:"host"`
	}
	if err := json.Unmarshal(raw, &hotspot); err != nil {
		return CheckResult{
			ID:      "client_policy",
			Status:  "warning",
			Title:   "Политика доступа клиента",
			Message: "Не удалось разобрать список клиентов",
			Detail:  err.Error(),
		}
	}

	for _, h := range hotspot.Host {
		if h.IP == clientIP {
			if h.Access != "" {
				return CheckResult{
					ID:      "client_policy",
					Status:  "ok",
					Title:   "Политика доступа клиента",
					Message: fmt.Sprintf("Клиент использует политику: %s", h.Access),
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
	raw, err := s.ndms.RCIGet(ctx, "/show/rc/dns-proxy")
	if err != nil {
		return CheckResult{
			ID:      "dns_encryption",
			Status:  "warning",
			Title:   "Шифрование DNS",
			Message: "Не удалось получить конфигурацию DNS-прокси",
			Detail:  err.Error(),
		}
	}

	lower := strings.ToLower(string(raw))
	keywords := []string{"dot", "tls", "doh", "https"}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return CheckResult{
				ID:      "dns_encryption",
				Status:  "ok",
				Title:   "Шифрование DNS",
				Message: "DNS-прокси использует зашифрованный транспорт",
			}
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
func (s *Service) createIPHost(ctx context.Context, domain, address string) error {
	payload := map[string]interface{}{
		"ip": map[string]interface{}{
			"host": map[string]interface{}{
				domain: map[string]interface{}{
					"address": address,
				},
			},
		},
	}
	_, err := s.ndms.RCIPost(ctx, payload)
	return err
}

// resolveHostname looks up the client hostname from the hotspot list.
func (s *Service) resolveHostname(ctx context.Context, ip string) string {
	raw, err := s.ndms.RCIGet(ctx, "/show/ip/hotspot")
	if err != nil {
		return ip
	}
	var hotspot struct {
		Host []struct {
			IP       string `json:"ip"`
			Name     string `json:"name"`
			Hostname string `json:"hostname"`
		} `json:"host"`
	}
	if err := json.Unmarshal(raw, &hotspot); err != nil {
		return ip
	}
	for _, h := range hotspot.Host {
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

// getBr0IP returns the first IPv4 address of the br0 interface.
func getBr0IP() string {
	iface, err := net.InterfaceByName("br0")
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.To4() != nil {
			return ip.To4().String()
		}
	}
	return ""
}
