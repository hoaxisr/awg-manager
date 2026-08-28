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
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// expectedHookTTL bounds how long a self-induced NDMS hook expectation
// stays valid. Past it, the token is pruned so a stale expectation can't
// absorb a later, legitimate external edge.
const expectedHookTTL = 15 * time.Second

// bootQuiescenceWindow is how long after we (re)start a NativeWG tunnel we
// treat an incoming conf=disabled as transient NDMS settling rather than a
// stop command. See decideNDMSHook + updateState.
const bootQuiescenceWindow = 20 * time.Second

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

	// Event bus for SSE publishing
	bus *events.Bus

	// Logging
	appLog *logging.ScopedLogger

	// clock returns current time; injectable for tests. nil → time.Now.
	clock func() time.Time

	// confSettleDelay overrides the package const; injectable for tests.
	confSettleDelay time.Duration

	// confLayerRunning reads the interface's CURRENT conf layer straight from
	// NDMS (fresh, not from the snapshot cache). Used to second-guess a
	// conf=disabled edge before acting on it. nil → check skipped.
	confLayerRunning func(ctx context.Context, ndmsName string) (bool, error)

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
func (o *Orchestrator) SetEventBus(bus *events.Bus) { o.bus = bus }

// SetInterfaceInvalidator wires the NDMS interface-cache refresh invoked on a
// kernel tunnel's confirmed "running" transition. nil-safe. See issue #328.
func (o *Orchestrator) SetInterfaceInvalidator(fn func(name string)) { o.ifaceInvalidator = fn }

// SetOnTunnelRunning wires a callback invoked when any tunnel (kernel or
// NativeWG) reaches confirmed running state. Used to restart HydraRoute Neo
// so it re-applies CONNMARK rules.
func (o *Orchestrator) SetOnTunnelRunning(fn func(tunnelID string)) { o.onTunnelRunning = fn }

// SetConfLayerProbe wires the fresh NDMS read of an interface's conf layer.
// nil-safe: without it, settleConfDisabled falls back to the hook edges alone.
func (o *Orchestrator) SetConfLayerProbe(fn func(ctx context.Context, ndmsName string) (bool, error)) {
	o.confLayerRunning = fn
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
// Runtime-only fields (Running, Monitoring, quiescentUntil)
// live only in the orchestrator's cache, so they are preserved across
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
	}
	o.state.tunnels[tunnelID] = fresh
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
	o.expectedHooks = append(o.expectedHooks, expectedHook{
		ndmsName:  ndmsName,
		level:     level,
		expiresAt: o.nowFn().Add(expectedHookTTL),
	})
}

// consumeExpectedHook checks if an NDMS hook matches a non-expired expected
// one. It first prunes expired expectations, then removes and returns true on
// the first matching live entry.
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

	for i, h := range o.expectedHooks {
		if h.ndmsName == ndmsName && h.level == level {
			o.expectedHooks = append(o.expectedHooks[:i], o.expectedHooks[i+1:]...)
			return true
		}
	}
	return false
}

// noteConfRunning records an external conf=running edge so a conf=disabled
// still settling can recognise it as an NDMS interface restart.
func (o *Orchestrator) noteConfRunning(ndmsName string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if t := o.state.findByNDMSName(ndmsName); t != nil {
		t.lastConfRunningAt = o.nowFn()
	}
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

	delay := o.confSettleDelay
	if delay <= 0 {
		delay = confSettleDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return false // caller gave up — leave the tunnel alone
	}

	o.mu.Lock()
	t = o.state.tunnels[tunnelID]
	bounced := t != nil && t.lastConfRunningAt.After(now)
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
	if o.confLayerRunning == nil {
		return true
	}
	up, err := o.confLayerRunning(ctx, event.NDMSName)
	if err != nil || !up {
		return true
	}
	o.appLog.Info("conf-settle", tunnelID,
		"NDMS держит интерфейс включённым — перезапуск в NDMS, туннель не останавливаем")
	return false
}

// awaitTunnelIdle blocks until nothing is executing for the tunnel behind
// ndmsName (or the wait gives up). An external conf=running that lands while
// our own stop is still running would otherwise be swallowed by decide's
// t.Running guard — and since that stop persists Enabled=false, no later
// event brings the tunnel back on its own (issue #669).
func (o *Orchestrator) awaitTunnelIdle(ctx context.Context, ndmsName string) {
	o.mu.Lock()
	var tunnelID string
	if t := o.state.findByNDMSName(ndmsName); t != nil {
		tunnelID = t.ID
	}
	o.mu.Unlock()
	if tunnelID == "" {
		return
	}
	if err := o.lockTunnel(ctx, tunnelID, "await-idle"); err == nil {
		o.unlockTunnel(tunnelID)
	}
}

// HandleEvent is the single entry point for ALL events.
// Decides what to do, then executes.
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

	if event.Type == EventNDMSHook && event.Layer == "conf" {
		switch event.Level {
		case "running":
			o.noteConfRunning(event.NDMSName)
			o.awaitTunnelIdle(ctx, event.NDMSName)
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
	o.mu.Lock()
	// Ensure tunnel is in cache (covers tunnels created/imported after startup)
	if event.Tunnel != "" {
		o.state.ensureTunnel(event.Tunnel, o.store)
	}
	actions := decide(event, &o.state)
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
			}
		}
	}
	o.mu.Unlock()

	if len(actions) == 0 {
		return nil
	}

	// Per-tunnel lock for execution
	tunnelID := event.Tunnel
	if tunnelID == "" {
		// Multi-tunnel events (Boot, Reconnect, WAN): group actions per
		// tunnel and run each group under that tunnel's lock so a concurrent
		// single-tunnel NDMS hook for the same tunnel cannot interleave a
		// Stop into the middle of our Start sequence (the boot kill race).
		return o.executeActionsGrouped(ctx, actions, event.Type.String())
	}

	// Single-tunnel event: lock that tunnel. Bounded acquisition (issue
	// #426): if a previous operation wedged (dead endpoint, slow NDMS), an
	// unbounded mutex made every subsequent start/stop/replace request
	// queue forever — piling up stale actions that then executed one after
	// another and kept the tunnel wedged until the daemon was restarted.
	// Failing fast with ErrOperationInProgress gives the UI an honest,
	// retryable "операция уже выполняется" instead of a hung request.
	if err := o.lockTunnel(ctx, tunnelID, event.Type.String()); err != nil {
		return err
	}
	defer o.unlockTunnel(tunnelID)
	return o.executeActions(ctx, actions)
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
// существовавший ID туннеля (диапазоны awg10..16 и awg20+ конечны).
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
	for _, action := range actions {
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
			publishInvalidatedBus(o.bus, "tunnels", "state-running")
			// Kernel tunnels: NDMS iflayerchanged hooks are unreliable for
			// OpkgTun, so the cache invalidate done at InterfaceUp can snapshot
			// a pre-"running" layer and then never get corrected — leaving
			// List* readers (policies/WAN/all) with a frozen "down" (#328).
			// Re-refresh now that the start sequence is fully complete and the
			// layer has had time to settle. nwg self-invalidates on its own
			// path (and uses a different NDMS name), so skip it.
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
			publishInvalidatedBus(o.bus, "tunnels", "state-stopped")
		case ActionDeleteKernel, ActionDeleteNativeWG:
			// tunnel:deleted remains as a no-op SSE for any legacy
			// subscriber; the frontend handler is removed so nobody
			// reacts. Future cleanup can drop this publish.
			o.bus.Publish("tunnel:deleted", events.TunnelDeletedEvent{ID: action.Tunnel})
			publishInvalidatedBus(o.bus, "tunnels", "deleted")
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

// publishInvalidatedBus posts a resource:invalidated hint. Duplicated
// here (from internal/api.publishInvalidated) to avoid an import cycle
// between the orchestrator and the api package.
//
// TODO(tech-debt): consolidate publishInvalidatedBus helpers into
// internal/events once the import-cycle with internal/api is resolved.
// Currently duplicated in internal/orchestrator and internal/pingcheck
// because those packages cannot import internal/api.
func publishInvalidatedBus(bus *events.Bus, resource, reason string) {
	if bus == nil {
		return
	}
	bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{
		Resource: resource,
		Reason:   reason,
	})
}
