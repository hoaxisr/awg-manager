package backend

import (
	"os"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
)

// Logger interface for backend logging.
type Logger interface {
	Warn(msg string, fields ...map[string]interface{})
	Info(msg string, fields ...map[string]interface{})
}

// IsKernelAvailable checks if the AmneziaWG kernel module is loaded.
func IsKernelAvailable() bool {
	_, err := os.Stat(kmod.SysfsPath)
	return err == nil
}

// waitForKernel polls for the kernel module sysfs entry with a timeout.
func waitForKernel(timeout time.Duration) bool {
	deadline := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if IsKernelAvailable() {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-ticker.C:
		}
	}
}

// New creates a kernel backend.
func New(log Logger) Backend {
	if IsKernelAvailable() {
		if log != nil {
			log.Info("Using kernel backend")
		}
		return NewKernel()
	}

	// Module may still be registering after insmod — wait with retry
	if log != nil {
		log.Info("Waiting for kernel module to become available")
	}
	if waitForKernel(5 * time.Second) {
		if log != nil {
			log.Info("Using kernel backend (after wait)")
		}
		return NewKernel()
	}

	if log != nil {
		log.Warn("Kernel module not available — tunnel operations may fail")
	}
	return NewKernel()
}
