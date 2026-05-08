package nwg

import "testing"

func TestPubKeyToHex(t *testing.T) {
	key := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="
	hex := pubKeyToHex(key)
	if len(hex) != 64 {
		t.Errorf("pubKeyToHex: expected 64 hex chars, got %d", len(hex))
	}
	if got := pubKeyToHex("invalid"); got != "" {
		t.Errorf("pubKeyToHex(invalid) = %q, want empty", got)
	}
}

func TestBuildProcLine(t *testing.T) {
	cfg := KmodConfig{
		EndpointIP:   "1.2.3.4",
		EndpointPort: 51820,
		H1:           "1", H2: "2", H3: "3", H4: "4",
		S1: 10, S2: 20, S3: 0, S4: 0,
		Jc: 5, Jmin: 50, Jmax: 1000,
	}
	line := buildProcLine(cfg)
	if line == "" {
		t.Error("buildProcLine returned empty")
	}
	expected := "1.2.3.4:51820"
	if len(line) < len(expected) || line[:len(expected)] != expected {
		t.Errorf("buildProcLine prefix = %q, want prefix %q", line[:20], expected)
	}
}

func TestCountProxySlotsList(t *testing.T) {
	data := `
(proxy slots)
144.31.251.248:32663 listen=127.0.0.1:52046 rx=10 tx=20

bad-line
1.2.3.4:443 listen=127.0.0.1:51820 rx=0 tx=0
`

	if got := countProxySlotsList(data); got != 2 {
		t.Fatalf("countProxySlotsList() = %d, want 2", got)
	}
}

func TestCountProxySlotsListEmpty(t *testing.T) {
	for _, data := range []string{"", "\n", "(empty)\n", "1.2.3.4:443 rx=0 tx=0"} {
		if got := countProxySlotsList(data); got != 0 {
			t.Fatalf("countProxySlotsList(%q) = %d, want 0", data, got)
		}
	}
}
