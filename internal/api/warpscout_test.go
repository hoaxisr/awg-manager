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
