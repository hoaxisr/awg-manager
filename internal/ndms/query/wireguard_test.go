package query

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// sampleWGInterfaceListJSON — two WG interfaces plus an unrelated ethernet.
// "Wireguard0" is the Keenetic built-in VPN Server; "Wireguard1" is ours.
const sampleWGInterfaceListJSON = `{
	"Wireguard0": {
		"id": "Wireguard0",
		"interface-name": "nwg0",
		"type": "Wireguard",
		"description": "Wireguard VPN Server",
		"state": "up",
		"link": "up",
		"connected": "yes",
		"address": "10.0.0.1",
		"mask": "255.255.255.0",
		"mtu": 1420,
		"wireguard": {
			"public-key": "SRVKEY0=",
			"listen-port": 51820,
			"peer": [
				{
					"public-key": "PEERA=",
					"description": "alice",
					"remote-endpoint-address": "1.2.3.4",
					"remote-port": 51820,
					"rxbytes": 100,
					"txbytes": 200,
					"last-handshake": 5,
					"online": true,
					"enabled": true
				}
			]
		}
	},
	"Wireguard1": {
		"id": "Wireguard1",
		"interface-name": "nwg1",
		"type": "Wireguard",
		"description": "ourserver",
		"state": "up",
		"link": "up",
		"connected": "yes",
		"address": "10.0.1.1",
		"mask": "255.255.255.0",
		"mtu": 1420,
		"wireguard": {
			"public-key": "SRVKEY1=",
			"listen-port": 51821,
			"peer": [
				{
					"public-key": "PEERB=",
					"description": "bob",
					"remote-endpoint-address": "5.6.7.8",
					"remote-port": 51820,
					"rxbytes": 10,
					"txbytes": 20,
					"last-handshake": 3,
					"online": true,
					"enabled": true
				}
			]
		}
	},
	"ISP": {
		"id": "ISP",
		"interface-name": "eth3",
		"type": "Ethernet",
		"description": "WAN",
		"state": "up",
		"link": "up",
		"connected": "yes"
	}
}`

const sampleWGRCInterfaceJSON = `{
	"description": "ourserver",
	"ip": {
		"address": {"address": "10.0.1.1", "mask": "255.255.255.0"},
		"mtu": "1420"
	},
	"wireguard": {
		"listen-port": {"port": 51821},
		"peer": [
			{
				"key": "PEERB=",
				"comment": "bob",
				"preshared-key": "PSK==",
				"allow-ips": [
					{"address": "10.0.1.2", "mask": "255.255.255.255"},
					{"address": "0.0.0.0", "mask": "0.0.0.0"}
				]
			}
		]
	}
}`
const sampleWGRCInterfaceReversedAllowIPsJSON = `{
	"description": "ourserver",
	"ip": {
		"address": {"address": "10.0.1.1", "mask": "255.255.255.0"},
		"mtu": "1420"
	},
	"wireguard": {
		"listen-port": {"port": 51821},
		"peer": [
			{
				"key": "PEERB=",
				"comment": "bob",
				"preshared-key": "PSK==",
				"allow-ips": [
					{"address": "0.0.0.0", "mask": "0.0.0.0"},
					{"address": "10.0.1.2", "mask": "255.255.255.255"}
				]
			}
		]
	}
}`

const sampleWGSingleInterfaceJSON = `{
	"id": "Wireguard1",
	"interface-name": "nwg1",
	"type": "Wireguard",
	"description": "ourserver",
	"state": "up",
	"link": "up",
	"connected": "yes",
	"address": "10.0.1.1",
	"mask": "255.255.255.0",
	"mtu": 1420,
	"wireguard": {
		"public-key": "SRVKEY1=",
		"listen-port": 51821,
		"peer": [
			{
				"public-key": "PEERB=",
				"description": "bob",
				"remote-endpoint-address": "5.6.7.8",
				"remote-port": 51820,
				"rxbytes": 10,
				"txbytes": 20,
				"last-handshake": 3,
				"online": true,
				"enabled": true
			}
		]
	}
}`

func primeWGFakeGetter(fg *FakeGetter) {
	fg.SetJSON("/show/interface/", sampleWGInterfaceListJSON)
	// Per-interface fetches go through POST (see transport.ShowInterface
	// rationale) — the fixture body must include the {"show":{"interface":…}}
	// envelope that NDMS returns over the wire.
	fg.SetPostInterface("Wireguard0", wrapShowInterface(stripOuterMapEntry(sampleWGInterfaceListJSON, "Wireguard0")))
	fg.SetPostInterface("Wireguard1", wrapShowInterface(sampleWGSingleInterfaceJSON))
	fg.SetJSON("/show/rc/interface/Wireguard0", `{"description":"builtin"}`)
	fg.SetJSON("/show/rc/interface/Wireguard1", sampleWGRCInterfaceJSON)
	fg.SetJSON("/show/interface/system-name?name=Wireguard0", `"nwg0"`)
	fg.SetJSON("/show/interface/system-name?name=Wireguard1", `"nwg1"`)
}

// wrapShowInterface produces the {"show":{"interface":<obj>}} envelope
// that NDMS returns from POST {"show":{"interface":{"name":…}}} queries.
// Test fixture helper — pairs with InterfaceStore.unwrapShowInterface in
// production.
func wrapShowInterface(inner string) string {
	return `{"show":{"interface":` + inner + `}}`
}

// stripOuterMapEntry extracts one key's JSON object from a map-shaped blob —
// trivial helper so the single-interface path has matching fixtures.
func stripOuterMapEntry(blob, key string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &m); err != nil {
		return "{}"
	}
	v, ok := m[key]
	if !ok {
		return "{}"
	}
	return string(v)
}

func TestWGServerStore_GetAll_ParsesRuntime(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	servers, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("want 2 servers, got %d", len(servers))
	}
	// Sorted by ID: Wireguard0, Wireguard1.
	if servers[0].ID != "Wireguard0" || servers[1].ID != "Wireguard1" {
		t.Errorf("order: %s, %s", servers[0].ID, servers[1].ID)
	}
	if servers[1].InterfaceName != "nwg1" {
		t.Errorf("system-name not resolved: %q", servers[1].InterfaceName)
	}
	if servers[1].PublicKey != "SRVKEY1=" || servers[1].ListenPort != 51821 {
		t.Errorf("runtime fields: pk=%q port=%d", servers[1].PublicKey, servers[1].ListenPort)
	}
	// Enrichment: Wireguard1 peer B should have AllowedIPs from RC.
	if len(servers[1].Peers) != 1 {
		t.Fatalf("want 1 peer, got %d", len(servers[1].Peers))
	}
	peer := servers[1].Peers[0]
	want := []string{"10.0.1.2/32", "0.0.0.0/0"}
	if len(peer.AllowedIPs) != len(want) {
		t.Fatalf("AllowedIPs enrichment missing: %+v", peer.AllowedIPs)
	}
	for i := range want {
		if peer.AllowedIPs[i] != want[i] {
			t.Fatalf("AllowedIPs[%d]: want %q, got %q (all=%+v)", i, want[i], peer.AllowedIPs[i], peer.AllowedIPs)
		}
	}
}

func TestWGServerStore_PeerDescription_FromRCCommentWhenRuntimeEmpty(t *testing.T) {
	fg := newFakeGetter()
	const ifaceList = `{
		"Wireguard1": {
			"id": "Wireguard1",
			"interface-name": "nwg1",
			"type": "Wireguard",
			"description": "ourserver",
			"state": "up",
			"connected": "yes",
			"address": "10.0.1.1",
			"mask": "255.255.255.0",
			"mtu": 1420,
			"wireguard": {
				"public-key": "SRVKEY1=",
				"listen-port": 51821,
				"peer": [
					{
						"public-key": "PEERB=",
						"remote-endpoint-address": "5.6.7.8",
						"remote-port": 51820,
						"enabled": false
					}
				]
			}
		}
	}`
	fg.SetJSON("/show/interface/", ifaceList)
	fg.SetPostInterface("Wireguard1", wrapShowInterface(sampleWGSingleInterfaceJSON))
	fg.SetJSON("/show/rc/interface/Wireguard1", sampleWGRCInterfaceJSON)
	fg.SetJSON("/show/interface/system-name?name=Wireguard1", `"nwg1"`)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))
	servers, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(servers) != 1 || len(servers[0].Peers) != 1 {
		t.Fatalf("unexpected servers: %+v", servers)
	}
	if got := servers[0].Peers[0].Description; got != "bob" {
		t.Fatalf("Description = %q, want bob from RC comment", got)
	}
}

func TestWGServerStore_GetAll_CacheHitSkipsFetch(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	_, _ = s.List(context.Background())
	_, _ = s.List(context.Background())
	// 2 calls total: one from WGServerStore's own /show/interface/ fetch,
	// one from InterfaceStore.ensureBootstrap which fires when WGServer
	// resolves system names. Subsequent List() hits both caches.
	if got := fg.Calls("/show/interface/"); got != 2 {
		t.Errorf("/show/interface/ calls: want 2 (WG fetch + Interfaces bootstrap), got %d", got)
	}
}

func TestWGServerStore_Get_Single(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	srv, err := s.Get(context.Background(), "Wireguard1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if srv == nil {
		t.Fatalf("Get: nil server")
	}
	if srv.ID != "Wireguard1" || srv.InterfaceName != "nwg1" {
		t.Errorf("ID/InterfaceName: %q / %q", srv.ID, srv.InterfaceName)
	}
	if srv.ListenPort != 51821 {
		t.Errorf("ListenPort: %d", srv.ListenPort)
	}
	if len(srv.Peers) != 1 {
		t.Errorf("peers: %d", len(srv.Peers))
	}
	want := []string{"10.0.1.2/32", "0.0.0.0/0"}
	if len(srv.Peers[0].AllowedIPs) != len(want) {
		t.Fatalf("AllowedIPs missing in Get(): %+v", srv.Peers[0].AllowedIPs)
	}
	for i := range want {
		if srv.Peers[0].AllowedIPs[i] != want[i] {
			t.Fatalf("AllowedIPs[%d]: want %q, got %q", i, want[i], srv.Peers[0].AllowedIPs[i])
		}
	}
}

func TestWGServerStore_GetAll_AllowedIPsPreservesRCOrderAndCIDR(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)
	fg.SetJSON("/show/rc/interface/Wireguard1", sampleWGRCInterfaceReversedAllowIPsJSON)
	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	servers, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(servers) != 2 || len(servers[1].Peers) != 1 {
		t.Fatalf("unexpected shape: %+v", servers)
	}
	got := servers[1].Peers[0].AllowedIPs
	want := []string{"0.0.0.0/0", "10.0.1.2/32"}
	if len(got) != len(want) {
		t.Fatalf("AllowedIPs: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllowedIPs[%d]: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestWGServerStore_GetAll_SkipsInvalidNonContiguousMask(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)
	fg.SetJSON("/show/rc/interface/Wireguard1", `{
		"description": "ourserver",
		"wireguard": {
			"peer": [
				{
					"key": "PEERB=",
					"allow-ips": [
						{"address": "10.0.1.2", "mask": "255.255.255.255"},
						{"address": "10.0.1.99", "mask": "255.0.255.0"}
					]
				}
			]
		}
	}`)
	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	servers, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(servers) != 2 || len(servers[1].Peers) != 1 {
		t.Fatalf("unexpected shape: %+v", servers)
	}
	got := servers[1].Peers[0].AllowedIPs
	want := []string{"10.0.1.2/32"}
	if len(got) != len(want) {
		t.Fatalf("AllowedIPs: want %+v, got %+v", want, got)
	}
	if got[0] != want[0] {
		t.Fatalf("AllowedIPs[0]: want %q, got %q", want[0], got[0])
	}
}

func TestWGServerStore_GetConfig_MergesRuntimeAndRC(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	cfg, err := s.GetConfig(context.Background(), "Wireguard1")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.PublicKey != "SRVKEY1=" {
		t.Errorf("PublicKey (runtime-sourced): %q", cfg.PublicKey)
	}
	if cfg.ListenPort != 51821 {
		t.Errorf("ListenPort (RC-sourced): %d", cfg.ListenPort)
	}
	if cfg.MTU != 1420 {
		t.Errorf("MTU (RC string→int): %d", cfg.MTU)
	}
	if cfg.Address != "10.0.1.1" {
		t.Errorf("Address: %q", cfg.Address)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("peers: %d", len(cfg.Peers))
	}
	p := cfg.Peers[0]
	if p.PublicKey != "PEERB=" || p.PresharedKey != "PSK==" {
		t.Errorf("peer keys: pk=%q psk=%q", p.PublicKey, p.PresharedKey)
	}
	// allow-ips: 10.0.1.2/32 + 0.0.0.0/0
	if len(p.AllowedIPs) != 2 {
		t.Fatalf("AllowedIPs: %+v", p.AllowedIPs)
	}
	gotSlash32 := false
	gotSlash0 := false
	for _, a := range p.AllowedIPs {
		if a == "10.0.1.2/32" {
			gotSlash32 = true
		}
		if a == "0.0.0.0/0" {
			gotSlash0 = true
		}
	}
	if !gotSlash32 || !gotSlash0 {
		t.Errorf("AllowedIPs CIDR conversion failed: %+v", p.AllowedIPs)
	}
	if p.Address != "10.0.1.2" {
		t.Errorf("peer Address (first /32): %q", p.Address)
	}
}

func TestWGServerStore_FindFreeIndex(t *testing.T) {
	fg := newFakeGetter()
	// Wireguard0 and Wireguard1 used → first free is 2.
	fg.SetJSON("/show/interface/", sampleWGInterfaceListJSON)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	idx, err := s.FindFreeIndex(context.Background())
	if err != nil {
		t.Fatalf("FindFreeIndex: %v", err)
	}
	if idx != 2 {
		t.Errorf("want 2, got %d", idx)
	}
}

// FindFreeIndex must start the scan at 0 — on a fresh device (no Wireguard
// interfaces) the first server has to be Wireguard0, not Wireguard1 (#308).
// The case above pre-occupies both 0 and 1, so it cannot catch a start-at-1
// off-by-one; these cases do.
func TestWGServerStore_FindFreeIndex_StartsAtZero(t *testing.T) {
	cases := map[string]struct {
		list string
		want int
	}{
		"fresh device (no wireguard)": {`{"GigabitEthernet1":{"id":"GigabitEthernet1","type":"GigabitEthernet"}}`, 0},
		"only Wireguard1 used":        {`{"Wireguard1":{"id":"Wireguard1","type":"Wireguard"}}`, 0},
		"Wireguard0 used":             {`{"Wireguard0":{"id":"Wireguard0","type":"Wireguard"}}`, 1},
		// Reuse a freed/gap index: 0 and 2 taken → the freed 1 is reused.
		"gap at 1 reused": {`{"Wireguard0":{"id":"Wireguard0","type":"Wireguard"},"Wireguard2":{"id":"Wireguard2","type":"Wireguard"}}`, 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fg := newFakeGetter()
			fg.SetJSON("/show/interface/", tc.list)
			s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))
			idx, err := s.FindFreeIndex(context.Background())
			if err != nil {
				t.Fatalf("FindFreeIndex: %v", err)
			}
			if idx != tc.want {
				t.Errorf("FindFreeIndex = %d, want %d", idx, tc.want)
			}
		})
	}
}

func TestWGServerStore_GetASCParams(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON("/show/rc/interface/Wireguard1/wireguard/asc", `{
		"jc": "4", "jmin": "40", "jmax": "70",
		"s1": "100", "s2": "200",
		"h1": "aaa", "h2": "bbb", "h3": "ccc", "h4": "ddd",
		"s3": "300", "s4": "400",
		"i1": "i1v", "i2": "i2v", "i3": "i3v", "i4": "i4v", "i5": "i5v"
	}`)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	// Base shape.
	raw, err := s.GetASCParams(context.Background(), "Wireguard1", false)
	if err != nil {
		t.Fatalf("GetASCParams base: %v", err)
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatalf("decode base: %v", err)
	}
	if _, ok := base["s3"]; ok {
		t.Errorf("base should not contain s3: %s", raw)
	}
	if string(base["jc"]) != "4" {
		t.Errorf("jc: %s", base["jc"])
	}

	// Extended shape.
	raw2, err := s.GetASCParams(context.Background(), "Wireguard1", true)
	if err != nil {
		t.Fatalf("GetASCParams extended: %v", err)
	}
	var ext map[string]json.RawMessage
	if err := json.Unmarshal(raw2, &ext); err != nil {
		t.Fatalf("decode ext: %v", err)
	}
	if string(ext["s3"]) != "300" {
		t.Errorf("s3: %s", ext["s3"])
	}
	if string(ext["i5"]) != `"i5v"` {
		t.Errorf("i5: %s", ext["i5"])
	}

	// Base fetch cached separately from extended → 2 RCI calls total.
	if got := fg.Calls("/show/rc/interface/Wireguard1/wireguard/asc"); got != 2 {
		t.Errorf("asc calls: want 2, got %d", got)
	}
}

func TestWGServerStore_ListSystemTunnels_FiltersBuiltInServer(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	tunnels, err := s.ListSystemTunnels(context.Background())
	if err != nil {
		t.Fatalf("ListSystemTunnels: %v", err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("want 1 tunnel (builtin filtered), got %d", len(tunnels))
	}
	if tunnels[0].ID != "Wireguard1" {
		t.Errorf("ID: %q", tunnels[0].ID)
	}
	if tunnels[0].InterfaceName != "nwg1" {
		t.Errorf("InterfaceName: %q", tunnels[0].InterfaceName)
	}
	if tunnels[0].Peer == nil {
		t.Fatal("Peer is nil")
	}
	if tunnels[0].Peer.PublicKey != "PEERB=" {
		t.Errorf("peer key: %q", tunnels[0].Peer.PublicKey)
	}
	// LastHandshake is seconds-ago (3) → must parse as RFC3339.
	if tunnels[0].Peer.LastHandshake == "" {
		t.Error("LastHandshake should be formatted RFC3339")
	}
	if _, err := time.Parse(time.RFC3339, tunnels[0].Peer.LastHandshake); err != nil {
		t.Errorf("LastHandshake not RFC3339: %q (%v)", tunnels[0].Peer.LastHandshake, err)
	}
}

func TestWGServerStore_InvalidateName_DropsListCache(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	// Warm both list and per-item caches.
	_, _ = s.List(context.Background())
	_, _ = s.Get(context.Background(), "Wireguard1")

	s.Invalidate("Wireguard1")

	// Per-server mutation must also bust the list cache — otherwise
	// GetAll keeps returning stale peer counts until TTL.
	_, _ = s.List(context.Background())
	_, _ = s.Get(context.Background(), "Wireguard1")

	// 3 calls: WGServer list before invalidation + Interfaces bootstrap +
	// WGServer list after invalidation. (Interfaces is bootstrapped
	// lazily on first ResolveSystemName, then cached for the rest of
	// the test.)
	if got := fg.Calls("/show/interface/"); got != 3 {
		t.Errorf("/show/interface/ calls: want 3 (WG list ×2 + Interfaces bootstrap), got %d", got)
	}
	if got := fg.PostInterfaceCalls("Wireguard1"); got != 2 {
		t.Errorf("POST show.interface name=Wireguard1 should be hit twice (item invalidated), got %d", got)
	}
}

func TestWGServerStore_InvalidateAll(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	_, _ = s.List(context.Background())
	_, _ = s.Get(context.Background(), "Wireguard1")
	_, _ = s.GetConfig(context.Background(), "Wireguard1")

	s.InvalidateAll()

	_, _ = s.List(context.Background())
	_, _ = s.Get(context.Background(), "Wireguard1")
	_, _ = s.GetConfig(context.Background(), "Wireguard1")

	// /show/interface/ is called: WGServer.List ×2 (cache busted) +
	// Interfaces bootstrap ×1 = 3 total.
	if got := fg.Calls("/show/interface/"); got != 3 {
		t.Errorf("/show/interface/: want 3 (WG list ×2 + Interfaces bootstrap), got %d", got)
	}
	// Single-interface POST happens once per Get() and once per GetConfig() → 4 total.
	if got := fg.PostInterfaceCalls("Wireguard1"); got != 4 {
		t.Errorf("POST show.interface name=Wireguard1: want 4, got %d", got)
	}
}

func TestFormatHandshakeSecondsAgo_Sentinels(t *testing.T) {
	if got := FormatHandshakeSecondsAgo(0); got != "" {
		t.Errorf("zero: want empty, got %q", got)
	}
	if got := FormatHandshakeSecondsAgo(noHandshakeMarker); got != "" {
		t.Errorf("max sentinel: want empty, got %q", got)
	}
	if got := FormatHandshakeSecondsAgo(10); got == "" {
		t.Errorf("positive: want RFC3339, got empty")
	}
}

// TestIPMaskToPrefix покрывает оба формата NDMS allow-ips mask:
// dotted-quad IPv4 + decimal prefix length (issue #216 — "::/0" приходит
// как mask="0", старый парсер отвергал).
func TestIPMaskToPrefix(t *testing.T) {
	cases := []struct {
		name string
		mask string
		want int
	}{
		// IPv6 prefix-length form (issue #216).
		{"ipv6 default route mask 0", "0", 0},
		{"ipv6 /64", "64", 64},
		{"ipv6 host /128", "128", 128},
		{"ipv6 prefix with surrounding space", "  64  ", 64},

		// IPv4 dotted-quad — backward compat.
		{"ipv4 host /32", "255.255.255.255", 32},
		{"ipv4 /24", "255.255.255.0", 24},
		{"ipv4 /16", "255.255.0.0", 16},
		{"ipv4 /0", "0.0.0.0", 0},

		// IPv4 prefix-length form (allowed for symmetry).
		{"ipv4 prefix-length form /32", "32", 32},
		{"ipv4 prefix-length form /24", "24", 24},

		// Garbage / out of range.
		{"empty", "", -1},
		{"negative", "-1", -1},
		{"too large", "129", -1},
		{"non-canonical mask", "255.0.255.0", -1},
		{"random text", "garbage", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ipMaskToPrefix(tc.mask); got != tc.want {
				t.Errorf("ipMaskToPrefix(%q) = %d, want %d", tc.mask, got, tc.want)
			}
		})
	}
}

// Состав системных туннелей спрашивает поллер метрик на КАЖДОМ тике, а выборка
// — это `/show/interface/` целиком. Без кэша при открытой панели мы запрашивали
// всё дерево интерфейсов четыре раза в минуту, только чтобы решить, что
// опрашивать (F364).
func TestWGServerStore_ListSystemTunnels_CachedBetweenCalls(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)
	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	if _, err := s.ListSystemTunnels(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Меряем ПРИРОСТ после первого вызова: первый тянет ещё и InterfaceStore,
	// который резолвит kernel-имена, и его обращения к делу не относятся.
	base := fg.Calls("/show/interface/")
	for i := 0; i < 5; i++ {
		if _, err := s.ListSystemTunnels(context.Background()); err != nil {
			t.Fatalf("вызов %d: %v", i, err)
		}
	}
	if got := fg.Calls("/show/interface/") - base; got != 0 {
		t.Errorf("пять повторов дали %d новых обращений к /show/interface/, ожидали 0", got)
	}
}

// ...но кэш обязан сбрасываться по хуку NDMS, иначе поднявшийся туннель не
// попадёт в опрос до истечения TTL.
func TestWGServerStore_ListSystemTunnels_InvalidateRefetches(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)
	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	if _, err := s.ListSystemTunnels(context.Background()); err != nil {
		t.Fatal(err)
	}
	base := fg.Calls("/show/interface/")
	s.InvalidateAll()
	if _, err := s.ListSystemTunnels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fg.Calls("/show/interface/") - base; got != 1 {
		t.Errorf("после сброса новых обращений %d, ожидали 1 — кэш не сбросился", got)
	}
}
