package firc

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

func buildTestDNSResponse(req []byte, rcode uint16, answers [][]byte) []byte {
	resp := make([]byte, len(req))
	copy(resp, req)
	flags := uint16(0x8180) | (rcode & 0x0f)
	binary.BigEndian.PutUint16(resp[2:4], flags)
	binary.BigEndian.PutUint16(resp[6:8], uint16(len(answers)))
	for _, ans := range answers {
		resp = append(resp, ans...)
	}
	return resp
}

func makeARecordPtr(ip [4]byte) []byte {
	rr := make([]byte, 16)
	binary.BigEndian.PutUint16(rr[0:2], 0xc00c) // pointer to question QNAME at offset 12
	binary.BigEndian.PutUint16(rr[2:4], 1)      // A
	binary.BigEndian.PutUint16(rr[4:6], 1)      // IN
	binary.BigEndian.PutUint32(rr[6:10], 60)    // TTL
	binary.BigEndian.PutUint16(rr[10:12], 4)    // RDLENGTH
	copy(rr[12:16], ip[:])
	return rr
}

func makeCNAMEPtr(target string) []byte {
	var rdata []byte
	for _, lbl := range strings.Split(strings.TrimSuffix(target, "."), ".") {
		rdata = append(rdata, byte(len(lbl)))
		rdata = append(rdata, lbl...)
	}
	rdata = append(rdata, 0)

	rr := make([]byte, 12+len(rdata))
	binary.BigEndian.PutUint16(rr[0:2], 0xc00c)
	binary.BigEndian.PutUint16(rr[2:4], 5)   // CNAME
	binary.BigEndian.PutUint16(rr[4:6], 1)   // IN
	binary.BigEndian.PutUint32(rr[6:10], 60) // TTL
	binary.BigEndian.PutUint16(rr[10:12], uint16(len(rdata)))
	copy(rr[12:], rdata)
	return rr
}

func TestLookupFircDNSIPv4_SlowResponseCNAMEAndErrors(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 512)
		for {
			n, addr, rErr := pc.ReadFrom(buf)
			if rErr != nil {
				return
			}
			req := make([]byte, n)
			copy(req, buf[:n])
			go func(pkt []byte, client net.Addr) {
				// Parse queried name at offset 12.
				name, _ := decodeFircDNSName(pkt, 12)
				switch name {
				case "slow-tunnel.example.com":
					// Exceeds Keenetic's resolv.conf 1.0s cutoff deliberately.
					time.Sleep(1200 * time.Millisecond)
					resp := buildTestDNSResponse(pkt, 0, [][]byte{
						makeARecordPtr([4]byte{198, 18, 2, 10}),
					})
					_, _ = pc.WriteTo(resp, client)
				case "cname.example.com":
					cnameRR := makeCNAMEPtr("edge.example.net")
					aRR := makeARecordPtr([4]byte{93, 184, 216, 34})
					resp := buildTestDNSResponse(pkt, 0, [][]byte{cnameRR, aRR})
					_, _ = pc.WriteTo(resp, client)
				case "missing.example.com":
					resp := buildTestDNSResponse(pkt, 3, nil) // NXDOMAIN
					_, _ = pc.WriteTo(resp, client)
				}
			}(req, addr)
		}
	}()

	dnsAddr := pc.LocalAddr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Slow tunnel resolution (>1s) must succeed without Keenetic resolv.conf timeout:1 failure.
	ipSlow, err := lookupFircDNSIPv4(ctx, dnsAddr, "slow-tunnel.example.com")
	if err != nil {
		t.Fatalf("slow lookup failed: %v", err)
	}
	if ipSlow.String() != "198.18.2.10" {
		t.Fatalf("slow IP = %q, want 198.18.2.10", ipSlow.String())
	}

	// 2. CNAME + A in same response packet.
	ipCname, err := lookupFircDNSIPv4(ctx, dnsAddr, "cname.example.com")
	if err != nil {
		t.Fatalf("cname lookup failed: %v", err)
	}
	if ipCname.String() != "93.184.216.34" {
		t.Fatalf("cname IP = %q, want 93.184.216.34", ipCname.String())
	}

	// 3. NXDOMAIN returns explicit error cleanly.
	if _, err := lookupFircDNSIPv4(ctx, dnsAddr, "missing.example.com"); err == nil {
		t.Fatalf("expected error on NXDOMAIN")
	}

	_ = pc.Close()
	<-done
}
