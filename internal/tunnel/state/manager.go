// Package state provides the single source of truth for tunnel state.
// Uses NDMS conf layer (intent) + process/interface checks for reliable state detection.
package state

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// Manager is the interface for determining tunnel state.
// This is the SINGLE SOURCE OF TRUTH for tunnel state.
// All other components should use this interface instead of doing their own checks.
type Manager interface {
	// GetState returns the comprehensive state of a tunnel.
	// This method checks all components:
	//   - NDMS OpkgTun existence and interface state
	//   - Process running status (via PID file)
	//   - WireGuard interface and peer status
	// and returns a unified StateInfo.
	// Use for operations (Start, Stop, WAN events) that need authoritative state.
	GetState(ctx context.Context, tunnelID string) tunnel.StateInfo

	// GetStateLightweight returns tunnel state without NDMS queries.
	// Checks only process status and WireGuard peer/traffic.
	// Use for dashboard polling (List) to avoid constant NDMS socket connections.
	GetStateLightweight(ctx context.Context, tunnelID string) tunnel.StateInfo
}

