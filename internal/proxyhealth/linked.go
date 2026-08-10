package proxyhealth

import (
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// AWGLinkedTunnelResolver finds linked tunnel kernel ifaces in AWG storage.
type AWGLinkedTunnelResolver struct {
	Store *storage.AWGTunnelStore
}

func (r *AWGLinkedTunnelResolver) FreeTurnLinkedIface(clientID string) (string, bool) {
	if r == nil || r.Store == nil {
		return "", false
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return "", false
	}
	tunnels, err := r.Store.List()
	if err != nil {
		return "", false
	}
	for _, tun := range tunnels {
		if strings.TrimSpace(tun.FreeTurnClientID) != clientID {
			continue
		}
		if !tun.Enabled {
			continue
		}
		iface := linkedTunnelIface(&tun)
		if iface == "" {
			continue
		}
		return iface, true
	}
	return "", false
}

func linkedTunnelIface(stored *storage.AWGTunnel) string {
	if stored == nil {
		return ""
	}
	if stored.Backend == "nativewg" {
		return nwg.NewNWGNames(stored.NWGIndex).IfaceName
	}
	if stored.Backend == "wdtt-raw" && strings.TrimSpace(stored.RawKernelIface) != "" {
		return strings.TrimSpace(stored.RawKernelIface)
	}
	return tunnel.NewNames(stored.ID).IfaceName
}
