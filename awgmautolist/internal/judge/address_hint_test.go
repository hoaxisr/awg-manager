package judge

import (
	"fmt"
	"net/netip"
	"testing"
)

func nthClient(i uint32) netip.Addr {
	x := i * 2654435761
	return netip.AddrFrom4([4]byte{byte(x >> 24), byte(x >> 16), byte(x >> 8), byte(x)})
}

func mustBypassAt(t *testing.T, j *Judge, f Flow, name string, now int64) Failure {
	t.Helper()
	out, ok := bareHelloTimes(j, f, name, 4, now)
	if !ok || !out.Bypass {
		t.Fatalf("bypass at %d: %+v, %v", now, out, ok)
	}
	return out
}

func rowForServer(rows []Address, server netip.Addr, proto uint8, port uint16) *Address {
	for i := range rows {
		if rows[i].Server == server && rows[i].Proto == proto && rows[i].Port == port {
			return &rows[i]
		}
	}
	return nil
}

func TestHintLookupKeysAndTTL(t *testing.T) {
	j := New()
	client := mustAddr("192.168.1.10")
	otherClient := mustAddr("192.168.1.11")
	server := mustAddr("203.0.113.20")
	otherServer := mustAddr("203.0.113.21")
	j.Hint(client, "a.test", []netip.Addr{server}, at(100))
	for _, tc := range []struct {
		name   string
		client netip.Addr
		server netip.Addr
		at     int64
		want   bool
	}{
		{name: "same pair", client: client, server: server, at: 101, want: true},
		{name: "other client", client: otherClient, server: server, at: 101},
		{name: "other server", client: client, server: otherServer, at: 101},
		{name: "TTL inclusive", client: client, server: server, at: 700, want: true},
		{name: "expired", client: client, server: server, at: 701},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := j.Hinted(tc.client, tc.server, at(tc.at)); got != tc.want {
				t.Fatalf("Hinted=%v, want %v", got, tc.want)
			}
		})
	}
	j.Hint(client, "b.test", []netip.Addr{server}, at(102))
	if !j.HintedName(client, server, "a.test", at(103)) || !j.HintedName(client, server, "b.test", at(103)) {
		t.Fatal("exact names were not retained independently")
	}
	if j.HintedName(client, server, "c.test", at(103)) || j.HintedName(otherClient, server, "a.test", at(103)) {
		t.Fatal("HintedName crossed its key")
	}
	if j.HintedName(client, server, "a.test", at(701)) || !j.HintedName(client, server, "b.test", at(701)) {
		t.Fatal("per-name TTL mismatch")
	}
}

func TestHintValidationAndMultipleAnswers(t *testing.T) {
	j := New()
	client := mustAddr("192.168.1.10")
	one := mustAddr("203.0.113.1")
	two := mustAddr("203.0.113.2")
	j.Hint(client, "", []netip.Addr{one}, at(100))
	j.Hint(client, string(make([]byte, NameMax)), []netip.Addr{one}, at(100))
	if j.hintCount != 0 {
		t.Fatal("empty or overlong names were retained")
	}
	j.Hint(client, "a.test", []netip.Addr{one, two}, at(100))
	if j.hintCount != 2 || !j.Hinted(client, one, at(100)) || !j.Hinted(client, two, at(100)) {
		t.Fatal("multiple DNS answers were not keyed separately")
	}
	j.Hint(client, "a.test", []netip.Addr{one}, at(200))
	if j.hintCount != 2 || !j.HintedName(client, one, "a.test", at(200)) {
		t.Fatal("same client+server+name was not deduplicated")
	}
}

func TestHintCapacityAndLRU(t *testing.T) {
	j := New()
	server := mustAddr("203.0.113.20")
	for i := uint32(0); i < HintCapacity; i++ {
		j.Hint(nthClient(i), "a.test", []netip.Addr{server}, at(100+int64(i/16)))
	}
	if j.hintCount != HintCapacity || len(j.hintByKey) != HintCapacity {
		t.Fatalf("hint size=%d/%d", j.hintCount, len(j.hintByKey))
	}
	for i := uint32(0); i < HintCapacity; i++ {
		if !j.Hinted(nthClient(i), server, at(400)) {
			t.Fatalf("hint %d missing", i)
		}
	}
	j.Hint(nthClient(0), "a.test", []netip.Addr{server}, at(400))
	j.Hint(nthClient(HintCapacity), "a.test", []netip.Addr{server}, at(400))
	if !j.Hinted(nthClient(0), server, at(400)) {
		t.Fatal("touched hint was evicted")
	}
	if j.Hinted(nthClient(1), server, at(400)) {
		t.Fatal("least recently used hint was not evicted")
	}
	if !j.Hinted(nthClient(2), server, at(400)) || !j.Hinted(nthClient(HintCapacity), server, at(400)) {
		t.Fatal("wrong hint eviction")
	}
}

func TestHintIndexSurvivesEvictions(t *testing.T) {
	j := New()
	server := mustAddr("203.0.113.20")
	for i := uint32(0); i < 3*HintCapacity; i++ {
		name := "a.test"
		if i&1 != 0 {
			name = "b.test"
		}
		j.Hint(nthClient(i), name, []netip.Addr{server}, at(100))
	}
	for i := uint32(0); i < 3*HintCapacity; i++ {
		want := i >= 2*HintCapacity
		if got := j.Hinted(nthClient(i), server, at(101)); got != want {
			t.Fatalf("hint %d: live=%v, want %v", i, got, want)
		}
	}
}

func TestFlowFirstDeduplication(t *testing.T) {
	j := New()
	client := mustAddr("192.168.1.10")
	server := mustAddr("203.0.113.20")
	if !j.FlowFirst(client, 40000, server, 17, 3478, at(100)) {
		t.Fatal("first flow was not new")
	}
	tests := []struct {
		name       string
		client     netip.Addr
		clientPort uint16
		server     netip.Addr
		proto      uint8
		port       uint16
		now        int64
		want       bool
	}{
		{name: "same five tuple", client: client, clientPort: 40000, server: server, proto: 17, port: 3478, now: 350},
		{name: "new client port", client: client, clientPort: 40001, server: server, proto: 17, port: 3478, now: 350, want: true},
		{name: "new protocol", client: client, clientPort: 40000, server: server, proto: 6, port: 3478, now: 350, want: true},
		{name: "new server port", client: client, clientPort: 40000, server: server, proto: 17, port: 3479, now: 350, want: true},
		{name: "new client", client: mustAddr("192.168.1.11"), clientPort: 40000, server: server, proto: 17, port: 3478, now: 350, want: true},
		{name: "new server", client: client, clientPort: 40000, server: mustAddr("203.0.113.21"), proto: 17, port: 3478, now: 350, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := j.FlowFirst(tc.client, tc.clientPort, tc.server, tc.proto, tc.port, at(tc.now)); got != tc.want {
				t.Fatalf("FlowFirst=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestFlowFirstTTLAndCapacity(t *testing.T) {
	for _, tc := range []struct {
		delay int64
		want  bool
	}{{300, false}, {301, true}} {
		j := New()
		client := mustAddr("192.168.1.10")
		server := mustAddr("203.0.113.20")
		j.FlowFirst(client, 40000, server, 17, 3478, at(100))
		if got := j.FlowFirst(client, 40000, server, 17, 3478, at(100+tc.delay)); got != tc.want {
			t.Errorf("delay %d: first=%v, want %v", tc.delay, got, tc.want)
		}
	}

	j := New()
	server := mustAddr("203.0.113.20")
	for i := uint32(0); i < FlowCapacity+1; i++ {
		if !j.FlowFirst(nthClient(i), 40000, server, 17, 3478, at(100)) {
			t.Fatalf("new flow %d was not first", i)
		}
	}
	if j.FlowFirst(nthClient(FlowCapacity), 40000, server, 17, 3478, at(101)) || j.FlowFirst(nthClient(1), 40000, server, 17, 3478, at(101)) {
		t.Fatal("live LRU entries became new")
	}
	if !j.FlowFirst(nthClient(0), 40000, server, 17, 3478, at(101)) {
		t.Fatal("oldest entry was not evicted")
	}
}

func TestFailedAddressAggregation(t *testing.T) {
	t.Run("one row per address with deduplicated clients", func(t *testing.T) {
		j := New()
		f := flowTo(50000, "203.0.113.20")
		mustBypassAt(t, j, f, "a.test", 100)
		g := flowTo(50001, "203.0.113.20")
		g.Client = mustAddr("192.168.1.11")
		mustBypassAt(t, j, g, "", 200)
		rows := j.Addresses(at(200))
		if len(rows) != 1 || rows[0].Fails != 2 || len(rows[0].Clients) != 2 || rows[0].Name != "a.test" {
			t.Fatalf("address aggregate: %+v", rows)
		}
		if rows[0].Proto != ProtocolTCP || rows[0].Port != 443 || !rows[0].FirstAt.Equal(at(103)) || !rows[0].LastAt.Equal(at(203)) {
			t.Fatalf("address metadata: %+v, first unix=%d last unix=%d", rows[0], rows[0].FirstAt.Unix(), rows[0].LastAt.Unix())
		}
	})

	t.Run("only first eight clients are stored", func(t *testing.T) {
		j := New()
		server := mustAddr("203.0.113.20")
		for i := 0; i < 9; i++ {
			f := flowTo(uint16(50000+i), server.String())
			f.Client = mustAddr(fmt.Sprintf("192.168.1.%d", 10+i))
			mustBypassAt(t, j, f, "a.test", 100+int64(i)*10)
		}
		rows := j.Addresses(at(200))
		if len(rows) != 1 || rows[0].Fails != 9 || len(rows[0].Clients) != AddressClientCapacity {
			t.Fatalf("client cap: %+v", rows)
		}
		if rows[0].Clients[7] != mustAddr("192.168.1.17") {
			t.Fatalf("wrong retained clients: %+v", rows[0].Clients)
		}
	})

	t.Run("IPv4 and IPv6 have different rows", func(t *testing.T) {
		j := New()
		mustBypassAt(t, j, flowTo(50000, "203.0.113.20"), "a.test", 100)
		v6 := testFlow(50001)
		v6.Client = mustAddr("2000::1")
		v6.Server = mustAddr("2600::9")
		mustBypassAt(t, j, v6, "a.test", 200)
		rows := j.Addresses(at(200))
		if len(rows) != 2 || !rows[0].Server.Is6() || !rows[1].Server.Is4() {
			t.Fatalf("address family rows: %+v", rows)
		}
	})

	t.Run("snapshot is independent", func(t *testing.T) {
		j := New()
		mustBypassAt(t, j, flowTo(50000, "203.0.113.20"), "a.test", 100)
		rows := j.Addresses(at(100))
		rows[0].Name = "changed"
		rows[0].Clients[0] = mustAddr("192.168.1.99")
		fresh := j.Addresses(at(100))
		if fresh[0].Name != "a.test" || fresh[0].Clients[0] != mustAddr("192.168.1.10") {
			t.Fatal("caller mutated internal address state")
		}
	})
}

func TestAddressExpiryOrderingAndQuota(t *testing.T) {
	t.Run("day expiry and fresh restart", func(t *testing.T) {
		j := New()
		f := flowTo(50000, "203.0.113.20")
		mustBypassAt(t, j, f, "old.test", 100)
		before := j.Addresses(at(103 + 86399))
		expired := j.Addresses(at(103 + 86400))
		if len(before) != 1 || len(expired) != 0 {
			t.Fatalf("address TTL boundary mismatch: before=%+v expired=%+v, row=%+v used=%v", before, expired, j.addr[0], j.addrUsed[0])
		}
		g := flowTo(50001, "203.0.113.20")
		mustBypassAt(t, j, g, "new.test", 100+86400)
		rows := j.Addresses(at(100 + 86400))
		if len(rows) != 1 || rows[0].Fails != 1 || !rows[0].FirstAt.Equal(at(103+86400)) || rows[0].Name != "new.test" {
			t.Fatalf("stale row did not restart: %+v", rows)
		}
	})

	t.Run("newest first and failed quota drops oldest", func(t *testing.T) {
		j := New()
		for i := 1; i <= AddressCapacity+1; i++ {
			f := flowTo(uint16(50000+i), fmt.Sprintf("203.0.113.%d", i))
			mustBypassAt(t, j, f, "x.test", 100+int64(i)*10)
		}
		rows := j.Addresses(at(800))
		if len(rows) != AddressCapacity || rows[0].Server != mustAddr("203.0.113.65") || rows[1].Server != mustAddr("203.0.113.64") || rows[63].Server != mustAddr("203.0.113.2") {
			t.Fatalf("ordering/eviction mismatch: len=%d first=%v last=%v", len(rows), rows[0].Server, rows[len(rows)-1].Server)
		}
	})

	t.Run("bypass quota cannot evict failed quota", func(t *testing.T) {
		j := New()
		for i := 1; i <= AddressCapacity; i++ {
			mustBypassAt(t, j, flowTo(uint16(50000+i), fmt.Sprintf("203.0.113.%d", i)), "x.test", 100+int64(i))
		}
		client := mustAddr("192.168.1.10")
		for i := 1; i <= AddressCapacity+1; i++ {
			j.BypassSeen(client, mustAddr(fmt.Sprintf("198.51.100.%d", i)), 17, 3478, "", "0000abcd", HowAnswer, at(1000+int64(i)))
		}
		rows := j.Addresses(at(2000))
		if len(rows) != 2*AddressCapacity {
			t.Fatalf("rows=%d, want %d", len(rows), 2*AddressCapacity)
		}
		for i := 1; i <= AddressCapacity; i++ {
			if rowForServer(rows, mustAddr(fmt.Sprintf("203.0.113.%d", i)), ProtocolTCP, 443) == nil {
				t.Fatalf("failed row %d was evicted", i)
			}
		}
		if rowForServer(rows, mustAddr("198.51.100.1"), 17, 3478) != nil || rowForServer(rows, mustAddr("198.51.100.65"), 17, 3478) == nil {
			t.Fatal("bypass LRU did not drop its own oldest row")
		}
	})
}

func TestBypassSeenAndMigration(t *testing.T) {
	client := mustAddr("192.168.1.10")
	server := mustAddr("203.0.113.20")
	t.Run("bypass metadata and protocol key", func(t *testing.T) {
		j := New()
		j.BypassSeen(client, server, 17, 3478, "stun.test", "0000abcdEXTRA", HowSNI, at(100))
		rows := j.Addresses(at(100))
		if len(rows) != 1 || rows[0].Outcome != OutcomeBypass || rows[0].How != HowSNI || rows[0].Group != "0000abcd" || rows[0].Name != "stun.test" {
			t.Fatalf("bypass row: %+v", rows)
		}
		j.BypassSeen(mustAddr("192.168.1.11"), server, 17, 3478, "", "0000beef", HowAnswer, at(110))
		rows = j.Addresses(at(110))
		if len(rows) != 1 || rows[0].Fails != 2 || len(rows[0].Clients) != 2 || rows[0].Name != "stun.test" || rows[0].How != HowAnswer || rows[0].Group != "0000beef" {
			t.Fatalf("bypass update: %+v", rows)
		}
		j.BypassSeen(client, server, ProtocolTCP, 3478, "", "0000abcd", HowPTR, at(120))
		if len(j.Addresses(at(120))) != 2 {
			t.Fatal("different protocol shared a row")
		}
	})

	t.Run("failure migrates live bypass history", func(t *testing.T) {
		j := New()
		j.BypassSeen(client, server, ProtocolTCP, 443, "seen.test", "0000abcd", HowSNI, at(100))
		j.BypassSeen(client, server, ProtocolTCP, 443, "", "0000abcd", HowSNI, at(101))
		f := flowTo(50000, server.String())
		mustBypassAt(t, j, f, "a.test", 200)
		rows := j.Addresses(at(200))
		if len(rows) != 1 || rows[0].Outcome != OutcomeFailed || rows[0].Why != WhyRetrans || rows[0].Fails != 3 || !rows[0].FirstAt.Equal(at(100)) || rows[0].Name != "a.test" {
			t.Fatalf("migration: %+v", rows)
		}
	})

	t.Run("bypass updates a live failed row in place", func(t *testing.T) {
		j := New()
		mustBypassAt(t, j, flowTo(50000, server.String()), "a.test", 100)
		j.BypassSeen(mustAddr("192.168.1.12"), server, ProtocolTCP, 443, "b.test", "0000beef", HowPTR, at(200))
		rows := j.Addresses(at(200))
		if len(rows) != 1 || rows[0].Outcome != OutcomeBypass || rows[0].How != HowPTR || rows[0].Group != "0000beef" || rows[0].Fails != 2 || !rows[0].FirstAt.Equal(at(103)) {
			t.Fatalf("failed row update: %+v", rows)
		}
	})
}

func TestDismissAddress(t *testing.T) {
	j := New()
	f := flowTo(50000, "203.0.113.20")
	mustBypassAt(t, j, f, "a.test", 100)
	if j.DismissAddress(mustAddr("203.0.113.21"), ProtocolTCP, 443, at(110)) {
		t.Fatal("unknown address dismissed")
	}
	if !j.DismissAddress(f.Server, ProtocolTCP, 443, at(110)) {
		t.Fatal("visible address was not dismissed")
	}
	if len(j.Addresses(at(111))) != 0 {
		t.Fatal("dismissed row remained visible")
	}
	mustBypassAt(t, j, flowTo(50001, f.Server.String()), "a.test", 120)
	for _, now := range []int64{130, 110 + 86399} {
		if len(j.Addresses(at(now))) != 0 {
			t.Fatalf("dismissed row visible at %d", now)
		}
	}
	rows := j.Addresses(at(110 + 86400))
	if len(rows) != 1 || rows[0].Fails != 2 {
		t.Fatalf("row did not reappear after dismissal: %+v", rows)
	}
}

func TestIsLocal(t *testing.T) {
	in := []string{
		"10.0.0.0", "10.255.255.255", "172.16.0.0", "172.31.255.255", "192.168.0.0",
		"192.168.255.255", "169.254.0.1", "127.0.0.1", "100.64.0.0", "100.127.255.255",
		"198.18.0.0", "198.19.255.255", "0.0.0.0", "0.255.255.255", "fc00::", "fdff:ffff::1",
		"fe80::1", "febf:ffff::1", "::1", "::ffff:192.168.1.10", "::ffff:100.64.0.1", "::", "::ffff:0.0.0.0",
	}
	out := []string{
		"9.255.255.255", "11.0.0.0", "172.15.255.255", "172.32.0.0", "192.167.255.255",
		"192.169.0.0", "169.253.255.255", "169.255.0.0", "126.255.255.255", "128.0.0.0",
		"100.63.255.255", "100.128.0.0", "198.17.255.255", "198.20.0.0", "203.0.113.7",
		"8.8.8.8", "1.0.0.0", "fbff:ffff::1", "fe00::1", "fec0::1", "::2", "2001:db8::1",
		"::ffff:8.8.8.8", "2a00:1450::1", "::2:0",
	}
	for _, s := range in {
		if !IsLocal(mustAddr(s)) {
			t.Errorf("%s: not local", s)
		}
	}
	for _, s := range out {
		if IsLocal(mustAddr(s)) {
			t.Errorf("%s: unexpectedly local", s)
		}
	}
	if IsLocal(netip.Addr{}) {
		t.Error("invalid address was local")
	}
}

func TestConfiguredLocalPools(t *testing.T) {
	v4 := netip.MustParsePrefix("11.0.0.0/16")
	v6 := netip.MustParsePrefix("2001:db8:1::/48")
	odd := netip.MustParsePrefix("11.0.0.0/9")
	for _, tc := range []struct {
		ip    string
		pools []netip.Prefix
		want  bool
	}{
		{ip: "11.0.255.9", pools: []netip.Prefix{v4, v6}, want: true},
		{ip: "2001:db8:1:ffff::1", pools: []netip.Prefix{v4, v6}, want: true},
		{ip: "11.1.0.0", pools: []netip.Prefix{v4, v6}},
		{ip: "2001:db8:2::1", pools: []netip.Prefix{v4, v6}},
		{ip: "11.0.255.9"},
		{ip: "b00::1", pools: []netip.Prefix{v4}},
		{ip: "11.127.0.1", pools: []netip.Prefix{odd}, want: true},
		{ip: "11.128.0.1", pools: []netip.Prefix{odd}},
	} {
		if got := IsLocal(mustAddr(tc.ip), tc.pools...); got != tc.want {
			t.Errorf("%s with %v: local=%v, want %v", tc.ip, tc.pools, got, tc.want)
		}
	}
}

func TestHostCapacityEvictsIdleOldest(t *testing.T) {
	j := New()
	first := testFlow(50000)
	helloTimes(j, first, "h0.example", 1, 100)
	for i := 1; i <= HostCapacity; i++ {
		f := testFlow(uint16(50000 + i))
		helloTimes(j, f, fmt.Sprintf("h%d.example", i), 1, 100)
	}
	if _, ok := bareHelloTimes(j, first, "h0.example", 3, 101); ok {
		t.Fatal("flow retained a host after its slot was reused")
	}
	late := mustFailureAt(t, j, testFlow(51000), "late.example", 100)
	if late.Host != "late.example" || late.Bypass {
		t.Fatalf("new host after capacity churn failed: %+v", late)
	}
}
