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
	chain, err := buildDNSChain(st)
	if err != nil {
		return err
	}
	types := cfg.dnsServerTypes()
	for _, r := range chain {
		if err := validateDNSRule(r, types); err != nil {
			return fmt.Errorf("dns-пресет %q: %w", st.Mode, err)
		}
	}
	cfg.DNS.Rules = append(kept, chain...)
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
