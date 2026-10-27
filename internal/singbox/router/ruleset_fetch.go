package router

import (
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// Служебный вход загрузки remote-наборов (F690). Нашу копию набора (инспектор,
// fakeip Tier-2) демон качает сам, а download_detour — outbound sing-box,
// которым наш HTTP-клиент пойти не может. Поэтому на диске слота живёт
// mixed-inbound на loopback с пользователем на каждый detour и правило
// «вошёл этим пользователем → outbound = detour»; демон ходит через него
// HTTP-прокси с логином = тег detour. Как и правило mDNS, вход есть только на
// диске: restoreConfig его снимает, UI и правка его не видят.
//
// Порт и тег — на слот: слоты router и fakeip взаимоисключающие, но в окне
// переключения включены оба, и общий тег/порт уронил бы проверку слотов.
// Пароль постоянный: случайный менял бы конфиг на каждом старте демона.
// Loopback-входы туннелей (config.go AddTunnel) тоже без защиты от локальных
// процессов — нового класса доступа это не открывает.
const (
	ruleSetFetchPortRouter = 51275
	ruleSetFetchPortFakeIP = 51276
	ruleSetFetchTagPrefix  = "awgm-rs-fetch-"
	ruleSetFetchPassword   = "awgm-rs-fetch"
)

func ruleSetFetchPort(slot orchestrator.Slot) int {
	if slot == orchestrator.SlotFakeIP {
		return ruleSetFetchPortFakeIP
	}
	return ruleSetFetchPortRouter
}

func ruleSetFetchTag(slot orchestrator.Slot) string {
	return ruleSetFetchTagPrefix + string(slot)
}

// ruleSetFetchDetour — outbound, которым sing-box сам качает набор, в форме
// слота на диске (applyHTTPClients): detour его http_client. "" — набор идёт
// напрямую: detour нет или он на пустой direct (клиент по ссылке rs-direct:).
func ruleSetFetchDetour(rs RuleSet) string {
	if rs.Type != "remote" || rs.HTTPClient == nil || rs.HTTPClient.Ref != "" {
		return ""
	}
	return rs.HTTPClient.Detour
}

// applyRuleSetFetch добавляет вход и правила по detour'ам наборов. Зовётся
// после applyHTTPClients; прежний вход снимается — повторная материализация
// без restore не плодит копий.
func applyRuleSetFetch(slot orchestrator.Slot, cfg *RouterConfig) {
	stripRuleSetFetch(cfg)
	var detours []string
	for _, rs := range cfg.Route.RuleSet {
		if d := ruleSetFetchDetour(rs); d != "" && !slices.Contains(detours, d) {
			detours = append(detours, d)
		}
	}
	if len(detours) == 0 {
		return
	}
	tag := ruleSetFetchTag(slot)
	in := Inbound{Type: "mixed", Tag: tag, Listen: "127.0.0.1", ListenPort: ruleSetFetchPort(slot)}
	rules := make([]Rule, 0, len(detours)+len(cfg.Route.Rules))
	for _, d := range detours {
		in.Users = append(in.Users, InboundUser{Username: d, Password: ruleSetFetchPassword})
		rules = append(rules, Rule{Inbound: []string{tag}, AuthUser: []string{d}, Action: "route", Outbound: d})
	}
	cfg.Inbounds = append(cfg.Inbounds, in)
	cfg.Route.Rules = append(rules, cfg.Route.Rules...)
}

// stripRuleSetFetch снимает вход и его правила (загрузчики слота,
// restoreConfig, повтор apply). Срезы пересобираются, только если есть что
// снять: вызывающие делят backing-массивы (out := *cfg).
func stripRuleSetFetch(cfg *RouterConfig) {
	isFetchIn := func(in Inbound) bool { return strings.HasPrefix(in.Tag, ruleSetFetchTagPrefix) }
	isFetchRule := func(r Rule) bool {
		return len(r.Inbound) == 1 && strings.HasPrefix(r.Inbound[0], ruleSetFetchTagPrefix)
	}
	if slices.ContainsFunc(cfg.Inbounds, isFetchIn) {
		cfg.Inbounds = slices.DeleteFunc(slices.Clone(cfg.Inbounds), isFetchIn)
	}
	if slices.ContainsFunc(cfg.Route.Rules, isFetchRule) {
		cfg.Route.Rules = slices.DeleteFunc(slices.Clone(cfg.Route.Rules), isFetchRule)
	}
}

// withRuleSetFetchVia помечает наборы слота прокси служебного входа — копией,
// конфиг вызывающего не трогается.
func withRuleSetFetchVia(slot orchestrator.Slot, sets []RuleSet) []RuleSet {
	out := slices.Clone(sets)
	for i := range out {
		out[i].fetchVia = ruleSetFetchVia(slot, out[i])
	}
	return out
}

func ruleSetFetchVia(slot orchestrator.Slot, rs RuleSet) string {
	d := ruleSetFetchDetour(rs)
	if d == "" {
		return ""
	}
	u := url.URL{
		Scheme: "http",
		User:   url.UserPassword(d, ruleSetFetchPassword),
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(ruleSetFetchPort(slot))),
	}
	return u.String()
}
