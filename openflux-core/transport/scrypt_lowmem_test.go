package transport

import (
	"bytes"
	"crypto/rand"
	"runtime"
	"testing"
	"time"

	"golang.org/x/crypto/scrypt"
)

// lowMemScrypt must give scrypt.Key's bytes for every stride, or peers that
// derive with one and the other would not share a key.
func TestLowMemScryptMatchesScrypt(t *testing.T) {
	for _, c := range []struct{ N, r, p, stride int }{
		{16, 1, 1, 1}, {16, 1, 1, 2}, {16, 1, 1, 16}, {1024, 8, 1, 8}, {1024, 8, 2, 4},
		{2048, 4, 1, 64}, {4096, 8, 1, 3 + 5}, {512, 2, 3, 512},
	} {
		for k := 0; k < 4; k++ {
			pw := make([]byte, 1+k*13)
			salt := make([]byte, 16)
			rand.Read(pw)
			rand.Read(salt)
			want, err := scrypt.Key(pw, salt, c.N, c.r, c.p, 32)
			if err != nil {
				t.Fatal(err)
			}
			got, err := lowMemScrypt(pw, salt, c.N, c.r, c.p, 32, c.stride)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("N=%d r=%d p=%d stride=%d: %x, want %x", c.N, c.r, c.p, c.stride, got, want)
			}
		}
	}
}

// The parameters masterKey uses, against the reference, and what it costs.
func TestLowMemScryptMasterParams(t *testing.T) {
	salt := []byte("openflux-master-salt-0000000000")
	pw := []byte("a secret of the length people use")
	t0 := time.Now()
	want, _ := scrypt.Key(pw, salt, 32768, 8, 1, 32)
	ref := time.Since(t0)
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	t0 = time.Now()
	got, err := lowMemScrypt(pw, salt, 32768, 8, 1, 32, scryptStride)
	low := time.Since(t0)
	runtime.ReadMemStats(&m1)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %x (%v), want %x", got, err, want)
	}
	alloc := m1.TotalAlloc - m0.TotalAlloc
	t.Logf("scrypt.Key %v; lowMemScrypt %v, %.1f MiB allocated", ref, low, float64(alloc)/(1<<20))
	if alloc > 5<<20 {
		t.Fatalf("allocated %d bytes, want under 5 MiB", alloc)
	}
}

func TestLowMemScryptRejectsBadParams(t *testing.T) {
	if _, err := lowMemScrypt(nil, nil, 1000, 8, 1, 32, 8); err == nil {
		t.Fatal("N not a power of two accepted")
	}
	if _, err := lowMemScrypt(nil, nil, 1024, 8, 1, 32, 3); err == nil {
		t.Fatal("stride not dividing N accepted")
	}
}
