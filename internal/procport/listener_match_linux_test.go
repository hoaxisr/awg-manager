//go:build linux

package procport

import (
	"strings"
	"testing"
)

// Строки из реального /proc/net/udp и /proc/net/udp6 (little-endian).
const (
	udpV4Loopback9000 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  123: 0100007F:2328 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 30111 2 00000000 0
`
	udpV4Wildcard9000 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  124: 00000000:2328 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 30222 2 00000000 0
`
	udp6Any9000 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  125: 00000000000000000000000000000000:2328 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 30333 2 00000000 0
`
	udp6MappedLoopback9000 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  126: 0000000000000000FFFF00000100007F:2328 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 30444 2 00000000 0
`
	tcpV4Loopback9000 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
  1: 0100007F:2328 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 40111 1 00000000 100 0 0 10 0
  2: 0100007F:2329 0100007F:9C40 01 00000000:00000000 00:00000000 00000000     0        0 40112 1 00000000 20 4 30 10 -1
`
)

func inodesFor(t *testing.T, table string, proto Proto, host string, port int) map[string]struct{} {
	t.Helper()
	want, ok := newAddrMatch(host, port)
	if !ok {
		t.Fatalf("newAddrMatch(%q, %d) не разобрал адрес", host, port)
	}
	out := make(map[string]struct{})
	collectListenerInodes(strings.NewReader(table), proto, want, out)
	return out
}

func TestCollectListenerInodesFindsLoopbackAndWildcard(t *testing.T) {
	cases := []struct {
		name  string
		table string
		host  string
		want  string
	}{
		{"v4 loopback", udpV4Loopback9000, "127.0.0.1", "30111"},
		// Слушатель на 0.0.0.0 занимает порт и для 127.0.0.1 — именно этот
		// случай раньше выглядел как «порт свободен».
		{"v4 wildcard по запросу loopback", udpV4Wildcard9000, "127.0.0.1", "30222"},
		// Дефолт Go для ":port" — dual-stack сокет, он живёт только в udp6.
		{"v6 :: по запросу loopback", udp6Any9000, "127.0.0.1", "30333"},
		{"v6 v4-mapped loopback", udp6MappedLoopback9000, "127.0.0.1", "30444"},
		{"v4 loopback по запросу 0.0.0.0", udpV4Loopback9000, "0.0.0.0", "30111"},
	}
	for _, tc := range cases {
		got := inodesFor(t, tc.table, ProtoUDP, tc.host, 9000)
		if _, ok := got[tc.want]; !ok {
			t.Fatalf("%s: inode %s не найден (got %v)", tc.name, tc.want, got)
		}
	}
}

func TestCollectListenerInodesSkipsOtherPortsAndStates(t *testing.T) {
	if got := inodesFor(t, udpV4Loopback9000, ProtoUDP, "127.0.0.1", 9001); len(got) != 0 {
		t.Fatalf("чужой порт не должен совпадать: %v", got)
	}
	if got := inodesFor(t, udpV4Loopback9000, ProtoUDP, "192.168.1.1", 9000); len(got) != 0 {
		t.Fatalf("чужой адрес не должен совпадать: %v", got)
	}
	// TCP: только LISTEN (0A), установленное соединение (01) — не слушатель.
	got := inodesFor(t, tcpV4Loopback9000, ProtoTCP, "127.0.0.1", 9001)
	if len(got) != 0 {
		t.Fatalf("ESTABLISHED не должен считаться слушателем: %v", got)
	}
	if got := inodesFor(t, tcpV4Loopback9000, ProtoTCP, "127.0.0.1", 9000); len(got) != 1 {
		t.Fatalf("LISTEN должен находиться: %v", got)
	}
}

// Big-endian (mips) печатает адрес в прямом порядке.
func TestAddrMatchAcceptsBigEndianForm(t *testing.T) {
	want, _ := newAddrMatch("127.0.0.1", 9000)
	if !want.matches("7F000001:2328") {
		t.Fatal("big-endian форма адреса должна совпадать")
	}
	if !want.matches("00000000000000000000FFFF7F000001:2328") {
		t.Fatal("big-endian v4-mapped форма должна совпадать")
	}
}
