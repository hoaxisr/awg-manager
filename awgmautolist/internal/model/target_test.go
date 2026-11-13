package model

import (
	"strings"
	"testing"
)

func TestTargetIdentity(t *testing.T) {
	domain, err := NewTarget("WWW.Example.COM.", "192.0.2.10", NetworkTCP, 443)
	if err != nil {
		t.Fatal(err)
	}
	if domain.Domain != "www.example.com" || domain.CandidateKey() != "domain|www.example.com|tcp|443" {
		t.Fatalf("unexpected domain identity: %+v %q", domain, domain.CandidateKey())
	}
	ip, err := NewTarget("", "2001:0db8::1", NetworkUDP, 3478)
	if err != nil {
		t.Fatal(err)
	}
	if ip.IP != "2001:db8::1" || ip.CandidateKey() != "ip|2001:db8::1" {
		t.Fatalf("unexpected IP identity: %+v %q", ip, ip.CandidateKey())
	}
	otherPort, err := NewTarget("", "2001:db8::1", NetworkUDP, 50000)
	if err != nil {
		t.Fatal(err)
	}
	if ip.CandidateKey() != otherPort.CandidateKey() || ip.ProbeKey() == otherPort.ProbeKey() {
		t.Fatalf("IP candidate/probe keys are confused: %q %q", ip.ProbeKey(), otherPort.ProbeKey())
	}
}

func TestObservedTargetSeparatesSNIAndRegisteredZone(t *testing.T) {
	target, err := NewObservedTarget("WWW.Example.CO.UK.", "192.0.2.10", NetworkTCP, 443)
	if err != nil {
		t.Fatal(err)
	}
	if target.Host != "www.example.co.uk" || target.Domain != "example.co.uk" {
		t.Fatalf("target=%+v", target)
	}
	if target.CandidateKey() != "domain|example.co.uk|tcp|443" || !strings.HasPrefix(target.ProbeKey(), "www.example.co.uk|") {
		t.Fatalf("identity/probe mismatch: %q %q", target.CandidateKey(), target.ProbeKey())
	}
}

func TestTargetRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		domain, ip string
		network    Network
		port       uint16
	}{
		{"", "", NetworkTCP, 443},
		{"bad/name", "", NetworkTCP, 443},
		{"", "not-an-ip", NetworkTCP, 443},
		{"example.com", "", "sctp", 443},
		{"example.com", "", NetworkTCP, 0},
	} {
		if _, err := NewTarget(test.domain, test.ip, test.network, test.port); err == nil {
			t.Fatalf("accepted invalid target: %+v", test)
		}
	}
}
