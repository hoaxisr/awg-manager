package query

import "testing"

// Verbatim /show/rc/ip/http from a live router (2026-08-20).
const liveIPHTTP = `{
  "port": "80",
  "security-level": { "public": true },
  "lockout-policy": { "threshold": "5", "duration": "15", "observation-window": "3" },
  "ssl": { "enable": false, "port": "443" },
  "proxy": {
    "awgm": {
      "upstream": { "proto": "http", "upstream": "192.168.0.1", "port": "2222" },
      "domain": { "ndns": true },
      "ssl": { "redirect": true },
      "security-level": { "public": true },
      "auth": true
    }
  }
}`

func TestParseHTTPProxies_LiveShape(t *testing.T) {
	entries, err := parseHTTPProxies([]byte(liveIPHTTP))
	if err != nil {
		t.Fatalf("parseHTTPProxies: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	got := entries[0]
	want := HTTPProxyEntry{Name: "awgm", UpstreamPort: "2222", Public: true, Auth: true}
	if got != want {
		t.Errorf("entry = %#v, want %#v", got, want)
	}
}

func TestParseHTTPProxies_NoProxies(t *testing.T) {
	entries, err := parseHTTPProxies([]byte(`{"port":"80","security-level":{"public":true}}`))
	if err != nil {
		t.Fatalf("parseHTTPProxies: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestParseHTTPProxies_Empty(t *testing.T) {
	entries, err := parseHTTPProxies(nil)
	if err != nil {
		t.Fatalf("parseHTTPProxies: %v", err)
	}
	if entries != nil {
		t.Errorf("got %#v, want nil", entries)
	}
}
