package proxylisten

import (
	"net"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// CrossChecker reports localhost UDP ports used by AWG tunnel peer endpoints
// and by the sibling proxy client (FreeTurn ↔ WDTT), plus WAN/server ports
// (FreeTurn/WDTT DTLS listen and WDTT internal WG).
type CrossChecker struct {
	AWGStore *storage.AWGTunnelStore

	FreeTurn *freeturn.Service
	WDTT     *wdtt.Service

	IncludeFreeTurnClients bool
	IncludeWdttClients     bool
}

func anyHostPort(addr string) (int, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 0, false
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

func (c *CrossChecker) OccupiedLocalListenPorts() (map[int]bool, error) {
	used := map[int]bool{}

	if c.AWGStore != nil {
		tunnels, err := c.AWGStore.List()
		if err != nil {
			return nil, err
		}
		for _, tun := range tunnels {
			if port, ok := freeturn.LocalListenPort(tun.Peer.Endpoint); ok {
				used[port] = true
			}
		}
	}

	if c.FreeTurn != nil {
		cfg, err := c.FreeTurn.GetConfig()
		if err != nil {
			return nil, err
		}
		if c.IncludeFreeTurnClients {
			for _, cl := range cfg.Clients {
				if port, ok := freeturn.LocalListenPort(cl.Config.Listen); ok {
					used[port] = true
				}
			}
		}
		for _, srv := range cfg.Servers {
			if port, ok := anyHostPort(srv.Config.Listen); ok {
				used[port] = true
			}
		}
	}

	if c.WDTT != nil {
		cfg, err := c.WDTT.GetConfig()
		if err != nil {
			return nil, err
		}
		if c.IncludeWdttClients {
			for _, cl := range cfg.Clients {
				if port, ok := wdtt.LocalListenPort(cl.Config.Listen); ok {
					used[port] = true
				}
			}
		}
		for _, srv := range cfg.Servers {
			if port, ok := anyHostPort(srv.Config.Listen); ok {
				used[port] = true
			}
			if srv.Config.WgPort > 0 {
				used[srv.Config.WgPort] = true
			}
		}
	}

	return used, nil
}
