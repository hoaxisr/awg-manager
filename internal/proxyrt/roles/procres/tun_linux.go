//go:build linux

package procres

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/hoaxisr/awg-manager/internal/netdev"
)

// openTunDev — шов над open("/dev/net/tun"): тест проверяет, что при
// отсутствующем устройстве до TUNSETIFF дело не доходит.
var openTunDev = func() (int, error) { return unix.Open("/dev/net/tun", unix.O_RDWR, 0) }

// OpenTunFD открывает существующий opkgtunN: IFF_TUN|IFF_NO_PI, без
// IFF_VNET_HDR, неблокирующий — контракт дескриптора §5.3 протокола.
// Паритет с internal/wdtt/tun_fd_linux.go:27 (openTunFD).
//
// Устройства нет — отказ, а не создание: TUNSETIFF по отсутствующему имени
// создал бы непостоянный tun под записью OpkgTunN мимо NDMS, и с выходом
// процесса он исчез бы под записью (0ba1, а списки в окне — 0767; R62).
// Persistent tun создаёт NDMS вместе с записью (ресурс ndms_iface раньше в
// цепочке); его нет — ждём следующего прогона, ошибка видна в состоянии.
func OpenTunFD(name string) (*os.File, error) {
	if _, err := netdev.Absent(name); err == nil {
		return nil, fmt.Errorf("tun %s отсутствует — создавать его мимо NDMS нельзя, ждём устройство записи", name)
	} else if !errors.Is(err, netdev.ErrPresent) {
		return nil, fmt.Errorf("tun %s: %w", name, err)
	}
	fd, err := openTunDev()
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
