package main

import (
	"net"
	"strings"
	"testing"
)

func TestShouldProbeExternalIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"", true},
		{"198.51.100.10", true},
		{"203.0.113.10", true},
		{"10.20.30.40", false},
		{"192.168.1.2", false},
		{"172.16.0.5", false},
		{"100.64.1.10", false},
		{"127.0.0.1", false},
	}

	for _, tc := range cases {
		var parsed net.IP
		if tc.ip != "" {
			parsed = net.ParseIP(tc.ip)
		}
		if got := shouldProbeExternalIP(parsed); got != tc.want {
			t.Errorf("shouldProbeExternalIP(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestClassifyRealityNetworkInfo(t *testing.T) {
	priv := classifyRealityNetworkInfo("10.20.30.40", "", "")
	if priv.WhiteIP {
		t.Fatalf("expected WhiteIP=false for private WAN, got %+v", priv)
	}
	if !strings.Contains(priv.Reason, "серый (приватный / CGNAT)") {
		t.Fatalf("expected gray-IP reason, got %q", priv.Reason)
	}
	if priv.PublicHost != "" {
		t.Fatalf("expected empty PublicHost on gray WAN without external/keendns, got %q", priv.PublicHost)
	}

	pubOK := classifyRealityNetworkInfo("198.51.100.10", "198.51.100.10", "home.example.keenetic.pro")
	if !pubOK.WhiteIP {
		t.Fatalf("expected WhiteIP=true when WAN matches ExternalIP, got %+v", pubOK)
	}
	if pubOK.PublicHost != "home.example.keenetic.pro" {
		t.Fatalf("expected keenDNSDirect as PublicHost, got %q", pubOK.PublicHost)
	}

	pubDirectNoKD := classifyRealityNetworkInfo("203.0.113.10", "203.0.113.10", "")
	if !pubDirectNoKD.WhiteIP || pubDirectNoKD.PublicHost != "203.0.113.10" {
		t.Fatalf("expected WhiteIP=true and IP PublicHost, got %+v", pubDirectNoKD)
	}

	natMismatch := classifyRealityNetworkInfo("203.0.113.10", "198.51.100.25", "")
	if natMismatch.WhiteIP {
		t.Fatalf("expected WhiteIP=false on NAT mismatch, got %+v", natMismatch)
	}
	if !strings.Contains(natMismatch.Reason, "NAT у провайдера") {
		t.Fatalf("expected NAT mismatch reason, got %q", natMismatch.Reason)
	}
	if natMismatch.PublicHost != "198.51.100.25" {
		t.Fatalf("expected fallback ExternalIP as PublicHost, got %q", natMismatch.PublicHost)
	}
}
