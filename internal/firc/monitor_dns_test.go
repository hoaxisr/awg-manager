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

func encodeTestDNSName(domain string) []byte {
	var out []byte
	for _, lbl := range strings.Split(strings.TrimSuffix(domain, "."), ".") {
		out = append(out, byte(len(lbl)))
		out = append(out, lbl...)
	}
	return append(out, 0)
}

func makeARecordNamed(owner string, ip [4]byte) []byte {
	var rr []byte
	rr = append(rr, encodeTestDNSName(owner)...)
	meta := make([]byte, 10)
	binary.BigEndian.PutUint16(meta[0:2], 1)  // A
	binary.BigEndian.PutUint16(meta[2:4], 1)  // IN
	binary.BigEndian.PutUint32(meta[4:8], 60) // TTL
	binary.BigEndian.PutUint16(meta[8:10], 4) // RDLENGTH
	rr = append(rr, meta...)
	return append(rr, ip[:]...)
}

func makeCNAMERecordNamed(owner, target string) []byte {
	var rr []byte
	rr = append(rr, encodeTestDNSName(owner)...)
	rdata := encodeTestDNSName(target)
	meta := make([]byte, 10)
	binary.BigEndian.PutUint16(meta[0:2], 5)                   // CNAME
	binary.BigEndian.PutUint16(meta[2:4], 1)                   // IN
	binary.BigEndian.PutUint32(meta[4:8], 60)                  // TTL
	binary.BigEndian.PutUint16(meta[8:10], uint16(len(rdata))) // RDLENGTH
	rr = append(rr, meta...)
	return append(rr, rdata...)
}

func TestParseFircDNSAResponse_StrictValidationAndOwnerChain(t *testing.T) {
	q, err := encodeFircDNSQueryA(0x42, "alias.example.com")
	if err != nil {
		t.Fatalf("encodeFircDNSQueryA: %v", err)
	}

	// 1. QR=0 must be rejected.
	qr0 := buildTestDNSResponse(q, 0, [][]byte{makeARecordPtr([4]byte{1, 2, 3, 4})})
	binary.BigEndian.PutUint16(qr0[2:4], 0x0100) // QR=0
	if _, _, _, err := parseFircDNSAResponse(qr0, "alias.example.com"); err == nil {
		t.Fatalf("expected QR=0 packet to be rejected")
	}

	// 2. Non-zero opcode must be rejected.
	badOp := buildTestDNSResponse(q, 0, [][]byte{makeARecordPtr([4]byte{1, 2, 3, 4})})
	binary.BigEndian.PutUint16(badOp[2:4], 0x8980) // QR=1, opcode=1
	if _, _, _, err := parseFircDNSAResponse(badOp, "alias.example.com"); err == nil {
		t.Fatalf("expected non-zero opcode packet to be rejected")
	}

	// 3. Truncated TC=1 must be rejected.
	tcPkt := buildTestDNSResponse(q, 0, [][]byte{makeARecordPtr([4]byte{1, 2, 3, 4})})
	binary.BigEndian.PutUint16(tcPkt[2:4], 0x8380) // TC=1
	if _, _, _, err := parseFircDNSAResponse(tcPkt, "alias.example.com"); err == nil {
		t.Fatalf("expected TC=1 packet to be rejected")
	}

	// 4. Question mismatch: wrong QNAME / QDCOUNT=0 / wrong QTYPE / wrong QCLASS.
	wrongDomain := buildTestDNSResponse(q, 0, [][]byte{makeARecordPtr([4]byte{1, 2, 3, 4})})
	if _, _, _, err := parseFircDNSAResponse(wrongDomain, "other.example.com"); err == nil {
		t.Fatalf("expected QNAME mismatch to be rejected")
	}
	zeroQD := buildTestDNSResponse(q, 0, nil)
	binary.BigEndian.PutUint16(zeroQD[4:6], 0)
	if _, _, _, err := parseFircDNSAResponse(zeroQD, "alias.example.com"); err == nil {
		t.Fatalf("expected QDCOUNT=0 to be rejected")
	}
	wrongQType := append([]byte(nil), buildTestDNSResponse(q, 0, nil)...)
	binary.BigEndian.PutUint16(wrongQType[len(q)-4:len(q)-2], 28) // AAAA instead of A
	if _, _, _, err := parseFircDNSAResponse(wrongQType, "alias.example.com"); err == nil {
		t.Fatalf("expected QTYPE=AAAA mismatch to be rejected")
	}
	wrongQClass := append([]byte(nil), buildTestDNSResponse(q, 0, nil)...)
	binary.BigEndian.PutUint16(wrongQClass[len(q)-2:len(q)], 3) // CH instead of IN
	if _, _, _, err := parseFircDNSAResponse(wrongQClass, "alias.example.com"); err == nil {
		t.Fatalf("expected QCLASS=CH mismatch to be rejected")
	}

	// 5. Answer RR for unrelated owner must NOT be accepted.
	spoofedAns := buildTestDNSResponse(q, 0, [][]byte{
		makeARecordNamed("spoofed.evil.org", [4]byte{10, 20, 30, 40}),
	})
	ip, cname, rcode, err := parseFircDNSAResponse(spoofedAns, "alias.example.com")
	if err != nil || rcode != 0 || ip != nil || cname != "" {
		t.Fatalf("expected unrelated owner answer to be ignored cleanly, got ip=%v cname=%q rcode=%d err=%v", ip, cname, rcode, err)
	}

	// 6. Valid inline CNAME chain with real owners: alias -> hop1 -> 93.184.216.34 (ignoring trailing unrelated A).
	validChain := buildTestDNSResponse(q, 0, [][]byte{
		makeCNAMERecordNamed("alias.example.com", "hop1.example.net"),
		makeARecordNamed("unrelated.example.net", [4]byte{1, 1, 1, 1}),
		makeARecordNamed("hop1.example.net", [4]byte{93, 184, 216, 34}),
	})
	ipChain, cnameChain, rcodeChain, err := parseFircDNSAResponse(validChain, "alias.example.com")
	if err != nil || rcodeChain != 0 || cnameChain != "" || ipChain == nil || ipChain.String() != "93.184.216.34" {
		t.Fatalf("valid CNAME->A chain failed: ip=%v cname=%q rcode=%d err=%v", ipChain, cnameChain, rcodeChain, err)
	}

	// 7. Standalone CNAME without inline A returns validated canonical CNAME target for depth follow-up.
	onlyCNAME := buildTestDNSResponse(q, 0, [][]byte{
		makeCNAMERecordNamed("alias.example.com", "next.example.net"),
	})
	ipOnly, cnameOnly, rcodeOnly, err := parseFircDNSAResponse(onlyCNAME, "alias.example.com")
	if err != nil || rcodeOnly != 0 || ipOnly != nil || cnameOnly != "next.example.net" {
		t.Fatalf("standalone CNAME failed: ip=%v cname=%q rcode=%d err=%v", ipOnly, cnameOnly, rcodeOnly, err)
	}
}
