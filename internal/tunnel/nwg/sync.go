// Runtime field-level sync methods for OperatorNativeWG.
//
// These are called from service.applyDiffNWG to push specific stored
// fields (DNS, address/MTU, peer, AWG params, description) to a running
// NDMS interface without restarting it. They are decoupled from the
// lifecycle (Create/Start/Stop/Delete), which lives in operator.go and
// owns the heavier orchestration around kmod, peer-via, etc.
package nwg

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// SyncDNS reconciles DNS servers for a NativeWG tunnel: clears oldDNS
// from NDMS, then applies newDNS. Either side may be nil/empty —
// passing both lists explicitly avoids needing applied-state tracking.
//
// Contract asymmetry vs OperatorOS5Impl.SyncDNS(ctx, id, dns): the OS5
// path tracks applied DNS internally and computes its own diff. The NWG
// path takes both lists as parameters and is stateless. This is
// deliberate — caller already knows oldDNS (it's the previous stored
// value), so the diff naturally lives at the call site.
//
// Use cases:
//   - Start tunnel: SyncDNS(ctx, stored, nil, tunnel.ParseDNSList(stored.Interface.DNS))
//   - Stop tunnel:  SyncDNS(ctx, stored, tunnel.ParseDNSList(stored.Interface.DNS), nil)
//   - Update DNS:   SyncDNS(ctx, stored, oldList, newList)
func (o *OperatorNativeWG) SyncDNS(ctx context.Context, stored *storage.AWGTunnel, oldDNS, newDNS []string) error {
	names := NewNWGNames(stored.NWGIndex)
	if len(oldDNS) > 0 {
		if err := o.commands.Interfaces.ClearDNS(ctx, names.NDMSName, oldDNS); err != nil {
			o.log.Warnf("nwg: clear DNS for %s: %v", names.NDMSName, err)
		}
	}
	if len(newDNS) > 0 {
		if err := o.commands.Interfaces.SetDNS(ctx, names.NDMSName, newDNS); err != nil {
			return fmt.Errorf("set DNS: %w", err)
		}
	}
	return nil
}

// SyncAWGParams applies AmneziaWG obfuscation parameters (Jc, Jmin,
// Jmax, S1-S4, H1-H4, I1-I5, Qlen) to a running NativeWG tunnel via
// RCI. Best-effort: if NDMS rejects (some firmware versions require
// interface down for ASC changes), failures bubble up so the caller
// can log a Warn and instruct the user to restart the tunnel.
func (o *OperatorNativeWG) SyncAWGParams(ctx context.Context, stored *storage.AWGTunnel) error {
	if !ndmsinfo.SupportsWireguardASC() {
		return fmt.Errorf("ASC not supported by firmware; restart tunnel to apply")
	}
	names := NewNWGNames(stored.NWGIndex)
	ascJSON, err := buildASCJSON(&stored.Interface)
	if err != nil {
		return fmt.Errorf("build ASC params: %w", err)
	}
	if ascJSON == nil {
		return nil
	}
	if err := o.commands.Wireguard.SetASCParams(ctx, names.NDMSName, ascJSON); err != nil {
		return fmt.Errorf("set ASC params: %w", err)
	}
	return nil
}

// SyncAddressMTU pushes the stored address and MTU to the NDMS interface.
// Called on Start (to override any changes made via the router UI)
// and on Update (to hot-apply changes to a running tunnel).
func (o *OperatorNativeWG) SyncAddressMTU(ctx context.Context, stored *storage.AWGTunnel) error {
	ndmsName := NewNWGNames(stored.NWGIndex).NDMSName
	ipv4 := extractIPv4(stored.Interface.Address)

	addr, mask := splitAddressMask(ipv4)
	if err := o.commands.Interfaces.SetAddress(ctx, ndmsName, addr, mask); err != nil {
		return fmt.Errorf("sync address: %w", err)
	}

	ipv6 := extractIPv6(stored.Interface.Address)
	if ipv6 != "" {
		if err := o.commands.Interfaces.SetIPv6Address(ctx, ndmsName, ipv6); err != nil {
			o.log.Warnf("nwg: sync ipv6 address on %s: %v", ndmsName, err)
		}
	} else {
		_ = o.commands.Interfaces.ClearIPv6Address(ctx, ndmsName)
	}

	if err := o.commands.Interfaces.SetMTU(ctx, ndmsName, stored.Interface.MTU); err != nil {
		return fmt.Errorf("sync mtu: %w", err)
	}

	o.log.Infof("nwg: synced address=%s ipv6=%s mtu=%d on %s", ipv4, ipv6, stored.Interface.MTU, ndmsName)
	return nil
}

// SyncPeer pushes the stored peer configuration to the NDMS interface.
// This applies key/allowed-ips/keepalive/preshared-key from storage.
func (o *OperatorNativeWG) SyncPeer(ctx context.Context, stored *storage.AWGTunnel) error {
	ndmsName := NewNWGNames(stored.NWGIndex).NDMSName
	o.appLog.Full("replace-config", stored.Name, "Syncing peer parameters to NDMS")

	peerCfg := rci.PeerConfig{
		PublicKey: stored.Peer.PublicKey,
		Endpoint:  stored.Peer.Endpoint,
	}
	if stored.Peer.PersistentKeepalive > 0 {
		peerCfg.KeepaliveInterval = stored.Peer.PersistentKeepalive
	}
	if stored.Peer.PresharedKey != "" {
		peerCfg.PresharedKey = stored.Peer.PresharedKey
	}

	for _, raw := range stored.Peer.AllowedIPs {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if _, netw, err := net.ParseCIDR(s); err == nil && netw != nil {
			ones, _ := netw.Mask.Size()
			item := rci.AllowedIP{Address: netw.IP.String(), Mask: strconv.Itoa(ones)}
			if netw.IP.To4() != nil {
				peerCfg.AllowedIPv4 = append(peerCfg.AllowedIPv4, item)
			} else {
				peerCfg.AllowedIPv6 = append(peerCfg.AllowedIPv6, item)
			}
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			if v4 := ip.To4(); v4 != nil {
				peerCfg.AllowedIPv4 = append(peerCfg.AllowedIPv4, rci.AllowedIP{
					Address: v4.String(),
					Mask:    "32",
				})
			} else {
				peerCfg.AllowedIPv6 = append(peerCfg.AllowedIPv6, rci.AllowedIP{
					Address: ip.String(),
					Mask:    "128",
				})
			}
		}
	}

	_, err := o.transport.PostBatch(ctx, []any{
		rci.CmdWireguardPeer(ndmsName, peerCfg),
		rci.CmdSave(),
	})
	if err != nil {
		return fmt.Errorf("sync peer: %w", err)
	}

	if stored.ISPInterface != "" {
		if _, err := o.transport.Post(ctx, rci.CmdWireguardPeerConnect(ndmsName, stored.Peer.PublicKey, stored.ISPInterface)); err != nil {
			o.log.Warnf("nwg: sync peer connect via on %s: %v", ndmsName, err)
		}
	}

	o.appLog.Full("replace-config", stored.Name, "Peer sync complete")
	o.log.Infof("nwg: synced peer on %s (allowed v4=%d, v6=%d)", ndmsName, len(peerCfg.AllowedIPv4), len(peerCfg.AllowedIPv6))
	return nil
}

// UpdateDescription updates the NDMS interface description.
func (o *OperatorNativeWG) UpdateDescription(ctx context.Context, stored *storage.AWGTunnel, name string) error {
	return o.commands.Interfaces.SetDescription(ctx, NewNWGNames(stored.NWGIndex).NDMSName, name)
}
