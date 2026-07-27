package proxylisten

import (
	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// CrossChecker reports localhost UDP ports used by AWG tunnel peer endpoints
// and by the sibling proxy client (FreeTurn ↔ WDTT).
type CrossChecker struct {
	AWGStore *storage.AWGTunnelStore

	FreeTurn *freeturn.Service
	WDTT     *wdtt.Service

	IncludeFreeTurnClients bool
	IncludeWdttClients     bool
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

	if c.IncludeFreeTurnClients && c.FreeTurn != nil {
		cfg, err := c.FreeTurn.GetConfig()
		if err != nil {
			return nil, err
		}
		for _, cl := range cfg.Clients {
			if port, ok := freeturn.LocalListenPort(cl.Config.Listen); ok {
				used[port] = true
			}
		}
	}

	if c.IncludeWdttClients && c.WDTT != nil {
		cfg, err := c.WDTT.GetConfig()
		if err != nil {
			return nil, err
		}
		for _, cl := range cfg.Clients {
			if port, ok := wdtt.LocalListenPort(cl.Config.Listen); ok {
				used[port] = true
			}
		}
	}

	return used, nil
}
