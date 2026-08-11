package main

import (
	"bytes"
	"context"
	"hash/fnv"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var pktPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 2048)
	},
}

func getPktBuf(size int) []byte {
	b := pktPool.Get().([]byte)
	if cap(b) < size {
		b = make([]byte, size)
	}
	return b[:size]
}

func putPktBuf(b []byte) {
	if cap(b) < 2048 {
		return
	}
	pktPool.Put(b[:cap(b)])
}

const (
	returnChBuf = 384

	// chunkSize — для vpn/socks (WireGuard): WG replay window переживает reorder между chunk'ами.
	chunkSize = 8
)

type WorkerSlot struct {
	ID     int
	SendCh chan []byte
}

type Dispatcher struct {
	localConn    net.PacketConn
	clientAddr   atomic.Pointer[net.Addr]
	mu           sync.Mutex
	workers      []*WorkerSlot
	rrIndex      int
	rrCount      int
	flowHash     bool // rawtun: привязка TCP/UDP-потока к одному relay (нет WG replay)
	ReturnCh     chan []byte
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	stats        *Stats
	firstPktUp   uint32
	firstPktDown uint32
}

func NewDispatcher(ctx context.Context, localConn net.PacketConn, stats *Stats) *Dispatcher {
	return newDispatcher(ctx, localConn, stats, false)
}

// NewRawTunDispatcher — uplink по 5-tuple hash: iperf/TCP не рвётся при N workers.
func NewRawTunDispatcher(ctx context.Context, localConn net.PacketConn, stats *Stats) *Dispatcher {
	return newDispatcher(ctx, localConn, stats, true)
}

func newDispatcher(ctx context.Context, localConn net.PacketConn, stats *Stats, flowHash bool) *Dispatcher {
	dctx, dcancel := context.WithCancel(ctx)
	d := &Dispatcher{
		localConn: localConn,
		flowHash:  flowHash,
		ReturnCh:  make(chan []byte, returnChBuf),
		ctx:       dctx,
		cancel:    dcancel,
		stats:     stats,
	}

	d.wg.Add(2)
	go d.readLoop()
	go d.writeLoop()
	return d
}

func (d *Dispatcher) Shutdown() {
	d.cancel()
	d.wg.Wait()
}

func (d *Dispatcher) Register(w *WorkerSlot) {
	d.mu.Lock()
	d.workers = append(d.workers, w)
	count := len(d.workers)
	d.mu.Unlock()
	log.Printf("[ДИСП] Воркер #%d зарегистрирован (всего: %d)", w.ID, count)
}

func (d *Dispatcher) Unregister(slot *WorkerSlot) {
	d.mu.Lock()
	for i, w := range d.workers {
		if w == slot {
			d.workers = append(d.workers[:i], d.workers[i+1:]...)
			break
		}
	}
	remaining := len(d.workers)
	if d.rrIndex >= remaining && remaining > 0 {
		d.rrIndex = d.rrIndex % remaining
	}
	d.rrCount = 0
	d.mu.Unlock()
	log.Printf("[ДИСП] Воркер #%d отключён (осталось: %d)", slot.ID, remaining)
}

// ipv4FlowHash — canonical 5-tuple (symmetric with server_raw pickDownlinkWorker).
func ipv4FlowHash(packet []byte) (uint32, bool) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return 0, false
	}
	ihl := int(packet[0]&0x0f) * 4
	if ihl < 20 || len(packet) < ihl+4 {
		return 0, false
	}
	proto := packet[9]
	if proto != 6 && proto != 17 {
		return 0, false
	}
	aIP := packet[12:16]
	bIP := packet[16:20]
	aPort := packet[ihl : ihl+2]
	bPort := packet[ihl+2 : ihl+4]
	if bytes.Compare(aIP, bIP) > 0 || (bytes.Equal(aIP, bIP) && bytes.Compare(aPort, bPort) > 0) {
		aIP, bIP = bIP, aIP
		aPort, bPort = bPort, aPort
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte{proto})
	_, _ = h.Write(aIP)
	_, _ = h.Write(aPort)
	_, _ = h.Write(bIP)
	_, _ = h.Write(bPort)
	return h.Sum32(), true
}

func (d *Dispatcher) readLoop() {
	defer d.wg.Done()

	buf := make([]byte, readBufSize)
	for {
		if err := d.ctx.Err(); err != nil {
			return
		}

		n, addr, err := d.localConn.ReadFrom(buf)
		if err != nil {
			if d.ctx.Err() != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}

		d.clientAddr.Store(&addr)
		d.stats.TotalBytesUp.Add(int64(n))

		if atomic.CompareAndSwapUint32(&d.firstPktUp, 0, 1) {
			log.Printf("[ДИСП] [ДЕБАГ] Получен ПЕРВЫЙ пакет от TUN (%d байт) с адреса %s", n, addr.String())
		}

		pkt := getPktBuf(n)
		copy(pkt, buf[:n])

		d.mu.Lock()
		nw := len(d.workers)
		if nw == 0 {
			d.mu.Unlock()
			putPktBuf(pkt)
			continue
		}

		if d.flowHash {
			if h, ok := ipv4FlowHash(pkt); ok {
				w := d.workers[int(h%uint32(nw))]
				d.mu.Unlock()
				select {
				case w.SendCh <- pkt:
				case <-d.ctx.Done():
					putPktBuf(pkt)
					return
				}
				continue
			}
		}

		sent := false
		idx := d.rrIndex % nw
		w := d.workers[idx]
		select {
		case w.SendCh <- pkt:
			sent = true
			d.rrCount++
			if d.rrCount >= chunkSize {
				d.rrIndex = (idx + 1) % nw
				d.rrCount = 0
			}
		default:
			for i := 1; i < nw; i++ {
				altIdx := (idx + i) % nw
				select {
				case d.workers[altIdx].SendCh <- pkt:
					sent = true
					d.rrIndex = altIdx
					d.rrCount = 1
				default:
				}
				if sent {
					break
				}
			}
		}

		if !sent {
			d.rrIndex = (idx + 1) % nw
			d.rrCount = 0
			putPktBuf(pkt)
		}
		d.mu.Unlock()
	}
}

func (d *Dispatcher) writeLoop() {
	defer d.wg.Done()

	for {
		select {
		case <-d.ctx.Done():
			return
		case pkt := <-d.ReturnCh:
			addrPtr := d.clientAddr.Load()
			if addrPtr == nil {
				putPktBuf(pkt)
				continue
			}
			addr := *addrPtr
			if atomic.CompareAndSwapUint32(&d.firstPktDown, 0, 1) {
				log.Printf("[ДИСП] [ДЕБАГ] Отправляем ПЕРВЫЙ пакет обратно в TUN (%d байт) на адрес %s", len(pkt), addr.String())
			}
			if _, err := d.localConn.WriteTo(pkt, addr); err != nil {
				if d.ctx.Err() != nil {
					putPktBuf(pkt)
					return
				}
			}
			d.stats.TotalBytesDown.Add(int64(len(pkt)))
			putPktBuf(pkt)
		}
	}
}
