package firc

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"time"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type UplinkAdapter struct {
	Operator     *Operator
	Active       func() bool
	DNSForDevice uplink.DNSForDevice
}

func (a *UplinkAdapter) UplinkCapabilities(uplink.Target) uplink.Capabilities {
	return uplink.Capabilities{ProviderDNS: true, RestartScope: "target", ControlledFailback: true, Limitation: "AWGM Route supports manual or controlled return with reconnection; independent candidate probe unavailable"}
}

func requiresUplinkDNS(t Tunnel) bool {
	if len(t.EndpointDNS) > 0 {
		return true
	}
	for _, source := range t.Sources {
		if source.URL != "" || source.Kind == "subscription" {
			return true
		}
		u, err := url.Parse(source.Link)
		if err != nil || net.ParseIP(u.Hostname()) == nil {
			return true
		}
	}
	return false
}

func validateEndpointDNS(servers []string) error {
	if len(servers) < 1 || len(servers) > 8 {
		return fmt.Errorf("one to eight explicit WAN DNS servers required")
	}
	for _, server := range servers {
		if _, err := uplink.ParseDNSServer(server); err != nil {
			return err
		}
	}
	return nil
}

func (a *UplinkAdapter) read(ctx context.Context, t uplink.Target) (Tunnel, TunnelInbound, error) {
	if t.Kind != "" {
		return Tunnel{}, TunnelInbound{}, uplink.ErrUnsupported
	}
	if a.Operator == nil || a.Active == nil || !a.Active() {
		return Tunnel{}, TunnelInbound{}, fmt.Errorf("AWGM Route is not the active engine")
	}
	if !a.Operator.IsRunning(ctx) {
		return Tunnel{}, TunnelInbound{}, fmt.Errorf("AWGM Route is stopped")
	}
	tunnels, err := a.Operator.Client().GetTunnels(ctx)
	if err != nil {
		return Tunnel{}, TunnelInbound{}, err
	}
	for _, v := range tunnels {
		if v.ID != t.Ref {
			continue
		}
		if requiresUplinkDNS(v) {
			health, err := a.Operator.Client().Ping(ctx)
			if err != nil {
				return Tunnel{}, TunnelInbound{}, err
			}
			if health.UplinkDNSVersion < 1 || a.DNSForDevice == nil {
				return Tunnel{}, TunnelInbound{}, fmt.Errorf("%w: AWGM Route runtime with uplinkDnsVersion >= 1 and an explicit WAN DNS catalogue required", uplink.ErrUnsupported)
			}
		}
		for _, in := range v.Inbounds {
			if UplinkPoolInbound(in) {
				return v, in, nil
			}
		}
		return Tunnel{}, TunnelInbound{}, fmt.Errorf("%w: dedicated local SOCKS inbound with node '*' required", uplink.ErrUnsupported)
	}
	return Tunnel{}, TunnelInbound{}, fmt.Errorf("AWGM Route tunnel not found")
}

func (a *UplinkAdapter) Validate(ctx context.Context, t uplink.Target) error {
	_, _, err := a.read(ctx, t)
	return err
}
func (a *UplinkAdapter) Observe(ctx context.Context, t uplink.Target) (uplink.Observation, error) {
	v, _, err := a.read(ctx, t)
	if err != nil {
		return uplink.Observation{}, err
	}
	device := ""
	if v.Uplink.Kind == "iface" {
		device = v.Uplink.Ref
	}
	return uplink.Observation{Device: device, Enabled: v.Enable}, nil
}
func (a *UplinkAdapter) Activate(ctx context.Context, t uplink.Target, expected, device string) error {
	o := a.Operator
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	if _, err := net.InterfaceByName(device); err != nil {
		return fmt.Errorf("%w: %w", uplink.ErrPathUnavailable, err)
	}
	v, _, err := a.read(ctx, t)
	if err != nil {
		return err
	}
	if !v.Enable {
		return fmt.Errorf("AWGM Route tunnel is disabled")
	}
	var servers []string
	if requiresUplinkDNS(v) {
		servers, err = a.DNSForDevice(ctx, device)
		if err == nil {
			err = validateEndpointDNS(servers)
		}
		if err != nil {
			return fmt.Errorf("%w: %w", uplink.ErrPathUnavailable, err)
		}
	}
	tunnels, err := o.Client().GetTunnels(ctx)
	if err != nil {
		return err
	}
	for i := range tunnels {
		if tunnels[i].ID == t.Ref {
			bound := ""
			if tunnels[i].Uplink.Kind == "iface" {
				bound = tunnels[i].Uplink.Ref
			}
			if bound != expected {
				return uplink.ErrConflict
			}
			tunnels[i].Uplink = TunnelUplink{Kind: "iface", Ref: device}
			if requiresUplinkDNS(tunnels[i]) {
				if len(servers) == 0 {
					return uplink.ErrConflict
				}
				tunnels[i].EndpointDNS = append([]string(nil), servers...)
			}
			_, err = o.applyTunnelsLocked(ctx, tunnels, false)
			return err
		}
	}
	return fmt.Errorf("AWGM Route tunnel disappeared")
}
func (a *UplinkAdapter) Probe(ctx context.Context, t uplink.Target, device string) error {
	a.Operator.mutationMu.Lock()
	defer a.Operator.mutationMu.Unlock()
	v, in, err := a.read(ctx, t)
	if err != nil {
		return err
	}
	if !v.Enable || v.Uplink.Kind != "iface" || v.Uplink.Ref != device {
		return fmt.Errorf("AWGM Route binding changed during probe")
	}
	if requiresUplinkDNS(v) {
		if err := validateEndpointDNS(v.EndpointDNS); err != nil {
			return err
		}
	}
	// The supervisor may still be spawning the replacement child after PUT.
	// Configuration acceptance alone is not evidence of a routed live tunnel.
	var probeErr error
	for {
		states, stateErr := a.Operator.Client().GetTunnelsState(ctx)
		if stateErr != nil {
			return stateErr
		}
		ready := false
		for _, s := range states {
			if s.ID == t.Ref && s.Status == "up" && s.UplinkOk {
				for _, ib := range s.Inbounds {
					if ib.Port == in.Port && ib.State == "up" {
						ready = true
						break
					}
				}
				break
			}
		}
		if ready {
			probeErr = uplink.ProbeSOCKS(ctx, in.Port, in.User, in.Password)
			if probeErr == nil {
				fresh, _, err := a.read(ctx, t)
				if err != nil {
					return err
				}
				if !fresh.Enable || fresh.Uplink.Kind != "iface" || fresh.Uplink.Ref != device {
					return fmt.Errorf("AWGM Route binding changed during probe")
				}
				if !reflect.DeepEqual(v, fresh) {
					return fmt.Errorf("AWGM Route configuration changed during probe")
				}
				return nil
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			if probeErr != nil {
				return fmt.Errorf("waiting for AWGM Route SOCKS readiness: %v: %w", probeErr, ctx.Err())
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}
