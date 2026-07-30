package wdtt

import "testing"

func TestParseOpkgTunIndex(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"OpkgTun17", 17, true},
		{"OpkgTun49", 49, true},
		{"OpkgTun90", 90, true},
		{"opkgtun17", 0, false},
		{"wdtt0", 0, false},
	}
	for _, tc := range tests {
		got, ok := parseOpkgTunIndex(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseOpkgTunIndex(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestServerConfigUsesNDMSOpkgTun(t *testing.T) {
	cfg := ServerConfig{NdmsIface: "OpkgTun20", WgIface: "opkgtun20"}
	if !cfg.usesNDMSOpkgTun() {
		t.Fatal("expected NDMS OpkgTun mode")
	}
	legacy := DefaultServerConfig()
	if legacy.usesNDMSOpkgTun() {
		t.Fatal("legacy wdtt0 must not use NDMS OpkgTun mode")
	}
}

func TestAllocateWdttOpkgIndex(t *testing.T) {
	idx, err := allocateWdttOpkgIndex(map[int]bool{17: true, 18: true})
	if err != nil {
		t.Fatal(err)
	}
	if idx != 19 {
		t.Fatalf("index = %d, want 19", idx)
	}
	busy := map[int]bool{}
	for i := wdttOpkgIndexMin; i <= wdttOpkgIndexMax; i++ {
		busy[i] = true
	}
	if _, err := allocateWdttOpkgIndex(busy); err == nil {
		t.Fatal("expected error when range exhausted")
	}
}

func TestIsLegacyWdttOpkgIndex(t *testing.T) {
	if !isLegacyWdttOpkgIndex(90) {
		t.Fatal("90 must be legacy")
	}
	if isLegacyWdttOpkgIndex(20) {
		t.Fatal("20 must not be legacy")
	}
}
