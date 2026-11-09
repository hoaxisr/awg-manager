package packetbridge

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/dnsfeed"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/judge"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
)

var (
	testClient = netip.MustParseAddr("192.168.1.10")
	testServer = netip.MustParseAddr("203.0.113.7")
)

func at(sec int64) time.Time { return time.Unix(sec, 0) }

func testEvent(kind packet.Kind, clientPort uint16) packet.Event {
	return packet.Event{
		Kind: kind,
		Packet: packet.Packet{
			Src: testClient, Dst: testServer,
			SrcPort: clientPort, DstPort: 443,
			Protocol: packet.ProtocolTCP,
			Seq:      1000, Ack: 40,
		},
		SNI: "blocked.example",
	}
}

func reversed(event packet.Event) packet.Event {
	event.Packet = event.Packet.Reverse()
	return event
}

func hint(bridge *Bridge, client, server netip.Addr, name string, now time.Time) {
	bridge.Answer(client, dnsfeed.Answer{
		QueriedName: name,
		FinalName:   "final.example",
		RealIPs:     []netip.Addr{server},
	}, now)
}

func TestHelloFeedsSeqAckSNIAndExposesFailure(t *testing.T) {
	j := judge.New()
	var failures []judge.Failure
	var direct, coverage []Result
	bridge := New(j, Config{Hooks: Hooks{
		Failure:  func(f judge.Failure) { failures = append(failures, f) },
		Direct:   func(r Result) { direct = append(direct, r) },
		Coverage: func(r Result) { coverage = append(coverage, r) },
	}})
	hint(bridge, testClient, testServer, "blocked.example", at(99))
	event := testEvent(packet.HELLO, 50000)

	for i := 0; i < 3; i++ {
		result, accepted := bridge.Process(event, at(100+int64(i)))
		if !accepted || result.Failure != nil || result.Nomination != nil {
			t.Fatalf("HELLO %d produced an early result: %+v, %v", i+1, result, accepted)
		}
	}
	result, accepted := bridge.Process(event, at(103))
	if !accepted || result.Failure == nil {
		t.Fatalf("fourth HELLO did not fail: %+v, %v", result, accepted)
	}
	failure := *result.Failure
	if failure.Host != "blocked.example" || failure.Why != judge.WhyRetrans || failure.Bypass {
		t.Fatalf("wrong HELLO failure: %+v", failure)
	}
	if failure.Client != testClient || result.Flow.Dst != testServer {
		t.Fatalf("wrong failure endpoints: failure=%+v flow=%+v", failure, result.Flow)
	}
	if result.Flow != event.Flow() || result.Kind != packet.HELLO || result.First || result.Bypass != nil {
		t.Fatalf("wrong result metadata: %+v", result)
	}
	if len(failures) != 1 || len(direct) != 1 || len(coverage) != 1 {
		t.Fatalf("callback counts: failures=%d direct=%d coverage=%d", len(failures), len(direct), len(coverage))
	}
	if direct[0].Failure == nil || direct[0].Flow != event.Flow() || coverage[0].Kind != packet.HELLO {
		t.Fatalf("correlation did not receive the result: direct=%+v coverage=%+v", direct[0], coverage[0])
	}
}

func TestResetUsesReversedFlow(t *testing.T) {
	j := judge.New()
	bridge := New(j, Config{})
	hint(bridge, testClient, testServer, "reset.example", at(100))
	hello := testEvent(packet.HELLO, 50000)
	hello.SNI = "reset.example"
	bridge.Process(hello, at(100))

	reset := reversed(testEvent(packet.RESET, 50000))
	result, accepted := bridge.Process(reset, at(101))
	if !accepted || result.Failure == nil {
		t.Fatalf("reversed RESET did not judge the HELLO flow: %+v, %v", result, accepted)
	}
	if result.Failure.Why != judge.WhyReset || result.Failure.Host != "reset.example" {
		t.Fatalf("wrong reset failure: %+v", result.Failure)
	}
	if result.Flow != hello.Flow() {
		t.Fatalf("RESET flow=%+v, want client flow %+v", result.Flow, hello.Flow())
	}
	if _, accepted := bridge.Process(reset, at(102)); !accepted {
		t.Fatal("second RESET was not accepted")
	} else if result, _ := bridge.Process(reset, at(103)); result.Failure != nil {
		t.Fatalf("judged RESET emitted twice: %+v", result.Failure)
	}
}

func TestAnsweredUsesReversedFlowAndBlocksReset(t *testing.T) {
	j := judge.New()
	bridge := New(j, Config{})
	hint(bridge, testClient, testServer, "answered.example", at(100))
	hello := testEvent(packet.HELLO, 50000)
	hello.SNI = "answered.example"
	bridge.Process(hello, at(100))

	answered := reversed(testEvent(packet.ANSWERED, 50000))
	result, accepted := bridge.Process(answered, at(101))
	if !accepted || result.observable() || result.Flow != hello.Flow() {
		t.Fatalf("ANSWERED result=%+v, accepted=%v", result, accepted)
	}
	reset := reversed(testEvent(packet.RESET, 50000))
	result, accepted = bridge.Process(reset, at(102))
	if !accepted || result.Failure != nil {
		t.Fatalf("RESET after ANSWERED emitted: %+v, %v", result, accepted)
	}
}

func TestFINAndCloseRSTFeedCloseAck(t *testing.T) {
	j := judge.New()
	bridge := New(j, Config{})
	var nominations []judge.Nomination
	bridge.hooks.Nomination = func(n judge.Nomination) { nominations = append(nominations, n) }

	for i, kind := range []packet.Kind{packet.FIN, packet.CLOSE_RST} {
		start := int64(100 + 20*i)
		event := testEvent(packet.HELLO, uint16(50001+i))
		event.SNI = "freeze.example"
		event.Ack = 1000
		hint(bridge, testClient, testServer, event.SNI, at(start))
		bridge.Process(event, at(start))
		bridge.Process(reversed(testEvent(packet.ANSWERED, event.SrcPort)), at(start+1))

		closeEvent := testEvent(kind, event.SrcPort)
		closeEvent.Ack = event.Ack + 16384
		result, accepted := bridge.Process(closeEvent, at(start+11))
		if !accepted {
			t.Fatalf("%s was rejected", kind)
		}
		if i == 0 && result.Nomination != nil {
			t.Fatalf("first stall note nominated: %+v", result.Nomination)
		}
		if i == 1 {
			if result.Nomination == nil || result.Nomination.Kind != judge.NominationStall {
				t.Fatalf("second stall note did not nominate: %+v", result)
			}
			if result.Nomination.Host != "freeze.example" || result.Nomination.Received != 16384 {
				t.Fatalf("CLOSE_RST lost Close acknowledgement: %+v", result.Nomination)
			}
		}
	}
	if len(nominations) != 1 {
		t.Fatalf("nomination callbacks=%d, want 1", len(nominations))
	}
}

func TestSYNFeedsSequenceAndNomination(t *testing.T) {
	j := judge.NewWithParams(judge.Params{FailThreshold: 1})
	bridge := New(j, Config{})
	hint(bridge, testClient, testServer, "connect.example", at(99))
	event := testEvent(packet.SYN, 50000)
	event.Seq = 999

	if result, accepted := bridge.Process(event, at(100)); !accepted || result.Nomination != nil {
		t.Fatalf("original SYN result=%+v accepted=%v", result, accepted)
	}
	result, accepted := bridge.Process(event, at(102))
	if !accepted || result.Nomination == nil {
		t.Fatalf("SYN retransmission did not nominate: %+v, %v", result, accepted)
	}
	if result.Nomination.Kind != judge.NominationNoConnect || result.Nomination.Host != "connect.example" || result.Nomination.Bypass {
		t.Fatalf("wrong SYN nomination: %+v", result.Nomination)
	}
}

func TestNEWUsesFlowFirstAndPreservesFiveTuple(t *testing.T) {
	j := judge.New()
	var flows []packet.Flow
	var direct, coverage int
	bridge := New(j, Config{Hooks: Hooks{
		Flow: func(f packet.Flow) {
			flows = append(flows, f)
			if f.SrcPort != 51000 || f.DstPort != 3478 {
				t.Errorf("flow callback lost tuple: %+v", f)
			}
		},
		Direct:   func(Result) { direct++ },
		Coverage: func(Result) { coverage++ },
	}})
	event := testEvent(packet.NEW, 51000)
	event.Protocol = packet.ProtocolUDP
	event.DstPort = 3478

	result, accepted := bridge.Process(event, at(100))
	if !accepted || !result.First || result.Flow != event.Flow() {
		t.Fatalf("first NEW result=%+v accepted=%v", result, accepted)
	}
	result, accepted = bridge.Process(event, at(300))
	if !accepted || result.First {
		t.Fatalf("duplicate NEW result=%+v accepted=%v", result, accepted)
	}
	result, _ = bridge.Process(event, at(601))
	if !result.First {
		t.Fatal("NEW did not expose FlowFirst after its TTL")
	}
	if len(flows) != 2 || direct != 2 || coverage != 2 {
		t.Fatalf("NEW callbacks: flows=%d direct=%d coverage=%d, want 2 each", len(flows), direct, coverage)
	}
	for _, flow := range flows {
		if flow.Protocol != packet.ProtocolUDP {
			t.Errorf("UDP flow callback protocol=%d", flow.Protocol)
		}
	}

	tcp := event
	tcp.Protocol = packet.ProtocolTCP
	if result, _ := bridge.Process(tcp, at(402)); !result.First {
		t.Fatal("NEW protocol was not preserved in the FlowFirst key")
	}
	if len(flows) != 3 || flows[2].Protocol != packet.ProtocolTCP {
		t.Fatalf("TCP flow callback did not preserve protocol: %+v", flows)
	}
}

func TestLocalAndInvalidEventsAreRejected(t *testing.T) {
	localServer := netip.MustParseAddr("10.20.30.40")
	for kind := packet.HELLO; kind <= packet.NEW; kind++ {
		t.Run(kind.String(), func(t *testing.T) {
			bridge := New(judge.New(), Config{})
			event := testEvent(kind, 50000)
			event.Dst = localServer
			if kind == packet.NEW {
				event.Protocol = packet.ProtocolUDP
			}
			if kind == packet.RESET || kind == packet.ANSWERED {
				event = reversed(event)
			}
			if result, accepted := bridge.Process(event, at(100)); accepted || result.observable() {
				t.Fatalf("local destination accepted: %+v", result)
			}
		})
	}

	t.Run("configured pool", func(t *testing.T) {
		bridge := New(judge.New(), Config{LocalPools: []netip.Prefix{
			netip.MustParsePrefix("203.0.113.0/24"),
		}})
		if _, accepted := bridge.Process(testEvent(packet.SYN, 50000), at(100)); accepted {
			t.Fatal("destination in configured local pool was accepted")
		}
	})

	t.Run("local client is valid", func(t *testing.T) {
		bridge := New(judge.New(), Config{})
		event := testEvent(packet.NEW, 50000)
		if result, accepted := bridge.Process(event, at(100)); !accepted || !result.First {
			t.Fatalf("ordinary LAN client rejected: %+v, %v", result, accepted)
		}
	})

	t.Run("invalid synthetic events", func(t *testing.T) {
		bridge := New(judge.New(), Config{})
		invalid := testEvent(packet.KindInvalid, 50000)
		if _, accepted := bridge.Process(invalid, at(100)); accepted {
			t.Fatal("invalid kind accepted")
		}
		invalid = testEvent(packet.HELLO, 50000)
		invalid.Protocol = packet.ProtocolUDP
		if _, accepted := bridge.Process(invalid, at(100)); accepted {
			t.Fatal("UDP HELLO accepted")
		}
		invalid = testEvent(packet.NEW, 50000)
		invalid.Protocol = 1
		if _, accepted := bridge.Process(invalid, at(100)); accepted {
			t.Fatal("ICMP NEW accepted")
		}
		invalid = testEvent(packet.SYN, 50000)
		invalid.Src = netip.Addr{}
		if _, accepted := bridge.Process(invalid, at(100)); accepted {
			t.Fatal("invalid client address accepted")
		}
	})

	var nilBridge *Bridge
	if _, accepted := nilBridge.Process(testEvent(packet.SYN, 50000), at(100)); accepted {
		t.Fatal("nil bridge accepted an event")
	}
	nilBridge.Answer(testClient, dnsfeed.Answer{}, at(100))
	if _, accepted := New(nil, Config{}).Process(testEvent(packet.SYN, 50000), at(100)); accepted {
		t.Fatal("bridge without a judge accepted an event")
	}
}

func TestAnswerPreservesClientQueriedNameAndRealServers(t *testing.T) {
	j := judge.New()
	bridge := New(j, Config{})
	otherServer := netip.MustParseAddr("2001:db8::7")
	otherClient := netip.MustParseAddr("192.168.1.11")
	answer := dnsfeed.Answer{
		QueriedName: "queried.example",
		FinalName:   "final.example",
		RealIPs:     []netip.Addr{testServer, otherServer},
	}
	bridge.Answer(testClient, answer, at(100))

	for _, server := range answer.RealIPs {
		if !j.HintedName(testClient, server, answer.QueriedName, at(101)) {
			t.Errorf("queried name was not hinted for real server %v", server)
		}
		if j.HintedName(testClient, server, answer.FinalName, at(101)) {
			t.Errorf("final CNAME replaced queried name for %v", server)
		}
		if j.Hinted(otherClient, server, at(101)) {
			t.Errorf("hint crossed to another client for %v", server)
		}
	}
}

func TestBypassViewsForFailureAndNomination(t *testing.T) {
	t.Run("HELLO failure", func(t *testing.T) {
		bridge := New(judge.New(), Config{})
		var failures int
		var bypasses []Bypass
		bridge.hooks.Failure = func(judge.Failure) { failures++ }
		bridge.hooks.Bypass = func(b Bypass) { bypasses = append(bypasses, b) }
		event := testEvent(packet.HELLO, 50000)
		var result Result
		for i := 0; i < 4; i++ {
			result, _ = bridge.Process(event, at(100+int64(i)))
		}
		if result.Failure == nil || !result.Failure.Bypass || result.Bypass == nil {
			t.Fatalf("missing bypass failure view: %+v", result)
		}
		bypass := *result.Bypass
		if bypass.Host != event.SNI || bypass.Client != testClient || bypass.Server != testServer || bypass.Why != judge.WhyRetrans {
			t.Fatalf("wrong bypass failure view: %+v", bypass)
		}
		if failures != 1 || len(bypasses) != 1 || bypasses[0] != bypass {
			t.Fatalf("bypass callbacks: failures=%d bypasses=%+v", failures, bypasses)
		}
	})

	t.Run("SYN nomination", func(t *testing.T) {
		bridge := New(judge.NewWithParams(judge.Params{FailThreshold: 1}), Config{})
		var nominations int
		var bypasses []Bypass
		bridge.hooks.Nomination = func(judge.Nomination) { nominations++ }
		bridge.hooks.Bypass = func(b Bypass) { bypasses = append(bypasses, b) }
		event := testEvent(packet.SYN, 50000)
		bridge.Process(event, at(100))
		result, _ := bridge.Process(event, at(102))
		if result.Nomination == nil || !result.Nomination.Bypass || result.Bypass == nil {
			t.Fatalf("missing bypass nomination view: %+v", result)
		}
		if result.Bypass.Why != judge.WhyNoConnect || result.Bypass.Host != "" {
			t.Fatalf("wrong bypass nomination view: %+v", result.Bypass)
		}
		if nominations != 1 || len(bypasses) != 1 {
			t.Fatalf("bypass callbacks: nominations=%d bypasses=%d", nominations, len(bypasses))
		}
	})
}

func TestSyntheticRawPacketThroughPacketParser(t *testing.T) {
	j := judge.NewWithParams(judge.Params{FailThreshold: 1})
	bridge := New(j, Config{})
	hint(bridge, testClient, testServer, "raw.example", at(99))
	raw := syntheticTCPv4(testClient, testServer, 50000, 443, 777, packet.TCPSYN)
	event, ok := packet.Parse(packet.SYN, raw)
	if !ok {
		t.Fatal("synthetic SYN packet did not parse")
	}
	bridge.Process(event, at(100))
	result, accepted := bridge.Process(event, at(102))
	if !accepted || result.Nomination == nil || result.Nomination.Kind != judge.NominationNoConnect {
		t.Fatalf("raw packet bridge result=%+v accepted=%v", result, accepted)
	}
}

func syntheticTCPv4(src, dst netip.Addr, srcPort, dstPort uint16, seq uint32, flags uint8) []byte {
	raw := make([]byte, 40)
	raw[0] = 0x45
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	raw[8] = 64
	raw[9] = packet.ProtocolTCP
	src4, dst4 := src.As4(), dst.As4()
	copy(raw[12:16], src4[:])
	copy(raw[16:20], dst4[:])
	binary.BigEndian.PutUint16(raw[20:22], srcPort)
	binary.BigEndian.PutUint16(raw[22:24], dstPort)
	binary.BigEndian.PutUint32(raw[24:28], seq)
	raw[32] = 5 << 4
	raw[33] = flags
	return raw
}
