package subscription

import (
	"encoding/json"
	"fmt"
)

// BuildSelector emits a sing-box selector outbound JSON wrapping memberTags.
// defaultTag must be one of memberTags; if empty, the first member is used.
func BuildSelector(selectorTag string, memberTags []string, defaultTag string) json.RawMessage {
	if defaultTag == "" && len(memberTags) > 0 {
		defaultTag = memberTags[0]
	}
	out := map[string]any{
		"type":                        "selector",
		"tag":                         selectorTag,
		"outbounds":                   memberTags,
		"interrupt_exist_connections": false,
	}
	if defaultTag != "" {
		out["default"] = defaultTag
	}
	raw, _ := json.Marshal(out)
	return raw
}

// singboxDefaultIdleTimeoutSec — idle_timeout групп sing-box по умолчанию
// (C.DefaultURLTestIdleTimeout, 30m). Интервал больше него sing-box отвергает
// на старте («interval must be less or equal than idle_timeout»).
const singboxDefaultIdleTimeoutSec = 1800

// probedGroup — общая часть групп, которые sing-box ведёт сам по URL-тестам
// (urltest, fallback).
func probedGroup(typ, tag string, memberTags []string, cfg URLTestConfig) map[string]any {
	def := DefaultURLTestConfig()
	if cfg.URL == "" {
		cfg.URL = def.URL
	}
	if cfg.IntervalSec <= 0 {
		cfg.IntervalSec = def.IntervalSec
	}
	out := map[string]any{
		"type":      typ,
		"tag":       tag,
		"outbounds": memberTags,
		"url":       cfg.URL,
		"interval":  fmt.Sprintf("%ds", cfg.IntervalSec),
	}
	if cfg.IntervalSec > singboxDefaultIdleTimeoutSec {
		out["idle_timeout"] = fmt.Sprintf("%ds", cfg.IntervalSec*2)
	}
	return out
}

// BuildURLTest emits a sing-box urltest outbound JSON wrapping memberTags.
// Sing-box probes each member by HEADing url every interval and routes
// through the fastest. Tolerance is the RTT advantage (in ms) that a
// faster member must hold before sing-box switches off the current one
// — prevents oscillation between members with similar latency.
func BuildURLTest(selectorTag string, memberTags []string, cfg URLTestConfig) json.RawMessage {
	if cfg.ToleranceMs < 0 {
		cfg.ToleranceMs = DefaultURLTestConfig().ToleranceMs
	}
	out := probedGroup("urltest", selectorTag, memberTags, cfg)
	if cfg.ToleranceMs > 0 {
		out["tolerance"] = cfg.ToleranceMs
	}
	raw, _ := json.Marshal(out)
	return raw
}

// BuildFallback emits a sing-box fallback outbound JSON wrapping memberTags.
// Sing-box uses the first healthy member in strict order, falling over when dead.
func BuildFallback(selectorTag string, memberTags []string, cfg URLTestConfig) json.RawMessage {
	out := probedGroup("fallback", selectorTag, memberTags, cfg)
	out["interrupt_exist_connections"] = true
	raw, _ := json.Marshal(out)
	return raw
}

// BuildGroupOutbound dispatches selector/urltest/fallback based on
// the subscription's effective mode. Centralised so service.go has a
// single call site for both refresh- and select-time materialisation.
func BuildGroupOutbound(sub Subscription, memberTags []string, defaultTag string) json.RawMessage {
	switch sub.EffectiveMode() {
	case ModeURLTest:
		return BuildURLTest(sub.SelectorTag, memberTags, sub.EffectiveURLTest())
	case ModeFallback:
		return BuildFallback(sub.SelectorTag, memberTags, sub.EffectiveURLTest())
	default:
		return BuildSelector(sub.SelectorTag, memberTags, defaultTag)
	}
}

// BuildAggregateGroupOutbound — аналог BuildGroupOutbound для сводных групп
// (#372): диспатчит selector/urltest/fallback по EffectiveMode группы.
func BuildAggregateGroupOutbound(g AggregateGroup, memberTags []string) json.RawMessage {
	switch g.EffectiveMode() {
	case ModeURLTest:
		return BuildURLTest(g.Tag, memberTags, g.EffectiveURLTest())
	case ModeFallback:
		return BuildFallback(g.Tag, memberTags, g.EffectiveURLTest())
	default:
		return BuildSelector(g.Tag, memberTags, "")
	}
}

// BuildMixedInbound emits the SOCKS5/HTTP listener that pairs with the
// selector. NDMS bridge picks up the listener as a Proxy interface.
func BuildMixedInbound(inboundTag string, listenPort uint16) json.RawMessage {
	out := map[string]any{
		"type":        "mixed",
		"tag":         inboundTag,
		"listen":      "127.0.0.1",
		"listen_port": listenPort,
	}
	raw, _ := json.Marshal(out)
	return raw
}

// BuildRouteRule emits the inbound→outbound route entry that ties the
// mixed inbound to the selector.
func BuildRouteRule(inboundTag, selectorTag string) json.RawMessage {
	out := map[string]any{
		"inbound":  inboundTag,
		"outbound": selectorTag,
	}
	raw, _ := json.Marshal(out)
	return raw
}
