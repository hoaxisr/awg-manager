package router

import (
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// dnsChainTagPrefix — префикс тегов managed-правил DNS-пресета. Конвенция
// вместо wire-поля: правило принадлежит оверлею, если его evaluate-тег или
// ссылка match_response начинается с префикса. Резервируется в
// AddDNSRule/UpdateDNSRule, чтобы пользователь не мог создать самозванца.
const dnsChainTagPrefix = "awgm-dns-"

// defaultPoisonCIDRs — сид списка «отравленных» ответов для antipoison
// (редактируемый пользователем через DNSChainPresetState.PoisonCIDRs).
func defaultPoisonCIDRs() []string {
	return []string{"0.0.0.0/32", "127.0.0.0/8", "10.10.34.34/32", "10.10.34.35/32"}
}

// isManagedDNSChainRule — правило принадлежит оверлею DNS-пресета.
func isManagedDNSChainRule(r DNSRule) bool {
	return strings.HasPrefix(r.Tag, dnsChainTagPrefix) ||
		(r.MatchResponse != nil && strings.HasPrefix(r.MatchResponse.Tag, dnsChainTagPrefix))
}

// ensureDNSChainOverlay приводит DNS-правила к состоянию пресета st: удаляет
// все managed-правила и, если пресет включён, аппендит цепочку в КОНЕЦ списка.
// Идемпотентна и вызывается на каждой записи конфига, поэтому пользовательские
// правила, добавленные после включения пресета, не остаются за цепочкой.
// Ошибки: неизвестный режим, несуществующий сервер, evaluate на fakeip-сервере,
// некорректный poison-CIDR (всё через validateDNSRule).
func ensureDNSChainOverlay(cfg *RouterConfig, st *storage.DNSChainPresetState) error {
	kept := make([]DNSRule, 0, len(cfg.DNS.Rules))
	for _, r := range cfg.DNS.Rules {
		if !isManagedDNSChainRule(r) {
			kept = append(kept, r)
		}
	}
	if st == nil || st.Mode == "" {
		cfg.DNS.Rules = kept
		return nil
	}
	chain, err := dnsChainRules(cfg, st)
	if err != nil {
		return err
	}
	cfg.DNS.Rules = append(kept, chain...)
	return nil
}

// dnsChainRules собирает цепочку пресета и проверяет каждое её правило в
// контексте cfg (сервер существует, evaluate не на fakeip-сервере). Отдельно от
// ensureDNSChainOverlay — тем же вызовом валидируется пресет в SetDNSChainPreset,
// до того как состояние уйдёт в настройки.
func dnsChainRules(cfg *RouterConfig, st *storage.DNSChainPresetState) ([]DNSRule, error) {
	chain, err := buildDNSChain(st)
	if err != nil {
		return nil, err
	}
	types := cfg.dnsServerTypes()
	for _, r := range chain {
		if err := validateDNSRule(r, types); err != nil {
			return nil, fmt.Errorf("dns-пресет %q: %w", st.Mode, err)
		}
	}
	return chain, nil
}

// dnsChainPresetState читает состояние пресета из настроек. nil = пресет не
// включён (или Settings не сконфигурирован — тестовые сборки).
func (s *ServiceImpl) dnsChainPresetState() (*storage.DNSChainPresetState, error) {
	if s.deps.Settings == nil {
		return nil, nil
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return nil, fmt.Errorf("dns-пресет: load settings: %w", err)
	}
	if settings == nil {
		return nil, nil
	}
	return settings.DNSChainPreset, nil
}

// ensureDNSChainOverlayFromState — ensure-хук записи конфига (withConfig): при
// активном пресете переассертит его цепочку в конец DNS-правил, чтобы
// пользовательские Add/Update/Move не оставляли правил ЗА цепочкой. Ошибка
// ensure = ошибка мутации: правка, из-за которой цепочка перестаёт собираться
// (например force-снос сервера пресета), не сохраняется. Пресет выключен →
// no-op: снятие цепочки делает SetDNSChainPreset, а не каждая запись.
func (s *ServiceImpl) ensureDNSChainOverlayFromState(cfg *RouterConfig) error {
	st, err := s.dnsChainPresetState()
	if err != nil || st == nil || st.Mode == "" {
		return err
	}
	return ensureDNSChainOverlay(cfg, st)
}

// captureDNSChainServerRename переносит переименование DNS-сервера в состояние
// пресета ДО ensure-хука — иначе тот упал бы «сервер не найден» и переименование
// сервера, на который ссылается пресет, стало бы невозможным. Прецедент —
// captureFakeIPRealServerEdit (issue #487).
func (s *ServiceImpl) captureDNSChainServerRename(oldTag, newTag string) error {
	if oldTag == newTag {
		return nil
	}
	st, err := s.dnsChainPresetState()
	if err != nil || st == nil || st.Mode == "" {
		return err
	}
	if st.DirectServer != oldTag && st.ProxyServer != oldTag {
		return nil
	}
	updated := *st
	if updated.DirectServer == oldTag {
		updated.DirectServer = newTag
	}
	if updated.ProxyServer == oldTag {
		updated.ProxyServer = newTag
	}
	if err := s.deps.Settings.SetDNSChainPresetState(&updated); err != nil {
		return fmt.Errorf("dns-пресет: save settings: %w", err)
	}
	return nil
}

// buildDNSChain — эталонные формы цепочек (подтверждены прогоном sing-box
// 1.14.0-beta.1). dns.final пресет не трогает: не совпавшие правила уходят в
// фолбэк-сервер конфига.
func buildDNSChain(st *storage.DNSChainPresetState) ([]DNSRule, error) {
	switch st.Mode {
	case "resilient":
		// Оба резолвера опрашиваются параллельно, отвечает первый успешный.
		return []DNSRule{
			{Action: "evaluate", Server: st.DirectServer, Tag: dnsChainTagPrefix + "rd"},
			{Action: "evaluate", Server: st.ProxyServer, Tag: dnsChainTagPrefix + "rp"},
			{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: dnsChainTagPrefix + "rd"}, ResponseRcode: "NOERROR", Race: true, Action: "respond"},
			{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: dnsChainTagPrefix + "rp"}, ResponseRcode: "NOERROR", Race: true, Action: "respond"},
		}, nil
	case "antipoison":
		// Ответ прямого резолвера с «отравленным» адресом переспрашивается у
		// проксирующего; остальные ответы отдаются как есть.
		cidrs := st.PoisonCIDRs
		if len(cidrs) == 0 {
			cidrs = defaultPoisonCIDRs()
		}
		return []DNSRule{
			{Action: "evaluate", Server: st.DirectServer, Tag: dnsChainTagPrefix + "ap"},
			{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: dnsChainTagPrefix + "ap"}, IPCIDR: cidrs, Action: "route", Server: st.ProxyServer},
			{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: dnsChainTagPrefix + "ap"}, Action: "respond"},
		}, nil
	}
	return nil, fmt.Errorf("dns-пресет: неизвестный режим %q", st.Mode)
}
