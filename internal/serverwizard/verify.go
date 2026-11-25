package serverwizard

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/egress"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
)

// TLSCertProbeFunc probes TLS certificate validity (hostname, validity window, chain) for a specific FQDN.
// Returns status ("ready", "pending", "invalid", "not_checked") and any verification error.
type TLSCertProbeFunc func(ctx context.Context, fqdn string) (string, error)

// VerifyLeafAndChainForFQDN strictly verifies that peerCerts contain a valid leaf certificate for fqdn:
// - non-empty certificate chain
// - current time within NotBefore..NotAfter
// - leaf.VerifyHostname(fqdn)
// - leaf.Verify(x509.VerifyOptions{DNSName: fqdn, Intermediates: ..., Roots: roots, CurrentTime: now})
func VerifyLeafAndChainForFQDN(fqdn string, peerCerts []*x509.Certificate, roots *x509.CertPool, now time.Time) error {
	fqdn = strings.TrimSpace(strings.TrimSuffix(fqdn, "."))
	if fqdn == "" {
		return errors.New("empty FQDN for certificate verification")
	}
	if len(peerCerts) == 0 || peerCerts[0] == nil {
		return fmt.Errorf("no peer certificate presented for %s", fqdn)
	}
	leaf := peerCerts[0]
	if now.IsZero() {
		now = time.Now()
	}
	if now.Before(leaf.NotBefore) {
		return fmt.Errorf("certificate for %s is not valid before %s", fqdn, leaf.NotBefore.UTC().Format(time.RFC3339))
	}
	if now.After(leaf.NotAfter) {
		return fmt.Errorf("certificate for %s expired at %s", fqdn, leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if err := leaf.VerifyHostname(fqdn); err != nil {
		return fmt.Errorf("certificate hostname mismatch for %s: %w", fqdn, err)
	}
	intermediates := x509.NewCertPool()
	for _, ic := range peerCerts[1:] {
		if ic != nil {
			intermediates.AddCert(ic)
		}
	}
	opts := x509.VerifyOptions{
		DNSName:       fqdn,
		Intermediates: intermediates,
		Roots:         roots,
		CurrentTime:   now,
	}
	if _, err := leaf.Verify(opts); err != nil {
		return fmt.Errorf("certificate chain verification failed for %s: %w", fqdn, err)
	}
	return nil
}

// DialAndVerifyFQDNCertificate connects to addr (or fqdn:443 if addr is empty) with TLS verification enabled
// (InsecureSkipVerify: false, ServerName: fqdn) and validates hostname, expiration, and chain.
func DialAndVerifyFQDNCertificate(ctx context.Context, fqdn string, addr string, roots *x509.CertPool) (string, error) {
	fqdn = strings.TrimSpace(strings.TrimSuffix(fqdn, "."))
	if fqdn == "" {
		return "not_checked", errors.New("empty FQDN")
	}
	target := strings.TrimSpace(addr)
	if target == "" {
		target = net.JoinHostPort(fqdn, "443")
	}

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 1500 * time.Millisecond},
		Config: &tls.Config{
			ServerName:         fqdn,
			InsecureSkipVerify: false,
			RootCAs:            roots,
			MinVersion:         tls.VersionTLS12,
		},
	}
	conn, err := dialer.DialContext(probeCtx, "tcp", target)
	if err != nil {
		var hostErr x509.HostnameError
		var certInvErr x509.CertificateInvalidError
		var unkAuthErr x509.UnknownAuthorityError
		if errors.As(err, &hostErr) || errors.As(err, &certInvErr) || errors.As(err, &unkAuthErr) || strings.Contains(strings.ToLower(err.Error()), "x509:") || strings.Contains(strings.ToLower(err.Error()), "tls:") {
			return "invalid", err
		}
		return "not_checked", err
	}
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return "not_checked", errors.New("connection is not a TLS connection")
	}
	state := tlsConn.ConnectionState()
	if err := VerifyLeafAndChainForFQDN(fqdn, state.PeerCertificates, roots, time.Now()); err != nil {
		return "invalid", err
	}
	return "ready", nil
}

const (
	VerificationScopeRouterAndDNS = "router_and_dns"
	VerificationScopeNote         = "Проверка выполняется с самого роутера: проверяются DNS-записи публичных доменов, готовность локальных служб, маршрутизация входящих подключений (:9009) и маршрут выхода трафика. Доступность снаружи через внешнюю сеть или мобильного оператора проверяется отдельно при подключении клиента."
)

// Verify executes post-configuration readiness and diagnostic checks without exposing secrets.
func (s *WizardService) Verify(ctx context.Context, kind string) (VerificationReport, error) {
	kind = strings.TrimSpace(kind)
	if kind != "xray" && kind != "tgwebproxy" && kind != "both" {
		return VerificationReport{}, fmt.Errorf("%w: unsupported verification kind: %s", ErrInvalidRequest, kind)
	}

	fp := ""
	if s.fingerprint != nil {
		fp = s.fingerprint.Compute(ctx)
	}

	var checks []VerificationCheck
	switch kind {
	case "xray":
		s.verifyXray(ctx, &checks)
	case "tgwebproxy":
		s.verifyTelegram(ctx, &checks)
	case "both":
		xrayEnabled := s.xray != nil && s.xray.GetConfig().Enabled
		tgEnabled := s.tg != nil && s.tg.GetConfig().Enabled
		if xrayEnabled && !tgEnabled {
			s.verifyXray(ctx, &checks)
			checks = append(checks, VerificationCheck{
				ID:      "tg_service_disabled",
				Service: "tg_mtproto",
				Title:   "Прокси Telegram",
				Status:  CheckStatusOK,
				Message: "Сервис не включён (проверка пропущена)",
			})
		} else if tgEnabled && !xrayEnabled {
			checks = append(checks, VerificationCheck{
				ID:      "xray_service_disabled",
				Service: "xray",
				Title:   "Сервер VLESS (XHTTP)",
				Status:  CheckStatusOK,
				Message: "Сервис не включён (проверка пропущена)",
			})
			s.verifyTelegram(ctx, &checks)
		} else {
			s.verifyXray(ctx, &checks)
			s.verifyTelegram(ctx, &checks)
		}
	}

	overall := CheckStatusOK
	for _, c := range checks {
		if c.Status == CheckStatusBlocked {
			overall = CheckStatusBlocked
			break
		}
		if c.Status == CheckStatusWarning && overall == CheckStatusOK {
			overall = CheckStatusWarning
		}
	}

	return VerificationReport{
		Kind:             kind,
		CheckedAt:        time.Now().UTC().Format(time.RFC3339),
		StateFingerprint: fp,
		Scope:            VerificationScopeRouterAndDNS,
		ScopeNote:        VerificationScopeNote,
		OverallStatus:    overall,
		Checks:           checks,
	}, nil
}

func (s *WizardService) verifyXray(ctx context.Context, checks *[]VerificationCheck) {
	if s.xray == nil {
		*checks = append(*checks, VerificationCheck{
			ID:          "xray_service",
			Service:     "xray",
			Title:       "Сервер VLESS (XHTTP)",
			Status:      CheckStatusBlocked,
			Category:    VerifyCategoryHTTPOrigin,
			Message:     "Служба VLESS (XHTTP) не инициализирована",
			Remediation: "Перезапустите awg-manager или примените настройку через мастер.",
		})
		return
	}

	cfg := s.xray.GetConfig()
	st := s.xray.GetStatus()
	listenPort := cfg.ListenPort
	if listenPort <= 0 {
		listenPort = 9008
	}
	dispPort := cfg.DispatcherPort
	if dispPort <= 0 {
		dispPort = 9009
	}
	pathPrefix := cdndispatcher.NormalizePathPrefix(cfg.Path)
	localTarget := fmt.Sprintf("127.0.0.1:%d", listenPort)

	// 1. Local VLESS listener check
	if !cfg.Enabled || !st.Running {
		*checks = append(*checks, VerificationCheck{
			ID:            "xray_listener",
			Service:       "xray",
			Title:         "Локальный сервер VLESS (XHTTP)",
			Status:        CheckStatusBlocked,
			Category:      VerifyCategoryHTTPOrigin,
			Message:       fmt.Sprintf("Сервер VLESS (XHTTP) остановлен (ожидается порт %d)", listenPort),
			Remediation:   "Включите сервер VLESS (XHTTP) или запустите мастер настройки.",
			TargetAddress: localTarget,
		})
	} else if err := s.probeTCP(localTarget); err != nil {
		cat := VerifyCategoryHTTPOrigin
		if isTimeoutError(err) {
			cat = VerifyCategoryTimeout
		}
		*checks = append(*checks, VerificationCheck{
			ID:            "xray_listener",
			Service:       "xray",
			Title:         "Локальный сервер VLESS (XHTTP)",
			Status:        CheckStatusBlocked,
			Category:      cat,
			Message:       fmt.Sprintf("Локальный порт %s не отвечает на подключение", localTarget),
			Remediation:   "Проверьте журнал ошибок сервера VLESS (XHTTP) и отсутствие конфликта портов.",
			TargetAddress: localTarget,
		})
	} else {
		*checks = append(*checks, VerificationCheck{
			ID:            "xray_listener",
			Service:       "xray",
			Title:         "Локальный сервер VLESS (XHTTP)",
			Status:        CheckStatusOK,
			Message:       fmt.Sprintf("Сервер принимает подключения на %s", localTarget),
			TargetAddress: localTarget,
		})
	}

	// 2. Active client credentials check (zero secret leakage)
	activeClients := 0
	for _, c := range cfg.Clients {
		if c.Enabled && strings.TrimSpace(c.ID) != "" {
			activeClients++
		}
	}
	if activeClients == 0 {
		*checks = append(*checks, VerificationCheck{
			ID:          "xray_auth",
			Service:     "xray",
			Title:       "Учётные записи клиентов VLESS",
			Status:      CheckStatusBlocked,
			Category:    VerifyCategoryAuth,
			Message:     "Нет ни одного активного клиента VLESS",
			Remediation: "Добавьте или включите хотя бы одного клиента в карточке сервера VLESS (XHTTP).",
		})
	} else {
		*checks = append(*checks, VerificationCheck{
			ID:      "xray_auth",
			Service: "xray",
			Title:   "Учётные записи клиентов VLESS",
			Status:  CheckStatusOK,
			Message: fmt.Sprintf("Активных профилей клиентов: %d", activeClients),
		})
	}

	// 3. Dispatcher (:9009) origin route check + 4. DNS resolution check for each configured domain
	domains := SplitPublicHostnames(cfg.PublicDomain)
	dispTarget := fmt.Sprintf("127.0.0.1:%d%s", dispPort, pathPrefix)
	if len(domains) == 0 {
		*checks = append(*checks, VerificationCheck{
			ID:          "xray_dispatcher",
			Service:     "dispatcher",
			Title:       "Маршрут входящих подключений (:9009)",
			Status:      CheckStatusBlocked,
			Category:    VerifyCategoryHTTPOrigin,
			Message:     "Не указан публичный домен для маршрутизации VLESS (XHTTP)",
			Remediation: "Укажите публичный домен в настройках сервера VLESS (XHTTP).",
		})
	} else {
		for idx, domain := range domains {
			dispID := "xray_dispatcher"
			dispTitle := "Маршрут входящих подключений (:9009)"
			dnsID := "xray_dns"
			dnsTitle := "DNS публичного домена VLESS"
			if len(domains) > 1 {
				dispID = fmt.Sprintf("xray_dispatcher_%d", idx+1)
				dispTitle = fmt.Sprintf("Маршрут входящих подключений (:9009) — %s", domain)
				dnsID = fmt.Sprintf("xray_dns_%d", idx+1)
				dnsTitle = fmt.Sprintf("DNS публичного домена VLESS — %s", domain)
			}

			routeHdr, err := s.probeDispatcherRoute(ctx, dispPort, domain, pathPrefix)
			if err != nil {
				cat := VerifyCategoryHTTPOrigin
				if isTimeoutError(err) {
					cat = VerifyCategoryTimeout
				}
				*checks = append(*checks, VerificationCheck{
					ID:            dispID,
					Service:       "dispatcher",
					Title:         dispTitle,
					Status:        CheckStatusBlocked,
					Category:      cat,
					Message:       fmt.Sprintf("Диспетчер (:%d) недоступен для домена %s", dispPort, domain),
					Remediation:   "Убедитесь, что служба входящих подключений (:9009) запущена.",
					TargetAddress: dispTarget,
				})
			} else if routeHdr != "xray" {
				*checks = append(*checks, VerificationCheck{
					ID:            dispID,
					Service:       "dispatcher",
					Title:         dispTitle,
					Status:        CheckStatusBlocked,
					Category:      VerifyCategoryHTTPOrigin,
					Message:       fmt.Sprintf("Запрос к %s%s не направляется в VLESS (получен маршрут %q)", domain, pathPrefix, routeHdr),
					Remediation:   "Проверьте правила доменов во «Входящих подключениях (:9009)» и совпадение пути сервиса.",
					TargetAddress: dispTarget,
				})
			} else {
				*checks = append(*checks, VerificationCheck{
					ID:            dispID,
					Service:       "dispatcher",
					Title:         dispTitle,
					Status:        CheckStatusOK,
					Message:       fmt.Sprintf("Запросы %s%s корректно направляются на VLESS (:%d)", domain, pathPrefix, listenPort),
					TargetAddress: dispTarget,
				})
			}

			s.verifyDomainDNS(ctx, dnsID, "xray", dnsTitle, domain, checks)
		}
	}

	// 5. Egress readiness check
	s.verifyXrayEgress(ctx, cfg.OutboundMode, cfg.OutboundInterface, checks)
}

func (s *WizardService) verifyTelegram(ctx context.Context, checks *[]VerificationCheck) {
	if s.tg == nil {
		*checks = append(*checks, VerificationCheck{
			ID:          "tg_service",
			Service:     "tg_mtproto",
			Title:       "Прокси Telegram",
			Status:      CheckStatusBlocked,
			Category:    VerifyCategoryHTTPOrigin,
			Message:     "Служба прокси Telegram не инициализирована",
			Remediation: "Перезапустите awg-manager или примените настройку через мастер.",
		})
		return
	}

	cfg := s.tg.GetConfig()
	st := s.tg.GetStatus()
	scenario := cfg.Scenario
	if scenario == "" {
		scenario = "dual"
	}

	// 1. MTProto (direct / Fake-TLS) check — independent from Web Proxy
	if scenario == "direct_fake_tls" || scenario == "dual" {
		directPort := cfg.DirectPort
		if directPort <= 0 {
			directPort = 8443
		}
		directTarget := fmt.Sprintf("127.0.0.1:%d", directPort)
		if !st.DirectOnline {
			*checks = append(*checks, VerificationCheck{
				ID:            "tg_mtproto_listener",
				Service:       "tg_mtproto",
				Title:         "Telegram: MTProto (прямое подключение)",
				Status:        CheckStatusBlocked,
				Category:      VerifyCategoryHTTPOrigin,
				Message:       fmt.Sprintf("Служба MTProto на порту %d не запущена", directPort),
				Remediation:   "Проверьте параметры прямого хоста и домена маскировки TLS, затем перезапустите службу.",
				TargetAddress: directTarget,
			})
		} else if err := s.probeTCP(directTarget); err != nil {
			cat := VerifyCategoryHTTPOrigin
			if isTimeoutError(err) {
				cat = VerifyCategoryTimeout
			}
			*checks = append(*checks, VerificationCheck{
				ID:            "tg_mtproto_listener",
				Service:       "tg_mtproto",
				Title:         "Telegram: MTProto (прямое подключение)",
				Status:        CheckStatusBlocked,
				Category:      cat,
				Message:       fmt.Sprintf("Локальный порт MTProto (%s) не отвечает на TCP-подключение", directTarget),
				Remediation:   "Убедитесь, что порт не занят другим процессом и служба MTProto активна.",
				TargetAddress: directTarget,
			})
		} else if strings.TrimSpace(cfg.TlsDomain) == "" || ValidateTlsDomain(cfg.TlsDomain) != nil {
			*checks = append(*checks, VerificationCheck{
				ID:            "tg_mtproto_listener",
				Service:       "tg_mtproto",
				Title:         "Telegram: MTProto (прямое подключение)",
				Status:        CheckStatusBlocked,
				Category:      VerifyCategoryTLS,
				Message:       "Не задан корректный домен маскировки TLS (Fake-TLS SNI)",
				Remediation:   "Укажите корректный домен маскировки TLS в настройках Telegram.",
				TargetAddress: directTarget,
			})
		} else {
			*checks = append(*checks, VerificationCheck{
				ID:            "tg_mtproto_listener",
				Service:       "tg_mtproto",
				Title:         "Telegram: MTProto (прямое подключение)",
				Status:        CheckStatusOK,
				Message:       fmt.Sprintf("Локальный порт MTProto (%d) принимает TCP-подключения; домен Fake-TLS SNI задан (%s). Внешняя доступность порта из интернета проверяется клиентом.", directPort, cfg.TlsDomain),
				TargetAddress: directTarget,
			})
		}

		var kdDisc *serveringress.KeenDNSDiscovery
		if s.keendns != nil {
			d := s.DiscoverKeenDNS(ctx)
			kdDisc = &d
		}

		directHost := strings.TrimSpace(cfg.DirectHost)
		if directHost != "" {
			if isCloudKeenDNSHost(directHost, kdDisc) {
				lookupFn := s.lookupHost
				if lookupFn == nil {
					lookupFn = net.DefaultResolver.LookupHost
				}
				dnsCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				addrs, lookupErr := lookupFn(dnsCtx, directHost)
				cancel()
				msg := fmt.Sprintf("Домен %s работает в облачном режиме KeenDNS (только HTTP/HTTPS). Прямое TCP-подключение MTProto через облачный KeenDNS невозможно.", directHost)
				if lookupErr == nil && len(addrs) > 0 {
					msg = fmt.Sprintf("Домен %s работает в облачном режиме KeenDNS (разрешается на облачный HTTP/HTTPS-прокси %s). Прямое TCP-подключение MTProto через облачный KeenDNS невозможно.", directHost, addrs[0])
				}
				*checks = append(*checks, VerificationCheck{
					ID:            "tg_mtproto_dns",
					Service:       "tg_mtproto",
					Title:         "DNS прямого адреса MTProto",
					Status:        CheckStatusBlocked,
					Category:      VerifyCategoryDNS,
					Message:       msg,
					Remediation:   "Переведите KeenDNS в режим «Прямой доступ» на белом IP, укажите другой прямой IP/домен либо переключите Telegram на режим «Веб-мост (через домен)».",
					TargetAddress: directHost,
				})
			} else if serveringress.IsPrivateOrCGNATIP(directHost) {
				*checks = append(*checks, VerificationCheck{
					ID:            "tg_mtproto_host",
					Service:       "tg_mtproto",
					Title:         "Прямой адрес MTProto",
					Status:        CheckStatusWarning,
					Category:      VerifyCategoryDNS,
					Message:       fmt.Sprintf("Прямой адрес MTProto (%s) относится к приватной/CGNAT подсети и недоступен клиентам из интернета.", directHost),
					Remediation:   "Укажите внешний белый IP/прямой домен либо переключитесь на «Веб-мост (через домен)».",
					TargetAddress: directHost,
				})
			} else if net.ParseIP(directHost) == nil {
				s.verifyDomainDNS(ctx, "tg_mtproto_dns", "tg_mtproto", "DNS прямого адреса MTProto", directHost, checks)
			}
		}

		if kdDisc != nil && kdDisc.WANPrivate {
			wanTarget := strings.TrimSpace(kdDisc.WANAddress)
			wanMsg := fmt.Sprintf("На WAN-интерфейсе роутера серый (приватный/CGNAT) адрес — прямые входящие TCP-подключения MTProto на порт %d из интернета недоступны без проброса порта с внешнего белого IP/VPS.", directPort)
			if wanTarget != "" {
				wanMsg = fmt.Sprintf("На WAN-интерфейсе роутера серый (приватный/CGNAT) адрес %s — прямые входящие TCP-подключения MTProto на порт %d из интернета недоступны без проброса порта с внешнего белого IP/VPS.", wanTarget, directPort)
			}
			*checks = append(*checks, VerificationCheck{
				ID:            "tg_mtproto_wan_ip",
				Service:       "tg_mtproto",
				Title:         "Внешний WAN-адрес для MTProto",
				Status:        CheckStatusWarning,
				Category:      VerifyCategoryFirewall,
				Message:       wanMsg,
				Remediation:   "Для роутера с серым IP используйте «Веб-мост (через домен)» через облако KeenDNS либо настройте проброс TCP-порта с внешнего белого IP/VPS.",
				TargetAddress: wanTarget,
			})
		}

		if tgwebproxy.IsDirectWANPortOpen(directPort) {
			*checks = append(*checks, VerificationCheck{
				ID:            "tg_mtproto_wan_firewall",
				Service:       "tg_mtproto",
				Title:         "WAN-фаервол для порта MTProto",
				Status:        CheckStatusOK,
				Message:       fmt.Sprintf("TCP-порт %d открыт в WAN-фаерволе роутера для входящих подключений MTProto", directPort),
				TargetAddress: fmt.Sprintf("0.0.0.0:%d", directPort),
			})
		} else {
			*checks = append(*checks, VerificationCheck{
				ID:            "tg_mtproto_wan_firewall",
				Service:       "tg_mtproto",
				Title:         "WAN-фаервол для порта MTProto",
				Status:        CheckStatusWarning,
				Category:      VerifyCategoryFirewall,
				Message:       fmt.Sprintf("TCP-порт %d не обнаружен в правилах WAN-фаервола Keenetic", directPort),
				Remediation:   "Перепримените настройки в мастере или перезапустите прокси Telegram для автоматического открытия порта.",
				TargetAddress: fmt.Sprintf("0.0.0.0:%d", directPort),
			})
		}
	}

	// 2. Web connection (CDN / Web Proxy + :9009 dispatcher) — independent from MTProto
	if scenario == "cdn_http" || scenario == "dual" {
		webPort := cfg.ListenPort
		if webPort <= 0 {
			webPort = 8085
		}
		webTarget := fmt.Sprintf("127.0.0.1:%d", webPort)
		if !st.WebProxyOnline {
			*checks = append(*checks, VerificationCheck{
				ID:            "tg_web_listener",
				Service:       "tg_web",
				Title:         "Telegram: Веб-подключение (локальная служба)",
				Status:        CheckStatusBlocked,
				Category:      VerifyCategoryHTTPOrigin,
				Message:       fmt.Sprintf("Веб-прокси Telegram на порту %d не запущен", webPort),
				Remediation:   "Включите веб-прокси Telegram или перезапустите службу.",
				TargetAddress: webTarget,
			})
		} else if err := s.probeTCP(webTarget); err != nil {
			cat := VerifyCategoryHTTPOrigin
			if isTimeoutError(err) {
				cat = VerifyCategoryTimeout
			}
			*checks = append(*checks, VerificationCheck{
				ID:            "tg_web_listener",
				Service:       "tg_web",
				Title:         "Telegram: Веб-подключение (локальная служба)",
				Status:        CheckStatusBlocked,
				Category:      cat,
				Message:       fmt.Sprintf("Порт веб-прокси (%s) не отвечает", webTarget),
				Remediation:   "Проверьте состояние процесса tproxy-server и порт 8085.",
				TargetAddress: webTarget,
			})
		} else {
			*checks = append(*checks, VerificationCheck{
				ID:            "tg_web_listener",
				Service:       "tg_web",
				Title:         "Telegram: Веб-подключение (локальная служба)",
				Status:        CheckStatusOK,
				Message:       fmt.Sprintf("Веб-прокси принимает запросы на %s", webTarget),
				TargetAddress: webTarget,
			})
		}

		tgDomains := SplitPublicHostnames(cfg.PublicHostname)
		dispPort := 9009
		dispTarget := fmt.Sprintf("127.0.0.1:%d/", dispPort)
		if len(tgDomains) == 0 {
			*checks = append(*checks, VerificationCheck{
				ID:          "tg_web_dispatcher",
				Service:     "tg_web",
				Title:       "Telegram: Маршрут входящих подключений (:9009)",
				Status:      CheckStatusBlocked,
				Category:    VerifyCategoryHTTPOrigin,
				Message:     "Не указан публичный домен для веб-подключения Telegram",
				Remediation: "Укажите публичный домен в настройках Telegram.",
			})
		} else {
			for idx, pubHost := range tgDomains {
				dispID := "tg_web_dispatcher"
				dispTitle := "Telegram: Маршрут входящих подключений (:9009)"
				dnsID := "tg_web_dns"
				dnsTitle := "DNS публичного домена Telegram"
				if len(tgDomains) > 1 {
					dispID = fmt.Sprintf("tg_web_dispatcher_%d", idx+1)
					dispTitle = fmt.Sprintf("Telegram: Маршрут входящих подключений (:9009) — %s", pubHost)
					dnsID = fmt.Sprintf("tg_web_dns_%d", idx+1)
					dnsTitle = fmt.Sprintf("DNS публичного домена Telegram — %s", pubHost)
				}

				routeHdr, err := s.probeDispatcherRoute(ctx, dispPort, pubHost, "/")
				if err != nil {
					cat := VerifyCategoryHTTPOrigin
					if isTimeoutError(err) {
						cat = VerifyCategoryTimeout
					}
					*checks = append(*checks, VerificationCheck{
						ID:            dispID,
						Service:       "tg_web",
						Title:         dispTitle,
						Status:        CheckStatusBlocked,
						Category:      cat,
						Message:       fmt.Sprintf("Диспетчер (:%d) недоступен для домена %s", dispPort, pubHost),
						Remediation:   "Убедитесь, что служба входящих подключений (:9009) включена.",
						TargetAddress: dispTarget,
					})
				} else if routeHdr != "tgwebproxy" {
					*checks = append(*checks, VerificationCheck{
						ID:            dispID,
						Service:       "tg_web",
						Title:         dispTitle,
						Status:        CheckStatusBlocked,
						Category:      VerifyCategoryHTTPOrigin,
						Message:       fmt.Sprintf("Запрос к %s/ не направляется в веб-прокси Telegram (маршрут %q)", pubHost, routeHdr),
						Remediation:   "Включите маршрут Telegram для домена во «Входящих подключениях (:9009)».",
						TargetAddress: dispTarget,
					})
				} else {
					*checks = append(*checks, VerificationCheck{
						ID:            dispID,
						Service:       "tg_web",
						Title:         dispTitle,
						Status:        CheckStatusOK,
						Message:       fmt.Sprintf("Запросы к %s/ корректно направляются в веб-прокси (:%d)", pubHost, webPort),
						TargetAddress: dispTarget,
					})
				}

				s.verifyDomainDNS(ctx, dnsID, "tg_web", dnsTitle, pubHost, checks)
			}
		}
	}

	// 3. Telegram Egress readiness check
	if len(cfg.Upstreams) > 0 {
		s.verifyTelegramUpstreams(ctx, tgwebproxy.UpstreamSpecsFromPublic(cfg.Upstreams), checks)
	} else {
		s.verifyTelegramEgress(ctx, cfg.UpstreamDevice, checks)
	}
}

func (s *WizardService) verifyXrayEgress(ctx context.Context, mode, iface string, checks *[]VerificationCheck) {
	if egress.IsLegacyEgress(mode) || egress.IsLegacyEgress(iface) {
		*checks = append(*checks, VerificationCheck{
			ID:          "xray_egress",
			Service:     "egress",
			Title:       "Маршрут выхода трафика VLESS",
			Status:      CheckStatusBlocked,
			Category:    VerifyCategoryEgress,
			Message:     egress.LegacyEgressLabel(),
			Remediation: "Выберите прямой выход (WAN), конкретный туннель или правила маршрутизации (FIRC).",
		})
		return
	}

	egressID := "direct"
	if mode == "router" {
		egressID = "firc:router"
	} else if mode == "interface" && iface != "" {
		egressID = iface
	}

	if s.egress != nil {
		res, err := s.egress.Resolve(ctx, egress.ResolveRequest{
			ServerKind: "xray",
			EgressID:   egressID,
		})
		if err != nil {
			*checks = append(*checks, VerificationCheck{
				ID:          "xray_egress",
				Service:     "egress",
				Title:       "Маршрут выхода трафика VLESS",
				Status:      CheckStatusBlocked,
				Category:    VerifyCategoryEgress,
				Message:     fmt.Sprintf("Маршрут выхода недоступен: %v", err),
				Remediation: "Проверьте активность выбранного туннеля или правил маршрутизации (FIRC).",
			})
			return
		}
		msg := "Прямой выход в интернет (WAN) активен"
		if res.Mode == "router" {
			msg = "Используются правила маршрутизации (FIRC)"
		} else if res.Mode == "interface" {
			msg = fmt.Sprintf("Используется туннельный интерфейс %s", res.Interface)
		}
		*checks = append(*checks, VerificationCheck{
			ID:      "xray_egress",
			Service: "egress",
			Title:   "Маршрут выхода трафика VLESS",
			Status:  CheckStatusOK,
			Message: msg,
		})
		return
	}

	if mode == "router" && s.egress == nil {
		*checks = append(*checks, VerificationCheck{
			ID:          "xray_egress",
			Service:     "egress",
			Title:       "Маршрут выхода трафика VLESS",
			Status:      CheckStatusBlocked,
			Category:    VerifyCategoryEgress,
			Message:     "Невозможно подтвердить готовность правил маршрутизации (FIRC): провайдер состояния не подключён",
			Remediation: "Проверьте работу службы FIRC или выберите прямой выход (WAN).",
		})
		return
	}

	*checks = append(*checks, VerificationCheck{
		ID:      "xray_egress",
		Service: "egress",
		Title:   "Маршрут выхода трафика VLESS",
		Status:  CheckStatusOK,
		Message: "Маршрут выхода готов",
	})
}

func (s *WizardService) verifyTelegramEgress(ctx context.Context, upstream string, checks *[]VerificationCheck) {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" || upstream == "direct" {
		*checks = append(*checks, VerificationCheck{
			ID:      "tg_egress",
			Service: "egress",
			Title:   "Маршрут выхода трафика Telegram",
			Status:  CheckStatusOK,
			Message: "Прямой выход в интернет (WAN) активен",
		})
		return
	}
	if egress.IsLegacyEgress(upstream) {
		*checks = append(*checks, VerificationCheck{
			ID:          "tg_egress",
			Service:     "egress",
			Title:       "Маршрут выхода трафика Telegram",
			Status:      CheckStatusBlocked,
			Category:    VerifyCategoryEgress,
			Message:     egress.LegacyEgressLabel(),
			Remediation: "Выберите прямой выход (WAN) или активный туннель.",
		})
		return
	}
	if s.egress != nil {
		res, err := s.egress.Resolve(ctx, egress.ResolveRequest{
			ServerKind: "tgwebproxy",
			EgressID:   upstream,
		})
		if err != nil {
			*checks = append(*checks, VerificationCheck{
				ID:          "tg_egress",
				Service:     "egress",
				Title:       "Маршрут выхода трафика Telegram",
				Status:      CheckStatusBlocked,
				Category:    VerifyCategoryEgress,
				Message:     fmt.Sprintf("Интерфейс выхода %s недоступен: %v", upstream, err),
				Remediation: "Проверьте состояние выбранного туннеля или переключите выход на прямой (WAN).",
			})
			return
		}
		*checks = append(*checks, VerificationCheck{
			ID:      "tg_egress",
			Service: "egress",
			Title:   "Маршрут выхода трафика Telegram",
			Status:  CheckStatusOK,
			Message: fmt.Sprintf("Используется туннельный интерфейс %s", res.Interface),
		})
		return
	}
	*checks = append(*checks, VerificationCheck{
		ID:      "tg_egress",
		Service: "egress",
		Title:   "Маршрут выхода трафика Telegram",
		Status:  CheckStatusOK,
		Message: fmt.Sprintf("Используется интерфейс %s", upstream),
	})
}

func (s *WizardService) verifyTelegramUpstreams(ctx context.Context, upstreams []tgwebproxy.UpstreamSpec, checks *[]VerificationCheck) {
	norm := tgwebproxy.NormalizeUpstreams(upstreams)
	var enabled []tgwebproxy.UpstreamSpec
	for _, u := range norm {
		if u.Enabled {
			enabled = append(enabled, u)
		}
	}
	if len(enabled) == 0 {
		*checks = append(*checks, VerificationCheck{
			ID:          "tg_egress",
			Service:     "egress",
			Title:       "Выходные каналы трафика Telegram",
			Status:      CheckStatusBlocked,
			Category:    VerifyCategoryEgress,
			Message:     "Не включён ни один выходной upstream-канал",
			Remediation: "Включите хотя бы один канал (Direct или SOCKS5) в настройках выхода Telegram.",
		})
		return
	}

	okCount := 0
	var okLabels, badLabels []string
	for _, u := range enabled {
		if u.Type == tgwebproxy.UpstreamTypeSocks5 {
			label := fmt.Sprintf("SOCKS5 %s", u.Address)
			dialFn := s.dialTimeout
			if dialFn == nil {
				dialFn = net.DialTimeout
			}
			conn, err := dialFn("tcp", u.Address, 1500*time.Millisecond)
			if err != nil {
				badLabels = append(badLabels, fmt.Sprintf("%s (%v)", label, err))
				continue
			}
			_ = conn.Close()
			okCount++
			okLabels = append(okLabels, fmt.Sprintf("%s (вес %d)", label, u.Weight))
			continue
		}

		dev := strings.TrimSpace(u.Device)
		if dev == "" || dev == "direct" {
			okCount++
			okLabels = append(okLabels, fmt.Sprintf("Direct WAN (вес %d)", u.Weight))
			continue
		}
		if s.egress != nil {
			res, err := s.egress.Resolve(ctx, egress.ResolveRequest{
				ServerKind: "tgwebproxy",
				EgressID:   dev,
			})
			if err != nil {
				badLabels = append(badLabels, fmt.Sprintf("%s (%v)", dev, err))
				continue
			}
			okCount++
			okLabels = append(okLabels, fmt.Sprintf("%s (вес %d)", res.Interface, u.Weight))
			continue
		}
		okCount++
		okLabels = append(okLabels, fmt.Sprintf("%s (вес %d)", dev, u.Weight))
	}

	switch {
	case okCount == len(enabled):
		*checks = append(*checks, VerificationCheck{
			ID:      "tg_egress",
			Service: "egress",
			Title:   "Выходные каналы трафика Telegram",
			Status:  CheckStatusOK,
			Message: fmt.Sprintf("Все активные каналы (%d) готовы: %s", okCount, strings.Join(okLabels, ", ")),
		})
	case okCount > 0:
		*checks = append(*checks, VerificationCheck{
			ID:          "tg_egress",
			Service:     "egress",
			Title:       "Выходные каналы трафика Telegram",
			Status:      CheckStatusWarning,
			Category:    VerifyCategoryEgress,
			Message:     fmt.Sprintf("Доступно %d из %d каналов (%s); недоступны: %s", okCount, len(enabled), strings.Join(okLabels, ", "), strings.Join(badLabels, "; ")),
			Remediation: "Проверьте недоступные туннели или SOCKS5-прокси — сейчас работает резервный канал.",
		})
	default:
		*checks = append(*checks, VerificationCheck{
			ID:          "tg_egress",
			Service:     "egress",
			Title:       "Выходные каналы трафика Telegram",
			Status:      CheckStatusBlocked,
			Category:    VerifyCategoryEgress,
			Message:     fmt.Sprintf("Все выбранные каналы недоступны: %s", strings.Join(badLabels, "; ")),
			Remediation: "Проверьте туннели/SOCKS5-прокси или добавьте прямой канал WAN.",
		})
	}
}

func (s *WizardService) verifyDomainDNS(ctx context.Context, id, service, title, domain string, checks *[]VerificationCheck) {
	lookupFn := s.lookupHost
	if lookupFn == nil {
		lookupFn = net.DefaultResolver.LookupHost
	}
	dnsCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	addrs, err := lookupFn(dnsCtx, domain)
	if err != nil || len(addrs) == 0 {
		cat := VerifyCategoryDNS
		if err != nil && isTimeoutError(err) {
			cat = VerifyCategoryTimeout
		}
		*checks = append(*checks, VerificationCheck{
			ID:            id,
			Service:       service,
			Title:         title,
			Status:        CheckStatusWarning,
			Category:      cat,
			Message:       fmt.Sprintf("Не удалось разрешить DNS-имя %s с роутера", domain),
			Remediation:   "Проверьте наличие A/AAAA или CNAME записи для домена у вашего DNS/CDN провайдера.",
			TargetAddress: domain,
		})
		return
	}

	*checks = append(*checks, VerificationCheck{
		ID:            id,
		Service:       service,
		Title:         title,
		Status:        CheckStatusOK,
		Message:       fmt.Sprintf("Домен %s успешно разрешается (%s)", domain, addrs[0]),
		TargetAddress: domain,
	})
}

func (s *WizardService) probeTCP(target string) error {
	dialFn := s.dialTimeout
	if dialFn == nil {
		dialFn = net.DialTimeout
	}
	conn, err := dialFn("tcp", target, 300*time.Millisecond)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func (s *WizardService) probeDispatcherRoute(ctx context.Context, dispPort int, host, path string) (string, error) {
	dialFn := s.dialTimeout
	if dialFn == nil {
		dialFn = net.DialTimeout
	}
	client := s.httpDoer
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
				DialContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
					return dialFn(network, addr, 300*time.Millisecond)
				},
			},
			Timeout: 600 * time.Millisecond,
		}
	}
	urlStr := fmt.Sprintf("http://127.0.0.1:%d%s", dispPort, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return "", err
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return resp.Header.Get("X-CDN-Route"), nil
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")
}
