// SPDX-License-Identifier: MIT

package rtpopus

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	dtlsnet "github.com/pion/dtls/v3/pkg/net"
	pionudp "github.com/pion/transport/v4/udp"
)

// bufPool устраняет per-packet heap-аллокацию на горячих путях чтения/записи
// серверного wrapped PacketConn.
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 1600+Overhead)
		return &b
	},
}

// Listen возвращает dtls PacketListener, AEAD-оборачивающий каждый принятый PacketConn.
func Listen(addr *net.UDPAddr, key []byte) (dtlsnet.PacketListener, error) {
	state, err := NewState(key)
	if err != nil {
		return nil, err
	}
	inner, err := pionudp.Listen("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("rtpopus:udp listen: %w", err)
	}
	return &packetListener{
		inner: dtlsnet.PacketListenerFromListener(inner),
		state: state,
	}, nil
}

type packetListener struct {
	inner dtlsnet.PacketListener
	state *State
}

func (l *packetListener) Accept() (net.PacketConn, net.Addr, error) {
	pc, addr, err := l.inner.Accept()
	if err != nil {
		return pc, addr, err
	}
	conn, err := NewConnFromState(l.state, true)
	if err != nil {
		return nil, addr, err
	}
	return &packetConn{inner: pc, conn: conn}, addr, nil
}

func (l *packetListener) Close() error   { return l.inner.Close() }
func (l *packetListener) Addr() net.Addr { return l.inner.Addr() }

// packetConn - per-peer net.PacketConn, AEAD-оборачивающий чтение/запись
// через bufPool для allocation-free горячего пути.
type packetConn struct {
	inner net.PacketConn
	conn  *Conn
}

func (c *packetConn) ReadFrom(p []byte) (int, net.Addr, error) {
	bp := bufPool.Get().(*[]byte) //nolint:errcheck // pool New always returns *[]byte
	buf := *bp
	need := len(p) + Overhead
	if cap(buf) < need {
		buf = make([]byte, need)
		*bp = buf
	}
	defer bufPool.Put(bp)

	n, addr, err := c.inner.ReadFrom(buf[:cap(buf)])
	if err != nil {
		return 0, addr, err
	}
	wire := buf[:n]
	if len(wire) < Overhead {
		return 0, addr, errors.New("rtpopus:packet too short")
	}
	m, err := c.conn.Unwrap(wire, p)
	if err != nil {
		return 0, addr, err
	}
	return m, addr, nil
}

func (c *packetConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	wireLen := Overhead + len(p)

	bp := bufPool.Get().(*[]byte) //nolint:errcheck // pool New always returns *[]byte
	out := *bp
	if cap(out) < wireLen {
		out = make([]byte, wireLen)
		*bp = out
	}
	out = out[:wireLen]
	defer bufPool.Put(bp)

	n, err := c.conn.WrapInto(out, p)
	if err != nil {
		return 0, err
	}
	if _, err := c.inner.WriteTo(out[:n], addr); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *packetConn) Close() error                       { return c.inner.Close() }
func (c *packetConn) LocalAddr() net.Addr                { return c.inner.LocalAddr() }
func (c *packetConn) SetDeadline(t time.Time) error      { return c.inner.SetDeadline(t) }
func (c *packetConn) SetReadDeadline(t time.Time) error  { return c.inner.SetReadDeadline(t) }
func (c *packetConn) SetWriteDeadline(t time.Time) error { return c.inner.SetWriteDeadline(t) }
