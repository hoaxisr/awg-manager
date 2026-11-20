package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/sbconfig"
)

func cloneUplinkBinding(b *uplink.Binding) *uplink.Binding {
	if b == nil {
		return nil
	}
	cp := *b
	cp.DNSServers = append([]string(nil), b.DNSServers...)
	return &cp
}

func releaseUplinkDialFields(ob map[string]any) map[string]any {
	copy := make(map[string]any, len(ob))
	for key, value := range ob {
		copy[key] = value
	}
	if resolver, ok := copy["domain_resolver"].(map[string]any); ok {
		if resolver["server"] == sbconfig.DNSTag(stringTag(copy["tag"])) {
			delete(copy, "domain_resolver")
		}
	}
	delete(copy, "inet4_bind_address")
	delete(copy, "inet6_bind_address")
	return copy
}

func stringTag(value any) string { s, _ := value.(string); return s }

// SetUplinkBindingsFn installs a read-only desired-state snapshot supplier.
// The callback must not acquire Service locks: flush already owns adapter.mu.
func (a *OperatorAdapter) SetUplinkBindingsFn(fn func() []sbconfig.Spec) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.uplinkBindingsFn = fn
}

func (s *Service) UplinkBindings() []sbconfig.Spec {
	if s.store == nil {
		return nil
	}
	var specs []sbconfig.Spec
	for _, sub := range s.store.List() {
		if sub.Enabled && sub.UplinkBinding != nil {
			specs = append(specs, sbconfig.Spec{Tag: sub.SelectorTag, Binding: *cloneUplinkBinding(sub.UplinkBinding), Group: true})
		}
	}
	return specs
}

type UplinkAdapter struct {
	Service      *Service
	Operator     *singbox.Operator
	Active       func() bool
	DNSForDevice uplink.DNSForDevice
}

func (a *UplinkAdapter) read(ctx context.Context, t uplink.Target) (*Subscription, map[string]any, error) {
	if a.Service == nil || a.Operator == nil || a.Active == nil || !a.Active() {
		return nil, nil, fmt.Errorf("sing-box subscription engine is inactive")
	}
	if running, _ := a.Operator.IsRunning(); !running {
		return nil, nil, singbox.ErrSingboxNotRunning
	}
	sub, err := a.Service.Get(t.Ref)
	if err != nil {
		return nil, nil, err
	}
	if !sub.Enabled {
		return sub, nil, nil
	}
	if a.DNSForDevice == nil {
		return nil, nil, fmt.Errorf("%w: no provider DNS catalogue", uplink.ErrUnsupported)
	}
	if sub.ListenPort == 0 {
		return nil, nil, fmt.Errorf("%w: dedicated subscription inbound required", uplink.ErrUnsupported)
	}
	port, err := singbox.VerifyUplinkRoute(a.Operator.ConfigDir(), sub.SelectorTag)
	if err != nil {
		return nil, nil, err
	}
	if port != int(sub.ListenPort) {
		return nil, nil, fmt.Errorf("subscription inbound port differs from runtime configuration")
	}
	b, err := os.ReadFile(filepath.Join(a.Operator.ConfigDir(), SlotSubscriptionsMeta.Filename))
	if err != nil {
		return nil, nil, err
	}
	var doc map[string]any
	if err = json.Unmarshal(b, &doc); err != nil {
		return nil, nil, err
	}
	// Validate the entire graph without choosing a WAN or doing DNS I/O.
	_, err = sbconfig.Apply(doc, []sbconfig.Spec{{Tag: sub.SelectorTag, Group: true, Binding: uplink.Binding{Device: "validate0", IPv4: "192.0.2.2", IPv6: "2001:db8::2", DNSServers: []string{"192.0.2.53"}}}})
	if err != nil {
		return nil, nil, err
	}
	return sub, doc, nil
}

func (a *UplinkAdapter) Validate(ctx context.Context, t uplink.Target) error {
	_, _, err := a.read(ctx, t)
	return err
}
func (a *UplinkAdapter) Observe(ctx context.Context, t uplink.Target) (uplink.Observation, error) {
	sub, doc, err := a.read(ctx, t)
	if err != nil {
		return uplink.Observation{}, err
	}
	if sub.Enabled && sub.UplinkBinding != nil {
		if err := sbconfig.Verify(doc, sub.SelectorTag, sub.BindInterface, true); err != nil {
			return uplink.Observation{}, fmt.Errorf("%w: managed subscription dial fields changed", uplink.ErrConflict)
		}
	}
	refresh := sub.UplinkBinding != nil && uplink.BindingNeedsRefresh(*sub.UplinkBinding)
	return uplink.Observation{Device: sub.BindInterface, Enabled: sub.Enabled, NeedsRebind: refresh}, nil
}
func (a *UplinkAdapter) Activate(ctx context.Context, t uplink.Target, expected, device string) error {
	sub, _, err := a.read(ctx, t)
	if err != nil {
		return err
	}
	if !sub.Enabled {
		return fmt.Errorf("subscription is disabled")
	}
	b, err := uplink.InspectBinding(device)
	if err != nil {
		return fmt.Errorf("%w: %w", uplink.ErrPathUnavailable, err)
	}
	b.DNSServers, err = a.DNSForDevice(ctx, device)
	if err != nil {
		return fmt.Errorf("%w: %w", uplink.ErrPathUnavailable, err)
	}
	binding := &b
	if _, err = a.Service.update(ctx, t.Ref, UpdatePatch{BindInterface: &device, ExpectedBind: &expected, UplinkBinding: &binding}); err != nil {
		return err
	}
	return a.Operator.RestartForUplink(ctx)
}
func (a *UplinkAdapter) Probe(ctx context.Context, t uplink.Target, device string) error {
	mu := a.Service.lockSub(t.Ref)
	mu.Lock()
	defer mu.Unlock()
	sub, doc, err := a.read(ctx, t)
	if err != nil {
		return err
	}
	if !sub.Enabled || sub.BindInterface != device || sub.UplinkBinding == nil {
		return fmt.Errorf("strict subscription binding is absent or changed")
	}
	if err = sbconfig.Verify(doc, sub.SelectorTag, device, true); err != nil {
		return err
	}
	if uplink.BindingNeedsRefresh(*sub.UplinkBinding) {
		return fmt.Errorf("provider source address changed during probe")
	}
	return a.Operator.ProbeUplinkSOCKS(ctx, sub.ListenPort)
}

func (*UplinkAdapter) UplinkCapabilities(uplink.Target) uplink.Capabilities {
	return uplink.Capabilities{ProviderDNS: true, RestartScope: "engine", ControlledFailback: true, Limitation: "controlled return restarts the engine and verifies traffic; failure restores the previous WAN"}
}
