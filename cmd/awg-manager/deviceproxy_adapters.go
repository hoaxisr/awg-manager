package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

// deviceproxySubscriptionOutboundsAdapter adapts *subscription.Service to
// the deviceproxy.SubscriptionOutboundsCatalog interface, exposing enabled
// subscription selector/urltest outbounds as device-proxy targets without
// creating a direct dependency from the subscription package on deviceproxy.
type deviceproxySubscriptionOutboundsAdapter struct {
	src *subscription.Service
}

func (a *deviceproxySubscriptionOutboundsAdapter) ListDeviceProxyOutbounds() []deviceproxy.SubscriptionOutboundInfo {
	if a == nil || a.src == nil {
		return nil
	}
	subs := a.src.List()
	out := make([]deviceproxy.SubscriptionOutboundInfo, 0, len(subs))

	for _, sub := range subs {
		if !sub.Enabled || sub.SelectorTag == "" || len(sub.MemberTags) == 0 {
			continue
		}
		label := strings.TrimSpace(sub.Label)
		if label == "" {
			label = sub.ID
		}
		active := sub.ActiveMember
		if active == "" && len(sub.MemberTags) > 0 {
			active = sub.MemberTags[0]
		}
		detail := active
		for _, m := range sub.Members {
			if m.Tag != active {
				continue
			}
			parts := []string{}
			if strings.TrimSpace(m.Label) != "" {
				parts = append(parts, strings.TrimSpace(m.Label))
			}
			if m.Protocol != "" {
				parts = append(parts, strings.ToUpper(m.Protocol))
			}
			if m.Server != "" {
				parts = append(parts, fmt.Sprintf("%s:%d", m.Server, m.Port))
			}
			if len(parts) > 0 {
				detail = strings.Join(parts, " · ")
			}
			break
		}
		out = append(out, deviceproxy.SubscriptionOutboundInfo{
			Tag:    sub.SelectorTag,
			Label:  label,
			Detail: detail,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Label < out[j].Label
	})
	return out
}

// deviceproxyRouterOutboundsAdapter adapts *router.ServiceImpl to the
// deviceproxy.RouterOutboundsCatalog interface, exposing router-defined
// outbounds (21-routing.json) as device-proxy targets. Only Source=="router"
// entries are returned (subscription composites are surfaced separately via
// SubscriptionOutboundsCatalog). Directs that stripAutoManagedDirect removes
// from the effective config are hidden — they are not selectable.
type deviceproxyRouterOutboundsAdapter struct {
	src *router.ServiceImpl
}

func (a *deviceproxyRouterOutboundsAdapter) ListDeviceProxyRouterOutbounds() []deviceproxy.RouterOutboundInfo {
	if a == nil || a.src == nil {
		return nil
	}
	views, err := a.src.ListCompositeOutbounds(context.Background())
	if err != nil {
		return nil
	}
	out := make([]deviceproxy.RouterOutboundInfo, 0, len(views))
	for _, v := range views {
		if v.Source != "router" {
			continue
		}
		o := v.Outbound
		if o.Type == "direct" && o.BindInterface != "" && router.IsStrippedDirectBind(o.BindInterface) {
			continue // не попадёт в эффективный конфиг → невыбираемо
		}
		detail := ""
		if o.Type == "direct" {
			if o.BindInterface != "" {
				detail = "direct · " + o.BindInterface
			} else {
				detail = "direct"
			}
		} else {
			detail = fmt.Sprintf("%s · %d", o.Type, len(o.Outbounds))
		}
		out = append(out, deviceproxy.RouterOutboundInfo{
			Tag:    o.Tag,
			Label:  o.Tag,
			Detail: detail,
			// Определение композита нужно device-proxy для graceful-
			// деградации: когда слот 20 припаркован (движок выключен),
			// селектор слота 30 подставляет default-член композита
			// вместо висячей ссылки на его тег (issue #465).
			DefaultMember: o.Default,
			Members:       append([]string(nil), o.Outbounds...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Label < out[j].Label
	})
	return out
}
