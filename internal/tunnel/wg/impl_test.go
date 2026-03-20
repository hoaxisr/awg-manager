package wg

import (
	"testing"
	"time"
)

func TestParseShowOutput_Empty(t *testing.T) {
	result := parseShowOutput("")
	if result.HasPeer {
		t.Error("Expected HasPeer=false for empty output")
	}
}

func TestParseShowOutput_NoPeer(t *testing.T) {
	output := `interface: opkgtun0
  public key: ABC123DEF456
  listening port: 51820
`
	result := parseShowOutput(output)

	if result.PublicKey != "ABC123DEF456" {
		t.Errorf("PublicKey = %q, want %q", result.PublicKey, "ABC123DEF456")
	}
	if result.ListenPort != 51820 {
		t.Errorf("ListenPort = %d, want %d", result.ListenPort, 51820)
	}
	if result.HasPeer {
		t.Error("Expected HasPeer=false")
	}
}

func TestParseShowOutput_WithPeer(t *testing.T) {
	output := `interface: opkgtun0
  public key: ABC123DEF456
  listening port: 51820

peer: XYZ789UVW012
  endpoint: 1.2.3.4:51820
  allowed ips: 0.0.0.0/0, ::/0
  latest handshake: 1 minute, 30 seconds ago
  transfer: 123.45 KiB received, 67.89 KiB sent
`
	result := parseShowOutput(output)

	if !result.HasPeer {
		t.Error("Expected HasPeer=true")
	}
	if result.PeerPublicKey != "XYZ789UVW012" {
		t.Errorf("PeerPublicKey = %q, want %q", result.PeerPublicKey, "XYZ789UVW012")
	}
	if result.Endpoint != "1.2.3.4:51820" {
		t.Errorf("Endpoint = %q, want %q", result.Endpoint, "1.2.3.4:51820")
	}
	if len(result.AllowedIPs) != 2 {
		t.Errorf("AllowedIPs count = %d, want 2", len(result.AllowedIPs))
	}
	if result.LastHandshake.IsZero() {
		t.Error("LastHandshake should not be zero")
	}
	if result.RxBytes == 0 {
		t.Error("RxBytes should not be zero")
	}
	if result.TxBytes == 0 {
		t.Error("TxBytes should not be zero")
	}
}

func TestParseHandshakeTime(t *testing.T) {
	tests := []struct {
		input    string
		wantZero bool
		minAge   time.Duration
		maxAge   time.Duration
	}{
		{"", true, 0, 0},
		{"none", true, 0, 0},
		{"30 seconds ago", false, 25 * time.Second, 35 * time.Second},
		{"1 minute, 30 seconds ago", false, 85 * time.Second, 95 * time.Second},
		{"2 hours, 5 minutes ago", false, 120*time.Minute + 4*time.Minute, 130 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseHandshakeTime(tt.input)
			if tt.wantZero {
				if !result.IsZero() {
					t.Error("Expected zero time")
				}
			} else {
				if result.IsZero() {
					t.Error("Expected non-zero time")
				}
				age := time.Since(result)
				if age < tt.minAge || age > tt.maxAge {
					t.Errorf("Age %v not in range [%v, %v]", age, tt.minAge, tt.maxAge)
				}
			}
		})
	}
}

func TestParseBytes(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"123 B", 123},
		{"123.45 KiB received", 126412}, // 123.45 * 1024
		{"67.89 KiB sent", 69519},       // 67.89 * 1024
		{"1.5 MiB received", 1572864},   // 1.5 * 1024 * 1024
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseBytes(tt.input)
			// Allow 1% tolerance for floating point
			diff := got - tt.want
			if diff < 0 {
				diff = -diff
			}
			maxDiff := tt.want / 100
			if maxDiff < 1 {
				maxDiff = 1
			}
			if diff > maxDiff {
				t.Errorf("parseBytes(%q) = %d, want %d (diff %d)", tt.input, got, tt.want, diff)
			}
		})
	}
}

func TestShowResult_HasRecentHandshake(t *testing.T) {
	// Test with recent handshake
	recent := &ShowResult{
		LastHandshake: time.Now().Add(-30 * time.Second),
	}
	if !recent.HasRecentHandshake(1 * time.Minute) {
		t.Error("Expected HasRecentHandshake=true for 30s old handshake")
	}
	if recent.HasRecentHandshake(10 * time.Second) {
		t.Error("Expected HasRecentHandshake=false for 30s old handshake with 10s threshold")
	}

	// Test with no handshake
	none := &ShowResult{}
	if none.HasRecentHandshake(1 * time.Minute) {
		t.Error("Expected HasRecentHandshake=false for zero time")
	}
}
