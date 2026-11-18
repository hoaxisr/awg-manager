//go:build !linux

package wireguard

import (
	"context"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type Managed struct{}

func (*Managed) OwnsTarget(string) bool        { return false }
func NewManaged(ManagedDeps) (*Managed, error) { return &Managed{}, nil }
func (*Managed) UplinkCapabilities(uplink.Target) uplink.Capabilities {
	return uplink.Capabilities{IndependentEngine: true, Limitation: "native uplink requires Linux"}
}
func (*Managed) UplinkTargets(context.Context) ([]uplink.TargetInfo, error) { return nil, nil }
func (*Managed) Validate(context.Context, uplink.Target) error              { return uplink.ErrUnsupported }
func (*Managed) Observe(context.Context, uplink.Target) (uplink.Observation, error) {
	return uplink.Observation{}, uplink.ErrUnsupported
}
func (*Managed) Activate(context.Context, uplink.Target, string, string) error {
	return uplink.ErrUnsupported
}
func (*Managed) Probe(context.Context, uplink.Target, string) error { return uplink.ErrUnsupported }
func (*Managed) ReleaseUplink(context.Context, uplink.Target) error { return nil }
func (*Managed) PrepareForStart(context.Context, string) error      { return uplink.ErrUnsupported }
