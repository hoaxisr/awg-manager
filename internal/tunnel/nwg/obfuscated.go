package nwg

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/payloads"
	"github.com/hoaxisr/awg-manager/internal/obfuscator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// obfRouteDetailsPrefix — начало StateInfo.Details, когда релей жив, а host-route
// до target поставить не удалось: без него трафик релея уходит в сам туннель.
const obfRouteDetailsPrefix = "маршрут до сервера не поставлен: "

// ObfuscatorRunner — процесс wg-obfuscator на туннель (internal/obfuscator.Runner).
type ObfuscatorRunner interface {
	Start(ctx context.Context, tunnelID string, o *storage.Obfuscator) error
	Stop(tunnelID string) error
	Alive(tunnelID string) bool
}

func (o *OperatorNativeWG) SetObfuscator(r ObfuscatorRunner) { o.obf = r }

// SetObfuscatorRouteSharing подключает проверку «host-route до этого IP держит
// другой туннель». Без неё Stop одного из двух туннелей с общим target-IP
// снимал бы маршрут, нужный второму.
func (o *OperatorNativeWG) SetObfuscatorRouteSharing(fn func(excludeID, ip string) bool) {
	o.obfRouteHeldByOther = fn
}

// removeObfHostRoute снимает host-route, если он не нужен другому туннелю.
func (o *OperatorNativeWG) removeObfHostRoute(ctx context.Context, tunnelID, ip string) error {
	if o.obfRouteHeldByOther != nil && o.obfRouteHeldByOther(tunnelID, ip) {
		o.appLog.Info("obfuscator", tunnelID, "host-route "+ip+" нужен другому туннелю, оставляем")
		return nil
	}
	return o.commands.Routes.RemoveHostRoute(ctx, ip)
}

// startObfuscated — путь Start для туннеля через релей:
//  1. процесс релея на 127.0.0.1:LocalPort (Runner.Start идемпотентен);
//  2. резолв target (retry + кэш ResolvedEndpointIP);
//  3. NDMS: peer endpoint = loopback, connect via, interface up — но только
//     если интерфейс ещё НЕ поднят на этот самый релей: Start прилетает на
//     каждый WAN-up и на рестарт демона, а батч по живому интерфейсу — churn;
//  4. host-route target/32 через WAN по RCI — трафик релея не должен уйти в
//     сам туннель.
//
// Резолв идёт ДО RCI-команд: на бутe DNS может быть ещё не готов, и отказ не
// должен оставлять поднятый интерфейс без релея. Маршрут ставится ПОСЛЕ батча:
// до него peer.via в RCI показывает прежний WAN, и на failover host-route ушёл
// бы через уже мёртвый канал. При отказе батча маршрута ещё нет — откат
// сводится к остановке релея. Ни ASC, ни kmod-слота у такого туннеля нет:
// WireGuard обычный.
func (o *OperatorNativeWG) startObfuscated(ctx context.Context, stored *storage.AWGTunnel) error {
	if o.obf == nil {
		return fmt.Errorf("обфускатор не подключён")
	}
	names := NewNWGNames(stored.NWGIndex)
	// Реестр endpoint-стража доводим до определённого состояния, как и
	// соседние start-пути: оставшаяся запись переписала бы loopback-endpoint
	// реальным адресом сервера.
	o.guardUnregister(stored.ID)
	if err := o.obf.Start(ctx, stored.ID, stored.Obfuscator); err != nil {
		return err
	}
	// Адрес прежнего маршрута берём ДО резолва: успешный резолв кладёт новый
	// IP в trackedIP, и разницу уже было бы не увидеть.
	prevIP := o.obfRouteIP(stored)
	targetIP, err := o.resolveTarget(stored)
	if err != nil {
		_ = o.obf.Stop(stored.ID)
		return err
	}
	loopback := "127.0.0.1:" + strconv.Itoa(stored.Obfuscator.LocalPort)
	st, ok := o.readObfIfaceState(ctx, names)
	// PeerOnline обязателен: залипший интерфейс (conf=running, пир offline —
	// KN-1910) должен получать батч, иначе он останется мёртвым навсегда.
	alreadyUp := ok && st.Exists && st.ConfLayer == "running" && st.PeerOnline &&
		st.PeerRemoteAddr == "127.0.0.1" && st.PeerRemotePort == stored.Obfuscator.LocalPort
	if alreadyUp {
		o.appLog.Info("start", names.NDMSName, "интерфейс уже поднят на "+loopback+", батч пропущен")
	} else {
		if err := o.SyncAddressMTU(ctx, stored); err != nil {
			o.appLog.Warn("sync-address-mtu", names.NDMSName, "on start: "+err.Error())
		}
		if err := o.SyncDNS(ctx, stored, nil, tunnel.ParseDNSList(stored.Interface.DNS)); err != nil {
			o.appLog.Warn("apply-dns", names.NDMSName, err.Error())
		}
		if o.hookNotifier != nil {
			o.hookNotifier.ExpectHook(names.NDMSName, "running")
		}
		cmds := []any{
			payloads.CmdWireguardPeerEndpoint(names.NDMSName, stored.Peer.PublicKey, loopback),
			payloads.CmdWireguardPeerConnect(names.NDMSName, stored.Peer.PublicKey, stored.ISPInterface),
			payloads.CmdInterfaceUp(names.NDMSName, true),
		}
		if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
			_ = o.obf.Stop(stored.ID)
			return fmt.Errorf("start obfuscated: %w", err)
		}
	}
	o.moveObfHostRoute(ctx, stored, prevIP, targetIP)
	o.appLog.Info("start", names.NDMSName, fmt.Sprintf("obfuscator %s %s -> %s (%s)",
		stored.Obfuscator.Flavor, loopback, stored.Obfuscator.Target, targetIP))
	return nil
}

// readObfIfaceState — снимок интерфейса по RCI. false = прочитать не удалось
// (транспорт или разбор), и решение принимается как при отсутствии данных.
func (o *OperatorNativeWG) readObfIfaceState(ctx context.Context, names NWGNames) (NWGState, bool) {
	body, err := o.fetchInterfaceRCI(ctx, names.NDMSName)
	if err != nil {
		return NWGState{}, false
	}
	st, err := parseRCIInterfaceResponse(body)
	if err != nil {
		return NWGState{}, false
	}
	return st, true
}

// stopObfuscated гасит релей и снимает host-route до target.
func (o *OperatorNativeWG) stopObfuscated(ctx context.Context, stored *storage.AWGTunnel) {
	if o.obf != nil {
		_ = o.obf.Stop(stored.ID)
	}
	o.clearObfRouteErr(stored.ID)
	if ip := o.obfRouteIP(stored); ip != "" {
		if err := o.removeObfHostRoute(ctx, stored.ID, ip); err != nil {
			o.appLog.Warn("stop", stored.ID, "снять host-route "+ip+": "+err.Error())
		}
	}
}

// SyncObfuscator — правка параметров у работающего (или упавшего) релея:
// перезапуск только релея; при смене target — переставить host-route.
// Возвращает IP, под который стоит маршрут: сервис кладёт его в ResolvedEndpointIP,
// иначе после рестарта демона снимать было бы нечего.
func (o *OperatorNativeWG) SyncObfuscator(ctx context.Context, stored *storage.AWGTunnel) (string, error) {
	if o.obf == nil || stored.Obfuscator == nil {
		return "", nil
	}
	prevIP := o.obfRouteIP(stored)
	targetIP, err := o.resolveTarget(stored)
	if err != nil {
		return "", err
	}
	o.moveObfHostRoute(ctx, stored, prevIP, targetIP)
	return targetIP, o.obf.Start(ctx, stored.ID, stored.Obfuscator)
}

// moveObfHostRoute переставляет host-route с прежнего адреса target'а на новый.
// Отказ маршрута Start не валит (на WAN-up туннель всё равно перезапустится),
// но остаётся в реестре причин и доезжает до пользователя через Details.
func (o *OperatorNativeWG) moveObfHostRoute(ctx context.Context, stored *storage.AWGTunnel, prevIP, targetIP string) {
	if prevIP != "" && prevIP != targetIP {
		if err := o.removeObfHostRoute(ctx, stored.ID, prevIP); err != nil {
			o.appLog.Warn("obfuscator", stored.ID, "снять прежний host-route "+prevIP+": "+err.Error())
		}
	}
	if err := o.addObfHostRoute(ctx, stored, targetIP); err != nil {
		o.appLog.Warn("obfuscator", stored.ID, "host-route до "+targetIP+": "+err.Error())
		o.setObfRouteErr(stored.ID, err.Error())
		return
	}
	o.clearObfRouteErr(stored.ID)
}

func (o *OperatorNativeWG) setObfRouteErr(tunnelID, msg string) {
	o.obfRouteMu.Lock()
	defer o.obfRouteMu.Unlock()
	if o.obfRouteErr == nil {
		o.obfRouteErr = make(map[string]string)
	}
	o.obfRouteErr[tunnelID] = msg
}

func (o *OperatorNativeWG) clearObfRouteErr(tunnelID string) {
	o.obfRouteMu.Lock()
	defer o.obfRouteMu.Unlock()
	delete(o.obfRouteErr, tunnelID)
}

func (o *OperatorNativeWG) obfRouteErrFor(tunnelID string) string {
	o.obfRouteMu.Lock()
	defer o.obfRouteMu.Unlock()
	return o.obfRouteErr[tunnelID]
}

// overlayObfuscatorState: WG-интерфейс стоит, релея нет → Broken + причина.
// Релей жив, но host-route до target не встал → состояние прежнее, причина в Details.
func (o *OperatorNativeWG) overlayObfuscatorState(stored *storage.AWGTunnel, info *tunnel.StateInfo) {
	if stored.Obfuscator == nil || o.obf == nil {
		return
	}
	switch info.State {
	case tunnel.StateRunning, tunnel.StateStarting, tunnel.StateBroken:
	default:
		return // Stopped/NotCreated — релей к состоянию отношения не имеет
	}
	if !o.obf.Alive(stored.ID) {
		info.State = tunnel.StateBroken
		info.Details = obfuscator.DetailsNotRunning
		return
	}
	// Релей жив, но host-route до сервера не встал — трафик релея уходит в
	// сам туннель. Состояние не меняем (RCI знает лучше), причину показываем.
	if info.State != tunnel.StateBroken {
		if msg := o.obfRouteErrFor(stored.ID); msg != "" {
			info.Details = obfRouteDetailsPrefix + msg
		}
	}
}

// obfSlotPredicate — для classifyNWGState на прошивке без ASC: «на 127.0.0.1:port
// кто-то наш слушает» — kmod-слот (обычный путь) или живой релей этого туннеля.
func (o *OperatorNativeWG) obfSlotPredicate(stored *storage.AWGTunnel) func(port int) bool {
	return func(port int) bool {
		if o.hasProxySlot != nil && o.hasProxySlot(port) {
			return true
		}
		return stored.Obfuscator != nil && o.obf != nil &&
			stored.Obfuscator.LocalPort == port && o.obf.Alive(stored.ID)
	}
}

// resolveTarget — тот же retry + кэш ResolvedEndpointIP, что у обычного
// endpoint'а (resolveEndpointWithFallback): подсовываем target вместо Peer.Endpoint.
// Резолв одноразовый: DDNS у target — зафиксированная потеря первой версии.
func (o *OperatorNativeWG) resolveTarget(stored *storage.AWGTunnel) (string, error) {
	probe := *stored
	probe.Peer.Endpoint = stored.Obfuscator.Target
	ip, _, err := o.resolveEndpointWithFallback(&probe)
	if err != nil {
		return "", fmt.Errorf("resolve target %s: %w", stored.Obfuscator.Target, err)
	}
	return ip, nil
}

// addObfHostRoute: host-форма ip route — {host, interface, auto, comment} — запись
// NDMS, переживает пересчёт таблицы. WAN: ISPInterface туннеля → peer.via из RCI (WAN,
// которым NDMS реально ведёт пира) → текущий дефолтный шлюз. Отказ только если
// это наш собственный WireguardN (дефолт через себя = петля).
func (o *OperatorNativeWG) addObfHostRoute(ctx context.Context, stored *storage.AWGTunnel, ip string) error {
	names := NewNWGNames(stored.NWGIndex)
	wan := strings.TrimSpace(stored.ISPInterface)
	if wan == "" {
		// Свежий запрос: peer.via читается уже после батча, поэтому на
		// failover сюда приходит новый WAN, а не тот, что был до подъёма.
		if st, ok := o.readObfIfaceState(ctx, names); ok && st.Exists {
			wan = st.PeerVia
		}
	}
	if wan == "" {
		var err error
		if wan, err = o.queries.Routes.GetDefaultGatewayInterface(ctx); err != nil {
			return fmt.Errorf("default WAN: %w", err)
		}
	}
	if wan == names.NDMSName {
		return fmt.Errorf("дефолтный маршрут уже через %s (сам туннель), host-route не ставится", wan)
	}
	return o.commands.Routes.AddStaticRoute(ctx, command.StaticRouteSpec{
		Host: ip, Interface: wan, Comment: "awgm-obfuscator " + stored.ID,
	})
}

// obfRouteIP — адрес, под которым стоит host-route: свежий резолв этого запуска,
// иначе сохранённый в записи.
//
// Loopback отсеивается: Create резолвит Peer.Endpoint, а у обфусцированного
// туннеля это 127.0.0.1:<port>, и трекер приносит сюда 127.0.0.1. Маршрута под
// таким адресом никогда не было — снимать его значит слать в NDMS лишний
// no-route на собственный loopback.
func (o *OperatorNativeWG) obfRouteIP(stored *storage.AWGTunnel) string {
	for _, candidate := range []string{o.GetTrackedEndpointIP(stored.ID), stored.ResolvedEndpointIP} {
		if ip := net.ParseIP(candidate); ip != nil && !ip.IsLoopback() {
			return candidate
		}
	}
	return ""
}
