package wire

import (
	"crypto/rand"
	"testing"
)

func TestNewClientCodec(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	// none/"" -> обфускация выключена (nil codec, без ошибки).
	for _, p := range []string{ProfileNone, ""} {
		c, err := NewClientCodec(p, nil)
		if err != nil || c != nil {
			t.Errorf("NewClientCodec(%q) = (%v, %v), want (nil, nil)", p, c, err)
		}
	}

	// rtpopus / rtpopus2 / rtpopus3 -> валидный codec с осмысленными размерами.
	for _, p := range []string{ProfileRTPOpus, ProfileRTPOpus2, ProfileRTPOpus3} {
		c, err := NewClientCodec(p, key)
		if err != nil || c == nil {
			t.Fatalf("NewClientCodec(%q) = (%v, %v), want non-nil codec", p, c, err)
		}
		if c.HeaderLen() <= 0 || c.Overhead() <= c.HeaderLen() {
			t.Errorf("%q: overhead=%d header=%d (overhead должен включать tag)", p, c.Overhead(), c.HeaderLen())
		}
		if c.MaxWire(100) != c.Overhead()+100 {
			t.Errorf("%q: MaxWire(100)=%d, want %d", p, c.MaxWire(100), c.Overhead()+100)
		}
	}

	// rtpopus2 несёт RTP extension -> заголовок больше, чем у v1.
	v1, _ := NewClientCodec(ProfileRTPOpus, key)
	v2, _ := NewClientCodec(ProfileRTPOpus2, key)
	if v2.HeaderLen() <= v1.HeaderLen() {
		t.Errorf("rtpopus2 header %d should exceed rtpopus %d (RTP extension)", v2.HeaderLen(), v1.HeaderLen())
	}

	if _, err := NewClientCodec("bogus", key); err == nil {
		t.Error("NewClientCodec(bogus): want error")
	}
}
