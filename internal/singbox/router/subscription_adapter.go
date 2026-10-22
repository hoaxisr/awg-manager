package router

import (
	"encoding/json"
)

// SubscriptionOutboundSource is the narrow contract router needs from
// the subscription package to surface its composite outbounds in the
// router's Outbounds list. Implemented by *subscription.OperatorAdapter
// via a thin wrapper to avoid the router package importing subscription.
type SubscriptionOutboundSource interface {
	SubscriptionOutbounds() []map[string]any
}

// SubscriptionCompositesAdapter wraps a SubscriptionOutboundSource and
// projects its raw outbound maps into typed router.Outbound values,
// keeping only composite types (selector/urltest/loadbalance) — plain
// proxy outbounds (vless/trojan/etc.) are members, not composites, and
// belong elsewhere in the routing UI.
type SubscriptionCompositesAdapter struct {
	src SubscriptionOutboundSource
}

func NewSubscriptionCompositesAdapter(src SubscriptionOutboundSource) *SubscriptionCompositesAdapter {
	return &SubscriptionCompositesAdapter{src: src}
}

// ListSubscriptionComposites returns the subscription-managed composite
// outbounds. Conversion goes through JSON because the source stores raw
// map[string]any; entries that fail to round-trip into Outbound or that
// are not composite types are silently skipped.
func (a *SubscriptionCompositesAdapter) ListSubscriptionComposites() []Outbound {
	if a == nil || a.src == nil {
		return nil
	}
	raw := a.src.SubscriptionOutbounds()
	out := make([]Outbound, 0, len(raw))
	for _, m := range raw {
		b, err := json.Marshal(m)
		if err != nil {
			continue
		}
		var o Outbound
		if err := json.Unmarshal(b, &o); err != nil {
			continue
		}
		switch o.Type {
		case "selector", "urltest", "loadbalance", "fallback":
			out = append(out, o)
		}
	}
	return out
}
