// Engine wires capture evidence, coverage, probes and the rule writer into
// one decision pipeline with a bounded queue, active-check deduplication and
// generation guards. The dependency contracts live in types.go.

package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/capture"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probe"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/store"
)

// Operational bounds. The queue drops instead of growing: after an outage a
// backlog would only contain stale observations, and a paused capture must
// not accumulate deferred work.
const (
	queueCapacity = 64
	workerCount   = 2
)

// captureOpMu сериализует блокирующие capture-операции (Pause/Start из
// монитора, recovery, Stop при смене UDP-режима) между собой: без него
// запоздалый Pause одного горуты может убить свежее поколение, только что
// запущенное Start другого. Capture (Controller) сам сериализует их через
// opMu, но здесь нужна перепроверка решения после снятия локов движка.
// TryLock-подход: горута, чьё решение устарело, отступает, а не ждёт.
var captureOpMu sync.Mutex

// monitorInterval is the periodic WAN check period. It is a variable so
// tests can shorten it; production always uses thirty seconds.
var monitorInterval = 30 * time.Second

// wanRetryInterval is the fast retry delay inside a failure series: the
// first two failures re-check after ~1s instead of waiting a full period.
// A variable so tests can shorten it.
var wanRetryInterval = 1 * time.Second

// wanFailThreshold is the number of consecutive failed WAN checks that
// pauses the capture. Two failed checks in a row keep the capture running.
const wanFailThreshold = 3

// Errors reported by user actions and gates.
var (
	ErrStopped      = errors.New("engine is stopped")
	ErrNotFound     = errors.New("candidate not found")
	ErrNoTunnel     = errors.New("no tunnel interface is available")
	ErrInvalidState = errors.New("candidate state does not allow this action")
	ErrAddLimit     = errors.New("rolling hourly addition limit reached")
	ErrNoWriter     = errors.New("AWGM rule writer is unavailable")
)

// attempt is one in-flight decision. It stays authoritative only while its
// context is alive and its generation is current.
type attempt struct {
	id         string
	ctx        context.Context
	cancel     context.CancelFunc
	generation uint64
}

func (a *attempt) valid(e *Engine) bool {
	return a.ctx.Err() == nil && e.gen.Load() == a.generation
}

// Engine owns the queue, the worker pool, the WAN monitor and the capture.
type Engine struct {
	deps Dependencies

	baseCtx    context.Context
	baseCancel context.CancelFunc
	startCtx   context.Context // родительский контекст Start: старт по wizard из PatchConfig
	stopCh     chan struct{}
	queue      chan capture.Suspect

	gen     atomic.Uint64
	corr    atomic.Uint64
	dropped atomic.Uint64

	mu              sync.Mutex
	running         bool
	active          map[string]*attempt
	queued          map[string]struct{}
	capture         Capture
	captureGen      uint64 // поколение capture: растёт при пересоздании, гасит устаревшие pause/resume
	captureRunning  bool
	capturePaused   bool
	wanFailures     int // подряд идущие неудачные WAN-проверки, под commitMu+mu
	lastWAN         model.CheckEvidence
	lastError       string
	monitorCancel   context.CancelFunc
	tunnelInterface string
	tunnelOutbound  string

	// commitMu serializes decision commits against each other: attempt
	// publications, automatic and manual writes, and generation
	// invalidations. Guard checks happen inside the lock, so a user action
	// and a late result can never interleave.
	commitMu sync.Mutex

	workers      sync.WaitGroup
	monitor      sync.WaitGroup
	captureWatch sync.WaitGroup
}

// New builds an engine around the given dependencies.
func New(deps Dependencies) *Engine {
	return &Engine{deps: deps, active: map[string]*attempt{}}
}

// Start launches capture, the optional WAN monitor and the workers — but
// only when the tunnel was chosen explicitly: an empty targetInterface means
// the initial setup is not finished, so the engine stays idle (Status
// reports setupRequired) while the HTTP API keeps serving the wizard.
// It is idempotent.
func (e *Engine) Start(ctx context.Context) error {
	if e.Running() {
		return nil
	}
	cfg := e.deps.Config.Config()
	if cfg.TargetInterface == "" {
		e.mu.Lock()
		// Запоминаем родительский контекст: PatchConfig (empty->explicit)
		// стартует движок уже после старта main.
		e.startCtx = ctx
		e.mu.Unlock()
		e.logf("setup required: no target interface configured, engine idle")
		return nil
	}
	e.pruneBlockedCandidates(cfg)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return nil
	}
	e.startCtx = ctx
	e.tunnelInterface, e.tunnelOutbound = "", ""
	e.baseCtx, e.baseCancel = context.WithCancel(ctx)
	e.stopCh = make(chan struct{})
	e.queue = make(chan capture.Suspect, queueCapacity)
	e.queued = make(map[string]struct{})
	e.startCaptureLocked(cfg.UDPEnabled)
	if cfg.WANMonitorEnabled {
		e.startMonitorLocked()
	}
	e.running = true
	for i := 0; i < workerCount; i++ {
		e.workers.Add(1)
		go e.worker()
	}
	e.captureWatch.Add(1)
	go e.superviseCapture()
	e.logf("engine started: udp=%v tunnelProbe=%v autoAdd=%v wanMonitor=%v",
		cfg.UDPEnabled, cfg.TunnelProbe, cfg.AutoAdd, cfg.WANMonitorEnabled)
	return nil
}

// Running reports whether the engine is started and not stopped.
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// startParentCtx returns the context remembered by Start, so a PatchConfig
// that completes the wizard can launch the engine under the same parent.
func (e *Engine) startParentCtx() context.Context {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.startCtx != nil {
		return e.startCtx
	}
	return context.Background()
}

// Stop closes admission, cancels and waits for the monitor, workers, capture
// and every in-flight probe. It is idempotent.
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.running = false
	stopCh := e.stopCh
	monitorCancel := e.monitorCancel
	captureRef := e.capture
	baseCancel := e.baseCancel
	e.tunnelInterface = ""
	e.tunnelOutbound = ""
	attempts := make([]*attempt, 0, len(e.active))
	for _, att := range e.active {
		attempts = append(attempts, att)
	}
	e.active = map[string]*attempt{}
	e.mu.Unlock()

	close(stopCh)
	for _, att := range attempts {
		att.cancel()
	}
	if monitorCancel != nil {
		monitorCancel()
	}
	baseCancel()
	e.workers.Wait()
	e.monitor.Wait()
	e.captureWatch.Wait()
	if captureRef != nil {
		_ = captureRef.Stop()
	}
	e.logf("engine stopped")
}

// PatchConfig persists a configuration merge patch and applies the
// operational consequences: every in-flight decision is invalidated, a UDP
// mode change rebuilds the capture and monitor changes start or stop the
// periodic WAN check. Disabling the monitor while paused resumes the capture,
// because a disabled monitor never pauses anything. The configuration store
// itself already forces autoAdd off when tunnelProbe is off.
//
// Tunnel lifecycle (BACKLOG section 3): the engine runs only with an
// explicitly chosen tunnel. empty->explicit starts it, explicit->empty stops
// it and deliberately leaves every AWGM rule untouched (no refresh is ever
// called with an empty interface), explicit A->B re-points the route objects
// at the new outbound immediately, so existing entries follow the new tunnel
// without waiting for the next add.
func (e *Engine) PatchConfig(data []byte) (config.Config, error) {
	old := e.deps.Config.Config()
	cfg, err := e.deps.Config.Patch(data)
	if err != nil {
		return config.Config{}, err
	}
	if cfg.TargetInterface == "" {
		// Без выбранного туннеля движок не работает, и ничего операционного
		// запускать нельзя (у idle-движка нет валидного baseCtx). Уход
		// explicit->empty останавливает рабочий движок, не перенаправляя и не
		// удаляя существующие AWGM-правила; empty->empty просто сохраняет
		// конфиг.
		if e.Running() {
			e.Stop()
			e.logf("target interface cleared: engine stopped, routing rules left untouched")
		}
		return cfg, nil
	}
	if old.TargetInterface == "" {
		// Wizard finished: launch the working engine. Start reads the fresh
		// config, so no apply section is needed here.
		if err := e.Start(e.startParentCtx()); err != nil {
			return cfg, err
		}
		return cfg, nil
	}
	e.commitMu.Lock()
	e.mu.Lock()
	e.invalidateLocked("configuration changed")
	var staleStop Capture
	if old.UDPEnabled != cfg.UDPEnabled {
		// Старое поколение заменяется фабрикой; его Stop (блокирующий,
		// ждёт <-done) выполняется вне локов ниже — под локами он дедлочит
		// так же, как Pause. captureGen растёт, чтобы запоздалый
		// pause/resume монитора не тронул ни старое, ни новое поколение.
		staleStop = e.capture
		e.captureGen++
		e.startCaptureLocked(cfg.UDPEnabled)
	}
	if old.WANMonitorEnabled != cfg.WANMonitorEnabled {
		if cfg.WANMonitorEnabled {
			e.startMonitorLocked()
		} else {
			e.stopMonitorLocked()
			// Монитор выключен — паузить нечему; первая удачная проверка
			// больше не придёт, снимаем паузу без Start: capture
			// пересоздаётся только в UDP-ветке выше.
			e.resumeCaptureLocked("WAN monitor disabled")
		}
	}
	e.mu.Unlock()
	e.commitMu.Unlock()

	// Смена targetInterface: пере-направить owner-правило и outbound на
	// новый туннель сразу, не дожидаясь первого добавления. I/O — вне
	// локов движка (writer сериализуется собственным mu). Ошибка не
	// откатывает сохранённый конфиг: следующий Add/retry повторит
	// обновление, текущее состояние описано в lastError.
	if old.TargetInterface != cfg.TargetInterface && e.deps.Writer != nil {
		if _, err := e.deps.Writer.RefreshTargetOutbound(e.baseCtx, cfg.TargetInterface); err != nil {
			e.logf("target interface %q: route objects not re-pointed: %v", cfg.TargetInterface, err)
			e.setLastError(fmt.Sprintf("target interface %q: %v", cfg.TargetInterface, err))
		} else {
			e.logf("target interface %q: route objects re-pointed", cfg.TargetInterface)
		}
	}

	// Блокирующая остановка заменённого поколения — вне локов движка.
	// Ожидание captureOpMu безопасно (локи движка сняты, Stop у живого
	// контроллера конечен); если пауза монитора прямо сейчас выполняется,
	// её завершение закроет и это поколение.
	if staleStop != nil {
		captureOpMu.Lock()
		start := time.Now()
		_ = staleStop.Stop()
		e.logf("replaced capture stopped in %dms", time.Since(start).Milliseconds())
		captureOpMu.Unlock()
	}
	e.logf("configuration applied")
	return cfg, nil
}

func (e *Engine) startCaptureLocked(udpAll bool) {
	e.captureGen++
	e.capture = e.deps.CaptureFactory(udpAll, e.enqueue)
	e.captureRunning = false
	if e.capturePaused {
		return // the WAN monitor keeps the capture paused
	}
	if err := e.capture.Start(e.baseCtx); err != nil {
		e.setLastErrorLocked("capture start: " + err.Error())
		return
	}
	e.captureRunning = true
}

// enqueue is the capture callback. It never blocks. Admission suppresses
// unnamed TCP targets and work that is already queued, active or decided; the
// bounded queue is reserved for distinct, actionable decisions.
func (e *Engine) enqueue(suspect capture.Suspect) {
	select {
	case <-e.stopCh:
		return
	default:
	}
	target := suspect.Target
	if target.Network == model.NetworkTCP && target.Domain == "" {
		// A TCP address without passive DNS/SNI evidence is not a safe domain
		// candidate. Keep UDP IP-only decisions for STUN/TURN unchanged.
		return
	}
	key := target.CandidateKey()
	e.mu.Lock()
	if e.capturePaused {
		// Поколение capture ставится на паузу: подозреваемый испорчен
		// неработающим WAN и не должен создавать кандидатов. Лок держится
		// до возврата, так что и после снятия локов движка, но до
		// завершения блокирующего Pause новый подозреваемый сюда не
		// пролезет.
		e.mu.Unlock()
		return
	}
	if _, busy := e.active[key]; busy {
		e.mu.Unlock()
		return
	}
	if _, pending := e.queued[key]; pending {
		e.mu.Unlock()
		return
	}
	if existing, ok := e.deps.State.Snapshot(time.Now()).Candidates[key]; ok &&
		existing.State != model.CandidateChecking {
		e.mu.Unlock()
		return
	}
	select {
	case e.queue <- suspect:
		e.queued[key] = struct{}{}
		e.mu.Unlock()
	default:
		e.mu.Unlock()
		e.dropped.Add(1)
		e.logf("queue full, suspect dropped: %s", key)
	}
}

func (e *Engine) worker() {
	defer e.workers.Done()
	for {
		select {
		case <-e.stopCh:
			return
		case suspect := <-e.queue:
			e.process(suspect, "")
		}
	}
}

// beginAttempt registers one in-flight decision. Duplicate signals for an
// active candidate and targets with an already decided candidate are
// dropped: IP-only targets dedup across ports by CandidateKey, while the
// observed endpoint stays inside the attempt. A manual recheck additionally
// requires the checking card prepared by Recheck, so it cannot recreate a
// candidate removed by a concurrent user action.
func (e *Engine) beginAttempt(key string, manual bool) (*attempt, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.queued, key)
	existing, exists := e.deps.State.Snapshot(time.Now()).Candidates[key]
	if manual {
		if !exists || existing.State != model.CandidateChecking {
			return nil, false
		}
	} else if exists && existing.State != model.CandidateChecking {
		e.logf("suspect skipped, candidate %s: %s", existing.State, key)
		return nil, false
	}
	if previous, busy := e.active[key]; busy {
		if !manual {
			e.logf("duplicate suspect dropped: %s", key)
			return nil, false
		}
		previous.cancel()
	}
	ctx, cancel := context.WithCancel(e.baseCtx)
	att := &attempt{
		id:         fmt.Sprintf("c%04d", e.corr.Add(1)),
		ctx:        ctx,
		cancel:     cancel,
		generation: e.gen.Load(),
	}
	e.active[key] = att
	return att, true
}

func (e *Engine) endAttempt(key string, att *attempt) {
	e.mu.Lock()
	if e.active[key] == att {
		delete(e.active, key)
	}
	e.mu.Unlock()
	att.cancel()
}

// invalidateLocked assumes commitMu (and, on the pause path, mu) is held.
func (e *Engine) invalidateLocked(reason string) {
	e.gen.Add(1)
	for _, att := range e.active {
		att.cancel()
	}
	_ = e.deps.State.Update(false, func(s *store.State) {
		for id, candidate := range s.Candidates {
			if candidate.State == model.CandidateChecking {
				delete(s.Candidates, id)
			}
		}
	})
	e.logf("decision generation invalidated: %s", reason)
}

// process runs the decision pipeline for one suspect:
// coverage and exclusions first, then the direct path, then the parallel
// direct and WAN checks, then the optional tunnel check, and finally the
// candidate publication or the gated automatic write. recheckState is set
// only for a manual recheck; its early exits then finalize the existing card.
func (e *Engine) process(suspect capture.Suspect, recheckState model.CandidateState) {
	target := suspect.Target
	key := target.CandidateKey()
	att, ok := e.beginAttempt(key, recheckState != "")
	if !ok {
		return
	}
	defer e.endAttempt(key, att)

	cand := model.Candidate{
		ID:            key,
		Target:        target,
		Reason:        suspect.Reason,
		ClientIP:      suspect.ClientIP,
		ClientMAC:     suspect.ClientMAC,
		FirstAt:       suspect.FirstAt,
		LastAt:        suspect.LastAt,
		Direct:        model.CheckEvidence{Verdict: model.VerdictNotChecked},
		WAN:           model.CheckEvidence{Verdict: model.VerdictNotChecked},
		Tunnel:        model.CheckEvidence{Verdict: model.VerdictNotChecked},
		CorrelationID: att.id,
	}
	defer func() {
		if recheckState == "" || !att.valid(e) {
			return
		}
		e.commitMu.Lock()
		defer e.commitMu.Unlock()
		current, ok := e.deps.State.Snapshot(time.Now()).Candidates[key]
		if !ok || current.State != model.CandidateChecking {
			return
		}
		state := recheckState
		if state == model.CandidateChecking {
			state = model.CandidateWaiting
		}
		perr := e.deps.State.Update(true, func(s *store.State) {
			current, ok := s.Candidates[key]
			if !ok || current.State != model.CandidateChecking {
				return
			}
			current.State = state
			current.Coverage = cand.Coverage
			current.Direct = cand.Direct
			current.WAN = cand.WAN
			current.Tunnel = cand.Tunnel
			current.LastError = cand.LastError
			current.LastAt = time.Now()
			current.CorrelationID = cand.CorrelationID
			s.Candidates[key] = current
		})
		if perr != nil {
			e.setLastError(fmt.Sprintf("recheck %s state write: %v", key, perr))
		}
	}()

	if e.paused() {
		cand.LastError = "capture is paused because WAN is unavailable"
		return // paused capture must not create deferred work
	}
	cfg := e.deps.Config.Config()
	e.logf("%s suspect %s reason=%s", att.id, describeTarget(target), suspect.Reason)

	// Coverage and exclusions run before any active check; an error or an
	// already covered or excluded target never becomes a candidate.
	coverageAt := time.Now()
	outcome, err := e.deps.Coverage.Check(att.ctx, suspect, cfg)
	if !att.valid(e) {
		return
	}
	cand.Coverage = outcome.Evidence
	if cand.Coverage.DurationMS == 0 {
		cand.Coverage.DurationMS = time.Since(coverageAt).Milliseconds()
	}
	if err != nil {
		cand.LastError = fmt.Sprintf("coverage: %v", err)
		e.setLastError(fmt.Sprintf("%s coverage: %v", att.id, err))
		e.logf("%s coverage error, no candidate: %v", att.id, err)
		return
	}
	if cand.Coverage.Covered || cand.Coverage.Excluded {
		e.logf("%s target covered=%v excluded=%v, no candidate", att.id, cand.Coverage.Covered, cand.Coverage.Excluded)
		return
	}

	directPath, err := e.deps.Routes.Resolve(att.ctx)
	if !att.valid(e) {
		return
	}
	if err != nil {
		cand.LastError = fmt.Sprintf("direct path: %v", err)
		e.setLastError(fmt.Sprintf("%s direct path: %v", att.id, err))
		e.logf("%s direct path error, no candidate: %v", att.id, err)
		return
	}

	// Direct target check and WAN check share one direct path and run in
	// parallel inside the probes dependency.
	results := e.deps.Probes.DirectAndWAN(att.ctx, target, cfg.WANCheckURL, probe.Path{Interface: directPath.Interface})
	if !att.valid(e) {
		return
	}
	cand.Direct = probeEvidence(results.Direct)
	cand.WAN = probeEvidence(results.WAN)
	e.logf("%s direct=%s wan=%s outbound=%s interface=%s%s%s",
		att.id, results.Direct.Verdict, results.WAN.Verdict, directPath.Outbound, directPath.Interface,
		probeLogDetail(results.Direct), probeLogDetail(results.WAN))
	if results.Direct.Verdict != model.VerdictBlocked || results.WAN.Verdict != model.VerdictReachable {
		// reachable needs no candidate; error and WAN failure prove nothing.
		switch {
		case results.Direct.Verdict == model.VerdictError:
			cand.LastError = "direct check failed"
		case results.WAN.Verdict == model.VerdictError:
			cand.LastError = "WAN check failed"
		case results.WAN.Verdict == model.VerdictBlocked:
			cand.LastError = "WAN check is blocked"
		}
		return
	}

	if !cfg.TunnelProbe {
		// Tunnel is off: the candidate is available for manual actions and
		// the tunnel interface for a later manual add resolves through the
		// default when the saved choice is empty.
		if tunnel, err := e.resolveTunnel(att.ctx, cfg); err == nil {
			cand.TargetInterface = tunnel.Interface
			cand.TargetOutbound = tunnel.Outbound
		}
		e.publishDecision(att, key, cand, model.CandidateWaiting, true)
		return
	}

	tunnel, err := e.resolveTunnel(att.ctx, cfg)
	if !att.valid(e) {
		return
	}
	if err != nil {
		e.setLastError(fmt.Sprintf("%s tunnel interface: %v", att.id, err))
		e.logf("%s tunnel unavailable, candidate waits without tunnel proof", att.id)
		cand.Tunnel = model.CheckEvidence{Verdict: model.VerdictError, Detail: err.Error(), CheckedAt: time.Now()}
		e.publishDecision(att, key, cand, model.CandidateWaiting, true)
		return
	}
	cand.TargetInterface = tunnel.Interface
	cand.TargetOutbound = tunnel.Outbound

	// The card is visible as checking while the tunnel probe runs; this
	// intermediate state is not persisted to flash.
	if !e.publishDecision(att, key, cand, model.CandidateChecking, false) {
		return
	}

	tunnelResult := e.deps.Probes.Check(att.ctx, target, probe.Path{Interface: tunnel.Interface})
	if !att.valid(e) {
		return
	}
	cand.Tunnel = probeEvidence(tunnelResult)
	e.logf("%s tunnel=%s interface=%s%s", att.id, tunnelResult.Verdict, tunnel.Interface, probeLogDetail(tunnelResult))
	if tunnelResult.Verdict == model.VerdictBlocked {
		e.discardCandidate(att, key)
		return
	}

	if tunnelResult.Verdict == model.VerdictReachable && e.autoAdd(att, &cand, directPath, recheckState != "") {
		return // the candidate is stored as added by autoAdd
	}
	e.publishDecision(att, key, cand, model.CandidateWaiting, true)
}

func (e *Engine) paused() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.capturePaused
}

func (e *Engine) additionsUsed() int {
	return len(e.deps.State.Snapshot(time.Now()).Additions)
}

func (e *Engine) setLastError(message string) {
	e.mu.Lock()
	e.lastError = message
	e.mu.Unlock()
}

func (e *Engine) setLastErrorLocked(message string) {
	e.lastError = message
}

func (e *Engine) logf(format string, args ...any) {
	if e.deps.Logger != nil {
		e.deps.Logger.Printf(format, args...)
	}
}

// Status is the operational picture for the main screen.
type Status struct {
	Running           bool                `json:"running"`
	SetupRequired     bool                `json:"setupRequired"`
	CaptureRunning    bool                `json:"captureActive"`
	CapturePaused     bool                `json:"capturePaused"`
	Protocols         []string            `json:"protocols"`
	UDPEnabled        bool                `json:"udpEnabled"`
	TargetInterface   string              `json:"targetInterface,omitempty"`
	TargetOutbound    string              `json:"targetOutbound,omitempty"`
	ResolveVia        string              `json:"resolveVia,omitempty"`
	LastWAN           model.CheckEvidence `json:"lastWAN"`
	QueueDepth        int                 `json:"queuedChecks"`
	ActiveChecks      int                 `json:"activeChecks"`
	QueueDropped      uint64              `json:"queueDropped,omitempty"`
	AdditionsUsed     int                 `json:"addsUsedHour"`
	AdditionsLimit    int                 `json:"addsLimit"`
	WaitingCandidates int                 `json:"waitingCandidates"`
	LastError         string              `json:"lastError,omitempty"`
}

func (e *Engine) Status() Status {
	e.mu.Lock()
	status := Status{
		Running:         e.running,
		CaptureRunning:  e.captureRunning,
		CapturePaused:   e.capturePaused,
		LastWAN:         e.lastWAN,
		LastError:       e.lastError,
		QueueDepth:      len(e.queue),
		ActiveChecks:    len(e.active),
		QueueDropped:    e.dropped.Load(),
		TargetInterface: e.tunnelInterface,
		TargetOutbound:  e.tunnelOutbound,
	}
	e.mu.Unlock()
	cfg := e.deps.Config.Config()
	status.SetupRequired = cfg.TargetInterface == ""
	status.UDPEnabled = cfg.UDPEnabled
	status.AdditionsLimit = cfg.AddsPerHour
	if cfg.TargetInterface != "" {
		status.TargetInterface = cfg.TargetInterface
	}
	status.ResolveVia = cfg.ResolveVia
	status.Protocols = []string{"tcp/443", "dns/53"}
	if cfg.UDPEnabled {
		status.Protocols = append(status.Protocols, "udp/stun-turn")
	}
	status.AdditionsUsed = e.additionsUsed()
	status.WaitingCandidates = e.deps.State.CountCandidates("waiting")
	return status
}

func probeEvidence(r probe.Result) model.CheckEvidence {
	return model.CheckEvidence{
		Verdict:    r.Verdict,
		Bytes:      int64(r.Bytes),
		DurationMS: r.Duration.Milliseconds(),
		Detail:     r.Detail,
		CheckedAt:  time.Now(),
	}
}

// probeLogDetail renders the curl error detail and the phase timings of one
// non-reachable probe result, so the journal shows which stage failed without
// a separate debug mode (TimeDNS/TimeConnect/TimeTLS stay observable). A
// reachable result adds nothing.
func probeLogDetail(r probe.Result) string {
	if r.Verdict == model.VerdictReachable {
		return ""
	}
	parts := make([]string, 0, 4)
	if r.Detail != "" {
		parts = append(parts, "detail="+strconv.Quote(r.Detail))
	}
	if r.TimeDNS > 0 {
		parts = append(parts, fmt.Sprintf("dns=%dms", r.TimeDNS.Milliseconds()))
	}
	if r.TimeConnect > 0 {
		parts = append(parts, fmt.Sprintf("connect=%dms", r.TimeConnect.Milliseconds()))
	}
	if r.TimeTLS > 0 {
		parts = append(parts, fmt.Sprintf("tls=%dms", r.TimeTLS.Milliseconds()))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " ") + ")"
}

func describeTarget(t model.Target) string {
	host := t.Domain
	if host == "" {
		host = t.IP
	}
	return host + " " + string(t.Network) + "/" + strconv.Itoa(int(t.Port))
}
