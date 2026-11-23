package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/sbconfig"
	"github.com/hoaxisr/awg-manager/internal/uplink/wireguard"
)

type uplinkWANResolver struct{ model *wan.Model }

type uplinkTargetAdapter struct{ tunnel, subscription uplink.Adapter }

func (a uplinkTargetAdapter) TargetExists(ctx context.Context, t uplink.Target) (bool, error) {
	if lookup, ok := a.adapter(t).(uplink.TargetLookup); ok {
		return lookup.TargetExists(ctx, t)
	}
	return false, uplink.ErrUnsupported
}

func (a uplinkTargetAdapter) UplinkCapabilities(t uplink.Target) uplink.Capabilities {
	if p, ok := a.adapter(t).(uplink.CapabilityProvider); ok {
		return p.UplinkCapabilities(t)
	}
	return uplink.Capabilities{RestartScope: "engine", Limitation: "independent candidate probe unavailable for subscriptions"}
}
func (a uplinkTargetAdapter) ProbeCandidate(ctx context.Context, t uplink.Target, device string) error {
	if p, ok := a.adapter(t).(uplink.CandidateProber); ok {
		return p.ProbeCandidate(ctx, t, device)
	}
	return uplink.ErrUnsupported
}

func (a uplinkTargetAdapter) adapter(t uplink.Target) uplink.Adapter {
	if t.Kind == "subscription" {
		return a.subscription
	}
	return a.tunnel
}
func (a uplinkTargetAdapter) Validate(ctx context.Context, t uplink.Target) error {
	return a.adapter(t).Validate(ctx, t)
}
func (a uplinkTargetAdapter) Observe(ctx context.Context, t uplink.Target) (uplink.Observation, error) {
	return a.adapter(t).Observe(ctx, t)
}
func (a uplinkTargetAdapter) Activate(ctx context.Context, t uplink.Target, old, next string) error {
	return a.adapter(t).Activate(ctx, t, old, next)
}
func (a uplinkTargetAdapter) Probe(ctx context.Context, t uplink.Target, device string) error {
	return a.adapter(t).Probe(ctx, t, device)
}

func (r uplinkWANResolver) Providers() []uplink.Provider {
	list := r.model.ForUI()
	result := make([]uplink.Provider, 0, len(list))
	for _, v := range list {
		if v.ID != "" {
			result = append(result, uplink.Provider{ID: v.ID, Device: v.Name, Label: v.Label, Up: v.Up})
		}
	}
	return result
}

func (a *app) setupUplink() {
	store, err := openQuarantining(a, filepath.Join(a.dataDir, "uplink-policies.json"), "политики uplink начаты заново", uplink.OpenStore)
	if err != nil {
		a.bootLog.Error("uplink-init", "", err.Error())
		return
	}
	wanDNS, err := openQuarantining(a, filepath.Join(a.dataDir, "uplink-wan-dns.json"), "DNS WAN для uplink начаты заново", uplink.OpenWANDNS)
	if err != nil {
		a.bootLog.Error("uplink-dns-init", "", err.Error())
		return
	}
	active := func(engine string) bool {
		s, err := a.settingsStore.Get()
		if err != nil || s == nil {
			return false
		}
		configured := s.SingboxRouter.RoutingEngine
		if configured == "" {
			configured = "sing-box"
		}
		return configured == engine
	}
	automaticDNS := func(ctx context.Context, device string) ([]string, error) {
		providerID := ""
		for _, p := range a.wanModel.ForUI() {
			if p.Name == device {
				providerID = p.ID
				break
			}
		}
		if providerID == "" {
			return nil, fmt.Errorf("unknown WAN device %q", device)
		}
		if a.ndmsTransportClient == nil {
			return nil, fmt.Errorf("NDMS DNS catalogue is unavailable")
		}
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		raw, err := a.ndmsTransportClient.GetRaw(queryCtx, "/show/rc/ip/name-server")
		if err != nil {
			return nil, err
		}
		servers, err := uplink.ProviderDNS(raw, providerID)
		if err == nil || !errors.Is(err, uplink.ErrNoExplicitProviderDNS) {
			return servers, err
		}
		liveRaw, liveErr := a.ndmsTransportClient.GetRaw(queryCtx, "/show/ip/name-server")
		if liveErr != nil {
			return nil, err
		}
		if liveServers, lErr := uplink.ProviderRuntimeDNS(liveRaw, providerID); lErr == nil {
			return liveServers, nil
		}
		return nil, err
	}
	dnsForDevice := func(ctx context.Context, device string) ([]string, error) {
		for _, p := range a.wanModel.ForUI() {
			if p.Name == device && p.ID != "" {
				return wanDNS.Resolve(ctx, p.ID, device, automaticDNS)
			}
		}
		return nil, fmt.Errorf("unknown WAN device %q", device)
	}
	kernelAdapter := a.setupKernelUplink(store)
	native, err := a.setupNativeUplink(store)
	var nativeAdapter uplink.Adapter = native
	if err != nil {
		a.bootLog.Error("uplink-native-init", "", err.Error())
		nativeAdapter = a.unavailableNativeAdapter(err)
	}
	c := uplink.New(store, uplinkWANResolver{a.wanModel}, map[string]uplink.Adapter{
		"kernel-amneziawg": kernelAdapter,
		"system-wireguard": a.setupSystemUplink(),
		"wireguard":        nativeAdapter,
		"sing-box": uplinkTargetAdapter{
			tunnel:       &singbox.UplinkAdapter{Operator: a.singboxOp, DNSForDevice: dnsForDevice, Active: func() bool { return active("sing-box") }},
			subscription: &subscription.UplinkAdapter{Service: a.subSvc, Operator: a.singboxOp, DNSForDevice: dnsForDevice, Active: func() bool { return active("sing-box") }},
		},
		"firc": &firc.UplinkAdapter{Operator: a.fircOp, DNSForDevice: dnsForDevice, Active: func() bool { return active("firc") }},
	})
	c.TargetCatalog = a.uplinkCatalog
	c.WANDNS = wanDNS
	c.OnTransition = func(id string, s uplink.State) {
		a.bootLog.Info("uplink", id, "state="+s.Phase+" provider="+s.Provider+" reason="+s.LastError)
	}
	c.OnStateChange = func(string) {
		a.eventBus.PublishInvalidated(events.ResourceUplink, "state-changed")
	}
	a.uplinkController.Store(c)
	if a.fircOp != nil {
		a.fircOp.SetManagedUplinkConfig(func(ctx context.Context, tunnels []firc.Tunnel) ([]firc.Tunnel, error) {
			return firc.MaterializeOwnedUplinks(ctx, tunnels, store.MaterializationPolicies(), c.Providers(), dnsForDevice, a.fircOp.Client())
		})
	}
	if a.singboxOp != nil {
		a.singboxOp.SetManagedUplinkConfig(func(ctx context.Context, doc map[string]any) (map[string]any, error) {
			return sbconfig.MaterializeOwned(ctx, doc, store.MaterializationPolicies(), c.Providers(), dnsForDevice)
		})
		a.singboxOp.SetTargetRenameCoordinator(func(ctx context.Context, old, next string, apply func() error) error {
			return c.RenameTarget(ctx, uplink.Target{Engine: "sing-box", Ref: old}, uplink.Target{Engine: "sing-box", Ref: next}, apply)
		})
	}
	uplinkHandler := &api.UplinkHandler{Controller: c}
	uplinkHandler.SetEventBus(a.eventBus)
	a.srv.SetUplinkHandler(uplinkHandler)
	go func() {
		// Do not race boot restoration or report the empty pre-boot WAN model.
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for atomic.LoadInt32(&a.bootDone) == 0 {
			select {
			case <-a.shutdownCtx.Done():
				return
			case <-ticker.C:
			}
		}
		c.Run(a.shutdownCtx)
	}()
}

var _ uplink.Adapter = (*wireguard.Managed)(nil)

// openQuarantining: файл с битым JSON откладывается в <path>.corrupt-<unix>
// (данные целы для ручного разбора), и uplink стартует с пустым. Иначе
// setupUplink выходил бы, не зарегистрировав ни одного стража, и туннели во
// владении адаптеров правились бы поверх живой привязки; с пустым списком
// политик страж на правке снимает привязку. Ошибки чтения (сбой накопителя) и
// неизвестную схему (откат версии) не трогаем: временный сбой не должен стать
// потерей политик.
func openQuarantining[T any](a *app, path, lostWhat string, open func(string) (*T, error)) (*T, error) {
	v, err := open(path)
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if err == nil || !(errors.As(err, &syntaxErr) || errors.As(err, &typeErr)) {
		return v, err
	}
	moved := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
	if rerr := os.Rename(path, moved); rerr != nil {
		return nil, fmt.Errorf("%w; quarantine: %v", err, rerr)
	}
	a.bootLog.Error("uplink-init", "", fmt.Sprintf("%s не разобрался (%v), отложен в %s; %s", path, err, moved, lostWhat))
	return open(path)
}
