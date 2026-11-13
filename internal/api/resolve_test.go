package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestLookupResolveAllFamiliesDistinguishesFailureAndNODATA(t *testing.T) {
	for _, tc := range []struct {
		name      string
		aaaaError *net.DNSError
		wantError bool
	}{
		{name: "AAAA SERVFAIL", aaaaError: &net.DNSError{Err: "server failure"}, wantError: true},
		{name: "AAAA NODATA", aaaaError: &net.DNSError{Err: "no such host", IsNotFound: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ips, err := lookupResolveAllFamilies(context.Background(), "partial.example", func(_ context.Context, network, _ string) ([]net.IP, error) {
				if network == "ip4" {
					return []net.IP{net.ParseIP("192.0.2.77")}, nil
				}
				return nil, tc.aaaaError
			})
			if tc.wantError {
				if err == nil || ips != nil {
					t.Fatalf("partial family failure accepted: ips=%v err=%v", ips, err)
				}
				return
			}
			if err != nil || !slices.Equal(ips, []string{"192.0.2.77"}) {
				t.Fatalf("NODATA was not kept distinct: ips=%v err=%v", ips, err)
			}
		})
	}
}

func TestResolveHandlerFamily(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   []string
	}{
		{
			name:   "default is IPv4 only",
			target: "/api/routing/resolve?domain=example.com",
			want:   []string{"192.0.2.10", "192.0.2.11"},
		},
		{
			name:   "all returns valid IPv4 and IPv6",
			target: "/api/routing/resolve?domain=example.com&family=all",
			want:   []string{"192.0.2.10", "2001:db8::10", "192.0.2.11", "2001:db8::11"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewResolveHandler()
			handler.lookupHost = func(_ context.Context, domain string) ([]string, error) {
				if domain != "example.com" {
					t.Fatalf("lookup domain = %q, want example.com", domain)
				}
				return []string{
					"192.0.2.10",
					"2001:db8::10",
					"not-an-ip",
					"192.0.2.11",
					"2001:db8::11",
				}, nil
			}
			handler.lookupIP = func(_ context.Context, network, domain string) ([]net.IP, error) {
				if domain != "example.com" {
					t.Fatalf("lookup domain = %q, want example.com", domain)
				}
				if network == "ip4" {
					return []net.IP{net.ParseIP("192.0.2.10"), nil, net.ParseIP("192.0.2.11")}, nil
				}
				return []net.IP{net.ParseIP("2001:db8::10"), net.ParseIP("2001:db8::11")}, nil
			}

			recorder := httptest.NewRecorder()
			handler.Resolve(recorder, httptest.NewRequest(http.MethodGet, tt.target, nil))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			var body struct {
				Success bool            `json:"success"`
				Data    ResolveResponse `json:"data"`
			}
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if !body.Success {
				t.Fatal("success = false, want true")
			}
			if body.Data.Domain != "example.com" {
				t.Fatalf("domain = %q, want example.com", body.Data.Domain)
			}
			if !slices.Equal(body.Data.IPs, tt.want) {
				t.Fatalf("IPs = %v, want %v", body.Data.IPs, tt.want)
			}
		})
	}
}
