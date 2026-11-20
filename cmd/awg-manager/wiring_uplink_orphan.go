package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func (a *app) storedUplinkAbsent(ctx context.Context, ref, device string) (bool, error) {
	if a.awgStore == nil || a.ndmsQueries == nil {
		return false, uplink.ErrUnsupported
	}
	if _, err := a.awgStore.Get(ref); !errors.Is(err, storage.ErrNotFound) {
		return false, err
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return false, err
	}
	for _, iface := range interfaces {
		if iface.Name == device {
			return false, nil
		}
	}
	if strings.HasPrefix(device, "nwg") {
		index := strings.TrimPrefix(device, "nwg")
		n, err := strconv.Atoi(index)
		if err != nil || n < 0 || strconv.Itoa(n) != index {
			return false, uplink.ErrConflict
		}
		id := "Wireguard" + index
		if _, found, err := a.ndmsQueries.WGServers.InterfaceRCRawRecent(ctx, id, 0); err != nil || found {
			return false, err
		}
		if _, found, err := a.ndmsQueries.WGServers.InterfaceRuntimeLive(ctx, id); err != nil || found {
			return false, err
		}
	}
	return true, nil
}
