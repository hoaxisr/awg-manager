package signature

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	mrand "math/rand"
	"strings"
	"testing"
)

var testInitialSalt, _ = hex.DecodeString("38762cf7f55934b34d179ae6a4c80cadccbb7f0a")

func expandLabel(t *testing.T, secret []byte, label string, n int) []byte {
	t.Helper()
	info := []byte{byte(n >> 8), byte(n), byte(len("tls13 " + label))}
	info = append(info, "tls13 "+label...)
	info = append(info, 0)
	out, err := hkdf.Expand(sha256.New, secret, string(info), n)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// decryptInitial снимает header protection и AEAD по RFC 9001 и возвращает
// (dcid, scid, plaintext payload).
func decryptInitial(t *testing.T, pkt []byte) (dcid, scid, payload []byte) {
	t.Helper()
	if pkt[0]&0xF0 != 0xC0 || !bytes.Equal(pkt[1:5], []byte{0, 0, 0, 1}) {
		t.Fatalf("not a v1 Initial: % x", pkt[:5])
	}
	p := 5
	dl := int(pkt[p])
	p++
	dcid = pkt[p : p+dl]
	p += dl
	sl := int(pkt[p])
	p++
	scid = pkt[p : p+sl]
	p += sl
	if pkt[p] != 0 {
		t.Fatalf("token length = %d, want 0", pkt[p])
	}
	p++
	// length varint
	var length int
	switch pkt[p] >> 6 {
	case 0:
		length = int(pkt[p])
		p++
	case 1:
		length = int(binary.BigEndian.Uint16(pkt[p:]) & 0x3FFF)
		p += 2
	case 2:
		length = int(binary.BigEndian.Uint32(pkt[p:]) & 0x3FFFFFFF)
		p += 4
	default:
		t.Fatal("8-byte length")
	}
	pnOff := p
	initial, err := hkdf.Extract(sha256.New, dcid, testInitialSalt)
	if err != nil {
		t.Fatal(err)
	}
	client := expandLabel(t, initial, "client in", 32)
	key := expandLabel(t, client, "quic key", 16)
	iv := expandLabel(t, client, "quic iv", 12)
	hp := expandLabel(t, client, "quic hp", 16)
	blk, _ := aes.NewCipher(hp)
	sample := pkt[pnOff+4 : pnOff+20]
	mask := make([]byte, 16)
	blk.Encrypt(mask, sample)
	hdr := append([]byte(nil), pkt[:pnOff]...)
	first := pkt[0] ^ (mask[0] & 0x0F)
	pnLen := int(first&0x03) + 1
	hdr[0] = first
	pn := make([]byte, pnLen)
	for i := 0; i < pnLen; i++ {
		pn[i] = pkt[pnOff+i] ^ mask[1+i]
	}
	hdr = append(hdr, pn...)
	nonce := append([]byte(nil), iv...)
	for i := 0; i < pnLen; i++ {
		nonce[len(nonce)-pnLen+i] ^= pn[i]
	}
	aead, _ := cipher.NewGCM(mustAES(t, key))
	ct := pkt[pnOff+pnLen : pnOff+length]
	payload, err = aead.Open(nil, nonce, ct, hdr)
	if err != nil {
		t.Fatalf("AEAD open: %v", err)
	}
	return dcid, scid, payload
}

func mustAES(t *testing.T, k []byte) cipher.Block {
	t.Helper()
	b, err := aes.NewCipher(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestQUICInitial_IsValidRFC9001Packet(t *testing.T) {
	pk, err := buildQUICProfile(mrand.New(mrand.NewSource(7)))
	if err != nil {
		t.Fatal(err)
	}
	if pk.I2 != "" || pk.I3 != "" || pk.I4 != "" || pk.I5 != "" {
		t.Fatalf("QUIC must fill only I1: %+v", pk)
	}
	if !strings.HasPrefix(pk.I1, "<b 0x") || strings.Count(pk.I1, "<") != 1 {
		t.Fatalf("I1 must be a single <b> token: %.40s…", pk.I1)
	}
	assertAllowedTokens(t, pk.I1)
	raw, err := hex.DecodeString(pk.I1[len("<b 0x") : len(pk.I1)-1])
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1200 {
		t.Fatalf("packet = %d bytes, want 1200 (RFC 9000 §14.1)", len(raw))
	}
	dcid, scid, payload := decryptInitial(t, raw)
	if len(dcid) != 8 || len(scid) != 8 {
		t.Fatalf("cid lengths %d/%d, want 8/8", len(dcid), len(scid))
	}
	// CRYPTO frame: 0x06, offset 0, length varint, then ClientHello.
	if payload[0] != 0x06 || payload[1] != 0x00 {
		t.Fatalf("payload does not start with CRYPTO frame @0: % x", payload[:4])
	}
	var chLen, p int
	switch payload[2] >> 6 {
	case 0:
		chLen, p = int(payload[2]), 3
	case 1:
		chLen, p = int(binary.BigEndian.Uint16(payload[2:])&0x3FFF), 4
	default:
		t.Fatal("unexpected CRYPTO length varint")
	}
	ch := payload[p : p+chLen]
	for _, b := range payload[p+chLen:] {
		if b != 0 {
			t.Fatal("trailing bytes after CRYPTO must be PADDING (0x00)")
		}
	}
	if ch[0] != 0x01 {
		t.Fatalf("handshake type %#x, want ClientHello", ch[0])
	}
	if int(ch[1])<<16|int(ch[2])<<8|int(ch[3]) != len(ch)-4 {
		t.Fatal("ClientHello length mismatch")
	}
	body := ch[4:]
	if !bytes.Equal(body[:2], []byte{0x03, 0x03}) {
		t.Fatal("legacy_version != 0x0303")
	}
	q := 2 + 32
	sid := int(body[q])
	q += 1 + sid
	if sid != 32 {
		t.Fatalf("session_id len %d, want 32", sid)
	}
	csLen := int(binary.BigEndian.Uint16(body[q:]))
	q += 2
	cs := body[q : q+csLen]
	q += csLen
	if !bytes.Contains(cs, []byte{0x13, 0x01}) || !bytes.Contains(cs, []byte{0x13, 0x02}) || !bytes.Contains(cs, []byte{0x13, 0x03}) {
		t.Fatalf("TLS 1.3 suites missing: % x", cs)
	}
	if csLen != 8 {
		t.Fatalf("cipher suites = %d bytes, want GREASE+3 suites (8)", csLen)
	}
	if body[q] != 1 || body[q+1] != 0 {
		t.Fatal("compression must be [1,0]")
	}
	q += 2
	extLen := int(binary.BigEndian.Uint16(body[q:]))
	q += 2
	exts := body[q : q+extLen]
	var sni, alpn, qtp []byte
	var order []uint16
	for e := 0; e < len(exts); {
		typ := binary.BigEndian.Uint16(exts[e:])
		l := int(binary.BigEndian.Uint16(exts[e+2:]))
		data := exts[e+4 : e+4+l]
		order = append(order, typ)
		switch typ {
		case 0x0000:
			sni = data
		case 0x0010:
			alpn = data
		case 0x0039:
			qtp = data
		}
		e += 4 + l
	}
	if len(order) != 14 {
		t.Fatalf("extension count %d, want 14 (§1.7.2 order)", len(order))
	}
	if order[0]&0x0F0F != 0x0A0A || order[len(order)-2]&0x0F0F != 0x0A0A || order[0] == order[len(order)-2] {
		t.Fatalf("GREASE first/second-to-last expected, got %#04x … %#04x", order[0], order[len(order)-2])
	}
	if order[len(order)-1] != 0x0015 {
		t.Fatal("padding must be last")
	}
	host := string(sni[5:])
	if !hostPoolHas(host) {
		t.Fatalf("SNI %q not from pool", host)
	}
	if !bytes.Equal(alpn, []byte{0x00, 0x03, 0x02, 'h', '3'}) {
		t.Fatalf("ALPN = % x, want h3", alpn)
	}
	// initial_source_connection_id (0x0f) внутри transport parameters равен SCID.
	if !bytes.Contains(qtp, append([]byte{0x0f, byte(len(scid))}, scid...)) {
		t.Fatalf("transport params lack initial_source_connection_id = scid")
	}
}

func TestQUICInitial_DiffersPerGeneration(t *testing.T) {
	a, _ := buildQUICProfile(mrand.New(mrand.NewSource(1)))
	b, _ := buildQUICProfile(mrand.New(mrand.NewSource(2)))
	if a.I1 == b.I1 {
		t.Fatal("two generations must not be identical")
	}
}
