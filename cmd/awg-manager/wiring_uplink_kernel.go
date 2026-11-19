package main

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/wireguard"
)

func (a *app) kernelPolicyEnabled(store *uplink.Store, id string) bool {
	for _, p := range store.List() {
		if p.Enabled && p.Target.Engine == "kernel-amneziawg" && p.Target.Ref == id {
			return true
		}
	}
	return false
}

func (a *app) setupKernelUplink(policies *uplink.Store) uplink.Adapter {
	if a.orch != nil {
		a.orch.SetManagedKernelUplink(func(id string) bool { return a.kernelPolicyEnabled(policies, id) }, func(ctx context.Context, id string) (string, error) {
			k := a.kernelUplink.Load()
			if k == nil {
				return "", uplink.ErrConflict
			}
			return k.StartupDevice(ctx, id)
		})
	}
	hooks, hasHooks := a.operator.(interface {
		SetUplinkLifecycle(func(string) bool, func(context.Context, string) error)
	})
	if !hasHooks {
		return unavailableNativeUplink{err: fmt.Errorf("%w: kernel fallback requires guarded OS5 lifecycle", uplink.ErrUnsupported), applies: func(id string) bool { return a.kernelPolicyEnabled(policies, id) }}
	}
	if hasHooks {
		hooks.SetUplinkLifecycle(func(id string) bool { return a.kernelPolicyEnabled(policies, id) }, func(ctx context.Context, id string) error {
			if !a.kernelPolicyEnabled(policies, id) {
				return nil
			}
			if c := a.uplinkController.Load(); c != nil && c.BackupPaused() {
				return fmt.Errorf("%w: kernel uplink paused for backup", uplink.ErrConflict)
			}
			k := a.kernelUplink.Load()
			if k == nil {
				return fmt.Errorf("%w: kernel uplink not initialized", uplink.ErrConflict)
			}
			return k.PrepareForStart(ctx, id)
		})
	}
	lookup := func(ctx context.Context, id string) (wireguard.ManagedTarget, error) {
		if !hasHooks || a.awgStore == nil {
			return wireguard.ManagedTarget{}, uplink.ErrUnsupported
		}
		t, err := a.awgStore.Get(id)
		if err != nil {
			return wireguard.ManagedTarget{}, err
		}
		index, parseErr := strconv.Atoi(strings.TrimPrefix(t.ID, "awg"))
		if parseErr != nil || index < 0 || fmt.Sprintf("awg%d", index) != t.ID {
			return wireguard.ManagedTarget{}, uplink.ErrUnsupported
		}
		if (t.Backend != "kernel" && t.Backend != "") || t.Obfuscator != nil || tunnel.IsTunnelRoute(t.ISPInterface) {
			return wireguard.ManagedTarget{}, uplink.ErrUnsupported
		}
		private, err := base64.StdEncoding.DecodeString(t.Interface.PrivateKey)
		if err != nil {
			return wireguard.ManagedTarget{}, uplink.ErrUnsupported
		}
		key, err := ecdh.X25519().NewPrivateKey(private)
		if err != nil {
			return wireguard.ManagedTarget{}, uplink.ErrUnsupported
		}
		return wireguard.ManagedTarget{Ref: t.ID, Label: t.Name, Interface: tunnel.NewNames(t.ID).IfaceName, Peer: t.Peer.PublicKey, Endpoint: t.Peer.Endpoint, TunnelDNS: tunnel.ParseDNSList(t.Interface.DNS), AllowedIPs: append([]string(nil), t.Peer.AllowedIPs...), LocalPublicKey: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), Enabled: t.Enabled}, nil
	}
	k, err := wireguard.NewKernel(wireguard.KernelDeps{ManagedDeps: wireguard.ManagedDeps{
		Directory: a.dataDir, Lookup: lookup, ConfirmAbsent: a.storedUplinkAbsent,
		List: func(ctx context.Context) ([]wireguard.ManagedTarget, error) {
			if a.awgStore == nil {
				return nil, nil
			}
			list, err := a.awgStore.ListStrict()
			if err != nil {
				return nil, err
			}
			var result []wireguard.ManagedTarget
			for _, t := range list {
				if (t.Backend == "kernel" || t.Backend == "") && t.Obfuscator == nil {
					v, e := lookup(ctx, t.ID)
					if e == nil {
						result = append(result, v)
					}
				}
			}
			return result, nil
		},
		Providers: func() []uplink.Provider {
			if a.wanModel == nil {
				return nil
			}
			return (uplinkWANResolver{a.wanModel}).Providers()
		},
		WithLock: func(ctx context.Context, id string, fn func() error) error {
			if a.orch == nil {
				return uplink.ErrUnsupported
			}
			return a.orch.WithTunnelLock(ctx, id, "kernel-uplink", fn)
		},
		// Kernel routing is owned by mark/table. Preserve the original ISP
		// metadata and baseline NDMS endpoint route for clean policy removal.
		ApplyBinding: func(ctx context.Context, id, provider, device string) error {
			if a.awgStore == nil {
				return uplink.ErrUnsupported
			}
			if err := a.awgStore.Update(id, func(t *storage.AWGTunnel) error { t.ActiveWAN = device; return nil }); err != nil {
				return err
			}
			if a.orch != nil {
				a.orch.RefreshTunnelState(id)
			}
			return nil
		},
	}, NativeMarks: func(ctx context.Context) (map[string][]uint32, error) {
		if a.nwgOp == nil {
			return nil, uplink.ErrUnsupported
		}
		return a.nwgOp.NativeUplinkPeerMarks(ctx)
	}, StartProviders: func(id string) []string {
		for _, p := range policies.List() {
			if p.Enabled && p.Target.Engine == "kernel-amneziawg" && p.Target.Ref == id {
				return append([]string(nil), p.Providers...)
			}
		}
		return nil
	}, RestoreRoute: func(ctx context.Context, id string) error {
		if a.orch == nil {
			return uplink.ErrUnsupported
		}
		return a.orch.RestoreKernelBaselineRoute(ctx, id)
	}})
	if err != nil {
		a.kernelUplinkFailed.Store(true)
		return unavailableNativeUplink{err: fmt.Errorf("%w: kernel uplink registry: %v", uplink.ErrConflict, err), applies: func(id string) bool {
			if a.awgStore == nil {
				return true
			}
			t, e := a.awgStore.Get(id)
			return e != nil || t.Backend == "kernel" || t.Backend == ""
		}}
	}
	a.kernelUplink.Store(k)
	return k
}

func (a *app) kernelTransportOwned(t *storage.AWGTunnel) bool {
	if t == nil {
		return false
	}
	if k := a.kernelUplink.Load(); k != nil && k.OwnsTarget(t.ID) {
		return true
	}
	return a.kernelUplinkFailed.Load() && (t.Backend == "kernel" || t.Backend == "")
}
