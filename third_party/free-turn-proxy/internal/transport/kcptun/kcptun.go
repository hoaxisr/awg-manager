package kcptun

import (
	"net"
	"time"

	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// Profile - настраиваемые KCP-параметры. Обе стороны туннеля должны совпадать.
type Profile struct {
	NoDelay    int
	Interval   int
	Resend     int
	NC         int
	SndWnd     int
	RcvWnd     int
	MTU        int
	ACKNoDelay bool
}

// FEC управляет шардами KCP forward-error-correction. Нулевые значения отключают FEC.
type FEC struct {
	Data   int
	Parity int
}

// DefaultProfile - исторический balanced-профиль, поставляемый с прокси.
func DefaultProfile() Profile {
	return Profile{
		NoDelay:    1,
		Interval:   20,
		Resend:     2,
		NC:         1,
		SndWnd:     512,
		RcvWnd:     512,
		MTU:        1200,
		ACKNoDelay: true,
	}
}

// DtlsPacketConn оборачивает net.Conn (DTLS) как net.PacketConn для KCP.
// Каждый DTLS Read/Write сохраняет границы сообщений (datagram семантика).
type DtlsPacketConn struct {
	conn net.Conn
}

func NewDtlsPacketConn(conn net.Conn) *DtlsPacketConn {
	return &DtlsPacketConn{conn: conn}
}

func (d *DtlsPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := d.conn.Read(b)
	return n, d.conn.RemoteAddr(), err
}

func (d *DtlsPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return d.conn.Write(b)
}

func (d *DtlsPacketConn) Close() error {
	return d.conn.Close()
}

func (d *DtlsPacketConn) LocalAddr() net.Addr {
	return d.conn.LocalAddr()
}

func (d *DtlsPacketConn) SetDeadline(t time.Time) error {
	return d.conn.SetDeadline(t)
}

func (d *DtlsPacketConn) SetReadDeadline(t time.Time) error {
	return d.conn.SetReadDeadline(t)
}

func (d *DtlsPacketConn) SetWriteDeadline(t time.Time) error {
	return d.conn.SetWriteDeadline(t)
}

// NewKCPOverDTLS создаёт KCP-сессию поверх DTLS-соединения.
// isServer: true - серверная сторона (listener), false - клиентская (dialer).
func NewKCPOverDTLS(dtlsConn net.Conn, isServer bool, profile Profile, fec FEC) (*kcp.UDPSession, error) {
	pc := NewDtlsPacketConn(dtlsConn)

	block, err := kcp.NewNoneBlockCrypt(nil) // DTLS уже шифрует
	if err != nil {
		return nil, err
	}

	var sess *kcp.UDPSession

	if isServer {
		var listener *kcp.Listener
		listener, err = kcp.ServeConn(block, fec.Data, fec.Parity, pc)
		if err != nil {
			return nil, err
		}
		if err = listener.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
			_ = listener.Close()
			return nil, err
		}
		sess, err = listener.AcceptKCP()
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
	} else {
		sess, err = kcp.NewConn2(dtlsConn.RemoteAddr(), block, fec.Data, fec.Parity, pc)
		if err != nil {
			return nil, err
		}
	}

	sess.SetNoDelay(profile.NoDelay, profile.Interval, profile.Resend, profile.NC)
	sess.SetWindowSize(profile.SndWnd, profile.RcvWnd)
	sess.SetMtu(profile.MTU)
	sess.SetACKNoDelay(profile.ACKNoDelay)

	return sess, nil
}

// DefaultSmuxConfig возвращает smux-конфигурацию, настроенную под TURN-туннель.
func DefaultSmuxConfig() *smux.Config {
	cfg := smux.DefaultConfig()
	cfg.MaxReceiveBuffer = 4 * 1024 * 1024
	cfg.MaxStreamBuffer = 1 * 1024 * 1024
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.KeepAliveTimeout = 30 * time.Second
	return cfg
}
