package localdeps

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/api"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// UpdateTunnelSettings goes through api.TunnelsHandler.EditTunnel — the
// tunnel card's own edit — so the checks, the live apply and the restart
// on a WAN change are the web interface's, not a copy of them.
//
// EditTunnel takes a partial edit in the shape of storage.AWGTunnel, and
// applies the interface block only when Address is sent and the peer block
// only when PublicKey is (applyTunnelUpdate): then it takes every field of
// the block as sent. So a block is sent whole, built from the stored
// record with the caller's fields laid over it. Keys are left empty, which
// EditTunnel reads as "keep the stored one" — they never leave the store.
func (l *Local) UpdateTunnelSettings(ctx context.Context, in mcpsrv.TunnelSettingsInput) (mcpsrv.TunnelDetail, []string, error) {
	if l.c.TunnelEdit == nil || l.c.TunnelStore == nil {
		return mcpsrv.TunnelDetail{}, nil, errUnavailable("tunnel editing")
	}
	stored, err := l.c.TunnelStore.Get(in.TunnelID)
	if err != nil || stored == nil {
		return mcpsrv.TunnelDetail{}, nil, fmt.Errorf("tunnel %q not found (use list_tunnels)", in.TunnelID)
	}
	interfaceEdit := in.MTU != nil || in.DNS != nil || in.AWG != nil
	if (interfaceEdit || in.PersistentKeepalive != nil || in.ISPInterface != nil) && stored.Backend == "wdtt-raw" {
		return mcpsrv.TunnelDetail{}, nil, fmt.Errorf("tunnel %q is a proxy-backed (wdtt-raw) tunnel: only connectivityCheck can be changed here; the rest is set on the WDTT instance", in.TunnelID)
	}

	var req storage.AWGTunnel
	if interfaceEdit {
		req.Interface = stored.Interface
		req.Interface.PrivateKey = ""
		if in.MTU != nil {
			req.Interface.MTU = *in.MTU
		}
		if in.DNS != nil {
			req.Interface.DNS = strings.TrimSpace(*in.DNS)
		}
		if a := in.AWG; a != nil {
			o := &req.Interface.AWGObfuscation
			for dst, src := range map[*int]*int{&o.Jc: a.Jc, &o.Jmin: a.Jmin, &o.Jmax: a.Jmax, &o.S1: a.S1, &o.S2: a.S2, &o.S3: a.S3, &o.S4: a.S4} {
				if src != nil {
					*dst = *src
				}
			}
			for dst, src := range map[*string]*string{&o.H1: a.H1, &o.H2: a.H2, &o.H3: a.H3, &o.H4: a.H4} {
				if src != nil {
					*dst = strings.TrimSpace(*src)
				}
			}
		}
	}
	if in.PersistentKeepalive != nil {
		req.Peer = stored.Peer
		req.Peer.PresharedKey = ""
		req.Peer.PersistentKeepalive = storage.Keepalive(strings.TrimSpace(*in.PersistentKeepalive))
	}
	if in.ISPInterface != nil {
		isp, label, err := l.ispInterface(ctx, in.TunnelID, *in.ISPInterface)
		if err != nil {
			return mcpsrv.TunnelDetail{}, nil, err
		}
		req.ISPInterface, req.ISPInterfaceLabel = isp, label
	}
	if in.ConnectivityCheck != nil {
		req.ConnectivityCheck = &storage.ConnectivityCheckConfig{Method: *in.ConnectivityCheck}
		if *in.ConnectivityCheck == "ping" {
			req.ConnectivityCheck.PingTarget = in.PingTarget
		}
	}

	edited, err := l.c.TunnelEdit(ctx, in.TunnelID, req)
	if err != nil {
		l.tunnelLog.Warn("update", stored.Name, "Failed to change tunnel settings (MCP): "+err.Error())
		var editErr *api.TunnelEditError
		if errors.As(err, &editErr) {
			// The card's own words: validators, the lock, a busy tunnel.
			return mcpsrv.TunnelDetail{}, nil, fmt.Errorf("%s", editErr.Message)
		}
		return mcpsrv.TunnelDetail{}, nil, err
	}
	l.tunnelLog.Info("update", stored.Name, "Tunnel settings changed (MCP)")
	detail, err := l.GetTunnel(ctx, in.TunnelID)
	if err != nil {
		return mcpsrv.TunnelDetail{}, edited.Warnings, fmt.Errorf("the settings are applied, but the tunnel could not be read back: %w", err)
	}
	return detail, edited.Warnings, nil
}

// ispInterface turns the tool's value into what the tunnel card sends:
// "auto", a kernel WAN name (the routing catalog's wan:<name> without the
// prefix) or tunnel:<id>. A WAN or a tunnel the catalog does not list is
// refused; the label is the catalog's name for it, as the card stores.
// A chain goes through another awg-manager tunnel only, as the card offers
// (its list is the other managed tunnels): resolveWAN looks the parent up
// in the tunnel store, so a system interface, a WAN or the tunnel itself
// would leave it unable to come up.
func (l *Local) ispInterface(ctx context.Context, tunnelID, v string) (isp, label string, err error) {
	if v == "auto" {
		return "auto", "", nil
	}
	lookup, wantType := v, "wan"
	if id, ok := strings.CutPrefix(v, "tunnel:"); ok {
		if id == tunnelID {
			return "", "", fmt.Errorf("a tunnel cannot be chained through itself")
		}
		lookup, wantType = id, "managed"
	}
	if l.c.Targets != nil {
		for _, e := range l.c.Targets.ListAll(ctx) {
			if e.ID == lookup && !e.Server && e.Type == wantType {
				label = sanitizeLabel(e.Name)
				if name, ok := strings.CutPrefix(v, "wan:"); ok {
					return name, label, nil
				}
				return v, label, nil
			}
		}
		return "", "", fmt.Errorf("%q is not a WAN connection or another awg-manager tunnel (use list_routing_targets: wan: ids and managed tunnels)", v)
	}
	if name, ok := strings.CutPrefix(v, "wan:"); ok {
		return name, "", nil
	}
	return v, "", nil
}
