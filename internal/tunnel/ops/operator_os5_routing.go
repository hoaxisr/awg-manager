package ops

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
)

// endpointWithResolvedIP substitutes a pre-resolved IP into the endpoint string.
// This avoids DNS re-resolution in SetupEndpointRoute, which can fail
// right after tunnel start when awg show has no endpoint yet and
// Go's pure-Go resolver can't resolve the domain on the router.
func endpointWithResolvedIP(endpoint, resolvedIP string) string {
	if resolvedIP == "" {
		return endpoint
	}
	_, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}
	return net.JoinHostPort(resolvedIP, port)
}

// getEndpointIPFromWG gets resolved endpoint IP from awg show.
// WireGuard resolves DNS when establishing connection, so we can get
// the already-resolved IP instead of doing another DNS lookup.
// Falls back to DNS resolve if awg show fails.
func (o *OperatorOS5Impl) getEndpointIPFromWG(ctx context.Context, tunnelID, fallbackEndpoint string) (string, error) {
	names := tunnel.NewNames(tunnelID)

	// Try to get from awg show (already resolved by WireGuard)
	if result, err := o.wg.Show(ctx, names.IfaceName); err == nil && result.Endpoint != "" {
		// Endpoint format is "IP:Port", extract just the IP
		host, _, splitErr := net.SplitHostPort(result.Endpoint)
		if splitErr == nil && host != "" {
			o.logInfo("resolve", tunnelID, "Got endpoint IP from awg show: "+host)
			return host, nil
		}
	}

	// Fallback to DNS resolve
	o.logInfo("resolve", tunnelID, "Falling back to DNS resolve for endpoint")
	return netutil.ResolveEndpointIP(fallbackEndpoint)
}

// SetupEndpointRoute adds a route to the VPN endpoint via kernel device.
// kernelDevice is the kernel interface name (e.g., "eth3") for oif constraint;
// empty string means no constraint (ip route get picks the best route).
// Returns the resolved endpoint IP on success, error on failure.
//
// Фатальность отказа решает вызывающий. Прежняя строка обещала «always fatal
// — prevents routing loops», и это неправда: ColdStart и Reconcile ставят Warn
// и продолжают (признак endpointRouteOK доживает до предупреждения в журнале),
// refreshEndpointRouteAfterResume тоже, а вот applyDiffKernel копит ошибку в
// errs, и handler на ней запись не сохраняет (fail-closed). Расхождение опасно не
// поведением, а тем, что следующий инженер поверит комментарию и сделает
// отказ фатальным: на одноканальном роутере этот маршрут для внешнего
// трафика избыточен — замерено на стенде 5.01, удаление маршрута путь
// трафика не изменило, потому что у WAN своя таблица со своим дефолтом.
// Значимость маршрута — мульти-WAN с привязкой к не-предпочтительному
// каналу и цепочки туннелей.
//
// Вызов идемпотентен: под ним `ip route replace`, поэтому повторять его на
// старте, реконнекте и при смене WAN безопасно.
func (o *OperatorOS5Impl) SetupEndpointRoute(ctx context.Context, tunnelID, endpoint, kernelDevice, ispName string) (string, error) {
	if endpoint == "" {
		return "", nil
	}

	// Get endpoint IP (prefer awg show, fallback to DNS resolve)
	endpointIP, err := o.getEndpointIPFromWG(ctx, tunnelID, endpoint)
	if err != nil {
		o.logWarn("setup_route", tunnelID, "Failed to resolve endpoint: "+err.Error())
		return "", fmt.Errorf("resolve endpoint: %w", err)
	}

	// Маршрут до петли не ставим. Связанные туннели wdtt/freeturn в WG-режиме
	// несут endpoint 127.0.0.1:<порт> — релей слушает на самом роутере. Роутер
	// такую команду ПРИНИМАЕТ (проверено на 5.01): в ядре оседает
	// `127.0.0.1 dev ppp0 scope link`, в конфиге NDMS — host-route
	// `127.0.0.1/32`. Трафик не страдает (таблица local выигрывает у main), но
	// ядро и конфигурация роутера засоряются.
	//
	// Мало не поставить — надо ещё и снять наследство: на роутере, поработавшем
	// под прежней версией, мусорный маршрут уже лежит, и NDMS переигрывает его
	// в ядро на каждой загрузке. Старт связанного туннеля после апгрейда —
	// первый момент, когда мы вообще узнаём про этот адрес, поэтому уборка
	// здесь, а не в отдельном проходе (F227). Снятие идёт общим путём, так что
	// сосед к тому же адресу своего маршрута не теряет.
	//
	// Возврат пустой: маршрута нет, и персистить вызывающему нечего. Петлю в
	// ResolvedEndpointIP это НЕ закрывает — она приезжает туда другим путём:
	// RestoreEndpointTracking кладёт её в карту на рестарте демона, а
	// оркестратор сохраняет GetTrackedEndpointIP в запись (F230).
	if skipEndpointHostRoute(endpointIP) {
		o.logInfo("setup_route", tunnelID, "endpoint не маршрутизируется ("+endpointIP+") — хост-маршрут не нужен")
		o.removeHostRouteIfUnused(ctx, "setup_route", tunnelID, endpointIP)
		return "", nil
	}

	// Resolve route target from kernel routing table.
	// oif constraint ensures we route via the intended WAN device.
	gateway, device, err := o.resolveKernelRouteTarget(ctx, endpointIP, kernelDevice)
	if err != nil {
		o.logWarn("setup_route", tunnelID, "Failed to resolve kernel route: "+err.Error())
		return "", fmt.Errorf("resolve kernel route for %s: %w", endpointIP, err)
	}

	// Build route target: prefer gateway IP, fallback to device (PPPoE/point-to-point).
	routeTarget := gateway
	routeDevice := "" // explicit dev for link-local gateways
	if routeTarget == "" {
		routeTarget = device
	} else if isIPv6LinkLocal(routeTarget) {
		// IPv6 link-local gateway requires explicit device
		routeDevice = device
	}

	if err := o.addKernelHostRoute(ctx, endpointIP, routeTarget, routeDevice); err != nil {
		o.logWarn("setup_route", tunnelID, "Failed to add kernel endpoint route: "+err.Error())
		o.appLog.Warn("start", tunnelID, "Маршрут до endpoint "+endpointIP+": "+err.Error())
		return "", fmt.Errorf("add kernel host route to %s: %w", endpointIP, err)
	}

	// Track for cleanup
	o.endpointRoutesMu.Lock()
	o.endpointRoutes[tunnelID] = endpointIP
	o.endpointRoutesMu.Unlock()

	logSuffix := ""
	if ispName != "" {
		logSuffix = " (" + ispName + ")"
	}
	o.logInfo("setup_route", tunnelID, "Added endpoint route to "+endpointIP+" via "+routeTarget+logSuffix)
	o.appLog.Info("start", tunnelID, "Маршрут до endpoint "+endpointIP+" через "+routeTarget+logSuffix)
	return endpointIP, nil
}

// CleanupEndpointRoute removes the endpoint route for a tunnel.
func (o *OperatorOS5Impl) CleanupEndpointRoute(ctx context.Context, tunnelID string) error {
	// Петлю снимаем тоже — см. гард в SetupEndpointRoute: он только на создании.
	o.removeHostRouteIfUnused(ctx, "cleanup_route", tunnelID, "")
	return nil
}

// removeHostRouteIfUnused забывает маршрут туннеля и снимает его из ядра и из
// конфига NDMS — но только если тот же адрес не держит другой туннель: три
// туннеля к одному серверу делят один host-route (F130/#867).
//
// Адрес берётся из карты, fallbackIP — запасной для случая, когда карты нет
// (удаление туннеля, который с рестарта демона не поднимался). Карта читается,
// правится и пересчитывается под ОДНИМ захватом: раздельные чтение и правка
// оставляли окно, в котором параллельный SetupEndpointRoute успевал положить
// туннелю новый адрес — тогда из карты уходила свежая запись, а из ядра
// снимался прежний маршрут.
//
// action уезжает в журнал: снятие бывает на трёх путях (правка карточки,
// откат неудачного Reconcile, удаление туннеля), и запись должна говорить,
// который из них сработал.
func (o *OperatorOS5Impl) removeHostRouteIfUnused(ctx context.Context, action, tunnelID, fallbackIP string) {
	o.endpointRoutesMu.Lock()
	endpointIP := o.endpointRoutes[tunnelID]
	if endpointIP == "" {
		endpointIP = fallbackIP
	}
	delete(o.endpointRoutes, tunnelID)
	stillInUse := false
	for _, ip := range o.endpointRoutes {
		if ip == endpointIP {
			stillInUse = true
			break
		}
	}
	o.endpointRoutesMu.Unlock()

	if endpointIP == "" {
		return
	}
	if stillInUse {
		o.logInfo(action, tunnelID, "IP "+endpointIP+" still in use by another tunnel")
		return
	}

	// NDMS кэширует маршруты ядра, но их снятие не отслеживает — снимаем в обоих.
	if err := o.delKernelHostRoute(ctx, endpointIP); err != nil {
		o.logWarn(action, tunnelID, "ip route del "+endpointIP+": "+err.Error())
	}
	if err := o.commands.Routes.RemoveHostRoute(ctx, endpointIP); err != nil {
		o.logWarn(action, tunnelID, "RemoveHostRoute: "+err.Error())
	}
	o.appLog.Info(action, tunnelID, "Маршрут до endpoint "+endpointIP+" удалён")
}

// RestoreEndpointTracking restores endpoint route tracking without creating the route.
// Used on daemon restart for tunnels that are already running.
// Returns the resolved endpoint IP on success, empty string on non-fatal failure.
//
// ВНИМАНИЕ: наличие маршрута в ядре здесь НЕ проверяется — карта заполняется
// на веру. Обычно вера оправдана (маршрут пережил перезапуск демона вместе с
// туннелем), но если таблицы успел перезаписать ndm, мы считаем маршрут
// живым, а его нет. Цена ошибки мала: карта нужна снятию маршрута, а снятие
// отсутствующего безвредно. Создать маршрут отсюда нечем — kernelDevice
// вызывающему неизвестен.
func (o *OperatorOS5Impl) RestoreEndpointTracking(ctx context.Context, tunnelID, endpoint string) (string, error) {
	if endpoint == "" {
		return "", nil
	}

	// Get endpoint IP (prefer awg show, fallback to DNS resolve)
	endpointIP, err := o.getEndpointIPFromWG(ctx, tunnelID, endpoint)
	if err != nil {
		o.logWarn("restore_tracking", tunnelID, "Failed to resolve endpoint: "+err.Error())
		return "", nil // Non-fatal
	}

	// Add to tracking map (route already exists in system)
	o.endpointRoutesMu.Lock()
	o.endpointRoutes[tunnelID] = endpointIP
	o.endpointRoutesMu.Unlock()

	o.logInfo("restore_tracking", tunnelID, "Restored endpoint tracking for "+endpointIP)
	return endpointIP, nil
}

// GetTrackedEndpointIP returns the currently tracked endpoint IP for a tunnel.
func (o *OperatorOS5Impl) GetTrackedEndpointIP(tunnelID string) string {
	o.endpointRoutesMu.RLock()
	defer o.endpointRoutesMu.RUnlock()
	return o.endpointRoutes[tunnelID]
}

// === Kernel route helpers (bypass NDMS) ===

// skipEndpointHostRoute сообщает, что хост-маршрут через WAN до такого адреса
// ставить незачем: петля, link-local, «неуказанный».
//
// Имя про решение, а не про свойство адреса: 127.0.0.1 и fe80:: как раз
// маршрутизируются — первый через lo, второй on-link, — просто не через WAN.
//
// Набор совпадает с SSRF-гардом `internal/proxyapp/wdttlink`, но совпадение
// случайное: там режут внутренние адреса, здесь — бессмысленные для маршрута.
// Тот же случай у nativewg решён иначе: `nwg.obfRouteIP`
// (`internal/tunnel/nwg/obfuscated.go`) отбрасывает петлю при ВЫБОРЕ адреса
// для снятия, потому что там маршрута никогда и не было. Чиня третий такой
// случай, загляните в оба.
func skipEndpointHostRoute(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		// Неразбираемый адрес пропускаем дальше: пусть отказывает команда ip,
		// а не молчаливый предикат — так причина видна в журнале.
		return false
	}
	return parsed.IsLoopback() || parsed.IsLinkLocalUnicast() ||
		parsed.IsLinkLocalMulticast() || parsed.IsUnspecified()
}

// isIPv6 returns true if the given IP string is an IPv6 address.
func isIPv6(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.To4() == nil
}

// addKernelHostRoute adds a host route via ip route command.
// routeTarget is either a gateway IP or a kernel interface name (for tunnels/PPPoE).
// device is optional; required when routeTarget is an IPv6 link-local address (fe80::).
func (o *OperatorOS5Impl) addKernelHostRoute(ctx context.Context, endpointIP, routeTarget, device string) error {
	prefix := "/32"
	ipCmd := "/opt/sbin/ip"
	family := []string{}
	if isIPv6(endpointIP) {
		prefix = "/128"
		family = []string{"-6"}
	}

	// `replace` is atomic: tunnels sharing the endpoint keep the route while
	// a neighbour (re)starts — del+add left a window where their packets
	// followed the default route (F130, #867).
	var args []string
	if net.ParseIP(routeTarget) != nil {
		// Gateway is an IP — route via gateway
		args = append([]string{}, family...)
		args = append(args, "route", "replace", endpointIP+prefix, "via", routeTarget)
		// IPv6 link-local gateways (fe80::) require explicit device
		if device != "" && isIPv6LinkLocal(routeTarget) {
			args = append(args, "dev", device)
		}
		result, err := o.ipRun(ctx, ipCmd, args...)
		if err != nil {
			return fmt.Errorf("ip route replace %s%s via %s: %w", endpointIP, prefix, routeTarget, exec.FormatError(result, err))
		}
	} else {
		// Gateway is an interface name (tunnel chaining, PPPoE)
		args = append([]string{}, family...)
		args = append(args, "route", "replace", endpointIP+prefix, "dev", routeTarget)
		result, err := o.ipRun(ctx, ipCmd, args...)
		if err != nil {
			return fmt.Errorf("ip route replace %s%s dev %s: %w", endpointIP, prefix, routeTarget, exec.FormatError(result, err))
		}
	}
	return nil
}

// isIPv6LinkLocal returns true if the IP is an IPv6 link-local address (fe80::/10).
func isIPv6LinkLocal(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLinkLocalUnicast() && parsed.To4() == nil
}

// delKernelHostRoute removes a host route.
func (o *OperatorOS5Impl) delKernelHostRoute(ctx context.Context, endpointIP string) error {
	prefix := "/32"
	family := []string{}
	if isIPv6(endpointIP) {
		prefix = "/128"
		family = []string{"-6"}
	}
	args := append([]string{}, family...)
	args = append(args, "route", "del", endpointIP+prefix)
	_, err := o.ipRun(ctx, "/opt/sbin/ip", args...)
	return err
}

// resolveKernelRouteTarget determines how the kernel currently routes to dstIP.
// When oifDevice is non-empty, constrains the lookup to that specific interface
// (ip route get <dstIP> oif <device>), ensuring the route uses the intended WAN.
// Returns either a gateway IP (DHCP WANs) or a device name (PPPoE).
func (o *OperatorOS5Impl) resolveKernelRouteTarget(ctx context.Context, dstIP, oifDevice string) (gateway, device string, err error) {
	args := []string{}
	if isIPv6(dstIP) {
		args = append(args, "-6")
	}
	args = append(args, "route", "get", dstIP)
	if oifDevice != "" {
		args = append(args, "oif", oifDevice)
	}
	result, runErr := o.ipRun(ctx, "/opt/sbin/ip", args...)
	if runErr != nil {
		return "", "", fmt.Errorf("ip route get %s: %w", dstIP, exec.FormatError(result, runErr))
	}
	// Output: "1.2.3.4 via 10.0.0.1 dev eth0 src 192.168.1.2"
	// or:     "1.2.3.4 dev ppp0 src 10.64.0.2" (point-to-point)
	// IPv6:   "2a00::1 from :: via fe80::1 dev eth0 src 2a00::2"
	// Format is the same for both families ("via <gw> dev <dev>").
	fields := strings.Fields(strings.TrimSpace(result.Stdout))
	for i, f := range fields {
		if f == "via" && i+1 < len(fields) {
			gateway = fields[i+1]
		}
		if f == "dev" && i+1 < len(fields) {
			device = fields[i+1]
		}
	}
	if device == "" {
		return "", "", fmt.Errorf("no device in ip route get output")
	}
	// Safety net: if the resolved device is one of our tunnel interfaces,
	// we'd create a routing loop (endpoint traffic going through the tunnel itself).
	// This can happen if the tunnel is first in Keenetic access policy and oif is empty.
	if strings.HasPrefix(device, "opkgtun") || strings.HasPrefix(device, "awgm") {
		return "", "", fmt.Errorf("routing loop detected: endpoint resolves to tunnel device %s", device)
	}
	return gateway, device, nil
}
