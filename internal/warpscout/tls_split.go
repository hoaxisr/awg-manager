package warpscout

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"time"
)

type splitHelloConn struct {
	net.Conn
	serverName string
	pause      time.Duration
	done       bool
}

// NewSplitTLSDialer оборачивает сетевой Dialer фрагментацией первого TLS-пакета
// (ClientHello) на два корректных TLS-фрейма со сплитом по середине SNI.
// Пробивает провайдерские ТСПУ, инспектирующие первый фрагмент TLS.
func NewSplitTLSDialer(baseDial func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := baseDial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		host, _, splitErr := net.SplitHostPort(addr)
		serverName := ""
		if splitErr == nil {
			host = strings.TrimSpace(host)
			if net.ParseIP(host) == nil {
				serverName = strings.ToLower(host)
			}
		}
		return &splitHelloConn{
			Conn:       conn,
			serverName: serverName,
			pause:      5 * time.Millisecond,
		}, nil
	}
}

func (c *splitHelloConn) Write(b []byte) (int, error) {
	if c.done {
		return c.Conn.Write(b)
	}
	c.done = true
	if len(b) > 5 && b[0] == 0x16 {
		recLen := int(binary.BigEndian.Uint16(b[3:5]))
		if recLen > 24 && len(b) >= 5+recLen && b[5] == 0x01 {
			if nd, ok := c.Conn.(interface{ SetNoDelay(bool) error }); ok {
				_ = nd.SetNoDelay(true)
			}
			p := b[5 : 5+recLen]
			tail := b[5+recLen:]

			cut := len(p) / 2
			if c.serverName != "" {
				if idx := bytes.Index(p, []byte(c.serverName)); idx >= 0 {
					cut = idx + len(c.serverName)/2
				}
			}
			if cut < 1 {
				cut = 1
			}
			if cut >= len(p) {
				cut = len(p) - 1
			}

			rec1 := make([]byte, 5+cut)
			rec1[0] = 0x16
			rec1[1] = b[1]
			rec1[2] = b[2]
			binary.BigEndian.PutUint16(rec1[3:5], uint16(cut))
			copy(rec1[5:], p[:cut])

			rem := len(p) - cut
			rec2 := make([]byte, 5+rem)
			rec2[0] = 0x16
			rec2[1] = b[1]
			rec2[2] = b[2]
			binary.BigEndian.PutUint16(rec2[3:5], uint16(rem))
			copy(rec2[5:], p[cut:])

			if err := writeAll(c.Conn, rec1); err != nil {
				return 0, err
			}
			if c.pause > 0 {
				time.Sleep(c.pause)
			}
			if err := writeAll(c.Conn, rec2); err != nil {
				return 0, err
			}
			if len(tail) > 0 {
				if err := writeAll(c.Conn, tail); err != nil {
					return 0, err
				}
			}
			return len(b), nil
		}
	}
	return c.Conn.Write(b)
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
