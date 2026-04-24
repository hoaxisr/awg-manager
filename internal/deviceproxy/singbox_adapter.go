package deviceproxy

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/singbox"
)

// SingboxAdapter bridges deviceproxy.Service (which speaks
// ExternalSpec) to singbox.Operator (which speaks DeviceProxySpec).
// Keeping the adapter here — rather than inside internal/singbox —
// preserves the one-way dependency: singbox knows nothing about
// deviceproxy, deviceproxy depends on singbox.
type SingboxAdapter struct {
	op *singbox.Operator
}

func NewSingboxAdapter(op *singbox.Operator) *SingboxAdapter {
	return &SingboxAdapter{op: op}
}

// ApplyDeviceProxy loads the current sing-box config, applies our
// inbound/outbound/rule set, then promotes via Operator.ApplyConfig.
func (a *SingboxAdapter) ApplyDeviceProxy(ctx context.Context, spec ExternalSpec) error {
	cfg, err := a.op.LoadCurrentConfig()
	if err != nil {
		return err
	}
	if err := cfg.EnsureDeviceProxy(toSingboxSpec(spec)); err != nil {
		return err
	}
	return a.op.ApplyConfig(ctx, cfg)
}

// ApplyDeviceProxyNoReload is the no-SIGHUP twin of ApplyDeviceProxy.
// Used by Service.SaveConfig when the diff is SelectedOutbound-only,
// so writing the new selector.default to config.json does not disturb
// the live selector.now that a hot-switch may have set.
func (a *SingboxAdapter) ApplyDeviceProxyNoReload(ctx context.Context, spec ExternalSpec) error {
	cfg, err := a.op.LoadCurrentConfig()
	if err != nil {
		return err
	}
	if err := cfg.EnsureDeviceProxy(toSingboxSpec(spec)); err != nil {
		return err
	}
	return a.op.ApplyConfigNoReload(ctx, cfg)
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

func (a *SingboxAdapter) IsRunning() bool {
	running, _ := a.op.IsRunningPublic()
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
	for _, a := range s.AWGTargets {
		out.AWGTargets = append(out.AWGTargets, singbox.DeviceProxyAWG{
			TunnelID:    a.TunnelID,
			KernelIface: a.KernelIface,
		})
	}
	return out
}
