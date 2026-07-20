package main

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/singbox"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

// operatorLifecycle adapts *singbox.Operator to installer.Lifecycle so
// the installer can stop/start the daemon during migration without the
// installer package taking a circular dependency on singbox.
type operatorLifecycle struct {
	op *singbox.Operator
}

func (l *operatorLifecycle) Stop(ctx context.Context) error {
	return l.op.Control(ctx, "stop")
}

func (l *operatorLifecycle) Start(ctx context.Context) error {
	return l.op.Control(ctx, "start")
}

// singboxAndSubLister satisfies singbox.tunnelLister by combining the regular
// sing-box tunnel list with the active outbound tags of enabled subscriptions.
// This lets DelayChecker probe subscription active members with the same
// periodic clash latency test it runs for regular sing-box tunnels.
type singboxAndSubLister struct {
	op   *singbox.Operator
	sub  *subscription.Service
	awg3 awg3TagLister
}

func (l *singboxAndSubLister) ListTunnels(ctx context.Context) ([]singbox.TunnelInfo, error) {
	return l.op.ListTunnels(ctx)
}

func (l *singboxAndSubLister) ListSubActiveTags() []string {
	var out []string
	if l.sub != nil {
		out = l.sub.ListActiveMemberTags()
	}
	if l.awg3 != nil {
		for _, t := range l.awg3.ListTags() {
			out = append(out, t.Tag)
		}
	}
	return out
}

// orchValidatorAdapter bridges singbox.Validator (no context) to the
// singboxorch.DraftValidator interface (with context).
type orchValidatorAdapter struct {
	v *singbox.Validator
}

func (a *orchValidatorAdapter) Validate(ctx context.Context, configDir string) error {
	return a.v.Validate(configDir)
}

// subProxySet adapts the subscription store to singbox.SubscriptionProxySet,
// exposing each subscription's allocated NDMS composite proxy (index/port/label)
// so the NDMS-proxy migration and orphan cleanup manage them. Сводные группы
// (#372) включены в тот же набор — иначе orphan cleanup реап их ProxyN.
type subProxySet struct {
	store  *subscription.Store
	groups *subscription.GroupStore
}

func (a subProxySet) SubscriptionProxies() []singbox.SubscriptionProxy {
	if a.store == nil {
		return nil
	}
	var out []singbox.SubscriptionProxy
	for _, sub := range a.store.List() {
		if sub.ProxyIndex < 0 || sub.ListenPort == 0 {
			continue
		}
		out = append(out, singbox.SubscriptionProxy{
			Index: sub.ProxyIndex,
			Port:  int(sub.ListenPort),
			Label: sub.Label,
		})
	}
	if a.groups != nil {
		for _, g := range a.groups.List() {
			if g.ProxyIndex < 0 || g.ListenPort == 0 {
				continue
			}
			out = append(out, singbox.SubscriptionProxy{
				Index: g.ProxyIndex,
				Port:  int(g.ListenPort),
				Label: g.Label,
			})
		}
	}
	return out
}

// dnsRewriteOrchAdapter adapts *singboxorch.Orchestrator to the
// dnsrewrite.Orchestrator interface (which uses plain string so the
// package stays decoupled from singboxorch.Slot).
type dnsRewriteOrchAdapter struct {
	orch *singboxorch.Orchestrator
}

func (a *dnsRewriteOrchAdapter) Save(slot string, data []byte) error {
	return a.orch.Save(singboxorch.Slot(slot), data)
}

func (a *dnsRewriteOrchAdapter) SetEnabled(slot string, on bool) error {
	return a.orch.SetEnabled(singboxorch.Slot(slot), on)
}
