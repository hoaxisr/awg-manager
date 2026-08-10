package main

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

const defaultRawTunName = "wdtturn0"

// wireguard-go tun.virtioNetHdrLen on Linux (amd64/arm64).
const tunVirtioHdrLen = 10

// pendingPacketConn блокирует ReadFrom до Attach (TUN после RAWCONF).
type pendingPacketConn struct {
	mu   sync.Mutex
	real net.PacketConn
	ready chan struct{}
}

func newPendingPacketConn() *pendingPacketConn {
	return &pendingPacketConn{ready: make(chan struct{})}
}

func (p *pendingPacketConn) Attach(pc net.PacketConn) {
	p.mu.Lock()
	p.real = pc
	p.mu.Unlock()
	close(p.ready)
}

func (p *pendingPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	<-p.ready
	p.mu.Lock()
	pc := p.real
	p.mu.Unlock()
	return pc.ReadFrom(b)
}

func (p *pendingPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	p.mu.Lock()
	pc := p.real
	p.mu.Unlock()
	if pc == nil {
		return 0, fmt.Errorf("pending conn not attached")
	}
	return pc.WriteTo(b, addr)
}

func (p *pendingPacketConn) Close() error {
	p.mu.Lock()
	pc := p.real
	p.mu.Unlock()
	if pc != nil {
		return pc.Close()
	}
	return nil
}

func (p *pendingPacketConn) LocalAddr() net.Addr { return &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (p *pendingPacketConn) SetDeadline(t time.Time) error {
	p.mu.Lock()
	pc := p.real
	p.mu.Unlock()
	if pc == nil {
		return nil
	}
	return pc.SetDeadline(t)
}
func (p *pendingPacketConn) SetReadDeadline(t time.Time) error {
	p.mu.Lock()
	pc := p.real
	p.mu.Unlock()
	if pc == nil {
		return nil
	}
	return pc.SetReadDeadline(t)
}
func (p *pendingPacketConn) SetWriteDeadline(t time.Time) error {
	p.mu.Lock()
	pc := p.real
	p.mu.Unlock()
	if pc == nil {
		return nil
	}
	return pc.SetWriteDeadline(t)
}

type tunPacketConn struct {
	dev tun.Device
}

func newTunPacketConn(dev tun.Device, name string) *tunPacketConn {
	if dev.BatchSize() > 1 {
		log.Printf("[RAW] TUN %s: virtio hdr (BatchSize=%d)", name, dev.BatchSize())
	}
	return &tunPacketConn{dev: dev}
}

func (t *tunPacketConn) useVirtio() bool {
	return t.dev.BatchSize() > 1
}

func (t *tunPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	// CreateTUN включает IFF_VNET_HDR; Read() с offset=0 вернёт 10 байт virtio + IP.
	// Uplink (TUN→workers) тогда уходит на сервер с битым IPv4 → ~0 uplink при нормальном downlink.
	if t.useVirtio() {
		need := tunVirtioHdrLen + len(p)
		buf := getPktBuf(need)
		buf = buf[:need]
		bufs := [][]byte{buf}
		sizes := make([]int, 1)
		n, err := t.dev.Read(bufs, sizes, tunVirtioHdrLen)
		if err != nil || n == 0 || sizes[0] == 0 {
			putPktBuf(buf)
			return 0, nil, err
		}
		pktLen := sizes[0]
		if pktLen > len(p) {
			pktLen = len(p)
		}
		copy(p, buf[tunVirtioHdrLen:tunVirtioHdrLen+pktLen])
		putPktBuf(buf)
		return pktLen, &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}, nil
	}
	bufs := [][]byte{p}
	sizes := make([]int, 1)
	n, err := t.dev.Read(bufs, sizes, 0)
	if err != nil || n == 0 || sizes[0] == 0 {
		return 0, nil, err
	}
	return sizes[0], &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}, nil
}

func (t *tunPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// CreateTUN включает IFF_VNET_HDR; Write() требует offset >= tunVirtioHdrLen.
	// BatchSize() > 1 — признак vnetHdr в wireguard-go (см. tun_linux.go initFromFlags).
	if t.useVirtio() {
		buf := getPktBuf(tunVirtioHdrLen + len(p))
		copy(buf[tunVirtioHdrLen:], p)
		n, err := t.dev.Write([][]byte{buf[:tunVirtioHdrLen+len(p)]}, tunVirtioHdrLen)
		putPktBuf(buf)
		if err != nil {
			return 0, err
		}
		if n > tunVirtioHdrLen {
			return n - tunVirtioHdrLen, nil
		}
		return 0, nil
	}
	return t.dev.Write([][]byte{p}, 0)
}

func (t *tunPacketConn) Close() error { return t.dev.Close() }
func (t *tunPacketConn) LocalAddr() net.Addr {
	return &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}
}
func (t *tunPacketConn) SetDeadline(time.Time) error      { return nil }
func (t *tunPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (t *tunPacketConn) SetWriteDeadline(time.Time) error { return nil }

func resolvedTunName() string {
	if compatTunName != nil {
		if n := strings.TrimSpace(*compatTunName); n != "" {
			return n
		}
	}
	return defaultRawTunName
}

func startRawTUN(conf RawConf) (tun.Device, net.PacketConn, error) {
	name := resolvedTunName()
	_ = exec.Command("ip", "link", "del", name).Run()

	dev, err := tun.CreateTUN(name, conf.MTU)
	if err != nil {
		return nil, nil, fmt.Errorf("CreateTUN: %w", err)
	}
	iface, err := dev.Name()
	if err != nil {
		dev.Close()
		return nil, nil, err
	}
	cidr := conf.ClientIP + "/32"
	for _, args := range [][]string{
		{"ip", "addr", "add", cidr, "dev", iface},
		{"ip", "link", "set", "dev", iface, "mtu", fmt.Sprintf("%d", conf.MTU)},
		{"ip", "link", "set", iface, "up"},
	} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			if !strings.Contains(string(out), "File exists") {
				dev.Close()
				return nil, nil, fmt.Errorf("%s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
			}
		}
	}
	log.Printf("[RAW] TUN %s поднят (%s), MTU %d, DNS %s", iface, cidr, conf.MTU, conf.DNS)
	return dev, newTunPacketConn(dev, iface), nil
}
