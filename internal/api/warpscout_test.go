package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
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

	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.198:1002", warpscout.SwapOptions{}); err != nil {
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
	if err := ops.SwapEndpoint(ctx, warpscout.TargetNative, "awg50", "162.159.192.193:891", warpscout.SwapOptions{}); err != nil {
		t.Fatalf("SwapEndpoint native: %v", err)
	}
	if svc.restartCalls != 0 || len(*rotated) != 1 {
		t.Fatalf("NativeWG: ни рестарта, ни ротации kernel-порта, получили restarts=%d rotated=%v", svc.restartCalls, *rotated)
	}

	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg12", "162.159.192.193:891", warpscout.SwapOptions{}); err != nil {
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
	if err := ops.SwapEndpoint(context.Background(), warpscout.TargetKernel, "awg12", "162.159.192.193:891", warpscout.SwapOptions{}); err != nil {
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
	if err := ops.SwapEndpoint(context.Background(), warpscout.TargetKernel, "awg11", "162.159.192.198:1002", warpscout.SwapOptions{}); err != nil {
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

	err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "1.1.1.1:2408\nJc = 1", warpscout.SwapOptions{})
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
	err = ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.198:1002", warpscout.SwapOptions{})
	if !errors.As(err, &editErr) || editErr.Code != "OPERATION_IN_PROGRESS" {
		t.Fatalf("занятый туннель — конфликт штатной правки: %v", err)
	}
}

func TestWarpOps_AddressAndUsedIPv4(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	tunStore := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	seedWarpTunnel(t, tunStore, "awg11", "kernel")

	ops := &WarpTunnelOps{Store: tunStore}
	ref, err := ops.Get(ctx, warpscout.TargetKernel, "awg11")
	if err != nil || ref.AddressV4 != "172.16.0.2" {
		t.Fatalf("ожидался addressV4=172.16.0.2 в TunnelRef, получили %+v (%v)", ref, err)
	}

	used := ops.UsedIPv4(ctx)
	found := false
	for _, ip := range used {
		if ip == "172.16.0.2" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("UsedIPv4 должен включать 172.16.0.2 из awg11: %v", used)
	}

	wsStore, err := warpscout.NewStore(t.TempDir(), warpscout.NewLocalCipher(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := wsStore.SetAccount(&warpscout.Account{
		ID:            "acct1",
		Token:         "tok",
		PrivateKey:    "cHJpdg==",
		PublicKey:     "pub",
		PeerPublicKey: warpscout.WarpPeerPublicKey,
		AddressV4:     "172.16.0.2",
	}); err != nil {
		t.Fatal(err)
	}
	wsSvc := warpscout.New(warpscout.Deps{Store: wsStore, Tunnels: ops, UsedIPv4: ops.UsedIPv4})
	t.Cleanup(wsSvc.Stop)
	h := NewWarpScoutHandler(wsSvc, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/warpscout/account/address", bytes.NewBufferString(`{"addressV4":"172.16.0.7/32"}`))
	h.Address(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /account/address status=%d body=%s", rec.Code, rec.Body.String())
	}
	var wrap struct {
		Data warpscout.Account `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wrap); err != nil {
		t.Fatal(err)
	}
	if wrap.Data.AddressV4 != "172.16.0.7" {
		t.Fatalf("ожидался addressV4=172.16.0.7 в ответе, получили %+v", wrap.Data)
	}
}

func TestWarpOps_Rename(t *testing.T) {
	ctx := context.Background()
	ops, svc, store, _ := newWarpOpsHarness(t, map[string]tunnel.State{
		"awg11": tunnel.StateRunning,
		"awg50": tunnel.StateRunning,
		"awg99": tunnel.StateRunning,
	})
	seedWarpTunnel(t, store, "awg11", "kernel")
	seedTunnel(t, store, &storage.AWGTunnel{
		ID:      "awg50",
		Name:    "WARP Locked",
		Enabled: true,
		Locked:  true,
		Backend: "nativewg",
		Peer:    storage.AWGPeer{PublicKey: warpscout.WarpPeerPublicKey, Endpoint: "162.159.192.82:864"},
	})
	seedTunnel(t, store, &storage.AWGTunnel{
		ID:      "awg99",
		Name:    "Custom",
		Enabled: true,
		Backend: "kernel",
		Peer:    storage.AWGPeer{PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", Endpoint: "1.2.3.4:51820"},
	})
	var updatedNames []string
	svc.updateFn = func(_ context.Context, _, newStored *storage.AWGTunnel) error {
		updatedNames = append(updatedNames, newStored.Name)
		return nil
	}

	if err := ops.Rename(ctx, warpscout.TargetKernel, "awg11", "WARP FRA"); err != nil {
		t.Fatalf("Rename kernel: %v", err)
	}
	if len(updatedNames) != 1 || updatedNames[0] != "WARP FRA" {
		t.Fatalf("ожидался вызов Svc.Update с именем WARP FRA, получили %v", updatedNames)
	}
	if svc.restartCalls != 0 {
		t.Fatalf("переименование не должно перезапускать туннель: restarts=%d", svc.restartCalls)
	}
	got11, err := store.Get("awg11")
	if err != nil || got11.Name != "WARP FRA" {
		t.Fatalf("имя awg11 не сохранилось в store: %+v (%v)", got11, err)
	}
	if err := ops.Rename(ctx, warpscout.TargetNative, "awg50", "WARP HEL"); !errors.Is(err, warpscout.ErrProtected) {
		t.Fatalf("залоченный туннель не должен переименовываться, err=%v", err)
	}
	if err := ops.Rename(ctx, warpscout.TargetKernel, "awg99", "WARP HEL"); !errors.Is(err, warpscout.ErrNotWarp) {
		t.Fatalf("не-WARP туннель не должен переименовываться, err=%v", err)
	}
}

func TestWarpTunnelOps_ViaMetadataAndObfuscationSources(t *testing.T) {
	ctx := context.Background()
	ops, _, store, _ := newWarpOpsHarness(t, map[string]tunnel.State{
		"awg50": tunnel.StateRunning,
		"awg60": tunnel.StateRunning,
		"awg20": tunnel.StateRunning,
	})
	seedTunnel(t, store, &storage.AWGTunnel{
		ID:       "awg50",
		Name:     "WARP HEL",
		Enabled:  true,
		Backend:  "nativewg",
		NWGIndex: 3,
		Interface: storage.AWGInterface{
			PrivateKey: "gZwP6wviWSsSUhp4AbX0iLuzKKVVTwlB5asMAQ+BWBE=",
			Address:    "172.16.0.10/32",
			AWGObfuscation: storage.AWGObfuscation{
				Jc: 4, Jmin: 40, Jmax: 70, I1: "<b 0x0102>",
			},
		},
		Peer: storage.AWGPeer{PublicKey: warpscout.WarpPeerPublicKey, Endpoint: "162.159.192.255:880"},
	})
	seedTunnel(t, store, &storage.AWGTunnel{
		ID:           "awg60",
		Name:         "WARP-in-WARP FRA",
		Enabled:      true,
		Backend:      "nativewg",
		NWGIndex:     4,
		ISPInterface: "Wireguard3",
		Interface: storage.AWGInterface{
			PrivateKey: "gZwP6wviWSsSUhp4AbX0iLuzKKVVTwlB5asMAQ+BWBF=",
			Address:    "172.16.0.11/32",
		},
		Peer: storage.AWGPeer{PublicKey: warpscout.WarpPeerPublicKey, Endpoint: "162.159.193.1:2408"},
	})
	seedTunnel(t, store, &storage.AWGTunnel{
		ID:      "awg20",
		Name:    "NuxtAWG",
		Enabled: true,
		Backend: "kernel",
		Interface: storage.AWGInterface{
			PrivateKey: "gZwP6wviWSsSUhp4AbX0iLuzKKVVTwlB5asMAQ+BWBG=",
			Address:    "10.0.0.2/32",
			AWGObfuscation: storage.AWGObfuscation{
				Jc: 5, Jmin: 10, Jmax: 50, S1: 99, S2: 88, H1: "100", I1: "<b 0xdead><r 4>",
			},
		},
		Peer: storage.AWGPeer{PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", Endpoint: "1.2.3.4:51820"},
	})

	list, err := ops.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var innerRef *warpscout.TunnelRef
	for i := range list {
		if list[i].ID == "awg60" {
			innerRef = &list[i]
		}
	}
	if innerRef == nil || innerRef.ViaTunnelID != "awg50" || innerRef.ViaIface != "if-awg50" || !innerRef.ViaRunning {
		t.Fatalf("Via* не заполнились для WARP-in-WARP: %+v", innerRef)
	}

	sources := ops.ObfuscationSources(ctx)
	if len(sources) != 2 {
		t.Fatalf("ожидались 2 источника обфускации (NuxtAWG + WARP HEL), получили %+v", sources)
	}
	// Не-WARP рабочий туннель NuxtAWG должен идти первым
	if sources[0].Name != "NuxtAWG" || sources[0].AWG.SignatureProfile != warpscout.SignatureCustom ||
		sources[0].AWG.Jc != 5 || sources[0].AWG.I1 != "<b 0xdead><r 4>" {
		t.Fatalf("неожиданный первый источник обфускации: %+v", sources[0])
	}
}

// Каскадный туннель без привязки к внешнему — обычный WARP под чужим именем.
// Сбой привязки отменяет создание: импортированный туннель удаляется и не
// запускается.
func TestWarpOps_CreateCascadeRollsBackWhenViaFails(t *testing.T) {
	ctx := context.Background()
	ops, svc, store, _ := newWarpOpsHarness(t, map[string]tunnel.State{
		"awg50": tunnel.StateRunning, "awg60": tunnel.StateStopped,
	})
	seedWarpTunnel(t, store, "awg50", "nativewg")
	svc.importFn = func(_ context.Context, _, name, backend string) (*service.TunnelWithStatus, error) {
		seedWarpTunnel(t, store, "awg60", backend)
		return &service.TunnelWithStatus{ID: "awg60", Name: name}, nil
	}
	var deleted []string
	svc.deleteFn = func(_ context.Context, id string) error {
		deleted = append(deleted, id)
		return nil
	}

	// В обвязке нет команд NDMS, поэтому привязка `connect via` невозможна.
	_, err := ops.Create(ctx, warpscout.TargetNative, "WARP-in-WARP HEL", "[Interface]\n",
		warpscout.CreateOptions{ViaMode: warpscout.TargetNative, ViaID: "awg50"})
	if err == nil {
		t.Fatal("создание каскада без привязки к внешнему туннелю прошло")
	}
	if len(deleted) != 1 || deleted[0] != "awg60" {
		t.Fatalf("импортированный туннель не откатан: deleted=%v", deleted)
	}
	if svc.startCalls != 0 {
		t.Fatalf("непривязанный туннель запущен: startCalls=%d", svc.startCalls)
	}
}

// Get одного туннеля без привязки не опрашивает состояние остальных: сторож
// зовёт его на каждой проверке, и лишние запросы шли бы в NDMS каждый тик.
func TestWarpOps_GetWithoutViaQueriesOnlyItself(t *testing.T) {
	ops, svc, store, _ := newWarpOpsHarness(t, map[string]tunnel.State{
		"awg11": tunnel.StateRunning, "awg12": tunnel.StateRunning, "awg13": tunnel.StateRunning,
	})
	for _, id := range []string{"awg11", "awg12", "awg13"} {
		seedWarpTunnel(t, store, id, "kernel")
	}
	var asked []string
	inner := svc.getFn
	svc.getFn = func(ctx context.Context, id string) (*service.TunnelWithStatus, error) {
		asked = append(asked, id)
		return inner(ctx, id)
	}
	if _, err := ops.Get(context.Background(), warpscout.TargetKernel, "awg11"); err != nil {
		t.Fatal(err)
	}
	for _, id := range asked {
		if id != "awg11" {
			t.Fatalf("Get(awg11) опросил состояние чужих туннелей: %v", asked)
		}
	}
}

// List спрашивает состояние каждого туннеля один раз — и когда привязок нет,
// и когда один туннель идёт через другой (раньше родителей опрашивали заново).
func TestWarpOps_ListQueriesEachTunnelOnce(t *testing.T) {
	ops, svc, store, _ := newWarpOpsHarness(t, map[string]tunnel.State{
		"awg50": tunnel.StateRunning, "awg51": tunnel.StateRunning, "awg52": tunnel.StateRunning,
	})
	for _, id := range []string{"awg50", "awg51", "awg52"} {
		seedWarpTunnel(t, store, id, "nativewg")
	}
	asked := map[string]int{}
	inner := svc.getFn
	svc.getFn = func(ctx context.Context, id string) (*service.TunnelWithStatus, error) {
		asked[id]++
		return inner(ctx, id)
	}
	check := func(stage string) []warpscout.TunnelRef {
		t.Helper()
		for k := range asked {
			delete(asked, k)
		}
		refs, err := ops.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for id, n := range asked {
			if n != 1 {
				t.Fatalf("%s: состояние %s спрошено %d раз(а): %v", stage, id, n, asked)
			}
		}
		return refs
	}
	check("без привязок")

	// У засеянных туннелей индекс интерфейса одинаковый — разводим.
	for i, id := range []string{"awg50", "awg51", "awg52"} {
		if err := store.Update(id, func(tun *storage.AWGTunnel) error {
			tun.NWGIndex = 3 + i
			if id == "awg51" {
				tun.ISPInterface = "Wireguard" + strconv.Itoa(3)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	refs := check("с каскадом")
	found := false
	for _, r := range refs {
		if r.ID == "awg51" {
			found = r.ViaTunnelID == "awg50" && r.ViaRunning && r.ViaIface == "if-awg50"
		}
	}
	if !found {
		t.Fatalf("привязка awg51 → awg50 не разрешена: %+v", refs)
	}
}

type apiJunkRunner struct {
	gate chan struct{}
	hits int
}

func (r *apiJunkRunner) Available() (string, bool) { return "/fake", true }

func (r *apiJunkRunner) Run(_ context.Context, p warpscout.ScanParams, on func(warpscout.EngineEvent)) error {
	if r.gate != nil {
		<-r.gate
	}
	total := len(p.Endpoints)
	on(warpscout.EngineEvent{Type: "start", Total: total})
	for i := 0; i < r.hits && i < total; i++ {
		on(warpscout.EngineEvent{
			Type: "hit", Endpoint: "162.159.192.1:2408", PingMs: 25, Colo: "HEL", Loc: "FI", Warp: "on",
		})
	}
	on(warpscout.EngineEvent{Type: "done", Scanned: total, Hits: r.hits})
	return nil
}

type apiPlainCipher struct{}

func (apiPlainCipher) Encrypt(s string) (string, error) { return "enc:" + s, nil }
func (apiPlainCipher) Decrypt(s string) (string, error) {
	return s[4:], nil
}

func TestWarpScoutHandler_FindJunkEndpoints(t *testing.T) {
	store, err := warpscout.NewStore(t.TempDir(), apiPlainCipher{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SetAccount(&warpscout.Account{
		ID:            "acc1",
		PrivateKey:    "priv",
		PeerPublicKey: warpscout.WarpPeerPublicKey,
		AddressV4:     "172.16.0.2/32",
	}); err != nil {
		t.Fatal(err)
	}

	runner := &apiJunkRunner{gate: make(chan struct{}), hits: 0}
	svc := warpscout.New(warpscout.Deps{
		Store:  store,
		Runner: runner,
		WAN:    func(context.Context) (string, bool) { return "eth3", true },
	})
	t.Cleanup(svc.Stop)
	h := NewWarpScoutHandler(svc, nil)

	// 1. Запуск find-junk -> 200 OK
	req := httptest.NewRequest(http.MethodPost, "/api/warpscout/find-junk/start",
		bytes.NewBufferString(`{"i1Profile":"quic","maxAttempts":1,"perSubnet":1}`))
	rec := httptest.NewRecorder()
	h.FindJunkStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("FindJunkStart code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2. Попытка применить во время выполнения -> 409 WARP_BUSY
	recBusy := httptest.NewRecorder()
	h.FindJunkApply(recBusy, httptest.NewRequest(http.MethodPost, "/api/warpscout/find-junk/apply", nil))
	if recBusy.Code != http.StatusConflict {
		t.Fatalf("FindJunkApply while running code=%d body=%s", recBusy.Code, recBusy.Body.String())
	}
	var errEnv struct {
		Error bool   `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(recBusy.Body.Bytes(), &errEnv); err != nil || !errEnv.Error || errEnv.Code != "WARP_BUSY" {
		t.Fatalf("unexpected busy envelope: %s", recBusy.Body.String())
	}

	// Завершаем 0-хитовый прогон -> Apply должен вернуть 400 INVALID_INPUT
	close(runner.gate)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && svc.State().FindJunk.Running {
		time.Sleep(10 * time.Millisecond)
	}
	recZero := httptest.NewRecorder()
	h.FindJunkApply(recZero, httptest.NewRequest(http.MethodPost, "/api/warpscout/find-junk/apply", nil))
	if recZero.Code != http.StatusBadRequest {
		t.Fatalf("FindJunkApply after zero hits code=%d body=%s", recZero.Code, recZero.Body.String())
	}
	if err := json.Unmarshal(recZero.Body.Bytes(), &errEnv); err != nil || errEnv.Code != "INVALID_INPUT" {
		t.Fatalf("unexpected invalid envelope: %s", recZero.Body.String())
	}

	// 3. Валидный прогон (15/15) -> FindJunkApply 200 OK с AWGParams
	runner.gate = nil
	runner.hits = 15
	rec2 := httptest.NewRequest(http.MethodPost, "/api/warpscout/find-junk/start",
		bytes.NewBufferString(`{"i1Profile":"stun","maxAttempts":1,"perSubnet":1}`))
	rec2Rec := httptest.NewRecorder()
	h.FindJunkStart(rec2Rec, rec2)
	if rec2Rec.Code != http.StatusOK {
		t.Fatalf("second FindJunkStart code=%d body=%s", rec2Rec.Code, rec2Rec.Body.String())
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && svc.State().FindJunk.Running {
		time.Sleep(10 * time.Millisecond)
	}

	recApply := httptest.NewRecorder()
	h.FindJunkApply(recApply, httptest.NewRequest(http.MethodPost, "/api/warpscout/find-junk/apply", nil))
	if recApply.Code != http.StatusOK {
		t.Fatalf("FindJunkApply valid code=%d body=%s", recApply.Code, recApply.Body.String())
	}
	var okEnv struct {
		Success bool                `json:"success"`
		Data    warpscout.AWGParams `json:"data"`
	}
	if err := json.Unmarshal(recApply.Body.Bytes(), &okEnv); err != nil || !okEnv.Success || okEnv.Data.SignatureProfile != "stun" {
		t.Fatalf("unexpected FindJunkApply envelope: %s", recApply.Body.String())
	}
}

func TestWarpScoutHandler_ClearHistory(t *testing.T) {
	store, err := warpscout.NewStore(t.TempDir(), apiPlainCipher{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.AppendHistory(warpscout.SwapEvent{
		At:          time.Now().UTC(),
		ProfileID:   "p1",
		ProfileName: "WARP HEL",
		OldEndpoint: "162.159.192.1:2408",
		NewEndpoint: "162.159.192.2:2408",
		OK:          true,
	}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	svc := warpscout.New(warpscout.Deps{Store: store})
	t.Cleanup(svc.Stop)
	h := NewWarpScoutHandler(svc, nil)

	recGet := httptest.NewRecorder()
	h.ClearHistory(recGet, httptest.NewRequest(http.MethodGet, "/api/warpscout/history/clear", nil))
	if recGet.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET ClearHistory code=%d, want 405", recGet.Code)
	}

	recPost := httptest.NewRecorder()
	h.ClearHistory(recPost, httptest.NewRequest(http.MethodPost, "/api/warpscout/history/clear", nil))
	if recPost.Code != http.StatusOK {
		t.Fatalf("POST ClearHistory code=%d body=%s", recPost.Code, recPost.Body.String())
	}
	if got := svc.State().History; len(got) != 0 {
		t.Fatalf("expected empty history after ClearHistory, got %d", len(got))
	}
}

func TestWarpOps_SwapManaged_UpdatesObfuscationAndRestartsActiveTunnel(t *testing.T) {
	ctx := context.Background()
	ops, svc, store, rotated := newWarpOpsHarness(t, map[string]tunnel.State{
		"awg11": tunnel.StateRunning,
		"awg50": tunnel.StateRunning,
		"awg12": tunnel.StateStopped,
	})
	initObf := storage.AWGObfuscation{
		Jc: 4, Jmin: 40, Jmax: 70, H1: "1", H2: "2", H3: "3", H4: "4", I1: "<b 0x01>",
	}
	for _, tc := range []struct {
		id, backend string
		enabled     bool
	}{
		{"awg11", "kernel", true},
		{"awg50", "nativewg", true},
		{"awg12", "kernel", false},
	} {
		seedWarpTunnel(t, store, tc.id, tc.backend)
		if err := store.Update(tc.id, func(tun *storage.AWGTunnel) error {
			tun.Enabled = tc.enabled
			tun.Interface.AWGObfuscation = initObf
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	var updatedObfs []storage.AWGObfuscation
	svc.updateFn = func(_ context.Context, _, newStored *storage.AWGTunnel) error {
		updatedObfs = append(updatedObfs, newStored.Interface.AWGObfuscation)
		return nil
	}

	nextOpts := warpscout.SwapOptions{
		Protocol: warpscout.ProtocolAWG,
		AWG: warpscout.AWGParams{
			Jc: 8, Jmin: 20, Jmax: 90,
			SignatureProfile: warpscout.SignatureCustom,
			I1:               "<b 0xaa>",
		},
	}

	// Kernel: смена обфускации сохраняется через EditTunnelWith и перезапускает активный туннель без отдельной ротации.
	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.198:1002", nextOpts); err != nil {
		t.Fatalf("SwapEndpoint kernel obf change: %v", err)
	}
	got11, _ := store.Get("awg11")
	if got11.Interface.AWGObfuscation.Jc != 8 || got11.Interface.AWGObfuscation.I1 != "<b 0xaa>" ||
		got11.Interface.AWGObfuscation.H1 != "1" || got11.Interface.AWGObfuscation.H4 != "4" {
		t.Fatalf("обфускация awg11 не обновилась: %+v", got11.Interface.AWGObfuscation)
	}
	if len(updatedObfs) != 1 || updatedObfs[0].Jc != 8 {
		t.Fatalf("Svc.Update не получил новую обфускацию: %+v", updatedObfs)
	}
	if svc.restartCalls != 1 || len(*rotated) != 0 {
		t.Fatalf("при смене маскировки активный kernel должен перезапускаться без ротации: restarts=%d rotated=%v", svc.restartCalls, *rotated)
	}

	// Повторная подмена с той же обфускацией: рестарта нет, только ротация UDP-порта.
	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.199:1002", nextOpts); err != nil {
		t.Fatalf("SwapEndpoint kernel same obf: %v", err)
	}
	if svc.restartCalls != 1 || len(*rotated) != 1 || (*rotated)[0] != "if-awg11" {
		t.Fatalf("повторная подмена kernel с той же обфускацией должна только ротировать порт: restarts=%d rotated=%v", svc.restartCalls, *rotated)
	}

	// NativeWG: смена обфускации тоже перезапускает активный туннель, повтор без изменений — без рестарта.
	if err := ops.SwapEndpoint(ctx, warpscout.TargetNative, "awg50", "162.159.192.201:1002", nextOpts); err != nil {
		t.Fatalf("SwapEndpoint native obf change: %v", err)
	}
	got50, _ := store.Get("awg50")
	if got50.Interface.AWGObfuscation.Jc != 8 || got50.Interface.AWGObfuscation.I1 != "<b 0xaa>" {
		t.Fatalf("обфускация awg50 не обновилась: %+v", got50.Interface.AWGObfuscation)
	}
	if svc.restartCalls != 2 {
		t.Fatalf("при смене маскировки NativeWG должен перезапускаться: restarts=%d", svc.restartCalls)
	}
	if err := ops.SwapEndpoint(ctx, warpscout.TargetNative, "awg50", "162.159.192.202:1002", nextOpts); err != nil {
		t.Fatalf("SwapEndpoint native same obf: %v", err)
	}
	if svc.restartCalls != 2 {
		t.Fatalf("повторная подмена NativeWG с той же обфускацией не должна перезапускать: restarts=%d", svc.restartCalls)
	}

	// Выключенный туннель: обновляет стор, но не запускается автоматически.
	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg12", "162.159.192.203:1002", nextOpts); err != nil {
		t.Fatalf("SwapEndpoint stopped: %v", err)
	}
	got12, _ := store.Get("awg12")
	if got12.Interface.AWGObfuscation.Jc != 8 || svc.restartCalls != 2 {
		t.Fatalf("остановленный туннель должен обновить стор без Restart: obf=%+v restarts=%d", got12.Interface.AWGObfuscation, svc.restartCalls)
	}
}

func TestWarpOps_SwapManaged_ProtocolWGClearsKernelAndRejectsNative(t *testing.T) {
	ctx := context.Background()
	ops, svc, store, _ := newWarpOpsHarness(t, map[string]tunnel.State{
		"awg11": tunnel.StateRunning,
		"awg50": tunnel.StateRunning,
	})
	initObf := storage.AWGObfuscation{Jc: 5, Jmin: 10, Jmax: 50, H1: "1", H2: "2", H3: "3", H4: "4", I1: "<b 0x11>"}
	for _, tc := range []struct{ id, backend string }{
		{"awg11", "kernel"},
		{"awg50", "nativewg"},
	} {
		seedWarpTunnel(t, store, tc.id, tc.backend)
		if err := store.Update(tc.id, func(tun *storage.AWGTunnel) error {
			tun.Interface.AWGObfuscation = initObf
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.198:1002", warpscout.SwapOptions{Protocol: warpscout.ProtocolWG}); err != nil {
		t.Fatalf("SwapEndpoint kernel wg: %v", err)
	}
	got11, _ := store.Get("awg11")
	if got11.Interface.AWGObfuscation != (storage.AWGObfuscation{}) {
		t.Fatalf("protocol=wg должен очищать обфускацию kernel-туннеля: %+v", got11.Interface.AWGObfuscation)
	}
	if svc.restartCalls != 1 {
		t.Fatalf("сброс маскировки должен перезапускать активный kernel-туннель: restarts=%d", svc.restartCalls)
	}

	if err := ops.SwapEndpoint(ctx, warpscout.TargetNative, "awg50", "162.159.192.198:1002", warpscout.SwapOptions{Protocol: warpscout.ProtocolWG}); !errors.Is(err, warpscout.ErrInvalidInput) {
		t.Fatalf("ожидалась ErrInvalidInput для NativeWG + wg, получили %v", err)
	}
	if svc.restartCalls != 1 {
		t.Fatalf("отклонённый swap NativeWG + wg не должен перезапускать: restarts=%d", svc.restartCalls)
	}
}

type recordedPost struct {
	body        string
	hasDeadline bool
}

type ctxCheckingPoster struct {
	mu     sync.Mutex
	posts  []recordedPost
	onPost func(body string)
}

func (p *ctxCheckingPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(payload)
	_, hasDeadline := ctx.Deadline()
	rec := recordedPost{body: string(b), hasDeadline: hasDeadline}
	p.mu.Lock()
	p.posts = append(p.posts, rec)
	cb := p.onPost
	p.mu.Unlock()
	if cb != nil {
		cb(rec.body)
	}
	return json.RawMessage(`{}`), nil
}

func (p *ctxCheckingPoster) snapshot() []recordedPost {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedPost(nil), p.posts...)
}

type stubSystemTunnelSvc struct {
	queries *ndmsquery.Queries
	cmds    *ndmscommand.Commands
	tunnel  *ndms.SystemWireguardTunnel
	curASC  json.RawMessage
}

func (s *stubSystemTunnelSvc) List(context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return nil, nil
}

func (s *stubSystemTunnelSvc) ListFresh(context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return nil, nil
}

func (s *stubSystemTunnelSvc) Get(ctx context.Context, _ string) (*ndms.SystemWireguardTunnel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.tunnel, nil
}

func (s *stubSystemTunnelSvc) GetASCParams(ctx context.Context, _ string) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.curASC, nil
}

func (s *stubSystemTunnelSvc) SetASCParams(ctx context.Context, name string, params json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	iface, _, ok, err := s.queries.Interfaces.Confirm(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("not confirmed")
	}
	return s.cmds.Wireguard.SetASCParams(ctx, iface, params)
}

func newSystemSwapTestHarness(t *testing.T) (*WarpTunnelOps, *ctxCheckingPoster) {
	t.Helper()
	fg := ndmsquery.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{"Wireguard5":{"id":"Wireguard5","interface-name":"nwg5","type":"Wireguard","description":"WARP Sys","state":"up","link":"up","wireguard":{"public-key":"PUB5=","peer":[]}}}`)
	fg.SetRC("Wireguard5", `{"wireguard":{"asc":{"jc":4,"jmin":40,"jmax":70},"peer":[{"key":"`+warpscout.WarpPeerPublicKey+`"}]}}`)
	fg.SetJSON("/show/running-config", `{"message":["!"]}`)
	queries := ndmsquery.NewQueries(ndmsquery.Deps{Getter: fg, Logger: ndmsquery.NopLogger()})
	poster := &ctxCheckingPoster{}
	cmds := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  poster,
		Queries: queries,
		Save:    ndmscommand.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil),
	})
	var sysSvc systemtunnel.Service = &stubSystemTunnelSvc{
		queries: queries,
		cmds:    cmds,
		curASC:  json.RawMessage(`{"jc":4,"jmin":40,"jmax":70}`),
		tunnel: &ndms.SystemWireguardTunnel{
			ID:            "Wireguard5",
			Description:   "WARP Sys",
			Status:        "up",
			InterfaceName: "nwg5",
			Address:       "172.16.0.2/32",
			PublicKey:     "PUB5=",
			Peer: &ndms.WireguardPeerInfo{
				PublicKey: warpscout.WarpPeerPublicKey,
				Endpoint:  "162.159.192.1:2408",
			},
		},
	}
	ops := &WarpTunnelOps{
		System:   sysSvc,
		Commands: cmds,
		Queries:  queries,
	}
	return ops, poster
}

func TestWarpOps_SwapSystem_RecoversInterfaceUpWhenRequestCanceled(t *testing.T) {
	nextOpts := warpscout.SwapOptions{
		Protocol: warpscout.ProtocolAWG,
		AWG: warpscout.AWGParams{
			Jc: 8, Jmin: 20, Jmax: 90,
			SignatureProfile: warpscout.SignatureCustom,
			I1:               "<b 0xaa>",
		},
	}

	t.Run("canceled_during_apply_still_restores_up", func(t *testing.T) {
		ops, poster := newSystemSwapTestHarness(t)
		reqCtx, reqCancel := context.WithCancel(context.Background())
		defer reqCancel()
		poster.onPost = func(body string) {
			if strings.Contains(body, `"up":false`) {
				reqCancel()
			}
		}

		err := ops.SwapEndpoint(reqCtx, warpscout.TargetSystem, "Wireguard5", "162.159.192.99:2408", nextOpts)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}

		posts := poster.snapshot()
		var sawDown, sawUpDeadline bool
		for _, p := range posts {
			if strings.Contains(p.body, `"up":false`) {
				sawDown = true
			}
			if strings.Contains(p.body, `"up":true`) {
				if !p.hasDeadline {
					t.Fatalf("expected InterfaceUp recovery call to carry bounded deadline: %+v", p)
				}
				sawUpDeadline = true
			}
		}
		if !sawDown || !sawUpDeadline {
			t.Fatalf("expected InterfaceDown and bounded InterfaceUp recovery posts, got %+v", posts)
		}
	})

	t.Run("canceled_after_endpoint_still_completes_up", func(t *testing.T) {
		ops, poster := newSystemSwapTestHarness(t)
		reqCtx, reqCancel := context.WithCancel(context.Background())
		defer reqCancel()
		poster.onPost = func(body string) {
			if strings.Contains(body, "162.159.192.99") {
				reqCancel()
			}
		}

		if err := ops.SwapEndpoint(reqCtx, warpscout.TargetSystem, "Wireguard5", "162.159.192.99:2408", nextOpts); err != nil {
			t.Fatalf("expected SwapEndpoint to finish cleanly with detached InterfaceUp recovery, got %v", err)
		}

		posts := poster.snapshot()
		var sawDown, sawASC, sawEP, sawUpDeadline bool
		for _, p := range posts {
			if strings.Contains(p.body, `"up":false`) {
				sawDown = true
			}
			if strings.Contains(p.body, `"jc":8`) {
				sawASC = true
			}
			if strings.Contains(p.body, "162.159.192.99") {
				sawEP = true
			}
			if strings.Contains(p.body, `"up":true`) {
				if !p.hasDeadline {
					t.Fatalf("expected InterfaceUp recovery call to carry bounded deadline: %+v", p)
				}
				sawUpDeadline = true
			}
		}
		if !sawDown || !sawASC || !sawEP || !sawUpDeadline {
			t.Fatalf("missing expected sequence (down=%v asc=%v ep=%v up=%v): %+v", sawDown, sawASC, sawEP, sawUpDeadline, posts)
		}
	})
}

func TestWarpOps_SwapManaged_AWG3FirmwareGating(t *testing.T) {
	ctx := context.Background()
	ops, _, store, _ := newWarpOpsHarness(t, map[string]tunnel.State{
		"awg11": tunnel.StateRunning,
		"awg50": tunnel.StateRunning,
	})
	seedWarpTunnel(t, store, "awg11", "kernel")
	seedWarpTunnel(t, store, "awg50", "nativewg")
	if err := store.Update("awg50", func(tun *storage.AWGTunnel) error {
		tun.NWGIndex = 3
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	asc3 := false
	ops.SupportsASC3 = func() bool { return asc3 }

	awg3Opts := warpscout.SwapOptions{
		Protocol: warpscout.ProtocolAWG,
		AWG: warpscout.AWGParams{
			Jc: 5, Jmin: 20, Jmax: 80,
			SignatureProfile: warpscout.SignatureCustom,
			I1:               "<b 0x01>",
			TimingJitter:     true,
			ContentPadding:   "10-100",
		},
	}

	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.80:2408", awg3Opts); err != nil {
		t.Fatalf("kernel swap awg3: %v", err)
	}
	kTun, _ := store.Get("awg11")
	if kTun.Interface.ContentPaddingAddition != "10-100" ||
		kTun.Interface.RekeyAfterTime != warpscout.WarpRekeyAfterTime ||
		kTun.Peer.PersistentKeepalive != warpscout.WarpJitterKeepalive {
		t.Fatalf("kernel tunnel did not receive AWG3 + ranged keepalive: iface=%+v peer=%+v", kTun.Interface.AWGObfuscation, kTun.Peer.PersistentKeepalive)
	}

	// Turning AWG3 off on kernel must reset keepalive 25-35 -> 25 and clear padding/timers.
	plainOpts := awg3Opts
	plainOpts.AWG.TimingJitter = false
	plainOpts.AWG.ContentPadding = ""
	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.81:2408", plainOpts); err != nil {
		t.Fatalf("kernel swap plain: %v", err)
	}
	kTun, _ = store.Get("awg11")
	if kTun.Interface.ContentPaddingAddition != "" || kTun.Interface.RekeyAfterTime != "" || kTun.Peer.PersistentKeepalive != "25" {
		t.Fatalf("kernel tunnel did not reset AWG3/keepalive: iface=%+v peer=%+v", kTun.Interface.AWGObfuscation, kTun.Peer.PersistentKeepalive)
	}

	// NativeWG on <5.02 (asc3=false) must omit AWG3 fields and keep scalar keepalive=25.
	if err := ops.SwapEndpoint(ctx, warpscout.TargetNative, "awg50", "162.159.192.82:2408", awg3Opts); err != nil {
		t.Fatalf("native swap asc2: %v", err)
	}
	nTun, _ := store.Get("awg50")
	if nTun.Interface.ContentPaddingAddition != "" || nTun.Interface.RekeyAfterTime != "" || nTun.Peer.PersistentKeepalive != "25" {
		t.Fatalf("native on <5.02 leaked AWG3 fields: iface=%+v peer=%+v", nTun.Interface.AWGObfuscation, nTun.Peer.PersistentKeepalive)
	}

	// NativeWG on >=5.02 (asc3=true) receives padding + timers while still keeping keepalive=25.
	asc3 = true
	if err := ops.SwapEndpoint(ctx, warpscout.TargetNative, "awg50", "162.159.192.83:2408", awg3Opts); err != nil {
		t.Fatalf("native swap asc3: %v", err)
	}
	nTun, _ = store.Get("awg50")
	if nTun.Interface.ContentPaddingAddition != "10-100" || nTun.Interface.RekeyAfterTime != warpscout.WarpRekeyAfterTime || nTun.Peer.PersistentKeepalive != "25" {
		t.Fatalf("native on >=5.02 did not apply AWG3 cleanly: iface=%+v peer=%+v", nTun.Interface.AWGObfuscation, nTun.Peer.PersistentKeepalive)
	}

	// AWG 3.1 gating: kernel gets RandomTrailers/DisableCookies ONLY when SupportsKernelAWG31() == true;
	// native gets them ONLY when SupportsASC3() == true.
	kmod31 := false
	ops.SupportsKernelAWG31 = func() bool { return kmod31 }
	asc3 = false
	awg31Opts := awg3Opts
	awg31Opts.AWG.RandomTrailers = true
	awg31Opts.AWG.DisableCookies = true

	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.84:2408", awg31Opts); err != nil {
		t.Fatalf("kernel <3.1 swap: %v", err)
	}
	kTun, _ = store.Get("awg11")
	if kTun.Interface.RandomTrailers || kTun.Interface.DisableCookies {
		t.Fatalf("kernel <3.1 must strip AWG 3.1 flags: %+v", kTun.Interface.AWGObfuscation)
	}

	kmod31 = true
	if err := ops.SwapEndpoint(ctx, warpscout.TargetKernel, "awg11", "162.159.192.85:2408", awg31Opts); err != nil {
		t.Fatalf("kernel >=3.1 swap: %v", err)
	}
	kTun, _ = store.Get("awg11")
	if !kTun.Interface.RandomTrailers || !kTun.Interface.DisableCookies {
		t.Fatalf("kernel >=3.1 must apply AWG 3.1 flags: %+v", kTun.Interface.AWGObfuscation)
	}

	if err := ops.SwapEndpoint(ctx, warpscout.TargetNative, "awg50", "162.159.192.86:2408", awg31Opts); err != nil {
		t.Fatalf("native asc2 awg31 swap: %v", err)
	}
	nTun, _ = store.Get("awg50")
	if nTun.Interface.RandomTrailers || nTun.Interface.DisableCookies {
		t.Fatalf("native on <5.02 must strip AWG 3.1 flags: %+v", nTun.Interface.AWGObfuscation)
	}
}

func TestWarpOps_SwapSystem_AWG3Gating(t *testing.T) {
	opts := warpscout.SwapOptions{
		Protocol: warpscout.ProtocolAWG,
		AWG: warpscout.AWGParams{
			Jc: 6, Jmin: 20, Jmax: 80,
			TimingJitter:   true,
			ContentPadding: "10-100",
		},
	}

	t.Run("asc2_omits_awg3_keys", func(t *testing.T) {
		ops, poster := newSystemSwapTestHarness(t)
		ops.SupportsASC3 = func() bool { return false }
		if err := ops.SwapEndpoint(context.Background(), warpscout.TargetSystem, "Wireguard5", "162.159.192.95:2408", opts); err != nil {
			t.Fatalf("SwapEndpoint asc2: %v", err)
		}
		for _, p := range poster.snapshot() {
			if strings.Contains(p.body, "content-padding-addition-start") || strings.Contains(p.body, "rekey-after-time-start") {
				t.Fatalf("ASC2 swap must not send AWG3 keys: %s", p.body)
			}
		}
	})

	t.Run("asc3_sends_full_awg3_payload", func(t *testing.T) {
		ops, poster := newSystemSwapTestHarness(t)
		ops.SupportsASC3 = func() bool { return true }
		awg31Opts := opts
		awg31Opts.AWG.RandomTrailers = true
		awg31Opts.AWG.DisableCookies = true
		if err := ops.SwapEndpoint(context.Background(), warpscout.TargetSystem, "Wireguard5", "162.159.192.96:2408", awg31Opts); err != nil {
			t.Fatalf("SwapEndpoint asc3: %v", err)
		}
		var sawAWG3 bool
		for _, p := range poster.snapshot() {
			if strings.Contains(p.body, `"content-padding-addition-start":10`) &&
				strings.Contains(p.body, `"content-padding-addition-end":100`) &&
				strings.Contains(p.body, `"rekey-after-time-start":100`) &&
				strings.Contains(p.body, `"rekey-after-time-end":120`) &&
				strings.Contains(p.body, `"random-trailers":1`) &&
				strings.Contains(p.body, `"disable-cookies":1`) {
				sawAWG3 = true
			}
		}
		if !sawAWG3 {
			t.Fatalf("expected ASCParamsAWG3 with 3.1 flags in payload, posts=%+v", poster.snapshot())
		}
	})
}

type stubSystemListSvc struct {
	*stubSystemTunnelSvc
}

func (s *stubSystemListSvc) List(context.Context) ([]ndms.SystemWireguardTunnel, error) {
	if s.tunnel == nil {
		return nil, nil
	}
	return []ndms.SystemWireguardTunnel{*s.tunnel}, nil
}

func TestWarpTunnelOps_ObfuscationSources_AWG3AndExportHandler(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID:      "awg30",
		Name:    "WARP3",
		Enabled: true,
		Backend: "kernel",
		Interface: storage.AWGInterface{
			PrivateKey: "gZwP6wviWSsSUhp4AbX0iLuzKKVVTwlB5asMAQ+BWBE=",
			Address:    "172.16.0.2/32",
			AWGObfuscation: storage.AWGObfuscation{
				Jc: 5, Jmin: 20, Jmax: 80,
				ContentPaddingAddition: "10-100",
				RekeyAfterTime:         warpscout.WarpRekeyAfterTime,
				RandomTrailers:         true,
				DisableCookies:         true,
			},
		},
		Peer: storage.AWGPeer{PublicKey: warpscout.WarpPeerPublicKey, Endpoint: "162.159.192.1:2408"},
	}); err != nil {
		t.Fatal(err)
	}
	sysStub := &stubSystemListSvc{
		stubSystemTunnelSvc: &stubSystemTunnelSvc{
			curASC: json.RawMessage(`{"jc":0,"jmin":0,"jmax":0,"content-padding-addition-start":16,"content-padding-addition-end":64,"rekey-after-time-start":100,"rekey-after-time-end":120,"random-trailers":1,"disable-cookies":1}`),
			tunnel: &ndms.SystemWireguardTunnel{
				ID:            "Wireguard5",
				Description:   "Sys3",
				Status:        "up",
				InterfaceName: "nwg5",
				Peer:          &ndms.WireguardPeerInfo{PublicKey: warpscout.WarpPeerPublicKey},
			},
		},
	}
	ops := &WarpTunnelOps{Store: store, System: sysStub}
	sources := ops.ObfuscationSources(ctx)
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %+v", sources)
	}
	byID := map[string]warpscout.AWGParams{}
	for _, s := range sources {
		byID[s.ID] = s.AWG
	}
	if m := byID["awg30"]; !m.TimingJitter || m.ContentPadding != "10-100" || !m.RandomTrailers || !m.DisableCookies {
		t.Fatalf("managed source awg30 missing AWG3/3.1 fields: %+v", m)
	}
	if sys := byID["Wireguard5"]; !sys.TimingJitter || sys.ContentPadding != "16-64" || !sys.RandomTrailers || !sys.DisableCookies {
		t.Fatalf("system source Wireguard5 missing AWG3/3.1 fields: %+v", sys)
	}

	wsStore, err := warpscout.NewStore(t.TempDir(), apiPlainCipher{})
	if err != nil {
		t.Fatal(err)
	}
	if err := wsStore.SetAccount(&warpscout.Account{
		ID: "acc", PrivateKey: "priv", PeerPublicKey: warpscout.WarpPeerPublicKey, AddressV4: "172.16.0.2/32",
	}); err != nil {
		t.Fatal(err)
	}
	if err := wsStore.SetAWG(warpscout.AWGParams{
		SignatureProfile: "quic_initial", Jc: 4, Jmin: 40, Jmax: 70, TimingJitter: true, ContentPadding: "10-100",
	}); err != nil {
		t.Fatal(err)
	}
	svc := warpscout.New(warpscout.Deps{Store: wsStore})
	t.Cleanup(svc.Stop)
	h := NewWarpScoutHandler(svc, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/warpscout/export", strings.NewReader(`{"endpoint":"162.159.192.10:2408","colo":"HEL","protocol":"awg"}`))
	req.Header.Set("Content-Type", "application/json")
	h.Export(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Export HTTP status=%d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Success bool                   `json:"success"`
		Data    warpscout.ExportResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || !env.Success {
		t.Fatalf("unexpected Export body: %s", rec.Body.String())
	}
	if env.Data.Filename != "warp-hel-awg.conf" || !strings.Contains(env.Data.Content, "ContentPaddingAddition = 10-100") {
		t.Fatalf("unexpected Export result: %+v", env.Data)
	}
}

func TestWarpScoutHandler_FindJunkApply_BodyAndBackcompat(t *testing.T) {
	store, err := warpscout.NewStore(t.TempDir(), apiPlainCipher{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SetAccount(&warpscout.Account{
		ID:            "acc1",
		PrivateKey:    "priv",
		PeerPublicKey: warpscout.WarpPeerPublicKey,
		AddressV4:     "172.16.0.2/32",
	}); err != nil {
		t.Fatal(err)
	}

	svc := warpscout.New(warpscout.Deps{
		Store:  store,
		Runner: &apiJunkRunner{hits: 14},
		WAN:    func(context.Context) (string, bool) { return "eth3", true },
	})
	t.Cleanup(svc.Stop)
	h := NewWarpScoutHandler(svc, nil)

	recStart := httptest.NewRecorder()
	h.FindJunkStart(recStart, httptest.NewRequest(http.MethodPost, "/api/warpscout/find-junk/start",
		bytes.NewBufferString(`{"i1Profile":"stun","maxAttempts":2,"thresholdPct":100,"perSubnet":1}`)))
	if recStart.Code != http.StatusOK {
		t.Fatalf("FindJunkStart code=%d body=%s", recStart.Code, recStart.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && svc.State().FindJunk.Running {
		time.Sleep(10 * time.Millisecond)
	}
	fj := svc.State().FindJunk
	if fj.Best == nil || len(fj.Attempts) < 2 {
		t.Fatalf("expected >=2 usable attempts and best, got %+v", fj)
	}

	// 1. Обратная совместимость: пустое тело применяет Best.
	recEmpty := httptest.NewRecorder()
	h.FindJunkApply(recEmpty, httptest.NewRequest(http.MethodPost, "/api/warpscout/find-junk/apply", nil))
	if recEmpty.Code != http.StatusOK {
		t.Fatalf("empty FindJunkApply code=%d body=%s", recEmpty.Code, recEmpty.Body.String())
	}
	var env struct {
		Success bool                `json:"success"`
		Data    warpscout.AWGParams `json:"data"`
	}
	if err := json.Unmarshal(recEmpty.Body.Bytes(), &env); err != nil || !env.Success {
		t.Fatalf("unexpected empty FindJunkApply body: %s", recEmpty.Body.String())
	}
	if env.Data.Jc != fj.Best.AWG.Jc || env.Data.Jmin != fj.Best.AWG.Jmin || env.Data.Jmax != fj.Best.AWG.Jmax {
		t.Fatalf("expected Best %+v, got %+v", fj.Best, env.Data)
	}
	if got := svc.State().FindJunk.AppliedAttempt; got != fj.Best.Attempt {
		t.Fatalf("expected AppliedAttempt=%d, got %d", fj.Best.Attempt, got)
	}

	// 2. Выбор конкретной попытки (#2) и передача флагов AWG 3.0 в JSON.
	recSel := httptest.NewRecorder()
	h.FindJunkApply(recSel, httptest.NewRequest(http.MethodPost, "/api/warpscout/find-junk/apply",
		bytes.NewBufferString(`{"attempt":2,"timingJitter":true,"contentPadding":"12-80"}`)))
	if recSel.Code != http.StatusOK {
		t.Fatalf("selected FindJunkApply code=%d body=%s", recSel.Code, recSel.Body.String())
	}
	if err := json.Unmarshal(recSel.Body.Bytes(), &env); err != nil || !env.Success {
		t.Fatalf("unexpected selected FindJunkApply body: %s", recSel.Body.String())
	}
	a2 := fj.Attempts[1]
	if env.Data.Jc != a2.AWG.Jc || env.Data.Jmin != a2.AWG.Jmin || env.Data.Jmax != a2.AWG.Jmax {
		t.Fatalf("expected attempt #2 params %+v, got %+v", a2, env.Data)
	}
	if !env.Data.TimingJitter || env.Data.ContentPadding != "12-80" {
		t.Fatalf("expected timingJitter=true contentPadding=12-80, got %+v", env.Data)
	}
	if got := svc.State().FindJunk.AppliedAttempt; got != 2 {
		t.Fatalf("expected AppliedAttempt=2, got %d", got)
	}

	// 3. Битый JSON -> 400 Bad Request.
	recBad := httptest.NewRecorder()
	h.FindJunkApply(recBad, httptest.NewRequest(http.MethodPost, "/api/warpscout/find-junk/apply",
		bytes.NewBufferString(`{"attempt":`)))
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON code=%d body=%s", recBad.Code, recBad.Body.String())
	}
}
