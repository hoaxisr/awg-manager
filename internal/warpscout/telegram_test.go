package warpscout

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func testResPQ(request []byte) []byte {
	response := make([]byte, 84)
	binary.LittleEndian.PutUint64(response[8:16], uint64(time.Now().Unix())<<32|1)
	binary.LittleEndian.PutUint32(response[16:20], 64)
	binary.LittleEndian.PutUint32(response[20:24], 0x05162463)
	copy(response[24:40], request[26:42])
	for i := 40; i < 56; i++ {
		response[i] = byte(i)
	}
	response[56] = 8
	copy(response[57:65], []byte{1, 2, 3, 4, 5, 6, 7, 9})
	binary.LittleEndian.PutUint32(response[68:72], 0x1cb5c415)
	binary.LittleEndian.PutUint32(response[72:76], 1)
	binary.LittleEndian.PutUint64(response[76:84], 0xc3b42b026ce86b21)
	return response
}

func answerTestTelegram(c net.Conn, mutate func([]byte) []byte) error {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	request := make([]byte, 42)
	if _, err := io.ReadFull(c, request); err != nil {
		return err
	}
	if request[0] != 0xef || request[1] != 10 ||
		binary.LittleEndian.Uint32(request[22:26]) != 0xbe7e8ef1 ||
		binary.LittleEndian.Uint64(request[2:10]) != 0 ||
		binary.LittleEndian.Uint64(request[10:18])%4 != 0 {
		return io.ErrUnexpectedEOF
	}
	response := testResPQ(request)
	if mutate != nil {
		response = mutate(response)
	}
	frame := append([]byte{byte(len(response) / 4)}, response...)
	for _, b := range frame {
		if _, err := c.Write([]byte{b}); err != nil {
			return err
		}
	}
	return nil
}

func TestTelegramRequiresProtocolResponseAndNonce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
		valid  bool
	}{
		{"valid resPQ", nil, true},
		{"wrong nonce", func(b []byte) []byte { b[24] ^= 1; return b }, false},
		{"wrong constructor", func(b []byte) []byte { b[20] = 0; return b }, false},
		{"invalid vector", func(b []byte) []byte { b[72] = 32; return b }, false},
		{"wrong envelope length", func(b []byte) []byte { b[16] = 60; return b }, false},
		{"HTTP front end", func([]byte) []byte { return []byte(strings.Repeat("HTTP", 21)) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			dial := func(context.Context, string, string) (net.Conn, error) {
				a, b := net.Pipe()
				go func() { defer close(done); _ = answerTestTelegram(b, tc.mutate) }()
				return a, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := probeTelegram(ctx, dial, "149.154.167.51:443")
			<-done
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v, want valid=%v", err, tc.valid)
			}
		})
	}
}

func TestTelegramOpenPortWithoutReplyFailsAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	dial := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			defer close(done)
			request := make([]byte, 42)
			_, _ = io.ReadFull(b, request)
			close(started)
			_, _ = io.Copy(io.Discard, b)
		}()
		return a, nil
	}
	errCh := make(chan error, 1)
	go func() { errCh <- probeTelegram(ctx, dial, "149.154.167.51:443") }()
	<-started
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("open port accepted without MTProto resPQ")
		}
	case <-time.After(time.Second):
		t.Fatal("probe did not cancel promptly")
	}
	<-done
}

func TestCheckTelegramQueriesDC2AndDC4(t *testing.T) {
	seen := map[string]bool{}
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		seen[addr] = true
		a, b := net.Pipe()
		go func() { _ = answerTestTelegram(b, nil) }()
		return a, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := checkTelegram(ctx, dial); err != nil {
		t.Fatal(err)
	}
	if !seen["149.154.167.51:443"] || !seen["149.154.167.91:443"] {
		t.Fatalf("expected DC2 and DC4, got %+v", seen)
	}
}
