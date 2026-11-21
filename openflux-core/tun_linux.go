//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/p1neappleXpress/OpenFlux/network"
	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/tunnel"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

// TUNClient is a Linux TUN-based L3 forwarder: no gVisor, no SOCKS5.
// IP packets flow straight between the Linux TUN interface (e.g. opkgtun0 or openflux0)
// and the OpenFlux transport.
type TUNClient struct {
	trans        transport.Transport
	fd           *os.File
	name         string
	mtu          int
	tunAddr      string
	manageRoutes bool

	inbound chan []byte

	packetsIn  atomic.Uint64
	packetsOut atomic.Uint64
	closed     atomic.Bool

	gateway     string
	routesAdded bool
}

func openTunLinux(reqName string) (*os.File, string, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open /dev/net/tun: %w", err)
	}

	if reqName == "" {
		reqName = "opkgtun0"
	}

	ifr, err := unix.NewIfreq(reqName)
	if err != nil {
		unix.Close(fd)
		return nil, "", err
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		return nil, "", fmt.Errorf("TUNSETIFF %q: %w", reqName, err)
	}

	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, "", fmt.Errorf("set nonblock: %w", err)
	}

	actualName := ifr.Name()
	return os.NewFile(uintptr(fd), actualName), actualName, nil
}

func NewTUNClient(trans transport.Transport, mtu int) (*TUNClient, error) {
	reqName := strings.TrimSpace(tunNameFlag)
	if reqName == "" {
		reqName = "opkgtun0"
	}

	fd, name, err := openTunLinux(reqName)
	if err != nil {
		// Fallback to openflux0 if custom/opkgtun0 failed
		if reqName != "openflux0" {
			var fallbackErr error
			fd, name, fallbackErr = openTunLinux("openflux0")
			if fallbackErr == nil {
				err = nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("open linux tun (%s): %w", reqName, err)
		}
	}

	addr := strings.TrimSpace(tunAddrFlag)
	if addr == "" {
		addr = "10.10.10.2/24"
	}

	c := &TUNClient{
		trans:        trans,
		fd:           fd,
		name:         name,
		mtu:          mtu,
		tunAddr:      addr,
		manageRoutes: manageRoutesFlag,
		inbound:      make(chan []byte, 4096),
	}
	return c, nil
}

func (c *TUNClient) Name() string { return c.name }

func (c *TUNClient) Gateway() string { return c.gateway }

func (c *TUNClient) SaveDefault() error {
	if c.manageRoutes {
		out, err := exec.Command("ip", "route", "show", "default").Output()
		if err == nil {
			fields := strings.Fields(string(out))
			for i := 0; i < len(fields)-1; i++ {
				if fields[i] == "via" {
					c.gateway = fields[i+1]
					break
				}
			}
		}
		utils.Debugf("[TUN] saved default gateway: %s", c.gateway)
	}
	return nil
}

func (c *TUNClient) SetupInterface() error {
	mtuStr := fmt.Sprintf("%d", c.mtu)
	_ = exec.Command("ip", "link", "set", "dev", c.name, "mtu", mtuStr).Run()
	_ = exec.Command("ip", "link", "set", "dev", c.name, "up").Run()

	if c.tunAddr != "" {
		_ = exec.Command("ip", "addr", "add", c.tunAddr, "dev", c.name).Run()
	}

	utils.Debugf("[TUN] interface %s configured (mtu=%d, addr=%s)", c.name, c.mtu, c.tunAddr)
	return nil
}

func (c *TUNClient) ConfigureDefault() error {
	if !c.manageRoutes {
		utils.Debugf("[TUN] manageRoutes is disabled, skipping default route manipulation (routing handled by system)")
		return nil
	}

	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if out, err := exec.Command("ip", "route", "add", prefix, "dev", c.name).CombinedOutput(); err != nil {
			utils.Debugf("[TUN] route add %s dev %s failed: %v (%s)", prefix, c.name, err, string(out))
		}
	}
	c.routesAdded = true
	utils.Debugf("[TUN] default routes installed")
	return nil
}

func (c *TUNClient) removeRoutes() {
	if !c.routesAdded {
		return
	}
	_ = exec.Command("ip", "route", "del", "0.0.0.0/1", "dev", c.name).Run()
	_ = exec.Command("ip", "route", "del", "128.0.0.0/1", "dev", c.name).Run()
	c.routesAdded = false
}

func (c *TUNClient) RestoreDefault() {
	c.removeRoutes()
}

func (c *TUNClient) purgeStaleHostRoutes() {}

func (c *TUNClient) Start() {
	go c.readFromTun()
	go c.writeToTun()

	c.trans.Receive(func(pkt []byte) {
		if c.closed.Load() {
			return
		}
		cp := make([]byte, len(pkt))
		copy(cp, pkt)
		tunnel.ClampDeviceSyn(c.trans, cp)
		select {
		case c.inbound <- cp:
		default:
			utils.Debugf("[TUN] inbound queue full, dropping")
		}
	})
}

func (c *TUNClient) readFromTun() {
	utils.Debugf("[TUN] readFromTun started, fd=%v name=%s", c.fd.Fd(), c.name)
	buf := make([]byte, 65535)
	for !c.closed.Load() {
		n, err := c.fd.Read(buf)
		if err != nil {
			if !c.closed.Load() {
				utils.Debugf("[TUN] read: %v", err)
			}
			return
		}
		if n < 20 {
			continue
		}
		// In IFF_NO_PI mode on Linux, buf begins with IPv4 header directly
		if buf[0]>>4 != 4 {
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		c.packetsOut.Add(1)

		network.LogPacket("TUN", network.DirOutbound, pkt)

		if err := tunnel.SendFromDevice(c.trans, pkt); err != nil {
			utils.Debugf("[TUN] trans.Send FAIL: %v", err)
		}
	}
}

func (c *TUNClient) writeToTun() {
	for pkt := range c.inbound {
		if c.closed.Load() {
			return
		}
		c.packetsIn.Add(1)

		network.LogPacket("TUN", network.DirInbound, pkt)

		if _, err := c.fd.Write(pkt); err != nil {
			if !c.closed.Load() {
				utils.Debugf("[TUN] write: %v", err)
			}
			return
		}
	}
}

func (c *TUNClient) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.removeRoutes()
	close(c.inbound)
	return c.fd.Close()
}

type SocketWatcher struct {
	onStable func()
	once     sync.Once
}

func NewSocketWatcher(gateway string, onStable func()) *SocketWatcher {
	return &SocketWatcher{onStable: onStable}
}

func (w *SocketWatcher) Start(interval time.Duration) {
	go func() {
		time.Sleep(200 * time.Millisecond)
		w.once.Do(func() {
			if w.onStable != nil {
				w.onStable()
			}
		})
	}()
}

func (w *SocketWatcher) Stop() {}
