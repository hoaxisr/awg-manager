// cmd/awg-manager/awg_outbounds_adapters.go
package main

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

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
func (a *awgoutboundsSingboxAdapter) Reload() error      { return a.op.Reload() }

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
			Tag: t.Tag, Label: t.Label, Kind: t.Kind, Iface: t.Iface,
		})
	}
	return out, nil
}

// routerAWGTagAdapter projects awgoutbounds.TagInfo into
// router.AWGTag. main.go owns this projection so router doesn't
// import awgoutbounds types.
type routerAWGTagAdapter struct {
	src awgoutbounds.Service
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
	return out, nil
}
