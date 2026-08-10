//go:build linux

package wdtt

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func (s *Service) clientTunFdSockPath(id string) string {
	return s.clientProcs.sockPath(clientTunSockName(id))
}

func clientTunSockName(id string) string {
	if id == "" || id == DefaultInstanceID {
		return "wdtt-tun.sock"
	}
	return "wdtt-tun-" + id + ".sock"
}

// openTunFD открывает существующий opkgtunN без IFF_VNET_HDR (plain IP, как Android VPN fd).
func openTunFD(name string) (*os.File, error) {
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

// sendTunFD передаёт TUN fd клиенту через unix-socket (SCM_RIGHTS).
func sendTunFD(ctx context.Context, sockPath, iface string) error {
	tun, err := openTunFD(iface)
	if err != nil {
		return err
	}
	defer tun.Close()

	deadline := time.Now().Add(20 * time.Second)
	var conn net.Conn
	var dialErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, dialErr = net.DialTimeout("unix", sockPath, time.Second)
		if conn != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if conn == nil {
		return fmt.Errorf("dial %s: %w", sockPath, dialErr)
	}
	defer conn.Close()

	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("expected UnixConn")
	}
	rights := unix.UnixRights(int(tun.Fd()))
	_, _, err = uc.WriteMsgUnix([]byte{0}, rights, nil)
	if err != nil {
		return fmt.Errorf("WriteMsgUnix: %w", err)
	}
	return nil
}
