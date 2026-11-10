package conntrackfeed

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
)

const testTCP = "ipv4 2 tcp 6 431999 ESTABLISHED src=192.168.1.10 dst=203.0.113.7 sport=51000 dport=443 packets=10 bytes=1000 src=203.0.113.7 dst=192.168.1.10 sport=443 dport=51000 packets=8 bytes=2048 [ASSURED] mark=0"

func TestParseOriginalTupleBytesAndState(t *testing.T) {
	entry, ok, err := ParseLine(testTCP)
	if err != nil || !ok {
		t.Fatalf("ParseLine = %v, %v", ok, err)
	}
	want := packet.Flow{
		Src: netip.MustParseAddr("192.168.1.10"), Dst: netip.MustParseAddr("203.0.113.7"),
		SrcPort: 51000, DstPort: 443, Protocol: packet.ProtocolTCP,
	}
	if entry.Flow != want || entry.State != "ESTABLISHED" || !entry.Assured || entry.Unreplied {
		t.Fatalf("flow/state = %+v", entry)
	}
	if entry.OriginalBytes != 1000 || entry.ReplyBytes != 2048 || entry.OriginalPackets != 10 || entry.ReplyPackets != 8 {
		t.Fatalf("directional counters = %+v", entry)
	}
	if entry.Flow.SrcPort == 443 || entry.Flow.Dst.String() == "192.168.1.10" {
		t.Fatal("reply tuple overwrote original direction")
	}

	entries, err := Parse([]byte(testTCP + "\n" +
		"ipv4 2 udp 17 29 src=192.168.1.10 dst=1.1.1.1 sport=5353 dport=53 packets=1 bytes=80 src=1.1.1.1 dst=192.168.1.10 sport=53 dport=5353 packets=1 bytes=120 [ASSURED]\n"))
	if err != nil || len(entries) != 2 || entries[1].Flow.Protocol != packet.ProtocolUDP {
		t.Fatalf("Parse UDP = %+v, %v", entries, err)
	}
	if _, _, err := ParseLine("ipv4 2 tcp 6 10 ESTABLISHED src=bad dst=203.0.113.7 sport=1 dport=443"); err == nil {
		t.Fatal("malformed TCP address was accepted")
	}
}

func TestProcSourceReadsConfiguredSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nf_conntrack")
	if err := os.WriteFile(path, []byte(testTCP+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := (ProcSource{Path: path}).Snapshot(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("ProcSource = %+v, %v", entries, err)
	}
}

func TestTrackerPruneIsAmortized(t *testing.T) {
	tracker := NewTracker(8, time.Minute)
	entry, _, err := ParseLine(testTCP)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	tracker.ApplySnapshot([]Entry{entry}, now)
	first := tracker.lastPrune
	tracker.ApplySnapshot([]Entry{entry}, now.Add(100*time.Millisecond))
	if !tracker.lastPrune.Equal(first) {
		t.Fatal("full conntrack prune repeated within one second")
	}
	tracker.ApplySnapshot([]Entry{entry}, now.Add(time.Second))
	if !tracker.lastPrune.After(first) {
		t.Fatal("conntrack prune did not run at the interval boundary")
	}
}

func TestTrackerNEWExpiryAndPacketByteCorrelation(t *testing.T) {
	tracker := NewTracker(2, 10*time.Second)
	now := time.Unix(100, 0)
	entry, _, err := ParseLine(testTCP)
	if err != nil {
		t.Fatal(err)
	}

	// AF_PACKET updates bytes/state but cannot announce NEW by itself.
	p := packet.Packet{
		Src: entry.Flow.Dst, Dst: entry.Flow.Src, SrcPort: 443, DstPort: entry.Flow.SrcPort,
		Protocol: packet.ProtocolTCP, Flags: packet.TCPACK, Payload: make([]byte, 512),
	}
	before, after := tracker.ObservePacket(p, now)
	if before.ReplyBytes != 0 || after.ReplyBytes != 512 || after.State != "ESTABLISHED" {
		t.Fatalf("packet tracking = before %+v after %+v", before, after)
	}

	fresh := tracker.ApplySnapshot([]Entry{entry}, now)
	if len(fresh) != 1 || fresh[0].ReplyBytes != 2048 {
		t.Fatalf("first conntrack NEW = %+v", fresh)
	}
	if again := tracker.ApplySnapshot([]Entry{entry}, now.Add(time.Second)); len(again) != 0 {
		t.Fatalf("duplicate NEW = %+v", again)
	}
	lookup, ok := tracker.Lookup(entry.Flow, now.Add(10*time.Second))
	if !ok || lookup.State != "ESTABLISHED" || lookup.ReplyBytes != 2048 {
		t.Fatalf("lookup at TTL = %+v, %v", lookup, ok)
	}

	// Once the flow has expired from recent tracking, a later snapshot is NEW.
	if _, ok := tracker.Lookup(entry.Flow, now.Add(12*time.Second)); ok {
		t.Fatal("flow did not expire")
	}
	if fresh := tracker.ApplySnapshot([]Entry{entry}, now.Add(12*time.Second)); len(fresh) != 1 {
		t.Fatalf("post-expiry NEW = %+v", fresh)
	}
}
