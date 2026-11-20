package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/wireguard"
)

// A broken native registry must fail closed without disabling unrelated proxy
// fallback policies or registering a typed nil adapter.
type unavailableNativeUplink struct {
	err     error
	applies func(string) bool
}

func (a *app) unavailableNativeAdapter(err error) unavailableNativeUplink {
	return unavailableNativeUplink{err: fmt.Errorf("%w: native uplink registry unavailable: %v", uplink.ErrConflict, err), applies: func(id string) bool {
		if a.awgStore == nil {
			return true
		}
		t, e := a.awgStore.Get(id)
		return e != nil || t.Backend == "nativewg"
	}}
}

func (a unavailableNativeUplink) UplinkCapabilities(uplink.Target) uplink.Capabilities {
	return uplink.Capabilities{IndependentEngine: true, Limitation: a.err.Error()}
}
func (a unavailableNativeUplink) UplinkTargets(context.Context) ([]uplink.TargetInfo, error) {
	return nil, a.err
}
func (a unavailableNativeUplink) Validate(context.Context, uplink.Target) error { return a.err }
func (a unavailableNativeUplink) Observe(context.Context, uplink.Target) (uplink.Observation, error) {
	return uplink.Observation{}, a.err
}
func (a unavailableNativeUplink) Activate(context.Context, uplink.Target, string, string) error {
	return a.err
}
func (a unavailableNativeUplink) Probe(context.Context, uplink.Target, string) error { return a.err }
func (a unavailableNativeUplink) ReleaseUplink(_ context.Context, t uplink.Target) error {
	if a.applies != nil && !a.applies(t.Ref) {
		return nil
	}
	return a.err
}

func (a *app) setupNativeUplink(policies *uplink.Store) (*wireguard.Managed, error) {
	var ready atomic.Pointer[wireguard.Managed]
	var failed atomic.Bool
	protected := func(id string) bool {
		for _, p := range policies.List() {
			if p.Enabled && p.Target.Engine == "wireguard" && p.Target.Ref == id {
				return true
			}
		}
		return false
	}
	nativeOrUnknown := func(id string) bool {
		if a.awgStore == nil {
			return true
		}
		t, err := a.awgStore.Get(id)
		return err != nil || t.Backend == "nativewg"
	}
	if a.tunnelService != nil {
		a.tunnelService.SetUplinkMutationGuard(func(ctx context.Context, id, operation string) (func(), error) {
			if failed.Load() && (protected(id) || nativeOrUnknown(id)) {
				return nil, fmt.Errorf("%w: native uplink initialization failed", uplink.ErrConflict)
			}
			if c := a.uplinkController.Load(); c != nil {
				return c.GuardNativeMutation(ctx, id, operation)
			}
			if protected(id) || a.kernelPolicyEnabled(policies, id) {
				return nil, fmt.Errorf("%w: native uplink controller is not ready", uplink.ErrConflict)
			}
			return func() {}, nil
		})
	}
	if a.awgStore != nil {
		a.awgStore.SetUplinkGuard(func(old, next *storage.AWGTunnel) error {
			manager := ready.Load()
			if old == nil || storage.SameUplinkTransport(old, next) {
				return nil
			}
			if protected(old.ID) || a.kernelPolicyEnabled(policies, old.ID) || a.kernelTransportOwned(old) || (manager != nil && manager.OwnsTarget(old.ID)) || (failed.Load() && old.Backend == "nativewg") {
				return fmt.Errorf("%w: relinquish native uplink before editing its transport", uplink.ErrConflict)
			}
			return nil
		})
	}
	if a.nwgOp != nil {
		a.nwgOp.SetUplinkBeforeStart(func(ctx context.Context, id string) error {
			if c := a.uplinkController.Load(); c != nil && c.BackupPaused() {
				return fmt.Errorf("%w: native uplink paused for backup", uplink.ErrConflict)
			}
			manager := ready.Load()
			if failed.Load() || (manager == nil && protected(id)) {
				return fmt.Errorf("%w: native uplink is not ready", uplink.ErrConflict)
			}
			if manager == nil {
				return nil
			}
			return manager.PrepareForStart(ctx, id)
		})
	}
	lookup := func(ctx context.Context, id string) (wireguard.ManagedTarget, error) {
		if a.awgStore == nil || a.nwgOp == nil {
			return wireguard.ManagedTarget{}, uplink.ErrUnsupported
		}
		t, err := a.awgStore.Get(id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return wireguard.ManagedTarget{}, uplink.ErrNotFound
			}
			return wireguard.ManagedTarget{}, err
		}
		if !a.nwgOp.SupportsNativeUplink(t) {
			return wireguard.ManagedTarget{}, uplink.ErrUnsupported
		}
		if len(t.Peer.AllowedIPs) != 1 || t.Peer.AllowedIPs[0] != "0.0.0.0/0" {
			return wireguard.ManagedTarget{}, uplink.ErrUnsupported
		}
		return wireguard.ManagedTarget{Ref: t.ID, Label: t.Name, Interface: nwg.NewNWGNames(t.NWGIndex).IfaceName, Peer: t.Peer.PublicKey, Endpoint: t.Peer.Endpoint, TunnelDNS: tunnel.ParseDNSList(t.Interface.DNS), Enabled: t.Enabled}, nil
	}
	deps := wireguard.ManagedDeps{Directory: a.dataDir, Lookup: lookup, ConfirmAbsent: a.storedUplinkAbsent, Providers: func() []uplink.Provider {
		if a.wanModel == nil {
			return nil
		}
		return (uplinkWANResolver{a.wanModel}).Providers()
	},
		PeerMark: func(ctx context.Context, target wireguard.ManagedTarget) (uint32, error) {
			if a.awgStore == nil || a.nwgOp == nil {
				return 0, uplink.ErrUnsupported
			}
			stored, err := a.awgStore.Get(target.Ref)
			if err != nil {
				return 0, err
			}
			if stored.Peer.PublicKey != target.Peer || stored.Peer.Endpoint != target.Endpoint || nwg.NewNWGNames(stored.NWGIndex).IfaceName != target.Interface {
				return 0, uplink.ErrConflict
			}
			return a.nwgOp.NativeUplinkPeerMark(ctx, stored)
		},
		PeerMarks: func(ctx context.Context) (map[string][]uint32, error) {
			if a.nwgOp == nil {
				return nil, uplink.ErrUnsupported
			}
			return a.nwgOp.NativeUplinkPeerMarks(ctx)
		},
		List: func(ctx context.Context) ([]wireguard.ManagedTarget, error) {
			if a.awgStore == nil {
				return nil, nil
			}
			list, err := a.awgStore.ListStrict()
			if err != nil {
				return nil, err
			}
			var targets []wireguard.ManagedTarget
			for _, t := range list {
				v, e := lookup(ctx, t.ID)
				if errors.Is(e, uplink.ErrUnsupported) {
					continue
				}
				if e != nil {
					return nil, e
				}
				targets = append(targets, v)
			}
			return targets, nil
		},
		WithLock: func(ctx context.Context, id string, work func() error) error {
			if a.orch == nil {
				return uplink.ErrUnsupported
			}
			return a.orch.WithTunnelLock(ctx, id, "uplink", work)
		},
		ApplyBinding: func(ctx context.Context, id, provider, device string) error {
			fresh, err := a.awgStore.Get(id)
			if err != nil {
				return err
			}
			if !a.nwgOp.SupportsNativeUplink(fresh) {
				return uplink.ErrUnsupported
			}
			label := provider
			if a.wanModel != nil {
				for _, p := range a.wanModel.ForUI() {
					if p.ID == provider && p.Label != "" {
						label = p.Label
						break
					}
				}
			}
			if err = a.awgStore.UpdateForUplink(id, func(t *storage.AWGTunnel) error {
				if !storage.SameUplinkTransport(fresh, t) {
					return uplink.ErrConflict
				}
				t.ISPInterface = provider
				t.ISPInterfaceLabel = label
				t.ActiveWAN = device
				return nil
			}); err != nil {
				return err
			}
			fresh.ISPInterface = provider
			fresh.ISPInterfaceLabel = label
			fresh.ActiveWAN = device
			if a.orch != nil {
				a.orch.RefreshTunnelState(id)
			}
			return a.nwgOp.ApplyNativeUplink(ctx, fresh, provider)
		},
	}
	manager, err := wireguard.NewManaged(deps)
	if err != nil {
		failed.Store(true)
		return nil, err
	}
	ready.Store(manager)
	return manager, nil
}
