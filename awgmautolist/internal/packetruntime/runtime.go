// Package packetruntime owns passive AF_PACKET capture, conntrack snapshots,
// and the single goroutine that mutates the packet judge. It has no firewall,
// route, NAT, MARK, or restoration-hook operations.
package packetruntime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/conntrackfeed"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/dnsfeed"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/judge"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packetbridge"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packetcap"
)

const (
	Backend                  = "af_packet"
	DefaultQueueCapacity     = 2048
	DefaultReadBurst         = 256
	DefaultPollInterval      = 250 * time.Millisecond
	DefaultConntrackInterval = 2 * time.Second
)

// Subscriber is the AF_PACKET reader seam used by Runtime and fake captures.
type Subscriber interface {
	FD() int
	Read(func([]byte)) error
	Close() error
}

// PollFunc waits for readable descriptors. The returned slice corresponds to
// fds. Implementations must block until activity, timeout, or context cancel.
type PollFunc func(ctx context.Context, fds []int, timeout time.Duration) ([]bool, error)

// Config contains operating-system seams. Production defaults are AF_PACKET
// and /proc/net/nf_conntrack; tests inject fake readers and snapshots.
type Config struct {
	Discover    func(context.Context) ([]string, error)
	OnResult    func(context.Context, packetbridge.Result)
	OnFlow      func(context.Context, conntrackfeed.Entry)
	OnFlowStart func(context.Context, packet.Flow) // SYN/HELLO: ранняя route-correlation
	Now         func() time.Time
	Verbose     func() bool
	Logf        func(string, ...any)

	QueueCapacity     int
	PollInterval      time.Duration
	ConntrackInterval time.Duration

	ProbePacket func() error
	Open        func(packetcap.Config) (Subscriber, error)
	Conntrack   conntrackfeed.Source
	Poll        PollFunc
	NewJudge    func() *judge.Judge
}

// Snapshot contains additive /status diagnostics. Running describes AF_PACKET
// only; conntrack can remain active as a fallback after a capture failure.
type Snapshot struct {
	Backend          string
	Running          bool
	ConntrackRunning bool
	LastError        string
	QueueDepth       int
	QueueDropped     uint64
	Events           uint64
	Malformed        uint64
	Lost             uint64
	ReadErrors       uint64
	DNSAnswers       uint64
	DNSErrors        uint64
	ConntrackFlows   uint64
	ConntrackErrors  uint64
}

type queueItem struct {
	raw         []byte
	at          time.Time
	snapshot    []conntrackfeed.Entry
	isConntrack bool
}

type boundSubscriber struct {
	iface string
	sub   Subscriber
}

type runState struct {
	ctx       context.Context
	cancel    context.CancelFunc
	queue     chan queueItem
	subs      []boundSubscriber
	bridge    *packetbridge.Bridge
	flows     *conntrackfeed.Tracker
	conntrack conntrackfeed.Source

	wg   sync.WaitGroup
	done chan struct{}
}

// Runtime serializes Start/Stop and owns at most one capture generation.
type Runtime struct {
	cfg Config
	ctl sync.Mutex

	mu              sync.Mutex
	current         *runState
	running         bool
	conntrackActive bool
	lastErr         string

	events          atomic.Uint64
	malformed       atomic.Uint64
	lost            atomic.Uint64
	readErrors      atomic.Uint64
	queueDropped    atomic.Uint64
	dnsAnswers      atomic.Uint64
	dnsErrors       atomic.Uint64
	conntrackFlows  atomic.Uint64
	conntrackErrors atomic.Uint64
}

func New(cfg Config) *Runtime {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = DefaultQueueCapacity
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.ConntrackInterval <= 0 {
		cfg.ConntrackInterval = DefaultConntrackInterval
	}
	if cfg.ProbePacket == nil {
		cfg.ProbePacket = packetcap.Probe
	}
	if cfg.Open == nil {
		cfg.Open = func(c packetcap.Config) (Subscriber, error) { return packetcap.Open(c) }
	}
	if cfg.Conntrack == nil {
		cfg.Conntrack = conntrackfeed.ProcSource{}
	}
	if cfg.Poll == nil {
		cfg.Poll = pollFDs
	}
	if cfg.NewJudge == nil {
		cfg.NewJudge = judge.New
	}
	return &Runtime{cfg: cfg}
}

// Start discovers LAN bridges and opens AF_PACKET readers. A capture failure
// closes partial readers and starts conntrack-only fallback; no failure path
// executes iptables, installs rules, or writes a restoration hook.
func (r *Runtime) Start(ctx context.Context) error {
	r.ctl.Lock()
	defer r.ctl.Unlock()

	r.mu.Lock()
	if r.current != nil {
		running := r.running
		r.mu.Unlock()
		if running {
			return nil
		}
		return errors.New("packetruntime: conntrack fallback is already active; disable before retry")
	}
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return r.startError(err)
	}

	var startErr error
	var interfaces []string
	if r.cfg.Discover == nil {
		startErr = errors.New("packetruntime: no interface discovery configured")
	} else {
		var err error
		interfaces, err = r.cfg.Discover(ctx)
		if err != nil {
			startErr = fmt.Errorf("discover LAN bridges: %w", err)
		} else if len(interfaces) == 0 {
			startErr = errors.New("packetruntime: no up private LAN bridges discovered")
		}
	}

	subs := make([]boundSubscriber, 0, len(interfaces))
	if startErr == nil {
		if err := r.cfg.ProbePacket(); err != nil {
			startErr = fmt.Errorf("AF_PACKET runtime probe: %w", err)
		}
	}
	if startErr == nil {
		seen := make(map[string]bool, len(interfaces))
		for _, iface := range interfaces {
			if seen[iface] {
				continue
			}
			seen[iface] = true
			sub, err := r.cfg.Open(packetcap.Config{Interface: iface})
			if err != nil {
				startErr = fmt.Errorf("open AF_PACKET on %s: %w", iface, err)
				break
			}
			subs = append(subs, boundSubscriber{iface: iface, sub: sub})
		}
	}
	if startErr != nil {
		for _, bound := range subs {
			_ = bound.sub.Close()
		}
		subs = nil
	}

	r.startState(ctx, subs, startErr)
	if startErr != nil {
		r.cfg.Logf("[autolist] AF_PACKET unavailable; using Clash/log/conntrack fallback: %v", startErr)
	}
	return startErr
}

func (r *Runtime) startState(ctx context.Context, subs []boundSubscriber, startErr error) {
	runCtx, cancel := context.WithCancel(ctx)
	state := &runState{
		ctx: runCtx, cancel: cancel, queue: make(chan queueItem, r.cfg.QueueCapacity),
		subs: subs, bridge: packetbridge.New(r.cfg.NewJudge(), packetbridge.Config{}),
		flows: conntrackfeed.NewTracker(0, 0), conntrack: r.cfg.Conntrack,
		done: make(chan struct{}),
	}
	r.mu.Lock()
	r.current = state
	r.running = len(subs) != 0
	r.conntrackActive = true
	if startErr == nil {
		r.lastErr = ""
	} else {
		r.lastErr = startErr.Error()
	}
	r.mu.Unlock()

	state.wg.Add(2) // judge owner and conntrack reader
	go r.own(state)
	go r.readConntrack(state)
	if len(subs) != 0 {
		state.wg.Add(1)
		go r.poll(state)
	}
	go r.supervise(state)
	if startErr == nil {
		r.debugf("AF_PACKET capture running on LAN interfaces %v", subs)
	}
}

func (r *Runtime) startError(err error) error {
	r.mu.Lock()
	r.running = false
	r.conntrackActive = false
	r.lastErr = err.Error()
	r.mu.Unlock()
	return err
}

// Stop cancels all readers and judging, then waits for socket cleanup. There
// are no firewall rules or hooks to remove.
func (r *Runtime) Stop(ctx context.Context) error {
	r.ctl.Lock()
	defer r.ctl.Unlock()
	r.mu.Lock()
	state := r.current
	r.mu.Unlock()
	if state == nil {
		return nil
	}
	state.cancel()
	select {
	case <-state.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) supervise(state *runState) {
	state.wg.Wait()
	for _, bound := range state.subs {
		_ = bound.sub.Close()
	}
	r.mu.Lock()
	if r.current == state {
		r.current = nil
		r.running = false
		r.conntrackActive = false
	}
	r.mu.Unlock()
	close(state.done)
}

// fail disables only AF_PACKET. The owner and conntrack goroutines remain
// active, preserving the non-firewall fallback until explicit Stop/disable.
func (r *Runtime) fail(state *runState, err error) {
	if err == nil || state.ctx.Err() != nil {
		return
	}
	r.readErrors.Add(1)
	for _, bound := range state.subs {
		_ = bound.sub.Close()
	}
	r.mu.Lock()
	r.running = false
	r.lastErr = err.Error()
	r.mu.Unlock()
	r.cfg.Logf("[autolist] AF_PACKET stopped; using Clash/log/conntrack fallback: %v", err)
}

func (r *Runtime) poll(state *runState) {
	defer state.wg.Done()
	fds := make([]int, len(state.subs))
	for i, bound := range state.subs {
		fds[i] = bound.sub.FD()
	}
	for state.ctx.Err() == nil {
		ready, err := r.cfg.Poll(state.ctx, fds, r.cfg.PollInterval)
		if err != nil {
			if state.ctx.Err() == nil {
				r.fail(state, fmt.Errorf("poll AF_PACKET: %w", err))
			}
			return
		}
		if len(ready) != len(state.subs) {
			r.fail(state, fmt.Errorf("poll AF_PACKET: got %d readiness flags for %d readers", len(ready), len(state.subs)))
			return
		}
		for i, bound := range state.subs {
			if !ready[i] || state.ctx.Err() != nil {
				continue
			}
			// readiness означает как минимум один кадр, но читать только его нельзя:
			// короткий DNS/TCP-всплеск иначе переполняет очередь между poll'ами.
			// Ограниченный бюджет не даёт одному интерфейсу голодать остальные.
			for range DefaultReadBurst {
				err := bound.sub.Read(func(raw []byte) {
					item := queueItem{raw: append([]byte(nil), raw...), at: r.cfg.Now()}
					select {
					case state.queue <- item:
					default:
						r.queueDropped.Add(1)
						r.debugf("packet queue full; dropping capture on %s", bound.iface)
					}
				})
				r.handleReadError(state, bound, err)
				if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
					break
				}
				if (err != nil && !r.Snapshot().Running) || state.ctx.Err() != nil {
					return
				}
			}
		}
	}
}

func (r *Runtime) handleReadError(state *runState, bound boundSubscriber, err error) {
	switch {
	case err == nil, errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, packetcap.ErrNotIP):
		return
	case errors.Is(err, packetcap.ErrTruncated), errors.Is(err, syscall.ENOBUFS):
		r.lost.Add(1)
		r.debugf("AF_PACKET %s loss/truncation: %v", bound.iface, err)
		return
	case errors.Is(err, packetcap.ErrMalformed):
		r.malformed.Add(1)
		r.debugf("AF_PACKET %s malformed frame: %v", bound.iface, err)
		return
	case errors.Is(err, net.ErrClosed) && state.ctx.Err() != nil:
		return
	default:
		r.fail(state, fmt.Errorf("read AF_PACKET %s: %w", bound.iface, err))
	}
}

func (r *Runtime) readConntrack(state *runState) {
	defer state.wg.Done()
	ticker := time.NewTicker(r.cfg.ConntrackInterval)
	defer ticker.Stop()
	for {
		entries, err := state.conntrack.Snapshot(state.ctx)
		if err != nil {
			if state.ctx.Err() == nil {
				r.conntrackErrors.Add(1)
				r.debugf("conntrack snapshot unavailable: %v", err)
			}
		} else {
			select {
			case state.queue <- queueItem{snapshot: entries, at: r.cfg.Now(), isConntrack: true}:
			default:
				r.queueDropped.Add(1)
			}
		}
		select {
		case <-state.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// own is the only goroutine that calls Judge, packetbridge, or flow tracking.
func (r *Runtime) own(state *runState) {
	defer state.wg.Done()
	for {
		select {
		case <-state.ctx.Done():
			return
		case item := <-state.queue:
			if item.isConntrack {
				r.processConntrack(state, item)
			} else {
				r.processPacket(state, item)
			}
		}
	}
}

func (r *Runtime) processConntrack(state *runState, item queueItem) {
	for _, entry := range state.flows.ApplySnapshot(item.snapshot, item.at) {
		r.conntrackFlows.Add(1)
		event := packet.Event{Kind: packet.NEW, Packet: packet.Packet{
			Src: entry.Flow.Src, Dst: entry.Flow.Dst, SrcPort: entry.Flow.SrcPort,
			DstPort: entry.Flow.DstPort, Protocol: entry.Flow.Protocol,
		}}
		result, accepted := state.bridge.Process(event, item.at)
		if accepted && result.First {
			if r.cfg.OnFlow != nil {
				r.cfg.OnFlow(state.ctx, entry)
			}
			if r.cfg.OnResult != nil {
				r.cfg.OnResult(state.ctx, result)
			}
		}
	}
}

func (r *Runtime) processPacket(state *runState, item queueItem) {
	head, ok := packet.ParseHead(item.raw)
	if !ok {
		r.malformed.Add(1)
		return
	}
	switch head.Protocol {
	case packet.ProtocolUDP:
		udp, ok := packet.ParseUDP(item.raw)
		if !ok {
			r.malformed.Add(1)
			return
		}
		state.flows.ObservePacket(udp, item.at)
		if udp.SrcPort == 53 {
			r.processDNS(state, item)
		}
		return
	case packet.ProtocolTCP:
		tcp, ok := packet.ParseTCP(item.raw)
		if !ok {
			r.malformed.Add(1)
			return
		}
		before, after := state.flows.ObservePacket(tcp, item.at)
		if tcp.SrcPort == 53 || tcp.DstPort == 53 {
			if tcp.SrcPort == 53 {
				r.processDNS(state, item)
			}
			return
		}
		if tcp.SrcPort != 443 && tcp.DstPort != 443 {
			return // kernel filter should make this unreachable; never guess NEW.
		}
		kind, ok := classifyTCP(tcp, before, after)
		if !ok {
			return
		}
		event, ok := packet.Parse(kind, item.raw)
		if !ok {
			r.malformed.Add(1)
			return
		}
		r.events.Add(1)
		r.debugf("event=%s flow=%s:%d -> %s:%d seq=%d ack=%d flags=%#x sni=%q",
			event.Kind, event.Src, event.SrcPort, event.Dst, event.DstPort, event.Seq, event.Ack, event.Flags, event.SNI)
		result, accepted := state.bridge.Process(event, item.at)
		if accepted && (event.Kind == packet.SYN || event.Kind == packet.HELLO) && r.cfg.OnFlowStart != nil {
			r.cfg.OnFlowStart(state.ctx, result.Flow)
		}
		if accepted && (result.Failure != nil || result.Nomination != nil) && r.cfg.OnResult != nil {
			r.cfg.OnResult(state.ctx, result)
		}
	default:
		return
	}
}

// classifyTCP uses immediate packet counters plus conntrack maxima. NEW is
// intentionally absent: only conntrack snapshots announce flow discovery.
func classifyTCP(p packet.Packet, before, after conntrackfeed.Entry) (packet.Kind, bool) {
	if p.DstPort == 443 {
		switch {
		case p.Flags&(packet.TCPSYN|packet.TCPACK) == packet.TCPSYN:
			return packet.SYN, true
		case len(p.Payload) > 5 && p.Payload[0] == 0x16 && p.Payload[5] == 0x01:
			return packet.HELLO, true
		case p.Flags&(packet.TCPRST|packet.TCPACK) == packet.TCPRST|packet.TCPACK:
			return packet.CLOSE_RST, true
		case p.Flags&packet.TCPFIN != 0:
			return packet.FIN, true
		}
	}
	if p.SrcPort == 443 {
		if p.Flags&packet.TCPRST != 0 {
			if after.ReplyBytes <= 255 {
				return packet.RESET, true
			}
			return packet.KindInvalid, false
		}
		if before.ReplyBytes < 512 && after.ReplyBytes >= 512 && after.ReplyBytes <= 4096 {
			return packet.ANSWERED, true
		}
	}
	return packet.KindInvalid, false
}

func (r *Runtime) processDNS(state *runState, item queueItem) {
	response, ok := packet.ParseDNSResponse(item.raw)
	if !ok {
		r.malformed.Add(1)
		r.debugf("malformed or incomplete DNS transport packet")
		return
	}
	var answer dnsfeed.Answer
	var err error
	if response.Protocol == packet.ProtocolUDP {
		answer, err = dnsfeed.ParseResponse(response.Payload)
	} else {
		answer, err = dnsfeed.ParseTCPResponse(response.Payload)
	}
	if err != nil {
		r.dnsErrors.Add(1)
		r.debugf("DNS response not used for hints: %v", err)
		return
	}
	r.debugf("DNS hint client=%s name=%q final=%q addresses=%v",
		response.Client, answer.QueriedName, answer.FinalName, answer.RealIPs)
	state.bridge.Answer(response.Client, answer, item.at)
	r.dnsAnswers.Add(1)
}

func (r *Runtime) debugf(format string, args ...any) {
	if r.cfg.Verbose != nil && r.cfg.Verbose() {
		r.cfg.Logf("[autolist:debug] "+format, args...)
	}
}

// Snapshot returns a consistent lifecycle state and lock-free counters.
func (r *Runtime) Snapshot() Snapshot {
	r.mu.Lock()
	out := Snapshot{
		Backend: Backend, Running: r.running, ConntrackRunning: r.conntrackActive,
		LastError: r.lastErr,
	}
	state := r.current
	r.mu.Unlock()
	if state != nil {
		out.QueueDepth = len(state.queue)
	}
	out.QueueDropped = r.queueDropped.Load()
	out.Events = r.events.Load()
	out.Malformed = r.malformed.Load()
	out.Lost = r.lost.Load()
	out.ReadErrors = r.readErrors.Load()
	out.DNSAnswers = r.dnsAnswers.Load()
	out.DNSErrors = r.dnsErrors.Load()
	out.ConntrackFlows = r.conntrackFlows.Load()
	out.ConntrackErrors = r.conntrackErrors.Load()
	return out
}
