package frame

import (
	"bytes"
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	input := append(Encode(Open, MaxStreamID, nil), Encode(Data, 7, []byte("payload"))...)
	frames, err := ParseAll(input, MaxPayload)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0].StreamID != MaxStreamID || !bytes.Equal(frames[1].Payload, []byte("payload")) {
		t.Fatalf("unexpected frames: %#v", frames)
	}
}

func TestRejectsPartialAndOversized(t *testing.T) {
	if _, err := ParseAll(Encode(Data, 1, []byte("x"))[:8], MaxPayload); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("expected incomplete frame, got %v", err)
	}
	if _, err := ParseAll(Encode(Data, 1, []byte("xx")), 1); !errors.Is(err, ErrPayload) {
		t.Fatalf("expected oversized payload, got %v", err)
	}
}

func TestHelloAndWindow(t *testing.T) {
	if err := ParseHello(Encode(Hello, 0, []byte{1})); err != nil {
		t.Fatal(err)
	}
	if err := ParseHello(Encode(Hello, 0, []byte{2})); err == nil {
		t.Fatal("accepted unsupported protocol version")
	}
	if _, err := WindowAmount([]byte{0, 0, 0, 0}); err == nil {
		t.Fatal("accepted zero window")
	}
}

func TestRejectsExcessiveFrameCount(t *testing.T) {
	input := bytes.Repeat(Encode(Close, 1, nil), MaxBatchFrames+1)
	if _, err := ParseAll(input, MaxPayload); err == nil {
		t.Fatal("accepted excessive frame count")
	}
}
