package signature

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	mrand "math/rand"
	"strings"
	"testing"
)

func TestDNS_QueryShapeAndRandomTXID(t *testing.T) {
	pk, err := buildDNS(mrand.New(mrand.NewSource(5)))
	if err != nil {
		t.Fatal(err)
	}
	assertAllowedTokens(t, pk.I1)
	if !strings.HasPrefix(pk.I1, "<r 2><b 0x") {
		t.Fatalf("TXID must be <r 2> then static body: %.30s", pk.I1)
	}
	body, _ := hex.DecodeString(pk.I1[len("<r 2><b 0x") : len(pk.I1)-1])
	if !bytes.Equal(body[:10], []byte{0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 1}) {
		t.Fatalf("header % x", body[:10])
	}
	p := 10
	var labels []string
	for body[p] != 0 {
		l := int(body[p])
		labels = append(labels, string(body[p+1:p+1+l]))
		p += 1 + l
	}
	p++
	if !hostPoolHas(strings.Join(labels, ".")) {
		t.Fatalf("qname %v not from pool", labels)
	}
	qt := binary.BigEndian.Uint16(body[p:])
	if qt != 1 && qt != 28 && qt != 65 {
		t.Fatalf("qtype %d", qt)
	}
	if binary.BigEndian.Uint16(body[p+2:]) != 1 {
		t.Fatal("qclass IN")
	}
	opt := body[p+4:]
	if !bytes.Equal(opt, []byte{0x00, 0x00, 0x29, 0x04, 0xD0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("EDNS0 OPT % x", opt)
	}
}
