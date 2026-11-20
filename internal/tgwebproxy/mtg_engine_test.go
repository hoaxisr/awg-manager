package tgwebproxy

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/essentials"
	"github.com/9seconds/mtg/v2/mtglib"
	"github.com/9seconds/mtg/v2/mtglib/obfuscation"

	mtgctr "github.com/hoaxisr/awg-manager/internal/tgwebproxy/ctr"
)

func allocateFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ephemeral port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func TestMtgEngine_StartStopAndSecretMatching(t *testing.T) {
	directPort := allocateFreePort(t)
	rawPort := allocateFreePort(t)

	engine := NewMtgEngine()
	defer engine.Stop()

	cfg := Config{
		Enabled:      true,
		Scenario:     ScenarioDual,
		DirectPort:   directPort,
		Backend:      fmt.Sprintf("127.0.0.1:%d", rawPort),
		Secret:       "00112233445566778899aabbccddeeff",
		LegacySecret: "ffeeddccbbaa99887766554433221100",
		TlsDomain:    "ya.ru",
	}

	if err := engine.Apply(cfg); err != nil {
		t.Fatalf("MtgEngine.Apply failed: %v", err)
	}

	if !engine.IsDirectRunning() {
		t.Fatalf("expected IsDirectRunning() == true")
	}
	if !engine.IsRawRunning() {
		t.Fatalf("expected IsRawRunning() == true")
	}

	// Verify idempotent Apply with same config
	if err := engine.Apply(cfg); err != nil {
		t.Fatalf("second MtgEngine.Apply failed: %v", err)
	}

	// Verify matchObfuscated2Secret matches both primary (0) and legacy (1) secrets
	sec0, err := parseMtgSecret(cfg.Secret, cfg.TlsDomain)
	if err != nil {
		t.Fatalf("parseMtgSecret sec0: %v", err)
	}
	sec1, err := parseMtgSecret(cfg.LegacySecret, cfg.TlsDomain)
	if err != nil {
		t.Fatalf("parseMtgSecret sec1: %v", err)
	}
	secrets := []mtglib.Secret{sec0, sec1}

	for wantIdx, sec := range secrets {
		c1, c2 := net.Pipe()
		doneCh := make(chan []byte, 1)
		go func() {
			defer c2.Close()
			buf := make([]byte, 64)
			_, _ = c2.Read(buf)
			doneCh <- buf
		}()
		_, err := (obfuscation.Obfuscator{Secret: sec.Key[:]}).SendHandshake(essentials.WrapNetConn(c1), 2)
		_ = c1.Close()
		if err != nil {
			t.Fatalf("SendHandshake for secret %d failed: %v", wantIdx, err)
		}
		frame := <-doneCh
		gotIdx := matchObfuscated2Secret(frame, secrets)
		if gotIdx != wantIdx {
			t.Errorf("matchObfuscated2Secret got index %d, want %d", gotIdx, wantIdx)
		}
	}

	// Verify empty probe on rawPort is cleanly closed without error
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", rawPort), time.Second)
	if err != nil {
		t.Fatalf("dial rawPort failed: %v", err)
	}
	_ = conn.Close()

	// Disable engine
	cfg.Enabled = false
	if err := engine.Apply(cfg); err != nil {
		t.Fatalf("MtgEngine.Apply(disabled) failed: %v", err)
	}
	if engine.IsDirectRunning() || engine.IsRawRunning() {
		t.Errorf("expected both listeners stopped after disabling")
	}
}

func TestMultiUserProfilesAndReveal(t *testing.T) {
	tmpDir := t.TempDir()
	svc := New(tmpDir, nil)
	defer svc.Close()

	// Initially 1 default user
	st0 := svc.GetStatus()
	if len(st0.Users) != 1 || st0.Users[0].ID != "default" {
		t.Fatalf("expected 1 default user initially, got %+v", st0.Users)
	}

	// Create second user
	if err := svc.CreateUser("Брат"); err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	st1 := svc.GetStatus()
	if len(st1.Users) != 2 {
		t.Fatalf("expected 2 users after CreateUser, got %d", len(st1.Users))
	}
	user2ID := st1.Users[1].ID

	// Reveal specific user secret
	rev2, err := svc.RevealUserSecret(user2ID)
	if err != nil {
		t.Fatalf("RevealUserSecret failed: %v", err)
	}
	if rev2.UserID != user2ID || rev2.UserRemark != "Брат" {
		t.Errorf("unexpected reveal user metadata: %+v", rev2)
	}
	if len(rev2.Users) != 2 {
		t.Errorf("expected 2 user entries in RevealData.Users, got %d", len(rev2.Users))
	}
	if rev2.Users[0].Secret == rev2.Users[1].Secret {
		t.Errorf("expected distinct secrets for different users")
	}

	// Toggle second user off
	disabled := false
	if err := svc.UpdateUser(user2ID, "", &disabled); err != nil {
		t.Fatalf("UpdateUser disable failed: %v", err)
	}
	activeSecs := svc.GetInternalConfig().ActiveSecrets()
	if len(activeSecs) != 1 {
		t.Errorf("expected 1 active secret when user 2 is disabled, got %d", len(activeSecs))
	}

	// Delete second user
	if err := svc.DeleteUser(user2ID); err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}
	if len(svc.GetStatus().Users) != 1 {
		t.Errorf("expected 1 user after DeleteUser")
	}
}

func TestObfuscatedStreamConnWrite_PooledChunksExactCiphertext(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	iv := bytes.Repeat([]byte{0x19}, 16)
	blkSend, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	blkRecv, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	wrapped := &obfuscatedStreamConn{
		Conn:       c1,
		sendCipher: cipher.NewCTR(blkSend, iv),
	}
	recvCTR := cipher.NewCTR(blkRecv, iv)

	if n, err := wrapped.Write(nil); err != nil || n != 0 {
		t.Fatalf("expected zero-byte Write to return (0, nil), got (%d, %v)", n, err)
	}

	payload := bytes.Repeat([]byte("mtproto-pooled-chunk-test-"), 3500) // ~91 KiB (> 2.8x 32 KiB pool buf)
	errCh := make(chan error, 1)
	go func() {
		n, werr := wrapped.Write(payload)
		if werr != nil {
			errCh <- werr
			return
		}
		if n != len(payload) {
			errCh <- fmt.Errorf("short write %d != %d", n, len(payload))
			return
		}
		errCh <- nil
	}()

	gotCipher := make([]byte, len(payload))
	if _, rerr := io.ReadFull(c2, gotCipher); rerr != nil {
		t.Fatalf("read cipher from pipe: %v", rerr)
	}
	if werr := <-errCh; werr != nil {
		t.Fatalf("wrapped.Write failed: %v", werr)
	}

	gotPlain := make([]byte, len(payload))
	recvCTR.XORKeyStream(gotPlain, gotCipher)
	if !bytes.Equal(gotPlain, payload) {
		t.Fatalf("decrypted payload mismatch after multi-chunk pooled Write")
	}
}

func TestObfuscatedStreamConn_MtgCtrRoundTrip(t *testing.T) {
	keySend := bytes.Repeat([]byte{0x11}, 32)
	ivSend := bytes.Repeat([]byte{0x22}, 16)
	keyRecv := bytes.Repeat([]byte{0x33}, 32)
	ivRecv := bytes.Repeat([]byte{0x44}, 16)

	aSend, err := mtgctr.New(keySend, ivSend)
	if err != nil {
		t.Fatalf("mtgctr.New aSend: %v", err)
	}
	defer mtgctr.Release(aSend)
	aRecv, err := mtgctr.New(keyRecv, ivRecv)
	if err != nil {
		t.Fatalf("mtgctr.New aRecv: %v", err)
	}
	defer mtgctr.Release(aRecv)

	bRecv, err := mtgctr.New(keySend, ivSend)
	if err != nil {
		t.Fatalf("mtgctr.New bRecv: %v", err)
	}
	defer mtgctr.Release(bRecv)
	bSend, err := mtgctr.New(keyRecv, ivRecv)
	if err != nil {
		t.Fatalf("mtgctr.New bSend: %v", err)
	}
	defer mtgctr.Release(bSend)

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	streamA := &obfuscatedStreamConn{Conn: c1, sendCipher: aSend, recvCipher: aRecv}
	streamB := &obfuscatedStreamConn{Conn: c2, sendCipher: bSend, recvCipher: bRecv}

	msgA := bytes.Repeat([]byte("tgwebproxy-ctr-a-to-b-"), 2000)
	msgB := bytes.Repeat([]byte("tgwebproxy-ctr-b-to-a-"), 1500)

	errCh := make(chan error, 2)
	go func() {
		if _, werr := streamA.Write(msgA); werr != nil {
			errCh <- fmt.Errorf("streamA.Write: %w", werr)
			return
		}
		gotB := make([]byte, len(msgB))
		if _, rerr := io.ReadFull(streamA, gotB); rerr != nil {
			errCh <- fmt.Errorf("streamA.Read: %w", rerr)
			return
		}
		if !bytes.Equal(gotB, msgB) {
			errCh <- fmt.Errorf("streamA decrypted reply mismatch")
			return
		}
		errCh <- nil
	}()
	go func() {
		gotA := make([]byte, len(msgA))
		if _, rerr := io.ReadFull(streamB, gotA); rerr != nil {
			errCh <- fmt.Errorf("streamB.Read: %w", rerr)
			return
		}
		if !bytes.Equal(gotA, msgA) {
			errCh <- fmt.Errorf("streamB decrypted payload mismatch")
			return
		}
		if _, werr := streamB.Write(msgB); werr != nil {
			errCh <- fmt.Errorf("streamB.Write: %w", werr)
			return
		}
		errCh <- nil
	}()

	for range 2 {
		if e := <-errCh; e != nil {
			t.Fatal(e)
		}
	}
}
