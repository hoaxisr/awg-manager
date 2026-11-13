//go:build linux

package dns

import (
	"fmt"
	"runtime"
	"syscall"
)

// bindToDeviceSupported is the compile-time capability flag for the platform.
var bindToDeviceSupported = true

// runtimeGOOS is the build-target OS, overridden in tests.
var runtimeGOOS = runtime.GOOS

// bindToDeviceImpl pins the socket to a kernel interface with
// SO_BINDTODEVICE so probes follow the detour interface only.
func bindToDeviceImpl(c syscall.RawConn, iface string) error {
	var ctrlErr, sockErr error
	ctrlErr = c.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptString(
			int(fd),
			syscall.SOL_SOCKET,
			syscall.SO_BINDTODEVICE,
			iface,
		)
	})
	if ctrlErr != nil {
		return fmt.Errorf("control: %w", ctrlErr)
	}
	if sockErr != nil {
		return fmt.Errorf("SO_BINDTODEVICE %q: %w", iface, sockErr)
	}
	return nil
}
