// Package bondclient реализует клиентскую сторону bonded VLESS lane:
// одно принятое TCP-соединение, распределённое (round-robin) по всем активным
// smux-сессиям в tcpfwd.SessionPool. Wire-формат фреймов - internal/wire/bondframe;
// пакет соединяет copy-loop локального TCP ↔ lanes.
package bondclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/tcpfwd"
	"github.com/samosvalishe/free-turn-proxy/internal/stats"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/bondframe"
	"github.com/xtaci/smux"
)

// Deps - зависимости хост-процесса для bond-клиента.
type Deps struct {
	Log logx.Logger
}

func (d *Deps) log() logx.Logger {
	if d.Log == nil {
		return logx.Nop()
	}
	return d.Log
}

// Handler связывает Deps и предоставляет Handle под сигнатуру tcpfwd.BondHandler.
type Handler struct {
	Deps Deps
}

// lane - один smux-поток внутри bonded TCP-соединения.
type lane struct {
	ps     *tcpfwd.PooledSession
	stream *smux.Stream
	mu     sync.Mutex
	dead   atomic.Bool
}

// Handle распределяет локальное TCP-соединение по всем активным сессиям-кандидатам.
// Сигнатура соответствует tcpfwd.BondHandler.
func (h *Handler) Handle(ctx context.Context, tcpConn net.Conn, connID uint64, candidates []*tcpfwd.PooledSession) {
	defer func() { _ = tcpConn.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Фаза 1: открыть потоки на доступных сессиях. Фаза 2: отправить Hello с
	// реальным числом lane - bondserver.waitForInitialLanes не будет ждать
	// lane, которые не были открыты.
	type pending struct {
		ps     *tcpfwd.PooledSession
		stream *smux.Stream
	}
	opened := make([]pending, 0, len(candidates))
	for _, ps := range candidates {
		if ps.Sess.IsClosed() {
			continue
		}
		stream, err := ps.Sess.OpenStream()
		if err != nil {
			h.Deps.log().Errorf("[bond %d] session %d open stream error: %s", connID, ps.ID, err)
			continue
		}
		opened = append(opened, pending{ps: ps, stream: stream})
	}

	if len(opened) > math.MaxUint16 {
		opened = opened[:math.MaxUint16]
	}
	lanes := make([]*lane, 0, len(opened))
	laneIDs := make([]string, 0, len(opened))
	laneCount := uint16(len(opened)) //nolint:gosec // bounded above by MaxUint16
	for i, p := range opened {
		if err := bondframe.WriteHello(p.stream, connID, uint16(i), laneCount); err != nil { //nolint:gosec // i < laneCount <= MaxUint16
			h.Deps.log().Errorf("[bond %d] session %d hello error: %s", connID, p.ps.ID, err)
			_ = p.stream.Close()
			continue
		}
		p.ps.Opened.Add(1)
		p.ps.Active.Add(1)
		lanes = append(lanes, &lane{ps: p.ps, stream: p.stream})
		laneIDs = append(laneIDs, strconv.Itoa(p.ps.ID))
	}

	if len(lanes) == 0 {
		h.Deps.log().Errorf("[bond %d] no usable lanes, rejecting TCP from %s", connID, tcpConn.RemoteAddr())
		return
	}
	context.AfterFunc(ctx, func() {
		now := time.Now()
		if err := tcpConn.SetDeadline(now); err != nil {
			h.Deps.log().Debugf("[bond %d] local TCP deadline error: %v", connID, err)
		}
		for _, l := range lanes {
			if err := l.stream.SetDeadline(now); err != nil {
				h.Deps.log().Debugf("[bond %d] session %d stream deadline error: %v", connID, l.ps.ID, err)
			}
		}
	})

	h.Deps.log().Debugf("[bond %d] TCP accept from=%s lanes=%d [%s]", connID, tcpConn.RemoteAddr(), len(lanes), strings.Join(laneIDs, ","))
	defer func() {
		for _, l := range lanes {
			_ = l.stream.Close()
			active := l.ps.Active.Add(-1)
			closed := l.ps.Closed.Add(1)
			h.Deps.log().Debugf("[bond %d] lane session %d close active=%d closed=%d totals: to-session=%s from-session=%s",
				connID, l.ps.ID, active, closed,
				stats.FormatByteCount(l.ps.ToSession.Load()), stats.FormatByteCount(l.ps.FromSession.Load()))
		}
	}()

	recvCh := make(chan bondframe.Frame, 1024)
	var readWG sync.WaitGroup
	for _, l := range lanes {
		readWG.Go(func() {
			for {
				f, err := bondframe.ReadFrame(l.stream)
				if err != nil {
					l.dead.Store(true)
					select {
					case <-ctx.Done():
					default:
						if !errors.Is(err, io.EOF) {
							h.Deps.log().Debugf("[bond %d] session %d read frame error: %v", connID, l.ps.ID, err)
						}
					}
					return
				}
				if f.Type == bondframe.FrameData {
					l.ps.FromSession.Add(uint64(len(f.Data)))
				}
				select {
				case recvCh <- f:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	go func() {
		readWG.Wait()
		close(recvCh)
	}()

	var wg sync.WaitGroup
	wg.Go(func() {
		h.copyTCPToBond(ctx, connID, tcpConn, lanes)
	})
	wg.Go(func() {
		h.copyBondToTCP(ctx, connID, tcpConn, recvCh)
		cancel()
	})
	wg.Wait()
}

func (h *Handler) copyTCPToBond(ctx context.Context, connID uint64, tcpConn net.Conn, lanes []*lane) {
	buf := make([]byte, bondframe.MaxChunk)
	var seq uint64
	var laneIdx uint64
	for {
		n, err := tcpConn.Read(buf)
		if n > 0 {
			l, writeErr := writeBondFrameToNextLane(ctx, lanes, bondframe.FrameData, seq, buf[:n], &laneIdx)
			if writeErr != nil {
				h.Deps.log().Errorf("[bond %d] write data error: %v", connID, writeErr)
				return
			}
			l.ps.ToSession.Add(uint64(n))
			seq++
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				h.Deps.log().Debugf("[bond %d] local TCP read finished with error: %v", connID, err)
			}
			for _, l := range lanes {
				if l.dead.Load() {
					continue
				}
				l.mu.Lock()
				writeErr := bondframe.WriteFrame(l.stream, bondframe.FrameFIN, seq, nil)
				l.mu.Unlock()
				if writeErr != nil && ctx.Err() == nil {
					h.Deps.log().Errorf("[bond %d] session %d write FIN error: %v", connID, l.ps.ID, writeErr)
				}
			}
			h.Deps.log().Debugf("[bond %d] upload finished chunks=%d", connID, seq)
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// writeBondFrameToNextLane пишет в следующий живой lane в порядке round-robin.
// В отличие от bondserver.writeToNextLane (который ждёт новые lane), набор
// lane клиента фиксирован на время жизни Handle - нечего ждать, fail fast.
func writeBondFrameToNextLane(ctx context.Context, lanes []*lane, typ byte, seq uint64, data []byte, laneIdx *uint64) (*lane, error) {
	for range lanes {
		idx := *laneIdx % uint64(len(lanes))
		*laneIdx++
		l := lanes[idx]
		if l.dead.Load() {
			continue
		}
		l.mu.Lock()
		err := bondframe.WriteFrame(l.stream, typ, seq, data)
		l.mu.Unlock()
		if err == nil {
			return l, nil
		}
		l.dead.Store(true)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("no live bond lanes")
}

func (h *Handler) copyBondToTCP(ctx context.Context, connID uint64, tcpConn net.Conn, recvCh <-chan bondframe.Frame) {
	chunks := bondframe.Reorder(ctx, tcpConn, recvCh, bondframe.ReorderHooks{
		OnOverflow: func(_ int) {
			h.Deps.log().Errorf("[bond %d] pending map overflow (>%d), closing", connID, bondframe.PendingCap)
		},
		OnUnknownType: func(typ byte) { h.Deps.log().Errorf("[bond %d] unknown frame type %d", connID, typ) },
		OnWriteError:  func(err error) { h.Deps.log().Errorf("[bond %d] local TCP write error: %v", connID, err) },
		OnCloseWrite:  h.Deps.log().Debugf,
	})
	h.Deps.log().Debugf("[bond %d] download finished chunks=%d", connID, chunks)
}
