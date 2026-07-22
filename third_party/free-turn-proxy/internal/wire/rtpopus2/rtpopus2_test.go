package rtpopus2

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeyLen)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestWrapInPlaceRoundTrip(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	cli, err := NewConn(key, false)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewConn(key, true)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("wireguard-ish payload 0123456789abcdef")
	buf := make([]byte, MaxWire(len(payload)))
	copy(buf[headerLen:], payload)
	n, err := cli.WrapInPlace(buf, len(payload))
	if err != nil {
		t.Fatal(err)
	}
	if n != overhead+len(payload) {
		t.Fatalf("wire len = %d, want %d", n, overhead+len(payload))
	}
	plain, err := srv.UnwrapInPlace(buf[:n])
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(plain, payload) {
		t.Fatalf("plaintext mismatch: %q", plain)
	}
}

func TestWrapIntoRoundTrip(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	cli, _ := NewConn(key, false)
	srv, _ := NewConn(key, true)

	payload := []byte("xray tcp bytes over smux")
	dst := make([]byte, MaxWire(len(payload)))
	n, err := cli.WrapInto(dst, payload)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(payload))
	m, err := srv.Unwrap(dst[:n], out)
	if err != nil {
		t.Fatal(err)
	}
	if m != len(payload) || !bytes.Equal(out[:m], payload) {
		t.Fatalf("mismatch: got %q", out[:m])
	}
}

func TestHeaderShape(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	cli, _ := NewConn(key, false)

	payload := []byte("abc")
	buf := make([]byte, MaxWire(len(payload)))
	copy(buf[headerLen:], payload)
	if _, err := cli.WrapInPlace(buf, len(payload)); err != nil {
		t.Fatal(err)
	}

	if buf[0] != rtpVersion {
		t.Errorf("byte0 = 0x%02x, want 0x%02x (V=2, X=1)", buf[0], rtpVersion)
	}
	if buf[1] != (rtpMarker | rtpPT) {
		t.Errorf("byte1 = 0x%02x, want 0x%02x (M=1 + PT=111 on first packet)", buf[1], rtpMarker|rtpPT)
	}
	if buf[12] != 0xBE || buf[13] != 0xDE {
		t.Errorf("ext profile = 0x%02x%02x, want 0xBEDE", buf[12], buf[13])
	}
	if w := binary.BigEndian.Uint16(buf[14:16]); w != 2 {
		t.Errorf("ext length = %d words, want 2", w)
	}
	if buf[16] != extAudioLevelHdr || buf[18] != extTransportHdr {
		t.Errorf("ext element headers = 0x%02x 0x%02x, want 0x%02x 0x%02x", buf[16], buf[18], extAudioLevelHdr, extTransportHdr)
	}
	if buf[24]&0x80 != 0 {
		t.Errorf("client nonce sessionID MSB set, want clear (direction bit)")
	}

	// Второй пакет того же conn: marker bit снят.
	buf2 := make([]byte, MaxWire(len(payload)))
	copy(buf2[headerLen:], payload)
	if _, err := cli.WrapInPlace(buf2, len(payload)); err != nil {
		t.Fatal(err)
	}
	if buf2[1]&rtpMarker != 0 {
		t.Errorf("marker bit set on 2nd packet, want clear")
	}
}

func TestServerDirectionBit(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	srv, _ := NewConn(key, true)
	payload := []byte("x")
	buf := make([]byte, MaxWire(len(payload)))
	copy(buf[headerLen:], payload)
	if _, err := srv.WrapInPlace(buf, len(payload)); err != nil {
		t.Fatal(err)
	}
	if buf[24]&0x80 == 0 {
		t.Errorf("server nonce sessionID MSB clear, want set (direction bit)")
	}
}

func TestTamperDetected(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	cli, _ := NewConn(key, false)
	srv, _ := NewConn(key, true)

	payload := []byte("integrity matters")
	buf := make([]byte, MaxWire(len(payload)))
	copy(buf[headerLen:], payload)
	n, _ := cli.WrapInPlace(buf, len(payload))

	buf[n-1] ^= 0xFF // портим последний байт tag
	if _, err := srv.UnwrapInPlace(buf[:n]); err == nil {
		t.Fatal("expected AEAD open failure on tampered tag")
	}
}

func TestWrongKeyFails(t *testing.T) {
	t.Parallel()
	cli, _ := NewConn(newKey(t), false)
	srv, _ := NewConn(newKey(t), true) // другой ключ

	payload := []byte("secret")
	buf := make([]byte, MaxWire(len(payload)))
	copy(buf[headerLen:], payload)
	n, _ := cli.WrapInPlace(buf, len(payload))
	if _, err := srv.UnwrapInPlace(buf[:n]); err == nil {
		t.Fatal("expected failure decrypting with wrong key")
	}
}
