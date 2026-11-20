package wireguard

import (
	"context"
	"net/netip"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type Runner func(context.Context, string, ...string) ([]byte, error)

type Path struct {
	Binding uplink.Binding
	Gateway netip.Addr
	Direct  bool
}

// ManagedTarget contains no private key. The native lifecycle remains owned by
// NDMS; AWGM only reserves routing resources for its existing kernel mark.
type ManagedTarget struct {
	Ref, Label, Interface, Peer, Endpoint string
	TunnelDNS                             []string
	AllowedIPs                            []string // nil retains legacy IPv4 default
	LocalPublicKey                        string
	Enabled                               bool
}

type ManagedDeps struct {
	Directory string
	Lookup    func(context.Context, string) (ManagedTarget, error)
	// ConfirmAbsent independently verifies that a saved target no longer exists.
	ConfirmAbsent func(context.Context, string, string) (bool, error)
	List          func(context.Context) ([]ManagedTarget, error)
	Providers     func() []uplink.Provider
	WithLock      func(context.Context, string, func() error) error
	ApplyBinding  func(context.Context, string, string, string) error
	Run           Runner
	PeerMark      func(context.Context, ManagedTarget) (uint32, error)
	PeerMarks     func(context.Context) (map[string][]uint32, error)
	ResolvePath   func(context.Context, string) (Path, error)
}
