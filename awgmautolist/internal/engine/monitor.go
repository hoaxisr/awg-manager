// Periodic WAN monitoring and its interaction with capture and decisions.
package engine

import (
	"context"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probe"
)

// startMonitorLocked launches the periodic WAN check: the first check runs
// immediately, then every monitorInterval. Disabled by default.
func (e *Engine) startMonitorLocked() {
	ctx, cancel := context.WithCancel(e.baseCtx)
	e.monitorCancel = cancel
	e.monitor.Add(1)
	go func() {
		defer e.monitor.Done()
		e.logf("WAN monitor started")
		ticker := time.NewTicker(monitorInterval)
		defer ticker.Stop()
		for {
			e.monitorCheck(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (e *Engine) stopMonitorLocked() {
	if e.monitorCancel != nil {
		e.monitorCancel()
		e.monitorCancel = nil
	}
}

// monitorCheck выполняет одну периодическую проверку WAN и применяет переход:
// неудачная проверка ставит capture на паузу, инвалидирует in-flight решения
// и блокирует коммиты; первая удачная проверка возобновляет capture с чистым
// состоянием потоков. Переходы логируются один раз при смене состояния, а не
// каждые 30 секунд. Выключенный монитор (или остановленный движок) ничего
// не применяет.
//
// Блокирующие capture.Pause()/Start() выполняются ТОЛЬКО после снятия
// commitMu и mu (см. pauseCaptureLocked): под локами они дедлочат — Pause
// ждёт закрытия поколения сокетов, а readLoop в это время сидит в
// emit→enqueue на e.mu. Порядок локов всюду: commitMu перед mu.
func (e *Engine) monitorCheck(ctx context.Context) {
	url := e.deps.Config.Config().WANCheckURL
	var result probe.Result
	path, err := e.deps.Routes.Resolve(ctx)
	if err != nil {
		result = probe.Result{Verdict: model.VerdictError, Detail: "direct path: " + err.Error()}
	} else {
		result = e.deps.Probes.CheckWAN(ctx, url, probe.Path{Interface: path.Interface})
	}
	if ctx.Err() != nil {
		return // the monitor was disabled or the engine stopped meanwhile
	}

	// Решение о переходе — под commitMu+mu, по образцу recoverCapture:
	// под локами только флаги, дренаж очереди и инвалидация; ссылку на
	// capture забираем для блокирующей операции вне локов.
	e.commitMu.Lock()
	e.mu.Lock()
	action := e.applyWANVerdictLocked(result)
	e.mu.Unlock()
	e.commitMu.Unlock()

	// Блокирующая операция — вне локов; длительность пишем в лог после
	// факта, чтобы в журнале читался порядок «решение → операция».
	if action.ref != nil {
		e.runCaptureAction(action)
	}
}

// captureAction — отложенное решение о блокирующей capture-операции,
// принятое под локами. gen — поколение capture на момент решения: если до
// исполнения решение устарело (UDP-патч пересоздал capture, пауза снята),
// устаревшее действие отбрасывается.
type captureAction struct {
	ref   Capture
	gen   uint64
	pause bool
}

// applyWANVerdictLocked обновляет lastWAN вердиктом этой проверки и
// принимает решение о паузе/возобновлении capture: любая неудача паузит,
// первая удачная после паузы возобновляет. Возвращает действие для
// блокирующего исполнения вне локов (ref==nil — операция не требуется).
// Требует commitMu и mu; вызывается только из monitorCheck.
func (e *Engine) applyWANVerdictLocked(result probe.Result) captureAction {
	e.lastWAN = model.CheckEvidence{
		Verdict:    result.Verdict,
		DurationMS: result.Duration.Milliseconds(),
		Detail:     result.Detail,
		CheckedAt:  time.Now(),
	}
	if result.Verdict == model.VerdictReachable {
		e.setLastErrorLocked("")
		if e.capturePaused {
			return e.resumeCaptureLocked("WAN reachable again")
		}
		return captureAction{}
	}
	e.setLastErrorLocked("WAN " + string(result.Verdict) + ": " + result.Detail)
	if e.capturePaused {
		return captureAction{} // уже на паузе — повторять Pause нечего
	}
	return e.pauseCaptureLocked(string(result.Verdict))
}

// pauseCaptureLocked фиксирует решение о паузе под локами: лог решения,
// флаги, дренаж очереди и инвалидацию. Отложенные подозреваемые до паузы
// устарели и не должны создавать решений. Сам блокирующий capture.Pause()
// вызывающая сторона обязана выполнить ПОСЛЕ снятия commitMu и mu
// (runCaptureAction): Pause ждёт закрытия текущего поколения сокетов, а
// readLoop в этот момент может сидеть в emit→enqueue на e.mu — вызов под
// локами дедлочит. In-flight решения инвалидируются при уже удержанном
// commitMu.
func (e *Engine) pauseCaptureLocked(reason string) captureAction {
	e.logf("WAN error: pausing capture: %s", reason)
	e.captureRunning = false
	e.capturePaused = true
	e.drainQueueLocked()
	e.invalidateLocked("WAN unavailable")
	return captureAction{ref: e.capture, gen: e.captureGen, pause: true}
}

// resumeCaptureLocked фиксирует решение о возобновлении под локами и
// возвращает действие Start вне локов (runCaptureAction). Флаг
// captureRunning ставится оптимистично и уточняется после Start. Гонка
// «resume монитора против recoverCapture» безопасна: оба идут через
// commitMu, а двойной Start сериализуется opMu контроллера capture и для
// уже работающего поколения возвращается без новой генерации.
func (e *Engine) resumeCaptureLocked(reason string) captureAction {
	e.logf("WAN monitor: resuming capture: %s", reason)
	e.capturePaused = false
	if e.capture == nil {
		return captureAction{}
	}
	e.captureRunning = true
	if strings.HasPrefix(e.lastError, "WAN ") {
		e.lastError = ""
	}
	return captureAction{ref: e.capture, gen: e.captureGen}
}

// runCaptureAction выполняет блокирующую capture-операцию вне локов
// движка. Вызывать только без e.mu и commitMu — под локами Pause/Start
// дедлочат (см. pauseCaptureLocked).
//
// Сериализация и защита от устаревших решений:
//   - captureOpMu страхует от гонки «устаревший pause против свежего
//     resume» (disable монитора, UDP-патч, recovery): операция выполняется
//     только если между решением и захватом captureOpMu не сменилось
//     поколение capture и пауза не снята. Иначе действие отбрасывается
//     как устаревшее.
//   - Двойной Start безопасен и без этого: opMu контроллера сериализует
//     его, а Start идемпотентен для работающего поколения.
func (e *Engine) runCaptureAction(action captureAction) {
	if action.ref == nil {
		return
	}
	if !captureOpMu.TryLock() {
		e.logf("capture action skipped: another capture operation is in flight")
		return
	}
	defer captureOpMu.Unlock()
	// Перепроверка под локами: решение могло устареть, пока конкурирующая
	// горутина вела свою commitMu-секцию.
	e.commitMu.Lock()
	e.mu.Lock()
	stale := action.gen != e.captureGen || action.pause != e.capturePaused
	e.mu.Unlock()
	e.commitMu.Unlock()
	if stale {
		e.logf("capture action skipped: decision is stale (wantPaused=%v gen=%d)", action.pause, action.gen)
		return
	}

	start := time.Now()
	var err error
	if action.pause {
		err = action.ref.Pause()
	} else {
		err = action.ref.Start(e.baseCtx)
	}
	took := time.Since(start).Milliseconds()
	if err != nil {
		verb := "pause"
		if !action.pause {
			verb = "resume"
		}
		e.setLastError("capture " + verb + ": " + err.Error())
		if !action.pause {
			e.mu.Lock()
			e.captureRunning = false
			e.mu.Unlock()
		}
		e.logf("capture %s failed after %dms: %s", verb, took, err)
		return
	}
	if action.pause {
		e.logf("WAN monitor: capture paused in %dms", took)
	} else {
		e.logf("WAN monitor: capture resumed in %dms", took)
	}
}

// drainQueueLocked empties the bounded queue. Called with mu held.
func (e *Engine) drainQueueLocked() {
	for {
		select {
		case <-e.queue:
		default:
			e.queued = make(map[string]struct{})
			return
		}
	}
}
