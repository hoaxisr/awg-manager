// Package backend provides abstraction for tunnel process management.
// Supports kernel module backend for AmneziaWG tunnels.
package backend

import (
	"context"
	"time"
)

// Type represents the backend implementation type.
type Type int

const (
	// TypeKernel uses the kernel module.
	TypeKernel Type = iota
)

// String returns a human-readable representation of the backend type.
func (t Type) String() string {
	switch t {
	case TypeKernel:
		return "kernel"
	default:
		return "unknown"
	}
}

// Backend is the interface for tunnel interface management.
type Backend interface {
	// Type returns the backend type.
	Type() Type

	// Start starts the tunnel interface.
	Start(ctx context.Context, ifaceName string) error

	// Stop stops the tunnel interface.
	Stop(ctx context.Context, ifaceName string) error

	// IsRunning checks if the tunnel interface is active.
	// Returns running status and PID (0 for kernel backend).
	IsRunning(ctx context.Context, ifaceName string) (running bool, pid int)

	// WaitReady waits for the interface and socket to be ready.
	WaitReady(ctx context.Context, ifaceName string, timeout time.Duration) error
}
