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

// keenDNSAddrTTL — как долго доверять прочитанным статическим записям роутера.
const keenDNSAddrTTL = 5 * time.Minute

// KeenDNSDomainProvider returns the router's booked KeenDNS/CrazeDNS FQDN
// (e.g. "home.netcraze.pro"). Empty string = not configured.
type KeenDNSDomainProvider interface {
	KeenDNSDomain(ctx context.Context) (string, error)
}

// KeenDNSAddrProvider возвращает IPv4, которые роутер сам отдаёт по host
// своей статической записью. Пустой список = записи нет.
type KeenDNSAddrProvider interface {
	KeenDNSAddrs(ctx context.Context, host string) ([]string, error)
}

// KeenDNSRewriteSyncer приводит managed-набор DNS-перезаписей пресета keendns
// к (domain, ips); пустые аргументы = снести набор.
type KeenDNSRewriteSyncer interface {
	SyncManagedKeenDNS(domain string, ips []string) error
}

// SetKeenDNSPreset wires optional providers for the keendns bypass preset
// (domain rewrite path). Safe to call after NewService; nil syncer = no-op.
//
// Под keenDNSMu, а не под s.mu: dnsrewrite строится в setupListen, а
// startup-Reconcile уже крутится в горутине из setupRouter — то есть запись
// этих полей гарантированно конкурирует с их чтением.
func (s *ServiceImpl) SetKeenDNSPreset(domain KeenDNSDomainProvider, addr KeenDNSAddrProvider, sync KeenDNSRewriteSyncer) {
	s.keenDNSMu.Lock()
	defer s.keenDNSMu.Unlock()
	s.keenDNSDomain = domain
	s.keenDNSAddr = addr
	s.keenDNSSync = sync
}

// keenDNSPreset снимает согласованный снапшот провайдеров пресета.
func (s *ServiceImpl) keenDNSPreset() (KeenDNSDomainProvider, KeenDNSAddrProvider, KeenDNSRewriteSyncer) {
	s.keenDNSMu.Lock()
	defer s.keenDNSMu.Unlock()
	return s.keenDNSDomain, s.keenDNSAddr, s.keenDNSSync
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
	domainProv, addrProv, sync := s.keenDNSPreset()
	if sync == nil {
		return
	}
	if !slices.Contains(sr.BypassPresets, PresetKeenDNS) {
		s.setKeenDNSBypass(nil)
		if err := sync.SyncManagedKeenDNS("", nil); err != nil {
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
	if domain == "" {
		// Confirmed: no booked FQDN → drop managed rewrites.
		s.warnKeenDNSOnce("unbooked",
			"пресет keendns включён, но KeenDNS не забронирован — перезаписи не создаются")
		s.setKeenDNSBypass(nil)
		if err := sync.SyncManagedKeenDNS("", nil); err != nil {
			s.appLog.Warn("keendns-rewrite", "", err.Error())
		}
		return
	}

	ips := s.keenDNSAddrs(ctx, addrProv, domain)
	if len(ips) == 0 {
		// Имя известно, а статической записи роутера нет (KeenDNS в режиме
		// прямого доступа либо RCI не ответил) — прежние перезаписи и обход
		// оставляем как есть.
		s.warnKeenDNSOnce("noaddr",
			"пресет keendns включён, но роутер не отдаёт статическую запись для "+domain+" — оставляем прежний managed rewrite")
		return
	}
	if err := sync.SyncManagedKeenDNS(domain, ips); err != nil {
		s.appLog.Warn("keendns-rewrite", domain, err.Error())
		return
	}
	s.setKeenDNSBypass(ips)
	s.clearKeenDNSWarn()
}

// keenDNSAddrs отдаёт адреса статической записи роутера для host, кэшируя их
// на keenDNSAddrTTL: Reconcile зовёт синк каждые 30с, а адрес сервиса KeenDNS
// не меняется годами — ходить за ним в RCI на каждом тике незачем. Сбой
// запроса или пустой ответ отдают last-good того же имени, если он есть.
func (s *ServiceImpl) keenDNSAddrs(ctx context.Context, prov KeenDNSAddrProvider, host string) []string {
	if prov == nil {
		return nil
	}
	s.keenDNSMu.Lock()
	fresh := s.keenDNSAddrHost == host && time.Since(s.keenDNSAddrAt) < keenDNSAddrTTL
	cached := slices.Clone(s.keenDNSAddrIPs)
	s.keenDNSMu.Unlock()
	if fresh && len(cached) > 0 {
		return cached
	}

	qctx, cancel := context.WithTimeout(ctx, keenDNSQueryTimeout)
	ips, err := prov.KeenDNSAddrs(qctx, host)
	cancel()
	if err != nil || len(ips) == 0 {
		if err != nil {
			s.appLog.Warn("keendns-rewrite", host, "статические записи роутера: "+err.Error())
		}
		if s.keenDNSAddrHostMatches(host) {
			return cached
		}
		return nil
	}

	s.keenDNSMu.Lock()
	s.keenDNSAddrHost = host
	s.keenDNSAddrIPs = slices.Clone(ips)
	s.keenDNSAddrAt = time.Now()
	s.keenDNSMu.Unlock()
	return ips
}

func (s *ServiceImpl) keenDNSAddrHostMatches(host string) bool {
	s.keenDNSMu.Lock()
	defer s.keenDNSMu.Unlock()
	return s.keenDNSAddrHost == host && len(s.keenDNSAddrIPs) > 0
}

// setKeenDNSBypass запоминает адреса, которые должны обходить sing-box, в
// форме CIDR для iptables. Пустой список = обхода нет.
func (s *ServiceImpl) setKeenDNSBypass(ips []string) {
	var cidrs []string
	for _, ip := range ips {
		cidrs = append(cidrs, ip+"/32")
	}
	s.keenDNSMu.Lock()
	s.keenDNSBypassCIDRs = cidrs
	s.keenDNSMu.Unlock()
}

// keenDNSBypass отдаёт текущие CIDR обхода пресета keendns для RestoreInputSpec.
func (s *ServiceImpl) keenDNSBypass() []string {
	s.keenDNSMu.Lock()
	defer s.keenDNSMu.Unlock()
	return slices.Clone(s.keenDNSBypassCIDRs)
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
