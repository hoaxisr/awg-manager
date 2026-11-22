package firc

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestApplyConntrackSnapshot_ClientsBreakdownAndSorting(t *testing.T) {
	// Mark values in nf_conntrack are decimal representations of 32-bit hex marks:
	// - grpTelegram: 0x40010000 = 1073807360
	// - grpAI:       0x40020000 = 1073872896
	// - XHTTP loopback mark: 0x40010000 | XHTTPServerFircMark
	conntrack := strings.Join([]string{
		"ipv4 2 tcp 6 300 ESTABLISHED src=192.168.1.50 dst=149.154.167.91 sport=51001 dport=443 packets=10 bytes=1000 src=149.154.167.91 dst=192.168.1.50 sport=443 dport=51001 packets=20 bytes=5000 [ASSURED] mark=1073807360 use=1",
		"ipv4 2 tcp 6 300 ESTABLISHED src=192.168.1.50 dst=149.154.167.92 sport=51002 dport=443 packets=5 bytes=500 src=149.154.167.92 dst=192.168.1.50 sport=443 dport=51002 packets=8 bytes=2000 [ASSURED] mark=1073807360 use=1",
		"ipv4 2 tcp 6 300 ESTABLISHED src=192.168.1.50 dst=198.18.0.5 sport=51003 dport=443 packets=4 bytes=300 src=198.18.0.5 dst=192.168.1.50 sport=443 dport=51003 packets=6 bytes=1200 [ASSURED] mark=1073872896 use=1",
		"ipv4 2 tcp 6 300 ESTABLISHED src=10.10.0.5 dst=149.154.167.50 sport=41000 dport=443 packets=3 bytes=200 src=149.154.167.50 dst=10.10.0.5 sport=443 dport=41000 packets=9 bytes=9000 [ASSURED] mark=1073807360 use=1",
		"ipv4 2 tcp 6 300 ESTABLISHED src=10.10.0.6 dst=149.154.167.51 sport=41001 dport=443 packets=2 bytes=100 src=149.154.167.51 dst=10.10.0.6 sport=443 dport=41001 packets=4 bytes=400 [ASSURED] mark=1073807360 use=1",
		"ipv4 2 tcp 6 300 ESTABLISHED src=127.0.0.1 dst=149.154.167.99 sport=42000 dport=443 packets=2 bytes=150 src=149.154.167.99 dst=127.0.0.1 sport=443 dport=42000 packets=3 bytes=600 [ASSURED] mark=1073807360 use=1",
		"ipv4 2 tcp 6 300 ESTABLISHED src=192.168.1.99 dst=8.8.8.8 sport=52000 dport=443 mark=0 use=1",
	}, "\n")

	snap := MonitorSnapshot{
		Running: true,
		Servers: []MonitorServerRow{
			{ID: "lan_br0", Name: "LAN / Wi-Fi"},
			{ID: "awg_server", Name: "AWGM WireGuard"},
			{ID: "vless_xhttp_server", Name: "VLESS XHTTP"},
		},
		Groups: []MonitorGroupRow{
			{ID: "tg", Name: "Telegram"},
			{ID: "ai", Name: "All AI"},
		},
	}

	markToGroupIdx := map[uint32]int{
		0x40010000: 0,
		0x40020000: 1,
	}
	serverIndex := map[string]int{
		"lan_br0":            0,
		"awg_server":         1,
		"vless_xhttp_server": 2,
	}
	serverNameMap := map[string]string{
		"lan_br0":            "LAN / Wi-Fi",
		"awg_server":         "AWGM WireGuard",
		"vless_xhttp_server": "VLESS XHTTP",
	}
	cidrs := buildCIDRSources(ServerRoutingConfig{})
	hostMap := map[string]monitorHostMeta{
		"192.168.1.50": {Name: "Workstation", MAC: "aa:bb:cc:dd:ee:01", Policy: "Policy3"},
	}

	applyConntrackSnapshot(
		strings.NewReader(conntrack),
		&snap,
		markToGroupIdx,
		nil,
		serverIndex,
		serverNameMap,
		cidrs,
		hostMap,
		nil,
	)

	if snap.TotalConns != 6 || snap.TotalClients != 4 {
		t.Fatalf("unexpected totals: conns=%d clients=%d", snap.TotalConns, snap.TotalClients)
	}
	// Global sort: 192.168.1.50 (3 conns) first, then 10.10.0.5 (1 conn, 9200 bytes), 127.0.0.1 (1 conn, 750 bytes), 10.10.0.6 (1 conn, 500 bytes).
	wantOrder := []string{"192.168.1.50", "10.10.0.5", "127.0.0.1", "10.10.0.6"}
	gotOrder := make([]string, len(snap.Clients))
	for i, c := range snap.Clients {
		gotOrder[i] = c.IP
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("global client order = %v, want %v", gotOrder, wantOrder)
	}

	top := snap.Clients[0]
	if top.Name != "Workstation" || top.MAC != "aa:bb:cc:dd:ee:01" || top.Policy != "Policy3" {
		t.Fatalf("unexpected host metadata for 192.168.1.50: %+v", top)
	}
	if top.Conns != 3 || top.TxBytes != 1800 || top.RxBytes != 8200 {
		t.Fatalf("unexpected stats for 192.168.1.50: %+v", top)
	}
	if !reflect.DeepEqual(top.Groups, []string{"All AI", "Telegram"}) {
		t.Fatalf("unexpected groups for 192.168.1.50: %v", top.Groups)
	}

	tg := snap.Groups[0]
	if tg.ActiveClients != 4 || tg.ActiveConns != 5 || len(tg.Clients) != 4 {
		t.Fatalf("unexpected Telegram group breakdown: activeClients=%d activeConns=%d len=%d", tg.ActiveClients, tg.ActiveConns, len(tg.Clients))
	}
	if tg.Clients[0].IP != "192.168.1.50" || tg.Clients[0].Conns != 2 || len(tg.Clients[0].Groups) != 0 {
		t.Fatalf("unexpected Telegram top client: %+v", tg.Clients[0])
	}

	ai := snap.Groups[1]
	if ai.ActiveClients != 1 || ai.ActiveConns != 1 || ai.Clients[0].IP != "192.168.1.50" {
		t.Fatalf("unexpected AI group breakdown: %+v", ai)
	}

	awg := snap.Servers[1]
	if awg.ActiveClients != 2 || awg.ActiveConns != 2 {
		t.Fatalf("unexpected AWG server counts: %+v", awg)
	}
	if awg.Clients[0].IP != "10.10.0.5" || awg.Clients[1].IP != "10.10.0.6" {
		t.Fatalf("unexpected AWG client order: %+v", awg.Clients)
	}
	xhttp := snap.Servers[2]
	if xhttp.ActiveClients != 1 || len(xhttp.Clients) != 1 || xhttp.Clients[0].IP != "127.0.0.1" {
		t.Fatalf("unexpected XHTTP server clients: %+v", xhttp)
	}
}

func TestResolveMonitorHostMap_CachesAndBacksOff(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "firc.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen unix: %v", err)
	}
	defer l.Close()

	var hits atomic.Int32
	var failNext atomic.Bool
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.URL.Path != "/api/v1/system/hosts" {
				http.NotFound(w, r)
				return
			}
			if failNext.Load() {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hosts": []SystemHost{
					{Name: "NasBox", MAC: "00:11:22:33:44:55", IP: "192.168.1.40", Policy: "Policy1"},
				},
			})
		}),
	}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()

	op := &Operator{
		client: NewClient(sock),
	}

	m1 := op.resolveMonitorHostMap(context.Background())
	if m1["192.168.1.40"].Name != "NasBox" {
		t.Fatalf("expected NasBox in resolved host map, got %+v", m1)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 RPC call, got %d", hits.Load())
	}

	// Second call within TTL must hit cache without calling fircd again.
	m2 := op.resolveMonitorHostMap(context.Background())
	if m2["192.168.1.40"].Policy != "Policy1" || hits.Load() != 1 {
		t.Fatalf("expected cached host map with 1 total RPC call, got hits=%d map=%+v", hits.Load(), m2)
	}

	// Expire cache and force failure -> keeps stale map and sets retry backoff.
	op.mu.Lock()
	op.monitorHostsCacheAt = op.monitorHostsCacheAt.Add(-monitorHostsCacheTTL * 2)
	op.mu.Unlock()
	failNext.Store(true)

	m3 := op.resolveMonitorHostMap(context.Background())
	if m3["192.168.1.40"].Name != "NasBox" || hits.Load() != 2 {
		t.Fatalf("expected fallback to stale map after error (hits=2), got hits=%d map=%+v", hits.Load(), m3)
	}
	// Next call inside backoff must not issue an RPC.
	_ = op.resolveMonitorHostMap(context.Background())
	if hits.Load() != 2 {
		t.Fatalf("expected error backoff to suppress RPC, got hits=%d", hits.Load())
	}
	_ = os.Remove(sock)
}
