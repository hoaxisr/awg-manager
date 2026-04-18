package traffic

import (
	"context"
	"time"
)

// RunningTunnel describes a tunnel that is currently active, including its
// traffic counters. Producers (tunnel/service, api/systemtunnels) fill it
// from a single NDMS call per tunnel; consumers (connectivity monitor) read
// it to drive their own logic.
//
// IfaceName is the KERNEL name (e.g. "nwg0", "opkgtun1") — used by
// connectivity probes that hit the kernel (ip, ping, curl via interface).
// NDMSName is the NDMS logical name (e.g. "Wireguard3", "OpkgTun0") —
// used when calling NDMS RCI endpoints. Code that confuses the two
// produces 404s against /show/interface/<name>/...
type RunningTunnel struct {
	ID            string
	BackendType   string // "kernel" or "nativewg" or "system"
	IfaceName     string
	NDMSName      string
	RxBytes       int64
	TxBytes       int64
	LastHandshake time.Time
	ConnectedAt   string // RFC3339 or empty
}

// TunnelLister returns the list of currently running tunnels with their
// traffic counters. Implementations collect state+traffic in a single call
// per tunnel.
type TunnelLister interface {
	RunningTunnels(ctx context.Context) []RunningTunnel
}
