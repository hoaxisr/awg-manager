package router

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// dnsUpstreamTypes — типы DNS-серверов с сетевым upstream-адресом (поле
// server). local/fakeip адреса не имеют, dial-проверки к ним не применимы.
// tcp в validDNSTypes нашей модели нет, но в сыром 20-router.json он
// представим — учитываем, чтобы не «зеленить» такой конфиг молча.
var dnsUpstreamTypes = map[string]bool{
	"udp":   true,
	"tcp":   true,
	"tls":   true,
	"https": true,
	"quic":  true,
	"h3":    true,
}

// dnsServerAddrIsDomain: server задан доменным именем. IP-литералы (включая
// IPv6 в скобках), IP:port и URL-подобные значения доменом не считаются:
// «1.1.1.1:853» — это ошибка формата поля (порт живёт в server_port), а не
// домен, и совет «укажите IP-адрес» для неё звучал бы издевательски.
func dnsServerAddrIsDomain(server string) bool {
	addr := strings.TrimSpace(server)
	if addr == "" {
		return false
	}
	if strings.Contains(addr, "/") {
		return false
	}
	if _, err := netip.ParseAddrPort(addr); err == nil {
		return false
	}
	trimmed := strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
	if _, err := netip.ParseAddr(trimmed); err == nil {
		return false
	}
	return true
}

// computeDNSDialIssues — предупреждения о dial-ловушках DNS-серверов,
// которые проходят жёсткую валидацию, но молча ломают поведение в рантайме.
//
// Скоуп проверок сознательно узкий. НЕ проверяется здесь:
//   - домен в server без domain_resolver — merged config.d всегда несёт
//     route.default_domain_resolver из 00-base.json (self-heal), sing-box
//     валидирует именно merged-конфиг, предупреждение было бы вечным ложным
//     срабатыванием на поддерживаемый флоу приложения;
//   - ссылки domain_resolver на несуществующие серверы — резолвятся
//     КРОСС-слотово (90-user, 00-base), slot-local проверка давала бы ложные
//     «несуществующий»; реально битые ссылки отлавливает оркестратор при
//     reload-валидации.
//
// Что проверяется:
//   - detour вместе с domain_resolver — по документации dial-полей «If
//     enabled, all other fields will be ignored»: резолвер молча не
//     применяется;
//   - цикл резолверов внутри слота (доменный сервер, чья цепочка
//     domain_resolver возвращается в себя) — deadlock резолвинга, который
//     reload-валидация не ловит; каждый цикл репортится один раз;
//   - дубликаты тегов DNS-серверов — sing-box отвергает такой конфиг на
//     старте, а мимо UI-операций он мог доехать только руками в JSON.
func computeDNSDialIssues(cfg *RouterConfig) []Issue {
	var issues []Issue

	seenTags := make(map[string]bool, len(cfg.DNS.Servers))
	for _, s := range cfg.DNS.Servers {
		if seenTags[s.Tag] {
			issues = append(issues, Issue{
				Severity: "warning",
				Kind:     "dns-duplicate-tag",
				Tag:      s.Tag,
				Message:  fmt.Sprintf("дублирующийся тег DNS-сервера %q — sing-box отвергнет конфиг при старте", s.Tag),
			})
			continue
		}
		seenTags[s.Tag] = true
	}

	for _, s := range cfg.DNS.Servers {
		if !dnsUpstreamTypes[s.Type] {
			continue
		}
		if s.Detour != "" && s.DomainResolver != nil {
			issues = append(issues, Issue{
				Severity: "warning",
				Kind:     "dns-detour-dial",
				Tag:      s.Tag,
				Message:  fmt.Sprintf("DNS-сервер %q: при заданном detour остальные dial-поля игнорируются — domain_resolver не будет применён", s.Tag),
			})
		}
	}

	defaultResolver := ""
	if cfg.Route.DefaultDomainResolver != nil {
		defaultResolver = strings.TrimSpace(cfg.Route.DefaultDomainResolver.Server)
	}

	reportedCycles := make(map[string]bool)
	for _, s := range cfg.DNS.Servers {
		if !dnsUpstreamTypes[s.Type] || s.Detour != "" || !dnsServerAddrIsDomain(s.Server) {
			continue
		}
		loop := dnsResolverCycle(cfg, s.Tag, defaultResolver)
		// Репортим цикл только его участникам (loop[0] == s.Tag): сервер,
		// чья цепочка лишь ВЕДЁТ в чужой цикл, — жертва, а не причина; сам
		// цикл будет найден при обходе его участников.
		if len(loop) == 0 || loop[0] != s.Tag {
			continue
		}
		members := append([]string(nil), loop[:len(loop)-1]...)
		sort.Strings(members)
		key := strings.Join(members, "\x00")
		if reportedCycles[key] {
			continue
		}
		reportedCycles[key] = true
		issues = append(issues, Issue{
			Severity: "warning",
			Kind:     "dns-domain-resolver",
			Tag:      s.Tag,
			Message:  fmt.Sprintf("цикл domain_resolver (%s) — резолвинг доменного адреса зациклен", strings.Join(loop, " → ")),
		})
	}

	return issues
}

// dnsResolverCycle идёт по цепочке «доменный сервер → его резолвер» начиная
// со start и возвращает участок цикла ([b c b] для цепочки a→b→c→b), если
// цепочка возвращается в уже пройденный тег. Обрывают цепочку: сервер с
// IP-адресом (резолвер не нужен), сервер с detour (dial-поля, включая
// резолвер, игнорируются — адрес уезжает на удалённое разрешение), тег
// чужого слота (byTag его не знает — кросс-слотовые ссылки валидирует
// оркестратор). nil — цикла нет.
func dnsResolverCycle(cfg *RouterConfig, start, defaultResolver string) []string {
	byTag := make(map[string]*DNSServer, len(cfg.DNS.Servers))
	for i := range cfg.DNS.Servers {
		byTag[cfg.DNS.Servers[i].Tag] = &cfg.DNS.Servers[i]
	}

	visited := []string{start}
	index := map[string]int{start: 0}
	cur := byTag[start]
	for cur != nil && dnsUpstreamTypes[cur.Type] && cur.Detour == "" && dnsServerAddrIsDomain(cur.Server) {
		next := defaultResolver
		if cur.DomainResolver != nil && strings.TrimSpace(cur.DomainResolver.Server) != "" {
			next = strings.TrimSpace(cur.DomainResolver.Server)
		}
		if next == "" {
			return nil
		}
		if at, ok := index[next]; ok {
			return append(visited[at:], next)
		}
		index[next] = len(visited)
		visited = append(visited, next)
		cur = byTag[next]
	}
	return nil
}

// computeDNSChainIssues — предупреждения, которые sing-box тоже считает
// warning'ами (exit 0): evaluate, чей ответ никто ниже не использует, и
// speculative без race-правила выше. Жёсткие порядковые нарушения — не здесь
// (validateDNSChain).
//
// Потребление тега — только по тегу: прогон релизного бинаря 1.14.0-beta.1 на
// фикстуре b1 (тегированный evaluate «rd» + анонимный evaluate + анонимный
// match_response ниже) даёт exit 0 с WARN «evaluate tag is never referenced:
// rd» — анонимный match_response читает последний анонимный ответ и тегированный
// evaluate не потребляет. Контроль c1 (match_response «rd») — exit 0 без WARN.
func computeDNSChainIssues(cfg *RouterConfig) []Issue {
	if cfg == nil {
		return nil
	}
	var issues []Issue
	rules := cfg.DNS.Rules
	// потребители тега ниже позиции i
	consumedBelow := func(tag string, from int) bool {
		for j := from + 1; j < len(rules); j++ {
			if mr := rules[j].MatchResponse; mr.IsEnabled() && mr.Tag == tag {
				return true
			}
		}
		return false
	}
	raceAbove := false
	for i, r := range rules {
		if r.Action == "evaluate" && r.Tag != "" && !consumedBelow(r.Tag, i) {
			issues = append(issues, Issue{
				Severity: "warning", Kind: "dns-chain", RuleIndex: i, Tag: r.Tag,
				Message: fmt.Sprintf("evaluate %q: ответ никто ниже не использует", r.Tag),
			})
		}
		if r.Speculative && !raceAbove {
			issues = append(issues, Issue{
				Severity: "warning", Kind: "dns-chain", RuleIndex: i,
				Message: "speculative без race-правила выше не даёт эффекта",
			})
		}
		if r.Race {
			raceAbove = true
		}
	}
	return issues
}
