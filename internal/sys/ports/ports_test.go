package ports

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseHexIPv4(t *testing.T) {
	tests := []struct {
		hexStr   string
		expected string
	}{
		{"00000000", "0.0.0.0"},
		{"0100007F", "127.0.0.1"},
		{"0101A8C0", "192.168.1.1"},
		{"3C5AA8C0", "192.168.90.60"},
	}

	for _, tt := range tests {
		res, err := parseHexIPv4(tt.hexStr)
		if err != nil {
			t.Fatalf("parseHexIPv4(%q) error: %v", tt.hexStr, err)
		}
		if res != tt.expected {
			t.Errorf("parseHexIPv4(%q) = %q, want %q", tt.hexStr, res, tt.expected)
		}
	}
}

func TestParseHexIPv6(t *testing.T) {
	hexStr := "00000000000000000000000000000000"
	res, err := parseHexIPv6(hexStr)
	if err != nil {
		t.Fatalf("parseHexIPv6 error: %v", err)
	}
	if res != "::" {
		t.Errorf("parseHexIPv6 = %q, want \"::\"", res)
	}
}

func TestScannerMock(t *testing.T) {
	tmp := t.TempDir()
	netDir := filepath.Join(tmp, "net")
	if err := os.MkdirAll(netDir, 0755); err != nil {
		t.Fatal(err)
	}

	tcpContent := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:08AE 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 31870 1 0000000000000000 100 0 0 10 0
   1: 0100007F:15E0 00000000:0000 01 00000000:00000000 00:00000000 00000000     0        0 31871 1 0000000000000000 100 0 0 10 0
`
	if err := os.WriteFile(filepath.Join(netDir, "tcp"), []byte(tcpContent), 0644); err != nil {
		t.Fatal(err)
	}

	udpContent := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:DACD 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 45678 2 0000000000000000 100 0 0 10 0
`
	if err := os.WriteFile(filepath.Join(netDir, "udp"), []byte(udpContent), 0644); err != nil {
		t.Fatal(err)
	}

	sc := &Scanner{
		procDir: tmp,
		initDir: tmp,
	}

	bindings, err := sc.List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	// 0x08AE = 2222 (TCP listen), 0xDACD = 56013 (UDP)
	if len(bindings) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(bindings))
	}

	if bindings[0].Port != 2222 || bindings[0].Proto != "tcp" {
		t.Errorf("binding 0: %+v", bindings[0])
	}
	if bindings[1].Port != 56013 || bindings[1].Proto != "udp" {
		t.Errorf("binding 1: %+v", bindings[1])
	}

	inspected, err := sc.InspectPort(56013, "udp")
	if err != nil {
		t.Fatalf("InspectPort error: %v", err)
	}
	if len(inspected) != 1 || inspected[0].Port != 56013 {
		t.Errorf("InspectPort(56013) = %+v", inspected)
	}
}
