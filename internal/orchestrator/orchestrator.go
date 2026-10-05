package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Окна хуков (В6): стенд 01.10, KN-1810 5.01.C.6, 16 замеров опоздания
// хуков под churn 14–30 с, max ≈30 с; окно = max × 1,5. Внешняя грань
// conf=disabled внутри окна не теряется: поглощённая грань перепроверяется
// при истечении окна (recheckAbsorbedDisabled), остаток — задержка до 45 с.

// expectedHookTTL bounds how long a self-induced NDMS hook expectation
// stays valid. Past it, the token is pruned so a stale expectation can't
// absorb a later, legitimate external edge.
const expectedHookTTL = 45 * time.Second

// bootQuiescenceWindow is how long after we (re)start a tunnel we
// treat an incoming conf=disabled as transient NDMS settling rather than a
// stop command. See decideNDMSHook + updateState + recheckAbsorbedDisabled.
const bootQuiescenceWindow = 45 * time.Second

// absorbedRecheckTimeout — потолок фоновой перепроверки поглощённой грани:
// ожидание замка туннеля, проба NDMS и остановка.
const absorbedRecheckTimeout = 30 * time.Second

// confSettleDelay is how long an external conf=disabled edge is held before it
// is acted on, waiting to see whether NDMS bounces the interface back to
// conf=running. See settleConfDisabled.
const confSettleDelay = 5 * time.Second

// PingCheckExecutor is the interface for monitoring operations.
// Satisfied by *pingcheck.Facade.
type PingCheckExecutor interface {
	StartMonitoring(tunnelID, tunnelName string, skipConfigure ...bool)
	StopMonitoring(tunnelID string)
}

// DNSRouteExecutor is the interface for DNS route operations.
type DNSRouteExecutor interface {
	Reconcile(ctx context.Context) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error
}

// StaticRouteExecutor is the interface for static route operations.
type StaticRouteExecutor interface {
	OnTunnelStart(ctx context.Context, tunnelID, tunnelIface string) error
	OnTunnelStop(ctx context.Context, tunnelID string) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error
	Reconcile(ctx context.Context) error
}

// ClientRouteExecutor is the interface for client route operations.
type ClientRouteExecutor interface {
	OnTunnelStart(ctx context.Context, tunnelID string, kernelIface string) error
	OnTunnelStop(ctx context.Context, tunnelID string) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error
}

// NativeWGExecutor is the interface for NativeWG operations.
// Satisfied by *nwg.OperatorNativeWG.
type NativeWGExecutor interface {
	Start(ctx context.Context, stored *storage.AWGTunnel) error
	Stop(ctx context.Context, stored *storage.AWGTunnel) error
	Delete(ctx context.Context, stored *storage.AWGTunnel) error
	SuspendProxy(ctx context.Context, stored *storage.AWGTunnel) error
	RestoreKmodTunnel(ctx context.Context, stored *storage.AWGTunnel) error
	GetState(ctx context.Context, stored *storage.AWGTunnel) tunnel.StateInfo
	ResolveActiveWAN(ctx context.Context, stored *storage.AWGTunnel) string
	GetTrackedEndpointIP(tunnelID string) string
	ConfigurePingCheck(ctx context.Context, stored *storage.AWGTunnel, cfg ndms.PingCheckConfig) error
	RemovePingCheck(ctx context.Context, stored *storage.AWGTunnel) error
}

// Orchestrator centralizes ALL tunnel lifecycle decisions.
// One brain: receives events, decides actions, executes them.
type Orchestrator struct {
	// Decision state (protected by mu)
	mu    sync.Mutex
	state State

	// Per-tunnel execution locks
	tunnelMu sync.Map

	// tunnelLockOwner: tunnelID -> lockHolder, кто держит tunnelMu.
	tunnelLockOwner sync.Map

	// Expected NDMS hooks — queue of hooks our own actions will trigger.
	// Consumed in HandleEvent to filter self-triggered iflayerchanged events.
	expectedHooks []expectedHook

	// Executors (no decision logic, only execution)
	store    *storage.AWGTunnelStore
	kernelOp ops.Operator
	nwgOp    NativeWGExecutor
	stateMgr state.Manager
	wanModel *wan.Model

	// Downstream executors
	pingCheck   PingCheckExecutor
	dnsRoute    DNSRouteExecutor
	staticRoute StaticRouteExecutor
	clientRoute ClientRouteExecutor

	// baseCtx — контекст жизни демона. Нужен отложенному буту: тот приезжает
	// из горутины NDMS-хука, у которой свой 60-секундный дедлайн
	// (internal/api/hook.go), а бут на нескольких туннелях с медленным NDMS
	// в него не укладывается — обрывался бы посередине и без повтора.
	baseCtx context.Context

	// Event bus for SSE publishing
	bus *events.Bus

	// Logging
	appLog *logging.ScopedLogger

	// clock returns current time; injectable for tests. nil → time.Now.
	// Читается без o.mu: ставится в New (или тестом до первого события) и
	// дальше не меняется — в отличие от хуков, которые ставит проводка.
	clock func() time.Time

	// confSettleDelay overrides the package const; injectable for tests.
	confSettleDelay time.Duration

	// schedule откладывает вызов fn на d; nil → time.AfterFunc. Как clock —
	// ставится тестом до первого события и дальше не меняется.
	schedule func(d time.Duration, fn func())

	// confLayerRunning (пишется и читается под o.mu — у остальных Set*-полей
	// контракт слабее: они ставятся однократно в setupOrchestrator до приёма
	// событий и дальше не меняются) reads the
	// interface's CURRENT conf layer straight from
	// NDMS (fresh, not from the snapshot cache). Им перепроверяются обе грани:
	// conf=disabled перед остановкой и conf=running перед подъёмом.
	// Ошибка значит «не знаем» — грань остаётся в силе. nil → check skipped.
	confLayerRunning func(ctx context.Context, ndmsName string) (bool, error)

	// recordPresent (под o.mu, как confLayerRunning) — есть ли запись
	// ndmsName в СВЕЖЕМ полном списке NDMS. Им перепроверяется ifdestroyed
	// перед остановкой (F569, R31). nil → проверка пропущена.
	recordPresent func(ctx context.Context, ndmsName string) (bool, error)

	// removedByUs (под o.mu, как recordPresent) — запись ndmsName снята нашим
	// `no interface` (InterfaceStore.RemovedByUs, П20): её ifdestroyed — свой,
	// пробы и реакции нет. nil → каждое ifdestroyed идёт в пробу.
	removedByUs func(ndmsName string) bool

	// ifaceInvalidator, when set, refreshes the NDMS interface cache for a
	// kernel tunnel's NDMS name on its confirmed "running" transition (#328).
	// nil-safe. Production wires an async closure; the orchestrator calls it
	// synchronously so async-ness stays a wiring detail (and tests deterministic).
	ifaceInvalidator func(name string)

	// onTunnelRunning is an optional callback on confirmed running
	// transition. Nil-safe.
	onTunnelRunning func(tunnelID string)
}

// New creates a new Orchestrator.
func New(
	store *storage.AWGTunnelStore,
	kernelOp ops.Operator,
	nwgOp *nwg.OperatorNativeWG,
	stateMgr state.Manager,
	wanModel *wan.Model,
	appLogger logging.AppLogger,
) *Orchestrator {
	o := &Orchestrator{
		state:    newState(),
		store:    store,
		kernelOp: kernelOp,
		stateMgr: stateMgr,
		wanModel: wanModel,
		appLog:   logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubOrchestrator),
		clock:    time.Now,
	}
	// Оператор приходит конкретным типом: nil-указатель, положенный в
	// интерфейсное поле напрямую, дал бы «не-nil интерфейс» и превратил
	// защитные проверки o.nwgOp == nil в панику.
	if nwgOp != nil {
		o.nwgOp = nwgOp
	}
	return o
}

// SetPingCheck sets the monitoring executor.
func (o *Orchestrator) SetPingCheck(pc PingCheckExecutor) { o.pingCheck = pc }

// SetDNSRoute sets the DNS route executor.
func (o *Orchestrator) SetDNSRoute(dr DNSRouteExecutor) { o.dnsRoute = dr }

// SetStaticRoute sets the static route executor.
func (o *Orchestrator) SetStaticRoute(sr StaticRouteExecutor) { o.staticRoute = sr }

// SetClientRoute sets the client route executor.
func (o *Orchestrator) SetClientRoute(cr ClientRouteExecutor) { o.clientRoute = cr }

// SetEventBus sets the event bus for SSE publishing.
//
// Все три хука ниже (bus, ifaceInvalidator, onTunnelRunning) ЧИТАЮТСЯ из
// updateState под o.mu, поэтому и пишутся под ним же: асимметрия
// «write-unlocked / read-locked» — та же болезнь, что у пробы conf-слоя, и
// стоит она столько же, сколько лишний Lock на старте демона. F258.
func (o *Orchestrator) SetEventBus(bus *events.Bus) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bus = bus
}

// SetInterfaceInvalidator wires the NDMS interface-cache refresh invoked on a
// kernel tunnel's confirmed "running" transition. nil-safe. See issue #328.
func (o *Orchestrator) SetInterfaceInvalidator(fn func(name string)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ifaceInvalidator = fn
}

// SetOnTunnelRunning wires a callback invoked when any tunnel (kernel or
// NativeWG) reaches confirmed running state. Used to restart HydraRoute Neo
// so it re-applies CONNMARK rules.
func (o *Orchestrator) SetOnTunnelRunning(fn func(tunnelID string)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.onTunnelRunning = fn
}

// SetConfLayerProbe wires the fresh NDMS read of an interface's conf layer.
// Им перепроверяются ОБЕ грани: conf=disabled перед остановкой и conf=running
// перед подъёмом. nil-safe: без пробы обе верят хукам как есть.
func (o *Orchestrator) SetConfLayerProbe(fn func(ctx context.Context, ndmsName string) (bool, error)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.confLayerRunning = fn
}

// SetRecordPresenceProbe wires the fresh full-list check of an NDMS record
// (Interfaces.Confirm). nil-safe: без пробы ifdestroyed принимается как есть.
func (o *Orchestrator) SetRecordPresenceProbe(fn func(ctx context.Context, ndmsName string) (bool, error)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recordPresent = fn
}

// SetRemovedByUsProbe wires the store's «снято нами» (Interfaces.RemovedByUs).
// nil-safe: без него свой ifdestroyed проверяется списком, как чужой.
func (o *Orchestrator) SetRemovedByUsProbe(fn func(ndmsName string) bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.removedByUs = fn
}

// ifdestroyedOurs — ifdestroyed записи, снятой нашим `no interface` (П20).
func (o *Orchestrator) ifdestroyedOurs(ndmsName string) bool {
	o.mu.Lock()
	fn := o.removedByUs
	o.mu.Unlock()
	if fn == nil || !fn(ndmsName) {
		return false
	}
	o.appLog.Debug("ifdestroyed", ndmsName, "ifdestroyed: снято нами")
	return true
}

// SetSupportsASC sets the ASC support flag.
func (o *Orchestrator) SetSupportsASC(fn func() bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.state.supportsASC = fn()
}

// RefreshTunnelState re-reads a tunnel from storage and updates the
// orchestrator's in-memory cache without emitting any actions.
//
// Settings-only mutations (ping-check toggle, name change, ISP interface
// reassignment, etc.) happen directly against the store in the API
// layer. Without this refresh the decide layer keeps making decisions
// off a stale snapshot — e.g. seeing PingCheck.Enabled=true after the
// user disabled it, which produces spurious ActionRemovePingCheck on
// the next lifecycle event and triggers NDMS "interface has no
// assigned profile" warnings.
//
// Runtime-only fields (Running, Monitoring, quiescentUntil,
// lastConfRunningAt, absorbedDisabledAt, recheckScheduled, bornAt) live only in the orchestrator's cache, so they are preserved across
// the refresh — reloading them from storage would clobber the action
// layer's view of the world.
func (o *Orchestrator) RefreshTunnelState(tunnelID string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	stored, err := o.store.Get(tunnelID)
	if err != nil {
		return
	}
	fresh := tunnelStateFromStored(stored)
	if cur, ok := o.state.tunnels[tunnelID]; ok {
		fresh.Running = cur.Running
		fresh.Monitoring = cur.Monitoring
		fresh.quiescentUntil = cur.quiescentUntil
		fresh.lastConfRunningAt = cur.lastConfRunningAt
		fresh.absorbedDisabledAt = cur.absorbedDisabledAt
		fresh.recheckScheduled = cur.recheckScheduled
		fresh.bornAt = cur.bornAt
	} else {
		fresh.bornAt = o.nowFn()
	}
	o.state.tunnels[tunnelID] = fresh
}

// SetBaseContext задаёт контекст жизни демона для работ, которые нельзя
// исполнять под коротким контекстом вызывающего (отложенный бут).
func (o *Orchestrator) SetBaseContext(ctx context.Context) {
	o.mu.Lock()
	o.baseCtx = ctx
	o.mu.Unlock()
}

// LoadState populates the state cache from storage and live operator state.
// Called once at startup before handling any events.
func (o *Orchestrator) LoadState(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.state.loadFromStore(o.store)
	o.state.anyWANUpFn = o.wanModel.AnyUp

	// Detect running state for each tunnel
	for _, t := range o.state.tunnels {
		if t.Backend == "nativewg" && o.nwgOp != nil {
			stored, err := o.store.Get(t.ID)
			if err != nil {
				continue
			}
			info := o.nwgOp.GetState(ctx, stored)
			t.Running = info.State == tunnel.StateRunning || info.State == tunnel.StateStarting
		} else if t.Backend != "nativewg" {
			info := o.stateMgr.GetState(ctx, t.ID)
			t.Running = info.State == tunnel.StateRunning
		}

		if t.Running && t.PingCheck != nil && t.PingCheck.Enabled {
			t.Monitoring = true
		}
	}
}

// expectedHook represents an NDMS hook we expect from our own actions.
type expectedHook struct {
	ndmsName  string
	level     string
	at        time.Time
	expiresAt time.Time
}

// nowFn returns the current time, honouring an injected clock in tests.
func (o *Orchestrator) nowFn() time.Time {
	if o.clock != nil {
		return o.clock()
	}
	return time.Now()
}

// ExpectHook registers an expected NDMS hook (implements tunnel.HookNotifier).
// Called by operators before InterfaceUp/Down. The expectation expires after
// expectedHookTTL so a stale token cannot absorb an unrelated later edge.
func (o *Orchestrator) ExpectHook(ndmsName, level string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.nowFn()
	o.expectedHooks = append(o.expectedHooks, expectedHook{
		ndmsName:  ndmsName,
		level:     level,
		at:        now,
		expiresAt: now.Add(expectedHookTTL),
	})
}

// consumeExpectedHook checks if an NDMS hook matches a non-expired expected
// one. It first prunes expired expectations, then removes and returns true on
// the first matching live entry.
//
// Ожидание, зарегистрированное раньше появления туннеля с этим именем в
// кэше (bornAt), принадлежит прежнему воплощению имени: FreeIndex отдаёт
// новому туннелю тот же OpkgTunN, а хвост Delete/отказавшего Start живёт
// expectedHookTTL. Такое ожидание грань нового туннеля не поглощает — она
// идёт в settle/окно/П13, как внешняя (M1 финального ревью F595).
func (o *Orchestrator) consumeExpectedHook(ndmsName, level string) bool {
	now := o.nowFn()
	kept := o.expectedHooks[:0]
	for _, h := range o.expectedHooks {
		if !now.Before(h.expiresAt) {
			continue
		}
		kept = append(kept, h)
	}
	o.expectedHooks = kept

	var born time.Time
	if t := o.state.findByNDMSName(ndmsName); t != nil {
		born = t.bornAt
	}
	for i, h := range o.expectedHooks {
		if h.ndmsName == ndmsName && h.level == level && !h.at.Before(born) {
			o.expectedHooks = append(o.expectedHooks[:i], o.expectedHooks[i+1:]...)
			return true
		}
	}
	return false
}

// noteConfRunning records an external conf=running edge so a conf=disabled
// still settling can recognise it as an NDMS interface restart.
func (o *Orchestrator) noteConfRunning(ndmsName string, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// Только вперёд: штамп — момент прихода, а записи идут в порядке выхода
	// из ожиданий; старый running, отпущенный позже, не откатывает новый.
	if t := o.state.findByNDMSName(ndmsName); t != nil && at.After(t.lastConfRunningAt) {
		t.lastConfRunningAt = at
	}
}

// settleDelay — confSettleDelay с подменой из теста.
func (o *Orchestrator) settleDelay() time.Duration {
	if o.confSettleDelay > 0 {
		return o.confSettleDelay
	}
	return confSettleDelay
}

// settleConfDisabled reports whether an external conf=disabled edge should be
// acted on. It returns false when NDMS is merely restarting the interface —
// either because conf=running follows within confSettleDelay, or because NDMS
// itself still reports the interface enabled when asked directly.
//
// Issue #667: a ping-check profile with `interface restart` (which awg-manager
// itself configures) makes NDMS bounce the interface conf disabled→running in
// about two seconds after a few failed probes. decideNDMSHook took the
// disabled edge as user intent and ran a full stop — interface down, static
// and client routes torn down — while the conf=running that followed was
// swallowed because the stop had not finished yet and the tunnel still looked
// Running.
//
// Issue #669: that stop is terminal. It ends in ActionPersistStopped
// (Enabled=false), and nothing in the daemon periodically reconciles tunnels
// back to their desired state — the only automatic way up is another external
// conf=running, which cannot come from an interface we just took down. So a
// single missed edge costs the user the tunnel until they re-enable it by hand.
//
// Holding the edge for confSettleDelay costs a genuine disable a few seconds
// of lag and nothing else.
func (o *Orchestrator) settleConfDisabled(ctx context.Context, event Event) bool {
	o.mu.Lock()
	t := o.state.findByNDMSName(event.NDMSName)
	now := o.nowFn()
	// Unknown or already-stopped tunnel, or still inside the boot-quiescence
	// window: decide() ignores the edge anyway, so don't sit on it.
	if t == nil || !t.Running || now.Before(t.quiescentUntil) {
		o.mu.Unlock()
		return true
	}
	tunnelID := t.ID
	o.mu.Unlock()

	timer := time.NewTimer(o.settleDelay())
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return false // caller gave up — leave the tunnel alone
	}

	o.mu.Lock()
	t = o.state.tunnels[tunnelID]
	bounced := t != nil && t.lastConfRunningAt.After(now)
	probe := o.confLayerRunning
	o.mu.Unlock()
	if t == nil {
		return true
	}
	if bounced {
		o.appLog.Info("conf-settle", tunnelID,
			"conf=disabled сменился на conf=running — рестарт интерфейса в NDMS, туннель не останавливаем")
		return false
	}

	// No running edge seen — but the edge may never arrive: hook delivery is
	// fire-and-forget, and NDMS can take longer than the settle window to
	// bring the interface back. Ask NDMS what it actually holds (issue #669:
	// one lost edge left the tunnel stopped and Enabled=false, which nothing
	// in the daemon ever undoes). An unreadable NDMS leaves the edge in force.
	if probe == nil {
		return true
	}
	up, err := probe(ctx, event.NDMSName)
	if err != nil || !up {
		return true
	}
	o.appLog.Info("conf-settle", tunnelID,
		"NDMS держит интерфейс включённым — перезапуск в NDMS, туннель не останавливаем")
	return false
}

// handleIfDestroyed — ifdestroyed записи нашего работающего kernel-туннеля.
// Хук опаздывает (стенд — до ~7 с), и за это окно Restart/ColdStart мог уже
// пересоздать запись — остановка по устаревшему хуку молча сняла бы Enabled.
// Поэтому проверка свежим списком, решение и остановка идут ПОД per-tunnel
// замком — тем же, которым сериализованы Start/Restart/ColdStart: запись не
// может появиться между проверкой и Stop. Запись есть — хук устарел,
// игнорируем. Список не прочитан — тоже НЕ останавливаем (R31): остановка на
// неопределённости выключила бы туннель пользователя без его ведома; хуже,
// чем пропустить снятие, которое вскроет следующий Stop/Start (Stop снесёт
// устройство, старт пересоздаст запись). Чужие и остановленные туннели — без
// замка и без чтения списка: decide для них ничего не делает.
func (o *Orchestrator) handleIfDestroyed(ctx context.Context, event Event) error {
	o.mu.Lock()
	t := o.state.findByNDMSName(event.NDMSName)
	o.mu.Unlock()
	if t == nil || t.Backend != "kernel" || !t.Running {
		return nil
	}
	tunnelID := t.ID
	if err := o.lockTunnel(ctx, tunnelID, event.Type.String()); err != nil {
		return err
	}
	defer o.unlockTunnel(tunnelID)
	// Повторно после замка (N6): Delete держал его и снял запись (Forget)
	// уже после того, как хук прошёл проверку на входе HandleEvent.
	if o.ifdestroyedOurs(event.NDMSName) {
		return nil
	}

	o.mu.Lock()
	probe := o.recordPresent
	o.mu.Unlock()
	if probe != nil {
		present, err := probe(ctx, event.NDMSName)
		if err != nil {
			o.appLog.Warn("ifdestroyed", tunnelID,
				fmt.Sprintf("запись %s: список NDMS не прочитан (%v) — туннель не останавливаем", event.NDMSName, err))
			return nil
		}
		if present {
			o.appLog.Info("ifdestroyed", tunnelID,
				fmt.Sprintf("запись %s уже есть в NDMS — хук ifdestroyed устарел, игнорируем", event.NDMSName))
			return nil
		}
	}
	if event.Now.IsZero() {
		event.Now = o.nowFn()
	}
	// decide — под тем же замком: Running мог смениться, пока ждали замок.
	actions, _, _ := o.decideLocked(event)
	return o.executeActions(ctx, actions)
}

// settleConfRunning — зеркало settleConfDisabled для грани conf=running.
//
// NDMS переигрывает конфигурацию сам и шлёт conf=running по интерфейсам,
// которых мы не трогали: на стенде 5.01 пачка пришла через девять секунд
// после удаления СОСЕДНЕГО OpkgTun. Поднимать туннель по такой грани нельзя —
// decideNDMSHook сознательно не смотрит на Enabled (внешнее включение из
// веб-интерфейса роутера обязано работать, issue #183), и ActionPersistRunning
// вернёт Enabled=true: стор начнёт противоречить тому, что нажал пользователь.
//
// Отличает грани не время, а факт: спрашиваем NDMS, что он держит СЕЙЧАС.
// Держит up — включение настоящее. Держит down — грань уже неверна, её
// породила чужая операция. Непрочитанный NDMS оставляет грань в силе, как и в
// settleConfDisabled: лучше лишний старт, чем туннель, лежащий до ручного
// вмешательства (#669).
func (o *Orchestrator) settleConfRunning(ctx context.Context, event Event) bool {
	o.mu.Lock()
	t := o.state.findByNDMSName(event.NDMSName)
	var tunnelID string
	var running bool
	if t != nil {
		tunnelID, running = t.ID, t.Running
	}
	probe := o.confLayerRunning
	o.mu.Unlock()

	// Неизвестный или уже работающий туннель decide и так не тронет.
	if tunnelID == "" || running || probe == nil {
		return true
	}

	up, err := probe(ctx, event.NDMSName)
	if ctx.Err() != nil {
		// Вызывающий сдался — исполнять на мёртвом контексте нечего: действия
		// отвалятся посередине. Тот же выбор, что в settleConfDisabled.
		return false
	}
	if err != nil || up {
		return true
	}
	o.appLog.Info("conf-settle", tunnelID,
		"NDMS держит интерфейс выключенным — conf=running не от пользователя, туннель не поднимаем")
	return false
}

// awaitTunnelIdle blocks until nothing is executing for the tunnel behind
// ndmsName (or the wait gives up). An external conf=running that lands while
// our own stop is still running would otherwise be swallowed by decide's
// t.Running guard — and since that stop persists Enabled=false, no later
// event brings the tunnel back on its own (issue #669).
// Возвращает true, если ждать пришлось: по туннелю в этот момент шла НАША
// операция. Это важно для settleConfRunning — её вопрос «что NDMS держит
// сейчас» после нашей же остановки получает ответ «down», потому что
// InterfaceDown только что его туда и записал, а не потому что грань чужая.
func (o *Orchestrator) awaitTunnelIdle(ctx context.Context, ndmsName string) bool {
	o.mu.Lock()
	var tunnelID string
	if t := o.state.findByNDMSName(ndmsName); t != nil {
		tunnelID = t.ID
	}
	o.mu.Unlock()
	if tunnelID == "" {
		return false
	}
	if o.tryLockTunnel(tunnelID, "await-idle") {
		o.unlockTunnel(tunnelID)
		return false
	}
	if err := o.lockTunnel(ctx, tunnelID, "await-idle"); err == nil {
		o.unlockTunnel(tunnelID)
	}
	return true
}

// tryLockTunnel — неблокирующий lockTunnel: берёт замок, если он свободен
// прямо сейчас, и сообщает, получилось ли.
func (o *Orchestrator) tryLockTunnel(tunnelID, owner string) bool {
	semAny, _ := o.tunnelMu.LoadOrStore(tunnelID, make(chan struct{}, 1))
	sem := semAny.(chan struct{})
	select {
	case sem <- struct{}{}:
		o.tunnelLockOwner.Store(tunnelID, &lockHolder{owner: owner, since: time.Now()})
		return true
	default:
		return false
	}
}

// HandleEvent is the single entry point for ALL events.
// Decides what to do, then executes.
// decideLocked принимает решение под o.mu и сообщает, был ли это отложенный
// бут. Выделено из HandleEvent, чтобы диспетчеризацию можно было проверить
// без исполнителей: иначе единственным признаком подмены decideBoot на что-то
// другое остаётся паника на nil-исполнителе, а это не проверка.
func (o *Orchestrator) decideLocked(event Event) (actions []Action, deferredBoot bool, baseCtx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Ensure tunnel is in cache (covers tunnels created/imported after startup)
	if event.Tunnel != "" {
		o.state.ensureTunnel(event.Tunnel, o.store, o.nowFn())
	}
	// Отложенный бут: загрузка прошла без WAN, и первое WAN-событие обязано
	// отработать за неё. Пометку снимает сам decideBoot.
	//
	// WANUp берём из модели WAN, а НЕ из факта прихода EventWANUp. Хук шлёт
	// это событие для любого интерфейса с ipv4-слоем, кроме туннельных
	// (IsNonISPInterface отсеивает только их): подъём LAN-моста br0 или
	// L2TP-клиента запускал бы полный бут при мёртвом WAN — холодный старт
	// всех туннелей и глобальный sweep маршрутов в никуда. Модель знает
	// только интерфейсы с ролью WAN из NDMS. Не подтвердилось — decideBoot
	// оставит пометку, и бут дождётся настоящего WAN.
	if event.Type == EventWANUp && o.state.bootPending {
		return decideBoot(Event{Type: EventBoot, WANUp: o.state.anyWANUp(), Now: event.Now}, &o.state), true, o.baseCtx
	}
	return decide(event, &o.state), false, o.baseCtx
}

func (o *Orchestrator) HandleEvent(ctx context.Context, event Event) error {
	// Filter self-triggered NDMS hooks before decide.
	// Our operators register expected hooks before InterfaceUp/Down.
	if event.Type == EventNDMSHook {
		o.mu.Lock()
		consumed := o.consumeExpectedHook(event.NDMSName, event.Level)
		o.mu.Unlock()
		if consumed {
			o.appLog.Debug("boot-trace", event.NDMSName,
				fmt.Sprintf("expected-hook consumed level=%s", event.Level))
			return nil
		}
	}

	// Своё снятие записи (F569) — факт карты, не ожидание (П20).
	if event.Type == EventNDMSIfDestroyed {
		if o.ifdestroyedOurs(event.NDMSName) {
			return nil
		}
		return o.handleIfDestroyed(ctx, event)
	}

	if event.Type == EventNDMSHook && event.Layer == "conf" {
		// Момент прихода грани — до ожиданий awaitTunnelIdle/settle: штамп
		// conf=running, поставленный после них, обгонял бы грань disabled,
		// пришедшую ПОЗЖЕ running, и та сходила бы за «bounce».
		arrived := event.Now
		if arrived.IsZero() {
			arrived = o.nowFn()
		}
		switch event.Level {
		case "running":
			// Ждали своей же операции — спрашивать NDMS бесполезно: он
			// отдаст то, что мы сами только что записали. Грань идёт в decide
			// как до появления пробы, иначе вернётся #669: наш Stop
			// персистит Enabled=false, и поднять туннель больше нечему.
			if !o.awaitTunnelIdle(ctx, event.NDMSName) && !o.settleConfRunning(ctx, event) {
				return nil
			}
			// Штамп «видели внешний running» ставим только для грани, которая
			// устояла: по нему settleConfDisabled отличает перезапуск
			// интерфейса в NDMS от настоящего выключения, и опровергнутая
			// грань подавляла бы там законную остановку.
			o.noteConfRunning(event.NDMSName, arrived)
		case "disabled":
			if !o.settleConfDisabled(ctx, event) {
				return nil
			}
		}
	}

	if event.Now.IsZero() {
		event.Now = o.nowFn()
	}

	// Decide (under lock)
	actions, deferredBoot, baseCtx := o.decideLocked(event)
	execCtx := ctx
	if deferredBoot && baseCtx != nil {
		execCtx = baseCtx
	}
	// Один список интерфейсов на все подтверждения события, включая бут по
	// всем туннелям (F557): между действиями событий — заново.
	execCtx = query.WithActionList(execCtx)

	o.mu.Lock()
	// conf=disabled detail: тот же резолвер, что decideNDMSHook —
	// findByNDMSName(event.NDMSName), layer=="conf" (НЕ event.Tunnel).
	if event.Type == EventNDMSHook && event.Layer == "conf" && event.Level == "disabled" {
		if t := o.state.findByNDMSName(event.NDMSName); t != nil && t.Running {
			sinceStart := bootQuiescenceWindow - t.quiescentUntil.Sub(event.Now)
			windowLeft := t.quiescentUntil.Sub(event.Now)
			stop := false
			for _, a := range actions {
				if a.Type == ActionStopKernel || a.Type == ActionStopNativeWG {
					stop = true
				}
			}
			if stop {
				o.appLog.Warn("boot-trace", t.ID,
					fmt.Sprintf("conf=disabled OUTSIDE-WINDOW->STOP sinceStart=%s windowLeft=%s", sinceStart.Round(time.Second), windowLeft.Round(time.Second)))
			} else {
				o.appLog.Debug("boot-trace", t.ID,
					fmt.Sprintf("conf=disabled suppressed sinceStart=%s windowLeft=%s", sinceStart.Round(time.Second), windowLeft.Round(time.Second)))
				// П13: грань могла быть внешней — окно её только откладывает.
				t.absorbedDisabledAt = event.Now
				if !t.recheckScheduled {
					t.recheckScheduled = true
					tunnelID, ndmsName := t.ID, event.NDMSName
					o.scheduleFn(o.recheckDue(t).Sub(event.Now), func() { o.recheckAbsorbedDisabled(tunnelID, ndmsName) })
				}
			}
		}
	}
	o.mu.Unlock()

	if len(actions) == 0 {
		return nil
	}

	if deferredBoot {
		o.appLog.Info("startup", "",
			fmt.Sprintf("отложенный бут пошёл по WAN-up (%s), действий: %d", event.WANIface, len(actions)))
	}

	// Per-tunnel lock for execution
	tunnelID := event.Tunnel
	if tunnelID == "" {
		// Multi-tunnel events (Boot, Reconnect, WAN): group actions per
		// tunnel and run each group under that tunnel's lock so a concurrent
		// single-tunnel NDMS hook for the same tunnel cannot interleave a
		// Stop into the middle of our Start sequence (the boot kill race).
		return o.executeActionsGrouped(execCtx, actions, event.Type.String())
	}

	// Single-tunnel event: lock that tunnel. Bounded acquisition (issue
	// #426): if a previous operation wedged (dead endpoint, slow NDMS), an
	// unbounded mutex made every subsequent start/stop/replace request
	// queue forever — piling up stale actions that then executed one after
	// another and kept the tunnel wedged until the daemon was restarted.
	// Failing fast with ErrOperationInProgress gives the UI an honest,
	// retryable "операция уже выполняется" instead of a hung request.
	if err := o.lockTunnel(execCtx, tunnelID, event.Type.String()); err != nil {
		return err
	}
	defer o.unlockTunnel(tunnelID)
	return o.executeActions(execCtx, actions)
}

// scheduleFn — o.schedule или time.AfterFunc.
func (o *Orchestrator) scheduleFn(d time.Duration, fn func()) {
	if o.schedule != nil {
		o.schedule(d, fn)
		return
	}
	time.AfterFunc(d, fn)
}

// recheckDue — срок перепроверки поглощённой грани: конец окна, но не
// раньше выдержки settle от самой грани (M2 финального ревью F595). Грань в
// последние секунды окна без выдержки пробовалась бы посреди рестарта
// интерфейса (#667: disabled→running за ~2 с) — ложная остановка, которой
// у внешней грани вне окна нет (settleConfDisabled). Вызывать под o.mu.
func (o *Orchestrator) recheckDue(t *tunnelState) time.Time {
	due := t.quiescentUntil
	if settled := t.absorbedDisabledAt.Add(o.settleDelay()); settled.After(due) {
		due = settled
	}
	return due
}

// recheckAbsorbedDisabled — единственная перепроверка грани conf=disabled,
// поглощённой окном quiescence (П13, В6). Окно гасит дрожание NDMS после
// нашего же подъёма, но ровно так же глотало бы и настоящее внешнее
// выключение в первые 45 с — туннель остался бы Running при выключенном
// интерфейсе. Поэтому при истечении окна: conf=running после грани — NDMS
// вернул интерфейс сам, 0 RCI; иначе одна проба. down — остановка как от
// внешней грани (Q1: Enabled=false); ошибка пробы — не останавливаем (R31).
//
// Всё под замком туннеля, действия — executeActions: замок уже взят,
// executeActionsGrouped взял бы тот же семафор второй раз и через
// tunnelLockTimeout упал бы ErrOperationInProgress (L1′(b)).
func (o *Orchestrator) recheckAbsorbedDisabled(tunnelID, ndmsName string) {
	// Контекст жизни демона, как у отложенного бута: на выходе демона
	// перепроверка не начнёт остановку. Без него (тесты, демон до
	// SetBaseContext) — context.Background().
	o.mu.Lock()
	base := o.baseCtx
	o.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, absorbedRecheckTimeout)
	defer cancel()
	if err := o.lockTunnel(ctx, tunnelID, "recheck-absorbed-disabled"); err != nil {
		// Метка остаётся, переноса нет (L1′(c)). Подъём/остановка под этим
		// замком метку сбросят или сделают ненужной, но замок держат и
		// владельцы, которые её не трогают (service.Update, endpoint-страж
		// nwg): держат дольше tunnelLockTimeout — поглощённая грань потеряна
		// до следующей (остаток В6 в трекере). Таймера больше нет — флаг
		// снимаем, чтобы он не врал.
		o.mu.Lock()
		if t := o.state.tunnels[tunnelID]; t != nil {
			t.recheckScheduled = false
		}
		o.mu.Unlock()
		o.appLog.Warn("conf-recheck", tunnelID, fmt.Sprintf("перепроверка грани: туннель занят (%v)", err))
		return
	}
	defer o.unlockTunnel(tunnelID)

	now := o.nowFn()
	o.mu.Lock()
	t := o.state.tunnels[tunnelID]
	if t == nil {
		o.mu.Unlock()
		return
	}
	t.recheckScheduled = false
	if !t.Running || t.absorbedDisabledAt.IsZero() {
		o.mu.Unlock()
		return
	}
	if due := o.recheckDue(t); now.Before(due) {
		// Окно продлено (повторный подъём/reconcile) или грань моложе
		// выдержки settle — решать на сроке.
		t.recheckScheduled = true
		o.scheduleFn(due.Sub(now), func() { o.recheckAbsorbedDisabled(tunnelID, ndmsName) })
		o.mu.Unlock()
		return
	}
	if t.lastConfRunningAt.After(t.absorbedDisabledAt) {
		t.absorbedDisabledAt = time.Time{}
		o.mu.Unlock()
		o.appLog.Info("conf-recheck", tunnelID,
			"поглощённая грань conf=disabled сменилась на conf=running — туннель не останавливаем")
		return
	}
	probe := o.confLayerRunning
	o.mu.Unlock()
	if probe == nil {
		return
	}

	up, err := probe(ctx, ndmsName)
	if err != nil {
		o.appLog.Warn("conf-recheck", tunnelID,
			fmt.Sprintf("поглощённая грань conf=disabled: NDMS не прочитан (%v) — туннель не останавливаем", err))
		return
	}
	if !up {
		actions, _, _ := o.decideLocked(Event{Type: EventNDMSHook, NDMSName: ndmsName, Layer: "conf", Level: "disabled", Now: now})
		if err := o.executeActions(query.WithActionList(ctx), actions); err != nil {
			// Как ошибка пробы: повтора нет, метка снимается. Остаток В6 —
			// туннель мог остаться Running при выключенном в NDMS интерфейсе
			// до следующей грани или действия пользователя.
			o.appLog.Warn("conf-recheck", tunnelID,
				fmt.Sprintf("поглощённая грань conf=disabled подтверждена NDMS, остановка не удалась: %v", err))
		} else {
			o.appLog.Info("conf-recheck", tunnelID, "поглощённая грань conf=disabled подтверждена NDMS — остановка")
		}
	} else {
		o.appLog.Info("conf-recheck", tunnelID,
			"поглощённая грань conf=disabled: NDMS держит интерфейс включённым — туннель не останавливаем")
	}
	o.mu.Lock()
	if t := o.state.tunnels[tunnelID]; t != nil {
		t.absorbedDisabledAt = time.Time{}
	}
	o.mu.Unlock()
}

// tunnelLockTimeout bounds how long a caller waits for a busy tunnel's
// execution lock before giving up with ErrOperationInProgress. Long enough
// to ride out a normal start/stop sequence ahead in the queue, short enough
// that the HTTP caller gets an answer instead of a hung request.
const tunnelLockTimeout = 15 * time.Second

// lockHolder records who took a tunnel's execution lock and when. Issue
// #795: a wedged tunnel rejected every UI action for minutes and the log
// named neither the holder nor how long it had been holding, so the app
// log alone could not tell a stuck operation from a slow one.
//
// refused считает, скольким вызывающим этот держатель отказал. По нему
// решается уровень строки освобождения: долгое держание само по себе
// штатно (boot/reconnect на медленном NDMS), а вот долгое держание,
// которому кто-то упёрся, — та самая улика.
type lockHolder struct {
	owner   string
	since   time.Time
	refused atomic.Int32
}

// WithTunnelLock выполняет fn под тем же per-tunnel замком, которым
// оркестратор сериализует свои действия. Нужен владельцам, которые правят
// живой туннель в обход событий (service.Update) и стражу endpoint'ов в
// nwg: без замка их работа переплетается с WAN-up по тому же туннелю.
func (o *Orchestrator) WithTunnelLock(ctx context.Context, tunnelID, owner string, fn func() error) error {
	if err := o.lockTunnel(ctx, tunnelID, owner); err != nil {
		return err
	}
	defer o.unlockTunnel(tunnelID)
	return fn()
}

// lockTunnel acquires the per-tunnel execution semaphore. Gives up when ctx
// is cancelled (client disconnected) or after tunnelLockTimeout. owner names
// the operation for the log — it is what identifies the holder when a later
// caller is refused.
func (o *Orchestrator) lockTunnel(ctx context.Context, tunnelID, owner string) error {
	semAny, _ := o.tunnelMu.LoadOrStore(tunnelID, make(chan struct{}, 1))
	sem := semAny.(chan struct{})
	timer := time.NewTimer(tunnelLockTimeout)
	defer timer.Stop()
	select {
	case sem <- struct{}{}:
		o.tunnelLockOwner.Store(tunnelID, &lockHolder{owner: owner, since: time.Now()})
		o.appLog.Debug("tunnel-lock", tunnelID, "взят: "+owner)
		return nil
	case <-ctx.Done():
		return o.lockBusyErr(tunnelID, owner, "контекст вызывающего отменён")
	case <-timer.C:
		return o.lockBusyErr(tunnelID, owner, "таймаут ожидания")
	}
}

// lockBusyErr logs why the caller was refused and who holds the lock right
// now, then returns the retryable error the HTTP layer turns into 409.
//
// Длительность держания сюда НЕ пишется: журнал сворачивает повторы по
// точному совпадению текста (logging.CoalesceOrAdd), а растущее число
// секунд делает каждую строку уникальной — залипший туннель залил бы
// журнал несворачиваемыми Warn каждые tunnelLockTimeout. Сколько держали
// на самом деле, говорит строка освобождения в unlockTunnel.
//
// «сейчас» в тексте — не оговорка: держатель мог смениться, пока мы ждали.
// Врать в улике хуже, чем назвать её приблизительной.
func (o *Orchestrator) lockBusyErr(tunnelID, owner, reason string) error {
	if hAny, ok := o.tunnelLockOwner.Load(tunnelID); ok {
		h := hAny.(*lockHolder)
		h.refused.Add(1)
		o.appLog.Warn("tunnel-lock", tunnelID,
			fmt.Sprintf("отказано %s (%s): сейчас держит %s", owner, reason, h.owner))
	} else {
		// Держателя нет — либо замок и правда свободен (ветки select
		// равноправны, при отменённом ctx выбор мог пасть на ctx.Done()),
		// либо держатель уже стёр свою запись и вот-вот отпустит.
		// Warn про занятость здесь соврал бы.
		o.appLog.Debug("tunnel-lock", tunnelID,
			fmt.Sprintf("отказано %s (%s): держателя нет — освободился или вот-вот отпустит", owner, reason))
	}
	return fmt.Errorf("%w (%s)", tunnel.ErrOperationInProgress, tunnelID)
}

// unlockTunnel releases the per-tunnel execution semaphore.
//
// Запись в tunnelMu намеренно не удаляется даже для удалённого туннеля:
// удалять её мог только сам держатель, и тогда конкурент успевал создать
// новый канал, а отложенный unlock сливал ЧУЖОЙ токен — взаимоисключение
// ломалось. Цена отказа от очистки — один пустой канал на когда-либо
// существовавший ID туннеля (пул номеров OpkgTun конечен — см.
// opkgtun.Ceiling).
func (o *Orchestrator) unlockTunnel(tunnelID string) {
	if hAny, ok := o.tunnelLockOwner.LoadAndDelete(tunnelID); ok {
		h := hAny.(*lockHolder)
		held := time.Since(h.since)
		// Warn только если держание кому-то реально помешало. Долгое
		// держание само по себе штатно: boot/reconnect на медленном NDMS
		// переваливает за tunnelLockTimeout каждый ребут.
		if refused := h.refused.Load(); refused > 0 {
			o.appLog.Warn("tunnel-lock", tunnelID,
				fmt.Sprintf("освобождён: %s держал %s, отказано попыткам: %d",
					h.owner, held.Round(time.Second), refused))
		} else {
			o.appLog.Debug("tunnel-lock", tunnelID,
				fmt.Sprintf("освобождён: %s держал %s", h.owner, held.Round(time.Millisecond)))
		}
	}
	if semAny, ok := o.tunnelMu.Load(tunnelID); ok {
		select {
		case <-semAny.(chan struct{}):
		default: // already released — no-op
		}
	}
}

// executeActions executes a list of actions sequentially.
// Updates state cache after each successful action.
func (o *Orchestrator) executeActions(ctx context.Context, actions []Action) error {
	var firstErr error
	// Туннели, чей Start в этом прогоне отказал: их хвост (маршруты на
	// WireguardN/OpkgTunN, ping-check, PersistRunning) не исполняется — он
	// адресовал бы команды интерфейсу, которого нет (E в журнале ndm), и
	// записал бы Enabled/StartedAt не поднятому туннелю (F546, решение 3).
	failedStart := map[string]bool{}
	for _, action := range actions {
		if failedStart[action.Tunnel] {
			continue
		}
		// Abandoned caller (client disconnected / request deadline) — stop
		// BETWEEN actions, never mid-action, so each executed step is whole.
		// Any partially-applied sequence is healed by the reconcile loop;
		// grinding through the rest of a stale queue while holding the
		// tunnel lock is what wedged the UI in issue #426.
		if err := ctx.Err(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		if err := o.executeOne(ctx, action); err != nil {
			o.appLog.Warn("execute-action", action.Tunnel, fmt.Sprintf("action type %d failed: %s", action.Type, err.Error()))
			if firstErr == nil {
				firstErr = err
			}
			if action.Type == ActionStartNativeWG || action.Type == ActionColdStartKernel {
				failedStart[action.Tunnel] = true
			}
			// Continue for boot/reconnect (best-effort), stop for user actions
			// TODO: refine error strategy in Phase 2 execute implementation
			continue
		}
		o.updateState(action)
	}
	return firstErr
}

// groupContiguousByTunnel splits a flat action list into contiguous runs
// sharing the same Tunnel value, preserving order. Boot/Reconnect/WANUp emit
// each tunnel's actions contiguously, so those events are fully serialized
// per tunnel. decideWANDown's non-ASC immediate-failover can emit a tunnel's
// Suspend and failover-Start in separate phases (non-contiguous) → that
// tunnel gets two groups and its lock is taken twice with a gap between;
// still deadlock-free and correct in execution order, just not gap-free
// against a concurrent hook. Tightening decideWANDown's ordering is tracked
// separately (out of scope for the boot-race fix).
func groupContiguousByTunnel(actions []Action) [][]Action {
	var groups [][]Action
	i := 0
	for i < len(actions) {
		tid := actions[i].Tunnel
		j := i
		for j < len(actions) && actions[j].Tunnel == tid {
			j++
		}
		groups = append(groups, actions[i:j])
		i = j
	}
	return groups
}

// executeActionsGrouped runs a multi-tunnel action list with per-tunnel
// serialization. Each tunnel's contiguous group runs under that tunnel's
// per-tunnel lock, acquired and released per group — never holding two
// tunnel locks at once, so there is no lock-ordering deadlock against
// concurrent single-tunnel hook events. Tunnel-less groups (Tunnel=="")
// run unlocked.
func (o *Orchestrator) executeActionsGrouped(ctx context.Context, actions []Action, owner string) error {
	var firstErr error
	for _, group := range groupContiguousByTunnel(actions) {
		if err := o.executeGroup(ctx, group, owner); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// executeGroup runs one same-tunnel action group. The per-tunnel lock is
// released via defer (iteration-scoped here, matching the single-tunnel
// path in HandleEvent) so a panic in executeActions cannot leak the lock.
func (o *Orchestrator) executeGroup(ctx context.Context, group []Action, owner string) error {
	tid := group[0].Tunnel
	if tid == "" {
		return o.executeActions(ctx, group)
	}
	// Bounded like the single-tunnel path: a wedged tunnel skips its group
	// (logged via firstErr) instead of stalling the whole boot/reconnect
	// sweep behind one dead endpoint.
	if err := o.lockTunnel(ctx, tid, owner); err != nil {
		return err
	}
	defer o.unlockTunnel(tid)
	return o.executeActions(ctx, group)
}

// executeOne is implemented in execute.go.

// updateState updates the internal state cache after a successful action.
func (o *Orchestrator) updateState(action Action) {
	o.mu.Lock()
	defer o.mu.Unlock()

	t := o.state.tunnels[action.Tunnel]
	if t == nil {
		return
	}

	switch action.Type {
	case ActionColdStartKernel, ActionStartNativeWG, ActionReconcileNativeWG, ActionReconcileKernel, ActionResumeKernel:
		t.Running = true
		t.quiescentUntil = o.nowFn().Add(bootQuiescenceWindow)
		// Новое окно — грань прошлого подъёма к нему не относится (L1′(d)).
		t.absorbedDisabledAt = time.Time{}
		t.recheckScheduled = false
		o.appLog.Debug("boot-trace", t.ID, fmt.Sprintf("tunnel-start action=%d", action.Type))
		// Refresh ActiveWAN from store. Execute layer persists the resolved
		// WAN; we mirror it into the in-memory cache so decideWANDown can
		// match correctly via affectedByWANDown.
		if stored, err := o.store.Get(action.Tunnel); err == nil {
			t.ActiveWAN = stored.ActiveWAN
		}
	case ActionStopKernel, ActionStopNativeWG:
		t.Running = false
		t.Monitoring = false
		t.ActiveWAN = ""
		o.appLog.Debug("boot-trace", t.ID, fmt.Sprintf("tunnel-stop action=%d", action.Type))
	case ActionSuspendProxy, ActionSuspendKernel:
		// Keep t.Running=true so the next WANUp picks Resume/Reconcile,
		// not a fresh ColdStart. Keep ActiveWAN so a duplicate WANDown
		// for the same iface does not re-trigger failover.
	case ActionStartMonitoring:
		t.Monitoring = true
	case ActionStopMonitoring:
		t.Monitoring = false
	case ActionDeleteKernel, ActionDeleteNativeWG:
		delete(o.state.tunnels, action.Tunnel)
	}

	// Publish SSE event
	if o.bus != nil {
		switch action.Type {
		case ActionColdStartKernel, ActionStartNativeWG, ActionReconcileNativeWG, ActionReconcileKernel, ActionResumeKernel:
			// tunnel:state is still consumed internally by
			// connectivity.Monitor (listens for "running" to trigger an
			// immediate check). Keep it until that dependency is
			// migrated. Frontend no longer listens.
			o.bus.Publish("tunnel:state", events.TunnelStateEvent{
				ID: t.ID, Name: t.Name, State: "running", Backend: t.Backend,
			})
			o.bus.PublishInvalidated(events.ResourceTunnels, "state-running")
			// Kernel tunnels: NDMS iflayerchanged hooks are unreliable for
			// OpkgTun, so the cache invalidate done at InterfaceUp can snapshot
			// a pre-"running" layer and then never get corrected — leaving
			// List* readers (policies/WAN/all) with a frozen "down" (#328).
			// Mark the cache dirty now that the start sequence is complete and
			// the layer has had time to settle: the next List*/Get/snapshot
			// reader takes one fresh list (F546). nwg marks it itself after its
			// batches (postIfaceBatch), so skip it.
			if o.ifaceInvalidator != nil && t.Backend == "kernel" {
				if ndmsName := tunnel.NewNames(t.ID).NDMSName; ndmsName != "" {
					o.ifaceInvalidator(ndmsName)
				}
			}
			if o.onTunnelRunning != nil {
				o.onTunnelRunning(t.ID)
			}
		case ActionStopKernel, ActionStopNativeWG, ActionSuspendProxy, ActionSuspendKernel:
			o.bus.Publish("tunnel:state", events.TunnelStateEvent{
				ID: t.ID, Name: t.Name, State: "stopped", Backend: t.Backend,
			})
			o.bus.PublishInvalidated(events.ResourceTunnels, "state-stopped")
		case ActionDeleteKernel, ActionDeleteNativeWG:
			// tunnel:deleted remains as a no-op SSE for any legacy
			// subscriber; the frontend handler is removed so nobody
			// reacts. Future cleanup can drop this publish.
			o.bus.Publish("tunnel:deleted", events.TunnelDeletedEvent{ID: action.Tunnel})
			o.bus.PublishInvalidated(events.ResourceTunnels, "deleted")
		}
	}
}

// QuiescentUntil returns the tunnel's current boot-quiescence deadline (the
// time until which a just-(re)started tunnel is considered "coming up"), or
// the zero time if the tunnel is unknown or no bring-up was attempted this
// session. Pure read — the API status layer uses it to display
// "pending/starting" instead of "broken" while a NativeWG tunnel is still
// being brought up. Does not mutate any state.
func (o *Orchestrator) QuiescentUntil(tunnelID string) time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	if t, ok := o.state.tunnels[tunnelID]; ok {
		return t.quiescentUntil
	}
	return time.Time{}
}
