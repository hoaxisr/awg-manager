package router

import (
	"context"
	"slices"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// keenDNSQueryTimeout ограничивает поход в NDMS за доменом. Sync вызывается
// из Reconcile, который уже держит transitionMu, — стоящий RCI (backstop
// транспорта 30с) иначе заблокировал бы на это время пользовательские
// Enable/Disable и смену режима.
const keenDNSQueryTimeout = 5 * time.Second

// KeenDNSDomainProvider returns the router's booked KeenDNS/CrazeDNS FQDN
// (e.g. "home.netcraze.pro"). Empty string = not configured.
type KeenDNSDomainProvider interface {
	KeenDNSDomain(ctx context.Context) (string, error)
}

// LANIPv4Provider returns the LAN-bridge IPv4 used as the rewrite target.
type LANIPv4Provider interface {
	LANIPv4() string
}

// KeenDNSRewriteSyncer приводит managed-набор DNS-перезаписей пресета keendns
// к (domain, lanIP); пустые аргументы = снести набор.
type KeenDNSRewriteSyncer interface {
	SyncManagedKeenDNS(domain, lanIP string) error
}

// SetKeenDNSPreset wires optional providers for the keendns bypass preset
// (domain rewrite path). Safe to call after NewService; nil syncer = no-op.
//
// Под keenDNSMu, а не под s.mu: dnsrewrite строится в setupListen, а
// startup-Reconcile уже крутится в горутине из setupRouter — то есть запись
// этих полей гарантированно конкурирует с их чтением.
func (s *ServiceImpl) SetKeenDNSPreset(domain KeenDNSDomainProvider, lan LANIPv4Provider, sync KeenDNSRewriteSyncer) {
	s.keenDNSMu.Lock()
	defer s.keenDNSMu.Unlock()
	s.keenDNSDomain = domain
	s.keenDNSLAN = lan
	s.keenDNSSync = sync
}

// keenDNSPreset снимает согласованный снапшот провайдеров пресета.
func (s *ServiceImpl) keenDNSPreset() (KeenDNSDomainProvider, LANIPv4Provider, KeenDNSRewriteSyncer) {
	s.keenDNSMu.Lock()
	defer s.keenDNSMu.Unlock()
	return s.keenDNSDomain, s.keenDNSLAN, s.keenDNSSync
}

// SyncKeenDNSRewrites runs the keendns-preset rewrite sync against current
// settings. Used at boot after SetKeenDNSPreset; Reconcile also calls it.
func (s *ServiceImpl) SyncKeenDNSRewrites(ctx context.Context) {
	if _, _, sync := s.keenDNSPreset(); sync == nil || s.deps.Settings == nil {
		return
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		s.appLog.Warn("keendns-rewrite", "", err.Error())
		return
	}
	sr, err := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if err != nil {
		s.appLog.Warn("keendns-rewrite", "", err.Error())
		return
	}
	s.syncKeenDNSRewrites(ctx, sr)
}

// syncKeenDNSRewrites applies or clears managed DNS rewrites when the
// keendns preset is toggled. Best-effort: failures are logged, never
// fail Reconcile/Enable (iptables path must keep working).
//
// Destructive clear only when the preset is off, or when KeenDNS is
// confirmed unbooked (empty domain, no provider error). Transient NDMS
// errors or a missing LAN IP leave existing managed rewrites intact.
func (s *ServiceImpl) syncKeenDNSRewrites(ctx context.Context, sr storage.SingboxRouterSettings) {
	domainProv, lanProv, sync := s.keenDNSPreset()
	if sync == nil {
		return
	}
	if !slices.Contains(sr.BypassPresets, PresetKeenDNS) {
		if err := sync.SyncManagedKeenDNS("", ""); err != nil {
			s.appLog.Warn("keendns-rewrite", "", err.Error())
		}
		return
	}

	var domain string
	if domainProv != nil {
		qctx, cancel := context.WithTimeout(ctx, keenDNSQueryTimeout)
		d, err := domainProv.KeenDNSDomain(qctx)
		cancel()
		if err != nil {
			// Keep last-good rewrites — do not treat a flap as "unbooked".
			s.warnKeenDNSOnce("err", "KeenDNS domain: "+err.Error())
			return
		}
		domain = d
	}
	var lan string
	if lanProv != nil {
		lan = lanProv.LANIPv4()
	}

	if domain == "" {
		// Confirmed: no booked FQDN → drop managed rewrites.
		s.warnKeenDNSOnce("unbooked",
			"пресет keendns включён, но KeenDNS не забронирован — перезаписи не создаются")
		if err := sync.SyncManagedKeenDNS("", ""); err != nil {
			s.appLog.Warn("keendns-rewrite", "", err.Error())
		}
		return
	}
	if lan == "" {
		// Domain known but LAN IP missing (iface down) — keep last rewrite.
		s.warnKeenDNSOnce("nolan",
			"пресет keendns включён, но нет LAN IP — оставляем прежний managed rewrite")
		return
	}
	if err := sync.SyncManagedKeenDNS(domain, lan); err != nil {
		s.appLog.Warn("keendns-rewrite", domain, err.Error())
		return
	}
	s.clearKeenDNSWarn()
}

// warnKeenDNSOnce пишет предупреждение только при СМЕНЕ состояния. Пресет
// включён по умолчанию, а Reconcile тикает каждые 30с: без этого гварда
// штатная конфигурация без KeenDNS (на прошивке без подсистемы /show/ndns
// отвечает 404) вечно писала бы warn в журнал.
func (s *ServiceImpl) warnKeenDNSOnce(state, msg string) {
	s.keenDNSMu.Lock()
	repeat := s.keenDNSWarnState == state
	s.keenDNSWarnState = state
	s.keenDNSMu.Unlock()
	if repeat {
		return
	}
	s.appLog.Warn("keendns-rewrite", "", msg)
}

func (s *ServiceImpl) clearKeenDNSWarn() {
	s.keenDNSMu.Lock()
	s.keenDNSWarnState = ""
	s.keenDNSMu.Unlock()
}
