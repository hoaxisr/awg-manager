package rtpopus

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestConnRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeyLen)
	payload := []byte("dtls record bytes")

	client, err := NewConn(key, false)
	if err != nil {
		t.Fatalf("NewConn(client): %v", err)
	}
	server, err := NewConn(key, true)
	if err != nil {
		t.Fatalf("NewConn(server): %v", err)
	}

	wire := make([]byte, MaxWire(len(payload)))
	n, err := client.WrapInto(wire, payload)
	if err != nil {
		t.Fatalf("WrapInto: %v", err)
	}
	wire = wire[:n]

	if wire[0] != rtpVersion {
		t.Fatalf("RTP byte0 = 0x%02X, want 0x%02X", wire[0], rtpVersion)
	}
	if wire[1] != rtpPT {
		t.Fatalf("RTP byte1 (PT) = 0x%02X, want 0x%02X", wire[1], rtpPT)
	}
	if bytes.Contains(wire, payload) {
		t.Fatalf("wrapped packet contains plaintext payload")
	}

	dst := make([]byte, 1600)
	m, err := server.Unwrap(wire, dst)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if m != len(payload) {
		t.Fatalf("unwrapped len = %d, want %d", m, len(payload))
	}
	if !bytes.Equal(dst[:m], payload) {
		t.Fatalf("round trip mismatch: got %q want %q", dst[:m], payload)
	}

	wire2 := make([]byte, MaxWire(len(payload)))
	n2, err := server.WrapInto(wire2, payload)
	if err != nil {
		t.Fatalf("server WrapInto: %v", err)
	}
	m2, err := client.Unwrap(wire2[:n2], dst)
	if err != nil {
		t.Fatalf("client Unwrap: %v", err)
	}
	if !bytes.Equal(dst[:m2], payload) {
		t.Fatalf("server->client round trip mismatch")
	}
}

func TestInPlaceRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeyLen)
	payload := []byte("dtls record bytes in-place")

	client, err := NewConn(key, false)
	if err != nil {
		t.Fatalf("NewConn(client): %v", err)
	}
	server, err := NewConn(key, true)
	if err != nil {
		t.Fatalf("NewConn(server): %v", err)
	}

	// Клиент: payload уже в buf[HeaderLen:], WrapInPlace без копии.
	buf := make([]byte, MaxWire(len(payload)))
	copy(buf[HeaderLen:], payload)
	n, err := client.WrapInPlace(buf, len(payload))
	if err != nil {
		t.Fatalf("WrapInPlace: %v", err)
	}
	if n != MaxWire(len(payload)) {
		t.Fatalf("WrapInPlace len = %d, want %d", n, MaxWire(len(payload)))
	}
	if bytes.Contains(buf[:n], payload) {
		t.Fatalf("wrapped packet contains plaintext payload")
	}

	// WrapInPlace и WrapInto должны давать совместимый wire (decode общим Unwrap).
	dst := make([]byte, 1600)
	m, err := server.Unwrap(buf[:n], dst)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if !bytes.Equal(dst[:m], payload) {
		t.Fatalf("WrapInPlace->Unwrap mismatch: got %q want %q", dst[:m], payload)
	}

	// Сервер: WrapInto -> клиент UnwrapInPlace (subslice внутрь wire, без копии).
	wire := make([]byte, MaxWire(len(payload)))
	wn, err := server.WrapInto(wire, payload)
	if err != nil {
		t.Fatalf("server wrap: %v", err)
	}
	plain, err := client.UnwrapInPlace(wire[:wn])
	if err != nil {
		t.Fatalf("UnwrapInPlace: %v", err)
	}
	if !bytes.Equal(plain, payload) {
		t.Fatalf("UnwrapInPlace mismatch: got %q want %q", plain, payload)
	}
}

func TestRTPHeaderProgression(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeyLen)
	c, err := NewConn(key, false)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	payload := []byte("x")

	wire1 := make([]byte, MaxWire(len(payload)))
	n1, err := c.WrapInto(wire1, payload)
	if err != nil {
		t.Fatalf("WrapInto 1: %v", err)
	}
	wire2 := make([]byte, MaxWire(len(payload)))
	n2, err := c.WrapInto(wire2, payload)
	if err != nil {
		t.Fatalf("WrapInto 2: %v", err)
	}
	if n1 != n2 {
		t.Fatalf("wire size variance: %d vs %d", n1, n2)
	}

	seq1 := binary.BigEndian.Uint16(wire1[2:4])
	seq2 := binary.BigEndian.Uint16(wire2[2:4])
	if seq2 != seq1+1 {
		t.Fatalf("seq did not increment: %d -> %d", seq1, seq2)
	}

	ts1 := binary.BigEndian.Uint32(wire1[4:8])
	ts2 := binary.BigEndian.Uint32(wire2[4:8])
	if ts2-ts1 != tsStep {
		t.Fatalf("timestamp step = %d, want %d", ts2-ts1, tsStep)
	}

	if !bytes.Equal(wire1[8:12], wire2[8:12]) {
		t.Fatalf("SSRC changed between packets")
	}
}

func TestDirectionBit(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeyLen)
	client, err := NewConn(key, false)
	if err != nil {
		t.Fatalf("NewConn(client): %v", err)
	}
	server, err := NewConn(key, true)
	if err != nil {
		t.Fatalf("NewConn(server): %v", err)
	}

	if client.sessionID[0]&0x80 != 0 {
		t.Fatalf("client sessionID MSB should be 0, got 0x%02X", client.sessionID[0])
	}
	if server.sessionID[0]&0x80 == 0 {
		t.Fatalf("server sessionID MSB should be 1, got 0x%02X", server.sessionID[0])
	}
	if client.ssrc[0]&0x80 != 0 {
		t.Fatalf("client SSRC MSB should be 0, got 0x%02X", client.ssrc[0])
	}
	if server.ssrc[0]&0x80 == 0 {
		t.Fatalf("server SSRC MSB should be 1, got 0x%02X", server.ssrc[0])
	}
}

func TestDecodeKeyRequiresValidKeyWhenEnabled(t *testing.T) {
	if key, err := DecodeKey(false, ""); err != nil || key != nil {
		t.Fatalf("disabled DecodeKey = (%v, %v), want (nil, nil)", key, err)
	}

	if _, err := DecodeKey(true, ""); err == nil {
		t.Fatalf("DecodeKey accepted empty key")
	}

	shortHex := strings.Repeat("ab", KeyLen-1)
	if _, err := DecodeKey(true, shortHex); err == nil {
		t.Fatalf("DecodeKey accepted short key")
	}

	fullHex := strings.Repeat("ab", KeyLen)
	key, err := DecodeKey(true, fullHex)
	if err != nil {
		t.Fatalf("DecodeKey returned error: %v", err)
	}
	if len(key) != KeyLen {
		t.Fatalf("decoded key len = %d, want %d", len(key), KeyLen)
	}
}

func TestUnwrapRejectsShortPacket(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeyLen)
	c, err := NewConn(key, false)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	if _, err := c.Unwrap([]byte("short"), make([]byte, 16)); err == nil {
		t.Fatalf("Unwrap accepted short packet")
	}
}

func TestUnwrapRejectsTamperedPacket(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeyLen)
	client, err := NewConn(key, false)
	if err != nil {
		t.Fatalf("NewConn(client): %v", err)
	}
	server, err := NewConn(key, true)
	if err != nil {
		t.Fatalf("NewConn(server): %v", err)
	}

	payload := []byte("integrity test")
	wire := make([]byte, MaxWire(len(payload)))
	n, err := client.WrapInto(wire, payload)
	if err != nil {
		t.Fatalf("WrapInto: %v", err)
	}
	wire = wire[:n]

	wire[headerLen+1] ^= 0xFF

	dst := make([]byte, 1600)
	if _, uerr := server.Unwrap(wire, dst); uerr == nil {
		t.Fatalf("Unwrap accepted tampered ciphertext")
	}

	n2, err := client.WrapInto(wire, payload)
	if err != nil {
		t.Fatalf("WrapInto: %v", err)
	}
	wire = wire[:n2]
	wire[8] ^= 0x01
	if _, uerr := server.Unwrap(wire, dst); uerr == nil {
		t.Fatalf("Unwrap accepted tampered AAD")
	}
}
