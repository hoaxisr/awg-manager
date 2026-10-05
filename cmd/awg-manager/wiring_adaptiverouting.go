package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/adaptiverouting"
	"github.com/hoaxisr/awg-manager/internal/api"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

type adaptiveTunnelAdapter struct {
	app *app
}

func (a *adaptiveTunnelAdapter) ListTunnels() []adaptiverouting.TunnelInfo {
	if a.app == nil {
		return nil
	}
	var out []adaptiverouting.TunnelInfo
	seenIDs := make(map[string]bool)

	if a.app.catalog != nil {
		for _, e := range a.app.catalog.ListAll(context.Background()) {
			seenIDs[e.ID] = true
			if e.Iface != "" {
				seenIDs[e.Iface] = true
			}
			iface := e.Iface
			if k, ok := a.app.catalog.GetKernelIface(context.Background(), e.ID); ok && k != "" {
				iface = k
			} else if a.app.ndmsQueries != nil && a.app.ndmsQueries.Interfaces != nil && !strings.HasPrefix(e.ID, "wan:") {
				if k := a.app.ndmsQueries.Interfaces.ResolveSystemName(context.Background(), e.Iface); k != "" && k != e.Iface {
					iface = k
				}
			}
			if strings.HasPrefix(iface, "Wireguard") {
				iface = "nwg" + strings.TrimPrefix(iface, "Wireguard")
			} else if strings.HasPrefix(iface, "OpkgTun") {
				iface = strings.ToLower(iface)
			}
			out = append(out, adaptiverouting.TunnelInfo{
				ID:        e.ID,
				Name:      e.Name,
				Interface: iface,
				Active:    e.Available && e.Status != "disabled" && e.Status != "stopped",
				Kind:      e.Type,
			})
		}
	}

	if a.app.tunnelService != nil {
		if tunnels, err := a.app.tunnelService.List(context.Background()); err == nil {
			for _, t := range tunnels {
				if seenIDs[t.ID] || (t.InterfaceName != "" && seenIDs[t.InterfaceName]) {
					continue
				}
				seenIDs[t.ID] = true
				iface := t.InterfaceName
				if strings.HasPrefix(iface, "Wireguard") {
					iface = "nwg" + strings.TrimPrefix(iface, "Wireguard")
				} else if strings.HasPrefix(iface, "OpkgTun") {
					iface = strings.ToLower(iface)
				}
				out = append(out, adaptiverouting.TunnelInfo{
					ID:        t.ID,
					Name:      t.Name,
					Interface: iface,
					Active:    t.State == tunnel.StateRunning,
					Kind:      string(t.Backend),
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

type singboxAdaptiveAdapter struct {
	app *app
}

func (s *singboxAdaptiveAdapter) ListSubscriptions() []adaptiverouting.SingboxSubscriptionInfo {
	if s == nil || s.app == nil || s.app.subSvc == nil {
		return nil
	}
	var out []adaptiverouting.SingboxSubscriptionInfo
	for _, sub := range s.app.subSvc.List() {
		cnt := len(sub.MemberTags)
		if len(sub.Members) > cnt {
			cnt = len(sub.Members)
		}
		tag := sub.SelectorTag
		if tag == "" {
			tag = "sub-" + sub.ID
		}
		out = append(out, adaptiverouting.SingboxSubscriptionInfo{
			ID:          sub.ID,
			Label:       sub.Label,
			Tag:         tag,
			Enabled:     sub.Enabled,
			MemberCount: cnt,
			Mode:        string(sub.Mode),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label)
	})
	return out
}

func (s *singboxAdaptiveAdapter) ListOutbounds() []adaptiverouting.SingboxOutboundInfo {
	if s == nil || s.app == nil {
		return nil
	}
	var out []adaptiverouting.SingboxOutboundInfo
	if s.app.subSvc != nil {
		for _, sub := range s.app.subSvc.List() {
			if !sub.Enabled {
				continue
			}
			for _, m := range sub.Members {
				name := m.Label
				if name == "" {
					name = m.Tag
				}
				out = append(out, adaptiverouting.SingboxOutboundInfo{
					Tag:       m.Tag,
					Type:      m.Protocol,
					Interface: "awgsus0",
					Enabled:   true,
					Label:     fmt.Sprintf("%s · %s", sub.Label, name),
				})
			}
		}
	}
	if s.app.singboxOp != nil {
		if tunnels, err := s.app.singboxOp.ListTunnels(context.Background()); err == nil {
			for _, t := range tunnels {
				if t.Tag == "" {
					continue
				}
				iface := t.KernelInterface
				if iface == "" {
					iface = "awgsus0"
				}
				out = append(out, adaptiverouting.SingboxOutboundInfo{
					Tag:       t.Tag,
					Type:      t.Protocol,
					Interface: iface,
					Enabled:   true,
					Label:     fmt.Sprintf("Sing-box · %s", t.Tag),
				})
			}
		}
	}
	if s.app.awg3Svc != nil {
		for _, t := range s.app.awg3Svc.ListTags() {
			if t.Tag == "" {
				continue
			}
			out = append(out, adaptiverouting.SingboxOutboundInfo{
				Tag:       t.Tag,
				Type:      "amneziawg",
				Interface: "awgsus0",
				Enabled:   true,
				Label:     fmt.Sprintf("AWG3 · %s", t.Tag),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label)
	})
	return out
}

func (a *app) setupAdaptiveRouting() {
	store, err := adaptiverouting.NewStore(a.dataDir)
	if err != nil {
		panic(fmt.Sprintf("initialize Susanin store: %v", err))
	}
	a.adaptiveRoutingStore = store

	tunnelAdapter := &adaptiveTunnelAdapter{app: a}
	sbAdapter := &singboxAdaptiveAdapter{app: a}
	catalog := adaptiverouting.NewCatalog(nil, tunnelAdapter, sbAdapter)
	refChecker := adaptiverouting.NewReferenceChecker(store)

	arch := a.routerArch
	if arch == "" {
		arch = detectArch()
	}
	svc := adaptiverouting.NewService(a.dataDir, catalog, refChecker, store, arch)
	lanIfaces := []string{storage.DefaultInterface}
	if a.settings != nil && len(a.settings.Server.Interfaces) > 0 {
		lanIfaces = a.settings.Server.Interfaces
	}
	svc.SetLanInterfaces(lanIfaces)

	if a.ndmsTransportClient != nil {
		markStore := ndmsquery.NewPolicyMarkStore(a.ndmsTransportClient, nil)
		svc.SetPolicyMarkResolver(markStore)
	}

	systemExec := adaptiverouting.NewSystemExecutor()
	mihomoExec := adaptiverouting.NewMihomoExecutor(nil)
	singboxExec := adaptiverouting.NewSingboxExecutor(a.sbOrch)
	svc.SetExecutors(systemExec, mihomoExec, singboxExec)
	a.adaptiveRoutingMihomoExec = mihomoExec

	handler := api.NewAdaptiveRoutingHandler(svc)
	a.adaptiveRoutingSvc = svc
	a.adaptiveRoutingHandler = handler
}
