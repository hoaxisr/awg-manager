package adaptiverouting

import (
	"testing"
)

func TestParseSusaninLogLine(t *testing.T) {
	line1 := "14:22:10 INFO: [AUTO-SUSANIN: CONFIRMED] 142.251.155.6:443"
	ev1 := parseSusaninLogLine(line1)
	if ev1.Action != "CONFIRMED" {
		t.Fatalf("expected CONFIRMED, got %s", ev1.Action)
	}
	if ev1.Target != "142.251.155.6:443" {
		t.Fatalf("expected target 142.251.155.6:443, got %s", ev1.Target)
	}
	if ev1.ResourceTitle == "" {
		t.Fatalf("expected non-empty ResourceTitle for 142.251.* (Google/YouTube), got empty")
	}
	t.Logf("Line 1 parsed: action=%s, target=%s, title=%s, org=%s, cc=%s",
		ev1.Action, ev1.Target, ev1.ResourceTitle, ev1.ResourceOrg, ev1.ResourceCC)

	line2 := "14:22:15 WARN: [AUTO-SUSANIN: TCP-STALL] 104.21.50.1:443"
	ev2 := parseSusaninLogLine(line2)
	if ev2.Action != "STALL" {
		t.Fatalf("expected STALL, got %s", ev2.Action)
	}
	if ev2.ResourceTitle != "Cloudflare CDN" {
		t.Fatalf("expected Cloudflare CDN, got %s", ev2.ResourceTitle)
	}

	line3 := "14:22:20 INFO: [AUTO-SUSANIN: QUIC] 172.67.180.2:443"
	ev3 := parseSusaninLogLine(line3)
	if ev3.Action != "QUIC" {
		t.Fatalf("expected QUIC, got %s", ev3.Action)
	}
}

func TestReleaseBinaries_PinsComplete(t *testing.T) {
	requiredArches := []string{"aarch64", "mips", "mipsel", "armv7"}
	for _, arch := range requiredArches {
		spec, ok := ReleaseBinaries[arch]
		if !ok {
			t.Errorf("missing ReleaseBinaries for arch %q", arch)
			continue
		}
		if spec.Version == "" {
			t.Errorf("empty version for %q", arch)
		}
		if spec.SHA256 == "" {
			t.Errorf("empty SHA256 for %q", arch)
		}
		if spec.Size <= 0 {
			t.Errorf("invalid size for %q: %d", arch, spec.Size)
		}
		if spec.URL == "" {
			t.Errorf("missing URL for %q", arch)
		}
	}
}

