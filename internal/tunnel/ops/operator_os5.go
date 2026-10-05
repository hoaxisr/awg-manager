package ops

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// interfaceReadyTimeout and socketReadyTimeout are defined in operator_os4.go
// (shared between OS4 and OS5 implementations).

// confirmOpkgTun — запись OpkgTun по NDMS-имени из свежего полного списка:
// ok=false — записи нет, ошибка — «не знаем» (список не прочитан). q == nil —
// обвязка без NDMS (тесты), пустое имя — запись KeeneticOS 4.x: записи нет.
//
// Свежий список, не кэш (Get): решения о владении зависят от текущего
// описания записи, а внешняя правка (`interface OpkgTunN description …`) не
// даёт NDMS-хука — кэш InterfaceStore может годами хранить устаревшее
// описание (F532). Он же — доказательство для команд по записи (F546).
func confirmOpkgTun(ctx context.Context, q *query.Queries, name string) (query.Confirmed, *ndms.Interface, bool, error) {
	if q == nil || name == "" {
		return query.Confirmed{}, nil, false, nil
	}
	return q.Interfaces.Confirm(ctx, name)
}

// requireOpkgTun — Confirm для правки живого туннеля: записи нет —
// ErrInterfaceGone с именем, команды по ней не шлются (F546). Kernel-туннель
// запись заводит сам на старте — отсюда совет перезапустить. Запись от
// KeeneticOS 4.x (NDMS-имени нет) — errOS4Tunnel.
func (o *OperatorOS5Impl) requireOpkgTun(ctx context.Context, op, tunnelID string) (query.Confirmed, error) {
	ndmsName := tunnel.NewNames(tunnelID).NDMSName
	if ndmsName == "" {
		return query.Confirmed{}, errOS4Tunnel(op, tunnelID)
	}
	iface, _, ok, err := confirmOpkgTun(ctx, o.queries, ndmsName)
	if err != nil {
		return query.Confirmed{}, tunnel.NewOpError(op, tunnelID, "ndms", fmt.Errorf("read OpkgTun record: %w", err))
	}
	if !ok {
		return query.Confirmed{}, tunnel.NewOpError(op, tunnelID, "ndms", fmt.Errorf("%s: %w — перезапустите туннель, запись будет создана заново", ndmsName, tunnel.ErrInterfaceGone))
	}
	return iface, nil
}

// ForeignRecordError — запись OpkgTunN в NDMS есть, но её описание не наше и
// живого amneziawg под ней нет: номер занят записью сторонней программы
// (F517). Фаза 3 (ip address, mtu, ip global) переписала бы её настройки.
// Типизирована: оператор и тесты отличают её от провала RCI.
type ForeignRecordError struct {
	NDMSName    string
	Description string // что стоит в записи
	Want        string // имя туннеля — то, что ставит CreateOpkgTun
}

func (e *ForeignRecordError) Error() string {
	return fmt.Sprintf("запись %s в NDMS не принадлежит туннелю «%s»: её описание — «%s»", e.NDMSName, e.Want, e.Description)
}

// ensureOpkgTunRecord — Фаза 1 ColdStart/Reconcile: запись OpkgTunN обязана
// быть НАШЕЙ, а не любой. Наша — описание равно имени туннеля (так её ставит
// CreateOpkgTun и переименование через UpdateDescription; так же NDMS
// восстанавливает сохранённую запись после ребута) либо под ней живое
// amneziawg — такие устройства создаём только мы, а описание разошлось
// (переименование без сохранения конфигурации); такое описание переписываем
// на имя туннеля. Иначе — ForeignRecordError
// ДО любой RCI-записи, до backend.Start и без rollbackStart: сторонняя
// программа завела запись на нашем номере (F517), её устройство не наше.
// Ошибка чтения — тоже отказ: «не знаем» ≠ «записи нет», Create поверх
// существующей записи переписал бы её описание.
func (o *OperatorOS5Impl) ensureOpkgTunRecord(ctx context.Context, op string, cfg tunnel.Config, names tunnel.Names) (iface query.Confirmed, justCreated bool, err error) {
	iface, rec, ok, err := confirmOpkgTun(ctx, o.queries, names.NDMSName)
	if err != nil {
		return query.Confirmed{}, false, tunnel.NewOpError(op, cfg.ID, "ndms", fmt.Errorf("read OpkgTun record: %w", err))
	}
	if !ok {
		free, err := o.freeOpkgTunDevice(ctx, op, cfg.ID, names)
		if err != nil {
			return query.Confirmed{}, false, err
		}
		iface, err := o.commands.Interfaces.CreateOpkgTun(ctx, names.NDMSName, cfg.Name, free)
		if err != nil {
			return query.Confirmed{}, false, tunnel.NewOpError(op, cfg.ID, "ndms", fmt.Errorf("create OpkgTun: %w", err))
		}
		o.logInfo(op, cfg.ID, "Created OpkgTun in NDMS")
		return iface, true, nil
	}
	if !o.recordIsOurs(ctx, rec, cfg.Name, names.IfaceName) {
		return query.Confirmed{}, false, tunnel.NewOpError(op, cfg.ID, "ndms",
			&ForeignRecordError{NDMSName: names.NDMSName, Description: rec.Description, Want: cfg.Name})
	}
	if rec.Description != cfg.Name {
		// Запись наша по живому amneziawg, описание разошлось — лечим: иначе
		// после ребута (устройства ещё нет) гейт счёл бы её чужой. Провал
		// записи старт не валит — запись всё равно наша.
		o.logWarn(op, cfg.ID, fmt.Sprintf("описание записи %s %q ≠ имени туннеля %q; под записью живое amneziawg — запись наша, описание переписываем",
			names.NDMSName, rec.Description, cfg.Name))
		if err := o.commands.Interfaces.SetDescription(ctx, iface, cfg.Name); err != nil {
			o.logWarn(op, cfg.ID, "set description: "+err.Error())
		}
	}
	return iface, false, nil
}

// freeOpkgTunDevice — доказательство «устройства opkgtunN нет» перед созданием
// записи OpkgTunN: при живом устройстве NDMS запись не создаёт (C 0xcffd00a9,
// стенд 5.01.C.6, F569). Так бывает после внешнего `no interface OpkgTunN` у
// работающего туннеля — прошивка наше amneziawg не снимает. Устройство
// сносится backend.Stop с гейтом держателя (F500): tun чужой программы не
// трогаем — отказ с держателем, создания нет. Сессия WG всё равно потеряна
// вместе с записью; Фаза 2 поднимет устройство заново.
func (o *OperatorOS5Impl) freeOpkgTunDevice(ctx context.Context, op, tunnelID string, names tunnel.Names) (netdev.Free, error) {
	free, err := netdev.Absent(names.IfaceName)
	if errors.Is(err, netdev.ErrPresent) {
		o.logInfo(op, tunnelID, fmt.Sprintf("записи %s в NDMS нет, а устройство %s живо — удаляем его перед созданием записи", names.NDMSName, names.IfaceName))
		if serr := o.backend.Stop(ctx, names.IfaceName); serr != nil {
			return free, tunnel.NewOpError(op, tunnelID, "kernel",
				fmt.Errorf("устройство %s не удалено — запись %s не создать: %w", names.IfaceName, names.NDMSName, serr))
		}
		free, err = netdev.Absent(names.IfaceName)
	}
	if err != nil {
		return free, tunnel.NewOpError(op, tunnelID, "kernel", err)
	}
	return free, nil
}

// recordIsOurs — единственное правило владения записью OpkgTunN (F517): её
// описание равно имени туннеля либо под ней живое amneziawg (такие устройства
// создаём только мы). Им пользуются и гейт старта, и переименование.
func (o *OperatorOS5Impl) recordIsOurs(ctx context.Context, rec *ndms.Interface, name, iface string) bool {
	if rec.Description == name {
		return true
	}
	running, _ := o.backend.IsRunning(ctx, iface)
	return running
}

// errOS4Tunnel — отказ обслуживать запись, оставшуюся от KeeneticOS 4.x.
//
// У идентификаторов awgm<N> NewNames не строит NDMS-имени, а весь путь OS 5.x
// стоит на OpkgTun: пустое имя уезжало в RCI идентификатором интерфейса. Само
// такое состояние не чинится — прошивка обновилась, а запись осталась в старом
// формате, поэтому текст называет пользователю его действие.
func errOS4Tunnel(op, tunnelID string) error {
	return tunnel.NewOpError(op, tunnelID, "ndms",
		fmt.Errorf("туннель создан на KeeneticOS 4.x — удалите его и импортируйте конфиг заново"))
}

// maskFromPrefix — точечная маска для NDMS из длины префикса. Ноль (префикс не
// задан) и любое значение вне 1..32 дают /32: до появления поля адрес
// интерфейса всегда получал именно её.
func maskFromPrefix(prefix int) string {
	if prefix < 1 || prefix > 32 {
		prefix = 32
	}
	return net.IP(net.CIDRMask(prefix, 32)).String()
}

// addressWithPrefix — форма для `ip address add`. Пустой адрес остаётся пустым:
// вызывающий сам решает, ставить ли его вообще.
func addressWithPrefix(addr string, prefix int) string {
	if addr == "" {
		return ""
	}
	if prefix < 1 || prefix > 32 {
		prefix = 32
	}
	return fmt.Sprintf("%s/%d", addr, prefix)
}

// applyKernelAddresses кладёт адреса конфига на kernel-устройство. Именно
// `replace`, не `add`: после SetAddress через RCI NDMS сам кладёт тот же адрес
// своим хуком (стенд 5.01, 08.09: на устройстве уже inet …/24, `add` отвечает
// «RTNETLINK answers: File exists», exit 2 — WARN в журнале при каждом старте
// после F97), а порядок его хука и нашего `link up` ничем не закреплён, поэтому
// ставим сами, идемпотентно. Единственная точка записи адресов на устройство:
// Start и Reconcile идут через неё, литерал команды больше нигде не пишется
// (пин — TestOS5_KernelAddressSingleWriter).
func (o *OperatorOS5Impl) applyKernelAddresses(ctx context.Context, scope string, cfg tunnel.Config, iface string) {
	if cfg.Address != "" {
		addr := addressWithPrefix(cfg.Address, cfg.AddressPrefix)
		if res, err := o.ipRun(ctx, "/opt/sbin/ip", "address", "replace", "dev", iface, addr); err != nil {
			o.logWarn(scope, cfg.ID, "Failed to set IPv4 address: "+exec.FormatError(res, err).Error())
		}
	}
	// v6 всегда снимаем перед установкой: `replace` кладёт новый /128, но
	// прежний с устройства не убирает — при смене адреса у живого туннеля на
	// интерфейсе осталось бы два, и выбор исходящего адреса стал бы
	// непредсказуемым. На Start операция пустая: устройство только что создано.
	// Убрать адрес больше некому: NDMS до kernel-интерфейса не дотягивается
	// (см. SyncAddress), а второй точки записи адресов контракт не допускает.
	if res, err := o.ipRun(ctx, "/opt/sbin/ip", "-6", "address", "flush", "dev", iface); err != nil {
		o.logWarn(scope, cfg.ID, "Failed to clear IPv6 address: "+exec.FormatError(res, err).Error())
	}
	if cfg.AddressIPv6 != "" {
		if res, err := o.ipRun(ctx, "/opt/sbin/ip", "-6", "address", "replace", "dev", iface, cfg.AddressIPv6+"/128"); err != nil {
			err = exec.FormatError(res, err)
			o.logWarn(scope, cfg.ID, "Failed to set IPv6 address: "+err.Error())
			o.appLog.Warn(scope, cfg.ID, "IPv6 адрес: "+err.Error())
		}
	}
}

// ipRunFunc is the signature for running ip commands.
// Defaults to exec.Run; overridden in tests to avoid real /opt/sbin/ip calls.
type ipRunFunc func(ctx context.Context, name string, args ...string) (*exec.Result, error)

// OperatorOS5Impl is the Operator implementation for Keenetic OS 5.0+.
// Uses NDMS for interface management, kernel backend for tunnel interfaces.
type OperatorOS5Impl struct {
	*clientRouteOps // provides the 5 client-route Operator methods

	queries  *query.Queries
	commands *command.Commands
	wg       wg.Client
	backend  Backend
	firewall firewall.Manager
	ipRun    ipRunFunc // ip command runner (mockable in tests)

	appLog *logging.ScopedLogger

	// Endpoint route tracking (tunnelID -> endpointIP)
	endpointRoutes map[string]string
	// routeHeldByOther — «host-route до ip держит ещё кто-то, кроме excludeID»,
	// по стору и через бэкенды. См. removeHostRouteIfUnused.
	routeHeldByOther func(excludeID, ip string) bool
	endpointRoutesMu sync.RWMutex

	// DNS tracking (tunnelID -> DNS servers applied via NDMS)
	// Used to clean up DNS entries on Stop/Delete.
	appliedDNS   map[string][]string
	appliedDNSMu sync.RWMutex

	// hookNotifier registers expected NDMS hooks for self-triggered changes,
	// preventing infinite loops when our InterfaceUp/InterfaceDown calls
	// trigger hooks that would otherwise cause orchestrator to react.
	hookNotifier tunnel.HookNotifier
}

// NewOperatorOS5 creates a new OS5 operator.
func NewOperatorOS5(
	queries *query.Queries,
	commands *command.Commands,
	wgClient wg.Client,
	backendImpl Backend,
	firewallMgr firewall.Manager,
) *OperatorOS5Impl {
	o := &OperatorOS5Impl{
		queries:        queries,
		commands:       commands,
		wg:             wgClient,
		backend:        backendImpl,
		firewall:       firewallMgr,
		ipRun:          exec.Run,
		endpointRoutes: make(map[string]string),
		appliedDNS:     make(map[string][]string),
	}
	// Wire clientRouteOps after o is built — it captures o.ipRun and
	// o.logWarn (bound to o) as the runner and warn-logger.
	o.clientRouteOps = newClientRouteOps(
		func(ctx context.Context, name string, args ...string) (*exec.Result, error) {
			return o.ipRun(ctx, name, args...)
		},
		o.logWarn,
	)
	return o
}

// SetHookNotifier sets the hook notifier for registering expected NDMS hooks.
// Must be called before any operations that change NDMS interface state.
func (o *OperatorOS5Impl) SetHookNotifier(hn tunnel.HookNotifier) {
	o.hookNotifier = hn
}

// expectHook registers an expected NDMS hook to filter self-triggered events.
// No-op if hookNotifier is not set.
func (o *OperatorOS5Impl) expectHook(ndmsName, level string) {
	if o.hookNotifier != nil {
		o.hookNotifier.ExpectHook(ndmsName, level)
	}
}

// ColdStart creates a tunnel from scratch or recreates from wrong type (tun → amneziawg).
// Full sequence: OpkgTun → ip link del + ip link add amneziawg → NDMS config →
// ip addr add → wg setconf → ip link set up → InterfaceUp → routes → firewall → Save.
// Used for: BootReady, NotCreated, Broken.
func (o *OperatorOS5Impl) ColdStart(ctx context.Context, cfg tunnel.Config) error {
	names := tunnel.NewNames(cfg.ID)

	if names.NDMSName == "" {
		return errOS4Tunnel("start", cfg.ID)
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		return tunnel.NewOpError("start", cfg.ID, "", err)
	}

	// === Phase 1: Ensure OpkgTun exists — and is OURS (F517) ===
	iface, justCreated, err := o.ensureOpkgTunRecord(ctx, "start", cfg, names)
	if err != nil {
		return err // без rollbackStart: чужую запись и её устройство не трогаем
	}

	// === Phase 2: kernel device (ip link add type amneziawg) ===
	// Устройство — ДО NDMS-конфига: на записи OpkgTun без kernel-устройства
	// (NDMS state: error — после ip link del, rmmod, отката неудачного старта)
	// роутер отвергает ip address как `system failed [0xcffd0217]`, а откат
	// сносил устройство снова — туннель не стартовал никогда (стенд 5.01,
	// 2026-09-05; трекер F97). Когда устройство появляется, запись сама
	// возвращается из error в down и принимает адрес.
	if err := o.backend.Start(ctx, names.IfaceName); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, iface, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "backend", err)
	}

	// Wait for interface to appear in /sys/class/net
	if err := o.backend.WaitReady(ctx, names.IfaceName, interfaceReadyTimeout); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, iface, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "backend", fmt.Errorf("wait ready: %w", err))
	}

	o.logInfo("start", cfg.ID, "Backend started (kernel)")
	o.appLog.Info("start", cfg.ID, "Интерфейс создан (kernel)")

	// === Phase 3: NDMS config ===
	// Always re-apply address/MTU — after ip link del + ip link add, NDMS
	// does not re-apply stored config to the new kernel interface.
	// SetAddress via RCI triggers NDMS to do "ip addr add" on the interface.
	__addr, __mask := cfg.Address, maskFromPrefix(cfg.AddressPrefix)
	if err := o.commands.Interfaces.SetAddress(ctx, iface, __addr, __mask); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, iface, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "ndms", fmt.Errorf("set address: %w", err))
	}

	if err := o.commands.Interfaces.SetMTU(ctx, iface, cfg.MTU); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, iface, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "ndms", fmt.Errorf("set MTU: %w", err))
	}

	// v6-адрес в NDMS не отправляем: применить его к нашему устройству роутер
	// не может. Kernel-путь удаляет tun-устройство, созданное NDMS, и ставит
	// своё `amneziawg` — после подмены Ip6Tools отвечает на любую попытку
	// `no such device[19]` (стенд 5.01, 12.09: при up, повторной командой и
	// после down/up; на нетронутом tun-устройстве адрес встаёт без ошибок).
	// Адрес кладёт applyKernelAddresses ниже, и он же возвращает его после
	// каждого старта, включая холодную загрузку.
	//
	// И не чистим: любое обращение к слою ipv6 будит NDMS — он видит адрес,
	// который мы положили на устройство, и заводит запись сам, после чего
	// пытается её применить и снова упирается в `no such device` (стенд 5.01,
	// 12.09). Оставленный в покое слой молчит, а запись прежних версий уходит
	// при ближайшем пересоздании интерфейса вместе с ним.

	// Ensure ip global is set — it's not part of CreateOpkgTun anymore
	// (split out to avoid premature nginx binding), so re-apply on every start.
	if err := o.commands.Interfaces.SetIPGlobal(ctx, iface); err != nil {
		o.logWarn("start", cfg.ID, "Failed to set ip global: "+err.Error())
	}

	o.logInfo("start", cfg.ID, "NDMS config applied (address + MTU + global)")

	// Apply DNS servers (idempotent, re-applied on every start)
	if len(cfg.DNS) > 0 {
		if err := o.commands.Interfaces.SetDNS(ctx, iface, cfg.DNS); err != nil {
			o.logWarn("start", cfg.ID, "Failed to set DNS: "+err.Error())
		} else {
			o.appliedDNSMu.Lock()
			o.appliedDNS[cfg.ID] = cfg.DNS
			o.appliedDNSMu.Unlock()
		}
	}

	// === Phase 4: Interface config + WireGuard configuration ===
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1280
	}
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "dev", names.IfaceName,
		"txqueuelen", "1000", "mtu", fmt.Sprintf("%d", mtu)); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, iface, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "kernel", fmt.Errorf("configure interface: %w", err))
	}
	o.logInfo("start", cfg.ID, fmt.Sprintf("Kernel interface configured (mtu=%d, qlen=1000)", mtu))

	if err := o.wg.SetConf(ctx, names.IfaceName, cfg.ConfPath); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, iface, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "wg", err)
	}

	o.logInfo("start", cfg.ID, "WireGuard config applied")

	// === Phase 5: Assign addresses + bring up ===
	o.applyKernelAddresses(ctx, "start", cfg, names.IfaceName)

	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", names.IfaceName); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, iface, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "link", fmt.Errorf("ip link up: %w", exec.FormatError(result, err)))
	}

	// NDMS InterfaceUp sets conf: running (intent UP). Commands register
	// the expected hook themselves via the HookNotifier wired at startup.
	// Always needed in Start: after Stop, InterfaceDown set conf: disabled.
	if err := o.commands.Interfaces.InterfaceUp(ctx, iface); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, iface, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "ndms", fmt.Errorf("interface up: %w", err))
	}

	o.logInfo("start", cfg.ID, "Interface up")

	// === Phase 6: Set up routing ===
	// Endpoint route: always set up when endpoint is configured.
	// Needed for tunnel chaining (tunnel through tunnel) and routing loop prevention.
	endpointRouteOK := false
	if cfg.Endpoint != "" {
		// Use pre-resolved IP when available — avoids DNS re-resolution which
		// can fail right after start (awg show empty, Go DNS may not work on router).
		routeEndpoint := endpointWithResolvedIP(cfg.Endpoint, cfg.EndpointIP)
		if _, err := o.SetupEndpointRoute(ctx, cfg.ID, routeEndpoint, cfg.KernelDevice, cfg.ISPInterface); err != nil {
			o.logWarn("start", cfg.ID, "Endpoint route failed (non-fatal): "+err.Error())
			o.appLog.Warn("start", cfg.ID, "Не удалось создать endpoint route: "+err.Error())
		} else {
			endpointRouteOK = true
		}
	} else {
		endpointRouteOK = true // no endpoint — nothing to route
	}

	// Default route: only when DefaultRoute is enabled.
	// NDMS manages the route via the kernel backend.
	// Non-fatal: if NDMS is not ready (e.g. boot race), tunnel starts without
	// default route. HandleWANUp will retry when WAN stabilizes.
	if cfg.DefaultRoute {
		if err := o.commands.Routes.SetDefaultRoute(ctx, iface); err != nil {
			o.logWarn("start", cfg.ID, "Default route failed (non-fatal): "+err.Error())
			o.appLog.Warn("start", cfg.ID, "Не удалось установить маршрут по умолчанию — будет повторная попытка при WAN UP")
		} else {
			if cfg.AddressIPv6 != "" {
				if err := o.commands.Routes.SetIPv6DefaultRoute(ctx, iface); err != nil {
					o.logWarn("start", cfg.ID, "Failed to set IPv6 default route: "+err.Error())
				}
			}
			o.appLog.Info("start", cfg.ID, "Маршрут по умолчанию добавлен через "+names.IfaceName)
			if !endpointRouteOK {
				o.appLog.Warn("start", cfg.ID, "Default route установлен без endpoint route — возможны проблемы с маршрутизацией")
			}
		}
	}

	o.logInfo("start", cfg.ID, "Routing configured")

	// === Phase 7: Add firewall rules ===
	// Use kernel interface name (opkgtun0), not NDMS name (OpkgTun0)
	if err := o.firewall.AddRules(ctx, names.IfaceName); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, iface, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "firewall", err)
	}

	o.logInfo("start", cfg.ID, "Firewall rules added")
	o.appLog.Info("start", cfg.ID, "Правила файрвола добавлены для "+names.IfaceName)

	o.logInfo("start", cfg.ID, "Tunnel started successfully")
	return nil
}

// Stop brings down a tunnel without destroying the interface.
// ip link set down + InterfaceDown (conf: disabled) + Save.
// NDMS handles routing/failover automatically when link goes down.
// Interface stays as amneziawg with WG config and address loaded.
//
// Только своё (F500/F517): устройство опускаем, лишь если оно наше amneziawg
// — на номере может стоять чужой tun (csqtt, #935); `conf: disabled` ставим
// лишь нашей записи — по правилу recordIsOurs с именем туннеля name. Пустое
// name (карточки нет) описанием не совпадает ни с чем: без живого amneziawg
// запись не трогаем.
//
// Запись подтверждается одним свежим списком в начале (F546): записи нет —
// `conf: disabled` ставить некому, команда по имени её СОЗДАЛА бы, а наше
// живое amneziawg сносится (записи нет ⇒ нашего устройства нет, F569); список не
// прочитан — локальные шаги (устройство, host-route) всё равно делаются,
// NDMS не трогаем, ошибка наружу.
func (o *OperatorOS5Impl) Stop(ctx context.Context, tunnelID, name string) error {
	names := tunnel.NewNames(tunnelID)
	iface, rec, ok, confirmErr := confirmOpkgTun(ctx, o.queries, names.NDMSName)

	running, _ := o.backend.IsRunning(ctx, names.IfaceName)
	switch {
	case running && confirmErr == nil && !ok && names.NDMSName != "":
		// Записи нет, наше amneziawg живо (внешнее `no interface`): устройство
		// сносим, а не опускаем — живое opkgtunN не даст NDMS создать запись
		// на следующем старте (C 0xcffd00a9, F569).
		if err := o.backend.Stop(ctx, names.IfaceName); err != nil {
			o.logWarn("stop", tunnelID, "delete kernel interface: "+err.Error())
		} else {
			o.logInfo("stop", tunnelID, fmt.Sprintf("запись %s снята в NDMS — устройство %s удалено", names.NDMSName, names.IfaceName))
			o.appLog.Info("stop", tunnelID, fmt.Sprintf("Запись %s снята в NDMS — устройство %s удалено", names.NDMSName, names.IfaceName))
		}
	case running:
		if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "down", "dev", names.IfaceName); err != nil {
			o.logWarn("stop", tunnelID, "ip link set down: "+err.Error())
		}
		// InterfaceDown sets conf: disabled — NDMS won't bring it up on its own.
		if ok {
			o.interfaceDownBestEffort(ctx, tunnelID, iface)
		}
	default:
		o.logInfo("stop", tunnelID, "kernel interface is not our amneziawg — link left untouched")
		switch {
		case ok && name != "" && o.recordIsOurs(ctx, rec, name, names.IfaceName):
			o.interfaceDownBestEffort(ctx, tunnelID, iface)
		case ok:
			o.logInfo("stop", tunnelID, fmt.Sprintf("record %s is not ours (description %q) — conf: disabled not set", names.NDMSName, rec.Description))
		}
	}
	// DNS, поставленный стартом, снимаем, как OS4 Stop (F561). Записи нет —
	// снимать не с чего (ссылка на отсутствующий — E), трекинг забываем;
	// список не прочитан — NDMS не трогаем, трекинг остаётся.
	switch {
	case ok:
		o.clearAppliedDNS(ctx, tunnelID, iface)
	case confirmErr == nil:
		o.appliedDNSMu.Lock()
		delete(o.appliedDNS, tunnelID)
		o.appliedDNSMu.Unlock()
	}
	if confirmErr == nil && !ok {
		o.logInfo("stop", tunnelID, "OpkgTun record absent in NDMS — conf: disabled not needed")
	}

	// Остановленному туннелю host-route не нужен, а карта маршрутов обязана
	// означать «маршрут стоит», а не «туннель когда-то стартовал»: иначе
	// остановленный сосед вечно держит чужой адрес от снятия (F231). Сосед,
	// который РАБОТАЕТ, маршрут удержит — снятие идёт общим путём с ref-count.
	o.removeHostRouteIfUnused(ctx, "stop", tunnelID, "")

	if confirmErr != nil {
		return tunnel.NewOpError("stop", tunnelID, "ndms", fmt.Errorf("read OpkgTun record: %w — conf: disabled not set", confirmErr))
	}

	o.logInfo("stop", tunnelID, "Tunnel stopped (link down, conf: disabled)")
	o.appLog.Info("stop", tunnelID, "Туннель остановлен")
	return nil
}

// clearAppliedDNS removes DNS servers that were applied during Start and clears tracking.
func (o *OperatorOS5Impl) clearAppliedDNS(ctx context.Context, tunnelID string, iface query.Confirmed) {
	o.appliedDNSMu.Lock()
	servers := o.appliedDNS[tunnelID]
	delete(o.appliedDNS, tunnelID)
	o.appliedDNSMu.Unlock()

	if len(servers) > 0 {
		_ = o.commands.Interfaces.ClearDNS(ctx, iface, servers)
		o.logInfo("stop", tunnelID, "DNS servers removed")
	}
}

// interfaceDownBestEffort tries to set NDMS conf: disabled.
// Retries up to 3 times for transient failures (NDMS busy/timeout).
// Exit 122 = NDMS permanent rejection (already down) — not an error.
func (o *OperatorOS5Impl) interfaceDownBestEffort(ctx context.Context, tunnelID string, iface query.Confirmed) {
	// Commands register the expected "disabled" hook on each attempt via
	// the HookNotifier wired at startup.
	for attempt := 1; attempt <= 3; attempt++ {
		err := o.commands.Interfaces.InterfaceDown(ctx, iface)
		if err == nil {
			o.logInfo("stop", tunnelID, "Interface down (conf: disabled)")
			return
		}
		if strings.Contains(err.Error(), "exit status 122") {
			o.logInfo("stop", tunnelID, "InterfaceDown: already disabled (exit 122)")
			return
		}
		o.logWarn("stop", tunnelID, fmt.Sprintf("InterfaceDown attempt %d/3 failed: %s", attempt, err))
		if attempt < 3 {
			time.Sleep(1 * time.Second)
		}
	}
	// Not fatal — cleanup continues regardless.
	// The enabled/disabled state is tracked in the program's own JSON storage.
}

// Delete completely removes a tunnel.
func (o *OperatorOS5Impl) Delete(ctx context.Context, stored *storage.AWGTunnel) error {
	names := tunnel.NewNames(stored.ID)

	// 1. Подтверждение записи — ДО любого сноса. Дальше порядок F596/F598:
	//    ожидания хуков → kernel-устройство → запись NDMS. Запись, снятая при живом amneziawg opkgtunN, даёт в журнале
	//    ndm `C Tun: system failed [0xcffd003b]` на каждом удалении (стенд
	//    01.10, A7/B); снятое устройство, затем запись — 0 E/C (A7, повторный
	//    Delete). Запись снимает всё своё: address, MTU, security-level,
	//    ip global, default route, DNS name-servers.
	//
	// У записи от KeeneticOS 4.x NDMS-имени нет: удалять в NDMS нечего, а пустая
	// строка уехала бы в RCI идентификатором интерфейса. Пропускаем шаг, но не
	// отказываем — иначе такой туннель нельзя удалить, то есть и пересоздать.
	//
	// Записи нет в свежем списке — сносить в NDMS нечего, `no interface` по
	// отсутствующему — строка E в журнале ndm (F546); устройство снимаем.
	//
	// Список не прочитан — ни host-route, ни устройство, ни NDMS, ни DNS не
	// трогаем, ошибка наружу: оркестратор не удалит запись туннеля, повтор
	// возможен. Устройство снимается только перед сносом записи, а без списка
	// сносить её нельзя (L4, N3). Маршруты правил и мониторинг оркестратор
	// снимает раньше, до вызова Delete (decideDelete) — они вне этого шага.
	iface, _, ok, confirmErr := confirmOpkgTun(ctx, o.queries, names.NDMSName)
	if confirmErr != nil {
		o.logWarn("delete", stored.ID, "read OpkgTun record: "+confirmErr.Error()+" — NDMS record and kernel interface kept")
		return tunnel.NewOpError("delete", stored.ID, "ndms", fmt.Errorf("read OpkgTun record: %w", confirmErr))
	}

	// 2. Remove endpoint route from kernel (host route to VPN server via WAN).
	//    Uses persisted IP. Fallback to DNS for old tunnels without stored IP.
	endpointIP := stored.ResolvedEndpointIP
	if endpointIP == "" && stored.Peer.Endpoint != "" {
		if ip, err := netutil.ResolveEndpointIP(stored.Peer.Endpoint); err == nil {
			endpointIP = ip
		}
	}
	// Петлю снимаем тоже: netutil.SkipHostRoute запрещает ставить маршрут, но
	// не снимать — наследство прежних версий уходит с роутера отсюда и из
	// гарда на старте. Через общий путь с ref-count: сосед к тому же серверу
	// маршрут не потеряет (F130/#867).
	o.removeHostRouteIfUnused(ctx, "delete", stored.ID, endpointIP)

	// 3. Ожидания — ДО Stop: исчезновение устройства под записью может дать
	//    грань по OpkgTunN раньше `no interface`; оркестратор должен поглотить
	//    её как свою, а не принять за внешнее снятие (ожидания живут 45 с).
	if ok {
		o.expectHook(names.NDMSName, "disabled")
		o.expectHook(names.NDMSName, "destroyed")
	}

	// 4. Remove kernel interface (our amneziawg — NDMS can't delete what we created).
	//    Через backend.Stop — с гейтом держателя (F500): устройство, открытое
	//    чужой программой, не наше — его не сносим, а удаление нашей записи
	//    туннеля продолжаем. Прочие ошибки (устройства уже нет) — как прежде,
	//    без шума.
	var held *backend.HeldError
	if err := o.backend.Stop(ctx, names.IfaceName); errors.As(err, &held) {
		o.logWarn("delete", stored.ID, "kernel interface kept: "+err.Error())
		o.appLog.Warn("delete", stored.ID, "Интерфейс "+names.IfaceName+" не удалён: "+err.Error())
	}

	// 5. Запись NDMS. Снос отвергнут (отсутствие записи DeleteOpkgTun уже
	//    терпит) — ошибка наружу, как у nwg: иначе запись туннеля уйдёт, а
	//    OpkgTunN останется на роутере без хозяина (F559). Остаток П2:
	//    устройство уже снято, и до повторного Delete прошивка пишет
	//    `C 0xcffd0ba1, no such device` — повтор снимает запись чисто (A7).
	//    Пересоздавать устройство ради отказа в окне Confirm→POST не стали.
	var ndmsErr error
	switch {
	case ok:
		if err := o.commands.Interfaces.DeleteOpkgTun(ctx, iface); err != nil {
			o.logWarn("delete", stored.ID, "DeleteOpkgTun: "+err.Error())
			ndmsErr = fmt.Errorf("delete OpkgTun record: %w", err)
		}
	case names.NDMSName != "":
		o.logInfo("delete", stored.ID, "OpkgTun record absent in NDMS — nothing to delete there")
	}

	// 6. Clear in-memory tracking (endpointRoutes уже забыт на шаге 1).
	//    Сохранения конфигурации среди шагов нет: его ведёт SaveCoordinator,
	//    который сам сводит запросы всех команд в одну запись.
	o.appliedDNSMu.Lock()
	delete(o.appliedDNS, stored.ID)
	o.appliedDNSMu.Unlock()

	if ndmsErr != nil {
		return tunnel.NewOpError("delete", stored.ID, "ndms", ndmsErr)
	}

	o.logInfo("delete", stored.ID, "Tunnel deleted")
	o.appLog.Info("delete", stored.ID, "Туннель удалён")
	return nil
}

// Reconcile re-applies NDMS/system configuration around an already-running process.
// Assumes: process is running, interface exists. Re-applies WG config, NDMS, routing, firewall.
func (o *OperatorOS5Impl) Reconcile(ctx context.Context, cfg tunnel.Config) error {
	names := tunnel.NewNames(cfg.ID)

	if names.NDMSName == "" {
		return errOS4Tunnel("reconcile", cfg.ID)
	}

	o.logInfo("reconcile", cfg.ID, "Reconciling NDMS state around running process")
	o.appLog.Info("reconcile", cfg.ID, "Восстановление конфигурации NDMS")

	// === Phase 1: Ensure OpkgTun exists — and is OURS (F517) ===
	iface, justCreated, err := o.ensureOpkgTunRecord(ctx, "reconcile", cfg, names)
	if err != nil {
		return err // без rollbackStart: чужую запись и её устройство не трогаем
	}

	// === Phase 2: Ensure kernel interface is amneziawg type ===
	// A live amneziawg device under an existing OpkgTun record is kept as is:
	// recreating it drops the session, and every awg-manager restart used to
	// do exactly that (F129, #867). It is recreated when the device is missing
	// or is not amneziawg (rmmod, manual ip link del while suspended), and
	// when the OpkgTun record had to be created: NDMS SetAddress below fails
	// (exit 122) on a running kernel-mode device, so the device must be fresh.
	// ip link del triggers transient NDMS state:error — safe under per-tunnel lock.
	running, _ := o.backend.IsRunning(ctx, names.IfaceName)
	if running && !justCreated {
		o.logInfo("reconcile", cfg.ID, "Kernel interface alive, kept")
	} else {
		// Живое amneziawg под только что созданной записью: backend.Start его
		// не тронул бы (IsRunning=true), а SetAddress ниже требует свежее —
		// сносим здесь. Отсутствующее или не-amneziawg устройство (plain tun
		// после ребута) сносит сам backend.Start — и отказывает HeldError,
		// если устройство держит чужая программа (F500). Сносить его тут
		// значило бы обойти этот гейт.
		if running {
			o.ipRun(ctx, "/opt/sbin/ip", "link", "del", "dev", names.IfaceName)
		}
		if err := o.backend.Start(ctx, names.IfaceName); err != nil {
			return tunnel.NewOpError("reconcile", cfg.ID, "backend", err)
		}
		if err := o.backend.WaitReady(ctx, names.IfaceName, interfaceReadyTimeout); err != nil {
			return tunnel.NewOpError("reconcile", cfg.ID, "backend", fmt.Errorf("wait ready: %w", err))
		}
		o.logInfo("reconcile", cfg.ID, "Kernel interface recreated as amneziawg")
	}
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1280
	}
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "dev", names.IfaceName,
		"txqueuelen", "1000", "mtu", fmt.Sprintf("%d", mtu)); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "kernel", fmt.Errorf("configure interface: %w", err))
	}

	// === Phase 3: Apply WireGuard configuration ===
	// syncconf keeps the established session when the file is unchanged;
	// on a fresh device it is equivalent to setconf.
	if err := o.wg.SyncConf(ctx, names.IfaceName, cfg.ConfPath); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "wg", err)
	}
	o.logInfo("reconcile", cfg.ID, "WireGuard config applied")

	// === Phase 3: Configure NDMS interface ===
	// Full config (address + MTU + IPv6) only when OpkgTun was just created.
	// SetAddress on a running kernel-mode interface fails (exit 122).
	// MTU is always re-applied: NDMS may have default (1420) if config was lost,
	// causing oversized encrypted packets that degrade upload throughput.
	if justCreated {
		__addr, __mask := cfg.Address, maskFromPrefix(cfg.AddressPrefix)
		if err := o.commands.Interfaces.SetAddress(ctx, iface, __addr, __mask); err != nil {
			return tunnel.NewOpError("reconcile", cfg.ID, "ndms", fmt.Errorf("set address: %w", err))
		}
		// v6 в NDMS не трогаем вовсе (см. ColdStart): адрес кладёт
		// applyKernelAddresses.
	}
	if cfg.MTU > 0 {
		if err := o.commands.Interfaces.SetMTU(ctx, iface, cfg.MTU); err != nil {
			o.logWarn("reconcile", cfg.ID, "Failed to re-apply NDMS MTU: "+err.Error())
		}
	}

	// Ensure ip global is set
	if err := o.commands.Interfaces.SetIPGlobal(ctx, iface); err != nil {
		o.logWarn("reconcile", cfg.ID, "Failed to set ip global: "+err.Error())
	}

	// Re-apply DNS servers (may have been lost after reboot)
	if len(cfg.DNS) > 0 {
		if err := o.commands.Interfaces.SetDNS(ctx, iface, cfg.DNS); err != nil {
			o.logWarn("reconcile", cfg.ID, "Failed to re-apply DNS: "+err.Error())
		} else {
			o.appliedDNSMu.Lock()
			o.appliedDNS[cfg.ID] = cfg.DNS
			o.appliedDNSMu.Unlock()
		}
	}

	o.applyKernelAddresses(ctx, "reconcile", cfg, names.IfaceName)

	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", names.IfaceName); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "link", fmt.Errorf("ip link up: %w", exec.FormatError(result, err)))
	}

	// NDMS InterfaceUp: only when OpkgTun was just created. Commands
	// register the expected "running" hook via HookNotifier.
	if justCreated {
		if err := o.commands.Interfaces.InterfaceUp(ctx, iface); err != nil {
			return tunnel.NewOpError("reconcile", cfg.ID, "ndms", fmt.Errorf("interface up: %w", err))
		}
	}

	o.logInfo("reconcile", cfg.ID, "Interface configured and up")

	// === Phase 4: Set up routing ===
	// Endpoint route: always set up when endpoint is configured
	endpointRouteOK := false
	if cfg.Endpoint != "" {
		routeEndpoint := endpointWithResolvedIP(cfg.Endpoint, cfg.EndpointIP)
		if _, err := o.SetupEndpointRoute(ctx, cfg.ID, routeEndpoint, cfg.KernelDevice, cfg.ISPInterface); err != nil {
			o.logWarn("reconcile", cfg.ID, "Endpoint route failed (non-fatal): "+err.Error())
			o.appLog.Warn("reconcile", cfg.ID, "Не удалось создать endpoint route: "+err.Error())
		} else {
			endpointRouteOK = true
		}
	} else {
		endpointRouteOK = true
	}

	// Default route: only when DefaultRoute is enabled.
	if cfg.DefaultRoute {
		if err := o.commands.Routes.SetDefaultRoute(ctx, iface); err != nil {
			_ = o.CleanupEndpointRoute(ctx, cfg.ID)
			return tunnel.NewOpError("reconcile", cfg.ID, "ndms", fmt.Errorf("set default route: %w", err))
		}
		if cfg.AddressIPv6 != "" {
			if err := o.commands.Routes.SetIPv6DefaultRoute(ctx, iface); err != nil {
				o.logWarn("reconcile", cfg.ID, "Failed to set IPv6 default route: "+err.Error())
			}
		}
		o.appLog.Info("reconcile", cfg.ID, "Маршрут по умолчанию добавлен через "+names.IfaceName)
		if !endpointRouteOK {
			o.appLog.Warn("reconcile", cfg.ID, "Default route установлен без endpoint route — возможны проблемы с маршрутизацией")
		}
	}

	o.logInfo("reconcile", cfg.ID, "Routing configured")

	// === Phase 5: Add firewall rules ===
	if err := o.firewall.AddRules(ctx, names.IfaceName); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "firewall", err)
	}
	o.logInfo("reconcile", cfg.ID, "Firewall rules added")
	o.appLog.Info("reconcile", cfg.ID, "Правила файрвола добавлены для "+names.IfaceName)

	o.logInfo("reconcile", cfg.ID, "Reconciliation complete")
	o.appLog.Info("reconcile", cfg.ID, "Конфигурация NDMS восстановлена")
	return nil
}

// SetDefaultRoute adds a default route through the tunnel interface.
func (o *OperatorOS5Impl) SetDefaultRoute(ctx context.Context, tunnelID string) error {
	iface, err := o.requireOpkgTun(ctx, "set_default_route", tunnelID)
	if err != nil {
		return err
	}
	if err := o.commands.Routes.SetDefaultRoute(ctx, iface); err != nil {
		return err
	}
	return nil
}

// RemoveDefaultRoute removes the default route through the tunnel interface.
// Записи нет в NDMS — маршрута через неё нет тоже: снимать нечего (F546).
func (o *OperatorOS5Impl) RemoveDefaultRoute(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)
	iface, _, ok, err := confirmOpkgTun(ctx, o.queries, names.NDMSName)
	if err != nil {
		return tunnel.NewOpError("remove_default_route", tunnelID, "ndms", fmt.Errorf("read OpkgTun record: %w", err))
	}
	if !ok {
		return nil
	}
	o.commands.Routes.RemoveIPv6DefaultRoute(ctx, iface)
	if err := o.commands.Routes.RemoveDefaultRoute(ctx, iface); err != nil {
		return err
	}
	return nil
}

// Suspend sets link down without removing the interface or changing NDMS conf.
// NDMS sees pending state and handles failover automatically.
// Routes and firewall are NOT touched — NDMS manages failover.
func (o *OperatorOS5Impl) Suspend(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "down", "dev", names.IfaceName); err != nil {
		return fmt.Errorf("suspend: ip link set down: %w", err)
	}
	o.logInfo("suspend", tunnelID, "Interface suspended (link down)")
	o.appLog.Info("suspend", tunnelID, "Интерфейс приостановлен")
	return nil
}

// Resume sets link up after Suspend. NDMS restores routing automatically.
func (o *OperatorOS5Impl) Resume(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", names.IfaceName); err != nil {
		return fmt.Errorf("resume: ip link set up: %w", err)
	}
	o.logInfo("resume", tunnelID, "Interface resumed (link up)")
	o.appLog.Info("resume", tunnelID, "Интерфейс возобновлён")
	return nil
}

// ApplyConfig applies a new WireGuard config to a running tunnel.
func (o *OperatorOS5Impl) ApplyConfig(ctx context.Context, tunnelID, configPath string) error {
	names := tunnel.NewNames(tunnelID)

	if err := o.wg.SetConf(ctx, names.IfaceName, configPath); err != nil {
		return tunnel.NewOpError("apply_config", tunnelID, "wg", err)
	}

	o.logInfo("apply_config", tunnelID, "Config applied")
	return nil
}

// SetMTU sets MTU on a running tunnel interface via NDMS.
func (o *OperatorOS5Impl) SetMTU(ctx context.Context, tunnelID string, mtu int) error {
	iface, err := o.requireOpkgTun(ctx, "set_mtu", tunnelID)
	if err != nil {
		return err
	}
	if err := o.commands.Interfaces.SetMTU(ctx, iface, mtu); err != nil {
		return tunnel.NewOpError("set_mtu", tunnelID, "ndms", err)
	}
	o.logInfo("set_mtu", tunnelID, fmt.Sprintf("MTU set to %d", mtu))
	return nil
}

// SyncDNS updates DNS servers on a running tunnel's NDMS interface.
func (o *OperatorOS5Impl) SyncDNS(ctx context.Context, tunnelID string, dns []string) error {
	iface, err := o.requireOpkgTun(ctx, "sync_dns", tunnelID)
	if err != nil {
		return err
	}
	// Clear previously applied DNS first
	o.appliedDNSMu.RLock()
	oldDNS := o.appliedDNS[tunnelID]
	o.appliedDNSMu.RUnlock()
	if len(oldDNS) > 0 {
		_ = o.commands.Interfaces.ClearDNS(ctx, iface, oldDNS)
	}
	// Трекинг — ровно то, что стоит на роутере: прежние сняты, новые — по
	// одному, до первого отказа. Иначе Stop снимал бы уже снятое (F561).
	var set []string
	var setErr error
	for _, srv := range dns {
		if setErr = o.commands.Interfaces.SetDNS(ctx, iface, []string{srv}); setErr != nil {
			break
		}
		set = append(set, srv)
	}
	o.appliedDNSMu.Lock()
	if len(set) > 0 {
		o.appliedDNS[tunnelID] = set
	} else {
		delete(o.appliedDNS, tunnelID)
	}
	o.appliedDNSMu.Unlock()
	if setErr != nil {
		return tunnel.NewOpError("sync_dns", tunnelID, "ndms", setErr)
	}
	o.logInfo("sync_dns", tunnelID, fmt.Sprintf("DNS synced: %v", dns))
	return nil
}

// SyncAddress updates IPv4/IPv6 address on a running tunnel's NDMS interface.
func (o *OperatorOS5Impl) SyncAddress(ctx context.Context, tunnelID string, address string, prefix int, ipv6 string) error {
	names := tunnel.NewNames(tunnelID)
	iface, err := o.requireOpkgTun(ctx, "sync_address", tunnelID)
	if err != nil {
		return err
	}
	__addr, __mask := address, maskFromPrefix(prefix)
	if err := o.commands.Interfaces.SetAddress(ctx, iface, __addr, __mask); err != nil {
		return tunnel.NewOpError("sync_address", tunnelID, "ndms", err)
	}
	// v6 правим прямо на устройстве, через ту же единственную точку записи:
	// в NDMS он до kernel-интерфейса не доходит вовсе (см. ColdStart), то есть
	// до этой правки смена v6-адреса у живого туннеля не применялась до его
	// перезапуска.
	o.applyKernelAddresses(ctx, "sync_address", tunnel.Config{
		ID: tunnelID, Address: address, AddressPrefix: prefix, AddressIPv6: ipv6,
	}, names.IfaceName)

	o.logInfo("sync_address", tunnelID, fmt.Sprintf("Address synced: %s, IPv6: %s", address, ipv6))
	return nil
}

// UpdateDescription updates the NDMS interface description for a tunnel.
func (o *OperatorOS5Impl) UpdateDescription(ctx context.Context, tunnelID, prevName, description string) error {
	return o.setDescription(ctx, tunnelID, prevName, description, false)
}

// CaptureDescription — описание записи без проверки владения: взятие
// стороннего туннеля забирает его запись осознанно. Только для Adopt.
func (o *OperatorOS5Impl) CaptureDescription(ctx context.Context, tunnelID, description string) error {
	return o.setDescription(ctx, tunnelID, "", description, true)
}

// setDescription пишет описание только существующей записи: та же RCI-форма на
// отсутствующей запись СОЗДАЁТ — без security-level, и Фаза 1 сочла бы её
// готовой; записи нет — её заведёт Фаза 1 с описанием = имени туннеля. Без
// capture запись обязана быть нашей по правилу гейта старта (recordIsOurs с
// ПРЕЖНИМ именем): иначе туннель, которому старт отказал на чужой записи,
// переименованием перезаписал бы её описание, и следующий старт взял бы
// чужую запись как свою (F517).
func (o *OperatorOS5Impl) setDescription(ctx context.Context, tunnelID, prevName, description string, capture bool) error {
	names := tunnel.NewNames(tunnelID)
	iface, rec, ok, err := confirmOpkgTun(ctx, o.queries, names.NDMSName)
	if err != nil {
		return tunnel.NewOpError("update_description", tunnelID, "ndms", fmt.Errorf("read OpkgTun record: %w", err))
	}
	if !ok {
		return nil
	}
	if !capture {
		// Пустое прежнее имя с описанием не сверяем: "" == "" признало бы
		// нашей любую запись без описания. Остаётся живое amneziawg.
		ours, _ := o.backend.IsRunning(ctx, names.IfaceName)
		if prevName != "" {
			ours = o.recordIsOurs(ctx, rec, prevName, names.IfaceName)
		}
		if !ours {
			return tunnel.NewOpError("update_description", tunnelID, "ndms",
				&ForeignRecordError{NDMSName: names.NDMSName, Description: rec.Description, Want: prevName})
		}
	}
	if err := o.commands.Interfaces.SetDescription(ctx, iface, description); err != nil {
		return tunnel.NewOpError("update_description", tunnelID, "ndms", err)
	}
	o.logInfo("update_description", tunnelID, fmt.Sprintf("Description updated to %q", description))
	return nil
}

// GetDefaultGatewayInterface returns the current default gateway interface name.
func (o *OperatorOS5Impl) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	return o.queries.Routes.GetDefaultGatewayInterface(ctx)
}

// rollbackStart cleans up after a failed start operation.
// justCreated indicates whether we created the OpkgTun in this Start attempt.
// When false (OpkgTun already existed), we preserve NDMS conf state (conf: running)
// so the tunnel stays in StateNeedsStart and can be retried.
func (o *OperatorOS5Impl) rollbackStart(ctx context.Context, tunnelID string, names tunnel.Names, iface query.Confirmed, justCreated bool) {
	o.logInfo("rollback", tunnelID, "Rolling back failed start")

	o.clearAppliedDNS(ctx, tunnelID, iface)
	_ = o.firewall.RemoveRules(ctx, names.IfaceName)
	if justCreated {
		// Запись создана этой попыткой — сносим её целиком, а не только
		// опускаем: иначе OpkgTunN остаётся на роутере после неудачного
		// первого старта (F560). Порядок как в Delete (F596/F598): ожидания
		// хуков (свой снос — не внешнее снятие), устройство, затем запись —
		// запись, снятая при живом opkgtunN, даёт C 0xcffd003b (стенд A7).
		o.expectHook(names.NDMSName, "disabled")
		o.expectHook(names.NDMSName, "destroyed")
		_ = o.backend.Stop(ctx, names.IfaceName)
		if err := o.commands.Interfaces.DeleteOpkgTun(ctx, iface); err != nil {
			o.logWarn("rollback", tunnelID, "DeleteOpkgTun: "+err.Error())
		}
		return
	}
	// Don't call InterfaceDown for existing OpkgTun — preserve conf: running.
	_ = o.backend.Stop(ctx, names.IfaceName)
}

// logInfo logs an info message via the UI-visible scoped logger.
func (o *OperatorOS5Impl) logInfo(action, target, message string) {
	o.appLog.Info(action, target, message)
}

// logWarn logs a warning message via the UI-visible scoped logger.
func (o *OperatorOS5Impl) logWarn(action, target, message string) {
	o.appLog.Warn(action, target, message)
}

// GetSystemName resolves an NDMS ID to its kernel interface name from
// InterfaceStore memory — без RCI (F570).
func (o *OperatorOS5Impl) GetSystemName(ctx context.Context, ndmsID string) string {
	return o.queries.Interfaces.ResolveSystemName(ctx, ndmsID)
}

// SetAppLogger sets the web UI logger.
// SetEndpointRouteSharing подключает проверку «host-route до этого IP держит
// другой туннель» поверх карты endpointRoutes. Нужна, потому что тот же
// host-route ставит обфусцированный nativewg-туннель (nwg.addObfHostRoute), а
// карта про чужой бэкенд ничего не знает.
func (o *OperatorOS5Impl) SetEndpointRouteSharing(fn func(excludeID, ip string) bool) {
	o.routeHeldByOther = fn
}

func (o *OperatorOS5Impl) SetAppLogger(logger logging.AppLogger) {
	o.appLog = logging.NewScopedLogger(logger, logging.GroupTunnel, logging.SubOps)
}

// Ensure OperatorOS5Impl implements Operator interface.
var _ Operator = (*OperatorOS5Impl)(nil)
