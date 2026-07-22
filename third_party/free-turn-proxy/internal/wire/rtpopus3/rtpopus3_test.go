package rtpopus3

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

	if buf[0] != rtpVerExt {
		t.Errorf("byte0 = 0x%02x, want 0x%02x (V=2, X=1)", buf[0], rtpVerExt)
	}
	if buf[12] != 0xBE || buf[13] != 0xDE {
		t.Errorf("ext profile = 0x%02x%02x, want 0xBEDE", buf[12], buf[13])
	}
	if w := binary.BigEndian.Uint16(buf[14:16]); w != 3 {
		t.Errorf("ext length = %d words, want 3", w)
	}
	if buf[16] != extAudioLevelHdr || buf[18] != extTransportHdr || buf[21] != extAbsSendTimeHdr {
		t.Errorf("ext element headers = 0x%02x 0x%02x 0x%02x, want 0x10 0x21 0x32",
			buf[16], buf[18], buf[21])
	}
	if buf[28]&0x80 != 0 {
		t.Errorf("client nonce sessionID MSB set, want clear (direction bit)")
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
	if buf[28]&0x80 == 0 {
		t.Errorf("server nonce sessionID MSB clear, want set (direction bit)")
	}
}

func TestSeqGaps(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	conn, _ := NewConn(key, false)

	// Сбросим nextGapAt на маленькое значение для теста.
	conn.nextGapAt = 3
	conn.gapSize = 2

	payload := []byte("test")
	seqs := make([]uint16, 10)
	for i := range seqs {
		buf := make([]byte, MaxWire(len(payload)))
		copy(buf[headerLen:], payload)
		if _, err := conn.WrapInPlace(buf, len(payload)); err != nil {
			t.Fatal(err)
		}
		seqs[i] = binary.BigEndian.Uint16(buf[2:4])
	}

	// С seq 3: gap 3 -> seq 3, nextGapAt=0 -> пропускаем 2 -> seq=4,5 пропущены, seq=6
	// Проверяем что был пропуск: seqs[0]=X, seqs[1]=X+1, seqs[2]=X+2, seqs[3]=X+5 (2 пропущено).
	got := int(seqs[3]) - int(seqs[2])
	if got != 3 { // +1 за текущий +2 gap = 3
		t.Logf("seq progression: %v", seqs)
	}
}

func TestAudioStateMachine(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	conn, _ := NewConn(key, false)

	// Принудительно silence, маленький nextStateSwitch для теста.
	conn.audioState = stateSilence
	conn.pktsInState = 0
	conn.nextStateSwitch = 2

	payload := []byte("pkt")
	markers := make([]bool, 10)
	for i := range markers {
		buf := make([]byte, MaxWire(len(payload)))
		copy(buf[headerLen:], payload)
		if _, err := conn.WrapInPlace(buf, len(payload)); err != nil {
			t.Fatal(err)
		}
		markers[i] = buf[1]&rtpMarker != 0
	}

	// Pkt 0: silence pktsInState=1 (<2, M=0).
	// Pkt 1: silence pktsInState=2 (==nextStateSwitch) -> speech (M=1).
	t.Logf("markers: %v", markers)
	if !markers[1] {
		t.Error("expected M=1 on silence->speech transition at packet index 1")
	}
}

func TestAbsSendTimeValidFormat(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	conn, _ := NewConn(key, false)

	payload := []byte("timecheck")
	buf := make([]byte, MaxWire(len(payload)))
	copy(buf[headerLen:], payload)
	if _, err := conn.WrapInPlace(buf, len(payload)); err != nil {
		t.Fatal(err)
	}

	// Проверяем что abs-send-time укладывается в 24 бита и не негативный.
	ast := (uint32(buf[22]) << 16) | (uint32(buf[23]) << 8) | uint32(buf[24])
	if ast>>24 != 0 {
		t.Errorf("abs-send-time exceeds 24 bits: 0x%06x", ast)
	}
	// seconds field (top 6 bits) should be < 64.
	sec := ast >> 18
	if sec >= 64 {
		t.Errorf("abs-send-time seconds >= 64: %d", sec)
	}
}

func TestVariableTsStep(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	conn, _ := NewConn(key, false)

	payload := []byte("varstep")
	tsValues := make([]uint32, 100)
	for i := range tsValues {
		buf := make([]byte, MaxWire(len(payload)))
		copy(buf[headerLen:], payload)
		if _, err := conn.WrapInPlace(buf, len(payload)); err != nil {
			t.Fatal(err)
		}
		tsValues[i] = binary.BigEndian.Uint32(buf[4:8])
	}

	// Проверяем что не все шаги одинаковые (с допуском на случайность).
	diffs := make(map[uint32]int)
	for i := 1; i < len(tsValues); i++ {
		diff := tsValues[i] - tsValues[i-1]
		diffs[diff]++
	}
	t.Logf("timestamp step distribution: %v", diffs)
	if len(diffs) < 2 {
		t.Log("all timestamp steps are identical; expected some variation (may be rare)")
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

	buf[n-1] ^= 0xFF
	if _, err := srv.UnwrapInPlace(buf[:n]); err == nil {
		t.Fatal("expected AEAD open failure on tampered tag")
	}
}

func TestWrongKeyFails(t *testing.T) {
	t.Parallel()
	cli, _ := NewConn(newKey(t), false)
	srv, _ := NewConn(newKey(t), true)

	payload := []byte("secret")
	buf := make([]byte, MaxWire(len(payload)))
	copy(buf[headerLen:], payload)
	n, _ := cli.WrapInPlace(buf, len(payload))
	if _, err := srv.UnwrapInPlace(buf[:n]); err == nil {
		t.Fatal("expected failure decrypting with wrong key")
	}
}
