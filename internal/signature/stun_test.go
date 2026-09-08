package signature

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	mrand "math/rand"
	"strings"
	"testing"
)

func TestSTUN_FingerprintValidAndStatic(t *testing.T) {
	pk, err := buildSTUN(mrand.New(mrand.NewSource(3)))
	if err != nil {
		t.Fatal(err)
	}
	if pk.I2 != "" {
		t.Fatal("STUN fills only I1")
	}
	assertAllowedTokens(t, pk.I1)
	if strings.Count(pk.I1, "<") != 1 {
		t.Fatalf("STUN must be one static <b>: FINGERPRINT covers the message")
	}
	msg, _ := hex.DecodeString(pk.I1[5 : len(pk.I1)-1])
	typ := binary.BigEndian.Uint16(msg)
	if typ != 0x0001 && typ != 0x0003 {
		t.Fatalf("type %#04x, want Binding(0x0001) or Allocate(0x0003, RFC 5766)", typ)
	}
	if binary.BigEndian.Uint32(msg[4:]) != 0x2112A442 {
		t.Fatal("magic cookie")
	}
	if int(binary.BigEndian.Uint16(msg[2:]))+20 != len(msg) {
		t.Fatal("length field")
	}
	// последний атрибут — FINGERPRINT, CRC32 по всему сообщению до него, XOR 0x5354554E
	fp := msg[len(msg)-8:]
	if binary.BigEndian.Uint16(fp) != 0x8028 || binary.BigEndian.Uint16(fp[2:]) != 4 {
		t.Fatal("FINGERPRINT attr")
	}
	want := crc32.ChecksumIEEE(msg[:len(msg)-8]) ^ 0x5354554E
	if binary.BigEndian.Uint32(fp[4:]) != want {
		t.Fatal("FINGERPRINT CRC mismatch")
	}
	// атрибуты выровнены по 4
	for p := 20; p < len(msg); {
		l := int(binary.BigEndian.Uint16(msg[p+2:]))
		p += 4 + (l+3)/4*4
		if p > len(msg) {
			t.Fatal("attribute overruns message")
		}
	}
	if _, ok := stunAttrs(msg)[0x8022]; !ok {
		t.Fatal("SOFTWARE attribute missing")
	}
}

// stunAttrs разбирает TLV после 20-байтового заголовка (значения без набивки).
func stunAttrs(msg []byte) map[uint16][]byte {
	out := map[uint16][]byte{}
	for p := 20; p+4 <= len(msg); {
		typ := binary.BigEndian.Uint16(msg[p:])
		l := int(binary.BigEndian.Uint16(msg[p+2:]))
		out[typ] = msg[p+4 : p+4+l]
		p += 4 + (l+3)/4*4
	}
	return out
}

func TestSTUN_AllocateHasTurnAttributes(t *testing.T) {
	// перебираем seed'ы, пока не выпадет Allocate (провайдеры с allocate имеют вероятность ≥ 0.67)
	for seed := int64(1); seed < 50; seed++ {
		pk, _ := buildSTUN(mrand.New(mrand.NewSource(seed)))
		msg, _ := hex.DecodeString(pk.I1[5 : len(pk.I1)-1])
		if binary.BigEndian.Uint16(msg) != 0x0003 {
			continue
		}
		attrs := stunAttrs(msg)
		for _, attr := range []uint16{0x0014, 0x000D, 0x0019, 0x0017, 0x0006} {
			if _, ok := attrs[attr]; !ok {
				t.Fatalf("Allocate lacks attribute %#04x", attr)
			}
		}
		if !bytes.Equal(attrs[0x0019], []byte{0x11, 0, 0, 0}) {
			t.Fatalf("REQUESTED-TRANSPORT % x", attrs[0x0019])
		}
		// REQUESTED-ADDRESS-FAMILY (RFC 6156 §4.1.1): семейство в ПЕРВОМ байте,
		// три байта RFFU нулевые.
		raf := attrs[0x0017]
		if len(raf) != 4 || (raf[0] != 0x01 && raf[0] != 0x02) || !bytes.Equal(raf[1:4], []byte{0, 0, 0}) {
			t.Fatalf("REQUESTED-ADDRESS-FAMILY % x", raf)
		}
		return
	}
	t.Fatal("no Allocate in 50 seeds")
}
