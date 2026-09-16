package router

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

func (s *ServiceImpl) computeIssues(cfg *RouterConfig) []Issue {
	var issues []Issue
	outboundTags := make(map[string]struct{})
	for _, o := range cfg.Outbounds {
		outboundTags[o.Tag] = struct{}{}
	}
	// AWG-direct outbounds live in 15-awg.json owned by awgoutbounds —
	// add them to the validation set so rules referencing awg-{id} tags
	// don't get flagged as orphans.
	if s.deps.AWGTags != nil {
		if awgTags, err := s.deps.AWGTags.ListTags(context.Background()); err == nil {
			for _, t := range awgTags {
				outboundTags[t.Tag] = struct{}{}
			}
		}
	}
	// Sing-box tunnels live in 10-tunnels.json owned by internal/singbox.
	// Their tags (e.g. "veesp" for a VLESS outbound) are valid route
	// targets but invisible to a router-only view of cfg.Outbounds.
	if s.deps.SingboxTunnels != nil {
		if tags, err := s.deps.SingboxTunnels.ListTunnelTags(context.Background()); err == nil {
			for _, tag := range tags {
				outboundTags[tag] = struct{}{}
			}
		}
	}
	// Subscription composites live in 40-subscriptions.json owned by subscription slot.
	// Their tags are valid route targets but invisible to a router-only view of cfg.Outbounds.
	if s.deps.SubscriptionComposites != nil {
		for _, o := range s.deps.SubscriptionComposites.ListSubscriptionComposites() {
			if o.Tag != "" {
				outboundTags[o.Tag] = struct{}{}
			}
		}
	}
	for i, r := range cfg.Route.Rules {
		issues = append(issues, s.computeRuleOutboundIssues(r, i, outboundTags)...)
	}
	if cfg.Route.Final != "" && !isKnownOutboundRef(cfg.Route.Final, outboundTags) {
		issues = append(issues, Issue{
			Severity: "warning",
			Kind:     "orphan-outbound",
			Tag:      cfg.Route.Final,
			Message:  fmt.Sprintf("route.final ссылается на несуществующий outbound %q", cfg.Route.Final),
		})
	}
	for i, o := range cfg.Outbounds {
		for _, member := range o.Outbounds {
			if !isKnownOutboundRef(member, outboundTags) {
				issues = append(issues, Issue{
					Severity: "warning",
					Kind:     "orphan-outbound",
					Tag:      member,
					Message:  fmt.Sprintf("outbound %q содержит несуществующий member %q", o.Tag, member),
				})
			}
		}
		if o.Default != "" && !isKnownOutboundRef(o.Default, outboundTags) {
			issues = append(issues, Issue{
				Severity:  "warning",
				Kind:      "orphan-outbound",
				RuleIndex: i,
				Tag:       o.Default,
				Message:   fmt.Sprintf("outbound %q использует несуществующий default %q", o.Tag, o.Default),
			})
		}
	}
	for _, srv := range cfg.DNS.Servers {
		if srv.Detour != "" && !isKnownOutboundRef(srv.Detour, outboundTags) {
			issues = append(issues, Issue{
				Severity: "warning",
				Kind:     "orphan-outbound",
				Tag:      srv.Detour,
				Message:  fmt.Sprintf("DNS server %q использует несуществующий detour %q", srv.Tag, srv.Detour),
			})
		}
	}
	for _, rs := range cfg.Route.RuleSet {
		// download_detour — хранимая форма; HTTPClient.Detour — то же поле в
		// материализованном слоте (sing-box 1.14, applyHTTPClients). cfg сюда
		// приходит через loadRouterConfig без restoreHTTPClients, так что
		// материализованный слот несёт только второе.
		detour := rs.DownloadDetour
		if detour == "" && rs.HTTPClient != nil {
			detour = rs.HTTPClient.Detour
		}
		if detour != "" && !isKnownOutboundRef(detour, outboundTags) {
			issues = append(issues, Issue{
				Severity: "warning",
				Kind:     "orphan-outbound",
				Tag:      detour,
				Message:  fmt.Sprintf("rule_set %q использует несуществующий download_detour %q", rs.Tag, detour),
			})
		}
	}
	for _, hc := range cfg.HTTPClients {
		if hc.Detour != "" && !isKnownOutboundRef(hc.Detour, outboundTags) {
			issues = append(issues, Issue{
				Severity: "warning",
				Kind:     "orphan-outbound",
				Tag:      hc.Detour,
				Message:  fmt.Sprintf("http_clients %q использует несуществующий detour %q", hc.Tag, hc.Detour),
			})
		}
	}

	ruleSetTags := make(map[string]struct{}, len(cfg.Route.RuleSet))
	for _, rs := range cfg.Route.RuleSet {
		ruleSetTags[rs.Tag] = struct{}{}
	}
	for i, r := range cfg.Route.Rules {
		issues = append(issues, computeRuleSetIssuesInRouteRule(r, i, ruleSetTags)...)
	}
	for i, r := range cfg.DNS.Rules {
		for _, tag := range r.RuleSet {
			if _, ok := ruleSetTags[tag]; !ok {
				issues = append(issues, Issue{
					Severity:  "warning",
					Kind:      "orphan-rule-set",
					RuleIndex: i,
					Tag:       tag,
					Message:   fmt.Sprintf("DNS-правило ссылается на несуществующий rule_set %q", tag),
				})
			}
		}
	}
	issues = append(issues, computeDNSDialIssues(cfg)...)
	issues = append(issues, computeDNSChainIssues(cfg)...)
	issues = append(issues, computeDNSRuleSetClientMatchIssues(cfg)...)
	return issues
}

// computeDNSRuleSetClientMatchIssues предупреждает про inline-набор с
// подсетью, из которой может прийти сам клиент, прицепленный к DNS-правилу.
//
// applyDNSRuleSetMatchSource переводит ip_cidr внутри такого набора на матч по
// адресу источника — и подсеть, накрывающая клиента, начинает матчить ВСЕ его
// DNS-запросы, а не только домены набора. Проверено на стенде: набор
// {domain_suffix: t.me} OR {ip_cidr: 127.0.0.0/8} с клиента 127.0.0.1 ловит и
// example.com.
//
// Только inline: их правила лежат у нас в конфиге и читаются даром. Что внутри
// remote/local .srs — известно лишь после decompile (минуты на MIPS), на такую
// цену проверка не тянет.
func computeDNSRuleSetClientMatchIssues(cfg *RouterConfig) []Issue {
	clientMatching := make(map[string]struct{})
	for _, rs := range cfg.Route.RuleSet {
		if rs.Type == "inline" && inlineRuleSetMatchesClientRange(rs) {
			clientMatching[rs.Tag] = struct{}{}
		}
	}
	if len(clientMatching) == 0 {
		return nil
	}
	var issues []Issue
	for i, r := range cfg.DNS.Rules {
		if r.MatchResponse.IsEnabled() {
			continue
		}
		for _, tag := range r.RuleSet {
			if _, ok := clientMatching[tag]; !ok {
				continue
			}
			issues = append(issues, Issue{
				Severity:  "warning",
				Kind:      "dns-rule-set-client-match",
				RuleIndex: i,
				Tag:       tag,
				Message: fmt.Sprintf("DNS-правило использует набор %q с подсетью локальной сети: "+
					"его ip_cidr сравнивается с адресом клиента, и правило поймает все запросы "+
					"из этой подсети. В DNS-правиле IP-часть набора не работает — уберите набор "+
					"из правила или вынесите домены в отдельный набор", tag),
			})
		}
	}
	return issues
}

// clientReachableRanges — диапазоны, из которых может прийти DNS-запрос от
// устройства сети: приватные сети, loopback, link-local и CGNAT.
var clientReachableRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fe80::/10"),
}

// inlineRuleSetMatchesClientRange сообщает, может ли ip_cidr inline-набора
// накрыть адрес самого клиента. Считаем ПЕРЕСЕЧЕНИЕ диапазонов, а не
// принадлежность базового адреса: 0.0.0.0/0 и 128.0.0.0/1 накрывают домашние
// сети, хотя их базовый адрес приватным не выглядит. Нераспознанный префикс
// пропускаем — валидацию содержимого делает компиляция набора, не этот обход.
func inlineRuleSetMatchesClientRange(rs RuleSet) bool {
	for _, rule := range rs.Rules {
		for _, cidr := range ruleMapIPCIDRs(rule) {
			p, err := netip.ParsePrefix(cidr)
			if err != nil {
				continue
			}
			for _, reachable := range clientReachableRanges {
				if p.Overlaps(reachable) {
					return true
				}
			}
		}
	}
	return false
}

// ruleMapIPCIDRs достаёт ip_cidr из правила набора во всех трёх формах, в
// которых оно у нас бывает: []any после JSON-декода, []string из
// datLinesToRuleSetRules и скаляр, который принимает сам sing-box. Зеркалит
// разбор в fakeip_cidr_routes.go.
func ruleMapIPCIDRs(rule map[string]any) []string {
	switch arr := rule["ip_cidr"].(type) {
	case []any:
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return arr
	case string:
		return []string{arr}
	}
	return nil
}

func (s *ServiceImpl) computeRuleOutboundIssues(r Rule, index int, outboundTags map[string]struct{}) []Issue {
	var issues []Issue
	if r.ActionIsRoute() && r.Outbound != "" && !isKnownOutboundRef(r.Outbound, outboundTags) {
		issues = append(issues, Issue{
			Severity:  "warning",
			Kind:      "orphan-rule",
			RuleIndex: index,
			Tag:       r.Outbound,
			Message:   fmt.Sprintf("правило ссылается на несуществующий outbound %q", r.Outbound),
		})
	}
	for _, nested := range r.Rules {
		issues = append(issues, s.computeRuleOutboundIssues(nested, index, outboundTags)...)
	}
	return issues
}

func isKnownOutboundRef(tag string, outboundTags map[string]struct{}) bool {
	if tag == "direct" || tag == "block" || tag == "dns" {
		return true
	}
	_, ok := outboundTags[tag]
	return ok
}

func computeRuleSetIssuesInRouteRule(r Rule, index int, ruleSetTags map[string]struct{}) []Issue {
	var issues []Issue
	for _, tag := range r.RuleSet {
		if _, ok := ruleSetTags[tag]; !ok {
			issues = append(issues, Issue{
				Severity:  "warning",
				Kind:      "orphan-rule-set",
				RuleIndex: index,
				Tag:       tag,
				Message:   fmt.Sprintf("правило ссылается на несуществующий rule_set %q", tag),
			})
		}
	}
	for _, nested := range r.Rules {
		issues = append(issues, computeRuleSetIssuesInRouteRule(nested, index, ruleSetTags)...)
	}
	return issues
}

func (s *ServiceImpl) ListPolicies(ctx context.Context) ([]PolicyInfo, error) {
	if s.deps.Policies == nil {
		return nil, fmt.Errorf("access policy provider not configured")
	}
	return s.deps.Policies.ListPolicies(ctx)
}

func (s *ServiceImpl) CreatePolicy(ctx context.Context, description string) (PolicyInfo, error) {
	if s.deps.Policies == nil {
		return PolicyInfo{}, fmt.Errorf("access policy provider not configured")
	}
	if description == "" {
		description = "awgm-router"
	}
	return s.deps.Policies.CreatePolicy(ctx, description)
}

func (s *ServiceImpl) ListPolicyDevices(ctx context.Context, policyName string) ([]PolicyDevice, error) {
	if s.deps.Policies == nil {
		return nil, fmt.Errorf("access policy provider not configured")
	}
	if policyName == "" {
		return nil, fmt.Errorf("policy name required")
	}
	return s.deps.Policies.ListDevicesForPolicy(ctx, policyName)
}

func (s *ServiceImpl) BindDevice(ctx context.Context, mac, policyName string) error {
	if s.deps.Policies == nil {
		return fmt.Errorf("access policy provider not configured")
	}
	if mac == "" || policyName == "" {
		return fmt.Errorf("mac and policyName required")
	}
	return s.deps.Policies.AssignDevice(ctx, mac, policyName)
}

func (s *ServiceImpl) UnbindDevice(ctx context.Context, mac string) error {
	if s.deps.Policies == nil {
		return fmt.Errorf("access policy provider not configured")
	}
	if mac == "" {
		return fmt.Errorf("mac required")
	}
	return s.deps.Policies.UnassignDevice(ctx, mac)
}

// inspectSlotConfig returns the EFFECTIVE config the inspector must walk —
// the slot that is live under the CURRENT routing mode — plus that slot for
// draft reporting. Issue #488: the inspector always walked the tproxy slot
// (20-router.json), so in fakeip-tun mode it explained decisions by DNS/route
// rules and rule-set names sing-box wasn't even running; the live rules were
// in the fakeip slot (21-fakeip.json).
func (s *ServiceImpl) inspectSlotConfig() (*RouterConfig, orchestrator.Slot, error) {
	mode := ""
	if s.deps.Settings != nil {
		settings, err := s.deps.Settings.Load()
		if err != nil {
			// Fail loud, not wrong: silently falling back to the tproxy slot
			// on a transient settings read error would reproduce the very
			// #488 bug (inspector explains decisions by the parked slot).
			return nil, orchestrator.SlotRouter, fmt.Errorf("inspector: load settings: %w", err)
		}
		if settings != nil {
			mode = settings.SingboxRouter.RoutingMode
		}
	}
	slot := orchestrator.SlotRouter
	if mode == "fakeip-tun" {
		slot = orchestrator.SlotFakeIP
	}
	cfg, err := s.loadRouterConfigForMode(mode)
	if err != nil {
		return nil, slot, err
	}
	if cfg == nil {
		cfg = NewEmptyConfig()
	}
	return cfg, slot, nil
}

// Inspect simulates which router rule would match the given input
// (a domain or an IP). The matcher walk is purely Go; only rule_set
// matchers shell out to `sing-box rule-set match` to consult the
// binary or downloaded JSON list. Reads the persisted config of the
// ACTIVE routing mode (tproxy or fakeip-tun slot) so the result reflects
// what the user would observe at runtime.
//
// When the sing-box binary is unavailable (dev machine, fresh install
// before the user has installed the package) rule_set matchers degrade
// to no-match and a Note is appended to the result — the rest of the
// inspector still works.
func (s *ServiceImpl) Inspect(ctx context.Context, input InspectInput) (InspectResult, error) {
	cfg, _, err := s.inspectSlotConfig()
	if err != nil {
		return InspectResult{}, err
	}
	final := cfg.Route.Final
	if final == "" {
		final = "direct"
	}
	// Как и в InspectDNS: правила — в восстановленном виде (теги без -srs,
	// как в UI и в остальных блоках трейса), карта наборов — с алиасами
	// материализованных inline'ов. Алиасы считаются до restoreConfig.
	m := s.ruleSetMaterializer()
	ruleSets := m.inspectRuleSetsWithInlineAliases(cfg)
	rules := m.restoreConfig(cfg).Route.Rules
	binary := ""
	if s.deps.Singbox != nil {
		binary = s.deps.Singbox.Binary()
	}
	s.inspectCacheOnce.Do(func() {
		s.inspectCache = newRuleSetCache("")
	})
	return Inspect(input, rules, ruleSets, final, binary, s.inspectCache), nil
}

// InspectDNS simulates which DNS rule would match the given domain and how
// the resolved DNS server classifies the resolution (fakeip → tunnel /
// real → upstream / local → router). It is the DNS-resolution branch that
// precedes the route inspector: a domain that gets a fakeip is then routed
// by Inspect. The matcher walk is purely Go; only rule_set matchers shell
// out to `sing-box rule-set match`. Reads the persisted config of the
// ACTIVE routing mode (tproxy or fakeip-tun slot).
func (s *ServiceImpl) InspectDNS(ctx context.Context, input InspectDNSInput) (InspectDNSResult, error) {
	cfg, _, err := s.inspectSlotConfig()
	if err != nil {
		return InspectDNSResult{}, err
	}
	m := s.ruleSetMaterializer()
	// DNS-правила — в восстановленном виде (ссылки на inline-теги без
	// -srs, как их видит пользователь в UI); карта наборов поэтому
	// дополняется алиасами материализованных inline'ов, иначе поиск
	// по восстановленному тегу давал «не определён в rule_set[]» (#506).
	// Алиасы считаются ДО restoreConfig: восстановление переписывает
	// ссылки правил через общие backing-массивы (out := *cfg), и «сырого»
	// вида ссылок после него уже нет.
	ruleSets := m.inspectRuleSetsWithInlineAliases(cfg)
	dnsRules := m.restoreConfig(cfg).DNS.Rules
	binary := ""
	if s.deps.Singbox != nil {
		binary = s.deps.Singbox.Binary()
	}
	s.inspectCacheOnce.Do(func() {
		s.inspectCache = newRuleSetCache("")
	})
	return InspectDNS(input, dnsRules, cfg.DNS.Servers, ruleSets, cfg.DNS.Final, binary, s.inspectCache), nil
}

func (s *ServiceImpl) InspectStream(ctx context.Context, input InspectInput) (<-chan InspectStreamEvent, error) {
	ch := make(chan InspectStreamEvent, 32)
	go func() {
		defer close(ch)
		emitEvent := func(ev InspectStreamEvent) bool {
			select {
			case <-ctx.Done():
				return false
			case ch <- ev:
				return true
			}
		}
		if !emitEvent(InspectStreamEvent{Type: "progress", Progress: &InspectProgress{Phase: "start", Message: "Запускаем инспектор маршрутов…"}}) {
			return
		}
		if !emitEvent(InspectStreamEvent{Type: "progress", Progress: &InspectProgress{Phase: "load_config", Message: "Загружаем конфигурацию маршрутизации…"}}) {
			return
		}
		cfg, slot, err := s.inspectSlotConfig()
		if err != nil {
			emitEvent(InspectStreamEvent{Type: "inspect-error", Error: err.Error()})
			return
		}
		final := cfg.Route.Final
		if final == "" {
			final = "direct"
		}
		usingDraft := false
		if s.deps.Orch != nil {
			usingDraft = s.deps.Orch.DraftInfo(slot).HasDraft
		}
		// Восстановленные правила + алиасы — как в Inspect/InspectDNS,
		// чтобы теги в прогрессе/условиях совпадали с UI (без -srs).
		m := s.ruleSetMaterializer()
		ruleSets := m.inspectRuleSetsWithInlineAliases(cfg)
		rules := m.restoreConfig(cfg).Route.Rules
		if !emitEvent(InspectStreamEvent{Type: "progress", Progress: &InspectProgress{
			Phase:        "config_loaded",
			Message:      fmt.Sprintf("Конфигурация загружена: %d правил, %d rule_set, final: %s", len(rules), len(cfg.Route.RuleSet), final),
			RuleTotal:    intPtr(len(rules)),
			RuleSetTotal: intPtr(len(cfg.Route.RuleSet)),
			Final:        final,
			UsingDraft:   usingDraft,
		}}) {
			return
		}
		binary := ""
		if s.deps.Singbox != nil {
			binary = s.deps.Singbox.Binary()
		}
		s.inspectCacheOnce.Do(func() {
			s.inspectCache = newRuleSetCache("")
		})
		res := InspectWithProgress(input, rules, ruleSets, final, binary, s.inspectCache, func(p InspectProgress) {
			select {
			case <-ctx.Done():
				return
			case ch <- InspectStreamEvent{Type: "progress", Progress: &p}:
			}
		})
		select {
		case <-ctx.Done():
			return
		case ch <- InspectStreamEvent{Type: "result", Result: &res}:
		}
	}()
	return ch, nil
}
