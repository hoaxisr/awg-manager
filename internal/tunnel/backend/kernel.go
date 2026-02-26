package backend

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	kernelPollInterval = 50 * time.Millisecond
)

// KernelBackend manages AmneziaWG kernel module interfaces.
// Uses ip link add/del type amneziawg for interface management.
type KernelBackend struct{}

// NewKernel creates a new kernel backend.
func NewKernel() *KernelBackend {
	return &KernelBackend{}
}

// Type returns the backend type.
func (b *KernelBackend) Type() Type {
	return TypeKernel
}

// Start creates a kernel AmneziaWG interface.
// First attempts cleanup of any existing interface, then creates new one.
func (b *KernelBackend) Start(ctx context.Context, ifaceName string) error {
	// Cleanup any existing interface (ignore error - may not exist)
	_, _ = exec.Run(ctx, "/opt/sbin/ip", "link", "del", "dev", ifaceName)

	// Create kernel interface
	result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "add", "dev", ifaceName, "type", "amneziawg")
	if err != nil {
		return fmt.Errorf("create kernel interface: %w", exec.FormatError(result, err))
	}

	return nil
}

// Stop removes the kernel AmneziaWG interface.
func (b *KernelBackend) Stop(ctx context.Context, ifaceName string) error {
	result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "del", "dev", ifaceName)
	if err != nil {
		return fmt.Errorf("delete kernel interface: %w", exec.FormatError(result, err))
	}
	return nil
}

// IsRunning checks if the kernel interface exists AND is amneziawg type.
// Returns (running, pid) where pid is always 0 for kernel backend.
// At boot NDMS recreates opkgtun* devices as plain "tun" — we must verify the type.
func (b *KernelBackend) IsRunning(ctx context.Context, ifaceName string) (bool, int) {
	if _, err := os.Stat("/sys/class/net/" + ifaceName); err != nil {
		return false, 0
	}

	// Verify interface is actually amneziawg (not plain tun recreated by NDMS)
	result, err := exec.Run(ctx, "/opt/sbin/ip", "-d", "link", "show", "dev", ifaceName)
	if err != nil {
		return false, 0
	}

	return strings.Contains(result.Stdout, "amneziawg"), 0
}

// WaitReady waits for the kernel interface to be ready.
// For kernel backend, only waits for interface to appear in /sys/class/net.
func (b *KernelBackend) WaitReady(ctx context.Context, ifaceName string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(kernelPollInterval)
	defer ticker.Stop()

	for {
		// Check interface exists
		if _, err := os.Stat("/sys/class/net/" + ifaceName); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("timeout waiting for kernel interface %s", ifaceName)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Ensure KernelBackend implements Backend interface.
var _ Backend = (*KernelBackend)(nil)
