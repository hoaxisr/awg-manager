package wdtt

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/listenfirewall"
)

func serverFirewallPortSpecs(cfg ServerConfig) []listenfirewall.PortSpec {
	if !listenfirewall.OpenEnabled(cfg.OpenFirewall) {
		return nil
	}
	var specs []listenfirewall.PortSpec
	add := func(addr string) {
		port, ok := listenfirewall.WANListenPort(addr)
		if !ok {
			return
		}
		specs = append(specs, listenfirewall.PortSpec{Port: port, Proto: "udp"})
	}
	add(cfg.Listen)
	add(cfg.EffectiveRawListen())
	if direct := cfg.EffectiveDirectListen(); direct != "" && direct != cfg.Listen {
		add(direct)
	}
	return listenfirewall.MergePortSpecs(specs)
}

func applyServerListenFirewall(ctx context.Context, cfg ServerConfig) error {
	for _, spec := range serverFirewallPortSpecs(cfg) {
		if err := listenfirewall.Apply(ctx, spec.Port, spec.Proto); err != nil {
			return err
		}
	}
	return nil
}

func removeServerListenFirewall(ctx context.Context, cfg ServerConfig) {
	for _, spec := range serverFirewallPortSpecs(cfg) {
		listenfirewall.Remove(ctx, spec.Port, spec.Proto)
	}
}

// RunningServerListenPorts returns WAN listen ports for running servers.
func (s *Service) RunningServerListenPorts() []listenfirewall.PortSpec {
	full, err := s.store.Load()
	if err != nil {
		return nil
	}
	var out []listenfirewall.PortSpec
	for _, srv := range full.Servers {
		if !s.serverProcs.get(srv.ID).Status().Running {
			continue
		}
		out = append(out, serverFirewallPortSpecs(srv.Config)...)
	}
	return listenfirewall.MergePortSpecs(out)
}

func serverFirewallConfigChanged(prev, next ServerConfig) bool {
	return prev.Listen != next.Listen ||
		prev.RawListen != next.RawListen ||
		prev.DirectListen != next.DirectListen ||
		prev.RelayMode != next.RelayMode ||
		!openFirewallEqual(prev.OpenFirewall, next.OpenFirewall)
}

// serverProcessConfigChanged — поля, требующие перезапуска wdtt-server (qWDTT слушает raw+direct одновременно).
func serverProcessConfigChanged(prev, next ServerConfig) bool {
	return prev.Listen != next.Listen ||
		prev.RawListen != next.RawListen ||
		prev.DirectListen != next.DirectListen ||
		prev.WgPort != next.WgPort ||
		prev.WgIface != next.WgIface ||
		prev.Password != next.Password ||
		prev.ConfigDir != next.ConfigDir
}

func serverAccessConfigChanged(prev, next ServerConfig) bool {
	return prev.NatMode != next.NatMode ||
		prev.Policy != next.Policy ||
		prev.RelayMode != next.RelayMode ||
		prev.NdmsIface != next.NdmsIface ||
		prev.WgIface != next.WgIface ||
		!stringSliceEqual(prev.LanSegments, next.LanSegments)
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Service) syncServerListenFirewall(ctx context.Context, id string, prev, next ServerConfig) {
	if serverFirewallConfigChanged(prev, next) {
		removeServerListenFirewall(ctx, prev)
	}
	if s.serverProcs.get(id).Status().Running {
		if err := applyServerListenFirewall(ctx, next); err != nil && s.appLog != nil {
			s.appLog.Warn("firewall", id, "INPUT для listen-порта: "+err.Error())
		}
	}
}

func openFirewallEqual(a, b *bool) bool {
	return listenfirewall.OpenEnabled(a) == listenfirewall.OpenEnabled(b)
}

// serverListenPortSpec kept for callers that only need the DTLS port.
func serverListenPortSpec(cfg ServerConfig) (listenfirewall.PortSpec, bool) {
	if !listenfirewall.OpenEnabled(cfg.OpenFirewall) {
		return listenfirewall.PortSpec{}, false
	}
	port, ok := listenfirewall.WANListenPort(cfg.Listen)
	if !ok {
		return listenfirewall.PortSpec{}, false
	}
	return listenfirewall.PortSpec{Port: port, Proto: "udp"}, true
}
