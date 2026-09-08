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
