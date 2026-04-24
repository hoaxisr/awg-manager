package dnsroute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseDomainLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Plain domains
		{"plain", "example.com", "example.com"},
		{"plain with spaces", "  example.com  ", "example.com"},
		{"uppercase", "Example.COM", "example.com"},

		// Comments and empty
		{"empty", "", ""},
		{"hash comment", "# this is a comment", ""},
		{"exclamation comment", "! adblock comment", ""},

		// Hosts format
		{"hosts zero", "0.0.0.0 ads.example.com", "ads.example.com"},
		{"hosts localhost", "127.0.0.1 tracker.example.com", "tracker.example.com"},
		{"hosts with tabs", "0.0.0.0\tads.example.com", "ads.example.com"},

		// Adblock
		{"adblock basic", "||example.com^", "example.com"},
		{"adblock no caret", "||example.com", "example.com"},

		// URLs with scheme
		{"https url", "https://example.com/path", "example.com"},
		{"http url", "http://example.com/path/page", "example.com"},

		// Wildcard
		{"wildcard prefix", "*.example.com", "example.com"},

		// Port
		{"with port", "example.com:8080", "example.com"},

		// Invalid entries
		{"leading dot TLD", ".ua", "ua"},

		// Filtered entries
		{"localhost", "localhost", ""},
		{"private IP 192.168", "192.168.1.1", ""},
		{"private IP 10.x", "10.0.0.1", ""},
		{"private IP 172.16", "172.16.0.1", ""},
		{"loopback IP", "127.0.0.1", ""},
		{"private CIDR", "192.168.0.0/24", ""},
		{"private CIDR 10", "10.0.0.0/8", ""},
		// Public CIDRs pass through with prefix preserved
		{"public CIDR /24", "8.8.4.0/24", "8.8.4.0/24"},
		{"public CIDR /32", "8.8.8.8/32", "8.8.8.8/32"},
		{"public CIDR normalized", "1.2.3.4/24", "1.2.3.0/24"},
		// URL with path still stripped (not a CIDR)
		{"domain with path", "example.com/path", "example.com"},
		// Public IPs pass through
		{"public IP", "8.8.8.8", "8.8.8.8"},
		{"contains space", "not a domain.com", ""},
		{"contains wildcard mid", "ex*ample.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDomainLine(tt.input)
			if got != tt.want {
				t.Errorf("parseDomainLine(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeGitHubURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"blob URL", "https://github.com/user/repo/blob/main/list.txt", "https://raw.githubusercontent.com/user/repo/main/list.txt"},
		{"blob with branch path", "https://github.com/user/repo/blob/master/dir/file.txt", "https://raw.githubusercontent.com/user/repo/master/dir/file.txt"},
		{"tree URL", "https://github.com/user/repo/tree/main/dir/file.txt", "https://raw.githubusercontent.com/user/repo/main/dir/file.txt"},
		{"already raw", "https://raw.githubusercontent.com/user/repo/main/list.txt", "https://raw.githubusercontent.com/user/repo/main/list.txt"},
		{"non-github", "https://example.com/domains.txt", "https://example.com/domains.txt"},
		{"github root no blob", "https://github.com/user/repo", "https://github.com/user/repo"},
		{"github non-blob action", "https://github.com/user/repo/issues/1", "https://github.com/user/repo/issues/1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeGitHubURL(tt.input)
			if got != tt.want {
				t.Errorf("normalizeGitHubURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestMergeDomains(t *testing.T) {
	t.Run("manual first then subscriptions", func(t *testing.T) {
		manual := []string{"a.com", "b.com"}
		subs := [][]string{{"c.com", "d.com"}, {"e.com"}}
		got := mergeDomains(manual, subs)
		want := []string{"a.com", "b.com", "c.com", "d.com", "e.com"}
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("deduplication across sources", func(t *testing.T) {
		manual := []string{"a.com", "b.com"}
		subs := [][]string{{"b.com", "c.com"}, {"a.com", "d.com"}}
		got := mergeDomains(manual, subs)
		want := []string{"a.com", "b.com", "c.com", "d.com"}
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("empty inputs", func(t *testing.T) {
		got := mergeDomains(nil, nil)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("manual normalizes case and whitespace", func(t *testing.T) {
		manual := []string{"  A.COM  ", "a.com"}
		got := mergeDomains(manual, nil)
		if len(got) != 1 || got[0] != "a.com" {
			t.Errorf("got %v, want [a.com]", got)
		}
	})
}

func TestFetchSubscription_ContentType(t *testing.T) {
	t.Run("accepts application/octet-stream", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("8.8.8.0/24\nexample.com\n"))
		}))
		defer srv.Close()

		got, err := fetchSubscription(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("fetchSubscription() error = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len(got) = %d, want 2 (%v)", len(got), got)
		}
	})

	t.Run("rejects json content-type", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		defer srv.Close()

		_, err := fetchSubscription(context.Background(), srv.URL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "неподдерживаемый формат") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
