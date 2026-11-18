package wireguard

import (
	"context"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type KernelDeps struct {
	ManagedDeps
	// NDMS peer marks are not exposed by standard device fwmark queries.
	NativeMarks    func(context.Context) (map[string][]uint32, error)
	StartProviders func(string) []string
	RestoreRoute   func(context.Context, string) error
}

func (*Kernel) UplinkCapabilities(uplink.Target) uplink.Capabilities {
	return uplink.Capabilities{IndependentEngine: true, RestartScope: "target", ControlledFailback: true, Limitation: "kernel AmneziaWG; IPv4 outer endpoint; manual or controlled return; disabling restores original device mark/routing mode"}
}
