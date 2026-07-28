package wdtt

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/listenfirewall"
)

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

func applyServerListenFirewall(ctx context.Context, cfg ServerConfig) error {
	if spec, ok := serverListenPortSpec(cfg); ok {
		return listenfirewall.Apply(ctx, spec.Port, spec.Proto)
	}
	return nil
}

func removeServerListenFirewall(ctx context.Context, cfg ServerConfig) {
	if spec, ok := serverListenPortSpec(cfg); ok {
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
		if spec, ok := serverListenPortSpec(srv.Config); ok {
			out = append(out, spec)
		}
	}
	return listenfirewall.MergePortSpecs(out)
}

func (s *Service) syncServerListenFirewall(ctx context.Context, id string, prev, next ServerConfig) {
	if prev.Listen != next.Listen || !openFirewallEqual(prev.OpenFirewall, next.OpenFirewall) {
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
