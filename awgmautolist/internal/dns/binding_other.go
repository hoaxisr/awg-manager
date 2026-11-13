//go:build !linux

package dns

import (
	"fmt"
	"runtime"
	"syscall"
)

// bindToDeviceSupported is the compile-time capability flag for the platform:
// SO_BINDTODEVICE is Linux-only, other platforms never bind.
var bindToDeviceSupported = false

// runtimeGOOS is the build-target OS, overridden in tests.
var runtimeGOOS = runtime.GOOS

// bindToDeviceImpl is unreachable off Linux: newBoundDialer refuses to bind
// before reaching this file, returning an explicit error instead.
func bindToDeviceImpl(c syscall.RawConn, iface string) error {
	return fmt.Errorf("interface binding is not supported on %s", runtimeGOOS)
}
