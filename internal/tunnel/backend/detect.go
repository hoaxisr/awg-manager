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
// Used when kernel mode is explicitly requested to handle the race between
// insmod completion and sysfs registration.
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

// Detect determines the appropriate backend type for the current system.
// Returns TypeKernel if kernel module is loaded, TypeUserspace otherwise.
func Detect() Type {
	if IsKernelAvailable() {
		return TypeKernel
	}
	return TypeUserspace
}

// New creates a new backend of the specified type.
func New(typ Type) Backend {
	switch typ {
	case TypeKernel:
		return NewKernel()
	default:
		return NewUserspace()
	}
}

// NewAuto detects and creates the appropriate backend.
// Uses kernel if available, otherwise userspace.
func NewAuto() Backend {
	return New(Detect())
}

// NewWithMode creates a backend based on the specified mode setting.
// Valid modes: "auto", "kernel", "userspace".
// If kernel mode is requested but module not loaded, waits briefly then falls back to userspace.
func NewWithMode(mode string, log Logger) Backend {
	switch mode {
	case "kernel":
		if IsKernelAvailable() {
			if log != nil {
				log.Info("Using kernel backend", map[string]interface{}{"mode": mode})
			}
			return NewKernel()
		}
		// Module may still be registering after insmod — wait with retry
		if log != nil {
			log.Info("Waiting for kernel module to become available", map[string]interface{}{"mode": mode})
		}
		if waitForKernel(5 * time.Second) {
			if log != nil {
				log.Info("Using kernel backend (after wait)", map[string]interface{}{"mode": mode})
			}
			return NewKernel()
		}
		if log != nil {
			log.Warn("Kernel mode requested but module not available, falling back to userspace",
				map[string]interface{}{"mode": mode})
		}
		return NewUserspace()

	case "userspace":
		if log != nil {
			log.Info("Using userspace backend", map[string]interface{}{"mode": mode})
		}
		return NewUserspace()

	default: // "auto" or empty
		if IsKernelAvailable() {
			if log != nil {
				log.Info("Auto-detected kernel backend", map[string]interface{}{"mode": "auto"})
			}
			return NewKernel()
		}
		// In auto mode, wait briefly in case module is still loading
		if waitForKernel(3 * time.Second) {
			if log != nil {
				log.Info("Auto-detected kernel backend (after wait)", map[string]interface{}{"mode": "auto"})
			}
			return NewKernel()
		}
		if log != nil {
			log.Info("Auto-detected userspace backend", map[string]interface{}{"mode": "auto"})
		}
		return NewUserspace()
	}
}
