package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

// MigrateLegacyMihomoResources executes legacy groups and rules migration in an isolated transaction.
func (s *ServiceImpl) MigrateLegacyMihomoResources(ctx context.Context) error {
	if s.deps.MihomoNativeProxies == nil {
		return nil
	}
	settings, err := s.deps.Settings.Get()
	if err != nil {
		return err
	}
	sr := settings.SingboxRouter
	if sr.RoutingEngine != "mihomo" {
		return nil
	}
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return err
	}
	var mihomoRules []mihomo.Rule
	for _, r := range cfg.Route.Rules {
		mihomoRules = append(mihomoRules, mihomo.Rule{
			DomainSuffix: r.DomainSuffix,
			Domain:       r.Domain,
			IPCIDR:       r.IPCIDR,
			SourceIPCIDR: r.SourceIPCIDR,
			RuleSet:      r.RuleSet,
			Action:       r.Action,
			Outbound:     r.Outbound,
		})
	}
	return MigrateLegacyMihomoResources(ctx, s.deps.MihomoNativeProxies, sr.ProxyGroups, mihomoRules)
}

// AssembleCompileInput builds the immutable snapshot of all inputs needed by CompileMihomoConfigFromInput.
func (s *ServiceImpl) AssembleCompileInput(ctx context.Context) (*MihomoCompileInput, error) {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	sr := settings.SingboxRouter

	tunIface, _ := s.tunModeIface()

	var subProxies []map[string]any
	subscriptionsLoaded := false
	if s.deps.Orch != nil {
		raw, loadErr := s.deps.Orch.LoadEffective(orchestrator.SlotSubscriptions)
		if loadErr != nil && !errors.Is(loadErr, orchestrator.ErrUnknownSlot) {
			return nil, fmt.Errorf("load subscription outbound slot: %w", loadErr)
		}
		if len(raw) != 0 {
			var slot struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			if unmarshalErr := json.Unmarshal(raw, &slot); unmarshalErr != nil {
				return nil, fmt.Errorf("decode subscription outbound slot: %w", unmarshalErr)
			}
			subProxies = slot.Outbounds
			subscriptionsLoaded = true
		}
	}
	if !subscriptionsLoaded && s.deps.SubscriptionComposites != nil && s.deps.SubscriptionComposites.src != nil {
		subProxies = s.deps.SubscriptionComposites.src.SubscriptionOutbounds()
	}

	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, fmt.Errorf("load router config: %w", err)
	}

	var ownOutbounds []map[string]any
	if len(cfg.Outbounds) > 0 {
		for _, ob := range cfg.Outbounds {
			if ob.Server == "127.0.0.1" || ob.Server == "localhost" {
				continue
			}
			obJSON, err := json.Marshal(ob)
			if err != nil {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(obJSON, &m); err == nil {
				ownOutbounds = append(ownOutbounds, m)
			}
		}
	}
	if s.deps.Orch != nil {
		raw, loadErr := s.deps.Orch.LoadEffective(orchestrator.SlotTunnels)
		if loadErr != nil && !errors.Is(loadErr, orchestrator.ErrUnknownSlot) {
			return nil, fmt.Errorf("load tunnel outbound slot: %w", loadErr)
		}
		if len(raw) != 0 {
			var slot struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			if err := json.Unmarshal(raw, &slot); err != nil {
				return nil, fmt.Errorf("decode tunnel outbound slot: %w", err)
			}
			ownOutbounds = append(ownOutbounds, slot.Outbounds...)
		}
	}
	awgLoaded := false
	if s.deps.Orch != nil {
		raw, loadErr := s.deps.Orch.LoadEffective(orchestrator.SlotAwg)
		if loadErr != nil && !errors.Is(loadErr, orchestrator.ErrUnknownSlot) {
			return nil, fmt.Errorf("load AWG outbound slot: %w", loadErr)
		}
		if len(raw) != 0 {
			var slot struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			if err := json.Unmarshal(raw, &slot); err != nil {
				return nil, fmt.Errorf("decode AWG outbound slot: %w", err)
			}
			ownOutbounds = append(ownOutbounds, slot.Outbounds...)
			awgLoaded = true
		}
	}
	if !awgLoaded && s.deps.AWGTags != nil {
		awgTags, listErr := s.deps.AWGTags.ListTags(ctx)
		if listErr != nil {
			return nil, fmt.Errorf("list AWG outbounds: %w", listErr)
		}
		for _, awg := range awgTags {
			if awg.Tag == "" || awg.Iface == "" {
				continue
			}
			ownOutbounds = append(ownOutbounds, map[string]any{
				"type":           "direct",
				"tag":            awg.Tag,
				"bind_interface": awg.Iface,
			})
		}
	}

	allProxies := append(subProxies, ownOutbounds...)

	var mihomoRules []mihomo.Rule
	for _, r := range cfg.Route.Rules {
		mihomoRules = append(mihomoRules, mihomo.Rule{
			DomainSuffix: r.DomainSuffix,
			Domain:       r.Domain,
			IPCIDR:       r.IPCIDR,
			SourceIPCIDR: r.SourceIPCIDR,
			RuleSet:      r.RuleSet,
			Action:       r.Action,
			Outbound:     r.Outbound,
		})
	}

	resources := mihomo.NativeResources{}
	var targetBridges []mihomo.BridgeRef
	if s.deps.MihomoNativeProxies != nil {
		for _, proxy := range s.deps.MihomoNativeProxies.ConfigProxies() {
			resources.Proxies = append(resources.Proxies, mihomo.Proxy(proxy))
		}
		resources.ProxyProviders = s.deps.MihomoNativeProxies.ConfigProviders()
		for _, rawGroup := range s.deps.MihomoNativeProxies.ConfigProviderGroups() {
			group := mihomo.ProxyGroup{}
			group.Name, _ = rawGroup["name"].(string)
			group.Type, _ = rawGroup["type"].(string)
			group.Use, _ = rawGroup["use"].([]string)
			group.Proxies, _ = rawGroup["proxies"].([]string)
			group.URL, _ = rawGroup["url"].(string)
			group.Interval, _ = rawGroup["interval"].(int)
			if lazyVal, lazyOk := rawGroup["lazy"].(bool); lazyOk {
				group.Lazy = &lazyVal
			}
			group.Strategy, _ = rawGroup["strategy"].(string)
			group.Tolerance, _ = rawGroup["tolerance"].(int)
			group.Timeout, _ = rawGroup["timeout"].(int)
			group.MaxFailedTimes, _ = rawGroup["max-failed-times"].(int)
			group.DisableUDP, _ = rawGroup["disable-udp"].(bool)
			group.IncludeAll, _ = rawGroup["include-all"].(bool)
			group.IncludeAllProxies, _ = rawGroup["include-all-proxies"].(bool)
			group.IncludeAllProviders, _ = rawGroup["include-all-providers"].(bool)
			group.Filter, _ = rawGroup["filter"].(string)
			group.ExcludeFilter, _ = rawGroup["exclude-filter"].(string)
			group.InterfaceName, _ = rawGroup["interface-name"].(string)
			group.ExcludeType, _ = rawGroup["exclude-type"].(string)
			group.ExpectedStatus, _ = rawGroup["expected-status"].(string)
			group.Hidden, _ = rawGroup["hidden"].(bool)
			group.Icon, _ = rawGroup["icon"].(string)
			resources.ProxyGroups = append(resources.ProxyGroups, group)
		}
		if settings.CreateNDMSProxyForSingbox {
			for _, bridge := range s.deps.MihomoNativeProxies.ConfigBridgeListeners() {
				resources.Listeners = append(resources.Listeners, mihomo.Listener{
					Name: bridge.Name, Type: "mixed", Port: bridge.Port,
					Listen: "127.0.0.1", Proxy: bridge.Proxy,
				})
			}
		}
		baseGroupPort := 12100
		for i, group := range resources.ProxyGroups {
			if group.Name != "" {
				resources.Listeners = append(resources.Listeners, mihomo.Listener{
					Name:   fmt.Sprintf("mihomo-group-%d", i),
					Type:   "mixed",
					Port:   baseGroupPort + i,
					Listen: "127.0.0.1",
					Proxy:  group.Name,
				})
			}
		}
		if err := s.deps.MihomoNativeProxies.ValidateRuntimeRules(); err != nil {
			return nil, fmt.Errorf("validate mihomo native rules: %w", err)
		}
		resources.GroupsAuthoritative = s.deps.MihomoNativeProxies.HasGroups()
		resources.Rules = s.deps.MihomoNativeProxies.ConfigRules()
		resources.RulesAuthoritative = s.deps.MihomoNativeProxies.HasRules()
		resources.RuleProviders = s.deps.MihomoNativeProxies.ConfigRuleProviders()

		for _, nb := range s.deps.MihomoNativeProxies.ListBridges() {
			targetBridges = append(targetBridges, mihomo.BridgeRef{
				ProxyIndex:      nb.Bridge.ProxyIndex,
				ProxyInterface:  nb.Bridge.ProxyInterface,
				KernelInterface: nb.Bridge.KernelInterface,
				ListenPort:      nb.Bridge.ListenPort,
				LegacyOwner:     nb.LegacyOwner,
				OwnerUUID:       mihomonative.BridgeOwnershipDescription(nb.Kind, nb.ID),
			})
		}
	}

	if s.deps.AdaptiveEgressProvider != nil {
		resources.AdaptiveEgress = s.deps.AdaptiveEgressProvider.AdaptiveConfig()
	}

	for _, srv := range cfg.DNS.Servers {
		sni := ""
		if srv.TLS != nil {
			sni = srv.TLS.ServerName
		}
		resources.DNSServers = append(resources.DNSServers, mihomo.DNSServerSpec{
			Tag:        srv.Tag,
			Type:       srv.Type,
			Server:     srv.Server,
			ServerPort: srv.ServerPort,
			Detour:     srv.Detour,
			SNI:        sni,
		})
	}
	for _, r := range cfg.DNS.Rules {
		resources.DNSRules = append(resources.DNSRules, mihomo.DNSRuleSpec{
			Domain:        r.Domain,
			DomainSuffix:  r.DomainSuffix,
			DomainKeyword: r.DomainKeyword,
			RuleSet:       r.RuleSet,
			Server:        r.Server,
		})
	}

	isPrimary := sr.Enabled && sr.RoutingEngine == "mihomo"
	hasNativeListeners := len(resources.Listeners) > 0
	hasAdaptiveEgress := resources.AdaptiveEgress != nil && resources.AdaptiveEgress.Enabled

	var sidecar bool
	var rMode mihomo.RuntimeMode

	if isPrimary {
		sidecar = false
		rMode = mihomo.RuntimeEnforced
	} else if hasNativeListeners || hasAdaptiveEgress {
		sidecar = true
		rMode = mihomo.RuntimePermissive
	} else {
		sidecar = false
		rMode = mihomo.RuntimeOff
	}

	vv := mihomo.SourceVersionVector{
		Revisions: make(map[mihomo.SourceID]uint64),
		Digests:   make(map[mihomo.SourceID]string),
	}

	if srBytes, err := json.Marshal(sr); err == nil {
		vv.Digests[mihomo.SourceSettings] = strictfs.ComputeBytesDigest(srBytes)
	}
	if cfgBytes, err := json.Marshal(cfg); err == nil {
		vv.Digests[mihomo.SourceRouterConfig] = strictfs.ComputeBytesDigest(cfgBytes)
	}
	if s.deps.MihomoNativeProxies != nil {
		if snapProducer, ok := s.deps.MihomoNativeProxies.(interface {
			Snapshot() (mihomonative.StoreSnapshot, error)
		}); ok {
			if snap, err := snapProducer.Snapshot(); err == nil {
				vv.Revisions[mihomo.SourceNativeResources] = snap.Revision()
				vv.Digests[mihomo.SourceNativeResources] = snap.Digest()
			}
		} else if resBytes, err := json.Marshal(resources); err == nil {
			vv.Digests[mihomo.SourceNativeResources] = strictfs.ComputeBytesDigest(resBytes)
		}
	}
	if proxiesBytes, err := json.Marshal(allProxies); err == nil {
		vv.Digests[mihomo.SourceSubscriptions] = strictfs.ComputeBytesDigest(proxiesBytes)
	}
	dynamicCIDRs := s.dynamicCloudCIDRs()
	if cidrBytes, err := json.Marshal(dynamicCIDRs); err == nil {
		vv.Digests[mihomo.SourceDynamicCloud] = strictfs.ComputeBytesDigest(cidrBytes)
	}
	vv.Digests[mihomo.SourceTunIface] = strictfs.ComputeBytesDigest([]byte(tunIface))

	return &MihomoCompileInput{
		RouterSettings:    sr,
		NativeResources:   resources,
		TunIface:          tunIface,
		SubProxies:        allProxies,
		MihomoRules:       mihomoRules,
		FinalOutbound:     cfg.Route.Final,
		DynamicCloudCIDRs: dynamicCIDRs,
		TargetBridges:     targetBridges,
		Sidecar:           sidecar,
		Mode:              rMode,
		VersionVector:     vv,
	}, nil
}

// CompileMihomoConfig gathers current inputs and executes pure compilation.
func (s *ServiceImpl) CompileMihomoConfig(ctx context.Context) (*mihomo.CompileResult, error) {
	input, err := s.AssembleCompileInput(ctx)
	if err != nil {
		return nil, err
	}
	return CompileMihomoConfigFromInput(input)
}
