package connections

import (
	"strings"
	"testing"
)

func TestParseConntrackLine_TCP(t *testing.T) {
	line := `ipv4     2 tcp      6 1187 ESTABLISHED src=192.168.1.15 dst=185.199.110.133 sport=49158 dport=443 packets=14 bytes=3389 src=185.199.110.133 dst=172.16.0.2 sport=443 dport=49158 packets=12 bytes=6182 [ASSURED] [FASTNAT] mark=268434092 nmark=0 sc=0 ifw=59 ifl=35 mac=b0:4a:b4:74:80:f8 slan attrs= use=2`
	conn, ok := parseConntrackLine(line)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if conn.Protocol != "tcp" {
		t.Errorf("protocol: got %q, want tcp", conn.Protocol)
	}
	if conn.Src != "192.168.1.15" {
		t.Errorf("src: got %q, want 192.168.1.15", conn.Src)
	}
	if conn.Dst != "185.199.110.133" {
		t.Errorf("dst: got %q, want 185.199.110.133", conn.Dst)
	}
	if conn.SrcPort != 49158 {
		t.Errorf("srcPort: got %d, want 49158", conn.SrcPort)
	}
	if conn.DstPort != 443 {
		t.Errorf("dstPort: got %d, want 443", conn.DstPort)
	}
	if conn.State != "ESTABLISHED" {
		t.Errorf("state: got %q, want ESTABLISHED", conn.State)
	}
	if conn.Packets != 26 {
		t.Errorf("packets: got %d, want 26", conn.Packets)
	}
	if conn.Bytes != 9571 {
		t.Errorf("bytes: got %d, want 9571", conn.Bytes)
	}
	if conn.ifw != 59 {
		t.Errorf("ifw: got %d, want 59", conn.ifw)
	}
	if conn.ClientMAC != "b0:4a:b4:74:80:f8" {
		t.Errorf("mac: got %q, want b0:4a:b4:74:80:f8", conn.ClientMAC)
	}
}

func TestParseConntrackLine_UDP(t *testing.T) {
	line := `ipv4     2 udp      17 98 src=198.51.100.207 dst=198.51.100.74 sport=53907 dport=53 packets=2 bytes=154 src=198.51.100.74 dst=198.51.100.207 sport=53 dport=53907 packets=2 bytes=331 [ASSURED] [FASTNAT] mark=0 nmark=256 sc=0 nomac swan no_if attrs= use=2`
	conn, ok := parseConntrackLine(line)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if conn.Protocol != "udp" {
		t.Errorf("protocol: got %q, want udp", conn.Protocol)
	}
	if conn.State != "" {
		t.Errorf("state: got %q, want empty (UDP has no state)", conn.State)
	}
	if conn.ifw != 0 {
		t.Errorf("ifw: got %d, want 0 (no_if)", conn.ifw)
	}
	if conn.ClientMAC != "" {
		t.Errorf("mac: got %q, want empty (nomac)", conn.ClientMAC)
	}
}

func TestParseConntrackLine_ICMP(t *testing.T) {
	line := `ipv4     2 icmp     1 src=172.16.0.2 dst=8.8.8.8 type=8 code=0 id=6184 packets=1 bytes=84 src=8.8.8.8 dst=172.16.0.2 type=0 code=0 id=0 packets=1 bytes=84 [FASTNAT] mark=0 nmark=0 sc=0 use=2`
	conn, ok := parseConntrackLine(line)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if conn.Protocol != "icmp" {
		t.Errorf("protocol: got %q, want icmp", conn.Protocol)
	}
	if conn.SrcPort != 0 || conn.DstPort != 0 {
		t.Errorf("ports: got %d/%d, want 0/0 for ICMP", conn.SrcPort, conn.DstPort)
	}
}

func TestParseConntrackLine_Loopback_Skipped(t *testing.T) {
	line := `ipv4     2 tcp      6 1186 ESTABLISHED src=127.0.0.1 dst=127.0.0.1 sport=55006 dport=79 packets=1338 bytes=142918 src=127.0.0.1 dst=127.0.0.1 sport=79 dport=55006 packets=930 bytes=1427210 [ASSURED] mark=0 nmark=0 sc=0 use=2`
	if _, ok := parseConntrackLine(line); ok {
		t.Error("expected skip for loopback connection")
	}
}

func TestParseConntrackLine_IPv6LoopbackSkipped_Legacy(t *testing.T) {
	line := `ipv6     10 tcp      6 299 ESTABLISHED src=::1 dst=::1 sport=1234 dport=80 packets=1 bytes=60 src=::1 dst=::1 sport=80 dport=1234 packets=1 bytes=60 mark=0 use=2`
	if _, ok := parseConntrackLine(line); ok {
		t.Error("expected skip for IPv6 connection")
	}
}

func TestParseConntrackLine_NewFields(t *testing.T) {
	line := `ipv4     2 tcp      6 1183 ESTABLISHED src=192.168.0.54 dst=198.51.100.232 sport=56939 dport=443 packets=10 bytes=2411 src=198.51.100.232 dst=203.0.113.72 sport=443 dport=56939 packets=10 bytes=1966 [ASSURED] [FASTNAT] mark=268434097 nmark=256 sc=0 ifw=32 ifl=28 mac=00:00:5e:00:53:01 slan attrs= use=2`
	c, ok := parseConntrackLine(line)
	if !ok {
		t.Fatal("expected line to parse")
	}
	if c.TTL != 1183 {
		t.Errorf("TTL = %d, want 1183", c.TTL)
	}
	if c.BytesOut != 2411 || c.BytesIn != 1966 {
		t.Errorf("BytesOut/In = %d/%d, want 2411/1966", c.BytesOut, c.BytesIn)
	}
	if c.Bytes != 2411+1966 {
		t.Errorf("Bytes = %d, want sum", c.Bytes)
	}
	if c.replyDst != "203.0.113.72" {
		t.Errorf("replyDst = %q, want 203.0.113.72", c.replyDst)
	}
	if c.mark != 268434097 {
		t.Errorf("mark = %d, want 268434097", c.mark)
	}
	if c.State != "ESTABLISHED" {
		t.Errorf("State = %q, want ESTABLISHED", c.State)
	}
}

func TestParseConntrackLine_IPv6Accepted(t *testing.T) {
	line := `ipv6     10 udp      17 8 src=fe80:0000:0000:0000:85b2:42a1:1ffb:996e dst=ff02:0000:0000:0000:0000:0000:0000:00fb sport=5353 dport=5353 packets=42 bytes=3654 [UNREPLIED] src=ff02:0000:0000:0000:0000:0000:0000:00fb dst=fe80:0000:0000:0000:85b2:42a1:1ffb:996e sport=5353 dport=5353 packets=0 bytes=0 mark=0 nmark=0 sc=0 ifl=28 mac=98:de:d0:0d:ab:c3 slan attrs= use=2`
	c, ok := parseConntrackLine(line)
	if !ok {
		t.Fatal("ipv6 line must parse now")
	}
	if c.Protocol != "udp" || c.TTL != 8 {
		t.Errorf("proto/ttl = %q/%d, want udp/8", c.Protocol, c.TTL)
	}
	// КРИТИЧНО: nf_conntrack печатает v6 развёрнуто, а rules-карта (object-group)
	// и карта локальных IP туннелей (net.IP.String()) — в сжатой RFC 5952 форме.
	// Без нормализации все v6-lookup'ы молча промахиваются.
	if c.Src != "fe80::85b2:42a1:1ffb:996e" {
		t.Errorf("Src = %q, want compressed fe80::85b2:42a1:1ffb:996e", c.Src)
	}
	if c.Dst != "ff02::fb" {
		t.Errorf("Dst = %q, want compressed ff02::fb", c.Dst)
	}
	if c.replyDst != "fe80::85b2:42a1:1ffb:996e" {
		t.Errorf("replyDst = %q, want compressed form", c.replyDst)
	}
}

func TestParseConntrackLine_IPv6LoopbackSkipped(t *testing.T) {
	line := `ipv6     10 tcp      6 100 ESTABLISHED src=0000:0000:0000:0000:0000:0000:0000:0001 dst=0000:0000:0000:0000:0000:0000:0000:0001 sport=1000 dport=2000 packets=1 bytes=10 src=0000:0000:0000:0000:0000:0000:0000:0001 dst=0000:0000:0000:0000:0000:0000:0000:0001 sport=2000 dport=1000 packets=1 bytes=10 mark=0 use=2`
	if _, ok := parseConntrackLine(line); ok {
		t.Error("::1 loopback pair must be skipped")
	}
}

func TestParseConntrack_MultiLine(t *testing.T) {
	data := `ipv4     2 tcp      6 1187 ESTABLISHED src=192.168.1.15 dst=185.199.110.133 sport=49158 dport=443 packets=14 bytes=3389 src=185.199.110.133 dst=172.16.0.2 sport=443 dport=49158 packets=12 bytes=6182 [ASSURED] [FASTNAT] mark=268434092 nmark=0 sc=0 ifw=59 ifl=35 mac=b0:4a:b4:74:80:f8 slan attrs= use=2
ipv4     2 tcp      6 1187 ESTABLISHED src=192.168.1.150 dst=198.51.100.179 sport=59460 dport=443 packets=317 bytes=17187 src=198.51.100.179 dst=198.51.100.207 sport=443 dport=59460 packets=318 bytes=49226 [ASSURED] [FASTNAT] mark=0 nmark=256 sc=0 ifw=36 ifl=35 mac=00:00:5e:00:53:02 slan attrs= use=3
ipv4     2 tcp      6 1186 ESTABLISHED src=127.0.0.1 dst=127.0.0.1 sport=55006 dport=79 packets=1338 bytes=142918 src=127.0.0.1 dst=127.0.0.1 sport=79 dport=55006 packets=930 bytes=1427210 [ASSURED] mark=0 nmark=0 sc=0 use=2`

	conns := parseConntrack(strings.NewReader(data))
	if len(conns) != 2 {
		t.Fatalf("expected 2 connections (loopback filtered), got %d", len(conns))
	}
	if conns[0].ifw != 59 {
		t.Errorf("conn[0] ifw: got %d, want 59", conns[0].ifw)
	}
	if conns[1].ifw != 36 {
		t.Errorf("conn[1] ifw: got %d, want 36", conns[1].ifw)
	}
}
