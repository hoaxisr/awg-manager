package packetruntime

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/conntrackfeed"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/judge"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packetbridge"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packetcap"
)

type fakeRead struct {
	raw []byte
	err error
}

type fakeSubscriber struct {
	fd     int
	iface  string
	mu     sync.Mutex
	reads  []fakeRead
	closed bool
}

func (s *fakeSubscriber) FD() int { return s.fd }
func (s *fakeSubscriber) Read(yield func([]byte)) error {
	s.mu.Lock()
	if len(s.reads) == 0 {
		s.mu.Unlock()
		return syscall.EAGAIN
	}
	item := s.reads[0]
	s.reads = s.reads[1:]
	s.mu.Unlock()
	if len(item.raw) != 0 {
		yield(item.raw)
	}
	return item.err
}
func (s *fakeSubscriber) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}
func (s *fakeSubscriber) pending() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reads), s.closed
}

type fakeCapture struct {
	mu   sync.Mutex
	subs map[int]*fakeSubscriber
	wake chan struct{}
}

func newFakeCapture() *fakeCapture {
	return &fakeCapture{subs: make(map[int]*fakeSubscriber), wake: make(chan struct{}, 1)}
}

func (f *fakeCapture) open(cfg packetcap.Config) (*fakeSubscriber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fd := 100 + len(f.subs)
	sub := &fakeSubscriber{fd: fd, iface: cfg.Interface}
	f.subs[fd] = sub
	return sub, nil
}

func (f *fakeCapture) inject(iface string, raw []byte, err error) {
	f.mu.Lock()
	var sub *fakeSubscriber
	for _, candidate := range f.subs {
		if candidate.iface == iface {
			sub = candidate
			break
		}
	}
	f.mu.Unlock()
	if sub == nil {
		return
	}
	sub.mu.Lock()
	sub.reads = append(sub.reads, fakeRead{raw: append([]byte(nil), raw...), err: err})
	sub.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *fakeCapture) poll(ctx context.Context, fds []int, _ time.Duration) ([]bool, error) {
	ready := make([]bool, len(fds))
	for {
		pending := false
		f.mu.Lock()
		for i, fd := range fds {
			if sub := f.subs[fd]; sub != nil {
				n, _ := sub.pending()
				ready[i] = n > 0
				pending = pending || ready[i]
			}
		}
		f.mu.Unlock()
		if pending {
			return ready, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.wake:
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (f *fakeCapture) allClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subs {
		if _, closed := sub.pending(); !closed {
			return false
		}
	}
	return len(f.subs) != 0
}

type fakeConntrack struct {
	mu      sync.Mutex
	entries []conntrackfeed.Entry
	err     error
}

func (f *fakeConntrack) Snapshot(context.Context) ([]conntrackfeed.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]conntrackfeed.Entry(nil), f.entries...), f.err
}

func TestRuntimeAFPacketDNSConntrackDiagnosticsAndCleanup(t *testing.T) {
	fake := newFakeCapture()
	conntrack := &fakeConntrack{}
	var unix atomic.Int64
	unix.Store(100)
	results := make(chan packetbridge.Result, 4)
	starts := make(chan packet.Flow, 1)
	var judgeInstance *judge.Judge
	runtime := New(Config{
		Discover:          func(context.Context) ([]string, error) { return []string{"br0"}, nil },
		OnResult:          func(_ context.Context, result packetbridge.Result) { results <- result },
		OnFlowStart:       func(_ context.Context, flow packet.Flow) { starts <- flow },
		Now:               func() time.Time { return time.Unix(unix.Load(), 0) },
		ConntrackInterval: 5 * time.Millisecond,
		ProbePacket:       func() error { return nil },
		Open:              func(c packetcap.Config) (Subscriber, error) { return fake.open(c) },
		Conntrack:         conntrack,
		Poll:              fake.poll,
		NewJudge: func() *judge.Judge {
			judgeInstance = judge.NewWithParams(judge.Params{FailThreshold: 1})
			return judgeInstance
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if status := runtime.Snapshot(); status.Backend != "af_packet" || !status.Running || !status.ConntrackRunning {
		t.Fatalf("start status = %+v", status)
	}

	client := netip.MustParseAddr("192.168.1.10")
	server := netip.MustParseAddr("203.0.113.7")
	fake.inject("br0", udpDNSv4(t, "runtime.example", client, server), nil)
	waitFor(t, func() bool { return runtime.Snapshot().DNSAnswers == 1 })
	if !judgeInstance.HintedName(client, server, "runtime.example", time.Unix(unix.Load(), 0)) {
		t.Fatal("DNS answer did not create the expected judge hint")
	}

	syn := tcpv4(client, server, 51000, 443, 999, packet.TCPSYN, nil)
	fake.inject("br0", syn, nil)
	select {
	case flow := <-starts:
		if flow.Src != client || flow.Dst != server || flow.SrcPort != 51000 || flow.DstPort != 443 {
			t.Fatalf("early flow correlation = %+v", flow)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SYN did not request early route correlation")
	}
	waitFor(t, func() bool { return runtime.Snapshot().Events == 1 })
	unix.Store(102)
	fake.inject("br0", syn, nil)
	select {
	case result := <-results:
		if result.Nomination == nil || result.Nomination.Host != "runtime.example" || result.Nomination.Bypass {
			t.Fatalf("DNS/SYN result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SYN nomination did not reach the runtime owner")
	}

	fake.inject("br0", []byte{0xff}, nil)
	fake.inject("br0", nil, packetcap.ErrTruncated)
	waitFor(t, func() bool {
		s := runtime.Snapshot()
		return s.Malformed >= 1 && s.Lost >= 1 && s.Events >= 2
	})

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := runtime.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if !fake.allClosed() {
		t.Fatal("not every AF_PACKET reader was closed")
	}
	if status := runtime.Snapshot(); status.Running || status.ConntrackRunning {
		t.Fatalf("runtime still active after Stop: %+v", status)
	}
}

func TestRuntimeDrainsAllReadyFramesWithinReadBudget(t *testing.T) {
	client := netip.MustParseAddr("192.168.1.10")
	server := netip.MustParseAddr("203.0.113.7")
	sub := &fakeSubscriber{fd: 100, iface: "br0"}
	for range 3 {
		sub.reads = append(sub.reads, fakeRead{raw: tcpv4(client, server, 51000, 443, 999, packet.TCPSYN, nil)})
	}
	var polls atomic.Int32
	runtime := New(Config{
		Discover:    func(context.Context) ([]string, error) { return []string{"br0"}, nil },
		ProbePacket: func() error { return nil },
		Open:        func(packetcap.Config) (Subscriber, error) { return sub, nil },
		Conntrack:   &fakeConntrack{},
		Poll: func(ctx context.Context, _ []int, _ time.Duration) ([]bool, error) {
			if polls.Add(1) == 1 {
				return []bool{true}, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return runtime.Snapshot().Events == 3 })
	if pending, _ := sub.pending(); pending != 0 {
		t.Fatalf("one readiness left %d readable frames queued in the socket", pending)
	}
	if got := runtime.Snapshot().QueueDropped; got != 0 {
		t.Fatalf("small readable burst dropped %d frames", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeConntrackNEWDoesNotUseAFPackets(t *testing.T) {
	flow := packet.Flow{
		Src: netip.MustParseAddr("192.168.1.10"), Dst: netip.MustParseAddr("203.0.113.8"),
		SrcPort: 51001, DstPort: 443, Protocol: packet.ProtocolTCP,
	}
	conntrack := &fakeConntrack{entries: []conntrackfeed.Entry{{
		Flow: flow, State: "ESTABLISHED", OriginalBytes: 100, ReplyBytes: 200,
	}}}
	flows := make(chan conntrackfeed.Entry, 1)
	runtime := New(Config{
		Discover:          func(context.Context) ([]string, error) { return []string{"br0"}, nil },
		OnFlow:            func(_ context.Context, entry conntrackfeed.Entry) { flows <- entry },
		ConntrackInterval: 5 * time.Millisecond,
		ProbePacket:       func() error { return nil },
		Open: func(packetcap.Config) (Subscriber, error) {
			return newFakeCapture().open(packetcap.Config{Interface: "br0"})
		},
		Conntrack: conntrack,
		Poll:      newFakeCapture().poll, // no AF_PACKET event is ever injected
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case entry := <-flows:
		if entry.Flow != flow || entry.ReplyBytes != 200 {
			t.Fatalf("NEW entry = %+v", entry)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("conntrack NEW was not emitted")
	}
	if got := runtime.Snapshot().ConntrackFlows; got != 1 {
		t.Fatalf("conntrack NEW count = %d; duplicate snapshot announced", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeFailureUsesConntrackFallbackWithoutFirewall(t *testing.T) {
	t.Run("open failure", func(t *testing.T) {
		runtime := New(Config{
			Discover:    func(context.Context) ([]string, error) { return []string{"br0"}, nil },
			ProbePacket: func() error { return nil },
			Open:        func(packetcap.Config) (Subscriber, error) { return nil, errors.New("no CAP_NET_RAW") },
			Conntrack:   &fakeConntrack{},
		})
		if err := runtime.Start(context.Background()); err == nil {
			t.Fatal("open failure did not fail Start")
		}
		status := runtime.Snapshot()
		if status.Running || !status.ConntrackRunning || status.LastError == "" || status.Backend != Backend {
			t.Fatalf("fallback status = %+v", status)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := runtime.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("fatal read keeps conntrack active and closes sockets", func(t *testing.T) {
		fake := newFakeCapture()
		runtime := New(Config{
			Discover:    func(context.Context) ([]string, error) { return []string{"br0"}, nil },
			ProbePacket: func() error { return nil },
			Open:        func(c packetcap.Config) (Subscriber, error) { return fake.open(c) },
			Conntrack:   &fakeConntrack{}, Poll: fake.poll,
		})
		if err := runtime.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		fake.inject("br0", nil, errors.New("capture failed"))
		waitFor(t, func() bool {
			status := runtime.Snapshot()
			return !status.Running && status.ConntrackRunning && status.ReadErrors == 1
		})
		if !fake.allClosed() {
			t.Fatal("failed AF_PACKET socket remained open")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := runtime.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestClassifyTCPEarlyResetAndAnsweredBoundaries(t *testing.T) {
	client := netip.MustParseAddr("192.168.1.10")
	server := netip.MustParseAddr("203.0.113.7")
	reset := packet.Packet{
		Src: server, Dst: client, SrcPort: 443, DstPort: 51000,
		Protocol: packet.ProtocolTCP, Flags: packet.TCPRST | packet.TCPACK,
	}
	if kind, ok := classifyTCP(reset, conntrackfeed.Entry{}, conntrackfeed.Entry{}); !ok || kind != packet.RESET {
		t.Fatalf("early reset = %v, %v", kind, ok)
	}
	if _, ok := classifyTCP(reset, conntrackfeed.Entry{}, conntrackfeed.Entry{ReplyBytes: 256}); ok {
		t.Fatal("late reset entered early-reset judge strategy")
	}
	answered := reset
	answered.Flags = packet.TCPACK
	if kind, ok := classifyTCP(answered,
		conntrackfeed.Entry{ReplyBytes: 100}, conntrackfeed.Entry{ReplyBytes: 512}); !ok || kind != packet.ANSWERED {
		t.Fatalf("512-byte crossing = %v, %v", kind, ok)
	}
	if _, ok := classifyTCP(answered,
		conntrackfeed.Entry{ReplyBytes: 100}, conntrackfeed.Entry{ReplyBytes: 4097}); ok {
		t.Fatal("response above 4096 bytes entered answered strategy")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func tcpv4(src, dst netip.Addr, srcPort, dstPort uint16, seq uint32, flags uint8, payload []byte) []byte {
	raw := make([]byte, 40+len(payload))
	raw[0] = 0x45
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	raw[8], raw[9] = 64, packet.ProtocolTCP
	src4, dst4 := src.As4(), dst.As4()
	copy(raw[12:16], src4[:])
	copy(raw[16:20], dst4[:])
	binary.BigEndian.PutUint16(raw[20:22], srcPort)
	binary.BigEndian.PutUint16(raw[22:24], dstPort)
	binary.BigEndian.PutUint32(raw[24:28], seq)
	raw[32], raw[33] = 5<<4, flags
	copy(raw[40:], payload)
	return raw
}

func udpDNSv4(t *testing.T, name string, client, real netip.Addr) []byte {
	t.Helper()
	message := make([]byte, 12)
	binary.BigEndian.PutUint16(message[0:2], 42)
	binary.BigEndian.PutUint16(message[2:4], 0x8180)
	binary.BigEndian.PutUint16(message[4:6], 1)
	binary.BigEndian.PutUint16(message[6:8], 1)
	for _, label := range splitDNSName(name) {
		message = append(message, byte(len(label)))
		message = append(message, label...)
	}
	message = append(message, 0, 0, 1, 0, 1)
	message = append(message, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4)
	ip := real.As4()
	message = append(message, ip[:]...)

	raw := make([]byte, 28+len(message))
	raw[0], raw[8], raw[9] = 0x45, 64, packet.ProtocolUDP
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	dns, client4 := netip.MustParseAddr("1.1.1.1").As4(), client.As4()
	copy(raw[12:16], dns[:])
	copy(raw[16:20], client4[:])
	binary.BigEndian.PutUint16(raw[20:22], 53)
	binary.BigEndian.PutUint16(raw[22:24], 5353)
	binary.BigEndian.PutUint16(raw[24:26], uint16(8+len(message)))
	copy(raw[28:], message)
	return raw
}

func splitDNSName(name string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			out = append(out, name[start:i])
			start = i + 1
		}
	}
	return out
}
