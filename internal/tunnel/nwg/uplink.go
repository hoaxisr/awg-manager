package nwg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type nativeMarkRuntime struct {
	ID        string `json:"id"`
	Wireguard struct {
		Peer []struct {
			Key     string `json:"public-key"`
			Address string `json:"remote-endpoint-address"`
			Port    int    `json:"remote-port"`
			Mark    uint32 `json:"fwmark"`
		} `json:"peer"`
	} `json:"wireguard"`
}

// Keenetic uses a peer-specific NDMS mark even when the standard wg device
// fwmark reports off. Read the driver's runtime view, never set that mark.
func (o *OperatorNativeWG) NativeUplinkPeerMark(ctx context.Context, t *storage.AWGTunnel) (uint32, error) {
	if o.queries == nil || o.queries.WGServers == nil {
		return 0, fmt.Errorf("NDMS WG queries unavailable")
	}
	name := NewNWGNames(t.NWGIndex).NDMSName
	raw, ok, err := o.queries.WGServers.InterfaceRuntimeLive(ctx, name)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("%w: NativeUplinkPeerMark interface %q absent", uplink.ErrConflict, name)
	}
	var runtime nativeMarkRuntime
	if err = json.Unmarshal(raw, &runtime); err != nil {
		return 0, err
	}
	if runtime.ID != name || len(runtime.Wireguard.Peer) != 1 {
		return 0, fmt.Errorf("%w: NativeUplinkPeerMark id=%q (%q) peers=%d", uplink.ErrConflict, runtime.ID, name, len(runtime.Wireguard.Peer))
	}
	p := runtime.Wireguard.Peer[0]
	endpoint, err := netip.ParseAddrPort(t.Peer.Endpoint)
	if err != nil || p.Key != t.Peer.PublicKey || p.Address != endpoint.Addr().String() || p.Port != int(endpoint.Port()) {
		return 0, fmt.Errorf("%w: NativeUplinkPeerMark peer mismatch", uplink.ErrConflict)
	}
	if p.Mark == 0 {
		return 0, fmt.Errorf("%w: NativeUplinkPeerMark fwmark=0 on %s", uplink.ErrUnsupported, name)
	}
	return p.Mark, nil
}
func (o *OperatorNativeWG) NativeUplinkPeerMarks(ctx context.Context) (map[string][]uint32, error) {
	if o.queries == nil || o.queries.WGServers == nil {
		return nil, fmt.Errorf("NDMS WG queries unavailable")
	}
	// Called immediately after NativeUplinkPeerMark (or in a tight preflight
	// sequence): a 500ms snapshot window reuses the just-fetched /show/interface/
	// runtime list instead of doubling full-table RCI fetches.
	interfaces, err := o.queries.WGServers.WireguardRuntimesMaxAge(ctx, 500*time.Millisecond)
	if err != nil {
		return nil, err
	}
	result := map[string][]uint32{}
	for id, raw := range interfaces {
		var info nativeMarkRuntime
		if err = json.Unmarshal(raw, &info); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(id, "Wireguard") {
			continue
		}
		index, err := strconv.Atoi(strings.TrimPrefix(id, "Wireguard"))
		if err != nil || index < 0 || index >= MaxTunnels {
			return nil, fmt.Errorf("%w: NativeUplinkPeerMarks invalid id %q", uplink.ErrConflict, id)
		}
		if info.ID != "" && info.ID != id {
			return nil, fmt.Errorf("%w: NativeUplinkPeerMarks id mismatch %q != %q", uplink.ErrConflict, info.ID, id)
		}
		for _, p := range info.Wireguard.Peer {
			if p.Mark != 0 {
				iface := NewNWGNames(index).IfaceName
				result[iface] = append(result[iface], p.Mark)
			}
		}
	}
	return result, nil
}

func (o *OperatorNativeWG) SupportsNativeUplink(t *storage.AWGTunnel) bool {
	return t != nil && t.Backend == "nativewg" && t.NWGIndex >= 0 && t.NWGIndex < MaxTunnels && t.Obfuscator == nil && config.IsAWGObfuscated(&t.Interface) && o.useASC(&t.Interface)
}
func (o *OperatorNativeWG) SetUplinkBeforeStart(fn func(context.Context, string) error) {
	o.uplinkBeforeStart = fn
}

// ApplyNativeUplink changes NDMS's durable connect-via without changing its key,
// endpoint. It does not write a mark itself; NDMS may assign a new one.
// The caller already owns the per-tunnel lock.
func (o *OperatorNativeWG) ApplyNativeUplink(ctx context.Context, t *storage.AWGTunnel, provider string) error {
	if !o.SupportsNativeUplink(t) {
		return fmt.Errorf("%w: tunnel %s does not support native ASC uplink", uplink.ErrUnsupported, t.ID)
	}
	return o.ApplySystemUplink(ctx, t, provider)
}

// ApplySystemUplink executes only the checked peer/via operation. The caller
// must establish system-client ownership, full peer/endpoint/prefix identity
// and server exclusion before calling it. Plain system WG needs no ASC gate.
func (o *OperatorNativeWG) ApplySystemUplink(ctx context.Context, t *storage.AWGTunnel, provider string) error {
	endpoint, err := netip.ParseAddrPort(t.Peer.Endpoint)
	if err != nil || !endpoint.Addr().Is4() || !endpoint.Addr().IsGlobalUnicast() || endpoint.Port() == 0 || provider == "" || strings.TrimSpace(provider) != provider {
		return fmt.Errorf("%w: ApplySystemUplink invalid endpoint=%q or provider=%q", uplink.ErrUnsupported, t.Peer.Endpoint, provider)
	}
	if !t.Enabled {
		return nil
	}
	names := NewNWGNames(t.NWGIndex)
	iface, err := o.RequireIface(ctx, t)
	if err != nil {
		return err
	}
	if err := o.commands.Wireguard.SetPeerConnectVia(ctx, iface, t.Peer.PublicKey, provider); err != nil {
		return err
	}
	if err := o.commands.Interfaces.InterfaceUp(ctx, iface); err != nil {
		return err
	}
	if o.queries == nil || o.queries.WGServers == nil {
		return fmt.Errorf("NDMS WG queries unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		raw, ok, err := o.queries.WGServers.InterfaceRuntimeLive(bounded, names.NDMSName)
		if err != nil {
			return err
		}
		if ok {
			var runtime struct {
				ID        string `json:"id"`
				Wireguard struct {
					Peer []struct {
						Key     string `json:"public-key"`
						Via     string `json:"via"`
						Address string `json:"remote-endpoint-address"`
						Port    int    `json:"remote-port"`
					} `json:"peer"`
				} `json:"wireguard"`
			}
			if err = json.Unmarshal(raw, &runtime); err != nil {
				return err
			}
			if runtime.ID != names.NDMSName || len(runtime.Wireguard.Peer) != 1 || runtime.Wireguard.Peer[0].Key != t.Peer.PublicKey {
				return fmt.Errorf("%w: native runtime peer changed", uplink.ErrConflict)
			}
			p := runtime.Wireguard.Peer[0]
			if p.Via == provider && p.Address == endpoint.Addr().String() && p.Port == int(endpoint.Port()) {
				return nil
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-bounded.Done():
			timer.Stop()
			return bounded.Err()
		case <-timer.C:
		}
	}
}
