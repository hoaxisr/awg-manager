package main

import (
	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// awgListenPortChecker marks localhost ports referenced by AWG tunnel peer
// endpoints so freeturn clients avoid colliding with existing WG configs.
type awgListenPortChecker struct {
	store *storage.AWGTunnelStore
}

func (a *awgListenPortChecker) OccupiedLocalListenPorts() (map[int]bool, error) {
	tunnels, err := a.store.List()
	if err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, tun := range tunnels {
		if port, ok := freeturn.LocalListenPort(tun.Peer.Endpoint); ok {
			used[port] = true
		}
	}
	return used, nil
}
