package signature

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	mrand "math/rand"
	"testing"
)

func TestDTLS_ClientHelloWithRuntimeRandom(t *testing.T) {
	pk, err := buildDTLS(mrand.New(mrand.NewSource(9)))
	if err != nil {
		t.Fatal(err)
	}
	assertAllowedTokens(t, pk.I1)
	// <b header…client_version><t><r 28><b 0x20><r 32><b cookie+suites+ext>
	toks := tagRe.FindAllStringSubmatch(pk.I1, -1)
	kinds := ""
	for _, m := range toks {
		kinds += m[1] + " "
	}
	if kinds != "b t r b r b " {
		t.Fatalf("token kinds %q", kinds)
	}
	if toks[2][2] != "28" || toks[4][2] != "32" {
		t.Fatalf("random/session sizes: %s/%s", toks[2][2], toks[4][2])
	}
	head, _ := hex.DecodeString(toks[0][2][2:])
	if head[0] != 0x16 || !bytes.Equal(head[1:3], []byte{0xfe, 0xfd}) {
		t.Fatal("record header")
	}
	recLen := int(binary.BigEndian.Uint16(head[11:]))
	if recLen != ByteSize(pk.I1)-13 {
		t.Fatalf("record length %d vs payload %d", recLen, ByteSize(pk.I1)-13)
	}
	if head[13] != 0x01 {
		t.Fatal("handshake type")
	}
	hsLen := int(head[14])<<16 | int(head[15])<<8 | int(head[16])
	if hsLen != recLen-12 {
		t.Fatal("handshake length")
	}
	tail, _ := hex.DecodeString(toks[5][2][2:])
	if tail[0] != 0 {
		t.Fatal("cookie length 0")
	}
	if !bytes.Equal(tail[1:3], []byte{0x00, 0x0C}) {
		t.Fatal("6 cipher suites")
	}
	if !bytes.Contains(tail, []byte{0x00, 0x0E, 0x00, 0x05, 0x00, 0x02, 0x00, 0x01, 0x00}) {
		t.Fatal("use_srtp")
	}
}

func TestDTLS_ExtensionOrderAndSNI(t *testing.T) {
	pk, err := buildDTLS(mrand.New(mrand.NewSource(9)))
	if err != nil {
		t.Fatal(err)
	}
	toks := tagRe.FindAllStringSubmatch(pk.I1, -1)
	tail, _ := hex.DecodeString(toks[5][2][2:])
	// tail = cookie(1) + cipher suites length(2) + suites(12) + compression(2) + ext length(2) + extensions.
	extStart := 1 + 2 + 12 + 2 + 2
	exts := tail[extStart:]

	var order []uint16
	var sni []byte
	for e := 0; e < len(exts); {
		typ := binary.BigEndian.Uint16(exts[e:])
		l := int(binary.BigEndian.Uint16(exts[e+2:]))
		data := exts[e+4 : e+4+l]
		order = append(order, typ)
		if typ == 0x0000 {
			sni = data
		}
		e += 4 + l
	}
	want := []uint16{0x0000, 0x000A, 0x000B, 0x000D, 0x000E, 0x0017}
	if len(order) != len(want) {
		t.Fatalf("extension count %d, want %d: %#04x", len(order), len(want), order)
	}
	for i, typ := range want {
		if order[i] != typ {
			t.Fatalf("extension order = %#04x, want %#04x", order, want)
		}
	}
	if sni == nil {
		t.Fatal("SNI extension (0x0000) missing")
	}
	host := string(sni[5:])
	if !hostPoolHas(host) {
		t.Fatalf("SNI %q not from hostPool", host)
	}
}
