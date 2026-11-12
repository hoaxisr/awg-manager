package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
	"github.com/hoaxisr/awg-manager/internal/warpscout"
)

// ── Адаптер туннелей для автопилота ─────────────────────────────

// WarpTunnelOps реализует warpscout.TunnelOps поверх сервисов AWGM:
// kernel/nativewg — через TunnelService (Update под замком оркестратора),
// system — через NDMS RCI. Все проверки защиты собраны здесь, чтобы ни
// ручное «Подменить», ни автопилот не дотянулись до серверных и служебных
// интерфейсов (Wireguard2/AWGM, managedServers, встроенный VPN-сервер,
// Phobos, связанные прокси-клиенты).
type WarpTunnelOps struct {
	Svc      TunnelService
	Store    *storage.AWGTunnelStore
	Settings *storage.SettingsStore
	System   systemtunnel.Service
	Commands *ndmscommand.Commands
	Queries  *ndmsquery.Queries
	Orch     *orchestrator.Orchestrator
	Tunnels  *TunnelsHandler // publishTunnelList после правок
	// RotateKernelPort переоткрывает эфемерный UDP-порт kernel-туннеля
	// (`awg set <iface> listen-port 0`), чтобы сбросить блокировку ТСПУ
	// по паре (IP клиента, исходный UDP-порт). В тестах подменяется.
	RotateKernelPort func(ctx context.Context, iface string) error
}

var _ warpscout.TunnelOps = (*WarpTunnelOps)(nil)

func modeOfBackend(backend string) (warpscout.TargetMode, bool) {
	switch backend {
	case "nativewg":
		return warpscout.TargetNative, true
	case "kernel", "":
		return warpscout.TargetKernel, true
	}
	return "", false
}

func managedProtectReason(t *storage.AWGTunnel) string {
	switch {
	case t.Locked:
		return "туннель защищён от изменений (замок на карточке)"
	case t.Obfuscator != nil:
		return "endpoint обфусцированного туннеля не правится"
	case strings.TrimSpace(t.WdttClientID) != "", strings.TrimSpace(t.FreeTurnClientID) != "":
		return "туннель принадлежит прокси-клиенту"
	}
	return ""
}

func (o *WarpTunnelOps) managedRef(ctx context.Context, t *storage.AWGTunnel) (warpscout.TunnelRef, bool) {
	mode, ok := modeOfBackend(t.Backend)
	if !ok {
		return warpscout.TunnelRef{}, false
	}
	ref := warpscout.TunnelRef{
		Mode: mode, ID: t.ID, Name: t.Name, Endpoint: t.Peer.Endpoint,
		PeerPublicKey: t.Peer.PublicKey,
		IsWarp:        strings.TrimSpace(t.Peer.PublicKey) == warpscout.WarpPeerPublicKey,
	}
	if reason := managedProtectReason(t); reason != "" {
		ref.Protected, ref.ProtectReason = true, reason
	}
	if o.Svc != nil {
		if tw, err := o.Svc.Get(ctx, t.ID); err == nil && tw != nil {
			ref.Iface = tw.InterfaceName
			// NativeWG без живого рукопожатия переходит в StateStarting / StateBroken,
			// хотя интерфейс административно включён — для автопилота это именно
			// повод проверить и подменить мёртвый endpoint, а не «туннель выключен».
			ref.Running = tw.State == tunnel.StateRunning ||
				(t.Enabled && (tw.State == tunnel.StateStarting || tw.State == tunnel.StateBroken))
		}
	}
	return ref, true
}

// systemProtectReason — почему системный интерфейс трогать нельзя.
func (o *WarpTunnelOps) systemProtectReason(t *ndms.SystemWireguardTunnel) string {
	if t.Description == ndms.BuiltInVPNServerDescription {
		return "встроенный VPN-сервер Keenetic"
	}
	if t.External != "" {
		return "интерфейс стороннего установщика (" + t.External + ")"
	}
	if strings.HasPrefix(t.Description, "AWGM WDTT") {
		return "служебный интерфейс WDTT"
	}
	if o.Settings != nil {
		if o.Settings.IsServerInterface(t.ID) {
			return "интерфейс отмечен как WG-сервер"
		}
		for _, ms := range o.Settings.GetManagedServers() {
			if ms.InterfaceName == t.ID {
				return "сервер AWGM (managedServers)"
			}
		}
	}
	for _, name := range managedNativeWGNames(o.Store) {
		if name == t.ID {
			return "туннель управляется AWGM — выберите тип native"
		}
	}
	return ""
}

func (o *WarpTunnelOps) systemRef(t *ndms.SystemWireguardTunnel) warpscout.TunnelRef {
	ref := warpscout.TunnelRef{
		Mode: warpscout.TargetSystem, ID: t.ID, Name: t.Description, Iface: t.InterfaceName,
		Running: t.Status == "up",
	}
	if ref.Name == "" {
		ref.Name = t.ID
	}
	if t.Peer != nil {
		ref.Endpoint = t.Peer.Endpoint
		ref.PeerPublicKey = t.Peer.PublicKey
		ref.IsWarp = strings.TrimSpace(t.Peer.PublicKey) == warpscout.WarpPeerPublicKey
	}
	if reason := o.systemProtectReason(t); reason != "" {
		ref.Protected, ref.ProtectReason = true, reason
	}
	return ref
}

// List — managed-туннели и системные WG-клиенты. Серверы и служебные
// интерфейсы в выбор не попадают вовсе.
func (o *WarpTunnelOps) List(ctx context.Context) ([]warpscout.TunnelRef, error) {
	out := []warpscout.TunnelRef{}
	if o.Store != nil {
		list, err := o.Store.List()
		if err != nil {
			return nil, err
		}
		for i := range list {
			if ref, ok := o.managedRef(ctx, &list[i]); ok {
				out = append(out, ref)
			}
		}
	}
	if o.System != nil {
		sys, err := o.System.List(ctx)
		if err == nil {
			for i := range sys {
				ref := o.systemRef(&sys[i])
				if ref.Protected {
					continue
				}
				out = append(out, ref)
			}
		}
	}
	return out, nil
}

// Get — один туннель.
func (o *WarpTunnelOps) Get(ctx context.Context, mode warpscout.TargetMode, id string) (*warpscout.TunnelRef, error) {
	switch mode {
	case warpscout.TargetKernel, warpscout.TargetNative:
		if o.Store == nil {
			return nil, errors.New("хранилище туннелей недоступно")
		}
		t, err := o.Store.Get(id)
		if err != nil {
			return nil, fmt.Errorf("туннель %s не найден", id)
		}
		ref, ok := o.managedRef(ctx, t)
		if !ok || ref.Mode != mode {
			return nil, fmt.Errorf("туннель %s не типа %s", id, mode)
		}
		return &ref, nil
	case warpscout.TargetSystem:
		if o.System == nil {
			return nil, errors.New("системные туннели недоступны")
		}
		if !isValidWireguardName(id) {
			return nil, fmt.Errorf("некорректное имя интерфейса %q", id)
		}
		t, err := o.System.Get(ctx, id)
		if err != nil || t == nil {
			return nil, fmt.Errorf("системный туннель %s не найден", id)
		}
		ref := o.systemRef(t)
		return &ref, nil
	}
	return nil, fmt.Errorf("неизвестный тип туннеля %q", mode)
}

// Create — новый туннель из .conf.
func (o *WarpTunnelOps) Create(ctx context.Context, mode warpscout.TargetMode, name, conf string) (*warpscout.TunnelRef, error) {
	switch mode {
	case warpscout.TargetKernel, warpscout.TargetNative:
		if o.Svc == nil {
			return nil, errors.New("сервис туннелей недоступен")
		}
		backend := "kernel"
		if mode == warpscout.TargetNative {
			backend = "nativewg"
		}
		tw, err := o.Svc.Import(ctx, conf, name, backend, service.ImportLink{})
		if err != nil {
			return nil, err
		}
		if err := o.Svc.Start(ctx, tw.ID); err != nil {
			// Туннель заведён — отказ старта не отменяет импорт.
			if o.Tunnels != nil {
				o.Tunnels.log.Warn("warpscout", name, "Туннель создан, но не запущен: "+err.Error())
			}
		}
		if o.Tunnels != nil {
			o.Tunnels.publishTunnelList(ctx)
		}
		return o.Get(ctx, mode, tw.ID)
	case warpscout.TargetSystem:
		if o.Commands == nil || o.Commands.Wireguard == nil {
			return nil, errors.New("NDMS недоступен")
		}
		res, err := o.Commands.Wireguard.ImportWireguardConfig(ctx, []byte(conf), sanitizeConfName(name)+".conf")
		if err != nil {
			return nil, err
		}
		if o.Commands.Interfaces != nil {
			if name != "" {
				_ = o.Commands.Interfaces.SetDescription(ctx, res.Created, name)
			}
			_ = o.Commands.Interfaces.InterfaceUp(ctx, res.Created)
		}
		// Даём NDMS поднять интерфейс, чтобы ответ уже нёс его состояние.
		time.Sleep(500 * time.Millisecond)
		return o.Get(ctx, mode, res.Created.Name())
	}
	return nil, fmt.Errorf("неизвестный тип туннеля %q", mode)
}

func sanitizeConfName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 128 && (r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "warp"
	}
	return b.String()
}

// SwapEndpoint — горячая подмена endpoint без пересоздания интерфейса.
func (o *WarpTunnelOps) SwapEndpoint(ctx context.Context, mode warpscout.TargetMode, id, endpoint string) error {
	ref, err := o.Get(ctx, mode, id)
	if err != nil {
		return err
	}
	if ref.Protected {
		return fmt.Errorf("%w: %s", warpscout.ErrProtected, ref.ProtectReason)
	}
	if !ref.IsWarp {
		return warpscout.ErrNotWarp
	}
	switch mode {
	case warpscout.TargetKernel, warpscout.TargetNative:
		return o.swapManaged(ctx, id, endpoint)
	case warpscout.TargetSystem:
		if o.Commands == nil || o.Commands.Wireguard == nil || o.Queries == nil || o.Queries.Interfaces == nil {
			return errors.New("NDMS недоступен")
		}
		iface, _, present, err := o.Queries.Interfaces.Confirm(ctx, id)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("интерфейс %s не найден в NDMS", id)
		}
		return o.Commands.Wireguard.SetPeerEndpoint(ctx, iface, ref.PeerPublicKey, endpoint)
	}
	return fmt.Errorf("неизвестный тип туннеля %q", mode)
}

// swapManaged — тот же путь, что у правки карточки (TunnelsHandler.Update):
// svc.Update применяет diff к живому интерфейсу (kernel: awg setconf +
// маршрут до нового адреса; nativewg: SyncPeer/SyncKmodSlot под замком
// оркестратора), затем запись обновляется транзакцией стора.
//
// Две тонкости живого переключения под ТСПУ:
//  1. `awg setconf` не трогает `listen-port`, если поле опущено в .conf, и
//     kernel-туннель продолжает слать рукопожатия со старого UDP-порта. Если
//     ТСПУ заблокировал поток по `(src_ip, sport)`, все новые endpoint'ы будут
//     молча терять пакеты, пока не сменится исходный порт (`listen-port 0`).
//  2. У туннеля с протухшим рукопожатием (`StateStarting` / `StateBroken`)
//     `svc.Update` пропускает runtime-синхронизацию (`shouldSyncRuntime`),
//     поэтому после сохранения нового endpoint'а такой включённый туннель
//     мягко перезапускается через `svc.Restart`.
func (o *WarpTunnelOps) swapManaged(ctx context.Context, id, endpoint string) error {
	if o.Svc == nil || o.Store == nil {
		return errors.New("сервис туннелей недоступен")
	}
	existing, err := o.Store.Get(id)
	if err != nil {
		return fmt.Errorf("туннель %s не найден", id)
	}
	tw, _ := o.Svc.Get(ctx, id)
	stalled := existing.Enabled && tw != nil &&
		(tw.State == tunnel.StateStarting || tw.State == tunnel.StateBroken)

	if existing.Peer.Endpoint != endpoint {
		merged := *existing
		merged.Peer.AllowedIPs = append([]string(nil), existing.Peer.AllowedIPs...)
		merged.Peer.Endpoint = endpoint
		if err := o.Svc.Update(ctx, existing, &merged); err != nil {
			return err
		}
		if err := o.Store.Update(id, func(t *storage.AWGTunnel) error {
			t.Peer.Endpoint = endpoint
			if merged.ResolvedEndpointIP != "" {
				t.ResolvedEndpointIP = merged.ResolvedEndpointIP
			}
			if merged.ActiveWAN != "" {
				t.ActiveWAN = merged.ActiveWAN
			}
			return nil
		}); err != nil {
			return err
		}
	}

	if stalled {
		if err := o.Svc.Restart(ctx, id); err != nil {
			return fmt.Errorf("перезапуск зависшего туннеля: %w", err)
		}
	} else if existing.Backend != "nativewg" && tw != nil && tw.State == tunnel.StateRunning {
		iface := tw.InterfaceName
		if iface == "" {
			iface = tunnel.NewNames(id).IfaceName
		}
		o.rotateKernelPort(ctx, iface)
	}

	if o.Orch != nil {
		o.Orch.RefreshTunnelState(id)
	}
	if o.Tunnels != nil {
		o.Tunnels.publishTunnelList(ctx)
	}
	return nil
}

func (o *WarpTunnelOps) rotateKernelPort(ctx context.Context, iface string) {
	if iface == "" {
		return
	}
	fn := o.RotateKernelPort
	if fn == nil {
		fn = defaultRotateKernelPort
	}
	if err := fn(ctx, iface); err != nil && o.Tunnels != nil {
		o.Tunnels.log.Warn("warpscout", iface, "Не удалось сменить UDP-порт интерфейса: "+err.Error())
	}
}

func defaultRotateKernelPort(ctx context.Context, iface string) error {
	res, err := exec.Run(ctx, "/opt/sbin/awg", "set", iface, "listen-port", "0")
	if err != nil {
		return exec.FormatError(res, err)
	}
	return nil
}

// WarpHTTPClientFactory — клиент Cloudflare API с прямым выходом через WAN.
// Короткий таймаут рукопожатия позволяет быстро перейти на резервный выход
// (прокси/туннель), если провайдер блокирует домен регистрации на сыром WAN.
func WarpHTTPClientFactory(wanIface string) (*http.Client, error) {
	tr, err := httpclient.NewTransport(httpclient.TransportConfig{Interface: wanIface, Proxy: httpclient.ProxyDirect})
	if err != nil {
		return nil, err
	}
	tr.TLSHandshakeTimeout = 6 * time.Second
	tr.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{Transport: tr, Timeout: 12 * time.Second}, nil
}

// WarpFallbackClients собирает резервные HTTP-клиенты для регистрации WARP и
// привязки WARP+ на случай блокировки API Cloudflare на прямом WAN:
//  1. настроенный маршрут загрузок (если выбран не direct);
//  2. локальный «Прокси для устройств» (deviceproxy, включая авторизацию инстанса);
//  3. до двух запущенных незащищённых не-WARP VPN-туннелей.
func WarpFallbackClients(ctx context.Context, dp *deviceproxy.Service, ops *WarpTunnelOps, dl *downloader.Service) []*http.Client {
	var out []*http.Client

	if dl != nil {
		if lease, err := dl.ResolveClient(ctx, nil); err == nil && lease != nil && lease.Client != nil {
			if tag := strings.TrimSpace(lease.Route.Tag); tag != "" && tag != "direct" {
				out = append(out, lease.Client)
			} else {
				lease.Close()
			}
		}
	}

	if dp != nil {
		for _, inst := range dp.GetSnapshot().Instances {
			if !inst.Enabled || inst.Port <= 0 || inst.Port > 65535 || strings.EqualFold(strings.TrimSpace(inst.SelectedOutbound), "direct") {
				continue
			}
			addr := findListeningLocalProxy(inst.Port)
			if addr == "" {
				continue
			}
			u := &url.URL{Scheme: "http", Host: addr}
			if inst.Auth.Enabled && inst.Auth.Username != "" {
				u.User = url.UserPassword(inst.Auth.Username, inst.Auth.Password)
			}
			if tr, err := httpclient.NewTransport(httpclient.TransportConfig{ProxyURL: u.String()}); err == nil {
				tr.TLSHandshakeTimeout = 8 * time.Second
				tr.ResponseHeaderTimeout = 12 * time.Second
				out = append(out, &http.Client{Transport: tr, Timeout: 15 * time.Second})
				break
			}
		}
	}

	if ops != nil {
		if list, err := ops.List(ctx); err == nil {
			added := 0
			for _, ref := range list {
				if !ref.Running || ref.Protected || ref.IsWarp || ref.Iface == "" {
					continue
				}
				tr, err := httpclient.NewTransport(httpclient.TransportConfig{Interface: ref.Iface, Proxy: httpclient.ProxyDirect})
				if err != nil {
					continue
				}
				tr.TLSHandshakeTimeout = 6 * time.Second
				tr.ResponseHeaderTimeout = 10 * time.Second
				out = append(out, &http.Client{Transport: tr, Timeout: 12 * time.Second})
				added++
				if added >= 2 {
					break
				}
			}
		}
	}

	return out
}

func findListeningLocalProxy(port int) string {
	p := strconv.Itoa(port)
	candidates := []string{"127.0.0.1:" + p}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP == nil || ipNet.IP.IsLoopback() {
				continue
			}
			if ip4 := ipNet.IP.To4(); ip4 != nil && ip4.IsPrivate() {
				candidates = append(candidates, net.JoinHostPort(ip4.String(), p))
			}
		}
	}
	for _, addr := range candidates {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return addr
		}
	}
	return ""
}

// ── HTTP-ручки ──────────────────────────────────────────────────

// WarpScoutHandler — REST модуля «WARP Сканер» (раздел «Инструменты»).
// Доступен на всех уровнях интерфейса.
type WarpScoutHandler struct {
	svc *warpscout.Service
	log *logging.ScopedLogger
}

// NewWarpScoutHandler создаёт хендлер.
func NewWarpScoutHandler(svc *warpscout.Service, appLog logging.AppLogger) *WarpScoutHandler {
	return &WarpScoutHandler{svc: svc, log: logging.NewScopedLogger(appLog, logging.GroupTunnel, logging.SubWarpScout)}
}

func (h *WarpScoutHandler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, warpscout.ErrNoAccount):
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "WARP_NO_ACCOUNT")
	case errors.Is(err, warpscout.ErrScanRunning), errors.Is(err, warpscout.ErrEngineBusy):
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "WARP_BUSY")
	case errors.Is(err, warpscout.ErrEngineMissing):
		// 409, а не 503: фронт на 503 оставляет только текст и теряет код.
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "WARP_ENGINE_MISSING")
	case errors.Is(err, warpscout.ErrProtected):
		response.ErrorWithStatus(w, http.StatusForbidden, err.Error(), "WARP_PROTECTED")
	case errors.Is(err, warpscout.ErrNotWarp):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "WARP_NOT_WARP")
	case errors.Is(err, warpscout.ErrInvalidInput):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_INPUT")
	case errors.Is(err, tunnel.ErrOperationInProgress):
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "OPERATION_IN_PROGRESS")
	default:
		response.Error(w, err.Error(), "WARP_FAILED")
	}
}

// State — снимок модуля.
//
//	@Summary	WARP scanner state
//	@Tags		warpscout
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/state [get]
func (h *WarpScoutHandler) State(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, h.svc.State())
}

// Targets — туннели для выбора цели автопилота.
//
//	@Summary	WARP autopilot targets
//	@Tags		warpscout
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/targets [get]
func (h *WarpScoutHandler) Targets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	list, err := h.svc.Targets(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, list)
}

// Register — регистрация учётной записи WARP.
//
//	@Summary	Register Cloudflare WARP account
//	@Tags		warpscout
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/account/register [post]
func (h *WarpScoutHandler) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	acct, err := h.svc.Register(r.Context())
	if err != nil {
		h.log.Warn("register", "warp", err.Error())
		h.fail(w, err)
		return
	}
	response.Success(w, acct)
}

type warpLicenseRequest struct {
	License string `json:"license"`
}

// License — привязка ключа WARP+.
//
//	@Summary	Apply WARP+ license
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/account/license [post]
func (h *WarpScoutHandler) License(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpLicenseRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	acct, err := h.svc.SetLicense(r.Context(), req.License)
	if err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, acct)
}

// DeleteAccount — забыть учётную запись.
//
//	@Summary	Delete WARP account
//	@Tags		warpscout
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/account/delete [post]
func (h *WarpScoutHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.DeleteAccountLocal(r.Context()); err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, map[string]bool{"ok": true})
}

// ScanStart — запуск ручного скана.
//
//	@Summary	Start WARP endpoint scan
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/scan/start [post]
func (h *WarpScoutHandler) ScanStart(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpscout.ScanRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.StartScan(req); err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, h.svc.State().Scan)
}

// ScanStop — остановка ручного скана.
//
//	@Summary	Stop WARP endpoint scan
//	@Tags		warpscout
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/scan/stop [post]
func (h *WarpScoutHandler) ScanStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	h.svc.StopScan()
	response.Success(w, map[string]bool{"ok": true})
}

// Apply — создать туннель из узла или подменить endpoint.
//
//	@Summary	Apply WARP endpoint
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/apply [post]
func (h *WarpScoutHandler) Apply(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpscout.ApplyRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	res, err := h.svc.Apply(r.Context(), req)
	if err != nil {
		h.log.Warn("apply", req.Endpoint, err.Error())
		h.fail(w, err)
		return
	}
	response.Success(w, res)
}

// Config — сохранить профили автопилота и параметры AWG.
//
//	@Summary	Save WARP autopilot config
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/config [post]
func (h *WarpScoutHandler) Config(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpscout.Config](w, r, http.MethodPost)
	if !ok {
		return
	}
	cfg, err := h.svc.SaveConfig(r.Context(), req)
	if err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, cfg)
}

type warpResetStatsRequest struct {
	ProfileID string `json:"profileId"`
}

// ResetStats — сбросить накопленную статистику профиля автопилота (или всех профилей).
//
//	@Summary	Reset WARP autopilot stats
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/stats/reset [post]
func (h *WarpScoutHandler) ResetStats(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpResetStatsRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.ResetStats(req.ProfileID); err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, map[string]bool{"ok": true})
}
