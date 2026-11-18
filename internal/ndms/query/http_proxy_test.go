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
	if got.Name != "awgm" || got.UpstreamProto != "http" || got.UpstreamHost != "192.168.0.1" || got.UpstreamPort != "2222" || !got.DomainNDNS || !got.SSLRedirect || !got.Public || !got.Auth {
		t.Errorf("unexpected entry: %#v", got)
	}
	if got.RawJSON == "" {
		t.Errorf("expected RawJSON to be populated")
	}
}

func TestParseHTTPRuntimeProxies(t *testing.T) {
	raw := []byte(`{
		"tg": {
			"fqdn": "tg.example.keenetic.pro",
			"proto": "http",
			"upstream": "127.0.0.1:9009",
			"host": "127.0.0.1",
			"allow": "public",
			"ndns": true
		}
	}`)
	rt, err := parseHTTPRuntimeProxies(raw)
	if err != nil {
		t.Fatalf("parseHTTPRuntimeProxies: %v", err)
	}
	item, ok := rt["tg"]
	if !ok {
		t.Fatalf("expected 'tg' runtime entry")
	}
	if item.FQDN != "tg.example.keenetic.pro" || item.Upstream != "127.0.0.1:9009" || !item.NDNS || item.Allow != "public" {
		t.Errorf("unexpected runtime item: %#v", item)
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
