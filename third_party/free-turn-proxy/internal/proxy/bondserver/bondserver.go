// Package bondserver реализует серверную сторону bonded VLESS lane:
// одно backend TCP-соединение, мультиплексированное по N smux-потокам с общим
// ConnID. Wire-формат фреймов - internal/wire/bondframe; пакет соединяет
// copy-loop backend TCP ↔ lanes и реестр per-ConnID.
package bondserver

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/bondframe"
	"github.com/xtaci/smux"
)

// laneStream - подмножество *smux.Stream, нужное serverLane. Определено как
// интерфейс, чтобы юнит-тесты могли подменять in-memory pipe без смоделированной
// smux-сессии.
type laneStream interface {
	io.Reader
	io.Writer
	SetDeadline(time.Time) error
}

// Deps - зависимости хост-процесса для bond-сервера.
type Deps struct {
	Log logx.Logger
}

func (d *Deps) log() logx.Logger {
	if d.Log == nil {
		return logx.Nop()
	}
	return d.Log
}

// Registry дедуплицирует одновременные lane с одинаковым ConnID
// в одно backend TCP-соединение.
type Registry struct {
	deps Deps

	mu    sync.Mutex
	conns map[uint64]*serverConn
}

func NewRegistry(deps Deps) *Registry {
	return &Registry{deps: deps, conns: make(map[uint64]*serverConn)}
}

// HandleStreamAfterMagic принимает smux-поток, у которого первые 4 magic-байта
// уже прочитаны (server-side multiplex pre-peek), читает Hello, прикрепляет
// lane и блокируется до завершения bond-соединения.
func (r *Registry) HandleStreamAfterMagic(ctx context.Context, stream *smux.Stream, connectAddr string, magic [4]byte) {
	r.handleStream(ctx, stream, connectAddr, func(rd io.Reader) (bondframe.Hello, error) {
		return bondframe.ReadHelloAfterMagic(rd, magic)
	})
}

func (r *Registry) handleStream(ctx context.Context, stream *smux.Stream, connectAddr string, readHello func(io.Reader) (bondframe.Hello, error)) {
	defer func() {
		if err := stream.Close(); err != nil && !errors.Is(err, smux.ErrGoAway) {
			r.deps.log().Errorf("bondserver: close smux stream: %v", err)
		}
	}()

	hello, err := readHello(stream)
	if err != nil {
		r.deps.log().Errorf("bondserver: bond hello: %v", err)
		return
	}

	conn := r.get(ctx, hello.ConnID, connectAddr)
	conn.addLane(&serverLane{
		index:  hello.LaneIndex,
		stream: stream,
	}, hello.LaneCount)

	select {
	case <-ctx.Done():
	case <-conn.done:
	}
}

func (r *Registry) get(ctx context.Context, id uint64, connectAddr string) *serverConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.conns[id]; c != nil {
		return c
	}
	connCtx, cancel := context.WithCancel(ctx)
	c := &serverConn{
		deps:        &r.deps,
		id:          id,
		connectAddr: connectAddr,
		ctx:         connCtx,
		cancel:      cancel,
		done:        make(chan struct{}),
		ready:       make(chan struct{}, 1),
		recvCh:      make(chan bondframe.Frame, 1024),
	}
	r.conns[id] = c
	go func() {
		<-c.done
		r.mu.Lock()
		if r.conns[id] == c {
			delete(r.conns, id)
		}
		r.mu.Unlock()
	}()
	return c
}

type serverLane struct {
	index  uint16
	stream laneStream
	mu     sync.Mutex
}

type serverConn struct {
	deps        *Deps
	id          uint64
	connectAddr string
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}

	lanesMu sync.RWMutex
	lanes   []*serverLane
	want    uint16
	ready   chan struct{}

	recvCh chan bondframe.Frame
	once   sync.Once
}

func (c *serverConn) addLane(l *serverLane, laneCount uint16) {
	c.lanesMu.Lock()
	if laneCount > c.want {
		c.want = laneCount
	}
	c.lanes = append(c.lanes, l)
	count := len(c.lanes)
	c.lanesMu.Unlock()
	c.deps.log().Debugf("[bond %d] lane %d attached (lanes=%d)", c.id, l.index, count)
	select {
	case c.ready <- struct{}{}:
	default:
	}

	c.once.Do(func() {
		go c.run()
	})
	go c.readLane(l)
}

func (c *serverConn) snapshotLanes() []*serverLane {
	c.lanesMu.RLock()
	defer c.lanesMu.RUnlock()
	out := make([]*serverLane, len(c.lanes))
	copy(out, c.lanes)
	return out
}

func (c *serverConn) removeLane(l *serverLane) int {
	c.lanesMu.Lock()
	defer c.lanesMu.Unlock()
	for i, lane := range c.lanes {
		if lane == l {
			c.lanes = append(c.lanes[:i], c.lanes[i+1:]...)
			break
		}
	}
	return len(c.lanes)
}

func (c *serverConn) waitForInitialLanes() {
	timer := time.NewTimer(bondframe.LaneAttachTimeout)
	defer timer.Stop()
	for {
		c.lanesMu.RLock()
		count := len(c.lanes)
		want := int(c.want)
		c.lanesMu.RUnlock()
		if want <= 0 || count >= want {
			return
		}
		select {
		case <-c.ctx.Done():
			return
		case <-c.ready:
		case <-timer.C:
			c.deps.log().Debugf("[bond %d] starting with %d/%d lanes after attach timeout", c.id, count, want)
			return
		}
	}
}

func (c *serverConn) readLane(l *serverLane) {
	for {
		f, err := bondframe.ReadFrame(l.stream)
		if err != nil {
			left := c.removeLane(l)
			select {
			case <-c.ctx.Done():
			default:
				if !errors.Is(err, io.EOF) {
					c.deps.log().Debugf("[bond %d] lane %d read error: %v (lanes=%d)", c.id, l.index, err, left)
				}
				if left == 0 {
					c.cancel()
				}
			}
			return
		}
		select {
		case c.recvCh <- f:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *serverConn) run() {
	defer close(c.done)
	defer c.cancel()

	c.waitForInitialLanes()

	backendConn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(c.ctx, "tcp", c.connectAddr)
	if err != nil {
		c.deps.log().Errorf("[bond %d] backend dial: %s", c.id, err)
		return
	}
	defer func() {
		if err := backendConn.Close(); err != nil {
			c.deps.log().Errorf("[bond %d] close backend connection: %v", c.id, err)
		}
	}()
	context.AfterFunc(c.ctx, func() {
		now := time.Now()
		if err := backendConn.SetDeadline(now); err != nil {
			c.deps.log().Errorf("[bond %d] backend deadline: %v", c.id, err)
		}
		for _, lane := range c.snapshotLanes() {
			if err := lane.stream.SetDeadline(now); err != nil {
				c.deps.log().Errorf("[bond %d] lane %d deadline: %v", c.id, lane.index, err)
			}
		}
	})
	c.deps.log().Debugf("[bond %d] backend connected", c.id)

	var wg sync.WaitGroup
	wg.Go(func() {
		c.copyBondToBackend(backendConn)
	})
	wg.Go(func() {
		defer c.cancel()
		c.copyBackendToBond(backendConn)
	})
	wg.Wait()
}

func (c *serverConn) copyBondToBackend(backendConn net.Conn) {
	chunks := bondframe.Reorder(c.ctx, backendConn, c.recvCh, bondframe.ReorderHooks{
		OnOverflow: func(_ int) {
			c.deps.log().Errorf("[bond %d] pending map overflow (>%d), closing", c.id, bondframe.PendingCap)
		},
		OnUnknownType: func(typ byte) { c.deps.log().Errorf("[bond %d] unknown frame type %d", c.id, typ) },
		OnWriteError:  func(err error) { c.deps.log().Errorf("[bond %d] backend write error: %v", c.id, err) },
		OnCloseWrite:  c.deps.log().Debugf,
	})
	c.deps.log().Debugf("[bond %d] upload to backend finished chunks=%d", c.id, chunks)
}

func (c *serverConn) copyBackendToBond(backendConn net.Conn) {
	buf := make([]byte, bondframe.MaxChunk)
	var seq uint64
	var laneIdx uint64
	for {
		n, err := backendConn.Read(buf)
		if n > 0 {
			// writeToNextLane - синхронная запись (WriteFrame возвращается до
			// переключения lane), поэтому buf[:n] передаётся напрямую - аналог bondclient.
			if writeErr := c.writeToNextLane(seq, buf[:n], &laneIdx); writeErr != nil {
				c.deps.log().Errorf("[bond %d] lane write data error: %v", c.id, writeErr)
				return
			}
			seq++
		}
		if err != nil {
			lanes := c.snapshotLanes()
			for _, lane := range lanes {
				lane.mu.Lock()
				writeErr := bondframe.WriteFrame(lane.stream, bondframe.FrameFIN, seq, nil)
				lane.mu.Unlock()
				if writeErr != nil && c.ctx.Err() == nil {
					c.deps.log().Errorf("[bond %d] lane %d write FIN error: %v", c.id, lane.index, writeErr)
				}
			}
			c.deps.log().Debugf("[bond %d] download from backend finished chunks=%d", c.id, seq)
			return
		}
		select {
		case <-c.ctx.Done():
			return
		default:
		}
	}
}

func (c *serverConn) writeToNextLane(seq uint64, data []byte, laneIdx *uint64) error {
	lanes := c.snapshotLanes()
	for {
		if len(lanes) == 0 {
			select {
			case <-c.ctx.Done():
				return c.ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
			lanes = c.snapshotLanes()
			continue
		}
		written := false
		for range lanes {
			lane := lanes[*laneIdx%uint64(len(lanes))]
			*laneIdx++
			lane.mu.Lock()
			err := bondframe.WriteFrame(lane.stream, bondframe.FrameData, seq, data)
			lane.mu.Unlock()
			if err == nil {
				written = true
				break
			}
			left := c.removeLane(lane)
			c.deps.log().Errorf("[bond %d] lane %d write error: %v (lanes=%d)", c.id, lane.index, err, left)
			if left == 0 {
				c.cancel()
				return err
			}
		}
		if written {
			return nil
		}
		lanes = c.snapshotLanes()
	}
}
