package firc

import (
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

// Метка группы в conntrack десятичная: 0x40010000 — Telegram, 0x40020000 — All AI.
const (
	testMarkTG = 0x40010000
	testMarkAI = 0x40020000
)

func conntrackLine(src, dst string, tx, rx int, mark uint32) string {
	return fmt.Sprintf(
		"ipv4 2 tcp 6 300 ESTABLISHED src=%s dst=%s sport=51001 dport=443 packets=1 bytes=%d src=%s dst=%s sport=443 dport=51001 packets=1 bytes=%d [ASSURED] mark=%d use=1\n",
		src, dst, tx, dst, src, rx, mark,
	)
}

// applyTestConntrack прогоняет conntrack через разбор с двумя группами и тремя источниками.
func applyTestConntrack(t *testing.T, r io.Reader, withClients bool) MonitorSnapshot {
	t.Helper()
	snap, err := tryApplyTestConntrack(t, r, withClients)
	if err != nil {
		t.Fatalf("applyConntrackSnapshot: %v", err)
	}
	return snap
}

func tryApplyTestConntrack(t *testing.T, r io.Reader, withClients bool) (MonitorSnapshot, error) {
	t.Helper()
	snap := MonitorSnapshot{
		Servers: []MonitorServerRow{{ID: "lan_br0"}, {ID: "awg_server"}, {ID: "vless_xhttp_server"}},
		Groups:  []MonitorGroupRow{{ID: "tg", Name: "Telegram"}, {ID: "ai", Name: "All AI"}},
	}
	_, fakeNet, err := net.ParseCIDR("198.18.5.0/24")
	if err != nil {
		t.Fatal(err)
	}
	err = applyConntrackSnapshot(
		r,
		&snap,
		map[uint32]int{testMarkTG: 0, testMarkAI: 1},
		[]fakeNetGroupEntry{{net: fakeNet, idx: 1}},
		map[string]int{"lan_br0": 0, "awg_server": 1, "vless_xhttp_server": 2},
		map[string]string{"lan_br0": "LAN / Wi-Fi", "awg_server": "AWGM WireGuard", "vless_xhttp_server": "VLESS XHTTP"},
		buildCIDRSources(ServerRoutingConfig{}),
		withClients,
	)
	return snap, err
}

func TestApplyConntrackSnapshot_ClientsBreakdownAndSorting(t *testing.T) {
	conntrack := conntrackLine("192.168.1.50", "149.154.167.91", 1000, 5000, testMarkTG) +
		conntrackLine("192.168.1.50", "149.154.167.92", 500, 2000, testMarkTG) +
		conntrackLine("192.168.1.50", "1.1.1.1", 300, 1200, testMarkAI) +
		conntrackLine("10.10.0.5", "149.154.167.50", 200, 9000, testMarkTG) +
		conntrackLine("10.10.0.6", "149.154.167.51", 100, 400, testMarkTG) +
		conntrackLine("127.0.0.1", "149.154.167.99", 150, 600, testMarkTG) +
		conntrackLine("192.168.1.99", "8.8.8.8", 10, 10, 0)

	snap := applyTestConntrack(t, strings.NewReader(conntrack), true)

	if snap.TotalConns != 6 || snap.TotalClients != 4 {
		t.Fatalf("unexpected totals: conns=%d clients=%d", snap.TotalConns, snap.TotalClients)
	}
	// Сначала по числу сессий, затем по трафику.
	want := []MonitorClientStat{
		{IP: "192.168.1.50", SourceID: "lan_br0", SourceName: "LAN / Wi-Fi", Conns: 3, TxBytes: 1800, RxBytes: 8200, Groups: []string{"All AI", "Telegram"}},
		{IP: "10.10.0.5", SourceID: "awg_server", SourceName: "AWGM WireGuard", Conns: 1, TxBytes: 200, RxBytes: 9000, Groups: []string{"Telegram"}},
		{IP: "127.0.0.1", SourceID: "vless_xhttp_server", SourceName: "VLESS XHTTP", Conns: 1, TxBytes: 150, RxBytes: 600, Groups: []string{"Telegram"}},
		{IP: "10.10.0.6", SourceID: "awg_server", SourceName: "AWGM WireGuard", Conns: 1, TxBytes: 100, RxBytes: 400, Groups: []string{"Telegram"}},
	}
	if !reflect.DeepEqual(snap.Clients, want) {
		t.Fatalf("clients = %+v\nwant %+v", snap.Clients, want)
	}

	tg := snap.Groups[0]
	if tg.ActiveClients != 4 || tg.ActiveConns != 5 || len(tg.Clients) != 4 {
		t.Fatalf("unexpected Telegram breakdown: %+v", tg)
	}
	if tg.Clients[0].IP != "192.168.1.50" || tg.Clients[0].Conns != 2 || tg.Clients[0].Groups != nil {
		t.Fatalf("unexpected Telegram top client: %+v", tg.Clients[0])
	}
	ai := snap.Groups[1]
	if ai.ActiveClients != 1 || ai.ActiveConns != 1 || ai.Clients[0].IP != "192.168.1.50" {
		t.Fatalf("unexpected AI breakdown: %+v", ai)
	}
	awg := snap.Servers[1]
	if awg.ActiveClients != 2 || awg.ActiveConns != 2 || awg.Clients[0].IP != "10.10.0.5" || awg.Clients[1].IP != "10.10.0.6" {
		t.Fatalf("unexpected AWG breakdown: %+v", awg)
	}
}

// Без запроса списков ответ прежний: только счётчики.
func TestApplyConntrackSnapshot_CountersOnlyWithoutClients(t *testing.T) {
	conntrack := conntrackLine("192.168.1.50", "1.1.1.1", 1, 1, testMarkTG) +
		conntrackLine("10.10.0.5", "1.1.1.1", 1, 1, testMarkTG)

	snap := applyTestConntrack(t, strings.NewReader(conntrack), false)

	if snap.TotalClients != 2 || snap.Groups[0].ActiveClients != 2 || snap.Servers[0].ActiveClients != 1 || snap.Servers[1].ActiveClients != 1 {
		t.Fatalf("unexpected counters: %+v", snap)
	}
	if snap.Clients != nil || snap.Groups[0].Clients != nil || snap.Servers[0].Clients != nil || snap.Servers[1].Clients != nil {
		t.Fatalf("client lists must be absent: %+v", snap)
	}
}

// Один клиент в разных записях адреса — один ключ в списке.
func TestApplyConntrackSnapshot_CanonicalClientIP(t *testing.T) {
	cases := []struct {
		name, src, same, want string
		wantSource            string
	}{
		{"IPv4-mapped", "::ffff:10.10.0.5", "10.10.0.5", "10.10.0.5", "awg_server"},
		{"IPv4-mapped loopback", "::ffff:127.0.0.1", "127.0.0.1", "127.0.0.1", "vless_xhttp_server"},
		{"uncompressed IPv6", "2001:0db8:0000:0000:0000:0000:0000:0010", "2001:db8::10", "2001:db8::10", "lan_br0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conntrack := conntrackLine(tc.src, "1.1.1.1", 1, 2, testMarkTG) + conntrackLine(tc.same, "1.1.1.1", 1, 2, testMarkTG)
			snap := applyTestConntrack(t, strings.NewReader(conntrack), true)
			if snap.TotalClients != 1 || len(snap.Clients) != 1 {
				t.Fatalf("clients = %+v, want one", snap.Clients)
			}
			if c := snap.Clients[0]; c.IP != tc.want || c.SourceID != tc.wantSource || c.Conns != 2 {
				t.Fatalf("client = %+v, want IP %s source %s", c, tc.want, tc.wantSource)
			}
		})
	}
}

// Сервер XHTTP узнаётся по метке и тогда, когда источник не loopback.
func TestApplyConntrackSnapshot_XHTTPMarkWithLANSource(t *testing.T) {
	conntrack := conntrackLine("192.168.1.50", "1.1.1.1", 1, 1, testMarkTG|XHTTPServerFircMark)

	snap := applyTestConntrack(t, strings.NewReader(conntrack), true)

	if snap.Servers[0].ActiveConns != 0 || snap.Servers[2].ActiveConns != 1 || snap.Servers[2].ActiveClients != 1 {
		t.Fatalf("session must belong to XHTTP server: %+v", snap.Servers)
	}
	if c := snap.Clients[0]; c.SourceID != "vless_xhttp_server" || c.SourceName != "VLESS XHTTP" {
		t.Fatalf("client source = %s (%s), want vless_xhttp_server", c.SourceID, c.SourceName)
	}
}

// Без метки группу определяет поддельный адрес назначения; чужая подсеть не считается.
func TestApplyConntrackSnapshot_FakeDstSelectsGroup(t *testing.T) {
	conntrack := conntrackLine("192.168.1.50", "198.18.5.7", 10, 20, 0) +
		conntrackLine("192.168.1.51", "198.19.0.7", 10, 20, 0)

	snap := applyTestConntrack(t, strings.NewReader(conntrack), true)

	if snap.TotalConns != 1 || snap.Groups[1].ActiveConns != 1 || snap.Groups[0].ActiveConns != 0 {
		t.Fatalf("fake dst must map to AI only: %+v", snap)
	}
	if len(snap.Groups[1].Clients) != 1 || snap.Groups[1].Clients[0].IP != "192.168.1.50" {
		t.Fatalf("unexpected AI clients: %+v", snap.Groups[1].Clients)
	}
}

// Списки обрезаются, счётчики остаются точными.
func TestApplyConntrackSnapshot_ClientLimit(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 140; i++ {
		sb.WriteString(conntrackLine(fmt.Sprintf("192.168.1.%d", i), "1.1.1.1", 100, 100, testMarkTG))
	}

	snap := applyTestConntrack(t, strings.NewReader(sb.String()), true)

	if snap.TotalClients != 140 || snap.Groups[0].ActiveClients != 140 || snap.Servers[0].ActiveClients != 140 {
		t.Fatalf("counters must stay exact: %+v", snap)
	}
	if len(snap.Clients) != maxMonitorClients || len(snap.Groups[0].Clients) != maxMonitorClients || len(snap.Servers[0].Clients) != maxMonitorClients {
		t.Fatalf("lists must be capped at %d", maxMonitorClients)
	}
}

// Обрыв чтения возвращается ошибкой, прочитанное до него не теряется.
func TestApplyConntrackSnapshot_ReadError(t *testing.T) {
	boom := errors.New("boom")
	r := io.MultiReader(strings.NewReader(conntrackLine("192.168.1.50", "1.1.1.1", 1, 1, testMarkTG)), iotest.ErrReader(boom))

	snap, err := tryApplyTestConntrack(t, r, false)

	if !errors.Is(err, boom) || snap.TotalConns != 1 {
		t.Fatalf("err = %v, conns = %d", err, snap.TotalConns)
	}
}
