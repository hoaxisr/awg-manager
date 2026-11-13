package capture

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

func testClientHello(t *testing.T, name string) []byte {
	t.Helper()
	body := append([]byte{3, 3}, make([]byte, 32)...)
	body = append(body, 0, 0, 2, 0x13, 0x01, 1, 0)
	host := []byte(name)
	entry := append([]byte{0, byte(len(host) >> 8), byte(len(host))}, host...)
	list := append([]byte{byte(len(entry) >> 8), byte(len(entry))}, entry...)
	extension := append([]byte{0, 0, byte(len(list) >> 8), byte(len(list))}, list...)
	body = append(body, byte(len(extension)>>8), byte(len(extension)))
	body = append(body, extension...)
	handshake := []byte{1, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	handshake = append(handshake, body...)
	return append([]byte{0x16, 3, 1, byte(len(handshake) >> 8), byte(len(handshake))}, handshake...)
}

func observedTCP(t *testing.T, src, dst string, srcPort, dstPort uint16, flags uint8, sequence uint32, payload []byte) []byte {
	t.Helper()
	transport := tcpPacket(srcPort, dstPort, flags, payload)
	binary.BigEndian.PutUint32(transport[4:8], sequence)
	return ethernetFrame(0x0800, ipv4Packet(t, networkTCP, src, dst, transport))
}

func observedUDP(t *testing.T, src, dst string, srcPort, dstPort uint16, payload []byte) []byte {
	t.Helper()
	return ethernetFrame(0x0800, ipv4Packet(t, networkUDP, src, dst, udpPacket(srcPort, dstPort, payload)))
}

func stunRequest() []byte {
	request := make([]byte, 20)
	binary.BigEndian.PutUint16(request[0:2], 1)
	binary.BigEndian.PutUint32(request[4:8], 0x2112a442)
	return request
}

func TestFlowQueuesStallOnceAndIgnoresRetransmission(t *testing.T) {
	now := time.Unix(1000, 0)
	table := NewFlowTable(false)
	table.Handle(observedTCP(t, "192.168.31.50", "185.59.211.40", 51000, 443, tcpSYN, 99, nil), now)
	table.Handle(observedTCP(t, "192.168.31.50", "185.59.211.40", 51000, 443, tcpACK, 100, testClientHello(t, "belenka.eu")), now.Add(time.Second))
	response := observedTCP(t, "185.59.211.40", "192.168.31.50", 443, 51000, tcpACK, 5000, make([]byte, 9000))
	table.Handle(response, now.Add(2*time.Second))
	table.Handle(response, now.Add(2*time.Second))
	if got := table.flows[flowKey{network: networkTCP, client: mustIP("192.168.31.50"), server: mustIP("185.59.211.40"), clientPort: 51000, serverPort: 443}].serverBytes; got != 9000 {
		t.Fatalf("server bytes=%d", got)
	}
	first := table.Scan(now.Add(5 * time.Second))
	second := table.Scan(now.Add(6 * time.Second))
	if len(first) != 1 || len(second) != 0 {
		t.Fatalf("suspects=%v then %v", first, second)
	}
	if first[0].Target.Domain != "belenka.eu" || first[0].Reason != "response-stall" ||
		first[0].ClientIP != "192.168.31.50" || first[0].ClientMAC == "" {
		t.Fatalf("unexpected suspect: %+v", first[0])
	}
}

func TestTCPTriggerBoundaries(t *testing.T) {
	now := time.Unix(1000, 0)
	t.Run("SYN timeout", func(t *testing.T) {
		table := NewFlowTable(false)
		table.Handle(observedTCP(t, "192.168.31.50", "185.59.211.40", 51000, 443, tcpSYN, 99, nil), now)
		got := table.Scan(now.Add(3 * time.Second))
		if len(got) != 1 || got[0].Reason != "connect-no-response" {
			t.Fatalf("got=%v", got)
		}
	})
	t.Run("TLS timeout", func(t *testing.T) {
		table := NewFlowTable(false)
		table.Handle(observedTCP(t, "192.168.31.50", "185.59.211.40", 51000, 443, tcpACK, 100, testClientHello(t, "belenka.eu")), now)
		got := table.Scan(now.Add(3 * time.Second))
		if len(got) != 1 || got[0].Reason != "tls-no-response" {
			t.Fatalf("got=%v", got)
		}
	})
	t.Run("server RST", func(t *testing.T) {
		table := NewFlowTable(false)
		got := table.Handle(observedTCP(t, "185.59.211.40", "192.168.31.50", 443, 51000, tcpRST, 100, nil), now)
		if got == nil || got.Reason != "tcp-rst" {
			t.Fatalf("got=%v", got)
		}
	})
	t.Run("complete short FIN response", func(t *testing.T) {
		table := NewFlowTable(false)
		table.Handle(observedTCP(t, "192.168.31.50", "185.59.211.40", 51000, 443, tcpACK, 100, testClientHello(t, "belenka.eu")), now)
		table.Handle(observedTCP(t, "185.59.211.40", "192.168.31.50", 443, 51000, tcpACK|tcpFIN, 5000, make([]byte, 8000)), now)
		if got := table.Scan(now.Add(3 * time.Second)); len(got) != 0 {
			t.Fatalf("FIN response became suspect: %v", got)
		}
	})
	t.Run("over body limit", func(t *testing.T) {
		table := NewFlowTable(false)
		table.Handle(observedTCP(t, "192.168.31.50", "185.59.211.40", 51000, 443, tcpACK, 100, testClientHello(t, "belenka.eu")), now)
		table.Handle(observedTCP(t, "185.59.211.40", "192.168.31.50", 443, 51000, tcpACK, 5000, make([]byte, bodyLimit+1)), now)
		if got := table.Scan(now.Add(10 * time.Second)); len(got) != 0 {
			t.Fatalf("large response became suspect: %v", got)
		}
	})
}

func TestUDPAllPorts(t *testing.T) {
	now := time.Unix(1000, 0)
	t.Run("STUN no response", func(t *testing.T) {
		table := NewFlowTable(true)
		table.Handle(observedUDP(t, "192.168.31.50", "157.240.0.18", 53000, 45395, stunRequest()), now)
		got := table.Scan(now.Add(3 * time.Second))
		if len(got) != 1 || got[0].Target.Port != 45395 || got[0].Reason != "udp-no-response" {
			t.Fatalf("got=%v", got)
		}
	})
	t.Run("default off", func(t *testing.T) {
		table := NewFlowTable(false)
		table.Handle(observedUDP(t, "192.168.31.50", "157.240.0.18", 53000, 3478, stunRequest()), now)
		if got := table.Scan(now.Add(3 * time.Second)); len(got) != 0 || table.Stats().Flows != 0 {
			t.Fatalf("default captured UDP: %v %+v", got, table.Stats())
		}
	})
	t.Run("answer prevents suspect", func(t *testing.T) {
		table := NewFlowTable(true)
		table.Handle(observedUDP(t, "192.168.31.50", "157.240.0.18", 53000, 3478, stunRequest()), now)
		table.Handle(observedUDP(t, "157.240.0.18", "192.168.31.50", 3478, 53000, stunRequest()), now.Add(time.Second))
		if got := table.Scan(now.Add(4 * time.Second)); len(got) != 0 {
			t.Fatalf("answered UDP became suspect: %v", got)
		}
	})
	t.Run("unknown UDP never emitted", func(t *testing.T) {
		table := NewFlowTable(true)
		table.Handle(observedUDP(t, "192.168.31.50", "157.240.0.18", 53000, 45000, make([]byte, 32)), now)
		if got := table.Scan(now.Add(3 * time.Second)); len(got) != 0 {
			t.Fatalf("non-STUN became suspect: %v", got)
		}
	})
}

func TestLocalRouterDNSProvidesNameHint(t *testing.T) {
	now := time.Unix(1000, 0)
	table := NewFlowTable(false)
	dns := dnsResponse(t, "belenka.eu", []byte{185, 59, 211, 40}, 1)
	table.Handle(observedUDP(t, "192.168.31.1", "192.168.31.50", 53, 53000, dns), now)
	table.Handle(observedTCP(t, "192.168.31.50", "185.59.211.40", 51000, 443, tcpSYN, 99, nil), now)
	got := table.Scan(now.Add(3 * time.Second))
	if len(got) != 1 || got[0].Target.Domain != "belenka.eu" {
		t.Fatalf("local DNS hint was not used: %v", got)
	}
}

func TestLateDNSAnswerNamesFlowBeforeEmit(t *testing.T) {
	now := time.Unix(1000, 0)
	table := NewFlowTable(false)
	table.Handle(observedTCP(t, "192.168.31.50", "185.59.211.40", 51000, 443, tcpSYN, 99, nil), now)
	dns := dnsResponse(t, "belenka.eu", []byte{185, 59, 211, 40}, 1)
	table.Handle(observedUDP(t, "192.168.31.1", "192.168.31.50", 53, 53000, dns), now.Add(time.Second))
	got := table.Scan(now.Add(3 * time.Second))
	if len(got) != 1 || got[0].Target.Domain != "belenka.eu" {
		t.Fatalf("late DNS answer was not attached: %v", got)
	}
}

func TestDNSHintsAggregateOneRegisteredZone(t *testing.T) {
	now := time.Unix(1000, 0)
	table := NewFlowTable(false)
	for i, name := range []string{"www.belenka.eu", "belenka.eu"} {
		dns := dnsResponse(t, name, []byte{185, 59, 211, 40}, 1)
		table.Handle(observedUDP(t, "192.168.31.1", "192.168.31.50", 53, uint16(53000+i), dns), now)
	}
	table.Handle(observedTCP(t, "192.168.31.50", "185.59.211.40", 51000, 443, tcpSYN, 99, nil), now)
	got := table.Scan(now.Add(3 * time.Second))
	if len(got) != 1 || got[0].Target.Domain != "belenka.eu" {
		t.Fatalf("apex+www became ambiguous: %v", got)
	}
}

func TestDNSHintsRejectDifferentRegisteredZones(t *testing.T) {
	now := time.Unix(1000, 0)
	table := NewFlowTable(false)
	for i, name := range []string{"belenka.eu", "example.com"} {
		dns := dnsResponse(t, name, []byte{185, 59, 211, 40}, 1)
		table.Handle(observedUDP(t, "192.168.31.1", "192.168.31.50", 53, uint16(53000+i), dns), now)
	}
	table.Handle(observedTCP(t, "192.168.31.50", "185.59.211.40", 51000, 443, tcpSYN, 99, nil), now)
	got := table.Scan(now.Add(3 * time.Second))
	if len(got) != 1 || got[0].Target.Domain != "" {
		t.Fatalf("different zones produced a domain rule: %v", got)
	}
}

func TestDNSHintsAreIsolatedPerClient(t *testing.T) {
	now := time.Unix(1000, 0)
	table := NewFlowTable(false)
	for i, test := range []struct {
		client string
		name   string
	}{{"192.168.31.50", "belenka.eu"}, {"192.168.31.51", "example.com"}} {
		dns := dnsResponse(t, test.name, []byte{185, 59, 211, 40}, 1)
		table.Handle(observedUDP(t, "192.168.31.1", test.client, 53, uint16(53000+i), dns), now)
		table.Handle(observedTCP(t, test.client, "185.59.211.40", uint16(51000+i), 443, tcpSYN, 99, nil), now)
	}
	got := table.Scan(now.Add(3 * time.Second))
	if len(got) != 2 {
		t.Fatalf("suspects=%v", got)
	}
	domains := map[string]string{}
	for _, suspect := range got {
		domains[suspect.ClientIP] = suspect.Target.Domain
	}
	if domains["192.168.31.50"] != "belenka.eu" || domains["192.168.31.51"] != "example.com" {
		t.Fatalf("cross-client DNS contamination: %v", domains)
	}
}

func TestResetDropsAllPassiveState(t *testing.T) {
	now := time.Unix(1000, 0)
	table := NewFlowTable(true)
	table.Handle(observedUDP(t, "192.168.31.50", "157.240.0.18", 53000, 3478, stunRequest()), now)
	table.Reset()
	if got := table.Scan(now.Add(3 * time.Second)); len(got) != 0 || table.Stats().Flows != 0 || table.Stats().DNSIPs != 0 {
		t.Fatalf("state survived reset: %v %+v", got, table.Stats())
	}
}

func mustIP(raw string) (ip netip.Addr) { return netip.MustParseAddr(raw) }
