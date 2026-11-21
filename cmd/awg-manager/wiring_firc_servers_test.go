package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type stubFircRCIGetter struct {
	jsonByPath map[string]string
}

func (g *stubFircRCIGetter) Get(_ context.Context, path string, dst any) error {
	raw, ok := g.jsonByPath[path]
	if !ok {
		return errors.New("not mocked: " + path)
	}
	return json.Unmarshal([]byte(raw), dst)
}

func (g *stubFircRCIGetter) GetRaw(_ context.Context, path string) ([]byte, error) {
	raw, ok := g.jsonByPath[path]
	if !ok {
		return nil, errors.New("not mocked: " + path)
	}
	return []byte(raw), nil
}

func (g *stubFircRCIGetter) Post(context.Context, any) (json.RawMessage, error) {
	return nil, nil
}

func newTestFircServerDiscoveryApp(t *testing.T) (*app, *storage.SettingsStore) {
	t.Helper()
	dataDir := t.TempDir()
	lockDir := t.TempDir()

	settingsStore := storage.NewSettingsStore(dataDir)
	if _, err := settingsStore.Load(); err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if err := settingsStore.Update(func(s *storage.Settings) error {
		s.ManagedServers = []storage.ManagedServer{
			{
				InterfaceName: "Wireguard5",
				Description:   "WG 3.1 Политика по умолчанию",
				Address:       "10.88.0.1",
				Mask:          "255.255.255.0",
			},
			{
				InterfaceName: "Wireguard3",
				Description:   "AWGM WG Server",
				Address:       "10.99.0.1",
				Mask:          "255.255.255.0",
			},
		}
		s.ServerInterfaces = []string{"Wireguard1"}
		return nil
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	awgStore := storage.NewAWGTunnelStoreWithLockDir(filepath.Join(dataDir, "tunnels"), lockDir)
	for _, tc := range []struct {
		id  string
		idx int
	}{
		{"nwg-tun-0", 0},
		{"nwg-tun-7", 7},
	} {
		tun := &storage.AWGTunnel{
			ID:       tc.id,
			Name:     tc.id,
			Backend:  "nativewg",
			NWGIndex: tc.idx,
		}
		if err := awgStore.Create(tun); err != nil {
			t.Fatalf("create awg tunnel %s: %v", tc.id, err)
		}
	}

	showIfaces := `{
		"Wireguard0": {"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","description":"Outbound 0","state":"up","link":"up","connected":"yes","address":"10.0.0.2","mask":"255.255.255.0"},
		"Wireguard1": {"id":"Wireguard1","interface-name":"nwg1","type":"Wireguard","description":"WG-S 3X (Router)","state":"up","link":"up","connected":"yes","address":"10.55.0.1","mask":"255.255.255.0"},
		"Wireguard2": {"id":"Wireguard2","interface-name":"nwg2","type":"Wireguard","description":"` + ndms.BuiltInVPNServerDescription + `","state":"up","link":"up","connected":"yes","address":"172.31.0.1","mask":"255.255.255.0"},
		"Wireguard3": {"id":"Wireguard3","interface-name":"nwg3","type":"Wireguard","description":"AWGM WG Server","state":"up","link":"up","connected":"yes","address":"10.99.0.1","mask":"255.255.255.0"},
		"Wireguard5": {"id":"Wireguard5","interface-name":"nwg5","type":"Wireguard","description":"WG 3.1 Политика по умолчанию","state":"up","link":"up","connected":"yes","address":"10.88.0.1","mask":"255.255.255.0"},
		"Wireguard7": {"id":"Wireguard7","interface-name":"nwg7","type":"Wireguard","description":"Youtube","state":"up","link":"up","connected":"yes","address":"10.0.7.2","mask":"255.255.255.0"}
	}`
	rcIfaces := `{
		"Wireguard0": {"description":"Outbound 0","ip":{"address":{"address":"10.0.0.2","mask":"255.255.255.0"}}},
		"Wireguard1": {"description":"WG-S 3X (Router)","ip":{"address":{"address":"10.55.0.1","mask":"255.255.255.0"}}},
		"Wireguard2": {"description":"` + ndms.BuiltInVPNServerDescription + `","ip":{"address":{"address":"172.31.0.1","mask":"255.255.255.0"}}},
		"Wireguard3": {"description":"AWGM WG Server","ip":{"address":{"address":"10.99.0.1","mask":"255.255.255.0"}}},
		"Wireguard5": {"description":"WG 3.1 Политика по умолчанию","ip":{"address":{"address":"10.88.0.1","mask":"255.255.255.0"}}},
		"Wireguard7": {"description":"Youtube","ip":{"address":{"address":"10.0.7.2","mask":"255.255.255.0"}}}
	}`
	fg := &stubFircRCIGetter{
		jsonByPath: map[string]string{
			"/show/interface/":                        showIfaces,
			"/show/rc/interface/":                     rcIfaces,
			"/show/running-config":                    `{"message":["ip hotspot","policy Wireguard5 Policy4","exit"]}`,
			"/show/interface/system-name?name=Wireguard0": `"nwg0"`,
			"/show/interface/system-name?name=Wireguard1": `"nwg1"`,
			"/show/interface/system-name?name=Wireguard2": `"nwg2"`,
			"/show/interface/system-name?name=Wireguard3": `"nwg3"`,
			"/show/interface/system-name?name=Wireguard5": `"nwg5"`,
			"/show/interface/system-name?name=Wireguard7": `"nwg7"`,
		},
	}

	fircOp := firc.NewOperator(firc.Options{ConfigDir: filepath.Join(dataDir, "firc")})
	a := &app{
		settingsStore: settingsStore,
		awgStore:      awgStore,
		ndmsQueries:   ndmsquery.NewQueries(ndmsquery.Deps{Getter: fg}),
		fircOp:        fircOp,
	}
	fircOp.SetWGServersProvider(a.listDynamicFircWGServers)
	return a, settingsStore
}

func TestListDynamicFircWGServers_ReproducesUserLayoutAndExcludesOutboundTunnels(t *testing.T) {
	a, _ := newTestFircServerDiscoveryApp(t)
	list := a.listDynamicFircWGServers(context.Background())

	byID := make(map[string]firc.DynamicWGServer, len(list))
	for _, s := range list {
		byID[s.ID] = s
	}

	for _, unexpected := range []string{"wg:Wireguard0", "wg:Wireguard7", "managed:Wireguard2", "awg_server", "keenetic_wg"} {
		if _, ok := byID[unexpected]; ok {
			t.Fatalf("unexpected server entry %q in dynamic catalog: %+v", unexpected, byID[unexpected])
		}
	}

	if s5, ok := byID["managed:Wireguard5"]; !ok || s5.Category != "awgm" || s5.KernelIface != "nwg5" || s5.Subnet != "10.88.0.0/24" || s5.Policy != "Policy4" {
		t.Fatalf("unexpected managed:Wireguard5: %+v (ok=%v)", s5, ok)
	}
	if s3, ok := byID["managed:Wireguard3"]; !ok || s3.Category != "awgm" || s3.KernelIface != "nwg3" || s3.Subnet != "10.99.0.0/24" {
		t.Fatalf("unexpected managed:Wireguard3: %+v (ok=%v)", s3, ok)
	}
	if s2, ok := byID["wg:Wireguard2"]; !ok || s2.Category != "system" || !s2.BuiltIn || s2.KernelIface != "nwg2" || s2.Subnet != "172.31.0.0/24" {
		t.Fatalf("unexpected built-in wg:Wireguard2: %+v (ok=%v)", s2, ok)
	}
	if s1, ok := byID["wg:Wireguard1"]; !ok || s1.Category != "system" || s1.BuiltIn || s1.KernelIface != "nwg1" || s1.Subnet != "10.55.0.0/24" {
		t.Fatalf("unexpected marked wg:Wireguard1: %+v (ok=%v)", s1, ok)
	}
}

func TestSyncFircAndRouterSettings_DynamicPerServerAndLegacyMigration(t *testing.T) {
	a, settingsStore := newTestFircServerDiscoveryApp(t)
	ctx := context.Background()

	// 1. Legacy awg_server + keenetic_wg in awgm-ext.json migrate cleanly to real IDs.
	legacyExt := firc.ExtConfig{
		ServerRouting: firc.ServerRoutingConfig{
			EnabledServers: []string{"awg_server", "keenetic_wg"},
			AwgServer:      true,
		},
	}
	if err := a.fircOp.SaveExtConfig(legacyExt); err != nil {
		t.Fatalf("save legacy ext: %v", err)
	}
	a.migrateLegacyFircServerRouting(ctx)
	migrated := a.fircOp.LoadExtConfig()
	for _, want := range []string{"managed:Wireguard5", "managed:Wireguard3", "wg:Wireguard2"} {
		if !slices.Contains(migrated.ServerRouting.EnabledServers, want) {
			t.Fatalf("expected migrated enabledServers to contain %q, got %v", want, migrated.ServerRouting.EnabledServers)
		}
	}
	if slices.Contains(migrated.ServerRouting.EnabledServers, "awg_server") || slices.Contains(migrated.ServerRouting.EnabledServers, "keenetic_wg") {
		t.Fatalf("expected legacy IDs removed after migration, got %v", migrated.ServerRouting.EnabledServers)
	}

	// 2. syncRouterSettingsFromFircExt writes per-server managed:Wireguard5 and iface:nwg2 refs.
	a.syncRouterSettingsFromFircExt(firc.ExtConfig{
		ServerRouting: firc.ServerRoutingConfig{
			EnabledServers: []string{"managed:Wireguard5", "wg:Wireguard2"},
		},
	})
	st, err := settingsStore.Get()
	if err != nil {
		t.Fatalf("settings.Get: %v", err)
	}
	if !slices.Equal(st.SingboxRouter.IngressInterfaces, []string{"managed:Wireguard5", "iface:nwg2"}) {
		t.Fatalf("unexpected IngressInterfaces = %v, want [managed:Wireguard5 iface:nwg2]", st.SingboxRouter.IngressInterfaces)
	}

	// 3. syncFircFromRouterSettings updates awgm-ext.json when server card toggles change.
	// Note: Create empty binaries or validate fails offline sync? No, StageExtConfigOffline runs if ValidateBinaries == nil.
	// Test switching Wireguard3 ON and Wireguard2 OFF via SingboxRouterSettings delta while Wireguard5 remains untouched.
	prev := storage.SingboxRouterSettings{IngressInterfaces: []string{"managed:Wireguard5", "iface:nwg2"}}
	next := storage.SingboxRouterSettings{IngressInterfaces: []string{"managed:Wireguard5", "managed:Wireguard3"}}

	// Ensure offline StageExtConfigOffline gate passes without binaries when FIRC is selected, or mark firc selected:
	_ = settingsStore.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "sing-box"
		return nil
	})
	// Save initial state to ext first
	_ = a.fircOp.SaveExtConfig(firc.ExtConfig{
		ServerRouting: firc.ServerRoutingConfig{
			EnabledServers: []string{"managed:Wireguard5", "wg:Wireguard2"},
		},
	})
	// Force binary-less sync branch via RoutingEngine == "firc" won't run ApplyGroups unless binaries exist,
	// Wait: when ApplyExtConfig runs without running fircd, ApplyGroups saves groups.yaml + ext.json without needing binaries!
	_ = settingsStore.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	})
	a.syncFircFromRouterSettings(ctx, prev, next)
	after := a.fircOp.LoadExtConfig()
	if !after.ServerRouting.IsServerEnabled("managed:Wireguard5") || !after.ServerRouting.IsServerEnabled("managed:Wireguard3") {
		t.Fatalf("expected managed:Wireguard5 and managed:Wireguard3 enabled, got %v", after.ServerRouting.EnabledServers)
	}
	if after.ServerRouting.IsServerEnabled("wg:Wireguard2") {
		t.Fatalf("expected wg:Wireguard2 disabled after card toggle off, got %v", after.ServerRouting.EnabledServers)
	}
}
