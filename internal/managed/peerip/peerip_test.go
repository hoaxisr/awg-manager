package peerip

import (
	"fmt"
	"testing"
)

// TestNextFree — адрес выдаётся вместо пользователя, поэтому ошибка здесь
// тихая: пир создастся и просто не будет работать. Повторяет
// suggestNextPeerIP из веб-интерфейса.
func TestNextFree(t *testing.T) {
	cases := []struct {
		name    string
		address string
		used    []string
		want    string
	}{
		{"first peer starts at .2", "10.0.0.1/24", nil, "10.0.0.2/32"},
		{"skips taken addresses", "10.0.0.1/24", []string{"10.0.0.2/32", "10.0.0.3/32"}, "10.0.0.4/32"},
		{"fills a gap left by a deleted peer", "10.0.0.1/24", []string{"10.0.0.3/32", "10.0.0.4/32"}, "10.0.0.2/32"},
		// The server's own address must never be handed to a client, even
		// when it sits in the middle of the range.
		{"never hands out the server address", "10.0.0.2/24", []string{}, "10.0.0.3/32"},
		{"address without a prefix works", "192.168.9.1", []string{"192.168.9.2"}, "192.168.9.3/32"},
		{"used entries without a prefix still count", "10.0.0.1/24", []string{"10.0.0.2"}, "10.0.0.3/32"},
		{"ignores entries that are not addresses", "10.0.0.1/24", []string{"", "garbage", "10.0.0.2/32"}, "10.0.0.3/32"},
		// No address is better than a wrong one: the caller must ask.
		{"refuses when the server address is unusable", "not-an-ip", nil, ""},
		{"refuses an IPv6 server address", "fd00::1/64", nil, ""},
	}
	for _, c := range cases {
		if got := NextFree(c.address, c.used); got != c.want {
			t.Errorf("%s: NextFree(%q, %v) = %q, want %q", c.name, c.address, c.used, got, c.want)
		}
	}
}

// TestNextFreeExhausted — когда свободных нет, пустая строка заставляет
// вызывающего сказать об этом, а не выдать .255 или .0.
func TestNextFreeExhausted(t *testing.T) {
	used := make([]string, 0, 253)
	for n := 2; n < 255; n++ {
		used = append(used, fmt.Sprintf("10.0.0.%d/32", n))
	}
	if got := NextFree("10.0.0.1/24", used); got != "" {
		t.Fatalf("a full subnet returned %q, want an empty result", got)
	}
}
