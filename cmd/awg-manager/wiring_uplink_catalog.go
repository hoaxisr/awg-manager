package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// One authenticated catalog feeds the policy editor. Unsupported backends are
// visible with a reason instead of silently disappearing or becoming writable.
func (a *app) uplinkCatalog(ctx context.Context) ([]uplink.TargetInfo, error) {
	var result []uplink.TargetInfo
	var failures []error
	activeEngine := "sing-box"
	if a.settingsStore != nil {
		if settings, err := a.settingsStore.Get(); err == nil && settings != nil && settings.SingboxRouter.RoutingEngine != "" {
			activeEngine = settings.SingboxRouter.RoutingEngine
		}
	}
	add := func(engine, ref, kind, label, reason string) {
		result = append(result, uplink.TargetInfo{Target: uplink.Target{Engine: engine, Ref: ref, Kind: kind}, Label: label, UnavailableReason: reason})
	}
	if a.singboxOp != nil {
		list, err := a.singboxOp.ListTunnels(ctx)
		if err != nil {
			failures = append(failures, fmt.Errorf("sing-box targets: %w", err))
		} else {
			for _, t := range list {
				reason := ""
				if activeEngine != "sing-box" {
					reason = "sing-box сейчас не выбран как активный движок"
				}
				add("sing-box", t.Tag, "", t.Tag+" · sing-box", reason)
			}
		}
	}
	fircActive := false
	if a.settingsStore != nil {
		if s, sErr := a.settingsStore.Get(); sErr == nil && s != nil && s.SingboxRouter.RoutingEngine == "firc" {
			fircActive = true
		}
	}
	if a.fircOp != nil && fircActive && a.fircOp.CheckNotForeign() == nil {
		list, err := a.fircOp.Client().GetTunnels(ctx)
		if err != nil {
			failures = append(failures, fmt.Errorf("AWGM Route targets: %w", err))
		} else {
			for _, t := range list {
				label := t.Description
				if label == "" {
					label = t.ID
				}
				reason := ""
				poolInbound := false
				for _, in := range t.Inbounds {
					if firc.UplinkPoolInbound(in) {
						poolInbound = true
					}
				}
				if !poolInbound {
					reason = "Локальный диагностический SOCKS-вход будет создан при сохранении"
				}
				if activeEngine != "firc" {
					reason = "AWGM Route сейчас не выбран как активный движок"
				}
				add("firc", t.ID, "", label+" · AWGM Route", reason)
				if !poolInbound {
					result[len(result)-1].SetupRequired = true
				}
			}
		}
	}
	if a.subSvc != nil {
		for _, s := range a.subSvc.List() {
			label := s.Label
			if label == "" {
				label = s.ID
			}
			reason := ""
			if activeEngine != "sing-box" {
				reason = "Подписка sing-box требует активного движка sing-box"
			}
			add("sing-box", s.ID, "subscription", label+" · подписка sing-box", reason)
		}
	}
	if a.awgStore != nil {
		list, err := a.awgStore.ListStrict()
		if err != nil {
			failures = append(failures, fmt.Errorf("AWG targets: %w", err))
		} else {
			for _, t := range list {
				if t.Backend == "nativewg" {
					reason := ""
					if a.nwgOp == nil || !a.nwgOp.SupportsNativeUplink(&t) {
						reason = "Эта native-конфигурация не поддерживается текущим адаптером"
					}
					if len(t.Peer.AllowedIPs) != 1 || t.Peer.AllowedIPs[0] != "0.0.0.0/0" {
						reason = "Пока поддержан только IPv4 AllowedIPs 0.0.0.0/0"
					}
					endpoint, err := netip.ParseAddrPort(t.Peer.Endpoint)
					if err != nil || !endpoint.Addr().Is4() {
						reason = "Нужен IPv4-адрес сервера"
					}
					if t.Interface.DNS == "" {
						reason = "Нужен явный DNS внутри туннеля"
					}
					add("wireguard", t.ID, "", t.Name+" · native WireGuard/ASC", reason)
				}
				if t.Backend == "kernel" || t.Backend == "" {
					add("kernel-amneziawg", t.ID, "", t.Name+" · kernel AmneziaWG", "Конфигурация не поддерживается адаптером: нужны OS5, прямой IPv4 endpoint, явный DNS и отсутствие relay")
				}
			}
		}
	}
	if a.ndmsQueries != nil && a.ndmsQueries.WGServers != nil {
		list, err := a.ndmsQueries.WGServers.ListSystemTunnelsFresh(ctx)
		if err != nil {
			failures = append(failures, fmt.Errorf("system WireGuard targets: %w", err))
		} else {
			for _, t := range list {
				if t.External != "" || t.Description == ndms.BuiltInVPNServerDescription {
					continue
				}
				if a.settingsStore != nil && a.settingsStore.IsServerInterface(t.ID) {
					continue
				}
				label := t.Description
				if label == "" {
					label = t.ID
				}
				add("system-wireguard", t.ID, "", label+" · system WireGuard", "Нужен клиентский WireGuard: один peer, IPv4 endpoint и явный DNS; серверные и управляемые другим сервисом интерфейсы исключены")
			}
		}
	}
	return result, errors.Join(failures...)
}
