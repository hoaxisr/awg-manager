//go:build linux

package testutil

import (
	"fmt"
	"net"
	"os"
	"testing"
)

// Номера не повторяются, лежат вне эфемерного диапазона, закрепление видно
// другому процессу (второй bind того же имени отказывает), занятый порт
// пропускается.
func TestFreePort(t *testing.T) {
	ephemeralLow := 32768
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range"); err == nil {
		_, _ = fmt.Sscan(string(b), &ephemeralLow)
	}
	seen := map[int]bool{}
	for range 50 {
		p := FreePort(t)
		if seen[p] {
			t.Fatalf("порт %d выдан дважды", p)
		}
		seen[p] = true
		if p >= ephemeralLow {
			t.Fatalf("порт %d в эфемерном диапазоне (от %d)", p, ephemeralLow)
		}
		if ln, err := net.Listen("unix", fmt.Sprintf("@awgm-test-port-%d", p)); err == nil {
			_ = ln.Close()
			t.Fatalf("порт %d не закреплён за процессом", p)
		}
	}

	busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", FreePort(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if portBindable(busy.Addr().(*net.TCPAddr).Port) {
		t.Fatal("занятый порт признан свободным")
	}
}
