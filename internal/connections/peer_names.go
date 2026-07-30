package connections

import (
	"context"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// WGServerPeersLister is the narrow contract for the system (NDMS) WG-server
// view: servers whose peers carry Description + AllowedIPs. Satisfied by
// api.ServersHandler; the same source feeds the sing-box connections monitor.
type WGServerPeersLister interface {
	ListServers(ctx context.Context) ([]ndms.WireguardServer, error)
}

// ManagedServersLister is the narrow contract for managed WG-servers, whose
// peers carry Description + TunnelIP. Satisfied by managed.Service.
type ManagedServersLister interface {
	List() []storage.ManagedServer
}

// SetPeerNameSources wires the optional WG peer-name sources (issue #639).
// Either may be nil — the service then resolves names from the NDMS hotspot
// (MAC-keyed) only, and tunnel clients keep rendering as raw IPs.
func (s *Service) SetPeerNameSources(wg WGServerPeersLister, managed ManagedServersLister) {
	s.wgServers = wg
	s.managedServers = managed
}

// resolvePeerNames builds the IP → peer-name map from both server kinds.
// Best-effort: a nil source or a list error contributes nothing.
func (s *Service) resolvePeerNames(ctx context.Context) map[string]string {
	var servers []ndms.WireguardServer
	if s.wgServers != nil {
		list, err := s.wgServers.ListServers(ctx)
		if err != nil {
			s.appLog.Warn("resolve-peer-names", "wg-servers", err.Error())
		} else {
			servers = list
		}
	}
	var managed []storage.ManagedServer
	if s.managedServers != nil {
		managed = s.managedServers.List()
	}
	return buildPeerNames(servers, managed)
}

// buildPeerNames maps each peer's single-host tunnel address to its
// description. Peers without a description or a host address are skipped.
func buildPeerNames(servers []ndms.WireguardServer, managed []storage.ManagedServer) map[string]string {
	out := make(map[string]string)
	for _, srv := range servers {
		for _, p := range srv.Peers {
			if p.Description == "" {
				continue
			}
			// Первая host-запись (/32|/128): у site-to-site пиров впереди
			// могут стоять маршрутизируемые подсети или 0.0.0.0/0.
			for _, entry := range p.AllowedIPs {
				if ip := peerHostIP(entry); ip != "" {
					out[ip] = p.Description
					break
				}
			}
		}
	}
	for _, srv := range managed {
		for _, p := range srv.Peers {
			if p.Description == "" {
				continue
			}
			if ip := peerHostIP(p.TunnelIP); ip != "" {
				out[ip] = p.Description
			}
		}
	}
	return out
}

// applyPeerNames fills ClientName from the peer map for connections that have
// no name yet. The NDMS hotspot (MAC-keyed) stays authoritative for LAN IPs.
func applyPeerNames(conns []Connection, names map[string]string) {
	if len(names) == 0 {
		return
	}
	for i := range conns {
		if conns[i].ClientName != "" {
			continue
		}
		if name, ok := names[conns[i].Src]; ok {
			conns[i].ClientName = name
		}
	}
}

// peerHostIP normalizes a single-host peer address ("10.0.0.2/32",
// "fd00::2/128" or bare "10.0.0.2") to a bare-IP map key. Returns "" for
// anything that is not a single host (e.g. "10.0.0.0/24") or not a valid IP.
func peerHostIP(entry string) string {
	entry = strings.TrimSpace(entry)
	host := entry
	if i := strings.IndexByte(entry, '/'); i >= 0 {
		if m := entry[i+1:]; m != "32" && m != "128" {
			return ""
		}
		host = entry[:i]
	}
	if net.ParseIP(host) == nil {
		return ""
	}
	return strings.ToLower(host)
}
