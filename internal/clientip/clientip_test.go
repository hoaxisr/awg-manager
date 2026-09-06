package clientip

import (
	"net/http/httptest"
	"testing"
)

func TestFromRequest(t *testing.T) {
	cases := map[string]string{
		"192.168.1.5:5555": "192.168.1.5",
		"[::1]:40003":      "::1",
		"10.0.0.1":         "10.0.0.1", // no port: returned verbatim
	}
	for addr, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = addr
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		if got := FromRequest(r); got != want {
			t.Errorf("FromRequest(%q) = %q, want %q", addr, got, want)
		}
	}
}
