package l3

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/p1neappleXpress/OpenFlux/utils"
)

// The Linux raw sockets deliver every packet the host receives. At -d the log
// must show the tunnel's packets only: the node's own ssh session, DNS answers
// and carrier connections would otherwise bury them (and, over ssh, feed on
// themselves).
func TestInboundLogShowsOnlyTunnelFlows(t *testing.T) {
	var logged bytes.Buffer
	utils.SetOutput(&logged)
	utils.SetLevel(utils.LevelPackets)
	t.Cleanup(func() {
		utils.SetLevel(utils.LevelOff)
		utils.SetOutput(os.Stderr)
	})

	n, _ := testNAT(t)
	b := &recordingBackend{}
	r := &recordingTransport{packets: make(chan []byte, 4)}
	ex := &L3Exit{backend: b, trans: r, ct: newConntrack(), udp: n}
	defer ex.Stop()
	egress := b.EgressIP()
	remote := [4]byte{203, 0, 113, 7}

	ex.handleFromTransport(tcpPacket(clientIPBytes, remote, 50000, 443, 0x02))
	ex.handleFromTransport(udpPacket(clientIPBytes, remote, 40000, 5353, []byte("q")))
	wire, ok := extractFlowKey(b.sent)
	if !ok {
		t.Fatal("UDP probe was not sent")
	}
	logged.Reset()

	// Host traffic: an ssh session, a DNS answer, a scan's SYN, an ICMP echo.
	ex.handleFromInternet(tcpPacket([4]byte{198, 51, 100, 9}, egress, 51234, 22, 0x10))
	ex.handleFromInternet(udpPacket([4]byte{8, 8, 8, 8}, egress, 53, 37293, []byte("answer")))
	ex.handleFromInternet(tcpPacket([4]byte{192, 0, 2, 99}, egress, 48642, 6989, 0x02))
	// The same remote, a port the tunnel never opened.
	ex.handleFromInternet(tcpPacket(remote, egress, 443, 50001, 0x12))
	if got := logged.String(); strings.Contains(got, "<-") {
		t.Fatalf("host traffic reached the packet log:\n%s", got)
	}
	if got := ex.dropNoConntrack.Load(); got != 4 {
		t.Fatalf("ignored packets counted = %d, want 4", got)
	}
	select {
	case p := <-r.packets:
		t.Fatalf("host traffic forwarded to the client: %x", p)
	default:
	}

	// The replies of the tunnel's own flows are logged once each, as they arrived.
	ex.handleFromInternet(tcpPacket(remote, egress, 443, 50000, 0x12))
	ex.handleFromInternet(udpPacket(remote, egress, 5353, wire.srcPort, []byte("a")))
	lines := 0
	for _, l := range strings.Split(logged.String(), "\n") {
		if strings.Contains(l, "<-") {
			lines++
			if !strings.Contains(l, "203.0.113.7:") || !strings.Contains(l, "192.0.2.10:") {
				t.Fatalf("logged line is not the pre-DNAT packet: %s", l)
			}
		}
	}
	if lines != 2 {
		t.Fatalf("tunnel replies logged = %d, want 2:\n%s", lines, logged.String())
	}
	if len(r.packets) != 2 {
		t.Fatalf("replies delivered to the client = %d, want 2", len(r.packets))
	}
}
