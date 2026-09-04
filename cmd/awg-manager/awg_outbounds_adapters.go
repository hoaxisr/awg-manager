// cmd/awg-manager/awg_outbounds_adapters.go
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/awg3endpoint"
	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/monitoring"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// settingsManagedServersAdapter satisfies awgoutbounds.ManagedServersQuery
// by reading from storage.SettingsStore. main.go wires it into Deps so
// the awgoutbounds package stays free of storage imports.
type settingsManagedServersAdapter struct {
	store *storage.SettingsStore
}

func newSettingsManagedServersAdapter(s *storage.SettingsStore) *settingsManagedServersAdapter {
	return &settingsManagedServersAdapter{store: s}
}

func (a *settingsManagedServersAdapter) ManagedServerInterfaceNames(ctx context.Context) []string {
	if a.store == nil {
		return nil
	}
	servers := a.store.GetManagedServers()
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		if s.InterfaceName != "" {
			out = append(out, s.InterfaceName)
		}
	}
	return out
}

// awgStoreAdapter wraps storage.AWGTunnelStore for awgoutbounds.
// Resolves each tunnel's kernel iface using the same convention
// the deviceproxy adapter uses (NativeWG → nwg<NWGIndex>, Kernel →
// tunnel.NewNames(ID).IfaceName).
type awgStoreAdapter struct {
	store *storage.AWGTunnelStore
}

func newAWGStoreAdapter(s *storage.AWGTunnelStore) *awgStoreAdapter {
	return &awgStoreAdapter{store: s}
}

func (a *awgStoreAdapter) List(ctx context.Context) ([]awgoutbounds.AWGTunnelInfo, error) {
	if a.store == nil {
		return nil, nil
	}
	tuns, err := a.store.List()
	if err != nil {
		return nil, fmt.Errorf("list awg tunnels: %w", err)
	}
	out := make([]awgoutbounds.AWGTunnelInfo, 0, len(tuns))
	for _, t := range tuns {
		t := t
		out = append(out, awgoutbounds.AWGTunnelInfo{
			ID:           t.ID,
			Name:         t.Name,
			BackendIface: awgKernelIface(&t),
			DNS:          t.Interface.DNS,
		})
	}
	return out, nil
}

// awgKernelIface returns the kernel-level interface name for an AWG
// tunnel — the value sing-box's bind_interface needs to actually
// route through the tunnel.
//
// Two backends, two formulas:
//   - NativeWG (Keenetic): nwg<NWGIndex> via nwg.NewNWGNames
//   - Kernel (Entware userspace AWG bridged via NDMS OpkgTun on OS5,
//     or direct on OS4): tunnel.NewNames(t.ID).IfaceName, which yields
//     opkgtunN on OS5 and awgmN on OS4 (per operator_os5.go:445 and
//     internal/tunnel/types.go:213).
//
// Note: deviceproxy.awgKernelIface returns t.ID directly for the kernel
// branch — that yields awgN on OS5, which is the userspace-AWG TUN name
// rather than the bridged NDMS interface. Both can show up in
// /sys/class/net so existing deviceproxy bind_interface values often
// happened to work, but tunnel.NewNames(...).IfaceName is the canonical
// kernel-iface formula and matches what operator_os5.go consults. Once
// Task 9 removes deviceproxy's local awgKernelIface and routes through
// awgoutbounds.ListTags, this function becomes the single source of truth.
func awgKernelIface(t *storage.AWGTunnel) string {
	if t.Backend == "nativewg" {
		return nwg.NewNWGNames(t.NWGIndex).IfaceName
	}
	return tunnel.NewNames(t.ID).IfaceName
}

// systemTunnelStoreAdapter projects deviceproxy.SystemTunnel into
// awgoutbounds.SystemTunnelInfo. main.go owns this projection so
// neither downstream package imports the other's types.
type systemTunnelStoreAdapter struct {
	src deviceproxy.SystemTunnelQuery
}

func newSystemTunnelStoreAdapter(src deviceproxy.SystemTunnelQuery) *systemTunnelStoreAdapter {
	return &systemTunnelStoreAdapter{src: src}
}

func (a *systemTunnelStoreAdapter) List(ctx context.Context) ([]awgoutbounds.SystemTunnelInfo, error) {
	if a.src == nil {
		return nil, nil
	}
	tuns, err := a.src.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]awgoutbounds.SystemTunnelInfo, 0, len(tuns))
	for _, t := range tuns {
		out = append(out, awgoutbounds.SystemTunnelInfo{
			ID:            t.ID,
			InterfaceName: t.InterfaceName,
			Description:   t.Description,
		})
	}
	return out, nil
}

// awgoutboundsSingboxAdapter exposes singbox.Operator as the
// awgoutbounds.SingboxController contract.
type awgoutboundsSingboxAdapter struct {
	op *singbox.Operator
}

func newAwgoutboundsSingboxAdapter(op *singbox.Operator) *awgoutboundsSingboxAdapter {
	return &awgoutboundsSingboxAdapter{op: op}
}

func (a *awgoutboundsSingboxAdapter) ConfigDir() string { return a.op.ConfigDir() }
func (a *awgoutboundsSingboxAdapter) Reload() error {
	return a.op.Process().Reload()
}

// awg3TagLister exposes AWG3 endpoint tags for merging into outbound
// catalogs. Satisfied by *awg3endpoint.Service.
type awg3TagLister interface {
	ListTags() []awg3endpoint.TagInfo
}

// awg3MergedAWGOutbounds wraps the awgoutbounds catalog and appends AWG3
// endpoint tags. It feeds api.NewAWGOutboundsHandler — the source of the
// router rule-editor outbound dropdown — so AWG3 endpoints are selectable
// there. Injected only at this handler adapter, NOT in
// awgoutbounds.ServiceImpl, so the deviceproxy adapter (a separate
// consumer) does not gain AWG3 tags (they lack a kernel iface for
// bind_interface — out of scope for v1).
// inner needs only ListTags, so it is typed to the narrow
// api.AWGOutboundsService rather than the full awgoutbounds.Service.
type awg3MergedAWGOutbounds struct {
	inner api.AWGOutboundsService
	awg3  awg3TagLister
}

func (a *awg3MergedAWGOutbounds) ListTags(ctx context.Context) ([]awgoutbounds.TagInfo, error) {
	tags, err := a.inner.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	if a.awg3 != nil {
		for _, t := range a.awg3.ListTags() {
			tags = append(tags, awgoutbounds.TagInfo{
				Tag: t.Tag, Label: t.Tag, Kind: "awg3", Iface: "",
			})
		}
	}
	return tags, nil
}

// deviceproxyAWGOutboundsAdapter projects awgoutbounds.TagInfo into
// deviceproxy.AWGTagInfo. main.go owns this projection so neither
// downstream package imports the other's types.
type deviceproxyAWGOutboundsAdapter struct {
	src awgoutbounds.Service
}

func (a *deviceproxyAWGOutboundsAdapter) ListTags(ctx context.Context) ([]deviceproxy.AWGTagInfo, error) {
	tags, err := a.src.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]deviceproxy.AWGTagInfo, 0, len(tags))
	for _, t := range tags {
		out = append(out, deviceproxy.AWGTagInfo{
			Tag: t.Tag, Label: t.Label, Iface: t.Iface,
		})
	}
	return out, nil
}

// routerAWGTagAdapter projects awgoutbounds.TagInfo into
// router.AWGTag. main.go owns this projection so router doesn't
// import awgoutbounds types.
type routerAWGTagAdapter struct {
	src  awgoutbounds.Service
	awg3 awg3TagLister
}

func (a *routerAWGTagAdapter) ListTags(ctx context.Context) ([]router.AWGTag, error) {
	tags, err := a.src.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]router.AWGTag, 0, len(tags))
	for _, t := range tags {
		out = append(out, router.AWGTag{Tag: t.Tag})
	}
	if a.awg3 != nil {
		for _, t := range a.awg3.ListTags() {
			out = append(out, router.AWGTag{Tag: t.Tag})
		}
	}
	return out, nil
}

// routerSingboxTunnelAdapter projects sing-box tunnels (10-tunnels.json)
// into the simple []string the router needs for cross-slot outbound
// validation. Mirrors routerAWGTagAdapter so router stays decoupled
// from internal/singbox types.
type routerSingboxTunnelAdapter struct {
	src *singbox.Operator
}

func (a *routerSingboxTunnelAdapter) ListTunnelTags(ctx context.Context) ([]string, error) {
	tunnels, err := a.src.ListTunnels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tunnels))
	for _, t := range tunnels {
		if t.Tag != "" {
			out = append(out, t.Tag)
		}
	}
	return out, nil
}

// subscriptionBindValidator bridges router bindable-interface validation
// into the subscription service and sing-box tunnel endpoints (#709).
// It validates against the unfiltered list of bindable interfaces so
// interfaces with direct outbounds remain usable by subscriptions and tunnels.
type subscriptionBindValidator struct {
	adapter *routerWANInterfaceAdapter
}

func (v subscriptionBindValidator) ValidateBindInterface(ctx context.Context, name string) error {
	if v.adapter == nil {
		return nil
	}
	ifaces, err := v.adapter.ListAllBindable(ctx)
	if err != nil {
		return err
	}
	for _, i := range ifaces {
		if i.Name == name {
			return nil
		}
	}
	return fmt.Errorf("bind_interface %q is not a selectable interface", name)
}

// monitoringSingboxTunnelAdapter projects sing-box tunnels into the
// shape monitoring.Scheduler expects. Lives here so the monitoring
// package stays free of singbox imports.
type monitoringSingboxTunnelAdapter struct {
	op  *singbox.Operator
	sub *subscription.Service
}

func (a *monitoringSingboxTunnelAdapter) List(ctx context.Context) ([]monitoring.SingboxTunnelInfo, error) {
	tunnels, err := a.op.ListTunnels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]monitoring.SingboxTunnelInfo, 0, len(tunnels))
	seen := make(map[string]bool, len(tunnels))
	subLabelByActiveTag := make(map[string]string)
	subMemberByTag := make(map[string]subscription.MemberInfo)
	type tunnelMeta struct {
		protocol  string
		security  string
		transport string
	}
	metaByTag := make(map[string]tunnelMeta, len(tunnels))
	for _, t := range tunnels {
		// Keep config-backed sing-box rows probeable by interface.
		// Runtime-only subscription member tags are appended separately below.
		if t.Tag == "" || t.KernelInterface == "" {
			continue
		}
		seen[t.Tag] = true
		metaByTag[t.Tag] = tunnelMeta{
			protocol:  strings.TrimSpace(t.Protocol),
			security:  strings.TrimSpace(t.Security),
			transport: strings.TrimSpace(t.Transport),
		}
		out = append(out, monitoring.SingboxTunnelInfo{
			Tag:           t.Tag,
			Name:          t.Tag, // sing-box TunnelInfo doesn't carry a separate Name field
			InterfaceName: t.KernelInterface,
			Protocol:      strings.TrimSpace(t.Protocol),
			Security:      strings.TrimSpace(t.Security),
			Transport:     strings.TrimSpace(t.Transport),
		})
	}
	if a.sub != nil {
		for _, sub := range a.sub.List() {
			if !sub.Enabled {
				continue
			}
			label := strings.TrimSpace(sub.Label)
			if label == "" {
				label = strings.TrimSpace(sub.SelectorTag)
			}
			if label == "" {
				label = strings.TrimSpace(sub.ActiveMember)
			}
			if sub.ActiveMember != "" {
				subLabelByActiveTag[sub.ActiveMember] = label
			}
			for _, tag := range sub.MemberTags {
				tag = strings.TrimSpace(tag)
				if tag == "" {
					continue
				}
				if _, exists := subLabelByActiveTag[tag]; !exists {
					subLabelByActiveTag[tag] = label
				}
			}
			for _, member := range sub.Members {
				tag := strings.TrimSpace(member.Tag)
				if tag == "" {
					continue
				}
				subMemberByTag[tag] = member
				if _, exists := subLabelByActiveTag[tag]; !exists {
					subLabelByActiveTag[tag] = label
				}
			}
		}
	}
	// Add active member tags from enabled subscriptions. These outbounds are
	// often "runtime-only" (no dedicated inbound/listen_port), so they may not
	// have a kernel interface in config-derived TunnelInfo, but they are still
	// probeable via Clash /proxies/<tag>/delay and should appear in monitoring.
	if a.sub != nil {
		for _, tag := range a.sub.ListActiveMemberTags() {
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			name := tag
			if label := subLabelByActiveTag[tag]; label != "" {
				name = label
			}
			member := subMemberByTag[tag]
			out = append(out, monitoring.SingboxTunnelInfo{
				Tag:           tag,
				Name:          name,
				InterfaceName: "",
				Subscription:  true,
				Protocol:      strings.TrimSpace(member.Protocol),
				Security:      strings.TrimSpace(member.Security),
				Transport:     strings.TrimSpace(member.Transport),
			})
		}
	}
	if a.sub != nil {
		for i := range out {
			if _, ok := subLabelByActiveTag[out[i].Tag]; !ok {
				continue
			}
			out[i].Subscription = true
			if member, ok := subMemberByTag[out[i].Tag]; ok {
				if out[i].Protocol == "" {
					out[i].Protocol = strings.TrimSpace(member.Protocol)
				}
				if out[i].Security == "" {
					out[i].Security = strings.TrimSpace(member.Security)
				}
				if out[i].Transport == "" {
					out[i].Transport = strings.TrimSpace(member.Transport)
				}
			} else if meta, ok := metaByTag[out[i].Tag]; ok {
				if out[i].Protocol == "" {
					out[i].Protocol = meta.protocol
				}
				if out[i].Security == "" {
					out[i].Security = meta.security
				}
				if out[i].Transport == "" {
					out[i].Transport = meta.transport
				}
			}
		}
	}
	return out, nil
}

// monitoringCompositesAdapter projects router composite outbounds
// into the shape monitoring.Scheduler expects.
type monitoringCompositesAdapter struct {
	svc router.Service
}

func (a *monitoringCompositesAdapter) List(ctx context.Context) ([]monitoring.CompositeOutboundInfo, error) {
	outs, err := a.svc.ListCompositeOutbounds(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]monitoring.CompositeOutboundInfo, 0, len(outs))
	for _, o := range outs {
		result = append(result, monitoring.CompositeOutboundInfo{
			Tag:     o.Tag,
			Type:    o.Type,
			Members: o.Outbounds, // router.Outbound's member-tags slice
		})
	}
	return result, nil
}
