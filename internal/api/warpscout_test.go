package api

import (
	"context"
	"errors"
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

func seedWarpTunnel(t *testing.T, store *storage.AWGTunnelStore, id, backend string) {
	t.Helper()
	seedTunnel(t, store, &storage.AWGTunnel{
		ID: id, Name: "WARP " + id, Enabled: true, Backend: backend,
		ActiveWAN: "eth3", ResolvedEndpointIP: "162.159.192.70",
		Interface: storage.AWGInterface{
			PrivateKey: "gZwP6wviWSsSUhp4AbX0iLuzKKVVTwlB5asMAQ+BWBE=",
			Address:    "172.16.0.2/32",
			MTU:        1280,
			DNS:        "1.1.1.1",
		},
		Peer: storage.AWGPeer{
			PublicKey:           warpscout.WarpPeerPublicKey,
			Endpoint:            "162.159.192.70:1014",
			AllowedIPs:          []string{"0.0.0.0/0"},
			PersistentKeepalive: "25",
		},
	})
}

// newWarpOpsHarness — WarpTunnelOps поверх настоящего TunnelsHandler: подмена
// идёт штатной правкой туннеля, а не своей записью в стор.
func newWarpOpsHarness(t *testing.T, states map[string]tunnel.State) (*WarpTunnelOps, *stubTunnelSvc, *storage.AWGTunnelStore, *[]string) {
	t.Helper()
	svc := &stubTunnelSvc{getFn: func(_ context.Context, id string) (*service.TunnelWithStatus, error) {
		return &service.TunnelWithStatus{ID: id, InterfaceName: "if-" + id, State: states[id]}, nil
	}}
	h, store := newTunnelsUpdateHarness(t, svc)
	rotated := &[]string{}
	ops := &WarpTunnelOps{Svc: svc, Store: store, Tunnels: h,
		RotateKernelPort: func(_ context.Context, iface string) error {
			*rotated = append(*rotated, iface)
			return nil
		}}
	return ops, svc, store, rotated
}

// Живой kernel-туннель: endpoint меняется, UDP-порт ротируется, рестарта нет.
// Зависший kernel-туннель правка в интерфейс не синхронизирует — рестарт.
// NativeWG в Broken синхронизируется самой правкой — рестарт лишний.
func TestWarpOps_SwapManagedRestartsOnlyStalledKernel(t *testing.T) {
	ctx := context.Background()
	ops, svc, store, rotated := newWarpOpsHarness(t, map[string]tunnel.State{
		"awg11": tunnel.StateRunning, "awg12": tunnel.StateStarting, "awg50": tunnel.StateBroken,
	})
	seedWarpTunnel(t, store, "awg11", "kernel")
	seedWarpTunnel(t, store, "awg12", "kernel")
	seedWarpTunnel(t, store, "awg50", "nativewg")

	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.198:1002"); err != nil {
		t.Fatalf("SwapEndpoint kernel: %v", err)
	}
	if len(*rotated) != 1 || (*rotated)[0] != "if-awg11" || svc.restartCalls != 0 {
		t.Fatalf("живой kernel: ротация порта без рестарта, получили rotated=%v restarts=%d", *rotated, svc.restartCalls)
	}
	if got, _ := store.Get("awg11"); got.Peer.Endpoint != "162.159.192.198:1002" {
		t.Fatalf("endpoint awg11 не сохранился: %+v", got.Peer)
	}

	ref50, err := ops.Get(ctx, warpscout.TargetNative, "awg50")
	if err != nil || !ref50.Running {
		t.Fatalf("включённый NativeWG в StateBroken должен считаться активной целью: %+v (%v)", ref50, err)
	}
	if err := ops.SwapEndpoint(ctx, warpscout.TargetNative, "awg50", "162.159.192.193:891"); err != nil {
		t.Fatalf("SwapEndpoint native: %v", err)
	}
	if svc.restartCalls != 0 || len(*rotated) != 1 {
		t.Fatalf("NativeWG: ни рестарта, ни ротации kernel-порта, получили restarts=%d rotated=%v", svc.restartCalls, *rotated)
	}

	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg12", "162.159.192.193:891"); err != nil {
		t.Fatalf("SwapEndpoint stalled kernel: %v", err)
	}
	if svc.restartCalls != 1 || len(*rotated) != 1 {
		t.Fatalf("зависший kernel: один рестарт без ротации, получили restarts=%d rotated=%v", svc.restartCalls, *rotated)
	}
}

// Туннель остановили руками, пока шла правка: рестарт включил бы его обратно.
func TestWarpOps_SwapManagedSkipsRestartOfDisabledTunnel(t *testing.T) {
	ops, svc, store, _ := newWarpOpsHarness(t, map[string]tunnel.State{"awg12": tunnel.StateStarting})
	seedWarpTunnel(t, store, "awg12", "kernel")
	svc.updateFn = func(context.Context, *storage.AWGTunnel, *storage.AWGTunnel) error {
		return store.Update("awg12", func(t *storage.AWGTunnel) error { t.Enabled = false; return nil })
	}
	if err := ops.SwapEndpoint(context.Background(), warpscout.TargetKernel, "awg12", "162.159.192.193:891"); err != nil {
		t.Fatal(err)
	}
	if svc.restartCalls != 0 {
		t.Fatalf("остановленный пользователем туннель перезапущен: restarts=%d", svc.restartCalls)
	}
}

// Подмена пишет только endpoint: поля оркестратора и пользователя, легшие в
// запись за время правки интерфейса, переживают её, а кэш резолва прежнего
// endpoint'а не переносится на новый.
func TestWarpOps_SwapManagedKeepsForeignFields(t *testing.T) {
	ops, svc, store, _ := newWarpOpsHarness(t, map[string]tunnel.State{"awg11": tunnel.StateRunning})
	seedWarpTunnel(t, store, "awg11", "kernel")
	svc.updateFn = func(context.Context, *storage.AWGTunnel, *storage.AWGTunnel) error {
		return store.Update("awg11", func(t *storage.AWGTunnel) error {
			t.ActiveWAN, t.Name = "ppp0", "переименован"
			return nil
		})
	}
	if err := ops.SwapEndpoint(context.Background(), warpscout.TargetKernel, "awg11", "162.159.192.198:1002"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get("awg11")
	if got.Peer.Endpoint != "162.159.192.198:1002" || got.ActiveWAN != "ppp0" || got.Name != "переименован" {
		t.Fatalf("чужие поля затёрты подменой: endpoint=%q activeWAN=%q name=%q", got.Peer.Endpoint, got.ActiveWAN, got.Name)
	}
	if got.ResolvedEndpointIP == "162.159.192.70" {
		t.Fatal("в записи остался адрес прежнего endpoint'а")
	}
}

// Endpoint проходит проверку штатной правки; защищённый туннель отвергается.
func TestWarpOps_SwapManagedRefusals(t *testing.T) {
	ctx := context.Background()
	ops, svc, store, _ := newWarpOpsHarness(t, map[string]tunnel.State{"awg11": tunnel.StateRunning})
	seedWarpTunnel(t, store, "awg11", "kernel")
	updates := 0
	svc.updateFn = func(context.Context, *storage.AWGTunnel, *storage.AWGTunnel) error { updates++; return nil }

	err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "1.1.1.1:2408\nJc = 1")
	var editErr *TunnelEditError
	if !errors.As(err, &editErr) || editErr.Code != "INVALID_ENDPOINT" || updates != 0 {
		t.Fatalf("endpoint с переводом строки должен отвергаться до правки: err=%v updates=%d", err, updates)
	}
	if got, _ := store.Get("awg11"); got.Peer.Endpoint != "162.159.192.70:1014" {
		t.Fatalf("отвергнутый endpoint попал в запись: %q", got.Peer.Endpoint)
	}

	svc.updateFn = func(context.Context, *storage.AWGTunnel, *storage.AWGTunnel) error {
		return tunnel.ErrOperationInProgress
	}
	err = ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.198:1002")
	if !errors.As(err, &editErr) || editErr.Code != "OPERATION_IN_PROGRESS" {
		t.Fatalf("занятый туннель — конфликт штатной правки: %v", err)
	}
}
