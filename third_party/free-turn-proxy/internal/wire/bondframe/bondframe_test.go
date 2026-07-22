package bondframe

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestHelloRoundTrip(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := WriteHello(&buf, 0xCAFEBABEDEADBEEF, 3, 7); err != nil {
		t.Fatalf("WriteHello: %v", err)
	}
	if buf.Len() != 17 {
		t.Fatalf("hello size = %d, want 17", buf.Len())
	}

	var magic [4]byte
	copy(magic[:], buf.Bytes()[:4])
	if string(magic[:]) != Magic {
		t.Fatalf("magic = %q, want %q", magic, Magic)
	}

	// Consume magic to simulate the server pre-peek path.
	buf.Next(4)
	got, err := ReadHelloAfterMagic(&buf, magic)
	if err != nil {
		t.Fatalf("ReadHelloAfterMagic: %v", err)
	}
	want := Hello{ConnID: 0xCAFEBABEDEADBEEF, LaneIndex: 3, LaneCount: 7}
	if got != want {
		t.Fatalf("hello = %+v, want %+v", got, want)
	}
}

func TestParseHelloHeader_BadInputs(t *testing.T) {
	t.Parallel()

	if _, err := ParseHelloHeader(make([]byte, 16)); err == nil {
		t.Errorf("short header should fail")
	}

	bad := make([]byte, 17)
	copy(bad[0:4], "XXXX")
	if _, err := ParseHelloHeader(bad); err == nil {
		t.Errorf("bad magic should fail")
	}

	bad = make([]byte, 17)
	copy(bad[0:4], Magic)
	bad[4] = 99
	if _, err := ParseHelloHeader(bad); err == nil {
		t.Errorf("unsupported version should fail")
	}
}

func TestFrameRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []Frame{
		{Type: FrameData, Seq: 0, Data: []byte("hello")},
		{Type: FrameData, Seq: 0xFFFFFFFFFFFFFFFF, Data: bytes.Repeat([]byte{0xAB}, 1500)},
		{Type: FrameFIN, Seq: 42, Data: nil},
	}
	for _, want := range cases {
		var buf bytes.Buffer
		if err := WriteFrame(&buf, want.Type, want.Seq, want.Data); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if got.Type != want.Type || got.Seq != want.Seq || !bytes.Equal(got.Data, want.Data) {
			t.Fatalf("frame = %+v, want %+v", got, want)
		}
	}
}

func TestReadFrame_RejectsOversize(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var hdr [13]byte
	hdr[0] = FrameData
	binary.BigEndian.PutUint32(hdr[9:13], 4*1024*1024+1)
	buf.Write(hdr[:])
	if _, err := ReadFrame(&buf); err == nil {
		t.Fatalf("oversize frame should be rejected")
	}
}

func TestReadFrame_TruncatedHeader(t *testing.T) {
	t.Parallel()

	buf := bytes.NewReader([]byte{1, 2, 3})
	_, err := ReadFrame(buf)
	if err == nil {
		t.Fatalf("truncated header should fail")
	}
}

func TestReadFrame_TruncatedPayload(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := WriteFrame(&buf, FrameData, 1, []byte("hello world")); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	truncated := buf.Bytes()[:13+5] // header + 5 of 11 payload bytes
	if _, err := ReadFrame(bytes.NewReader(truncated)); err == nil {
		t.Fatalf("truncated payload should fail")
	}
}

// Sanity: ReadHelloAfterMagic surfaces underlying EOF, not a silent zero.
func TestReadHelloAfterMagic_EOF(t *testing.T) {
	t.Parallel()

	var magic [4]byte
	copy(magic[:], Magic)
	if _, err := ReadHelloAfterMagic(bytes.NewReader(nil), magic); err == nil || err == io.ErrShortBuffer {
		t.Fatalf("expected EOF-like error, got %v", err)
	}
}
