//go:build !linux

package wireguard

import (
	"context"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type Kernel struct{}

func NewKernel(KernelDeps) (*Kernel, error)                                { return &Kernel{}, nil }
func (*Kernel) OwnsTarget(string) bool                                     { return false }
func (*Kernel) UplinkTargets(context.Context) ([]uplink.TargetInfo, error) { return nil, nil }
func (*Kernel) Validate(context.Context, uplink.Target) error              { return uplink.ErrUnsupported }
func (*Kernel) Observe(context.Context, uplink.Target) (uplink.Observation, error) {
	return uplink.Observation{}, uplink.ErrUnsupported
}
func (*Kernel) Activate(context.Context, uplink.Target, string, string) error {
	return uplink.ErrUnsupported
}
func (*Kernel) Probe(context.Context, uplink.Target, string) error { return uplink.ErrUnsupported }
func (*Kernel) ReleaseUplink(context.Context, uplink.Target) error { return nil }
func (*Kernel) PrepareForStart(context.Context, string) error      { return uplink.ErrUnsupported }
func (*Kernel) StartupDevice(context.Context, string) (string, error) {
	return "", uplink.ErrUnsupported
}
