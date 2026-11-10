package dnsfeed

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"testing"
)

type testRR struct {
	name  []byte
	typ   uint16
	rdata []byte
}

func TestParseResponseAAndNormalization(t *testing.T) {
	msg := dnsResponse("WWW.Example.COM.", typeA,
		testAddress("www.example.com", typeA, "192.0.2.10"))
	got, err := ParseResponse(msg)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if got.ID != 0xbeef {
		t.Fatalf("ID = %#x, want 0xbeef", got.ID)
	}
	if got.QueriedName != "www.example.com" || got.FinalName != "www.example.com" {
		t.Fatalf("names = %q, %q; want normalized queried name", got.QueriedName, got.FinalName)
	}
	wantIPs := []netip.Addr{netip.MustParseAddr("192.0.2.10")}
	if !slices.Equal(got.RealIPs, wantIPs) {
		t.Fatalf("RealIPs = %v, want %v", got.RealIPs, wantIPs)
	}
}

func TestParseResponseCompressedCNAMEChainOutOfOrder(t *testing.T) {
	// The address and second CNAME deliberately precede the first CNAME. The
	// answer section need not be in chain order, and addresses may belong to
	// any member of the chain rather than only to its final name.
	msg := startMessage(wireName("WWW.Example.COM"), typeAAAA, 5)

	edgePos := len(msg)
	msg = appendTestRR(msg, wireName("Edge.CDN.Net"), typeAAAA, addressBytes(t, "2001:db8::1"))

	aliasPos := len(msg)
	msg = appendTestRR(msg, wireName("Alias.Example.Net"), typeCNAME, pointer(edgePos))
	msg = appendTestRR(msg, pointer(12), typeCNAME, pointer(aliasPos))
	msg = appendTestRR(msg, pointer(12), typeA, addressBytes(t, "192.0.2.20"))
	msg = appendTestRR(msg, pointer(aliasPos), typeAAAA, addressBytes(t, "2001:db8::2"))

	got, err := ParseResponse(msg)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if got.QueriedName != "www.example.com" {
		t.Errorf("QueriedName = %q", got.QueriedName)
	}
	if got.FinalName != "edge.cdn.net" {
		t.Errorf("FinalName = %q, want edge.cdn.net", got.FinalName)
	}
	wantIPs := []netip.Addr{
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("192.0.2.20"),
		netip.MustParseAddr("2001:db8::2"),
	}
	if !slices.Equal(got.RealIPs, wantIPs) {
		t.Fatalf("RealIPs = %v, want %v", got.RealIPs, wantIPs)
	}
}

func TestParseResponseAcceptsDuplicateIdenticalCNAME(t *testing.T) {
	msg := dnsResponse("a.example", typeA,
		testCNAME("a.example", "b.example"),
		testCNAME("A.EXAMPLE", "B.EXAMPLE"),
		testAddress("b.example", typeA, "192.0.2.1"))
	got, err := ParseResponse(msg)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if got.FinalName != "b.example" || len(got.RealIPs) != 1 {
		t.Fatalf("unexpected answer: %+v", got)
	}
}

func TestParseResponseSkipsUnrelatedAddressesAndCapsAt16(t *testing.T) {
	rrs := []testRR{
		testAddress("unrelated.invalid", typeA, "203.0.113.99"),
		testAddress("also-unrelated.invalid", typeAAAA, "2001:db8::ffff"),
	}
	for i := 1; i <= 18; i++ {
		rrs = append(rrs, testAddress("a.example", typeA, fmt.Sprintf("192.0.2.%d", i)))
	}
	got, err := ParseResponse(dnsResponse("a.example", typeA, rrs...))
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if len(got.RealIPs) != maxRealIPs {
		t.Fatalf("len(RealIPs) = %d, want %d", len(got.RealIPs), maxRealIPs)
	}
	if got.RealIPs[0] != netip.MustParseAddr("192.0.2.1") ||
		got.RealIPs[15] != netip.MustParseAddr("192.0.2.16") {
		t.Fatalf("wrong retained addresses: %v", got.RealIPs)
	}
}

func TestParseResponseIgnoresUnrelatedCNAMELoop(t *testing.T) {
	msg := dnsResponse("a.example", typeA,
		testCNAME("x.invalid", "y.invalid"),
		testCNAME("y.invalid", "x.invalid"),
		testAddress("a.example", typeA, "192.0.2.1"))
	if _, err := ParseResponse(msg); err != nil {
		t.Fatalf("unrelated CNAMEs must not poison a usable answer: %v", err)
	}
}

func TestParseResponseRejectsCompressionPointerLoops(t *testing.T) {
	indirect := wireMessage([]byte{0xc0, 0x12}, typeA, nil) // 12 -> 18
	indirect = append(indirect, 0xc0, 0x0c)                 // 18 -> 12

	answerSelf := startMessage(wireName("a.example"), typeA, 1)
	answerPos := len(answerSelf)
	answerSelf = appendTestRR(answerSelf, pointer(answerPos), typeA, addressBytes(t, "192.0.2.1"))

	tests := []struct {
		name string
		msg  []byte
	}{
		{"self pointer in question", wireMessage([]byte{0xc0, 0x0c}, typeA, nil)},
		{"indirect question pointers", indirect},
		{"label returns to itself", wireMessage([]byte{1, 'a', 0xc0, 0x0c}, typeA, nil)},
		{"answer owner self pointer", answerSelf},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseResponse(tt.msg)
			if !errors.Is(err, ErrNameLoop) {
				t.Fatalf("error = %v, want ErrNameLoop", err)
			}
		})
	}
}

func TestParseResponseRejectsCNAMELoops(t *testing.T) {
	tests := []struct {
		name string
		rrs  []testRR
	}{
		{"self", []testRR{
			testCNAME("a.example", "a.example"),
			testAddress("a.example", typeA, "192.0.2.1"),
		}},
		{"two names", []testRR{
			testCNAME("a.example", "b.example"),
			testCNAME("b.example", "A.EXAMPLE"),
			testAddress("b.example", typeA, "192.0.2.1"),
		}},
		{"longer cycle", []testRR{
			testCNAME("a.example", "b.example"),
			testCNAME("b.example", "c.example"),
			testCNAME("c.example", "b.example"),
			testAddress("c.example", typeA, "192.0.2.1"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseResponse(dnsResponse("a.example", typeA, tt.rrs...))
			if !errors.Is(err, ErrCNAMELoop) {
				t.Fatalf("error = %v, want ErrCNAMELoop", err)
			}
		})
	}
}

func TestParseResponseRejectsAmbiguousOrOverlongCNAMEChain(t *testing.T) {
	t.Run("conflicting targets", func(t *testing.T) {
		msg := dnsResponse("a.example", typeA,
			testCNAME("a.example", "b.example"),
			testCNAME("a.example", "c.example"),
			testAddress("b.example", typeA, "192.0.2.1"))
		if _, err := ParseResponse(msg); !errors.Is(err, ErrMalformed) {
			t.Fatalf("error = %v, want ErrMalformed", err)
		}
	})

	t.Run("overlong", func(t *testing.T) {
		rrs := make([]testRR, 0, maxChainNames+1)
		for i := 0; i < maxChainNames; i++ {
			rrs = append(rrs, testCNAME(fmt.Sprintf("n%d.example", i), fmt.Sprintf("n%d.example", i+1)))
		}
		rrs = append(rrs, testAddress(fmt.Sprintf("n%d.example", maxChainNames), typeA, "192.0.2.1"))
		if _, err := ParseResponse(dnsResponse("n0.example", typeA, rrs...)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("error = %v, want ErrMalformed", err)
		}
	})
}

func TestParseResponseRejectsMalformedAndUnusableMessages(t *testing.T) {
	valid := dnsResponse("a.example", typeA, testAddress("a.example", typeA, "192.0.2.1"))
	clone := func(b []byte) []byte { return append([]byte(nil), b...) }

	query := clone(valid)
	query[2] &^= 0x80
	truncatedFlag := clone(valid)
	truncatedFlag[2] |= 0x02
	extraAnswer := clone(valid)
	binary.BigEndian.PutUint16(extraAnswer[6:8], 2)
	missingAdditional := clone(valid)
	binary.BigEndian.PutUint16(missingAdditional[10:12], 1)
	nxdomain := clone(valid)
	nxdomain[3] = (nxdomain[3] &^ 0x0f) | 3
	noQuestion := clone(valid)
	binary.BigEndian.PutUint16(noQuestion[4:6], 0)

	tests := []struct {
		name string
		msg  []byte
		want error
	}{
		{"empty", nil, ErrTruncated},
		{"short header", make([]byte, 11), ErrTruncated},
		{"query rather than response", query, ErrNotResponse},
		{"DNS truncated flag", truncatedFlag, ErrTruncated},
		{"short question", valid[:15], ErrTruncated},
		{"short RDATA", valid[:len(valid)-1], ErrTruncated},
		{"declared extra answer", extraAnswer, ErrTruncated},
		{"declared missing additional", missingAdditional, ErrTruncated},
		{"trailing byte", append(clone(valid), 0), ErrMalformed},
		{"A RDLENGTH", dnsResponse("a.example", typeA, testRR{name: wireName("a.example"), typ: typeA, rdata: []byte{1, 2, 3}}), ErrMalformed},
		{"AAAA RDLENGTH", dnsResponse("a.example", typeAAAA, testRR{name: wireName("a.example"), typ: typeAAAA, rdata: make([]byte, 15)}), ErrMalformed},
		{"CNAME trailing RDATA", dnsResponse("a.example", typeA, testRR{name: wireName("a.example"), typ: typeCNAME, rdata: append(wireName("b.example"), 0)}), ErrMalformed},
		{"pointer outside message", wireMessage([]byte{0xc0, 0xff}, typeA, nil), ErrMalformed},
		{"unsupported label tag", wireMessage([]byte{0x40, 0}, typeA, nil), ErrMalformed},
		{"missing question", noQuestion, ErrNoUsableAnswers},
		{"NXDOMAIN", nxdomain, ErrNoUsableAnswers},
		{"unsupported question type", dnsResponse("a.example", 65, testAddress("a.example", typeA, "192.0.2.1")), ErrNoUsableAnswers},
		{"root question", dnsResponse("", typeA, testAddress("", typeA, "192.0.2.1")), ErrNoUsableAnswers},
		{"empty answer section", dnsResponse("a.example", typeA), ErrNoUsableAnswers},
		{"only CNAME", dnsResponse("a.example", typeA, testCNAME("a.example", "b.example")), ErrNoUsableAnswers},
		{"only unrelated address", dnsResponse("a.example", typeA, testAddress("b.example", typeA, "192.0.2.1")), ErrNoUsableAnswers},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseResponse(tt.msg)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if got.QueriedName != "" || got.FinalName != "" || len(got.RealIPs) != 0 {
				t.Fatalf("nonzero Answer on error: %+v", got)
			}
		})
	}
}

func TestParseResponseNormalizesEscapedLabels(t *testing.T) {
	// Labels contain a literal dot, a backslash, and a non-printable octet.
	questionName := []byte{3, 'A', '.', 'B', 1, '\\', 1, 1, 0}
	msg := startMessage(questionName, typeA, 1)
	msg = appendTestRR(msg, pointer(12), typeA, addressBytes(t, "192.0.2.1"))
	got, err := ParseResponse(msg)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	const want = "a\\.b.\\\\.\\001"
	if got.QueriedName != want || got.FinalName != want {
		t.Fatalf("names = %q, %q; want %q", got.QueriedName, got.FinalName, want)
	}
}

func TestParseTCPResponse(t *testing.T) {
	payload := dnsResponse("a.example", typeA, testAddress("a.example", typeA, "192.0.2.1"))
	frame := make([]byte, 2, len(payload)+2)
	binary.BigEndian.PutUint16(frame, uint16(len(payload)))
	frame = append(frame, payload...)

	got, err := ParseTCPResponse(frame)
	if err != nil {
		t.Fatalf("ParseTCPResponse: %v", err)
	}
	if got.QueriedName != "a.example" || len(got.RealIPs) != 1 {
		t.Fatalf("unexpected answer: %+v", got)
	}

	tests := []struct {
		name  string
		frame []byte
		want  error
	}{
		{"missing length", []byte{0}, ErrTruncated},
		{"short body", frame[:len(frame)-1], ErrTruncated},
		{"trailing byte", append(slices.Clone(frame), 0), ErrMalformed},
		{"empty DNS payload", []byte{0, 0}, ErrTruncated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseTCPResponse(tt.frame); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func FuzzParseResponse(f *testing.F) {
	f.Add([]byte(nil))
	f.Add(dnsResponse("a.example", typeA, testAddress("a.example", typeA, "192.0.2.1")))
	f.Add(wireMessage([]byte{0xc0, 0x0c}, typeA, nil))
	f.Add(dnsResponse("a.example", typeAAAA, testCNAME("a.example", "b.example"), testAddress("b.example", typeAAAA, "2001:db8::1")))
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, _ = ParseResponse(payload)
	})
}

func dnsResponse(question string, queryType uint16, answers ...testRR) []byte {
	return wireMessage(wireName(question), queryType, answers)
}

func wireMessage(questionName []byte, queryType uint16, answers []testRR) []byte {
	msg := startMessage(questionName, queryType, len(answers))
	for _, rr := range answers {
		msg = appendTestRR(msg, rr.name, rr.typ, rr.rdata)
	}
	return msg
}

func startMessage(questionName []byte, queryType uint16, answerCount int) []byte {
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:2], 0xbeef)
	binary.BigEndian.PutUint16(msg[2:4], 0x8180) // QR, RD, RA
	binary.BigEndian.PutUint16(msg[4:6], 1)
	binary.BigEndian.PutUint16(msg[6:8], uint16(answerCount))
	msg = append(msg, questionName...)
	msg = binary.BigEndian.AppendUint16(msg, queryType)
	msg = binary.BigEndian.AppendUint16(msg, 1) // IN
	return msg
}

func appendTestRR(msg, name []byte, typ uint16, rdata []byte) []byte {
	msg = append(msg, name...)
	msg = binary.BigEndian.AppendUint16(msg, typ)
	msg = binary.BigEndian.AppendUint16(msg, 1) // IN
	msg = binary.BigEndian.AppendUint32(msg, 300)
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(rdata)))
	return append(msg, rdata...)
}

func testAddress(name string, typ uint16, ip string) testRR {
	return testRR{name: wireName(name), typ: typ, rdata: addressBytes(nil, ip)}
}

func testCNAME(name, target string) testRR {
	return testRR{name: wireName(name), typ: typeCNAME, rdata: wireName(target)}
}

func addressBytes(t testing.TB, ip string) []byte {
	if t != nil {
		t.Helper()
	}
	addr := netip.MustParseAddr(ip)
	if addr.Is4() {
		raw := addr.As4()
		return raw[:]
	}
	raw := addr.As16()
	return raw[:]
}

func wireName(name string) []byte {
	if name == "" || name == "." {
		return []byte{0}
	}
	name = name[:len(name)-boolInt(name[len(name)-1] == '.')]
	var out []byte
	start := 0
	for i := 0; i <= len(name); i++ {
		if i != len(name) && name[i] != '.' {
			continue
		}
		label := name[start:i]
		if len(label) == 0 || len(label) > 63 {
			panic("invalid test DNS label")
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
		start = i + 1
	}
	return append(out, 0)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func pointer(off int) []byte {
	if off < 0 || off > 0x3fff {
		panic("invalid test compression pointer")
	}
	return []byte{0xc0 | byte(off>>8), byte(off)}
}
