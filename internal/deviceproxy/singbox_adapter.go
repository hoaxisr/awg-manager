package deviceproxy

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// SingboxAdapter bridges deviceproxy.Service (which speaks
// ExternalSpec) to singbox.Operator (which speaks DeviceProxySpec).
// Keeping the adapter here — rather than inside internal/singbox —
// preserves the one-way dependency: singbox knows nothing about
// deviceproxy, deviceproxy depends on singbox.
//
// Production wiring also injects the orchestrator: ApplyDeviceProxy
// writes its standalone fragment (30-deviceproxy.json) through
// SlotDeviceProxy and toggles the slot via SetEnabled. Прежний фолбэк без
// оркестратора (device-proxy, встроенный в 10-tunnels.json) снят — без
// оркестратора адаптер отказывает явно.
type SingboxAdapter struct {
	op   *singbox.Operator
	orch *orchestrator.Orchestrator
}

func NewSingboxAdapter(op *singbox.Operator) *SingboxAdapter {
	return &SingboxAdapter{op: op}
}

// SetOrch wires the config.d orchestrator after construction. The
// orchestrator is built post-Operator (it needs Operator.Process) so
// we can't supply it in the constructor without a cycle.
func (a *SingboxAdapter) SetOrch(orch *orchestrator.Orchestrator) {
	a.orch = orch
}

// ApplyDeviceProxy persists the device-proxy slot via the orchestrator.
func (a *SingboxAdapter) ApplyDeviceProxy(ctx context.Context, spec ExternalSpec) error {
	if a.orch != nil {
		sbSpec := toSingboxSpec(spec)
		data, err := singbox.BuildDeviceProxyConfig(sbSpec)
		if err != nil {
			return fmt.Errorf("build deviceproxy config: %w", err)
		}
		if err := a.orch.Save(orchestrator.SlotDeviceProxy, data); err != nil {
			return err
		}
		if err := a.orch.SetEnabled(orchestrator.SlotDeviceProxy, sbSpec.Enabled); err != nil {
			return err
		}
		return nil
	}

	// Легаси-путь (device-proxy, встроенный в 10-tunnels.json) снят вместе с
	// эпохой orch == nil: в проде SetOrch зовётся до старта HTTP. Явная
	// ошибка здесь, а не отказ где-то в глубине про «tunnels config».
	return fmt.Errorf("apply deviceproxy: orchestrator not wired")
}

// ApplyDeviceProxyNoReload is the no-SIGHUP twin of ApplyDeviceProxy.
// Used by Service.SaveConfig when the diff is SelectedOutbound-only,
// so writing the new selector.default does not disturb the live
// selector.now that a hot-switch may have set.
//
// On the orchestrator path this maps to SaveSilent — the slot file is
// updated but no reload is scheduled. The slot's enabled flag is also
// pushed through SetEnabled in case the spec.Enabled has changed; that
// IS allowed to schedule a reload (toggling enabled is by definition
// a config-content change, not a no-op selector-default tweak).
func (a *SingboxAdapter) ApplyDeviceProxyNoReload(ctx context.Context, spec ExternalSpec) error {
	if a.orch == nil {
		// Прежде здесь был легаси-путь через Operator.ApplyConfigNoReload. Он
		// был недостижим в проде (SetOrch зовётся до старта HTTP) и при этом
		// неработоспособен: делал os.Rename файла поверх КАТАЛОГА config.d.
		return fmt.Errorf("apply deviceproxy (no-reload): orchestrator not wired")
	}
	sbSpec := toSingboxSpec(spec)
	data, err := singbox.BuildDeviceProxyConfig(sbSpec)
	if err != nil {
		return fmt.Errorf("build deviceproxy config: %w", err)
	}
	if err := a.orch.SaveSilent(orchestrator.SlotDeviceProxy, data); err != nil {
		return err
	}
	// SetEnabled is a no-op when the desired state already matches
	// (no rename, no reload arming). Safe to call unconditionally.
	return a.orch.SetEnabled(orchestrator.SlotDeviceProxy, sbSpec.Enabled)
}

// ApplyDeviceProxyInstances persists multiple device-proxy instances via
// the orchestrator.
func (a *SingboxAdapter) ApplyDeviceProxyInstances(ctx context.Context, specs []ExternalInstanceSpec) error {
	if a.orch != nil {
		sbSpecs := make([]singbox.DeviceProxyInstanceSpec, 0, len(specs))
		for _, spec := range specs {
			sbSpecs = append(sbSpecs, toSingboxInstanceSpec(spec))
		}

		data, err := singbox.BuildDeviceProxyInstancesConfig(sbSpecs)
		if err != nil {
			return fmt.Errorf("build deviceproxy instances config: %w", err)
		}

		if err := a.orch.Save(orchestrator.SlotDeviceProxy, data); err != nil {
			return err
		}

		enabled := false
		for _, spec := range specs {
			if spec.Enabled {
				enabled = true
				break
			}
		}

		if err := a.orch.SetEnabled(orchestrator.SlotDeviceProxy, enabled); err != nil {
			return err
		}
		return nil
	}

	// Легаси-путь снят — см. ApplyDeviceProxy.
	return fmt.Errorf("apply deviceproxy instances: orchestrator not wired")
}

// AvailableOutboundTags returns the outbound tags declared by ENABLED
// orchestrator slots other than the device-proxy slot itself. NB: это
// НЕ в точности множество pruneDanglingSelectorRefsLocked — prune строит
// его без exclude (включая слот 30); здесь слот 30 исключён намеренно,
// потому что его селекторы — потребители, а не кандидаты в члены.
// Service.buildSpec uses it to keep
// generated selectors free of dangling refs when a source slot is
// parked (router engine off → 20-router.json composites gone, issue
// #465). Returns nil when the orchestrator is not wired (legacy /
// tests) — nil means "unknown", the caller keeps legacy behaviour.
func (a *SingboxAdapter) AvailableOutboundTags() map[string]bool {
	if a.orch == nil {
		return nil
	}
	return a.orch.EnabledOutboundTags(orchestrator.SlotDeviceProxy)
}

// GetSelectorActive returns the currently-active member of the named
// selector. Thin pass-through — see singbox.Operator for the contract.
func (a *SingboxAdapter) GetSelectorActive(ctx context.Context, selectorTag string) (string, error) {
	return a.op.GetSelectorActive(ctx, selectorTag)
}

func (a *SingboxAdapter) SetSelectorDefault(ctx context.Context, selectorTag, memberTag string) error {
	return a.op.SetSelectorDefault(ctx, selectorTag, memberTag)
}

func (a *SingboxAdapter) TunnelTags() []string {
	tunnels, err := a.op.ListTunnels(context.Background())
	if err != nil {
		return nil
	}
	tags := make([]string, 0, len(tunnels))
	for _, t := range tunnels {
		tags = append(tags, t.Tag)
	}
	return tags
}

// TunnelOutbounds returns standalone sing-box tunnel metadata used by
// device-proxy outbound list (second detail line in UI).
func (a *SingboxAdapter) TunnelOutbounds() []TunnelOutboundInfo {
	tunnels, err := a.op.ListTunnels(context.Background())
	if err != nil {
		return nil
	}
	out := make([]TunnelOutboundInfo, 0, len(tunnels))
	for _, t := range tunnels {
		out = append(out, TunnelOutboundInfo{
			Tag:      t.Tag,
			Protocol: t.Protocol,
			Server:   t.Server,
			Port:     t.Port,
		})
	}
	return out
}

func (a *SingboxAdapter) IsRunning() bool {
	running, _ := a.op.IsRunning()
	return running
}

func toSingboxSpec(s ExternalSpec) singbox.DeviceProxySpec {
	out := singbox.DeviceProxySpec{
		Enabled:     s.Enabled,
		ListenAddr:  s.ListenAddr,
		Port:        s.Port,
		SelectedTag: s.SelectedTag,
		SBTags:      s.SBTags,
	}
	if s.Auth.Enabled {
		out.Auth = singbox.DeviceProxyAuth{
			Enabled:  true,
			Username: s.Auth.Username,
			Password: s.Auth.Password,
		}
	}
	out.AWGTags = append([]string(nil), s.AWGTags...)
	return out
}

func toSingboxInstanceSpec(s ExternalInstanceSpec) singbox.DeviceProxyInstanceSpec {
	out := singbox.DeviceProxyInstanceSpec{
		ID:          s.ID,
		Enabled:     s.Enabled,
		ListenAddr:  s.ListenAddr,
		Port:        s.Port,
		SelectedTag: s.SelectedTag,
		SBTags:      s.SBTags,
	}
	if s.Auth.Enabled {
		out.Auth = singbox.DeviceProxyAuth{
			Enabled:  true,
			Username: s.Auth.Username,
			Password: s.Auth.Password,
		}
	}
	out.AWGTags = append([]string(nil), s.AWGTags...)
	return out
}
