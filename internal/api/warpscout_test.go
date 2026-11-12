package api

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/warpscout"
)

func TestWarpOps_ManagedProtection(t *testing.T) {
	cases := []struct {
		t    storage.AWGTunnel
		want bool
	}{
		{storage.AWGTunnel{}, false},
		{storage.AWGTunnel{Locked: true}, true},
		{storage.AWGTunnel{WdttClientID: "c1"}, true},
		{storage.AWGTunnel{FreeTurnClientID: "c1"}, true},
		{storage.AWGTunnel{Obfuscator: &storage.Obfuscator{}}, true},
	}
	for i, c := range cases {
		if got := managedProtectReason(&c.t) != ""; got != c.want {
			t.Fatalf("случай %d: protected=%v, ждали %v", i, got, c.want)
		}
	}
	if _, ok := modeOfBackend("wdtt-raw"); ok {
		t.Fatal("wdtt-raw не должен попадать в цели автопилота")
	}
}

func TestWarpOps_SystemProtection(t *testing.T) {
	dir := t.TempDir()
	st := storage.NewSettingsStore(dir)
	if _, err := st.Load(); err != nil {
		t.Fatal(err)
	}
	o := &WarpTunnelOps{Settings: st, Store: storage.NewAWGTunnelStore(filepath.Join(dir, "tunnels"))}
	warpPeer := &ndms.WireguardPeerInfo{PublicKey: warpscout.WarpPeerPublicKey, Endpoint: "162.159.192.1:2408"}

	builtin := o.systemRef(&ndms.SystemWireguardTunnel{ID: "Wireguard0", Description: ndms.BuiltInVPNServerDescription, Peer: warpPeer})
	if !builtin.Protected {
		t.Fatal("встроенный VPN-сервер должен быть защищён")
	}
	phobos := o.systemRef(&ndms.SystemWireguardTunnel{ID: "Wireguard3", External: "phobos", Peer: warpPeer})
	if !phobos.Protected {
		t.Fatal("интерфейс Phobos должен быть защищён")
	}
	server := o.systemRef(&ndms.SystemWireguardTunnel{ID: "Wireguard2", Description: "AWGM",
		Peer: &ndms.WireguardPeerInfo{PublicKey: "AAAA"}})
	if server.IsWarp {
		t.Fatal("сервер с чужими пирами не может считаться WARP")
	}
	plain := o.systemRef(&ndms.SystemWireguardTunnel{ID: "Wireguard5", Description: "warp", Status: "up", InterfaceName: "nwg5", Peer: warpPeer})
	if plain.Protected || !plain.IsWarp || !plain.Running || plain.Iface != "nwg5" {
		t.Fatalf("обычный WARP-туннель: %+v", plain)
	}
}

func TestSanitizeConfName(t *testing.T) {
	if got := sanitizeConfName("WARP Финляндия-1"); got != "WARP-1" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeConfName("тест"); got != "warp" {
		t.Fatalf("got %q", got)
	}
}

func TestWarpFallbackClients_DeviceProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	store := deviceproxy.NewStore(filepath.Join(dir, "deviceproxy.json"))
	if err := store.Save(deviceproxy.Config{
		Enabled:          true,
		Port:             port,
		SelectedOutbound: "vless-main",
	}); err != nil {
		t.Fatal(err)
	}
	dp := deviceproxy.NewService(deviceproxy.Deps{Store: store})

	clients := WarpFallbackClients(context.Background(), dp, nil, nil)
	if len(clients) != 1 || clients[0] == nil {
		t.Fatalf("ожидался 1 резервный HTTP-клиент через Прокси для устройств, получили %d", len(clients))
	}

	_ = ln.Close()
	if none := WarpFallbackClients(context.Background(), dp, nil, nil); len(none) != 0 {
		t.Fatalf("закрытый порт прокси не должен попадать в fallback, получили %d", len(none))
	}
}

type warpStubTunnelSvc struct {
	TunnelService
	status   map[string]*service.TunnelWithStatus
	updated  []string
	restarts []string
}

func (s *warpStubTunnelSvc) Get(_ context.Context, id string) (*service.TunnelWithStatus, error) {
	return s.status[id], nil
}

func (s *warpStubTunnelSvc) Update(_ context.Context, _, newStored *storage.AWGTunnel) error {
	s.updated = append(s.updated, newStored.Peer.Endpoint)
	return nil
}

func (s *warpStubTunnelSvc) Restart(_ context.Context, id string) error {
	s.restarts = append(s.restarts, id)
	return nil
}

func TestWarpOps_SwapManagedRotatesKernelPortAndRecoversStalled(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID:      "awg11",
		Name:    "WARP HEL",
		Enabled: true,
		Backend: "kernel",
		Interface: storage.AWGInterface{
			PrivateKey: "gZwP6wviWSsSUhp4AbX0iLuzKKVVTwlB5asMAQ+BWBE=",
			Address:    "172.16.0.2/32",
			MTU:        1280,
		},
		Peer: storage.AWGPeer{
			PublicKey:  warpscout.WarpPeerPublicKey,
			Endpoint:   "162.159.192.70:1014",
			AllowedIPs: []string{"0.0.0.0/0"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&storage.AWGTunnel{
		ID:       "awg50",
		Name:     "WARP Native",
		Enabled:  true,
		Backend:  "nativewg",
		NWGIndex: 3,
		Interface: storage.AWGInterface{
			PrivateKey: "gZwP6wviWSsSUhp4AbX0iLuzKKVVTwlB5asMAQ+BWBE=",
			Address:    "172.16.0.2/32",
			MTU:        1280,
		},
		Peer: storage.AWGPeer{
			PublicKey:  warpscout.WarpPeerPublicKey,
			Endpoint:   "162.159.192.82:864",
			AllowedIPs: []string{"0.0.0.0/0"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	svc := &warpStubTunnelSvc{
		status: map[string]*service.TunnelWithStatus{
			"awg11": {ID: "awg11", InterfaceName: "opkgtun11", State: tunnel.StateRunning},
			"awg50": {ID: "awg50", InterfaceName: "nwg3", State: tunnel.StateBroken},
		},
	}
	var rotated []string
	ops := &WarpTunnelOps{
		Svc:   svc,
		Store: store,
		RotateKernelPort: func(_ context.Context, iface string) error {
			rotated = append(rotated, iface)
			return nil
		},
	}

	// 1. Живой kernel-туннель: подмена endpoint + смена эфемерного UDP-порта.
	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.198:1002"); err != nil {
		t.Fatalf("SwapEndpoint kernel: %v", err)
	}
	if len(rotated) != 1 || rotated[0] != "opkgtun11" {
		t.Fatalf("ожидалась ротация UDP-порта opkgtun11, получили %v", rotated)
	}
	if len(svc.restarts) != 0 {
		t.Fatalf("живой kernel-туннель не должен перезапускаться: %v", svc.restarts)
	}
	got11, err := store.Get("awg11")
	if err != nil || got11.Peer.Endpoint != "162.159.192.198:1002" {
		t.Fatalf("endpoint awg11 не сохранился: %+v (%v)", got11, err)
	}

	// 2. Включённый NativeWG с протухшим рукопожатием (StateBroken): считается Running для автопилота и перезапускается.
	ref50, err := ops.Get(ctx, warpscout.TargetNative, "awg50")
	if err != nil || !ref50.Running {
		t.Fatalf("включённый NativeWG в StateBroken должен считаться активной целью: %+v (%v)", ref50, err)
	}
	if err := ops.SwapEndpoint(ctx, warpscout.TargetNative, "awg50", "162.159.192.193:891"); err != nil {
		t.Fatalf("SwapEndpoint native: %v", err)
	}
	if len(svc.restarts) != 1 || svc.restarts[0] != "awg50" {
		t.Fatalf("зависший NativeWG должен перезапускаться через Restart: %v", svc.restarts)
	}
	if len(rotated) != 1 {
		t.Fatalf("для NativeWG не должна вызываться ротация kernel UDP-порта: %v", rotated)
	}
}
