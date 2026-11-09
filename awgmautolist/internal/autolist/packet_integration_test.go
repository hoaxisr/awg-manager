package autolist

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/conntrackfeed"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/judge"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packetcap"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packetruntime"
)

var (
	packetTestClient = netip.MustParseAddr("192.168.31.10")
	packetTestServer = netip.MustParseAddr("203.0.113.77")
)

type packetFakeRead struct {
	raw []byte
	err error
}

type packetFakeSubscriber struct {
	iface string
	fd    int
	mu    sync.Mutex
	reads []packetFakeRead
	shut  bool
}

func (s *packetFakeSubscriber) FD() int { return s.fd }
func (s *packetFakeSubscriber) Read(yield func([]byte)) error {
	s.mu.Lock()
	if len(s.reads) == 0 {
		s.mu.Unlock()
		return syscall.EAGAIN
	}
	item := s.reads[0]
	s.reads = s.reads[1:]
	s.mu.Unlock()
	if len(item.raw) > 0 {
		yield(item.raw)
	}
	return item.err
}
func (s *packetFakeSubscriber) Close() error {
	s.mu.Lock()
	s.shut = true
	s.mu.Unlock()
	return nil
}

type packetFakeCapture struct {
	mu   sync.Mutex
	subs map[string]*packetFakeSubscriber
	wake chan struct{}
}

func newPacketFakeCapture() *packetFakeCapture {
	return &packetFakeCapture{subs: make(map[string]*packetFakeSubscriber), wake: make(chan struct{}, 1)}
}

func (f *packetFakeCapture) open(c packetcap.Config) (packetruntime.Subscriber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sub := &packetFakeSubscriber{iface: c.Interface, fd: 200 + len(f.subs)}
	f.subs[c.Interface] = sub
	return sub, nil
}

func (f *packetFakeCapture) inject(iface string, raw []byte) {
	f.mu.Lock()
	sub := f.subs[iface]
	f.mu.Unlock()
	if sub == nil {
		return
	}
	sub.mu.Lock()
	sub.reads = append(sub.reads, packetFakeRead{raw: append([]byte(nil), raw...)})
	sub.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *packetFakeCapture) poll(ctx context.Context, fds []int, _ time.Duration) ([]bool, error) {
	ready := make([]bool, len(fds))
	for {
		pending := false
		f.mu.Lock()
		byFD := make(map[int]*packetFakeSubscriber, len(f.subs))
		for _, sub := range f.subs {
			byFD[sub.fd] = sub
		}
		f.mu.Unlock()
		for i, fd := range fds {
			sub := byFD[fd]
			if sub == nil {
				continue
			}
			sub.mu.Lock()
			ready[i] = len(sub.reads) > 0
			sub.mu.Unlock()
			pending = pending || ready[i]
		}
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

func (f *packetFakeCapture) closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subs {
		sub.mu.Lock()
		shut := sub.shut
		sub.mu.Unlock()
		if !shut {
			return false
		}
	}
	return len(f.subs) == 1
}

type packetHarness struct {
	t      *testing.T
	awgm   *fakeAWGM
	eng    *Engine
	fake   *packetFakeCapture
	unix   atomic.Int64
	cancel context.CancelFunc
}

func newPacketHarness(t *testing.T, autoAdd bool) *packetHarness {
	t.Helper()
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{PacketEnabled: true, AutoAdd: autoAdd, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	h := &packetHarness{t: t, awgm: awgm, eng: eng, fake: newPacketFakeCapture()}
	workerCtx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go eng.packetCorrelationWorker(workerCtx)
	h.unix.Store(100)
	eng.now = func() time.Time { return time.Unix(h.unix.Load(), 0) }
	eng.obs.now = eng.now
	eng.packet = packetruntime.New(packetruntime.Config{
		Discover:          eng.rules.LANBridges,
		OnResult:          eng.processPacketResult,
		Now:               eng.now,
		ConntrackInterval: 5 * time.Millisecond,
		ProbePacket:       func() error { return nil },
		Open:              h.fake.open,
		Conntrack: conntrackfeed.SourceFunc(func(context.Context) ([]conntrackfeed.Entry, error) {
			return nil, nil
		}),
		Poll: h.fake.poll,
		NewJudge: func() *judge.Judge {
			return judge.NewWithParams(judge.Params{FailThreshold: 1})
		},
	})
	if err := eng.packet.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.stop)
	return h
}

func (h *packetHarness) stop() {
	h.t.Helper()
	cfg := h.eng.Config()
	cfg.PacketEnabled = false
	if err := h.eng.SetConfig(cfg); err != nil {
		h.t.Errorf("disable packet runtime: %v", err)
	}
	h.cancel()
}

func (h *packetHarness) advance(seconds int64) { h.unix.Add(seconds) }

func (h *packetHarness) flow(port uint16) packet.Flow {
	return packet.Flow{
		Src: packetTestClient, Dst: packetTestServer, SrcPort: port, DstPort: 443,
		Protocol: packet.ProtocolTCP,
	}
}

func (h *packetHarness) route(flow packet.Flow, direct bool) {
	h.t.Helper()
	h.awgm.resetConns()
	outbound, chains := "direct", []string{"direct"}
	if !direct {
		outbound, chains = "vpn-group", []string{"node1", "vpn-group"}
	}
	h.awgm.mu.Lock()
	h.awgm.conns = append(h.awgm.conns, map[string]any{
		"id": fmt.Sprintf("packet-%d", flow.SrcPort),
		"metadata": map[string]any{
			"network": "tcp", "sourceIP": flow.Src.String(), "sourcePort": fmt.Sprint(flow.SrcPort),
			"destinationIP": flow.Dst.String(), "destinationPort": fmt.Sprint(flow.DstPort),
			"host": "route.example",
		},
		"upload": 1000, "download": 1000, "start": time.Unix(h.unix.Load(), 0).UTC().Format(time.RFC3339),
		"outbound": outbound, "chains": chains,
	})
	h.awgm.mu.Unlock()
	h.eng.obs.PollConnections(context.Background())
}

func (h *packetHarness) dns(name string) {
	h.t.Helper()
	before := h.eng.packet.Snapshot().DNSAnswers
	h.fake.inject("br0", packetDNSUDP(name, packetTestServer))
	h.await(func() bool { return h.eng.packet.Snapshot().DNSAnswers == before+1 })
}

func (h *packetHarness) await(condition func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			h.t.Fatalf("packet condition did not become true; snapshot=%+v rows=%+v", h.eng.Snapshot(), h.eng.table.List())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *packetHarness) candidate(domain string) Candidate {
	h.t.Helper()
	rows := h.eng.table.List()
	if len(rows) != 1 || rows[0].Domain != domain {
		h.t.Fatalf("candidates = %+v, want only %s", rows, domain)
	}
	return rows[0]
}

func TestPacketInterfaceDiscoveryFiltersUpPrivateBridges(t *testing.T) {
	awgm := newFakeAWGM(t)
	client := NewRuleClient(awgm.srv.URL, "")
	interfaces, err := client.LANBridges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) != 1 || interfaces[0] != "br0" {
		t.Fatalf("LAN bridges = %v, want only up private br0", interfaces)
	}
}

func TestPacketResetDirectListedWaitsForBlockedProbeAndAutoAdds(t *testing.T) {
	h := newPacketHarness(t, true)
	flow := h.flow(50000)
	h.route(flow, true)
	h.dns("www.belenka.ru")
	hello := packetHelloTCP(flow, "www.belenka.ru", 1000, 40)
	h.fake.inject("br0", hello)
	h.fake.inject("br0", packetResetTCP(flow))
	h.await(func() bool {
		return len(h.eng.table.List()) == 1 && h.eng.Snapshot()["probesQueued"] == 1
	})
	row := h.candidate("belenka.ru")
	if row.Host != "www.belenka.ru" || row.IP != packetTestServer.String() || row.Why != WhyReset.String() || row.State != StateCandidate.String() {
		t.Fatalf("reset adapter lost candidate metadata: %+v", row)
	}
	if got := h.awgm.addedRules(); len(got) != 0 {
		t.Fatalf("autoAdd=true added a rule before blocked confirmation: %v", got)
	}

	task, ok := h.eng.nextProbe(true)
	if !ok || task.host != "www.belenka.ru" || task.domain != "belenka.ru" || task.ip != packetTestServer.String() || task.why != WhyReset || task.source != "packet" || task.probeOnly {
		t.Fatalf("listed RESET probe metadata = %+v, ok=%v", task, ok)
	}
	h.eng.table.MarkChecking(task.domain)
	if got := h.awgm.addedRules(); len(got) != 0 {
		t.Fatalf("starting a probe added a rule before its verdict: %v", got)
	}
	h.eng.completeProbe(task, ProbeResult{Verdict: VerdictBlocked, Why: WhyReset, TunnelProbed: true, TunnelVerdict: VerdictReachable}, h.eng.now())
	h.await(func() bool {
		return len(h.eng.table.List()) == 0 && len(h.awgm.addedRules()) == 1
	})
	if got := h.awgm.addedRules(); len(got) != 1 || got[0] != "belenka.ru" {
		t.Fatalf("blocked direct-listed auto-add = %v", got)
	}
	if history := snapshotHistory(t, h.eng); len(history) != 0 {
		t.Fatalf("auto-add не должен создавать историю/статус: %+v", history)
	}
}

func TestPacketHelloRetransmissionDirectListed(t *testing.T) {
	h := newPacketHarness(t, false)
	flow := h.flow(50001)
	h.route(flow, true)
	h.dns("retrans.example")
	raw := packetHelloTCP(flow, "retrans.example", 1000, 40)
	for range 4 {
		h.fake.inject("br0", raw)
	}
	h.await(func() bool { return len(h.eng.table.List()) == 1 })
	row := h.candidate("retrans.example")
	if row.Why != WhyNoAnswer.String() || row.State != StateCandidate.String() {
		t.Fatalf("HELLO retrans row = %+v", row)
	}
	if h.eng.Snapshot()["probesQueued"] != 1 {
		t.Fatal("listed HELLO was not queued for its first active probe")
	}
}

func TestPacketDirectCorrelationPendingAndNonDirect(t *testing.T) {
	t.Run("result before Clash waits then lists", func(t *testing.T) {
		h := newPacketHarness(t, false)
		flow := h.flow(50002)
		h.dns("pending.example")
		h.fake.inject("br0", packetHelloTCP(flow, "pending.example", 1000, 40))
		h.fake.inject("br0", packetResetTCP(flow))
		h.await(func() bool { return h.eng.Snapshot()["packetPending"] == 1 })
		if len(h.eng.table.List()) != 0 || h.eng.Snapshot()["probesQueued"] != 0 {
			t.Fatal("unknown route created a candidate or probe")
		}
		h.route(flow, true)
		h.eng.flushPendingPackets(context.Background())
		h.await(func() bool { return len(h.eng.table.List()) == 1 })
		h.candidate("pending.example")
	})

	t.Run("known non-direct and bypass never list", func(t *testing.T) {
		h := newPacketHarness(t, false)
		flow := h.flow(50003)
		h.route(flow, false)
		h.dns("routed.example")
		h.fake.inject("br0", packetHelloTCP(flow, "routed.example", 1000, 40))
		h.fake.inject("br0", packetResetTCP(flow))
		h.await(func() bool { return h.eng.Snapshot()["packetNonDirect"] == uint64(1) })
		if len(h.eng.table.List()) != 0 {
			t.Fatal("non-direct flow became candidate")
		}

		bypassFlow := h.flow(50004)
		h.route(bypassFlow, true)
		// No DNS hint: judge classifies this as bypass even with SNI.
		h.fake.inject("br0", packetHelloTCP(bypassFlow, "bypass.example", 1000, 40))
		h.fake.inject("br0", packetResetTCP(bypassFlow))
		h.advance(1)
		h.eng.flushPendingPackets(context.Background())
		if len(h.eng.table.List()) != 0 {
			t.Fatalf("bypass observation became candidate: %+v", h.eng.table.List())
		}
	})

	t.Run("unknown expires without active probe conversion", func(t *testing.T) {
		h := newPacketHarness(t, false)
		flow := h.flow(50005)
		h.dns("unknown.example")
		h.fake.inject("br0", packetHelloTCP(flow, "unknown.example", 1000, 40))
		h.fake.inject("br0", packetResetTCP(flow))
		h.await(func() bool { return h.eng.Snapshot()["packetPending"] == 1 })
		h.advance(int64(PacketCorrelationTTL/time.Second) + 1)
		h.eng.flushPendingPackets(context.Background())
		if len(h.eng.table.List()) != 0 || h.eng.Snapshot()["probesQueued"] != 0 {
			t.Fatal("expired unknown route was listed or probed")
		}
		if h.eng.Snapshot()["packetCorrelationExpired"] != uint64(1) {
			t.Fatal("expired correlation was not diagnosed")
		}
	})
}

func TestPacketSYNUsesCandidateProbeLifecycle(t *testing.T) {
	h := newPacketHarness(t, false)
	flow := h.flow(50006)
	h.route(flow, true)
	h.dns("www.syn.example")
	raw := packetTCPv4(flow, 999, 0, packet.TCPSYN, nil)
	h.fake.inject("br0", raw)
	h.await(func() bool { return h.eng.packet.Snapshot().Events == 1 })
	h.advance(2)
	h.fake.inject("br0", raw)
	h.await(func() bool { return h.eng.Snapshot()["probesQueued"] == 1 })
	row := h.candidate("syn.example")
	if row.Why != WhyNoConnect.String() || row.State != StateCandidate.String() {
		t.Fatalf("SYN candidate = %+v", row)
	}
	task, ok := h.eng.nextProbe(true)
	if !ok || task.host != "www.syn.example" || task.domain != "syn.example" || task.ip != flow.Dst.String() || task.probeOnly {
		t.Fatalf("SYN probe metadata = %+v, %v", task, ok)
	}
	h.eng.table.MarkChecking(task.domain)
	if row = h.candidate("syn.example"); row.State != StateChecking.String() {
		t.Fatalf("SYN did not enter CHECKING: %+v", row)
	}
	h.eng.completeProbe(task, ProbeResult{Verdict: VerdictUnknown}, h.eng.now())
	if row = h.candidate("syn.example"); row.State != StateCandidate.String() {
		t.Fatalf("unknown SYN probe lifecycle = %+v", row)
	}
}

func TestPacketFINStallNomination(t *testing.T) {
	h := newPacketHarness(t, false)
	h.dns("freeze.example")
	for i, port := range []uint16{50007, 50008} {
		flow := h.flow(port)
		h.route(flow, true)
		before := h.eng.packet.Snapshot().Events
		h.fake.inject("br0", packetHelloTCP(flow, "freeze.example", 1000, 1000))
		h.await(func() bool { return h.eng.packet.Snapshot().Events == before+1 })
		h.fake.inject("br0", packetAnsweredTCP(flow))
		h.await(func() bool { return h.eng.packet.Snapshot().Events == before+2 })
		h.advance(11)
		h.fake.inject("br0", packetTCPv4(flow, 1001, 17384, packet.TCPFIN|packet.TCPACK, nil))
		h.await(func() bool { return h.eng.packet.Snapshot().Events == before+3 })
		if i == 0 {
			h.advance(1)
		}
	}
	h.await(func() bool { return h.eng.Snapshot()["probesQueued"] == 1 })
	row := h.candidate("freeze.example")
	if row.Why != WhyFreeze.String() {
		t.Fatalf("FIN nomination = %+v", row)
	}
}

func TestPacketDisableCleansUpAndDisabledFallback(t *testing.T) {
	h := newPacketHarness(t, false)
	if status := h.eng.Snapshot(); status["packetBackend"] != "af_packet" || status["packetRunning"] != true {
		t.Fatalf("AF_PACKET start status = %+v", status)
	}
	h.stop()
	if !h.fake.closed() {
		t.Fatal("config disable left AF_PACKET readers open")
	}
	status := h.eng.Snapshot()
	if status["packetRunning"] != false || status["packetConntrackRunning"] != false {
		t.Fatalf("capture/conntrack remained active after disable: %+v", status)
	}

	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	for range DefaultFailThreshold {
		awgm.pushDialLog("fallback.example", "443")
	}
	eng.tick(context.Background())
	rows := eng.table.List()
	if len(rows) != 1 || rows[0].Domain != "fallback.example" {
		t.Fatalf("disabled Clash/log fallback = %+v", rows)
	}
	if eng.Snapshot()["packetRunning"] != false {
		t.Fatal("disabled packet runtime started itself")
	}
}

func packetTCPv4(flow packet.Flow, seq, ack uint32, flags uint8, payload []byte) []byte {
	raw := make([]byte, 40+len(payload))
	raw[0] = 0x45
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	raw[8], raw[9] = 64, packet.ProtocolTCP
	src, dst := flow.Src.As4(), flow.Dst.As4()
	copy(raw[12:16], src[:])
	copy(raw[16:20], dst[:])
	binary.BigEndian.PutUint16(raw[20:22], flow.SrcPort)
	binary.BigEndian.PutUint16(raw[22:24], flow.DstPort)
	binary.BigEndian.PutUint32(raw[24:28], seq)
	binary.BigEndian.PutUint32(raw[28:32], ack)
	raw[32], raw[33] = 5<<4, flags
	copy(raw[40:], payload)
	return raw
}

func packetHelloTCP(flow packet.Flow, sni string, seq, ack uint32) []byte {
	return packetTCPv4(flow, seq, ack, packet.TCPACK|packet.TCPPSH, packetClientHello(sni))
}

func packetResetTCP(flow packet.Flow) []byte {
	return packetTCPv4(flow.Reverse(), 50, 1001, packet.TCPRST|packet.TCPACK, nil)
}

func packetAnsweredTCP(flow packet.Flow) []byte {
	// Cross the same 512-byte reply threshold as the former connbytes stream.
	return packetTCPv4(flow.Reverse(), 50, 1001, packet.TCPACK, make([]byte, 512))
}

func packetClientHello(name string) []byte {
	body := make([]byte, 0, 256)
	body = append(body, 0x03, 0x03)
	body = append(body, bytes.Repeat([]byte{0xab}, 32)...)
	body = append(body, 0, 0, 2, 0x13, 0x01, 1, 0)
	extensions := []byte{0, 0}
	extensions = binary.BigEndian.AppendUint16(extensions, uint16(len(name)+5))
	extensions = binary.BigEndian.AppendUint16(extensions, uint16(len(name)+3))
	extensions = append(extensions, 0)
	extensions = binary.BigEndian.AppendUint16(extensions, uint16(len(name)))
	extensions = append(extensions, name...)
	body = binary.BigEndian.AppendUint16(body, uint16(len(extensions)))
	body = append(body, extensions...)
	out := []byte{0x16, 0x03, 0x01}
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)+4))
	out = append(out, 0x01, 0)
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)))
	return append(out, body...)
}

func packetDNSUDP(name string, real netip.Addr) []byte {
	message := make([]byte, 12)
	binary.BigEndian.PutUint16(message[0:2], 7)
	binary.BigEndian.PutUint16(message[2:4], 0x8180)
	binary.BigEndian.PutUint16(message[4:6], 1)
	binary.BigEndian.PutUint16(message[6:8], 1)
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			message = append(message, byte(i-start))
			message = append(message, name[start:i]...)
			start = i + 1
		}
	}
	message = append(message, 0, 0, 1, 0, 1)
	message = append(message, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4)
	ip := real.As4()
	message = append(message, ip[:]...)

	raw := make([]byte, 28+len(message))
	raw[0], raw[8], raw[9] = 0x45, 64, packet.ProtocolUDP
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	dns, client := netip.MustParseAddr("1.1.1.1").As4(), packetTestClient.As4()
	copy(raw[12:16], dns[:])
	copy(raw[16:20], client[:])
	binary.BigEndian.PutUint16(raw[20:22], 53)
	binary.BigEndian.PutUint16(raw[22:24], 5353)
	binary.BigEndian.PutUint16(raw[24:26], uint16(8+len(message)))
	copy(raw[28:], message)
	return raw
}
