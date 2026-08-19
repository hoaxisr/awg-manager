package router

import (
	"context"
	"slices"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// keenDNSQueryTimeout ограничивает поход в NDMS. Sync вызывается из
// Reconcile, который уже держит transitionMu, — стоящий RCI (backstop
// транспорта 30с) иначе заблокировал бы на это время пользовательские
// Enable/Disable и смену режима.
const keenDNSQueryTimeout = 5 * time.Second

// keenDNSInfoTTL — как долго доверять прочитанным с роутера данным.
const keenDNSInfoTTL = 5 * time.Minute

// KeenDNSInfoProvider отдаёт данные пресета keendns с самого роутера: FQDN,
// который роутеру выдал KeenDNS, и IPv4, к которым роутер направляет свои
// KeenDNS-имена (статические записи ndnproxy + адрес доступа в режиме direct).
// Пустые значения = KeenDNS не настроен.
type KeenDNSInfoProvider interface {
	KeenDNSInfo(ctx context.Context) (fqdn string, addrs []string, err error)
}

// KeenDNSPresetSyncer включает или снимает блок пресета keendns в слоте DNS.
type KeenDNSPresetSyncer interface {
	SetKeenDNSEnabled(on bool, extraDomain string) error
}

// SetKeenDNSPreset wires optional providers for the keendns bypass preset.
// Safe to call after NewService; nil syncer = no-op.
//
// Под keenDNSMu, а не под s.mu: dnsrewrite строится в setupListen, а
// startup-Reconcile уже крутится в горутине из setupRouter — то есть запись
// этих полей гарантированно конкурирует с их чтением.
func (s *ServiceImpl) SetKeenDNSPreset(info KeenDNSInfoProvider, sync KeenDNSPresetSyncer) {
	s.keenDNSMu.Lock()
	defer s.keenDNSMu.Unlock()
	s.keenDNSInfoProv = info
	s.keenDNSSync = sync
}

// keenDNSPreset снимает согласованный снапшот провайдеров пресета.
func (s *ServiceImpl) keenDNSPreset() (KeenDNSInfoProvider, KeenDNSPresetSyncer) {
	s.keenDNSMu.Lock()
	defer s.keenDNSMu.Unlock()
	return s.keenDNSInfoProv, s.keenDNSSync
}

// SyncKeenDNSPreset runs the keendns-preset sync against current settings.
// Used at boot after SetKeenDNSPreset; Reconcile also calls it.
func (s *ServiceImpl) SyncKeenDNSPreset(ctx context.Context) {
	if _, sync := s.keenDNSPreset(); sync == nil || s.deps.Settings == nil {
		return
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		s.appLog.Warn("keendns-preset", "", err.Error())
		return
	}
	sr, err := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if err != nil {
		s.appLog.Warn("keendns-preset", "", err.Error())
		return
	}
	s.syncKeenDNSPreset(ctx, sr)
}

// syncKeenDNSPreset приводит блок пресета в слоте DNS и обход в iptables к
// состоянию настроек. Best-effort: сбои логируются, но не валят
// Reconcile/Enable — путь iptables обязан работать дальше.
//
// Имена KeenDNS уходят резолверу самого роутера, поэтому список доменов от
// NDMS не зависит: провайдер нужен только ради адресов обхода и FQDN вне
// известных зон. Сбой NDMS оставляет последний удачный обход как есть.
func (s *ServiceImpl) syncKeenDNSPreset(ctx context.Context, sr storage.SingboxRouterSettings) {
	infoProv, sync := s.keenDNSPreset()
	if sync == nil {
		return
	}
	if !slices.Contains(sr.BypassPresets, PresetKeenDNS) {
		s.setKeenDNSBypass(nil)
		if err := sync.SetKeenDNSEnabled(false, ""); err != nil {
			s.appLog.Warn("keendns-preset", "", err.Error())
		}
		return
	}

	fqdn, addrs, ok := s.keenDNSInfo(ctx, infoProv)
	if err := sync.SetKeenDNSEnabled(true, fqdn); err != nil {
		s.appLog.Warn("keendns-preset", fqdn, err.Error())
		return
	}
	if ok {
		s.setKeenDNSBypass(addrs)
	}
}

// keenDNSInfo отдаёт (FQDN, адреса обхода) с роутера, кэшируя их на
// keenDNSInfoTTL: Reconcile зовёт синк каждые 30с, а адреса KeenDNS не
// меняются годами. Третье значение = «данные достоверны»; при сбое RCI без
// last-good оно false, и вызывающий не трогает уже установленный обход.
func (s *ServiceImpl) keenDNSInfo(ctx context.Context, prov KeenDNSInfoProvider) (string, []string, bool) {
	if prov == nil {
		return "", nil, false
	}
	s.keenDNSMu.Lock()
	fresh := !s.keenDNSInfoAt.IsZero() && time.Since(s.keenDNSInfoAt) < keenDNSInfoTTL
	cachedFQDN, cachedAddrs := s.keenDNSFQDN, slices.Clone(s.keenDNSAddrs)
	s.keenDNSMu.Unlock()
	if fresh {
		return cachedFQDN, cachedAddrs, true
	}

	qctx, cancel := context.WithTimeout(ctx, keenDNSQueryTimeout)
	fqdn, addrs, err := prov.KeenDNSInfo(qctx)
	cancel()
	if err != nil {
		s.warnKeenDNSOnce("err", "данные KeenDNS с роутера: "+err.Error())
		if !s.keenDNSInfoAt.IsZero() {
			return cachedFQDN, cachedAddrs, true
		}
		return "", nil, false
	}
	if len(addrs) == 0 {
		// Адресов роутер не отдал: KeenDNS не настроен, прошивка без
		// /show/ndns либо RCI ответил пустотой. Правило DNS всё равно нужно
		// (порталы my.keenetic.net обслуживаются всегда), а прежний обход
		// не снимаем — иначе разовая пустота на 5 минут вернула бы issue.
		s.warnKeenDNSOnce("noaddr",
			"пресет keendns включён, но роутер не отдаёт адресов своих KeenDNS-имён")
		addrs = cachedAddrs
	} else {
		s.clearKeenDNSWarn()
	}

	s.keenDNSMu.Lock()
	s.keenDNSFQDN = fqdn
	s.keenDNSAddrs = slices.Clone(addrs)
	s.keenDNSInfoAt = time.Now()
	s.keenDNSMu.Unlock()
	return fqdn, addrs, true
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
	s.appLog.Warn("keendns-preset", "", msg)
}

func (s *ServiceImpl) clearKeenDNSWarn() {
	s.keenDNSMu.Lock()
	s.keenDNSWarnState = ""
	s.keenDNSMu.Unlock()
}
