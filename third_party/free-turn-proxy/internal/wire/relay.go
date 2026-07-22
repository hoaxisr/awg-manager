package wire

import (
	"net"
	"sync"
	"time"
)

// relayBufPool устраняет per-packet heap-аллокацию на горячих путях
// RelayPacketConn. Дефолтный размер покрывает overhead любого профиля.
var relayBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 1600+128)
		return &b
	},
}

// RelayPacketConn оборачивает TURN relay PacketConn, направляя все записи
// фиксированному Peer. Если Codec non-nil, пакеты заворачиваются/разворачиваются
// wire-профилем (мимикрия). Заменяет прежний rtpopus.RelayPacketConn - теперь
// обобщён по Codec, работает с любым профилем.
type RelayPacketConn struct {
	Relay net.PacketConn
	Peer  net.Addr
	Codec Codec
}

func (r *RelayPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if r.Codec == nil {
		return r.Relay.ReadFrom(b)
	}
	bp := relayBufPool.Get().(*[]byte) //nolint:errcheck // pool New always returns *[]byte
	buf := *bp
	need := r.Codec.MaxWire(len(b))
	if cap(buf) < need {
		buf = make([]byte, need)
		*bp = buf
	}
	defer relayBufPool.Put(bp)

	n, addr, err := r.Relay.ReadFrom(buf[:cap(buf)])
	if err != nil {
		return 0, addr, err
	}
	m, err := r.Codec.Unwrap(buf[:n], b)
	if err != nil {
		return 0, addr, err
	}
	return m, addr, nil
}

func (r *RelayPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	if r.Codec == nil {
		return r.Relay.WriteTo(b, r.Peer)
	}
	wireLen := r.Codec.MaxWire(len(b))

	bp := relayBufPool.Get().(*[]byte) //nolint:errcheck // pool New always returns *[]byte
	out := *bp
	if cap(out) < wireLen {
		out = make([]byte, wireLen)
		*bp = out
	}
	out = out[:wireLen]
	defer relayBufPool.Put(bp)

	n, err := r.Codec.WrapInto(out, b)
	if err != nil {
		return 0, err
	}
	if _, err = r.Relay.WriteTo(out[:n], r.Peer); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (r *RelayPacketConn) Close() error                       { return r.Relay.Close() }
func (r *RelayPacketConn) LocalAddr() net.Addr                { return r.Relay.LocalAddr() }
func (r *RelayPacketConn) SetDeadline(t time.Time) error      { return r.Relay.SetDeadline(t) }
func (r *RelayPacketConn) SetReadDeadline(t time.Time) error  { return r.Relay.SetReadDeadline(t) }
func (r *RelayPacketConn) SetWriteDeadline(t time.Time) error { return r.Relay.SetWriteDeadline(t) }
