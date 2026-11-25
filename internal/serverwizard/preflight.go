package serverwizard

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/cdn"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/egress"
	"github.com/hoaxisr/awg-manager/internal/sys/procnet"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
)

// PreflightEngine executes non-mutating readiness checks before wizard planning.
type PreflightEngine struct {
	xhttpdPath string
	procDir    string
	statFn     func(path string) (os.FileInfo, error)
	findProcFn func(procDir, addr string, port int) (procnet.ListenerLookup, error)
	xraySvc    XrayStateReader
	dispSvc    DispatcherStateReader
	tgSvc      TgWebProxyStateReader
	recReader  RecoveryStateReader
	egressAdp  *egress.Adapter
	keendns    serveringress.KeenDNSOperator
}

func NewPreflightEngine(
	xray XrayStateReader,
	disp DispatcherStateReader,
	tg TgWebProxyStateReader,
	rec RecoveryStateReader,
	egressAdp *egress.Adapter,
) *PreflightEngine {
	return &PreflightEngine{
		xhttpdPath: xhttpserver.DefaultBinaryPath,
		procDir:    "/proc",
		statFn:     os.Stat,
		findProcFn: procnet.FindListeningProcess,
		xraySvc:    xray,
		dispSvc:    disp,
		tgSvc:      tg,
		recReader:  rec,
		egressAdp:  egressAdp,
	}
}

// SetProcDir overrides procfs root path for testing.
func (pe *PreflightEngine) SetProcDir(dir string) {
	pe.procDir = dir
}

// SetStatFn overrides file stat probe for testing.
func (pe *PreflightEngine) SetStatFn(fn func(path string) (os.FileInfo, error)) {
	pe.statFn = fn
}

// SetFindProcFn overrides socket listener probe for testing.
func (pe *PreflightEngine) SetFindProcFn(fn func(procDir, addr string, port int) (procnet.ListenerLookup, error)) {
	pe.findProcFn = fn
}

// SetKeenDNS wires the KeenDNS operator for preflight conflict and availability checks.
func (pe *PreflightEngine) SetKeenDNS(k serveringress.KeenDNSOperator) {
	pe.keendns = k
}

// Run executes all checks for the specified wizard request and returns a consolidated report.
func (pe *PreflightEngine) Run(ctx context.Context, req WizardPlanRequest) PreflightResponse {
	var checks []PreflightCheck

	// 1. Check recovery required (fail-fast)
	if pe.recReader != nil {
		reqRec, reason := pe.recReader.IsRecoveryRequired()
		if reqRec {
			checks = append(checks, PreflightCheck{
				ID:          "system_recovery",
				Title:       "Целостность транзакций координатора",
				Status:      CheckStatusBlocked,
				Message:     "Система находится в состоянии требуемого восстановления (recovery_required)",
				Details:     reason,
				Remediation: "Выполните сброс или ручное завершение зависшей транзакции перед изменением конфигурации.",
			})
		} else {
			checks = append(checks, PreflightCheck{
				ID:      "system_recovery",
				Title:   "Целостность транзакций координатора",
				Status:  CheckStatusOK,
				Message: "Состояние транзакций стабильно, откатов не требуется",
			})
		}
	}

	// 2. Kind-specific checks
	switch req.Kind {
	case "tgwebproxy":
		pe.checkTelegram(ctx, req, &checks)
	case "xray":
		pe.checkXray(ctx, req, &checks)
	case "both":
		xr := req.forXray()
		tr := req.forTelegram()
		pe.checkXray(ctx, xr, &checks)
		if (req.UpstreamDevice == "firc:router" || req.UpstreamDevice == "router") && strings.TrimSpace(req.TgUpstreamDevice) == "" {
			checks = append(checks, PreflightCheck{
				ID:          "tg_upstream_required",
				Title:       "Маршрут выхода трафика Telegram",
				Status:      CheckStatusBlocked,
				Message:     "Для VLESS выбраны правила маршрутизации (FIRC), но маршрут выхода для Telegram не выбран",
				Remediation: "Явно выберите маршрут выхода трафика для прокси Telegram (прямой выход WAN или конкретный туннель).",
			})
		}
		pe.checkTelegram(ctx, tr, &checks)
		pe.checkCombinedPortCollisions(xr, tr, &checks)
	default:
		checks = append(checks, PreflightCheck{
			ID:          "wizard_kind",
			Title:       "Тип мастера настройки",
			Status:      CheckStatusBlocked,
			Message:     fmt.Sprintf("Неподдерживаемый тип мастера: %s", req.Kind),
			Remediation: "Выберите поддерживаемый тип: 'tgwebproxy', 'xray' или 'both'.",
		})
	}

	isTgDirectOnly := req.Kind == "tgwebproxy" && strings.TrimSpace(req.Scenario) == "direct_fake_tls"
	kdLabel := strings.ToLower(strings.TrimSpace(req.KeenDNSLabel))
	kdMode := strings.ToLower(strings.TrimSpace(req.KeenDNSMode))
	kdNonManual := kdMode != "" && kdMode != "manual"
	if isTgDirectOnly && (kdLabel != "" || kdNonManual) {
		checks = append(checks, PreflightCheck{
			ID:          "keendns_publication",
			Title:       "Публикация адреса KeenDNS",
			Status:      CheckStatusBlocked,
			Message:     "Домен 4-го уровня KeenDNS работает только для HTTP/HTTPS (Web Proxy) и неприменим для прямого MTProto",
			Remediation: "Для MTProto укажите белый IP роутера или прямой домен, либо переключитесь на сценарий Web Proxy / Оба способа.",
		})
	} else if kdLabel != "" {
		dispPort := req.DispatcherPort
		if dispPort <= 0 {
			dispPort = 9009
		}
		if pe.keendns == nil {
			checks = append(checks, PreflightCheck{
				ID:          "keendns_publication",
				Title:       "Публикация адреса KeenDNS",
				Status:      CheckStatusBlocked,
				Message:     "Создание адреса KeenDNS недоступно в данном окружении",
				Remediation: "Введите существующий адрес вручную.",
			})
		} else {
			rec, err := pe.keendns.PreparePublication(ctx, serveringress.KeenDNSPublicationCandidate{
				Label:        kdLabel,
				UpstreamHost: "127.0.0.1",
				UpstreamPort: dispPort,
			})
			if err != nil {
				checks = append(checks, PreflightCheck{
					ID:          "keendns_publication",
					Title:       "Публикация адреса KeenDNS",
					Status:      CheckStatusBlocked,
					Message:     err.Error(),
					Remediation: "Выберите другое короткое имя поддомена KeenDNS или введите существующий адрес вручную.",
				})
			} else if rec.ExistedBefore {
				checks = append(checks, PreflightCheck{
					ID:      "keendns_publication",
					Title:   "Публикация адреса KeenDNS",
					Status:  CheckStatusOK,
					Message: fmt.Sprintf("Существующий адрес %s будет переиспользован", rec.FQDN),
				})
			} else {
				checks = append(checks, PreflightCheck{
					ID:      "keendns_publication",
					Title:   "Публикация адреса KeenDNS",
					Status:  CheckStatusOK,
					Message: fmt.Sprintf("Будет создан новый адрес %s -> 127.0.0.1:%d", rec.FQDN, dispPort),
				})
			}
		}
	}

	canProceed := true
	hasWarnings := false
	for _, c := range checks {
		if c.Status == CheckStatusBlocked {
			canProceed = false
		} else if c.Status == CheckStatusWarning {
			hasWarnings = true
		}
	}

	return PreflightResponse{
		CanProceed:  canProceed,
		HasWarnings: hasWarnings,
		Checks:      checks,
	}
}

func (pe *PreflightEngine) checkTelegram(ctx context.Context, req WizardPlanRequest, checks *[]PreflightCheck) {
	scenario := strings.TrimSpace(req.Scenario)
	if scenario == "" {
		scenario = "dual"
	}

	// Binary checks
	// mtg и tproxy встроены в процесс: внешних бинарей нет, проверять нечего.
	if scenario != "cdn_http" {
		*checks = append(*checks, PreflightCheck{
			ID:      "bin_telemt",
			Title:   "Встроенное ядро mtg (MTProto Fake-TLS)",
			Status:  CheckStatusOK,
			Message: "Ядро mtg встроено в процесс awg-manager",
		})
	}
	if scenario != "direct_fake_tls" {
		*checks = append(*checks, PreflightCheck{
			ID:      "bin_tproxy",
			Title:   "Встроенный Web Proxy (tproxy)",
			Status:  CheckStatusOK,
			Message: "Модуль Web Proxy встроен в процесс awg-manager",
		})
	}

	// Ports checks
	directPort := req.DirectPort
	if directPort == 0 {
		directPort = 8443
	}
	webPort := resolveTelegramWebPort(req)
	dispPort := req.DispatcherPort
	if dispPort <= 0 {
		dispPort = 9009
	}
	xrayPort := 9008
	if pe.xraySvc != nil && !req.DisableOtherService {
		if xc := pe.xraySvc.GetConfig(); xc.ListenPort > 0 {
			xrayPort = xc.ListenPort
		}
	}

	selfPID := os.Getpid()
	var currentWebPID int
	var currentDirectPID int
	var currentDirectPort int
	var currentWebPort int
	var directOnline bool
	if pe.tgSvc != nil {
		st := pe.tgSvc.GetStatus()
		cfg := pe.tgSvc.GetConfig()
		currentWebPID = st.PID
		currentDirectPID = st.DirectPID
		directOnline = st.DirectOnline
		currentDirectPort = st.DirectPort
		if currentDirectPort <= 0 {
			currentDirectPort = cfg.DirectPort
		}
		currentWebPort = st.Port
		if currentWebPort <= 0 {
			currentWebPort = cfg.ListenPort
		}
	}
	if currentDirectPID == 0 {
		if pidBytes, err := os.ReadFile("/opt/var/run/telemt.pid"); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(pidBytes))); err == nil {
				currentDirectPID = p
				directOnline = true
			}
		}
	}

	allowedDirectPID := 0
	if currentDirectPID == selfPID {
		if directOnline && currentDirectPort > 0 && directPort == currentDirectPort {
			allowedDirectPID = currentDirectPID
		}
	} else if currentDirectPID > 0 && (currentDirectPort == 0 || directPort == currentDirectPort) {
		allowedDirectPID = currentDirectPID
	}

	allowedWebPID := 0
	if currentWebPID == selfPID {
		if currentWebPort > 0 && webPort == currentWebPort {
			allowedWebPID = currentWebPID
		}
	} else if currentWebPID > 0 && (currentWebPort == 0 || webPort == currentWebPort) {
		allowedWebPID = currentWebPID
	}

	if scenario != "cdn_http" {
		if req.DirectPort != 0 {
			if err := tgwebproxy.ValidateDirectPort(req.DirectPort); err != nil {
				*checks = append(*checks, PreflightCheck{
					ID:          "port_direct_valid",
					Title:       "Корректность порта MTProto",
					Status:      CheckStatusBlocked,
					Message:     err.Error(),
					Remediation: "Укажите свободный TCP-порт из диапазона 1024–65535, не занятый службами роутера.",
				})
			}
		}
		if scenario != "direct_fake_tls" && directPort == webPort {
			*checks = append(*checks, PreflightCheck{
				ID:          "port_collision_direct",
				Title:       "Проверка пересечения портов",
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Порт Telegram MTProto (%d) совпадает с портом Telegram Web (%d)", directPort, webPort),
				Remediation: "Укажите разные порты для прямого MTProto и веб-прокси Telegram.",
			})
		} else if directPort == 9008 || (pe.xraySvc != nil && pe.xraySvc.GetConfig().Enabled && !req.DisableOtherService && directPort == xrayPort) {
			*checks = append(*checks, PreflightCheck{
				ID:          "port_direct_conflict",
				Title:       fmt.Sprintf("Порт MTProto Direct (%d)", directPort),
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Порт %d конфликтует с VLESS (XHTTP)", directPort),
				Remediation: "Выберите другой порт для MTProto Direct (по умолчанию 8443).",
			})
		} else if directPort == dispPort || directPort == 8084 || directPort == 8086 || directPort == 2398 {
			*checks = append(*checks, PreflightCheck{
				ID:          "port_direct_conflict",
				Title:       fmt.Sprintf("Порт MTProto Direct (%d)", directPort),
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Порт %d занят внутренним сервисом роутера", directPort),
				Remediation: "Выберите другой порт для MTProto Direct (по умолчанию 8443).",
			})
		} else if directPort >= 1024 && directPort <= 65535 {
			pe.checkPort("port_direct", fmt.Sprintf("Порт прямого подключения (%d)", directPort), directPort, allowedDirectPID, checks)
		}
	}
	if scenario != "direct_fake_tls" {
		if webPort == 9008 || (pe.xraySvc != nil && pe.xraySvc.GetConfig().Enabled && !req.DisableOtherService && webPort == xrayPort) {
			*checks = append(*checks, PreflightCheck{
				ID:          "port_web",
				Title:       fmt.Sprintf("Порт веб-прокси (%d)", webPort),
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Порт %d зарезервирован для локального сервиса VLESS (XHTTP)", webPort),
				Remediation: "Используйте отдельный порт для Telegram Web Proxy (по умолчанию 8085).",
			})
		} else if webPort == dispPort || webPort == 8084 || webPort == 8086 || webPort == 2398 || (scenario == "dual" && webPort == directPort) {
			*checks = append(*checks, PreflightCheck{
				ID:          "port_web",
				Title:       fmt.Sprintf("Порт веб-прокси (%d)", webPort),
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Порт веб-прокси %d конфликтует с другим локальным портом сервиса", webPort),
				Remediation: "Используйте отдельный порт для Telegram Web Proxy (по умолчанию 8085).",
			})
		} else {
			pe.checkPort("port_web", fmt.Sprintf("Порт веб-прокси (%d)", webPort), webPort, allowedWebPID, checks)
		}
	}

	var kdDisc *serveringress.KeenDNSDiscovery
	if scenario != "cdn_http" && pe.keendns != nil {
		if disc, err := pe.keendns.Discover(ctx, dispPort); err == nil {
			kdDisc = &disc
		}
	}

	if req.DirectHost != "" {
		if err := ValidateDirectHost(req.DirectHost); err != nil {
			*checks = append(*checks, PreflightCheck{
				ID:          "direct_host_format",
				Title:       "Формат прямого хоста",
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Некорректный прямой хост: %v", err),
				Remediation: "Укажите публичный IP адрес роутера или домен (KeenDNS / DynDNS).",
			})
		} else if serveringress.IsPrivateOrCGNATIP(req.DirectHost) && scenario != "cdn_http" {
			*checks = append(*checks, PreflightCheck{
				ID:          "direct_host_private_ip",
				Title:       "Адрес прямого подключения MTProto",
				Status:      CheckStatusWarning,
				Message:     fmt.Sprintf("Адрес %s относится к частной/CGNAT подсети — для подключения Telegram из интернета требуется белый IP или прямой домен", req.DirectHost),
				Remediation: "Укажите белый публичный IP роутера, прямой домен KeenDNS 3-го уровня или переключитесь на сценарий Web Proxy.",
			})
		} else if scenario != "cdn_http" && isCloudKeenDNSHost(req.DirectHost, kdDisc) {
			*checks = append(*checks, PreflightCheck{
				ID:          "direct_host_keendns_cloud",
				Title:       "Адрес прямого подключения MTProto",
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Домен %s работает в облачном режиме KeenDNS (только HTTP/HTTPS) — прямое TCP-подключение MTProto через него недоступно", req.DirectHost),
				Remediation: "Переведите KeenDNS в режим «Прямой доступ» в настройках Keenetic (требуется белый IP), укажите другой белый IP/домен или переключитесь на сценарий Веб-мост (cdn_http).",
			})
		}
	}
	if scenario != "cdn_http" && kdDisc != nil && kdDisc.WANPrivate {
		wanAddr := strings.TrimSpace(kdDisc.WANAddress)
		wanMsg := fmt.Sprintf("На WAN-интерфейсе роутера серый (приватный/CGNAT) адрес — прямой TCP-порт %d MTProto из интернета недоступен без внешнего проброса порта (DMZ/VPS).", directPort)
		if wanAddr != "" {
			wanMsg = fmt.Sprintf("На WAN-интерфейсе роутера серый (приватный/CGNAT) адрес %s — прямой TCP-порт %d MTProto из интернета недоступен без внешнего проброса порта (DMZ/VPS).", wanAddr, directPort)
		}
		*checks = append(*checks, PreflightCheck{
			ID:          "wan_cgnat_direct_mtproto",
			Title:       "WAN-адрес роутера для прямого MTProto",
			Status:      CheckStatusWarning,
			Message:     wanMsg,
			Remediation: "Для роутера с серым IP используйте сценарий «Веб-мост (через домен)» через облако KeenDNS либо настройте проброс TCP-порта с внешнего белого IP/VPS.",
		})
	}
	if scenario != "direct_fake_tls" && req.PublicDomain != "" {
		if err := ValidatePublicHostname(req.PublicDomain); err != nil {
			*checks = append(*checks, PreflightCheck{
				ID:          "public_domain_format",
				Title:       "Формат публичного домена",
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Некорректный публичный домен: %v", err),
				Remediation: "Укажите корректный FQDN для CDN (например, tg.mydomain.com). Публичный домен не может быть IP-адресом.",
			})
		}
	}

	// CDN Profile check
	if req.CdnProfileID != "" {
		if _, ok := cdn.GetProfile(req.CdnProfileID); !ok {
			*checks = append(*checks, PreflightCheck{
				ID:          "cdn_profile",
				Title:       "Профиль CDN",
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Неподдерживаемый профиль CDN: %s", req.CdnProfileID),
				Remediation: "Выберите поддерживаемый профиль CDN (cdn_get, cdn_ws, cdn_full или direct).",
			})
		} else if scenario == "direct_fake_tls" && req.CdnProfileID != cdn.ProfileDirectID {
			*checks = append(*checks, PreflightCheck{
				ID:          "cdn_profile",
				Title:       "Профиль CDN",
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Профиль CDN %s несовместим с режимом прямого подключения MTProto", req.CdnProfileID),
				Remediation: "Выберите профиль direct или включите веб-мост Telegram.",
			})
		} else if scenario != "direct_fake_tls" && req.ConnectionWay == "cdn" && req.CdnProfileID == cdn.ProfileDirectID {
			*checks = append(*checks, PreflightCheck{
				ID:          "cdn_profile",
				Title:       "Профиль CDN",
				Status:      CheckStatusBlocked,
				Message:     "Для подключения через CDN выберите проксируемый профиль CDN (например, cdn_get)",
				Remediation: "Выберите профиль cdn_get, cdn_ws или cdn_full.",
			})
		}
	}

	// Egress upstream check
	if req.UpstreamDevice != "" {
		if egress.IsLegacyEgress(req.UpstreamDevice) {
			*checks = append(*checks, PreflightCheck{
				ID:          "upstream_device",
				Title:       "Маршрут выхода трафика",
				Status:      CheckStatusBlocked,
				Message:     egress.LegacyEgressLabel(),
				Remediation: "Выберите прямой выход (WAN) или активный туннель.",
			})
		} else if req.UpstreamDevice == "firc:router" || req.UpstreamDevice == "router" {
			*checks = append(*checks, PreflightCheck{
				ID:          "upstream_device",
				Title:       "Маршрут выхода трафика Telegram",
				Status:      CheckStatusBlocked,
				Message:     "Режим маршрутизации FIRC поддерживается только для VLESS (XHTTP); для Telegram выберите прямой выход (WAN) или конкретный туннель",
				Remediation: "Укажите маршрут выхода для Telegram (direct или имя туннельного интерфейса).",
			})
		} else if pe.egressAdp != nil {
			opt, ok := pe.egressAdp.FindOption(ctx, req.UpstreamDevice)
			if !ok {
				*checks = append(*checks, PreflightCheck{
					ID:          "upstream_device",
					Title:       "Маршрут выхода трафика",
					Status:      CheckStatusWarning,
					Message:     fmt.Sprintf("Указанный интерфейс '%s' не найден в каталоге", req.UpstreamDevice),
					Remediation: "Выберите существующий туннель или прямой выход (direct).",
				})
			} else if !opt.Available {
				*checks = append(*checks, PreflightCheck{
					ID:          "upstream_device",
					Title:       "Маршрут выхода трафика",
					Status:      CheckStatusBlocked,
					Message:     fmt.Sprintf("Интерфейс '%s' сейчас не готов: %s", req.UpstreamDevice, opt.DegradedMsg),
					Remediation: "Убедитесь, что выбранный туннель поднят и работоспособен.",
				})
			} else {
				*checks = append(*checks, PreflightCheck{
					ID:      "upstream_device",
					Title:   "Маршрут выхода трафика",
					Status:  CheckStatusOK,
					Message: fmt.Sprintf("Интерфейс '%s' активен и готов маршрутизировать трафик", opt.Name),
				})
			}
		}
	}
}

func (pe *PreflightEngine) checkXray(ctx context.Context, req WizardPlanRequest, checks *[]PreflightCheck) {
	// Mandatory C awgm-xhttpd binary check (never checks /opt/sbin/xray)
	if strings.TrimSpace(os.Getenv("AWGM_DISABLE_C_XHTTPD")) == "1" {
		*checks = append(*checks, PreflightCheck{
			ID:          "bin_xhttpd",
			Title:       "C-демон VLESS (awgm-xhttpd)",
			Status:      CheckStatusBlocked,
			Message:     "C-демон awgm-xhttpd отключён через AWGM_DISABLE_C_XHTTPD=1",
			Remediation: "Снимите переменную окружения AWGM_DISABLE_C_XHTTPD и перезапустите awg-manager.",
		})
	} else if pe.xraySvc != nil {
		st := pe.xraySvc.GetStatus()
		if st.UnavailableReason != "" {
			*checks = append(*checks, PreflightCheck{
				ID:          "bin_xhttpd",
				Title:       "C-демон VLESS (awgm-xhttpd)",
				Status:      CheckStatusBlocked,
				Message:     st.UnavailableReason,
				Remediation: "Переустановите пакет awg-manager, содержащий бинарный файл /opt/sbin/awgm-xhttpd.",
			})
		} else if st.Installed {
			ver := strings.TrimSpace(st.Version)
			if ver == "" {
				ver = "awgm-xhttpd"
			}
			*checks = append(*checks, PreflightCheck{
				ID:      "bin_xhttpd",
				Title:   "C-демон VLESS (awgm-xhttpd)",
				Status:  CheckStatusOK,
				Message: fmt.Sprintf("C-демон %s установлен и готов", ver),
			})
		} else {
			pe.checkBinary("bin_xhttpd", "C-демон VLESS (awgm-xhttpd)", pe.xhttpdPath, checks)
		}
	} else {
		pe.checkBinary("bin_xhttpd", "C-демон VLESS (awgm-xhttpd)", pe.xhttpdPath, checks)
	}

	// Reject unsupported WebSocket / legacy transport or mode for VLESS
	mode := strings.TrimSpace(req.Mode)
	if req.CdnProfileID == "cdn_ws" || strings.EqualFold(mode, "ws") ||
		(!req.PreserveTransport && mode != "" && mode != "xhttp_get") ||
		(req.PreserveTransport && strings.TrimSpace(req.RawTransport) != "" && !strings.EqualFold(strings.TrimSpace(req.RawTransport), "xhttp")) {
		*checks = append(*checks, PreflightCheck{
			ID:          "vless_transport",
			Title:       "Транспорт VLESS (XHTTP)",
			Status:      CheckStatusBlocked,
			Message:     fmt.Sprintf("Режим/транспорт (%q / %q) не поддерживается C-демоном awgm-xhttpd", req.CdnProfileID, mode),
			Remediation: "Выберите поддерживаемый профиль (cdn_get, cdn_full или direct) и режим xhttp_get.",
		})
	}

	// Ports checks
	listenPort := req.ListenPort
	if listenPort <= 0 {
		listenPort = 9008
	}
	dispPort := req.DispatcherPort
	if dispPort <= 0 {
		dispPort = 9009
	}

	var currentXrayPID int
	if pe.xraySvc != nil {
		currentXrayPID = pe.xraySvc.GetStatus().PID
	}

	var currentDispPID int
	if pe.dispSvc != nil && pe.dispSvc.IsRunning() {
		currentDispPID = os.Getpid()
	}

	if listenPort == dispPort {
		*checks = append(*checks, PreflightCheck{
			ID:          "port_collision_xray_disp",
			Title:       "Проверка пересечения портов",
			Status:      CheckStatusBlocked,
			Message:     fmt.Sprintf("Порт VLESS (XHTTP) (%d) совпадает с портом диспетчера (%d)", listenPort, dispPort),
			Remediation: "Укажите разные порты для VLESS (XHTTP) и входящего диспетчера.",
		})
	} else {
		pe.checkPort("port_xray", fmt.Sprintf("Локальный порт VLESS (XHTTP) (:%d)", listenPort), listenPort, currentXrayPID, checks)
	}
	pe.checkPort("port_dispatcher", fmt.Sprintf("Порт входящих подключений (:%d)", dispPort), dispPort, currentDispPID, checks)

	// Domain & Path check
	if req.PublicDomain != "" {
		if err := ValidatePublicHostname(req.PublicDomain); err != nil {
			*checks = append(*checks, PreflightCheck{
				ID:          "domain_format",
				Title:       "Формат публичного домена",
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Некорректный публичный домен: %v", err),
				Remediation: "Укажите корректный FQDN (например, cdn.mydomain.com). Публичный домен не может быть IP-адресом.",
			})
		} else {
			*checks = append(*checks, PreflightCheck{
				ID:      "domain_format",
				Title:   "Формат публичного домена",
				Status:  CheckStatusOK,
				Message: fmt.Sprintf("Публичный домен %s корректен", req.PublicDomain),
			})
		}
	}

	if req.Path != "" {
		if !strings.HasPrefix(req.Path, "/") {
			*checks = append(*checks, PreflightCheck{
				ID:          "path_format",
				Title:       "Формат пути сервиса",
				Status:      CheckStatusBlocked,
				Message:     "Путь должен начинаться с символа '/'",
				Remediation: "Укажите путь в формате '/cdn-bridge/'.",
			})
		}
	}

	// Outbound routing check (A1/A2: FIRC / tunnel / direct; never silently fall back or require Mihomo :1099)
	selectedEgress := strings.TrimSpace(req.UpstreamDevice)
	if egress.IsLegacyEgress(selectedEgress) {
		*checks = append(*checks, PreflightCheck{
			ID:          "egress_route",
			Title:       "Маршрут выхода трафика",
			Status:      CheckStatusBlocked,
			Message:     egress.LegacyEgressLabel(),
			Remediation: "Выберите прямой выход (WAN), конкретный туннель или правила маршрутизации (FIRC).",
		})
	} else if pe.egressAdp != nil {
		res, err := pe.egressAdp.Resolve(ctx, egress.ResolveRequest{
			ServerKind: "xray",
			EgressID:   selectedEgress,
		})
		if err != nil {
			*checks = append(*checks, PreflightCheck{
				ID:          "egress_route",
				Title:       "Маршрут выхода трафика",
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Выбранный маршрут выхода недоступен: %v", err),
				Remediation: "Выберите доступный маршрут выхода (WAN, активный туннель или правила FIRC).",
			})
		} else {
			msg := "Прямой выход в интернет (WAN) готов"
			if res.Mode == "router" {
				msg = "Маршрутизация через правила FIRC активна и готова"
			} else if res.Mode == "interface" {
				msg = fmt.Sprintf("Туннельный интерфейс %s активен и готов", res.Interface)
			}
			*checks = append(*checks, PreflightCheck{
				ID:      "egress_route",
				Title:   "Маршрут выхода трафика",
				Status:  CheckStatusOK,
				Message: msg,
			})
		}
	} else {
		*checks = append(*checks, PreflightCheck{
			ID:      "egress_route",
			Title:   "Маршрут выхода трафика",
			Status:  CheckStatusOK,
			Message: "Маршрут исходящего трафика готов",
		})
	}
}

func (pe *PreflightEngine) checkCombinedPortCollisions(xr, tr WizardPlanRequest, checks *[]PreflightCheck) {
	xrayPort := xr.ListenPort
	if xrayPort <= 0 {
		xrayPort = 9008
	}
	dispPort := xr.DispatcherPort
	if dispPort <= 0 {
		dispPort = 9009
	}
	tgWebPort := tr.ListenPort
	if tgWebPort <= 0 {
		tgWebPort = 8085
	}
	tgDirectPort := tr.DirectPort
	if tgDirectPort <= 0 {
		tgDirectPort = 8443
	}

	used := map[int]string{
		xrayPort: "VLESS (XHTTP)",
		dispPort: "Входящие подключения (:9009)",
	}
	scenario := tr.Scenario
	if scenario == "" {
		scenario = "dual"
	}
	if scenario == "cdn_http" || scenario == "dual" {
		if prev, ok := used[tgWebPort]; ok {
			*checks = append(*checks, PreflightCheck{
				ID:          "port_collision_web",
				Title:       "Проверка пересечения портов",
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Порт Telegram Web (%d) совпадает с портом %s", tgWebPort, prev),
				Remediation: "Укажите разные локальные порты для сервисов.",
			})
		}
		used[tgWebPort] = "Telegram Web"
	}
	if scenario == "direct_fake_tls" || scenario == "dual" {
		if prev, ok := used[tgDirectPort]; ok {
			*checks = append(*checks, PreflightCheck{
				ID:          "port_collision_direct",
				Title:       "Проверка пересечения портов",
				Status:      CheckStatusBlocked,
				Message:     fmt.Sprintf("Порт Telegram MTProto (%d) совпадает с портом %s", tgDirectPort, prev),
				Remediation: "Укажите разные порты для сервисов.",
			})
		}
	}
}

func (pe *PreflightEngine) checkBinary(id, title, path string, checks *[]PreflightCheck) {
	info, err := pe.statFn(path)
	if err != nil {
		*checks = append(*checks, PreflightCheck{
			ID:          id,
			Title:       title,
			Status:      CheckStatusBlocked,
			Message:     fmt.Sprintf("Файл не найден по пути %s", path),
			Details:     err.Error(),
			Remediation: fmt.Sprintf("Установите соответствующий пакет в Entware: %s", path),
		})
		return
	}
	if info.IsDir() {
		*checks = append(*checks, PreflightCheck{
			ID:          id,
			Title:       title,
			Status:      CheckStatusBlocked,
			Message:     fmt.Sprintf("По пути %s обнаружен каталог вместо исполняемого файла", path),
			Remediation: "Удалите каталог и установите исполняемый бинарный файл.",
		})
		return
	}

	*checks = append(*checks, PreflightCheck{
		ID:      id,
		Title:   title,
		Status:  CheckStatusOK,
		Message: fmt.Sprintf("Бинарный файл доступен (%d байт)", info.Size()),
	})
}

func (pe *PreflightEngine) checkPort(id, title string, port int, allowedPID int, checks *[]PreflightCheck) {
	lookup, err := pe.findProcFn(pe.procDir, "0.0.0.0", port)
	if err != nil {
		// Procnet failure must fail closed!
		*checks = append(*checks, PreflightCheck{
			ID:          id,
			Title:       title,
			Status:      CheckStatusBlocked,
			Message:     fmt.Sprintf("Не удалось проверить состояние порта %d в procfs", port),
			Details:     err.Error(),
			Remediation: "Проверьте доступность procfs и права процесса awg-manager.",
		})
		return
	}

	if !lookup.SocketFound {
		*checks = append(*checks, PreflightCheck{
			ID:      id,
			Title:   title,
			Status:  CheckStatusOK,
			Message: fmt.Sprintf("Порт %d свободен", port),
		})
		return
	}

	// Socket is found
	if allowedPID > 0 && lookup.PID == allowedPID {
		*checks = append(*checks, PreflightCheck{
			ID:      id,
			Title:   title,
			Status:  CheckStatusOK,
			Message: fmt.Sprintf("Порт %d занят текущим процессом службы (PID %d) и будет переконфигурирован", port, lookup.PID),
		})
		return
	}

	// Conflict with another process or unresolved PID
	pidInfo := "неизвестный PID"
	if lookup.PID > 0 {
		pidInfo = fmt.Sprintf("PID %d", lookup.PID)
	}
	*checks = append(*checks, PreflightCheck{
		ID:          id,
		Title:       title,
		Status:      CheckStatusBlocked,
		Message:     fmt.Sprintf("Конфликт порта: порт %d уже слушает сторонний процесс (%s)", port, pidInfo),
		Details:     fmt.Sprintf("Socket inode: %s", lookup.SocketInode),
		Remediation: fmt.Sprintf("Остановите конфликтующий процесс (%s) или выберите другой порт.", pidInfo),
	})
}

// isCloudKeenDNSHost reports whether host matches the router's cloud-mode KeenDNS base domain or any 4th-level subdomain under it.
func isCloudKeenDNSHost(host string, disc *serveringress.KeenDNSDiscovery) bool {
	if disc == nil || disc.State != "supported" || !strings.EqualFold(strings.TrimSpace(disc.AccessMode), "cloud") {
		return false
	}
	base := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(disc.BaseFQDN), "."))
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" || base == "" {
		return false
	}
	return h == base || strings.HasSuffix(h, "."+base)
}

// Ensure interface compatibility
var _ = net.ParseIP
