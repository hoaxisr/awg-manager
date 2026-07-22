// Package shape реализует packet pacing для RTP-мимикрии: межпакетная задержка
// с jitter (±10%) и редкими burst'ами. Оборачивает net.PacketConn/PacketListener,
// вставляя sleep перед WriteTo; сами пакеты не меняются.
package shape

import (
	"net"
	"sync"
	"time"

	dtlsnet "github.com/pion/dtls/v3/pkg/net"

	"github.com/samosvalishe/free-turn-proxy/internal/randx"
)

const (
	jitterPct    = 0.10 // доля interval для равномерного jitter (±10%)
	burstMax     = 3    // макс. пакетов в одном burst
	burstPercent = 30   // шанс начать burst, %
)

// Shaper управляет межпакетной задержкой. Wait безопасен для конкурентных вызовов.
type Shaper struct {
	interval time.Duration

	mu       sync.Mutex
	lastSend time.Time
	burst    int // осталось пакетов в текущем burst (0 = не в burst)
}

// New создаёт Shaper; interval=0 отключает pacing.
func New(interval time.Duration) *Shaper {
	return &Shaper{interval: interval}
}

func (s *Shaper) Wait() {
	if s.interval <= 0 {
		return
	}

	s.mu.Lock()
	// Burst: пакеты внутри батча идут без задержки.
	if s.burst > 0 {
		s.burst--
		s.lastSend = time.Now()
		s.mu.Unlock()
		return
	}
	if randx.Intn(100) < burstPercent {
		s.burst = randx.Intn(burstMax) // 0..burstMax-1 оставшихся после текущего
		s.lastSend = time.Now()
		s.mu.Unlock()
		return
	}

	wait := s.interval - time.Since(s.lastSend)
	if wait > 0 {
		if jitter := time.Duration(float64(s.interval) * jitterPct); jitter > 0 {
			wait += time.Duration(randx.Intn(int(jitter)*2+1)) - jitter
		}
	}
	s.lastSend = time.Now().Add(max(wait, 0))
	s.mu.Unlock()

	if wait > 0 {
		time.Sleep(wait)
	}
}

// ShapedPacketConn применяет pacing к WriteTo обёрнутого net.PacketConn.
type ShapedPacketConn struct {
	net.PacketConn
	shaper *Shaper
}

func (s *ShapedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	s.shaper.Wait()
	return s.PacketConn.WriteTo(b, addr)
}

// WrapPacketConn оборачивает conn с межпакетной задержкой interval.
// interval=0 возвращает conn без обёртки (passthrough).
func WrapPacketConn(conn net.PacketConn, interval time.Duration) net.PacketConn {
	if interval <= 0 {
		return conn
	}
	return &ShapedPacketConn{PacketConn: conn, shaper: New(interval)}
}

// shapedPacketListener выдаёт каждому принятому PacketConn свой Shaper.
type shapedPacketListener struct {
	inner    dtlsnet.PacketListener
	interval time.Duration
}

func (l *shapedPacketListener) Accept() (net.PacketConn, net.Addr, error) {
	pc, addr, err := l.inner.Accept()
	if err != nil {
		return pc, addr, err
	}
	return &ShapedPacketConn{PacketConn: pc, shaper: New(l.interval)}, addr, nil
}

func (l *shapedPacketListener) Close() error   { return l.inner.Close() }
func (l *shapedPacketListener) Addr() net.Addr { return l.inner.Addr() }

// WrapPacketListener добавляет pacing к WriteTo каждого принятого PacketConn
// (server-side shaping). interval=0 возвращает оригинальный listener.
func WrapPacketListener(l dtlsnet.PacketListener, interval time.Duration) dtlsnet.PacketListener {
	if interval <= 0 {
		return l
	}
	return &shapedPacketListener{inner: l, interval: interval}
}
