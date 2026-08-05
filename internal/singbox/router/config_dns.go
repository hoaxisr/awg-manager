package router

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

var validDNSTypes = map[string]bool{
	"udp":    true,
	"tls":    true,
	"https":  true,
	"quic":   true,
	"h3":     true,
	"local":  true,
	"fakeip": true,
}

var validDNSStrategies = map[string]bool{
	"":            true,
	"prefer_ipv4": true,
	"prefer_ipv6": true,
	"ipv4_only":   true,
	"ipv6_only":   true,
}

var validTLSVersions = map[string]int{
	"1.0": 10,
	"1.1": 11,
	"1.2": 12,
	"1.3": 13,
}

var dnsTLSServerTypes = map[string]bool{
	"tls": true, "quic": true, "https": true, "h3": true,
}

var validDNSRuleActions = map[string]bool{
	"":           true,
	"route":      true,
	"reject":     true,
	"predefined": true,
	"evaluate":   true,
	"respond":    true,
}

// validDNSRcodes — словарь miekg dns.StringToRcode (upstream принимает его
// целиком, плюс числовую форму). Мы чуть строже: без DSOTYPENI и без алиаса
// NOTIMPL, числовую форму тоже не принимаем — сознательное сужение.
var validDNSRcodes = map[string]bool{
	"NOERROR": true, "FORMERR": true, "SERVFAIL": true, "NXDOMAIN": true,
	"NOTIMP": true, "REFUSED": true, "YXDOMAIN": true, "YXRRSET": true,
	"NXRRSET": true, "NOTAUTH": true, "NOTZONE": true, "BADSIG": true,
	"BADKEY": true, "BADTIME": true, "BADMODE": true, "BADNAME": true,
	"BADALG": true, "BADTRUNC": true, "BADCOOKIE": true,
}

var validRejectMethods = map[string]bool{
	"": true, "default": true, "drop": true,
}

const managedDNSDirectTag = "dns-direct"

// scrubDNSServerDetourStored normalizes only values that must never be kept in
// the editable router config (explicit direct → empty). Legacy detour on
// dns-direct is preserved so the UI can warn until the user saves.
func scrubDNSServerDetourStored(s *DNSServer) {
	if strings.TrimSpace(s.Detour) == "direct" {
		s.Detour = ""
	}
}

// scrubDNSServerDetourForSingbox clears detour values that must not reach
// sing-box: empty, explicit "direct", and any detour on dns-direct. A named
// detour makes sing-box ignore other dial fields, so it also clears the stale
// domain resolver instead of retaining a misleading configuration.
func scrubDNSServerDetourForSingbox(s *DNSServer) {
	d := strings.TrimSpace(s.Detour)
	if d == "" || d == "direct" || s.Tag == managedDNSDirectTag {
		s.Detour = ""
	} else {
		s.Detour = d
		s.DomainResolver = nil
	}
}

// SanitizeDNSConfigForSingbox prepares DNS servers for the routing slots / sing-box.
func SanitizeDNSConfigForSingbox(cfg *RouterConfig) {
	if cfg == nil {
		return
	}
	for i := range cfg.DNS.Servers {
		scrubDNSServerDetourForSingbox(&cfg.DNS.Servers[i])
	}
}

// SanitizeDNSConfig normalizes stored router config after load (direct only).
func SanitizeDNSConfig(cfg *RouterConfig) {
	if cfg == nil {
		return
	}
	for i := range cfg.DNS.Servers {
		scrubDNSServerDetourStored(&cfg.DNS.Servers[i])
	}
}

func validateDNSServer(s DNSServer) error {
	if strings.TrimSpace(s.Tag) == "" {
		return fmt.Errorf("dns server tag is required")
	}
	if !validDNSTypes[s.Type] {
		return fmt.Errorf("dns server %q: unknown type %q", s.Tag, s.Type)
	}
	if s.Type == "fakeip" {
		if strings.TrimSpace(s.Inet4Range) == "" && strings.TrimSpace(s.Inet6Range) == "" {
			return fmt.Errorf("dns server %q: fakeip requires inet4_range or inet6_range", s.Tag)
		}
		if r := strings.TrimSpace(s.Inet4Range); r != "" {
			p, err := netip.ParsePrefix(r)
			if err != nil {
				return fmt.Errorf("dns server %q: invalid inet4_range %q: %w", s.Tag, r, err)
			}
			if !p.Addr().Is4() {
				return fmt.Errorf("dns server %q: inet4_range %q is not IPv4", s.Tag, r)
			}
		}
		if r := strings.TrimSpace(s.Inet6Range); r != "" {
			p, err := netip.ParsePrefix(r)
			if err != nil {
				return fmt.Errorf("dns server %q: invalid inet6_range %q: %w", s.Tag, r, err)
			}
			if p.Addr().Is4() {
				return fmt.Errorf("dns server %q: inet6_range %q is not IPv6", s.Tag, r)
			}
		}
		return nil
	}
	if s.Type != "local" && strings.TrimSpace(s.Server) == "" {
		return fmt.Errorf("dns server %q: server is required", s.Tag)
	}
	if s.ServerPort < 0 || s.ServerPort > 65535 {
		return fmt.Errorf("dns server %q: server_port %d out of range", s.Tag, s.ServerPort)
	}
	if !validDNSStrategies[s.Strategy] {
		return fmt.Errorf("dns server %q: unknown strategy %q", s.Tag, s.Strategy)
	}
	if s.DomainResolver != nil {
		if strings.TrimSpace(s.DomainResolver.Server) == "" {
			return fmt.Errorf("dns server %q: domain_resolver.server is required", s.Tag)
		}
		if !validDNSStrategies[s.DomainResolver.Strategy] {
			return fmt.Errorf("dns server %q: domain_resolver.strategy %q unknown", s.Tag, s.DomainResolver.Strategy)
		}
	}
	if s.TLS != nil {
		if !dnsTLSServerTypes[s.Type] {
			return fmt.Errorf("dns server %q: tls is unsupported for type %q", s.Tag, s.Type)
		}
		if s.TLS.MinVersion != "" && validTLSVersions[s.TLS.MinVersion] == 0 {
			return fmt.Errorf("dns server %q: unknown tls min_version %q", s.Tag, s.TLS.MinVersion)
		}
		if s.TLS.MaxVersion != "" && validTLSVersions[s.TLS.MaxVersion] == 0 {
			return fmt.Errorf("dns server %q: unknown tls max_version %q", s.Tag, s.TLS.MaxVersion)
		}
		if s.TLS.MinVersion != "" && s.TLS.MaxVersion != "" && validTLSVersions[s.TLS.MinVersion] > validTLSVersions[s.TLS.MaxVersion] {
			return fmt.Errorf("dns server %q: tls min_version must not exceed max_version", s.Tag)
		}
	}
	return nil
}

func validateDNSRule(r DNSRule, servers map[string]string) error {
	if !dnsRuleHasMatcher(r) {
		// A matcher-less DNS rule is valid sing-box — it matches every query
		// (catch-all). Permit it PROVIDED it carries a server or an action;
		// a bare rule with neither is a no-op and almost certainly a mistake
		// (bug #445 phase 3). A catch-all with a server is the frontend
		// contract: a normal DNSRule with zero matchers plus a `server`.
		if strings.TrimSpace(r.Server) == "" && strings.TrimSpace(r.Action) == "" {
			return ErrInvalidMatchers
		}
	}
	for _, c := range r.SourceIPCIDR {
		if err := validateCIDROrAddr("dns rule: invalid source_ip_cidr", c); err != nil {
			return err
		}
	}
	for _, rx := range r.DomainRegex {
		if _, err := regexp.Compile(rx); err != nil {
			return fmt.Errorf("dns rule: invalid domain_regex %q: %w", rx, err)
		}
	}
	if !validDNSRuleActions[r.Action] {
		return fmt.Errorf("dns rule: unknown action %q", r.Action)
	}
	// Инварианты sing-box 1.14 (сверено живыми прогонами sing-box check
	// beta.1): parse-FATAL'ы полей. Порядок правил — validateDNSChain.
	if r.Tag != "" && r.Action != "evaluate" {
		return fmt.Errorf("dns rule: tag допустим только для action=evaluate")
	}
	switch r.Action {
	case "respond", "reject", "predefined":
		if r.Speculative {
			return fmt.Errorf("dns rule: speculative недопустим для action=%s", r.Action)
		}
	}
	if r.Race {
		if !r.MatchResponse.IsEnabled() {
			return fmt.Errorf("dns rule: race требует match_response")
		}
		if r.Action == "evaluate" {
			return fmt.Errorf("dns rule: race требует финального действия (route/respond/reject/predefined)")
		}
		if r.Speculative {
			return fmt.Errorf("dns rule: race и speculative несовместимы")
		}
	}
	// ip_cidr/response_* без match_response: sing-box FATAL'ит их, как только
	// в конфиге появляется первое evaluate/respond/race-правило (legacy-режим
	// выключается конфиг-уровнево). Требуем match_response всегда, чтобы
	// валидное сегодня правило не взрывалось от добавления соседнего evaluate.
	hasResponseFields := r.ResponseRcode != "" || len(r.ResponseAnswer) > 0 ||
		len(r.ResponseNS) > 0 || len(r.ResponseExtra) > 0
	if (hasResponseFields || len(r.IPCIDR) > 0) && !r.MatchResponse.IsEnabled() {
		return fmt.Errorf("dns rule: ip_cidr и response_* поля требуют match_response")
	}
	if r.ResponseRcode != "" && !validDNSRcodes[r.ResponseRcode] {
		return fmt.Errorf("dns rule: unknown response_rcode %q", r.ResponseRcode)
	}
	for _, recs := range [][]string{r.ResponseAnswer, r.ResponseNS, r.ResponseExtra} {
		for _, rec := range recs {
			if strings.TrimSpace(rec) == "" {
				return fmt.Errorf("dns rule: пустая запись в response_answer/ns/extra")
			}
		}
	}
	for _, c := range r.IPCIDR {
		if err := validateCIDROrAddr("dns rule: invalid ip_cidr", c); err != nil {
			return err
		}
	}
	switch r.Action {
	case "reject":
		if !validRejectMethods[r.RejectMethod] {
			return fmt.Errorf("dns rule: unknown reject method %q", r.RejectMethod)
		}
		return nil
	case "predefined":
		if r.Rcode != "" && !validDNSRcodes[r.Rcode] {
			return fmt.Errorf("dns rule: unknown rcode %q", r.Rcode)
		}
		return nil
	case "respond":
		// respond у sing-box отвергает чужие поля на парсе (unknown field):
		// server (route), rcode (predefined), method (reject).
		if strings.TrimSpace(r.Server) != "" {
			return fmt.Errorf("dns rule: server недопустим для action=respond")
		}
		if strings.TrimSpace(r.Rcode) != "" {
			return fmt.Errorf("dns rule: rcode недопустим для action=respond")
		}
		if strings.TrimSpace(r.RejectMethod) != "" {
			return fmt.Errorf("dns rule: method недопустим для action=respond")
		}
		return nil
	}
	// route/evaluate (или пустое действие)
	if strings.TrimSpace(r.Server) == "" {
		return fmt.Errorf("dns rule: server is required when action is route/evaluate")
	}
	if _, ok := servers[r.Server]; !ok {
		return fmt.Errorf("%w: %q", ErrDNSInvalidServer, r.Server)
	}
	if r.Action == "evaluate" && servers[r.Server] == "fakeip" {
		return fmt.Errorf("dns rule: evaluate не может использовать fakeip-сервер %q", r.Server)
	}
	return nil
}

// validateDNSChain — порядковые инварианты цепочек evaluate/match_response.
// Всё это FATAL у `sing-box check` beta.1 (dns/router.go
// validateLegacyDNSModeDisabledRules), поэтому жёстко: битая цепочка не должна
// доехать до apply. Прогоны релизного бинаря 1.14.0-beta.1 (fixtures):
//   - тегированный evaluate + анонимный match_response ниже (a1) → exit 1,
//     «requires a preceding evaluate action without `tag`»: тег анонимного
//     потребителя не удовлетворяет, требуем анонимный evaluate;
//   - два анонимных evaluate подряд (a2) → exit 0 (только WARN
//     «overwritten … before any use»): дубль анонимных — не ошибка;
//   - respond без match_response и без evaluate выше (c6) → exit 1, а после
//     анонимного evaluate (c5) → exit 0: bare respond — анонимный потребитель.
func validateDNSChain(rules []DNSRule) error {
	_, err := firstDNSChainViolation(rules)
	return err
}

// firstDNSChainViolation возвращает индекс первого правила, ломающего цепочку,
// и саму ошибку (иначе -1, nil). Индекс нужен force-удалению сервера, которое
// вычищает битые правила до фикс-пойнта.
func firstDNSChainViolation(rules []DNSRule) (int, error) {
	seenTags := map[string]bool{}
	anonymousAbove := false
	for i, r := range rules {
		if r.MatchResponse.IsEnabled() {
			if tag := r.MatchResponse.Tag; tag != "" {
				if !seenTags[tag] {
					return i, fmt.Errorf("dns rule %d: match_response %q — выше нет evaluate с этим тегом", i, tag)
				}
			} else if !anonymousAbove {
				return i, fmt.Errorf("dns rule %d: match_response — выше нет анонимного evaluate", i)
			}
		} else if r.Action == "respond" {
			// respond без match_response — анонимный потребитель (needsAnonymous).
			if !anonymousAbove {
				return i, fmt.Errorf("dns rule %d: respond без match_response требует анонимного evaluate выше", i)
			}
		}
		if r.Action == "evaluate" {
			if r.Tag == "" {
				anonymousAbove = true
			} else {
				if seenTags[r.Tag] {
					return i, fmt.Errorf("dns rule %d: дубль тега evaluate %q", i, r.Tag)
				}
				seenTags[r.Tag] = true
			}
		}
	}
	return -1, nil
}

func dnsRuleHasMatcher(r DNSRule) bool {
	return len(r.RuleSet) > 0 ||
		len(r.SourceIPCIDR) > 0 ||
		len(r.DomainSuffix) > 0 ||
		len(r.Domain) > 0 ||
		len(r.DomainKeyword) > 0 ||
		len(r.DomainRegex) > 0 ||
		r.MatchResponse.IsEnabled() ||
		len(r.IPCIDR) > 0 ||
		len(r.QueryType) > 0
}

// DNSRulesShadowedByCatchAll returns the indices of DNS rules that can never
// match because an earlier rule is a matcher-less catch-all: sing-box
// evaluates DNS rules top-down and a matcher-less rule matches every query, so
// nothing after it is ever reached. Returns nil when there is no catch-all or
// nothing follows it. Intended for the UI / validator to surface dead rules
// (bug #445 phase 3). A matcher-less `evaluate` is not a catch-all: it does not
// terminate the chain and therefore shadows nothing (sing-box 1.14).
func (c *RouterConfig) DNSRulesShadowedByCatchAll() []int {
	catchAll := -1
	for i := range c.DNS.Rules {
		// evaluate не терминирует цепочку — не затеняет (sing-box 1.14).
		if c.DNS.Rules[i].Action == "evaluate" {
			continue
		}
		if !dnsRuleHasMatcher(c.DNS.Rules[i]) {
			catchAll = i
			break
		}
	}
	if catchAll < 0 || catchAll == len(c.DNS.Rules)-1 {
		return nil
	}
	shadowed := make([]int, 0, len(c.DNS.Rules)-catchAll-1)
	for i := catchAll + 1; i < len(c.DNS.Rules); i++ {
		shadowed = append(shadowed, i)
	}
	return shadowed
}

// dnsServerTypes возвращает карту тег → тип DNS-сервера: тип нужен валидатору
// правил (evaluate несовместим с fakeip-сервером).
func (c *RouterConfig) dnsServerTypes() map[string]string {
	types := make(map[string]string, len(c.DNS.Servers))
	for _, s := range c.DNS.Servers {
		types[s.Tag] = s.Type
	}
	return types
}

// engineDNSServerTags — теги DNS-серверов, которые ставит движок fakeip-режима
// (`20-fakeip.json`): пул синтетических адресов и настоящий upstream. Заняты
// нами, пользователю их брать нельзя.
//
// Почему это валидируется, а не «просто не делайте так»: сервер с тегом `real`,
// заведённый пользователем в режиме tproxy, живёт в общем слоте, который
// активен во ВСЕХ режимах. Переключение в fakeip добавляет движковый `real` в
// режимный слот — merged-конфиг получает два одинаковых тега, наш кросс-слот
// валидатор отдаёт duplicate-dns, и reload блокируется навсегда (движок молча
// работает на старом конфиге).
var engineDNSServerTags = map[string]bool{"real": true, "fakeip": true}

// checkDNSServerTagReserved отвергает движковый тег. Проверяется только при
// СОЗДАНИИ сервера и при переименовании В движковый тег: правка уже
// существующего `real` (смена upstream-адреса, issue #487) — поддержанный
// сценарий fakeip-редактора и обязана проходить.
func checkDNSServerTagReserved(tag string) error {
	if engineDNSServerTags[strings.TrimSpace(tag)] {
		return fmt.Errorf("%w: тег %q зарезервирован движком fakeip-режима", ErrDNSServerTagConflict, tag)
	}
	return nil
}

// domainResolverResolvable сообщает, годится ли тег в domain_resolver.
//
// Кроме серверов этого слота (local) принимаются теги СОСЕДНИХ слотов
// (external): sing-box сливает config.d в один документ и резолвит ссылки
// глобально — ровно так же судит кросс-слотовый валидатор оркестратора
// (unknown-dns-server в validateWithEnabled). Знай проверка только свой слот,
// правка сервера с чужим резолвером падала бы навсегда: миграция 5D0
// (pickDomainResolverFor) законно целит hostname-сервер на dns-bootstrap из
// 00-base.json, а снять резолвер нельзя — hostname-сервер без него роняет
// `sing-box check`. Пустой external (тесты, вызовы без оркестратора) сохраняет
// прежнее поведение «только свой слот».
func domainResolverResolvable(tag string, local map[string]string, external map[string]bool) bool {
	if _, ok := local[tag]; ok {
		return true
	}
	return external[tag]
}

// dnsResolverCycleWith сообщает, замкнётся ли цепочка domain_resolver в кольцо,
// если сервер replaced (пустая строка — ничего не заменяется, это add) станет
// сервером next.
//
// Зачем отдельная проверка: кольцо `a -> b -> a` — и вырожденное `a -> a` —
// наш валидатор пропускает, потому что он гоняет `sing-box check`, а тот такой
// конфиг принимает. Ронять движок кольцо будет на СТАРТЕ: «start service:
// circular server dependency: a -> b -> a» (проверено живым бинарём). То есть
// пользователь видит «сохранено», reload отрабатывает, а следующий подъём
// sing-box уже не случится.
//
// Цепочка обходится ТОЛЬКО от изменённого сервера: остальной граф был без
// колец до правки, и новое кольцо обязано проходить через него. Ссылка на
// сервер соседнего слота (типичный dns-bootstrap из 00-base.json) обрывает
// обход — чужих резолверов мы отсюда не видим, а кольцо через них собрать
// нечем: базовый слот ссылок на слот маршрутизации не держит.
func (c *RouterConfig) dnsResolverCycleWith(replaced string, next DNSServer) bool {
	resolverOf := func(tag string) (string, bool) {
		if tag == next.Tag {
			if next.DomainResolver == nil {
				return "", true
			}
			return next.DomainResolver.Server, true
		}
		for _, s := range c.DNS.Servers {
			if s.Tag != tag || (replaced != "" && s.Tag == replaced) {
				continue
			}
			if s.DomainResolver == nil {
				return "", true
			}
			// Переименование сервера переписывает ссылки на него
			// (renameDNSServerReferences), поэтому кольцо считается по
			// КОНЕЧНОМУ виду конфига, а не по текущему.
			if replaced != "" && s.DomainResolver.Server == replaced {
				return next.Tag, true
			}
			return s.DomainResolver.Server, true
		}
		return "", false
	}
	seen := map[string]bool{next.Tag: true}
	for cur := next.Tag; ; {
		to, ok := resolverOf(cur)
		if !ok || to == "" {
			return false
		}
		if seen[to] {
			return true
		}
		seen[to] = true
		cur = to
	}
}

// AddDNSServer / UpdateDNSServer — обёртки без знания о соседних слотах:
// резолвер ищется только среди серверов ЭТОГО конфига. Продакшен ходит через
// сервис (service_dns.go / service_fakeip_crud.go), который подмешивает теги
// включённых слотов из оркестратора.
func (c *RouterConfig) AddDNSServer(s DNSServer) error {
	return c.addDNSServer(s, nil)
}

func (c *RouterConfig) addDNSServer(s DNSServer, external map[string]bool) error {
	if err := validateDNSServer(s); err != nil {
		return err
	}
	if err := checkDNSServerTagReserved(s.Tag); err != nil {
		return err
	}
	return c.addEngineDNSServer(s, external)
}

// addEngineDNSServer — тот же addDNSServer, но БЕЗ запрета на движковый тег:
// серверы `fakeip` и `real` заводит сам движок fakeip-режима, и это
// единственный легальный путь их появления. Валидация формы (тип, диапазоны
// пула, ссылка domain_resolver) остаётся полной.
func (c *RouterConfig) addEngineDNSServer(s DNSServer, external map[string]bool) error {
	scrubDNSServerDetourForSingbox(&s)
	if err := validateDNSServer(s); err != nil {
		return err
	}
	for _, existing := range c.DNS.Servers {
		if existing.Tag == s.Tag {
			return fmt.Errorf("%w: %q", ErrDNSServerTagConflict, s.Tag)
		}
	}
	if s.DomainResolver != nil && s.DomainResolver.Server != s.Tag {
		if !domainResolverResolvable(s.DomainResolver.Server, c.dnsServerTypes(), external) {
			return fmt.Errorf("%w: domain_resolver.server %q not found", ErrDNSServerNotFound, s.DomainResolver.Server)
		}
	}
	if c.dnsResolverCycleWith("", s) {
		return fmt.Errorf("%w: %q", ErrDNSResolverCycle, s.Tag)
	}
	c.DNS.Servers = append(c.DNS.Servers, s)
	return nil
}

func (c *RouterConfig) UpdateDNSServer(tag string, s DNSServer) error {
	return c.updateDNSServer(tag, s, nil)
}

func (c *RouterConfig) updateDNSServer(tag string, s DNSServer, external map[string]bool) error {
	scrubDNSServerDetourForSingbox(&s)
	if err := validateDNSServer(s); err != nil {
		return err
	}
	if s.Tag != tag {
		if err := checkDNSServerTagReserved(s.Tag); err != nil {
			return err
		}
	}
	idx := -1
	for i, existing := range c.DNS.Servers {
		if existing.Tag == tag {
			idx = i
			continue
		}
		if existing.Tag == s.Tag && tag != s.Tag {
			return fmt.Errorf("%w: %q", ErrDNSServerTagConflict, s.Tag)
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: %q", ErrDNSServerNotFound, tag)
	}
	if s.DomainResolver != nil {
		types := c.dnsServerTypes()
		delete(types, tag)
		types[s.Tag] = s.Type
		if !domainResolverResolvable(s.DomainResolver.Server, types, external) {
			return fmt.Errorf("%w: domain_resolver.server %q not found", ErrDNSServerNotFound, s.DomainResolver.Server)
		}
	}
	if c.dnsResolverCycleWith(tag, s) {
		return fmt.Errorf("%w: %q", ErrDNSResolverCycle, s.Tag)
	}
	// Смена типа на fakeip обходила инвариант «evaluate не может использовать
	// fakeip-сервер»: правила проверяются только на своих мутациях. Правила ещё
	// ссылаются на СТАРЫЙ tag — renameDNSServerReferences идёт ниже.
	if s.Type == "fakeip" {
		for _, r := range c.DNS.Rules {
			if r.Action == "evaluate" && r.Server == tag {
				return fmt.Errorf("dns server %q: тип fakeip недопустим — на сервер ссылается evaluate-правило", tag)
			}
		}
	}
	c.DNS.Servers[idx] = s
	if tag != s.Tag {
		c.renameDNSServerReferences(tag, s.Tag)
	}
	return nil
}

func (c *RouterConfig) renameDNSServerReferences(oldTag, newTag string) {
	for i := range c.DNS.Rules {
		if c.DNS.Rules[i].Server == oldTag {
			c.DNS.Rules[i].Server = newTag
		}
	}
	for i := range c.DNS.Servers {
		if c.DNS.Servers[i].DomainResolver != nil && c.DNS.Servers[i].DomainResolver.Server == oldTag {
			c.DNS.Servers[i].DomainResolver.Server = newTag
		}
	}
	if c.DNS.Final == oldTag {
		c.DNS.Final = newTag
	}
}

func (c *RouterConfig) DeleteDNSServer(tag string, force bool) error {
	refs := c.dnsServerReferences(tag)
	if len(refs) > 0 && !force {
		return fmt.Errorf("%w: %q referenced by %s", ErrDNSServerReferenced, tag, strings.Join(refs, ", "))
	}
	filtered := make([]DNSServer, 0, len(c.DNS.Servers))
	for _, s := range c.DNS.Servers {
		if s.Tag != tag {
			filtered = append(filtered, s)
		}
	}
	c.DNS.Servers = filtered
	if force {
		rules := make([]DNSRule, 0, len(c.DNS.Rules))
		for _, r := range c.DNS.Rules {
			if r.Server == tag {
				continue
			}
			rules = append(rules, r)
		}
		// Каскад: снос правил сервера может осиротить потребителей убитых
		// evaluate (match_response по тегу, bare respond) — вычищаем их до
		// фикс-пойнта, потому что каскад транзитивен: удалённый потребитель
		// сам мог быть evaluate для правил ниже. Инвариант на выходе —
		// validateDNSChain(c.DNS.Rules) == nil, чтобы битая цепочка не
		// сохранилась и не всплыла невнятным FATAL'ом на apply.
		for {
			i, err := firstDNSChainViolation(rules)
			if err == nil {
				break
			}
			rules = slices.Delete(rules, i, i+1)
		}
		c.DNS.Rules = rules
		for i := range c.DNS.Servers {
			if c.DNS.Servers[i].DomainResolver != nil && c.DNS.Servers[i].DomainResolver.Server == tag {
				c.DNS.Servers[i].DomainResolver = nil
			}
		}
		if c.DNS.Final == tag {
			c.DNS.Final = ""
		}
	}
	return nil
}

func (c *RouterConfig) dnsServerReferences(tag string) []string {
	var refs []string
	for i, r := range c.DNS.Rules {
		if r.Server == tag {
			refs = append(refs, fmt.Sprintf("rule[%d]", i))
		}
	}
	for _, s := range c.DNS.Servers {
		if s.Tag == tag {
			continue
		}
		if s.DomainResolver != nil && s.DomainResolver.Server == tag {
			refs = append(refs, fmt.Sprintf("server[%s].domain_resolver", s.Tag))
		}
	}
	if c.DNS.Final == tag {
		refs = append(refs, "final")
	}
	return refs
}

// dnsChainTagReserved — пользовательское правило не может носить тег
// awgm-dns-* и не может ссылаться на него: этим префиксом помечены managed-
// правила DNS-пресета, и самозванца снёс бы ближайший ensureDNSChainOverlay.
func dnsChainTagReserved(r DNSRule) error {
	if isManagedDNSChainRule(r) {
		return ErrDNSChainTagReserved
	}
	return nil
}

// dnsChainRuleManaged — правило по индексу принадлежит оверлею пресета и
// потому не редактируется/не удаляется/не двигается пользователем: ближайший
// ensureDNSChainOverlay всё равно перезапишет цепочку.
func (c *RouterConfig) dnsChainRuleManaged(index int) error {
	if isManagedDNSChainRule(c.DNS.Rules[index]) {
		return ErrDNSRuleManaged
	}
	return nil
}

func (c *RouterConfig) AddDNSRule(r DNSRule) error {
	if err := dnsChainTagReserved(r); err != nil {
		return err
	}
	if err := validateDNSRule(r, c.dnsServerTypes()); err != nil {
		return err
	}
	candidate := append(slices.Clone(c.DNS.Rules), r)
	if err := validateDNSChain(candidate); err != nil {
		return err
	}
	c.DNS.Rules = candidate
	return nil
}

func (c *RouterConfig) UpdateDNSRule(index int, r DNSRule) error {
	if index < 0 || index >= len(c.DNS.Rules) {
		return ErrDNSRuleIndexOutOfRange
	}
	if err := c.dnsChainRuleManaged(index); err != nil {
		return err
	}
	if err := dnsChainTagReserved(r); err != nil {
		return err
	}
	if err := validateDNSRule(r, c.dnsServerTypes()); err != nil {
		return err
	}
	candidate := slices.Clone(c.DNS.Rules)
	candidate[index] = r
	if err := validateDNSChain(candidate); err != nil {
		return err
	}
	c.DNS.Rules = candidate
	return nil
}

func (c *RouterConfig) DeleteDNSRule(index int) error {
	if index < 0 || index >= len(c.DNS.Rules) {
		return ErrDNSRuleIndexOutOfRange
	}
	if err := c.dnsChainRuleManaged(index); err != nil {
		return err
	}
	candidate := slices.Delete(slices.Clone(c.DNS.Rules), index, index+1)
	if err := validateDNSChain(candidate); err != nil {
		return err
	}
	c.DNS.Rules = candidate
	return nil
}

func (c *RouterConfig) MoveDNSRule(from, to int) error {
	n := len(c.DNS.Rules)
	if from < 0 || from >= n || to < 0 || to >= n {
		return ErrDNSRuleIndexOutOfRange
	}
	if from == to {
		return nil
	}
	// Только from: перенос ПОЛЬЗОВАТЕЛЬСКОГО правила через managed-хвост
	// легален — ensureDNSChainOverlay нормализует цепочку обратно в конец.
	if err := c.dnsChainRuleManaged(from); err != nil {
		return err
	}
	r := c.DNS.Rules[from]
	without := slices.Delete(slices.Clone(c.DNS.Rules), from, from+1)
	rules := make([]DNSRule, 0, n)
	rules = append(rules, without[:to]...)
	rules = append(rules, r)
	rules = append(rules, without[to:]...)
	if err := validateDNSChain(rules); err != nil {
		return err
	}
	c.DNS.Rules = rules
	return nil
}

// MoveDNSServer reorders the DNS server at index `from` to index `to`.
// ponytail: server order is cosmetic — sing-box references servers by tag;
// endpoint exists only for UX-consistent reordering.
func (c *RouterConfig) MoveDNSServer(from, to int) error {
	n := len(c.DNS.Servers)
	if from < 0 || from >= n || to < 0 || to >= n {
		return ErrDNSServerIndexOutOfRange
	}
	if from == to {
		return nil
	}
	s := c.DNS.Servers[from]
	without := append(c.DNS.Servers[:from:from], c.DNS.Servers[from+1:]...)
	servers := make([]DNSServer, 0, n)
	servers = append(servers, without[:to]...)
	servers = append(servers, s)
	servers = append(servers, without[to:]...)
	c.DNS.Servers = servers
	return nil
}

func (c *RouterConfig) SetDNSGlobals(final, strategy string) error {
	if final != "" {
		if _, ok := c.dnsServerTypes()[final]; !ok {
			return fmt.Errorf("%w: final %q", ErrDNSServerNotFound, final)
		}
	}
	if !validDNSStrategies[strategy] {
		return fmt.Errorf("dns: unknown strategy %q", strategy)
	}
	c.DNS.Final = final
	c.DNS.Strategy = strategy
	return nil
}
