// Package wg provides an interface for WireGuard/AmneziaWG operations.
// Operations are performed via the awg CLI tool.
package wg

import (
	"context"
	"time"
)

// Client is the interface for WireGuard operations.
type Client interface {
	// SetConf applies a configuration file to an interface.
	// Command: awg setconf <iface> <confPath>
	SetConf(ctx context.Context, iface, confPath string) error

	// Show retrieves the current state of an interface.
	// Command: awg show <iface>
	Show(ctx context.Context, iface string) (*ShowResult, error)

	// RemovePeer removes a peer from an interface.
	// Command: awg set <iface> peer <publicKey> remove
	RemovePeer(ctx context.Context, iface, publicKey string) error

	// GetPeerPublicKey extracts the peer public key from interface state.
	// Uses Show internally.
	GetPeerPublicKey(ctx context.Context, iface string) (string, error)
}

// ShowResult contains the parsed output of awg show.
type ShowResult struct {
	// Interface info
	PublicKey  string
	ListenPort int

	// Peer info (first peer only - tunnels have single peer)
	HasPeer       bool
	PeerPublicKey string
	Endpoint      string
	AllowedIPs    []string
	LastHandshake time.Time
	RxBytes       int64
	TxBytes       int64
}

// HasRecentHandshake returns true if there was a handshake in the last duration.
func (r *ShowResult) HasRecentHandshake(within time.Duration) bool {
	if r.LastHandshake.IsZero() {
		return false
	}
	return time.Since(r.LastHandshake) < within
}
