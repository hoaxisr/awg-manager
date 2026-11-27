package firc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeKeyCParity_VectorsAndEdgeCases(t *testing.T) {
	t.Run("pct_decode_preserves_plus_and_invalid_percent", func(t *testing.T) {
		got := PctDecodeRaw("NL%20+1%2B2%25%3A%23%@@")
		want := "NL +1+2%:#%@@"
		if got != want {
			t.Fatalf("PctDecodeRaw = %q, want %q", got, want)
		}
	})

	t.Run("spaces_cyrillic_emoji_and_special_chars", func(t *testing.T) {
		rawLink := "tt://user:pass@1.2.3.4:443?sni=cover.example.com&record_fragment=1#%F0%9F%87%B1%F0%9F%87%BB%20LAT%20(%D0%9B%D0%B0%D1%82%D0%B2%D0%B8%D1%8F)%20(Premium)%20%3A%23%25%2B1"
		gotKey := CanonicalFircNodeKeyFromLink("a1b2c3d4", rawLink)
		wantKey := "a1b2c3d4:🇱🇻 LAT (Латвия) (Premium) %3A%23%25+1"
		if gotKey != wantKey {
			t.Fatalf("got key %q, want %q", gotKey, wantKey)
		}
	})

	t.Run("first_hash_is_fragment_boundary_and_internal_hash_is_escaped", func(t *testing.T) {
		rawLink := "vless://11111111-1111-1111-1111-111111111111@1.2.3.4:443#Node #1 +Top"
		gotKey := CanonicalFircNodeKeyFromLink("deadbeef", rawLink)
		wantKey := "deadbeef:Node %231 +Top"
		if gotKey != wantKey {
			t.Fatalf("got key %q, want %q", gotKey, wantKey)
		}
	})

	t.Run("utf8_cut_at_100_bytes_drops_incomplete_multibyte_tail", func(t *testing.T) {
		// 98 ASCII bytes + one 4-byte emoji (102 bytes total) -> cut at 100 splits emoji, so C drops it -> 98 bytes.
		rawName := strings.Repeat("a", 98) + "🇱"
		disp := CanonicalFircNodeDisplayName(rawName)
		if len(disp) != 98 || disp != strings.Repeat("a", 98) {
			t.Fatalf("disp len=%d (%q), want 98 'a's", len(disp), disp)
		}
		// Notice 96 'a's + 4-byte emoji = 100 bytes exact fit -> kept intact.
		exactFit := strings.Repeat("a", 96) + "🇱"
		dispExact := CanonicalFircNodeDisplayName(exactFit)
		if dispExact != exactFit {
			t.Fatalf("exactFit trimmed unexpectedly: %q vs %q", dispExact, exactFit)
		}
	})

	t.Run("encoded_base_part_capped_below_175_bytes", func(t *testing.T) {
		// Each ':' becomes '%3A' (3 bytes): 58 * 3 = 174 (< 175), 59 * 3 = 177 (>= 175 -> stops at 174).
		rawName := strings.Repeat(":", 90)
		enc := EncodeFircNodeKeyPart(CanonicalFircNodeDisplayName(rawName))
		if len(enc) != 174 {
			t.Fatalf("enc len = %d, want 174 (%q)", len(enc), enc)
		}
	})

	t.Run("duplicate_names_in_source_receive_hash_k_suffixes", func(t *testing.T) {
		keys := CanonicalFircSourceNodeKeys("00112233", []string{
			"Germany 🇩🇪",
			"Germany 🇩🇪",
			"France #1",
			"Germany 🇩🇪",
			"France #1",
		})
		want := []string{
			"00112233:Germany 🇩🇪",
			"00112233:Germany 🇩🇪#2",
			"00112233:France %231",
			"00112233:Germany 🇩🇪#3",
			"00112233:France %231#2",
		}
		for i := range want {
			if keys[i] != want[i] {
				t.Fatalf("keys[%d] = %q, want %q", i, keys[i], want[i])
			}
		}
	})
}

func TestNormalizeFircNodeRef_RepairsPercentEncodedStaleKeys(t *testing.T) {
	link := "tt://alice:secret@tt.example.com:443?sni=cover.example&record_fragment=1#%F0%9F%87%B1%F0%9F%87%BB%20LAT%20(%D0%9B%D0%B0%D1%82%D0%B2%D0%B8%D1%8F)%20(Premium)"
	srcID := deterministicHex8ID("src:lat:" + link)
	tun := Tunnel{
		ID:     "lat",
		Device: "tunvless0",
		Enable: true,
		Sources: []TunnelSource{
			{ID: srcID, Kind: "link", Name: "🇱🇻 LAT (Латвия) (Premium)", Link: link},
		},
		Inbounds: []TunnelInbound{
			{Port: 1099, Listen: "br0", Node: srcID + ":%F0%9F%87%B1%F0%9F%87%BB%20LAT%20(%D0%9B%D0%B0%D1%82%D0%B2%D0%B8%D1%8F)%20(Premium)"},
		},
	}

	NormalizeTunnelNodeRefs(&tun)
	want := srcID + ":🇱🇻 LAT (Латвия) (Premium)"
	if tun.Inbounds[0].Node != want {
		t.Fatalf("inbound node = %q, want %q", tun.Inbounds[0].Node, want)
	}

	// Whole-pool '*' and duplicate '#2' keys must remain untouched.
	if got := NormalizeFircNodeRef(TunnelNodePool, tun); got != TunnelNodePool {
		t.Fatalf("pool key mutated: %q", got)
	}
	if got := NormalizeFircNodeRef("aabbccdd:Node Name#2", Tunnel{}); got != "aabbccdd:Node Name#2" {
		t.Fatalf("duplicate suffix key mutated: %q", got)
	}

	// Subscription without explicit Order resolves default inbound node to '*'.
	subOnly := []Tunnel{{
		ID:     "subonly",
		Device: "tunvless1",
		Enable: true,
		Sources: []TunnelSource{
			{ID: "99887766", Kind: "subscription", Name: "SubPool", URL: "https://example.com/sub"},
		},
	}}
	withInbound := EnsureInboundPort1099(subOnly, 1099)
	if len(withInbound[0].Inbounds) != 1 || withInbound[0].Inbounds[0].Node != TunnelNodePool {
		t.Fatalf("expected subscription without Order to attach node %q, got %+v", TunnelNodePool, withInbound[0].Inbounds)
	}

	// Empty tunnel (no Sources, no Order) also resolves auto-created :1099 inbound node to '*' (not "default").
	emptyTunnel := []Tunnel{{
		ID:     "empty",
		Device: "tunvless2",
		Enable: true,
	}}
	emptyWithInbound := EnsureInboundPort1099(emptyTunnel, 1099)
	if len(emptyWithInbound[0].Inbounds) != 1 || emptyWithInbound[0].Inbounds[0].Node != TunnelNodePool {
		t.Fatalf("expected empty tunnel to attach node %q, got %+v", TunnelNodePool, emptyWithInbound[0].Inbounds)
	}

	// Legacy "default" inbound selector normalizes cleanly to '*'.
	if got := NormalizeFircNodeRef("default", Tunnel{}); got != TunnelNodePool {
		t.Fatalf("expected legacy 'default' to normalize to %q, got %q", TunnelNodePool, got)
	}
	legacyTun := Tunnel{
		ID:       "legacy",
		Device:   "tunvless3",
		Enable:   true,
		Inbounds: []TunnelInbound{{Port: 1099, Listen: "br0", Node: "default"}},
	}
	NormalizeTunnelNodeRefs(&legacyTun)
	if legacyTun.Inbounds[0].Node != TunnelNodePool {
		t.Fatalf("NormalizeTunnelNodeRefs did not normalize legacy 'default': got %q", legacyTun.Inbounds[0].Node)
	}
}

func TestMigration_TrustTunnelFragmentEmojiNodeAndGroups(t *testing.T) {
	sbDir := filepath.Join(t.TempDir(), "singbox")
	cfgDir := filepath.Join(sbDir, "config.d")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}

	tenTunnels := `{
  "outbounds": [
    {
      "type": "trusttunnel",
      "tag": "🇱🇻 LAT (Латвия) (Premium)",
      "server": "1.2.3.4",
      "server_port": 443,
      "username": "u",
      "password": "p",
      "tls": {
        "enabled": true,
        "server_name": "cover.example",
        "fragment": true
      }
    }
  ]
}`
	fifteenAwg := `{"outbounds":[{"type":"direct","tag":"awg-awg20","bind_interface":"nwg20"}]}`
	twentyRouter := `{
  "outbounds": [
    {"type": "fallback", "tag": "One", "outbounds": ["awg-awg20", "🇱🇻 LAT (Латвия) (Premium)"]}
  ],
  "route": {
    "rules": [
      {"domain_suffix": ["example.org"], "outbound": "One"},
      {"domain": ["onlylat.example"], "outbound": "🇱🇻 LAT (Латвия) (Premium)"}
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(cfgDir, "10-tunnels.json"), []byte(tenTunnels), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "15-awg.json"), []byte(fifteenAwg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "20-router.json"), []byte(twentyRouter), 0644); err != nil {
		t.Fatal(err)
	}

	// Add subscription fixture with Cyrillic/emoji + duplicate member names + + sign.
	subsJSON := `[
  {
    "id": "sub1",
    "label": "Мульти пул +1",
    "url": "https://sub.example.com/feed",
    "enabled": true,
    "refreshHours": 6,
    "selectorTag": "proxy-sub",
    "activeMember": "m2",
    "members": [
      {"tag": "m1", "label": "🇩🇪 Berlin +1"},
      {"tag": "m2", "label": "🇩🇪 Berlin +1"},
      {"tag": "m3", "label": "🇳🇱 AMS:1 #vip"}
    ],
    "excludedMembers": [
      {"tag": "m3", "label": "🇳🇱 AMS:1 #vip"}
    ]
  }
]`
	if err := os.WriteFile(filepath.Join(sbDir, "subscriptions.json"), []byte(subsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := MigrateFromSingboxConfigDirWithOptions(cfgDir, &MigrationOptions{})
	if err != nil {
		t.Fatalf("MigrateFromSingboxConfigDirWithOptions: %v", err)
	}
	res.Tunnels = EnsureInboundPort1099(res.Tunnels, 1099)

	if err := verifyMigratedTunnelsResolved(res.Tunnels); err != nil {
		t.Fatalf("verifyMigratedTunnelsResolved failed: %v", err)
	}

	lat := res.Tunnels[0]
	if !strings.Contains(lat.Sources[0].Link, "record_fragment=1") {
		t.Fatalf("expected TrustTunnel tls.fragment=true -> record_fragment=1, got %q", lat.Sources[0].Link)
	}
	wantLatKey := lat.Sources[0].ID + ":🇱🇻 LAT (Латвия) (Premium)"
	if len(lat.Inbounds) != 1 || lat.Inbounds[0].Node != wantLatKey {
		t.Fatalf("inbound[0].Node = %q, want canonical %q", lat.Inbounds[0].Node, wantLatKey)
	}

	// Verify subscription Order/Exclude canonical keys (#2 duplicate + escaped ':'/'#' + preserved '+').
	if len(res.Tunnels) < 2 {
		t.Fatalf("expected subscription tunnel as well, got %d tunnels", len(res.Tunnels))
	}
	subTun := res.Tunnels[1]
	subSrcID := subTun.Sources[0].ID
	if len(subTun.Order) < 3 || subTun.Order[0] != subSrcID+":🇩🇪 Berlin +1#2" {
		t.Fatalf("unexpected subscription Order: %+v", subTun.Order)
	}
	wantExcl := subSrcID + ":🇳🇱 AMS%3A1 %23vip"
	if len(subTun.Exclude) != 1 || subTun.Exclude[0] != wantExcl {
		t.Fatalf("unexpected subscription Exclude: %+v (want %q)", subTun.Exclude, wantExcl)
	}

	// Verify groups preserve fallback/direct interface mappings without dropping or inventing links.
	groupByName := make(map[string]Group)
	for _, g := range res.Groups {
		groupByName[g.Name] = g
	}
	oneGrp, ok := groupByName["one"]
	if !ok {
		t.Fatalf("expected group 'one', got %+v", res.Groups)
	}
	if len(oneGrp.Interface) != 2 || oneGrp.Interface[0] != "nwg20" || oneGrp.Interface[1] != lat.Device {
		t.Fatalf("group 'one' interfaces = %+v, want [nwg20 %s]", oneGrp.Interface, lat.Device)
	}
	directLat, ok := groupByName["lat-premium"]
	if !ok {
		t.Fatalf("expected group 'lat-premium', got %+v", res.Groups)
	}
	if len(directLat.Interface) != 1 || directLat.Interface[0] != lat.Device {
		t.Fatalf("direct group 'lat-premium' interfaces = %+v, want [%s]", directLat.Interface, lat.Device)
	}
	// Subscription tunnel had no router rule referencing proxy-sub -> empty group list is expected!
	for _, g := range res.Groups {
		for _, iface := range g.Interface {
			if iface == subTun.Device {
				t.Fatalf("unexpected group %q referencing unrouted subscription tunnel %s", g.Name, subTun.Device)
			}
		}
	}
}

func TestVerifyLiveSyncedTunnels_RejectsNoNodeInboundAndRollsBack(t *testing.T) {
	sbDir := t.TempDir()
	cfgDir := filepath.Join(sbDir, "config.d")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	tenTunnels := `{
  "outbounds": [
    {
      "type": "vless",
      "tag": "🇱🇻 LAT (Латвия)",
      "server": "1.2.3.4",
      "server_port": 443,
      "uuid": "11111111-1111-1111-1111-111111111111"
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(cfgDir, "10-tunnels.json"), []byte(tenTunnels), 0644); err != nil {
		t.Fatal(err)
	}

	fircDir := t.TempDir()
	// Pre-populate original tunnels/groups to verify rollback restores them.
	origTunnels := []Tunnel{{
		ID:     "orig",
		Device: "tunvless0",
		Enable: true,
		Sources: []TunnelSource{
			{ID: "11223344", Kind: "link", Link: "vless://11111111-1111-1111-1111-111111111111@9.9.9.9:443#Orig"},
		},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/system/netfilter":
			_, _ = w.Write([]byte(`{"available":true,"chainsPresent":true,"rulesCount":1}`))
			return
		case "/api/v1/tunnels":
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode(map[string]any{"tunnels": origTunnels})
				return
			}
			if r.Method == http.MethodPut {
				var req []Tunnel
				_ = json.NewDecoder(r.Body).Decode(&req)
				_ = json.NewEncoder(w).Encode(PutTunnelsResponse{Tunnels: req})
				return
			}
		case "/api/v1/groups":
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": []Group{}})
			return
		case "/api/v1/tunnels/state":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tunnels": []TunnelState{{
					ID:     "lat",
					Device: "tunvless0",
					Status: "up",
					Nodes: []NodeState{
						{Key: "aaaa1111:🇱🇻 LAT (Латвия)", Name: "🇱🇻 LAT (Латвия)"},
					},
					Inbounds: []InboundState{
						{Port: 1099, State: "no-node", Why: "selected node not found"},
					},
				}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	op := NewOperator(Options{
		ConfigDir:   fircDir,
		InboundPort: 1099,
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	if err := op.SaveTunnelsFile(origTunnels); err != nil {
		t.Fatal(err)
	}

	_, err := op.MigrateFromSingboxWithOptions(context.Background(), cfgDir, &MigrationOptions{})
	if err == nil || !strings.Contains(err.Error(), "no-node") {
		t.Fatalf("expected rollback error citing no-node inbound state, got %v", err)
	}

	restored, loadErr := op.LoadTunnelsFile()
	if loadErr != nil || len(restored) != 1 || restored[0].ID != "orig" {
		t.Fatalf("expected original tunnels restored after rollback, got %+v (err=%v)", restored, loadErr)
	}
}
