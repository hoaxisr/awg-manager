package domain

import "testing"

func TestZone(t *testing.T) {
	for host, want := range map[string]string{
		"WWW.Example.CO.UK.": "example.co.uk",
		"www.example.com":    "example.com",
		"example.com":        "example.com",
		"user.github.io":     "user.github.io", // shared-hosting suffix
		"localhost":          "localhost",      // safe exact fallback
	} {
		if got := Zone(host); got != want {
			t.Errorf("Zone(%q)=%q, want %q", host, got, want)
		}
	}
	if got := Zone("192.0.2.1"); got != "" {
		t.Fatalf("IP zone=%q", got)
	}
}
