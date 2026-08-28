//go:build linux

package procres

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// OpenTunFD открывает существующий opkgtunN: IFF_TUN|IFF_NO_PI, без
// IFF_VNET_HDR, неблокирующий — контракт дескриптора §5.3 протокола.
// Паритет с internal/wdtt/tun_fd_linux.go:27 (openTunFD).
func OpenTunFD(name string) (*os.File, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("TUNSETIFF %s: %w", name, err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
