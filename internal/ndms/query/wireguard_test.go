package query

import (
	"context"
	"encoding/json"
	"strings"
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
	fg.SetJSON("/show/interface/Wireguard0", stripOuterMapEntry(sampleWGInterfaceListJSON, "Wireguard0"))
	fg.SetJSON("/show/interface/Wireguard1", sampleWGSingleInterfaceJSON)
	fg.SetJSON("/show/rc/interface/Wireguard0", `{"description":"builtin"}`)
	fg.SetJSON("/show/rc/interface/Wireguard1", sampleWGRCInterfaceJSON)
	fg.SetJSON("/show/interface/system-name?name=Wireguard0", `"nwg0"`)
	fg.SetJSON("/show/interface/system-name?name=Wireguard1", `"nwg1"`)
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
	if len(peer.AllowedIPs) != 2 {
		t.Fatalf("AllowedIPs enrichment missing: %+v", peer.AllowedIPs)
	}
	wantHave := func(s string) bool {
		for _, v := range peer.AllowedIPs {
			if strings.Contains(v, s) {
				return true
			}
		}
		return false
	}
	if !wantHave("10.0.1.2") {
		t.Errorf("AllowedIPs missing peer IP: %+v", peer.AllowedIPs)
	}
}

func TestWGServerStore_GetAll_CacheHitSkipsFetch(t *testing.T) {
	fg := newFakeGetter()
	primeWGFakeGetter(fg)

	s := NewWGServerStore(fg, NopLogger(), NewInterfaceStore(fg, NopLogger()))

	_, _ = s.List(context.Background())
	_, _ = s.List(context.Background())
	if got := fg.Calls("/show/interface/"); got != 1 {
		t.Errorf("/show/interface/ calls: want 1, got %d", got)
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

	if got := fg.Calls("/show/interface/"); got != 2 {
		t.Errorf("/show/interface/ should be hit twice (list invalidated), got %d", got)
	}
	if got := fg.Calls("/show/interface/Wireguard1"); got != 2 {
		t.Errorf("/show/interface/Wireguard1 should be hit twice (item invalidated), got %d", got)
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

	// /show/interface/ is called by GetAll twice (cache busted).
	if got := fg.Calls("/show/interface/"); got != 2 {
		t.Errorf("/show/interface/: want 2, got %d", got)
	}
	// Single-interface GET happens once per Get() and once per GetConfig() → 4 total.
	if got := fg.Calls("/show/interface/Wireguard1"); got != 4 {
		t.Errorf("/show/interface/Wireguard1: want 4, got %d", got)
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
