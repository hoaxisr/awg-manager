package bypassset

import (
	"bytes"
	"strings"
	"testing"
)

// ── NormalizeEntry ────────────────────────────────────────────────────────────

func TestNormalizeEntry_ValidCIDR(t *testing.T) {
	if got := NormalizeEntry("10.0.0.0/8"); got != "10.0.0.0/8" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeEntry_BareIPBecomesSlash32(t *testing.T) {
	if got := NormalizeEntry("1.2.3.4"); got != "1.2.3.4/32" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeEntry_CanonicalisesCIDR(t *testing.T) {
	// Host bits should be masked: 10.0.0.1/8 → 10.0.0.0/8
	if got := NormalizeEntry("10.0.0.1/8"); got != "10.0.0.0/8" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeEntry_IPv6Rejected(t *testing.T) {
	if got := NormalizeEntry("::1/128"); got != "" {
		t.Errorf("expected empty for IPv6, got %q", got)
	}
}

func TestNormalizeEntry_IPv6BareRejected(t *testing.T) {
	if got := NormalizeEntry("fe80::1"); got != "" {
		t.Errorf("expected empty for IPv6, got %q", got)
	}
}

func TestNormalizeEntry_GarbageRejected(t *testing.T) {
	if got := NormalizeEntry("not-an-ip"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestNormalizeEntry_EmptyString(t *testing.T) {
	if got := NormalizeEntry(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestNormalizeEntry_Whitespace(t *testing.T) {
	if got := NormalizeEntry("  1.2.3.4  "); got != "1.2.3.4/32" {
		t.Errorf("got %q", got)
	}
}

// ── IPSetBinary path detection ────────────────────────────────────────────────

func TestIPSetBinary_ReturnsPresentPath(t *testing.T) {
	// Stub executable that passes the health probe (`ipset version` → exit 0).
	bin := writeStubIPSet(t, "#!/bin/sh\nexit 0\n")
	original := ipsetBinaryPaths
	ipsetBinaryPaths = []string{bin}
	defer func() {
		ipsetBinaryPaths = original
		resetIPSetHealthForTest()
	}()
	resetIPSetHealthForTest()

	if got := IPSetBinary(); got != bin {
		t.Errorf("expected %q, got %q", bin, got)
	}
	if !IsIPSetAvailable() {
		t.Error("IsIPSetAvailable() should be true when binary exists and runs")
	}
}

func TestIPSetBinary_ReturnsEmptyWhenNotFound(t *testing.T) {
	original := ipsetBinaryPaths
	ipsetBinaryPaths = []string{"/nonexistent/path/ipset-xyz"}
	defer func() {
		ipsetBinaryPaths = original
		resetIPSetHealthForTest()
	}()
	resetIPSetHealthForTest()

	if got := IPSetBinary(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	if IsIPSetAvailable() {
		t.Error("IsIPSetAvailable() should be false when binary missing")
	}
}

// ── chunkedAddToSet — restore input format ────────────────────────────────────
// We can't call the real `ipset` in unit tests, but writeRestoreLines is the
// exact production renderer addEntriesToSet pipes to `ipset restore -exist`.

func restoreInput(cidrs []string) string {
	var b bytes.Buffer
	writeRestoreLines(&b, SetName, cidrs)
	return b.String()
}

func TestRestoreInput_BatchFormat(t *testing.T) {
	input := restoreInput([]string{"1.2.3.0/24", "5.6.7.8", "invalid"})
	lines := strings.Split(strings.TrimSpace(input), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (invalid skipped), got %d:\n%s", len(lines), input)
	}
	if !strings.Contains(lines[0], "1.2.3.0/24") {
		t.Errorf("line 0: %q", lines[0])
	}
	if !strings.Contains(lines[1], "5.6.7.8/32") {
		t.Errorf("line 1: %q", lines[1])
	}
}

func TestRestoreInput_EmptyInput_NoOutput(t *testing.T) {
	input := restoreInput(nil)
	if input != "" {
		t.Errorf("expected empty output for nil input, got %q", input)
	}
}

func TestRestoreInput_AllInvalid_NoOutput(t *testing.T) {
	input := restoreInput([]string{"::1", "garbage", ""})
	if input != "" {
		t.Errorf("expected empty output for all-invalid input, got %q", input)
	}
}

func TestRestoreInput_SetNameInOutput(t *testing.T) {
	input := restoreInput([]string{"10.0.0.1"})
	if !strings.Contains(input, SetName) {
		t.Errorf("expected %q in output, got %q", SetName, input)
	}
}
