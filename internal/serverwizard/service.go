package serverwizard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/cdn"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/egress"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

type CoordinatorBridge interface {
	WithIngressLock(txID string, fn func() error) error
	ExecuteIngressTransaction(ctx context.Context, params serveringress.IngressTransactionParams) error
	IsRecoveryRequired() (bool, string)
}

type XrayServerComponent interface {
	GetConfig() vlessserver.Config
	GetStatus() vlessserver.Status
	GenerateLinks(clientID string) (*vlessserver.ShareLinks, error)
}

type TgWebProxyComponent interface {
	GetConfig() tgwebproxy.PublicConfig
	GetStatus() tgwebproxy.Status
	RevealSecret() (tgwebproxy.RevealData, error)
}

type DispatcherComponent interface {
	GetConfig() cdndispatcher.Config
	IsRunning() bool
}

type WizardCapabilitiesResponse struct {
	Kind             string                          `json:"kind"`
	Scenarios        []string                        `json:"scenarios,omitempty"`
	Modes            []string                        `json:"modes,omitempty"`
	Profiles         []cdn.Profile                   `json:"profiles"`
	EgressOpts       []egress.EgressOption           `json:"egress_options"`
	KeenDNS          *serveringress.KeenDNSDiscovery `json:"keendns,omitempty"`
	Configured       bool                            `json:"configured"`
	Running          bool                            `json:"running"`
	RecoveryNeed     bool                            `json:"recovery_required"`
	StateFingerprint string                          `json:"state_fingerprint,omitempty"`
}

type WizardService struct {
	coord                    CoordinatorBridge
	xray                     XrayServerComponent
	tg                       TgWebProxyComponent
	disp                     DispatcherComponent
	preflight                *PreflightEngine
	fingerprint              *FingerprintEngine
	planStore                *PlanStore
	jobRunner                *JobRunner
	egress                   *egress.Adapter
	keendns                  serveringress.KeenDNSOperator
	dialTimeout              func(network, address string, timeout time.Duration) (net.Conn, error)
	httpDoer                 HTTPDoer
	lookupHost               func(ctx context.Context, host string) ([]string, error)
	tlsCertProbe             TLSCertProbeFunc
	directWANFirewallChecker func(port int) (tgwebproxy.DirectWANFirewallState, error)
}

// HTTPDoer abstracts HTTP requests for readiness probing and testing.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

func NewWizardService(
	coord CoordinatorBridge,
	xray XrayServerComponent,
	tg TgWebProxyComponent,
	disp DispatcherComponent,
	preflight *PreflightEngine,
	fingerprint *FingerprintEngine,
	planStore *PlanStore,
	jobRunner *JobRunner,
	egress *egress.Adapter,
) *WizardService {
	return &WizardService{
		coord:       coord,
		xray:        xray,
		tg:          tg,
		disp:        disp,
		preflight:   preflight,
		fingerprint: fingerprint,
		planStore:   planStore,
		jobRunner:   jobRunner,
		egress:      egress,
		dialTimeout: net.DialTimeout,
		lookupHost:  net.DefaultResolver.LookupHost,
	}
}

// SetDialTimeout configures a custom dialer function for testing readiness probes.
func (s *WizardService) SetDialTimeout(fn func(network, address string, timeout time.Duration) (net.Conn, error)) {
	s.dialTimeout = fn
}

// SetHTTPDoer configures a custom HTTP client for testing readiness probes.
func (s *WizardService) SetHTTPDoer(doer HTTPDoer) {
	s.httpDoer = doer
}

// SetLookupHost configures a custom DNS resolver function for testing verification probes.
func (s *WizardService) SetLookupHost(fn func(ctx context.Context, host string) ([]string, error)) {
	s.lookupHost = fn
}

// SetTLSCertProbe configures a dedicated TLS certificate verification probe for a specific FQDN.
func (s *WizardService) SetTLSCertProbe(fn TLSCertProbeFunc) {
	s.tlsCertProbe = fn
}

// SetDirectWANFirewallChecker configures a custom WAN firewall state probe for testing.
func (s *WizardService) SetDirectWANFirewallChecker(fn func(port int) (tgwebproxy.DirectWANFirewallState, error)) {
	s.directWANFirewallChecker = fn
}

func (s *WizardService) checkDirectWANFirewall(port int) (tgwebproxy.DirectWANFirewallState, error) {
	if s.directWANFirewallChecker != nil {
		return s.directWANFirewallChecker(port)
	}
	return tgwebproxy.CheckDirectWANFirewall(port)
}

func (s *WizardService) probeFQDNCertStatus(ctx context.Context, fqdn string) string {
	fqdn = strings.TrimSpace(fqdn)
	if fqdn == "" || s.tlsCertProbe == nil {
		return "not_checked"
	}
	status, err := s.tlsCertProbe(ctx, fqdn)
	switch status {
	case "ready":
		if err == nil {
			return "ready"
		}
		return "invalid"
	case "pending", "invalid", "not_checked":
		return status
	default:
		if err != nil {
			return "invalid"
		}
		return "not_checked"
	}
}

func (s *WizardService) buildKeenDNSPlanItem(ctx context.Context, label string, dispPort int) PlanItem {
	if s.keendns != nil {
		disc := s.DiscoverKeenDNS(ctx)
		for _, pub := range disc.Publications {
			if strings.EqualFold(pub.Label, label) && pub.CompatibleWithIngress {
				return PlanItem{
					Action:      "keep",
					Target:      "keendns_proxy",
					Description: fmt.Sprintf("Переиспользование существующего адреса KeenDNS %s -> 127.0.0.1:%d (без перезаписи настроек роутера и без вызова сохранения конфигурации)", label, dispPort),
					NewValue:    label,
				}
			}
		}
	}
	return PlanItem{
		Action:      "publish",
		Target:      "keendns_proxy",
		Description: fmt.Sprintf("Создание адреса KeenDNS %s -> 127.0.0.1:%d и сохранение конфигурации роутера (system.configuration.save — сохраняет все текущие несохранённые настройки роутера)", label, dispPort),
		NewValue:    label,
	}
}

// SetKeenDNS wires the KeenDNS operator into the wizard service and preflight engine.
func (s *WizardService) SetKeenDNS(k serveringress.KeenDNSOperator) {
	s.keendns = k
	if s.preflight != nil {
		s.preflight.SetKeenDNS(k)
	}
}

// DiscoverKeenDNS reads current KeenDNS status, publications, and local WAN address facts.
func (s *WizardService) DiscoverKeenDNS(ctx context.Context) serveringress.KeenDNSDiscovery {
	if s.keendns == nil {
		return serveringress.KeenDNSDiscovery{
			State:         "unsupported",
			StatusMessage: "Подсистема KeenDNS недоступна",
		}
	}
	dispPort := 9009
	if s.xray != nil && s.xray.GetConfig().DispatcherPort > 0 {
		dispPort = s.xray.GetConfig().DispatcherPort
	}
	disc, err := s.keendns.Discover(ctx, dispPort)
	if err != nil {
		return serveringress.KeenDNSDiscovery{
			State:         "read_error",
			StatusMessage: err.Error(),
		}
	}
	return disc
}

// GetCapabilities returns dynamic capabilities, profiles, and egress routes for the given wizard.
func (s *WizardService) GetCapabilities(ctx context.Context, kind string) WizardCapabilitiesResponse {
	var currentEgress []string
	if s.xray != nil && (kind == "xray" || kind == "both" || kind == "") {
		xc := s.xray.GetConfig()
		switch xc.OutboundMode {
		case "router":
			currentEgress = append(currentEgress, "firc:router")
		case "socks":
			currentEgress = append(currentEgress, "mihomo:1099")
		case "interface":
			if strings.TrimSpace(xc.OutboundInterface) != "" {
				currentEgress = append(currentEgress, strings.TrimSpace(xc.OutboundInterface))
			}
		default:
			if xc.Enabled || xc.PublicDomain != "" {
				currentEgress = append(currentEgress, "direct")
			}
		}
	}
	if s.tg != nil && (kind == "tgwebproxy" || kind == "both" || kind == "") {
		tc := s.tg.GetConfig()
		up := strings.TrimSpace(tc.UpstreamDevice)
		if up != "" {
			currentEgress = append(currentEgress, up)
		} else if tc.Enabled {
			currentEgress = append(currentEgress, "direct")
		}
	}

	var egressOpts []egress.EgressOption
	if s.egress != nil {
		egressOpts = s.egress.ListOptionsForServerWithCurrent(ctx, kind, currentEgress...)
	}

	profiles := cdn.ListProfilesForServer(kind)

	var configured, running, recReq bool
	if s.coord != nil {
		recReq, _ = s.coord.IsRecoveryRequired()
	}

	var scenarios []string
	var modes []string

	switch kind {
	case "tgwebproxy":
		scenarios = []string{"dual", "direct_fake_tls", "cdn_http"}
		if s.tg != nil {
			st := s.tg.GetStatus()
			configured = st.Installed
			running = st.Running
		}
	case "xray":
		modes = []string{"xhttp_get"}
		if s.xray != nil {
			st := s.xray.GetStatus()
			configured = st.Configured
			running = st.Running
		}
	case "both":
		scenarios = []string{"dual", "direct_fake_tls", "cdn_http"}
		modes = []string{"xhttp_get"}
		if s.xray != nil {
			st := s.xray.GetStatus()
			if st.Configured {
				configured = true
			}
			if st.Running {
				running = true
			}
		}
		if s.tg != nil {
			st := s.tg.GetStatus()
			if st.Installed {
				configured = true
			}
			if st.Running {
				running = true
			}
		}
	}

	fp := ""
	if s.fingerprint != nil {
		fp = s.fingerprint.Compute(ctx)
	}

	var kdDisc *serveringress.KeenDNSDiscovery
	if s.keendns != nil {
		d := s.DiscoverKeenDNS(ctx)
		kdDisc = &d
	}

	return WizardCapabilitiesResponse{
		Kind:             kind,
		Scenarios:        scenarios,
		Modes:            modes,
		Profiles:         profiles,
		EgressOpts:       egressOpts,
		KeenDNS:          kdDisc,
		Configured:       configured,
		Running:          running,
		RecoveryNeed:     recReq,
		StateFingerprint: fp,
	}
}

// Preflight runs readiness checks and captures current state fingerprint.
func (s *WizardService) Preflight(ctx context.Context, req WizardPlanRequest) PreflightResponse {
	resp := s.preflight.Run(ctx, req)
	resp.Fingerprint = s.fingerprint.Compute(ctx)
	return resp
}

// Plan builds a dry-run change plan and saves it server-side with fingerprint and TTL.
func (s *WizardService) Plan(ctx context.Context, sessionID string, req WizardPlanRequest) (*ServerPlanRecord, error) {
	// 1. Build desired configuration first (normalizes, validates strict matrix, resolves egress)
	desired, err := BuildDesiredConfig(ctx, s.egress, req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}

	// 2. Run preflight checks
	preflightResp := s.preflight.Run(ctx, req)
	if !preflightResp.CanProceed {
		return nil, fmt.Errorf("%w: preflight checks blocked progress", ErrInvalidRequest)
	}

	// 3. Compute current fingerprint with ComputeStrict
	fp, err := s.fingerprint.ComputeStrict(ctx)
	if err != nil {
		return nil, fmt.Errorf("state probe failed: %w", err)
	}

	// 4. Generate plan
	var plan ChangePlan
	switch req.Kind {
	case "tgwebproxy":
		plan = s.buildTelegramPlan(desired, fp)
	case "xray":
		plan = s.buildXrayPlan(desired, fp)
	case "both":
		plan = s.buildCombinedPlan(desired, fp)
	default:
		return nil, fmt.Errorf("%w: unsupported wizard kind: %s", ErrInvalidRequest, req.Kind)
	}

	// 5. Save server-side (stores Desired with zero secrets!)
	rec := s.planStore.SavePlan(req, desired, sessionID, plan, fp, DefaultPlanTTL)
	return rec, nil
}

func (s *WizardService) buildTelegramPlan(desired DesiredWizardConfig, fp string) ChangePlan {
	directPort := desired.DirectPort
	if directPort <= 0 {
		directPort = 8443
	}
	listenPort := desired.ListenPort
	if listenPort <= 0 {
		listenPort = 8085
	}
	dispPort := desired.DispatcherPort
	if dispPort <= 0 {
		dispPort = 9009
	}
	upstream := desired.UpstreamDevice
	if upstream == "" {
		upstream = "direct"
	}

	var items []PlanItem

	if desired.Scenario == "direct_fake_tls" || desired.Scenario == "dual" {
		items = append(items,
			PlanItem{
				Action:      "enable",
				Target:      "telemt",
				Description: fmt.Sprintf("MTProto Fake-TLS direct прокси на порту %d", directPort),
				NewValue:    fmt.Sprintf(":%d", directPort),
			},
		)
		if desired.DirectHost != "" {
			items = append(items, PlanItem{
				Action:      "enable",
				Target:      "wan_firewall",
				Description: fmt.Sprintf("Автоматическое открытие TCP-порта %d в WAN-фаерволе Keenetic для MTProto", directPort),
				NewValue:    fmt.Sprintf("tcp/%d", directPort),
			})
			items = append(items, PlanItem{
				Action:      "modify",
				Target:      "direct_host",
				Description: fmt.Sprintf("Прямой хост подключения: %s", desired.DirectHost),
				NewValue:    desired.DirectHost,
			})
		}
	}

	if desired.Scenario == "cdn_http" && s.tg != nil {
		if cur := s.tg.GetConfig(); cur.OpensDirectWAN() {
			items = append(items, PlanItem{
				Action:      "disable",
				Target:      "wan_firewall",
				Description: fmt.Sprintf("Закрытие TCP-порта %d MTProto в WAN-фаерволе Keenetic", cur.DirectPort),
				OldValue:    fmt.Sprintf("tcp/%d", cur.DirectPort),
			})
		}
	}

	if desired.Scenario == "cdn_http" || desired.Scenario == "dual" {
		items = append(items,
			PlanItem{
				Action:      "enable",
				Target:      "tproxy-server",
				Description: fmt.Sprintf("Web Proxy демон на локальном порту %d для CDN туннеля", listenPort),
				NewValue:    fmt.Sprintf("127.0.0.1:%d", listenPort),
			},
			PlanItem{
				Action:      "modify",
				Target:      "cdn-dispatcher",
				Description: "CDN Диспетчер: проксирование пути /?bridge= на порт Web Proxy",
				NewValue:    fmt.Sprintf("0.0.0.0:%d -> http://127.0.0.1:%d", dispPort, listenPort),
			},
		)
		if desired.PublicHostname != "" {
			items = append(items, PlanItem{
				Action:      "modify",
				Target:      "public_domain",
				Description: fmt.Sprintf("CDN Ingress домен: %s", desired.PublicHostname),
				NewValue:    desired.PublicHostname,
			})
		}
	}

	items = append(items, PlanItem{
		Action:      "modify",
		Target:      "upstream_egress",
		Description: fmt.Sprintf("Маршрутизация исходящего трафика Telegram через %s", upstream),
		NewValue:    upstream,
	})

	if desired.KeenDNSLabel != "" && (desired.Scenario == "cdn_http" || desired.Scenario == "dual") {
		items = append(items, s.buildKeenDNSPlanItem(context.Background(), desired.KeenDNSLabel, dispPort))
	}

	if desired.DisableOtherService && desired.ServerKind == "tgwebproxy" {
		items = append(items, PlanItem{
			Action:      "disable",
			Target:      "awgm-xhttpd",
			Description: "Отключение сервера VLESS по выбору в мастере",
			NewValue:    "disabled",
		})
	}

	summary := "Настройка сервера Telegram (MTProto Fake-TLS + Web CDN)"
	switch desired.Scenario {
	case "direct_fake_tls":
		summary = "Настройка прокси Telegram (MTProto Fake-TLS)"
	case "cdn_http":
		summary = "Настройка сервера Telegram Web Proxy (Web CDN)"
	}

	return ChangePlan{
		Summary:          summary,
		Items:            items,
		RestartRequired:  true,
		StateFingerprint: fp,
	}
}

func (s *WizardService) buildXrayPlan(desired DesiredWizardConfig, fp string) ChangePlan {
	listenPort := desired.ListenPort
	if listenPort <= 0 {
		listenPort = 9008
	}
	dispPort := desired.DispatcherPort
	if dispPort <= 0 {
		dispPort = 9009
	}
	path := desired.Path
	if path == "" {
		path = "/cdn-bridge/"
	}
	transportDesc := "xhttp/packet-up"
	if desired.Transport == "xhttp" && desired.Mode != "" {
		transportDesc = "xhttp/" + desired.Mode
	} else if desired.Transport != "" && desired.Transport != "xhttp" {
		transportDesc = desired.Transport
	}
	remark := desired.ClientRemark
	if remark == "" {
		remark = "Основное устройство"
	}

	clientItem := PlanItem{
		Action:      "create",
		Target:      "client",
		Description: fmt.Sprintf("Создание клиента подключения '%s' с криптографическим UUID", remark),
		NewValue:    remark,
	}
	if s.xray != nil {
		existingClients := s.xray.GetConfig().Clients
		if len(existingClients) > 0 {
			clientItem = PlanItem{
				Action:      "keep",
				Target:      "client",
				Description: fmt.Sprintf("Сохранение существующих профилей устройств (%d шт.)", len(existingClients)),
				NewValue:    existingClients[0].Remark,
			}
		}
	}

	items := []PlanItem{
		{
			Action:      "enable",
			Target:      "awgm-xhttpd",
			Description: fmt.Sprintf("Запуск C-сервера VLESS (awgm-xhttpd) (локальный порт %d, транспорт %s)", listenPort, transportDesc),
			NewValue:    fmt.Sprintf("127.0.0.1:%d", listenPort),
		},
		{
			Action:      "enable",
			Target:      "cdn-dispatcher",
			Description: fmt.Sprintf("Домены внешнего доступа (:9009): маршрутизация пути %s на VLESS (XHTTP)", path),
			NewValue:    fmt.Sprintf("0.0.0.0:%d -> 127.0.0.1:%d", dispPort, listenPort),
		},
		clientItem,
	}

	outboundDesc := "Прямой выход в интернет (WAN)"
	outboundVal := "direct"
	if desired.OutboundMode == "router" {
		outboundDesc = "Маршрутизация исходящего трафика VLESS через правила маршрутизации (AWGM Route)"
		outboundVal = "firc:router"
	} else if desired.OutboundMode == "socks" {
		outboundDesc = egress.LegacyEgressLabel()
		outboundVal = "127.0.0.1:1099"
	} else if desired.OutboundMode == "interface" && desired.OutboundInterface != "" {
		outboundDesc = fmt.Sprintf("Маршрутизация исходящего трафика VLESS через туннель %s", desired.OutboundInterface)
		outboundVal = desired.OutboundInterface
	}

	items = append(items, PlanItem{
		Action:      "modify",
		Target:      "outbound_routing",
		Description: outboundDesc,
		NewValue:    outboundVal,
	})

	if desired.PublicHostname != "" {
		items = append(items, PlanItem{
			Action:      "modify",
			Target:      "public_domain",
			Description: fmt.Sprintf("Домен подключения (для клиентов): %s", desired.PublicHostname),
			NewValue:    desired.PublicHostname,
		})
	}

	if desired.OriginHost != "" {
		originScheme := desired.OriginScheme
		if originScheme == "" {
			originScheme = "http"
		}
		originPort := desired.OriginPort
		if originPort <= 0 {
			originPort = dispPort
		}
		originVal := fmt.Sprintf("%s://%s:%d", originScheme, desired.OriginHost, originPort)
		items = append(items, PlanItem{
			Action:      "modify",
			Target:      "origin_source",
			Description: fmt.Sprintf("Сервер-источник (адрес роутера): %s", originVal),
			NewValue:    originVal,
		})
	}

	if desired.KeenDNSLabel != "" {
		items = append(items, s.buildKeenDNSPlanItem(context.Background(), desired.KeenDNSLabel, dispPort))
	}

	if desired.DisableOtherService && desired.ServerKind == "xray" {
		items = append(items, PlanItem{
			Action:      "disable",
			Target:      "tgwebproxy",
			Description: "Отключение сервиса Telegram по выбору в мастере",
			NewValue:    "disabled",
		})
	}

	return ChangePlan{
		Summary:          fmt.Sprintf("Настройка сервера VLESS (XHTTP) для клиента '%s'", remark),
		Items:            items,
		RestartRequired:  true,
		StateFingerprint: fp,
	}
}

func (s *WizardService) buildCombinedPlan(desired DesiredWizardConfig, fp string) ChangePlan {
	xrayPlan := s.buildXrayPlan(desired, fp)
	tgDesired := desired
	tgDesired.KeenDNSLabel = "" // avoid duplicate KeenDNS item in combined plan
	tgDesired.ListenPort = desired.TgListenPort
	if tgDesired.ListenPort <= 0 {
		tgDesired.ListenPort = 8085
	}
	if desired.TgPublicHostname != "" {
		tgDesired.PublicHostname = desired.TgPublicHostname
	}
	tgDesired.UpstreamDevice = desired.TgUpstreamDevice
	tgPlan := s.buildTelegramPlan(tgDesired, fp)

	items := make([]PlanItem, 0, len(xrayPlan.Items)+len(tgPlan.Items))
	items = append(items, xrayPlan.Items...)
	items = append(items, tgPlan.Items...)

	return ChangePlan{
		Summary:          "Единая настройка внешнего доступа: VLESS (XHTTP) и Telegram",
		Items:            items,
		RestartRequired:  true,
		StateFingerprint: fp,
	}
}

// Apply atomically reserves plan and starts async execution.
func (s *WizardService) Apply(ctx context.Context, sessionID, planID string) (string, error) {
	jobID := s.jobRunner.GenerateJobID()

	// 1. Reserve plan atomically
	rec, err := s.planStore.Reserve(planID, sessionID, jobID)
	if err != nil {
		return "", err
	}

	// 2. Create async job with reserved jobID
	jobCtx, cancelFn := context.WithCancel(context.Background())
	job := s.jobRunner.CreateJobWithID(jobID, rec.Kind, sessionID, cancelFn)

	// 3. Start execution goroutine
	go s.executeApplyJob(jobCtx, job.ID, rec)

	return job.ID, nil
}

func (s *WizardService) executeApplyJob(ctx context.Context, jobID string, rec *ServerPlanRecord) {
	txID := fmt.Sprintf("wiz-apply-%d-%d", time.Now().UnixNano(), os.Getpid())

	// Phase 1: Preparing
	s.jobRunner.UpdateStep(jobID, JobPhasePreparing, 20, "Проверка состояния и подготовка транзакции...")
	if s.jobRunner.IsCancelRequested(jobID) {
		_ = s.planStore.Release(rec.PlanID, jobID)
		s.jobRunner.MarkCancelled(jobID)
		return
	}

	// Phase 2: Applying
	s.jobRunner.UpdateStep(jobID, JobPhaseApplying, 45, "Генерация секретов и запись конфигурационных файлов...")
	if s.jobRunner.IsCancelRequested(jobID) {
		_ = s.planStore.Release(rec.PlanID, jobID)
		s.jobRunner.MarkCancelled(jobID)
		return
	}

	txParams := serveringress.IngressTransactionParams{
		TxID:                txID,
		ExpectedFingerprint: rec.StateFingerprint,
		FingerprintFunc: func(checkCtx context.Context) (string, error) {
			return s.fingerprint.ComputeStrict(checkCtx)
		},
		ServerKind: rec.Kind,
		ReadinessProbe: func(probeCtx context.Context, topo serveringress.IngressTopology) error {
			s.jobRunner.UpdateStep(jobID, JobPhaseVerifying, 75, "Проверка готовности сетевых сокетов...")
			return s.probeTopologyReadiness(probeCtx, topo)
		},
		OnPointOfNoReturn: func(ptxID string) error {
			s.jobRunner.SetPhaseNonCancellable(jobID, JobPhaseCommitting, 80, "Фиксация конфигурации (точка невозврата)...")
			if _, err := s.planStore.MarkConsumed(rec.PlanID, jobID); err != nil {
				return fmt.Errorf("plan_consume_failed_after_commit_boundary: %w", err)
			}
			return nil
		},
	}

	var creds *RevealCredentials
	var tgSecret string
	var clientUUID string

	switch rec.Kind {
	case "tgwebproxy":
		var err error
		tgSecret, err = s.resolveOrGenerateTgSecret(rec.Request.PreserveExistingCredentials)
		if err != nil {
			_ = s.planStore.Release(rec.PlanID, jobID)
			s.jobRunner.FailJob(jobID, ErrCodeInvalidRequest, "генерация криптографического ключа не удалась")
			return
		}

		// Pre-commit link generation via pure function BuildTgLinks
		tgCfg := tgwebproxy.Config{
			Enabled:        true,
			Scenario:       rec.Desired.Scenario,
			DirectHost:     rec.Desired.DirectHost,
			DirectPort:     rec.Desired.DirectPort,
			TlsDomain:      rec.Desired.TlsDomain,
			ListenPort:     rec.Desired.ListenPort,
			PublicHostname: rec.Desired.PublicHostname,
			CarrierMode:    rec.Desired.CarrierMode,
			UpstreamDevice: rec.Desired.UpstreamDevice,
			Secret:         tgSecret,
		}
		reveal, err := tgwebproxy.BuildTgLinks(tgCfg, tgSecret)
		if err != nil {
			_ = s.planStore.Release(rec.PlanID, jobID)
			s.jobRunner.FailJob(jobID, ErrCodeInvalidRequest, fmt.Sprintf("построение Telegram ссылок не удалось: %v", err))
			return
		}
		creds = &RevealCredentials{
			Kind:         "tgwebproxy",
			DirectLink:   reveal.MtproxyURL,
			WebLink:      reveal.TgURL,
			TgSecret:     tgSecret,
			DirectHost:   rec.Desired.DirectHost,
			DirectPort:   rec.Desired.DirectPort,
			PublicHost:   rec.Desired.PublicHostname,
			TgEgress:     describeTgEgress(rec.Desired.UpstreamDevice),
			OriginHost:   rec.Desired.OriginHost,
			OriginScheme: rec.Desired.OriginScheme,
			OriginPort:   rec.Desired.OriginPort,
		}

		tgPreset, tgIsCDN := telegramIngressPlacement(rec.Desired, tgCfg)
		txParams.Telegram = &serveringress.IngressTelegramCandidate{
			Enabled:        true,
			Scenario:       rec.Desired.Scenario,
			NetworkPreset:  tgPreset,
			IsCDN:          tgIsCDN,
			DirectHost:     rec.Desired.DirectHost,
			DirectPort:     rec.Desired.DirectPort,
			TlsDomain:      rec.Desired.TlsDomain,
			ListenPort:     rec.Desired.ListenPort,
			BackendPort:    2398,
			AdminPort:      8086,
			PublicHostname: rec.Desired.PublicHostname,
			CarrierMode:    rec.Desired.CarrierMode,
			UpstreamDevice: rec.Desired.UpstreamDevice,
			Secret:         tgSecret,
		}
		if rec.Desired.DisableOtherService && s.xray != nil {
			txParams.Xray = &serveringress.IngressXrayCandidate{
				Enabled:           false,
				PreserveTransport: true,
			}
		}

	case "xray":
		var err error
		var clientRemark string
		clientUUID, clientRemark, err = s.resolveOrGenerateClient(
			rec.Request.PreserveExistingCredentials,
			rec.Request.ExistingClientUUID,
			rec.Desired.ClientRemark,
		)
		if err != nil {
			_ = s.planStore.Release(rec.PlanID, jobID)
			s.jobRunner.FailJob(jobID, ErrCodeInvalidRequest, "генерация UUID клиента не удалась")
			return
		}

		// Pre-commit link generation via pure function BuildShareLinks on the exact effective candidate
		xrayPublicPort := rec.Desired.PublicPort
		if xrayPublicPort <= 0 {
			xrayPublicPort = 443
		}
		xrayPath := rec.Desired.Path
		if xrayPath == "" {
			xrayPath = "/cdn-bridge/"
		}

		xrayCand := serveringress.IngressXrayCandidate{
			Enabled:           true,
			ListenAddress:     "127.0.0.1",
			ListenPort:        rec.Desired.ListenPort,
			PublicDomain:      rec.Desired.PublicHostname,
			PublicPort:        xrayPublicPort,
			ConnectionWay:     rec.Request.ConnectionWay,
			OriginType:        rec.Request.OriginType,
			OriginHost:        rec.Desired.OriginHost,
			OriginScheme:      rec.Desired.OriginScheme,
			OriginPort:        rec.Desired.OriginPort,
			Path:              xrayPath,
			Transport:         rec.Desired.Transport,
			Mode:              rec.Desired.Mode,
			UplinkMethod:      rec.Desired.UplinkMethod,
			PreserveTransport: rec.Request.PreserveTransport,
			OutboundMode:      rec.Desired.OutboundMode,
			OutboundInterface: rec.Desired.OutboundInterface,
			OutboundSocksPort: rec.Desired.OutboundSocksPort,
			ClientRemark:      clientRemark,
			ClientUUID:        clientUUID,
		}
		var baseXrayCfg vlessserver.Config
		if s.xray != nil {
			baseXrayCfg = s.xray.GetConfig()
		}
		xrayCfg := serveringress.BuildEffectiveXrayCandidate(baseXrayCfg, xrayCand)
		client := vlessserver.Client{
			ID:      clientUUID,
			Remark:  clientRemark,
			Enabled: true,
		}
		shareLinks, err := vlessserver.BuildShareLinks(xrayCfg, client)
		if err != nil {
			_ = s.planStore.Release(rec.PlanID, jobID)
			s.jobRunner.FailJob(jobID, ErrCodeInvalidRequest, fmt.Sprintf("построение Xray ссылок не удалось: %v", err))
			return
		}
		creds = &RevealCredentials{
			Kind:         "xray",
			VlessURL:     shareLinks.VlessURL,
			HappJSON:     shareLinks.HappJSON,
			SingboxJSON:  shareLinks.SingboxJSON,
			MihomoYAML:   shareLinks.MihomoYAML,
			UUID:         clientUUID,
			Remark:       clientRemark,
			PublicHost:   rec.Desired.PublicHostname,
			XrayEgress:   describeXrayEgress(rec.Desired.OutboundMode, rec.Desired.OutboundInterface),
			OriginHost:   rec.Desired.OriginHost,
			OriginScheme: rec.Desired.OriginScheme,
			OriginPort:   rec.Desired.OriginPort,
		}

		txParams.Xray = &xrayCand
		if rec.Desired.DisableOtherService && s.tg != nil {
			txParams.Telegram = &serveringress.IngressTelegramCandidate{
				Enabled: false,
			}
		}

	case "both":
		var err error
		tgSecret, err = s.resolveOrGenerateTgSecret(rec.Request.PreserveExistingCredentials)
		if err != nil {
			_ = s.planStore.Release(rec.PlanID, jobID)
			s.jobRunner.FailJob(jobID, ErrCodeInvalidRequest, "генерация криптографического ключа не удалась")
			return
		}

		var clientRemark string
		clientUUID, clientRemark, err = s.resolveOrGenerateClient(
			rec.Request.PreserveExistingCredentials,
			rec.Request.ExistingClientUUID,
			rec.Desired.ClientRemark,
		)
		if err != nil {
			_ = s.planStore.Release(rec.PlanID, jobID)
			s.jobRunner.FailJob(jobID, ErrCodeInvalidRequest, "генерация UUID клиента не удалась")
			return
		}

		xrayPublicPort := rec.Desired.PublicPort
		if xrayPublicPort <= 0 {
			xrayPublicPort = 443
		}
		xrayPath := rec.Desired.Path
		if xrayPath == "" {
			xrayPath = "/cdn-bridge/"
		}
		xrayCand := serveringress.IngressXrayCandidate{
			Enabled:           true,
			ListenAddress:     "127.0.0.1",
			ListenPort:        rec.Desired.ListenPort,
			PublicDomain:      rec.Desired.PublicHostname,
			PublicPort:        xrayPublicPort,
			ConnectionWay:     rec.Request.ConnectionWay,
			OriginType:        rec.Request.OriginType,
			OriginHost:        rec.Desired.OriginHost,
			OriginScheme:      rec.Desired.OriginScheme,
			OriginPort:        rec.Desired.OriginPort,
			Path:              xrayPath,
			Transport:         rec.Desired.Transport,
			Mode:              rec.Desired.Mode,
			UplinkMethod:      rec.Desired.UplinkMethod,
			PreserveTransport: rec.Request.PreserveTransport,
			OutboundMode:      rec.Desired.OutboundMode,
			OutboundInterface: rec.Desired.OutboundInterface,
			OutboundSocksPort: rec.Desired.OutboundSocksPort,
			ClientRemark:      clientRemark,
			ClientUUID:        clientUUID,
		}
		var baseXrayCfg vlessserver.Config
		if s.xray != nil {
			baseXrayCfg = s.xray.GetConfig()
		}
		xrayCfg := serveringress.BuildEffectiveXrayCandidate(baseXrayCfg, xrayCand)
		client := vlessserver.Client{
			ID:      clientUUID,
			Remark:  clientRemark,
			Enabled: true,
		}
		shareLinks, err := vlessserver.BuildShareLinks(xrayCfg, client)
		if err != nil {
			_ = s.planStore.Release(rec.PlanID, jobID)
			s.jobRunner.FailJob(jobID, ErrCodeInvalidRequest, fmt.Sprintf("построение VLESS ссылок не удалось: %v", err))
			return
		}

		tgListenPort := rec.Desired.TgListenPort
		if tgListenPort <= 0 {
			tgListenPort = 8085
		}
		tgPublicHost := ""
		if rec.Desired.Scenario != "direct_fake_tls" {
			tgPublicHost = rec.Desired.TgPublicHostname
			if tgPublicHost == "" {
				tgPublicHost = rec.Desired.PublicHostname
			}
		}
		tgCfg := tgwebproxy.Config{
			Enabled:        true,
			Scenario:       rec.Desired.Scenario,
			DirectHost:     rec.Desired.DirectHost,
			DirectPort:     rec.Desired.DirectPort,
			TlsDomain:      rec.Desired.TlsDomain,
			ListenPort:     tgListenPort,
			PublicHostname: tgPublicHost,
			CarrierMode:    rec.Desired.CarrierMode,
			UpstreamDevice: rec.Desired.TgUpstreamDevice,
			Secret:         tgSecret,
		}
		reveal, err := tgwebproxy.BuildTgLinks(tgCfg, tgSecret)
		if err != nil {
			_ = s.planStore.Release(rec.PlanID, jobID)
			s.jobRunner.FailJob(jobID, ErrCodeInvalidRequest, fmt.Sprintf("построение Telegram ссылок не удалось: %v", err))
			return
		}

		creds = &RevealCredentials{
			Kind:         "both",
			VlessURL:     shareLinks.VlessURL,
			HappJSON:     shareLinks.HappJSON,
			SingboxJSON:  shareLinks.SingboxJSON,
			MihomoYAML:   shareLinks.MihomoYAML,
			UUID:         clientUUID,
			Remark:       clientRemark,
			DirectLink:   reveal.MtproxyURL,
			WebLink:      reveal.TgURL,
			TgSecret:     tgSecret,
			DirectHost:   rec.Desired.DirectHost,
			DirectPort:   rec.Desired.DirectPort,
			PublicHost:   rec.Desired.PublicHostname,
			TgPublicHost: tgPublicHost,
			XrayEgress:   describeXrayEgress(rec.Desired.OutboundMode, rec.Desired.OutboundInterface),
			TgEgress:     describeTgEgress(rec.Desired.TgUpstreamDevice),
			OriginHost:   rec.Desired.OriginHost,
			OriginScheme: rec.Desired.OriginScheme,
			OriginPort:   rec.Desired.OriginPort,
		}

		tgPreset, tgIsCDN := telegramIngressPlacement(rec.Desired, tgCfg)
		txParams.Xray = &xrayCand
		txParams.Telegram = &serveringress.IngressTelegramCandidate{
			Enabled:        true,
			Scenario:       rec.Desired.Scenario,
			NetworkPreset:  tgPreset,
			IsCDN:          tgIsCDN,
			DirectHost:     rec.Desired.DirectHost,
			DirectPort:     rec.Desired.DirectPort,
			TlsDomain:      rec.Desired.TlsDomain,
			ListenPort:     tgListenPort,
			BackendPort:    2398,
			AdminPort:      8086,
			PublicHostname: tgPublicHost,
			CarrierMode:    rec.Desired.CarrierMode,
			UpstreamDevice: rec.Desired.TgUpstreamDevice,
			Secret:         tgSecret,
		}

	default:
		_ = s.planStore.Release(rec.PlanID, jobID)
		s.jobRunner.FailJob(jobID, ErrCodeInvalidRequest, fmt.Sprintf("неизвестный тип сервера: %s", rec.Kind))
		return
	}

	if rec.Desired.KeenDNSLabel != "" {
		dispPort := rec.Desired.DispatcherPort
		if dispPort <= 0 {
			dispPort = 9009
		}
		var services []string
		switch rec.Kind {
		case "xray":
			services = []string{"xray"}
		case "tgwebproxy":
			if rec.Desired.Scenario != "direct_fake_tls" {
				services = []string{"tgwebproxy"}
			}
		case "both":
			if rec.Desired.Scenario != "direct_fake_tls" {
				services = []string{"xray", "tgwebproxy"}
			} else {
				services = []string{"xray"}
			}
		}
		if len(services) > 0 {
			txParams.KeenDNS = &serveringress.KeenDNSPublicationCandidate{
				Label:        rec.Desired.KeenDNSLabel,
				UpstreamHost: "127.0.0.1",
				UpstreamPort: dispPort,
				Services:     services,
			}
		}
	}

	// Execute transaction through coordinator
	err := s.coord.ExecuteIngressTransaction(ctx, txParams)
	if err != nil {
		if errors.Is(err, serveringress.ErrRecoveryRequired) {
			diagMsg := fmt.Sprintf("Координатор требует восстановления: %v", err)
			if strings.Contains(err.Error(), "plan_consume_failed_after_commit_boundary") {
				diagMsg = "Координатор требует восстановления: plan_consume_failed_after_commit_boundary"
			}
			s.jobRunner.MarkRecoveryRequired(jobID, diagMsg)
			return
		}
		if s.jobRunner.IsCancelRequested(jobID) {
			_ = s.planStore.Release(rec.PlanID, jobID)
			s.jobRunner.MarkCancelled(jobID)
			return
		}
		if errors.Is(err, serveringress.ErrPlanStale) {
			_ = s.planStore.Release(rec.PlanID, jobID)
			s.jobRunner.FailJob(jobID, ErrCodePlanStale, "План устарел: системное состояние изменилось во время подготовки")
			return
		}
		_ = s.planStore.Release(rec.PlanID, jobID)
		s.jobRunner.FailJob(jobID, ErrCodeInvalidRequest, err.Error())
		return
	}

	if rec.Desired.KeenDNSLabel != "" && creds != nil && s.keendns != nil {
		disc := s.DiscoverKeenDNS(ctx)
		if disc.BaseFQDN != "" {
			creds.KeenDNSFQDN = rec.Desired.KeenDNSLabel + "." + disc.BaseFQDN
		}
		creds.KeenDNSCertStatus = s.probeFQDNCertStatus(ctx, creds.KeenDNSFQDN)
	}

	// Phase 4: Succeeded (plan was already marked consumed in OnPointOfNoReturn)
	s.jobRunner.UpdateStep(jobID, JobPhaseCommitting, 90, "Фиксация изменений конфигурации...")
	s.jobRunner.SucceedJob(jobID, creds)
}

func (s *WizardService) probeTopologyReadiness(ctx context.Context, topo serveringress.IngressTopology) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	dialFn := s.dialTimeout
	if dialFn == nil {
		dialFn = net.DialTimeout
	}

	deadline := time.Now().Add(25 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	probeInterval := 50 * time.Millisecond

	// 1. Probe Xray listener if configured and enabled
	if topo.XrayEnabled && topo.XrayPort > 0 {
		target := fmt.Sprintf("127.0.0.1:%d", topo.XrayPort)
		ready := false
		var lastErr error
		for time.Now().Before(deadline) {
			if ctx.Err() != nil {
				break
			}
			conn, err := dialFn("tcp", target, 200*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				ready = true
				break
			}
			lastErr = err
			time.Sleep(probeInterval)
		}
		if !ready {
			return fmt.Errorf("xray listener %s not ready: %w", target, lastErr)
		}
	}

	tgNeedsDirect := topo.TgEnabled && (topo.TgScenario == "direct_fake_tls" || topo.TgScenario == "dual")
	tgNeedsWeb := topo.TgEnabled && (topo.TgScenario == "cdn_http" || topo.TgScenario == "dual")

	// 2. Probe Telegram direct listener if configured
	if tgNeedsDirect {
		if topo.TgDirectPort > 0 {
			target := fmt.Sprintf("127.0.0.1:%d", topo.TgDirectPort)
			ready := false
			var lastErr error
			for time.Now().Before(deadline) {
				conn, err := dialFn("tcp", target, 200*time.Millisecond)
				if err == nil {
					_ = conn.Close()
					ready = true
					break
				}
				lastErr = err
				time.Sleep(probeInterval)
			}
			if !ready {
				return fmt.Errorf("telemt direct listener %s not ready: %w", target, lastErr)
			}
		}
	} else if topo.TgDirectPort > 0 {
		if s.dialTimeout == nil {
			target := fmt.Sprintf("127.0.0.1:%d", topo.TgDirectPort)
			if conn, err := net.DialTimeout("tcp", target, 200*time.Millisecond); err == nil {
				_ = conn.Close()
				return fmt.Errorf("unexpected listener on telemt direct port %s when direct MTProto is disabled", target)
			}
		}
	}

	// 3. Probe Telegram web listener if configured
	if tgNeedsWeb {
		if topo.TgWebPort > 0 {
			target := fmt.Sprintf("127.0.0.1:%d", topo.TgWebPort)
			ready := false
			var lastErr error
			for time.Now().Before(deadline) {
				conn, err := dialFn("tcp", target, 200*time.Millisecond)
				if err == nil {
					_ = conn.Close()
					ready = true
					break
				}
				lastErr = err
				time.Sleep(probeInterval)
			}
			if !ready {
				return fmt.Errorf("tproxy web listener %s not ready: %w", target, lastErr)
			}
		}
	} else if topo.TgWebPort > 0 {
		if s.dialTimeout == nil {
			target := fmt.Sprintf("127.0.0.1:%d", topo.TgWebPort)
			if conn, err := net.DialTimeout("tcp", target, 200*time.Millisecond); err == nil {
				_ = conn.Close()
				return fmt.Errorf("unexpected listener on web proxy port %s when Telegram Web Proxy is disabled", target)
			}
		}
	}

	// 4. Probe CDN Dispatcher if configured
	dispPort := topo.DispatcherPort
	if dispPort <= 0 {
		dispPort = 9009
	}
	checkXray := topo.XrayEnabled && (topo.XrayPublicHostname != "" || topo.PublicHostname != "")
	checkTg := tgNeedsWeb && (topo.TgPublicHostname != "" || topo.PublicHostname != "")
	if checkXray || checkTg {
		target := fmt.Sprintf("127.0.0.1:%d", dispPort)
		ready := false
		var lastErr error
		for time.Now().Before(deadline) {
			conn, err := dialFn("tcp", target, 200*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				ready = true
				break
			}
			lastErr = err
			time.Sleep(probeInterval)
		}
		if !ready {
			return fmt.Errorf("cdn dispatcher %s not ready: %w", target, lastErr)
		}

		// Strict HTTP route identity check
		if checkXray || checkTg {
			xrayHost := topo.XrayPublicHostname
			if xrayHost == "" {
				xrayHost = topo.PublicHostname
			}
			xrayPath := cdndispatcher.NormalizePathPrefix(topo.XrayPathPrefix)

			tgHost := topo.TgPublicHostname
			if tgHost == "" {
				tgHost = topo.PublicHostname
			}

			client := s.httpDoer
			if client == nil {
				client = &http.Client{
					Transport: &http.Transport{
						DisableKeepAlives: true,
						DialContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
							if dialFn != nil {
								return dialFn(network, addr, 200*time.Millisecond)
							}
							var d net.Dialer
							d.Timeout = 200 * time.Millisecond
							return d.DialContext(dialCtx, network, addr)
						},
					},
					Timeout: 500 * time.Millisecond,
				}
			}

			httpReady := false
			var lastHttpErr error
			for time.Now().Before(deadline) {
				if ctx.Err() != nil {
					break
				}

				attemptOk := true

				if checkXray {
					for _, h := range SplitPublicHostnames(xrayHost) {
						xrayUrl := fmt.Sprintf("http://127.0.0.1:%d%s", dispPort, xrayPath)
						hReq, err := http.NewRequestWithContext(ctx, "GET", xrayUrl, nil)
						if err != nil {
							return fmt.Errorf("create xray probe request: %w", err)
						}
						hReq.Host = h
						hResp, err := client.Do(hReq)
						if err != nil {
							lastHttpErr = fmt.Errorf("http get xray probe (%s): %w", xrayUrl, err)
							attemptOk = false
							break
						}
						routeHeader := hResp.Header.Get("X-CDN-Route")
						_ = hResp.Body.Close()
						if routeHeader != "xray" {
							lastHttpErr = fmt.Errorf("unexpected or missing X-CDN-Route header for Xray: got %q, want %q", routeHeader, "xray")
							attemptOk = false
							break
						}
					}
				}

				if attemptOk && checkTg {
					for _, h := range SplitPublicHostnames(tgHost) {
						tgUrl := fmt.Sprintf("http://127.0.0.1:%d/", dispPort)
						hReq, err := http.NewRequestWithContext(ctx, "GET", tgUrl, nil)
						if err != nil {
							return fmt.Errorf("create tg probe request: %w", err)
						}
						hReq.Host = h
						hResp, err := client.Do(hReq)
						if err != nil {
							lastHttpErr = fmt.Errorf("http get tg probe (%s): %w", tgUrl, err)
							attemptOk = false
							break
						}
						routeHeader := hResp.Header.Get("X-CDN-Route")
						_ = hResp.Body.Close()
						if routeHeader != "tgwebproxy" {
							lastHttpErr = fmt.Errorf("unexpected or missing X-CDN-Route header for Telegram: got %q, want %q", routeHeader, "tgwebproxy")
							attemptOk = false
							break
						}
					}
				}

				if attemptOk {
					httpReady = true
					break
				}

				time.Sleep(probeInterval)
			}

			if !httpReady {
				if lastHttpErr != nil {
					return fmt.Errorf("cdn dispatcher route identity probe failed: %w", lastHttpErr)
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("cdn dispatcher route identity probe failed: timeout waiting for ready route")
			}
		}
	}

	return nil
}

func (s *WizardService) GetJob(ctx context.Context, jobID, sessionID string) (JobStatusResponse, error) {
	return s.jobRunner.GetJob(jobID, sessionID)
}

// CancelJob attempts to cancel a job before committing phase.
func (s *WizardService) CancelJob(ctx context.Context, jobID, sessionID string) error {
	return s.jobRunner.CancelJob(jobID, sessionID)
}

// Reveal exposes transient credentials for a completed job.
func (s *WizardService) Reveal(ctx context.Context, jobID, sessionID string) (*RevealCredentials, error) {
	return s.jobRunner.RevealSecrets(jobID, sessionID)
}

func (s *WizardService) resolveOrGenerateTgSecret(preserve bool) (string, error) {
	if preserve && s.tg != nil {
		if rev, err := s.tg.RevealSecret(); err == nil {
			sec := strings.TrimSpace(rev.Secret)
			if len(sec) == 32 {
				return sec, nil
			}
		}
	}
	secBytes := make([]byte, 16)
	if _, err := rand.Read(secBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(secBytes), nil
}

func (s *WizardService) resolveOrGenerateClient(preserve bool, existingUUID, remark string) (string, string, error) {
	resolvedRemark := strings.TrimSpace(remark)
	if resolvedRemark == "" {
		resolvedRemark = "Основное устройство"
	}
	if trimmed := strings.TrimSpace(existingUUID); trimmed != "" {
		if s.xray != nil {
			for _, c := range s.xray.GetConfig().Clients {
				if strings.EqualFold(strings.TrimSpace(c.ID), trimmed) && strings.TrimSpace(c.Remark) != "" {
					return trimmed, strings.TrimSpace(c.Remark), nil
				}
			}
		}
		return trimmed, resolvedRemark, nil
	}
	if preserve && s.xray != nil {
		cfg := s.xray.GetConfig()
		for _, c := range cfg.Clients {
			if c.Enabled && strings.EqualFold(strings.TrimSpace(c.Remark), resolvedRemark) && strings.TrimSpace(c.ID) != "" {
				return strings.TrimSpace(c.ID), strings.TrimSpace(c.Remark), nil
			}
		}
		for _, c := range cfg.Clients {
			if c.Enabled && strings.TrimSpace(c.ID) != "" {
				rem := strings.TrimSpace(c.Remark)
				if rem == "" {
					rem = resolvedRemark
				}
				return strings.TrimSpace(c.ID), rem, nil
			}
		}
	}
	uuidBytes := make([]byte, 16)
	if _, err := rand.Read(uuidBytes); err != nil {
		return "", "", err
	}
	uuidBytes[6] = (uuidBytes[6] & 0x0f) | 0x40
	uuidBytes[8] = (uuidBytes[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x",
		uuidBytes[0:4], uuidBytes[4:6], uuidBytes[6:8], uuidBytes[8:10], uuidBytes[10:16])
	return id, resolvedRemark, nil
}

func describeXrayEgress(mode, iface string) string {
	switch mode {
	case "router":
		return "firc:router"
	case "interface":
		if strings.TrimSpace(iface) != "" {
			return strings.TrimSpace(iface)
		}
		return "direct"
	default:
		return "direct"
	}
}

func describeTgEgress(upstream string) string {
	u := strings.TrimSpace(upstream)
	if u == "" || u == "direct" {
		return "direct"
	}
	return u
}

func telegramIngressPlacement(desired DesiredWizardConfig, cfg tgwebproxy.Config) (string, *bool) {
	isDirect := desired.ConnectionWay == "direct" || desired.CDNProfileID == "direct"
	if isDirect {
		isCDN := false
		if cfg.PublicHostname == "" {
			return tgwebproxy.PresetStaticIP, &isCDN
		}
		return tgwebproxy.InferEndpointPreset(tgwebproxy.WebProxyEndpoint{
			Hostname: cfg.PublicHostname,
			IsCDN:    false,
		}, cfg), &isCDN
	}
	isCDN := desired.ConnectionWay == "external_https" || desired.ConnectionWay == "cdn" || strings.HasPrefix(desired.CDNProfileID, "cdn_")
	if !isCDN {
		return "", nil
	}
	return tgwebproxy.InferEndpointPreset(tgwebproxy.WebProxyEndpoint{
		Hostname: cfg.PublicHostname,
		IsCDN:    true,
	}, cfg), &isCDN
}
