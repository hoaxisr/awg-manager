package dnscheck

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

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
	port    int

	mu     sync.Mutex
	tokens map[string]*tokenState
}

// NewService creates a new DNS check service and starts a background cleanup goroutine.
func NewService(ndmsClient ndms.Client, dns DnsRouteProvider, tunnels TunnelStateProvider, log *logger.Logger, port int) *Service {
	s := &Service{
		ndms:    ndmsClient,
		dns:     dns,
		tunnels: tunnels,
		log:     log,
		port:    port,
		tokens:  make(map[string]*tokenState),
	}
	go s.cleanupLoop()
	return s
}

// cleanupLoop periodically removes stale tokens.
func (s *Service) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.cleanupStale()
	}
}

// cleanupStale removes tokens older than 30 seconds.
func (s *Service) cleanupStale() {
	now := time.Now()
	s.mu.Lock()
	stale := make([]string, 0)
	for tok, st := range s.tokens {
		if now.Sub(st.createdAt) > 30*time.Second {
			stale = append(stale, tok)
		}
	}
	s.mu.Unlock()

	for _, tok := range stale {
		s.cleanup(tok)
	}
}

// Start runs checks 1, 2, 4, 5 server-side, creates an ip host entry, and returns
// a StartResponse with the token for check 3 (pending).
func (s *Service) Start(ctx context.Context, clientIP string) (*StartResponse, error) {
	token, err := generateToken()
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	routerIP := getBr0IP()
	hostname := s.resolveHostname(ctx, clientIP)

	domain := fmt.Sprintf("awgm-dnscheck-%s.test", token)

	checks := make([]CheckResult, 5)

	// Check 1: tunnel running
	checks[0] = s.checkTunnel(ctx)

	// Check 2: DNS routes configured
	checks[1] = s.checkRoutes(ctx)

	// Check 3: DNS probe (pending — client must call probe endpoint)
	checks[2] = CheckResult{
		ID:      "dns_probe",
		Status:  "pending",
		Title:   "DNS-запрос через туннель",
		Message: "Ожидание DNS-запроса...",
	}

	// Check 4: client policy
	checks[3] = s.checkPolicy(ctx, clientIP)

	// Check 5: encryption (DoT/DoH)
	checks[4] = s.checkEncryption(ctx)

	// Create ip host entry so the probe domain resolves to our router IP.
	if routerIP != "" {
		if err := s.createIPHost(ctx, domain, routerIP); err != nil {
			s.log.Warnf("dnscheck: failed to create ip host %s: %v", domain, err)
		}
	}

	st := &tokenState{
		token:     token,
		clientIP:  clientIP,
		hostname:  hostname,
		domain:    domain,
		routerIP:  routerIP,
		checks:    checks,
		createdAt: time.Now(),
	}

	s.mu.Lock()
	s.tokens[token] = st
	s.mu.Unlock()

	// Safety cleanup: remove token after 20 seconds regardless of Complete call.
	// Must be longer than probe timeout (3s) + network latency + Complete round-trip.
	go func() {
		time.Sleep(20 * time.Second)
		s.mu.Lock()
		_, exists := s.tokens[token]
		s.mu.Unlock()
		if exists {
			s.log.Infof("dnscheck: safety cleanup for token %s", token)
			s.cleanup(token)
		}
	}()

	return &StartResponse{
		Token:    token,
		ClientIP: clientIP,
		Hostname: hostname,
		Port:     s.port,
		Checks:   checks,
	}, nil
}

// MarkReached marks that the probe endpoint was hit for the given token.
func (s *Service) MarkReached(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.tokens[token]; ok {
		st.reached = true
	}
}

// Complete finalises the diagnostic, builds check 3 result, removes the ip host
// entry, and returns all 5 checks.
func (s *Service) Complete(ctx context.Context, token string, dnsReached bool) (*CompleteResponse, error) {
	s.mu.Lock()
	st, ok := s.tokens[token]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("unknown token: %s", token)
	}
	reached := st.reached || dnsReached
	checks := make([]CheckResult, len(st.checks))
	copy(checks, st.checks)
	s.mu.Unlock()

	// Build check 3 result.
	if reached {
		checks[2] = CheckResult{
			ID:      "dns_probe",
			Status:  "ok",
			Title:   "DNS-запрос через туннель",
			Message: "DNS-запрос успешно прошёл через туннель",
		}
	} else {
		checks[2] = CheckResult{
			ID:      "dns_probe",
			Status:  "fail",
			Title:   "DNS-запрос через туннель",
			Message: "DNS-запрос не достиг роутера через туннель",
			Detail:  "Клиент не сделал DNS-запрос к домену диагностики за отведённое время",
		}
	}

	// Cleanup token and ip host entry.
	s.cleanup(token)

	return &CompleteResponse{Checks: checks}, nil
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

	// Parse hotspot response: {"host": [{"ip": "...", "access": "...", ...}]}
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

// removeIPHost removes an ip host entry via RCI.
func (s *Service) removeIPHost(ctx context.Context, domain string) error {
	payload := map[string]interface{}{
		"ip": map[string]interface{}{
			"host": map[string]interface{}{
				domain: map[string]interface{}{
					"no": true,
				},
			},
		},
	}
	_, err := s.ndms.RCIPost(ctx, payload)
	return err
}

// cleanup removes a token from the map and deletes its ip host entry.
func (s *Service) cleanup(token string) {
	s.mu.Lock()
	st, ok := s.tokens[token]
	if ok {
		delete(s.tokens, token)
	}
	s.mu.Unlock()

	if ok && st.domain != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.removeIPHost(ctx, st.domain); err != nil {
			s.log.Warnf("dnscheck: failed to remove ip host %s: %v", st.domain, err)
		}
	}
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

// generateToken generates a random 4-byte hex token.
func generateToken() (string, error) {
	b := make([]byte, 4)
	// Use time-based pseudo-random to avoid importing crypto/rand
	// and to keep it simple; tokens are short-lived (10-30s).
	now := time.Now().UnixNano()
	b[0] = byte(now)
	b[1] = byte(now >> 8)
	b[2] = byte(now >> 16)
	b[3] = byte(now >> 24)
	return hex.EncodeToString(b), nil
}
