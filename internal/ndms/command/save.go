package command

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	ndmsevents "github.com/hoaxisr/awg-manager/internal/ndms/events"
)

// Poster is the minimum surface SaveCoordinator needs from the NDMS
// transport. Real implementations use *transport.Client.
type Poster interface {
	Post(ctx context.Context, payload any) (json.RawMessage, error)
}

// PostSaveInvalidator is the minimal cache-invalidation surface
// SaveCoordinator needs after a successful save. RunningConfigStore
// from internal/ndms/query satisfies it via the embedded
// *cache.ListStore[T].InvalidateAll() promoted method.
//
// The interface is declared here (not in query/) to keep
// SaveCoordinator's package free of an import cycle with query/.
type PostSaveInvalidator interface {
	InvalidateAll()
}

// savePayload is the NDMS command for "persist running-config to flash".
// Matches the exact shape Keenetic's own web UI uses:
//
//	{"system":{"configuration":{"save":{}}}}
//
// (This is `system configuration save` in ndmc CLI form.)
// The previous shorthand {"save": true} was not a valid RCI path and
// silently no-oped on OS5 — changes survived the session but certain
// state fields (e.g. dns-proxy.route "disable" flag) never made it
// into `/show/sc/...` views that the router UI reads from.
var savePayload = map[string]any{
	"system": map[string]any{
		"configuration": map[string]any{
			"save": map[string]any{},
		},
	},
}

// Временные величины полёта сохранения (П25, вариант А владельца 06.10
// 15:50). Это страховки на случай пропущенного события, а не механизм:
// конец сохранения — событие ConfigurationSaved шины ndm.
const (
	// SaveEventCap — потолок полёта при подключённой шине: событие не пришло
	// (потеряно ndm, не наш формат) — полёт закрывается с Warn. Запись по
	// стенду 4–10 с (К42), под нагрузкой событие +4,3–6,9 с от POST.
	SaveEventCap = 30 * time.Second
	// saveFallback — полёт без сигнала (шины нет или она оборвалась во время
	// полёта, uptime не прочитан): верхняя граница длительности записи по
	// стенду (К42: `saving → configuration saved` 4–10 с), считается от POST.
	saveFallback = 10 * time.Second
	// SaveAfterRemoval — наше сохранение не раньше, чем через 5 с после
	// нашего `no interface`. Эвристика: сигнала «снятая запись разобрана» нет
	// ни на шине, ни в хуках (event-bus-evidence.md §4); проход сохранения,
	// стартующий ≤~4 с после `removed`, ловит недоразобранную запись — E
	// Base/L3Base (dn3-round2-evidence.md §3: 20/33 против 0/12 вне окна;
	// под нагрузкой хвост разбора 2–3 с).
	SaveAfterRemoval = 5 * time.Second
)

// SaveCoordinator debounces flash-write Save requests into a single POST
// per burst. See design spec §5.2-5.3.
//
// Полёт сохранения (П25): от POST до события ConfigurationSaved шины ndm с
// raise_time ≥ t0 (uptime перед POST; тот же домен часов). Пока полёт
// открыт, удаление записи (HoldForRemoval) ждёт: проход сохранения,
// открытый при `no interface`, ловит снимаемую запись — E в журнале ndm
// (D-N3, dn3-round2-evidence.md). Порядок замков — saveSem → mu; полёт
// открывает только владелец saveSem (fire/Flush) в одной секции mu с
// проверкой удержаний и паузы после сноса, закрывает — тоже он (closeFlight
// до отпускания saveSem). Шина, таймеры полёта, HoldForRemoval берут только mu.
type SaveCoordinator struct {
	poster     Poster
	publisher  StatusPublisher
	debounce   time.Duration
	maxWait    time.Duration
	retryDelay time.Duration
	maxRetries int

	settleDelay time.Duration
	invalidator PostSaveInvalidator

	eventCap, fallback, afterRemoval time.Duration
	uptime                           func() float64
	log                              WarnLogger

	// saveSem (ёмкость 1) сериализует POST вместе с ожиданием его события:
	// fire берёт блокирующе, Flush — по ctx.
	saveSem chan struct{}

	mu           sync.Mutex
	timer        *time.Timer
	firstAt      time.Time // zero if no pending batch
	pendingCount int
	state        SaveState
	lastError    string
	lastSaveAt   time.Time
	retryCount   int // consecutive failures in current batch

	cur           *saveFlight
	holds         int
	holdsZero     chan struct{} // закрывается, когда holds возвращается к 0
	deferred      bool          // fire отложен удержанием — release перевзведёт
	lastRemovalAt time.Time
	busUp         bool
	observer      func(deferred bool)
	// Эпохи (Н10b): requested растёт на каждом Request, saved — значение
	// requested на момент POST полёта, закрытого событием или потолком.
	requested, saved uint64
}

// saveFlight — одно наше сохранение в полёте. Принадлежит владельцу POST.
type saveFlight struct {
	ch        chan struct{} // закрыт — полёт окончен (holders идут)
	t0        float64       // uptime перед POST; 0 — не прочитан (unknown)
	openedAt  time.Time
	unknown   bool // сигнала не будет: закрывает saveFallback от openedAt
	pending   int  // pendingCount на момент POST: столько правок покрывает запись
	done      bool
	requested uint64
	timers    []*time.Timer
}

// WarnLogger — журнал координатора (Warn о потере сигнала сохранения).
type WarnLogger interface {
	Warnf(format string, args ...any)
}

type nopWarnLogger struct{}

func (nopWarnLogger) Warnf(string, ...any) {}

const (
	defaultRetryDelay = 5 * time.Second
	defaultMaxRetries = 3
)

// NewSaveCoordinator constructs a coordinator with production defaults.
// debounce    — delay before firing Save after the last Request().
// maxWait     — hard ceiling from first Request() in the current batch.
// settleDelay — pause after a successful save before invalidating the
//
//	RunningConfig cache. NDMS publishes running-config
//	asynchronously after writing flash, so reads issued
//	immediately after save would still see the old view.
//	Pass 0 to disable settle entirely (skip both sleep and
//	invalidate).
//
// invalidator — cache surface invalidated after settle. Pass nil to
//
//	disable settle (sleep is still skipped). Typically
//	wired to query.Queries.RunningConfig.
//
// Retries: 3 attempts 5 seconds apart after a failed fire.
//
// Шина ndm до первого OnBusState(true) считается отключённой: каждый полёт —
// unknown, закрывается через saveFallback от POST (fail-closed). Шину
// подключают и демон, и уборка (events.NewSaveBusReader); fallback 0 — только
// тестовые харнессы без шины: SetSaveTimings(…, 0, …).
func NewSaveCoordinator(
	poster Poster,
	pub StatusPublisher,
	debounce, maxWait, settleDelay time.Duration,
	invalidator PostSaveInvalidator,
) *SaveCoordinator {
	return &SaveCoordinator{
		poster:       poster,
		publisher:    pub,
		debounce:     debounce,
		maxWait:      maxWait,
		retryDelay:   defaultRetryDelay,
		maxRetries:   defaultMaxRetries,
		settleDelay:  settleDelay,
		invalidator:  invalidator,
		state:        SaveStateIdle,
		eventCap:     SaveEventCap,
		fallback:     saveFallback,
		afterRemoval: SaveAfterRemoval,
		uptime:       ndmsevents.ReadUptime,
		log:          nopWarnLogger{},
		saveSem:      make(chan struct{}, 1),
	}
}

// SetSettleDelay overrides the post-save settle delay — used by tests
// that need sub-second timings. Mirrors the SetRetryPolicy pattern.
func (s *SaveCoordinator) SetSettleDelay(d time.Duration) {
	s.mu.Lock()
	s.settleDelay = d
	s.mu.Unlock()
}

// SetRetryPolicy overrides the retry delay and max retries — used by tests
// that need sub-second timings.
func (s *SaveCoordinator) SetRetryPolicy(delay time.Duration, maxRetries int) {
	s.mu.Lock()
	s.retryDelay = delay
	s.maxRetries = maxRetries
	s.mu.Unlock()
}

// SetSaveTimings — тест-шов: потолок полёта, запасной потолок без сигнала и
// пауза после сноса. Харнесс без шины ставит fallback 0: ждать события нечем.
// Прод его не зовёт (уборка с M2 финального ревью F595 — с шиной).
func (s *SaveCoordinator) SetSaveTimings(eventCap, fallback, afterRemoval time.Duration) {
	s.mu.Lock()
	s.eventCap, s.fallback, s.afterRemoval = eventCap, fallback, afterRemoval
	s.mu.Unlock()
}

// SetUptimeReader — часы t0 (тест-шов Б2; по умолчанию /proc/uptime).
func (s *SaveCoordinator) SetUptimeReader(fn func() float64) {
	s.mu.Lock()
	s.uptime = fn
	s.mu.Unlock()
}

// SetLogger — журнал Warn о потере сигнала сохранения.
func (s *SaveCoordinator) SetLogger(l WarnLogger) {
	s.mu.Lock()
	s.log = l
	s.mu.Unlock()
}

// SetFireObserver — тест-шов: fn(deferred) зовётся после решения fire
// (true — отложен удержанием или паузой после сноса, false — POST), вне mu.
// fn обязан не блокировать.
func (s *SaveCoordinator) SetFireObserver(fn func(deferred bool)) {
	s.mu.Lock()
	s.observer = fn
	s.mu.Unlock()
}

// Epoch — (requested, saved): saved ≥ эпохи правки ⇔ правка покрыта
// завершённым сохранением (FlushPendingSave).
func (s *SaveCoordinator) Epoch() (requested, saved uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requested, s.saved
}

// Request schedules a debounced Save. Non-blocking.
func (s *SaveCoordinator) Request() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requested++
	now := time.Now()
	if s.firstAt.IsZero() {
		s.firstAt = now
	}
	s.pendingCount++

	fireAt := now.Add(s.debounce)
	maxFireAt := s.firstAt.Add(s.maxWait)
	if fireAt.After(maxFireAt) {
		fireAt = maxFireAt
	}

	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(fireAt.Sub(now), s.fire)

	s.setStateLocked(SaveStatePending, "")
}

// OnConfigurationSaved — событие шины: закрывает наш полёт, если оно не
// раньше нашего POST (raise ≥ t0 > 0). Чужое сохранение, склеенное ndm с
// нашим, тоже его закрывает — запись тогда действительно окончена. O(1) под
// mu: зовётся из горутины чтения шины.
func (s *SaveCoordinator) OnConfigurationSaved(raise float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.cur
	if f == nil || f.done || f.unknown {
		return
	}
	if raise >= f.t0 {
		s.completeLocked(f)
	}
}

// OnBusState — подключение шины. Потеря во время полёта: события промежутка
// потеряны (ndm не досылает), полёт становится unknown и закрывается через
// saveFallback от POST — сразу, если этот срок уже прошёл.
func (s *SaveCoordinator) OnBusState(connected bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	was := s.busUp
	s.busUp = connected
	if was && !connected {
		s.log.Warnf("шина событий ndm отключена: конец сохранения не виден, каждое сохранение ждёт %s", s.fallback)
	}
	if f := s.cur; !connected && f != nil && !f.done && !f.unknown {
		f.unknown = true
		s.armFallbackLocked(f)
	}
}

// HoldForRemoval — удержание сохранения на время сноса записи: ждёт
// окончания нашего сохранения в полёте (по ctx: отмена — удержание снято,
// ошибка, `no interface` не шлётся), а пока удержание держится, fire не
// стреляет (deferred). Вложенные удержания считаются; release взводит
// отложенное сохранение только при отпускании последнего. release
// идемпотентен.
func (s *SaveCoordinator) HoldForRemoval(ctx context.Context) (release func(), err error) {
	s.mu.Lock()
	if s.holds == 0 {
		s.holdsZero = make(chan struct{})
	}
	s.holds++
	f := s.cur
	s.mu.Unlock()
	if f != nil {
		select {
		case <-f.ch:
		case <-ctx.Done():
			s.releaseHold()
			return nil, fmt.Errorf("wait for configuration save: %w", ctx.Err())
		}
	}
	var once sync.Once
	return func() { once.Do(s.releaseHold) }, nil
}

func (s *SaveCoordinator) releaseHold() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holds--
	if s.holds > 0 {
		return
	}
	close(s.holdsZero)
	if s.deferred || s.pendingCount > 0 {
		s.deferred = false
		s.firstAt = time.Now()
		if s.timer != nil {
			s.timer.Stop()
		}
		s.timer = time.AfterFunc(s.debounce, s.fire)
	}
}

// NoteRemoval — наш `no interface` снял запись: следующее наше сохранение
// не раньше SaveAfterRemoval.
func (s *SaveCoordinator) NoteRemoval() {
	s.mu.Lock()
	s.lastRemovalAt = time.Now()
	s.mu.Unlock()
}

// removalGapLocked — сколько ещё ждать до конца паузы после сноса.
func (s *SaveCoordinator) removalGapLocked() time.Duration {
	if s.lastRemovalAt.IsZero() {
		return 0
	}
	return time.Until(s.lastRemovalAt.Add(s.afterRemoval))
}

// openFlightLocked открывает полёт перед POST. Только владелец saveSem.
func (s *SaveCoordinator) openFlightLocked() *saveFlight {
	f := &saveFlight{
		ch:        make(chan struct{}),
		t0:        s.uptime(),
		openedAt:  time.Now(),
		requested: s.requested,
		pending:   s.pendingCount,
	}
	// Н1: полёт, закрытый потолком при записи дольше eventCap, оставляет
	// хвост: его событие может закрыть следующий полёт раньше. Записи > 30 с
	// не наблюдались — не чиним.
	s.cur = f
	if !s.busUp || f.t0 == 0 {
		// Б1: t0 == 0 — любое событие (raise ≥ 0) закрыло бы полёт сразу.
		f.unknown = true
		s.armFallbackLocked(f)
	}
	f.timers = append(f.timers, time.AfterFunc(s.eventCap, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !f.done {
			s.log.Warnf("событие ConfigurationSaved не пришло за %s — сохранение считается законченным", s.eventCap)
			s.completeLocked(f)
		}
	}))
	return f
}

// armFallbackLocked — закрыть unknown-полёт через fallback от POST.
// Отрицательная задержка (срок прошёл) — AfterFunc стреляет сразу (Н5).
func (s *SaveCoordinator) armFallbackLocked(f *saveFlight) {
	d := time.Until(f.openedAt.Add(s.fallback))
	f.timers = append(f.timers, time.AfterFunc(d, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !f.done {
			s.log.Warnf("конец сохранения не виден (шина ndm не подключена) — считается законченным через %s", s.fallback)
			s.completeLocked(f)
		}
	}))
}

// settledLocked — сохранение f записано: снимаются правки, заказанные до
// его POST; заказанные во время полёта ждут своего fire (их таймер взведён).
func (s *SaveCoordinator) settledLocked(f *saveFlight) {
	s.pendingCount = max(s.pendingCount-f.pending, 0)
	s.retryCount = 0
	s.lastSaveAt = time.Now()
	if s.pendingCount > 0 {
		s.setStateLocked(SaveStatePending, "")
		return
	}
	s.setStateLocked(SaveStateIdle, "")
}

// completeLocked — сохранение окончено (событие, потолок, fallback):
// holders идут, эпоха saved покрывает запросы до POST.
func (s *SaveCoordinator) completeLocked(f *saveFlight) {
	if f.done {
		return
	}
	endFlight(f)
	if f.requested > s.saved {
		s.saved = f.requested
	}
}

// endFlight — holders идут, таймеры полёта сняты. Под mu.
func endFlight(f *saveFlight) {
	f.done = true
	close(f.ch)
	for _, t := range f.timers {
		t.Stop()
	}
}

// closeFlight — владелец снимает свой полёт (defer до отпускания saveSem):
// на отказе POST полёт закрывается без эпохи; cur обнуляется, только если
// он ещё этот полёт.
func (s *SaveCoordinator) closeFlight(f *saveFlight) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !f.done {
		endFlight(f)
	}
	if s.cur == f {
		s.cur = nil
	}
}

func (s *SaveCoordinator) observe(deferred bool) {
	s.mu.Lock()
	fn := s.observer
	s.mu.Unlock()
	if fn != nil {
		fn(deferred)
	}
}

// fire runs on the timer goroutine. Performs the Save POST, waits for its
// end (ConfigurationSaved or a ceiling), publishes status transitions, and
// schedules a retry on failure.
//
// saveSem берётся ДО решения и открытия полёта: иначе Flush, держащий
// saveSem в ожидании удержаний, и holder, ждущий открытого здесь полёта,
// ждали бы друг друга (TestSave_NoDeadlock_FlushHoldsSaveMu).
func (s *SaveCoordinator) fire() {
	// settle — после отпускания saveSem (defer'ы LIFO: полёт закрыт, saveSem
	// отпущен, затем settle): пауза публикации running-config не держит Flush.
	var settle func()
	defer func() {
		if settle != nil {
			settle()
		}
	}()
	s.saveSem <- struct{}{}
	defer func() { <-s.saveSem }() // closeFlight зарегистрирован позже — идёт раньше (Н3)

	s.mu.Lock()
	// Этот fire обслуживает заказ сам: таймер, взведённый после его
	// срабатывания, не нужен (иначе два отложенных fire дали бы два POST).
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.pendingCount == 0 {
		// Заказ уже покрыт сохранением, начатым после него (fire, ждавший
		// saveSem за чужим полётом).
		s.mu.Unlock()
		return
	}
	if s.holds > 0 {
		s.deferred = true
		s.mu.Unlock()
		s.observe(true)
		return
	}
	if gap := s.removalGapLocked(); gap > 0 {
		s.timer = time.AfterFunc(gap, s.fire)
		s.mu.Unlock()
		s.observe(true)
		return
	}
	// Clear firstAt so a new Request() starts a fresh batch.
	// pendingCount is intentionally preserved so the SSE status reflects
	// how many mutations accumulated since the last successful Save.
	s.firstAt = time.Time{}
	f := s.openFlightLocked()
	s.setStateLocked(SaveStateSaving, "")
	s.mu.Unlock()
	s.observe(false)
	defer s.closeFlight(f)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err := s.poster.Post(ctx, savePayload)
	cancel()
	if err == nil {
		<-f.ch // потолок полёта гарантирует возврат
	}

	s.mu.Lock()
	if err == nil {
		s.settledLocked(f)
		// Snapshot settle deps under the lock so SetSettleDelay races
		// can't tear our view of (delay, invalidator).
		settleDelay := s.settleDelay
		invalidator := s.invalidator
		s.mu.Unlock()

		// Post-save settle (outside mu and saveSem): wait for NDMS to publish
		// the updated running-config view, then invalidate the cache so the
		// next reader gets fresh data.
		if settleDelay > 0 && invalidator != nil {
			settle = func() {
				time.Sleep(settleDelay)
				invalidator.InvalidateAll()
				events.PublishInvalidatedTo(s.publisher, events.ResourceSaveStatus, "save-settled")
			}
		}
		return
	}

	s.retryCount++
	if s.retryCount > s.maxRetries {
		s.setStateLocked(SaveStateFailed, err.Error())
		s.mu.Unlock()
		return
	}
	s.setStateLocked(SaveStateError, err.Error())
	// Schedule retry — fresh fire after retryDelay.
	s.timer = time.AfterFunc(s.retryDelay, s.fire)
	s.mu.Unlock()
}

// Flush runs Save synchronously, bypassing debounce. Called on graceful
// shutdown, backup quiesce, F568 and by the UI "Retry save" button. Clears
// Failed state on success. On failure, transitions directly to
// SaveStateFailed — Flush is itself the explicit retry, so there is no point
// in scheduling another. Returns the underlying error (nil on success).
//
// По ctx ждёт saveSem (летящий fire со своим событием), отпускания удержаний
// и паузы после сноса (В5); затем POST и его событие. Событие ждётся без
// ctx: снять удержания до конца записи — открыть D-N3; ожидание ограничено
// потолком полёта.
func (s *SaveCoordinator) Flush(ctx context.Context) error {
	select {
	case s.saveSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.saveSem }()

	s.mu.Lock()
	for {
		var wait <-chan struct{}
		var gapTimer *time.Timer
		if s.holds > 0 {
			wait = s.holdsZero
		} else if gap := s.removalGapLocked(); gap > 0 {
			gapTimer = time.NewTimer(gap)
		} else {
			break
		}
		s.mu.Unlock()
		var tc <-chan time.Time
		if gapTimer != nil {
			tc = gapTimer.C
		}
		select {
		case <-wait:
		case <-tc:
		case <-ctx.Done():
			if gapTimer != nil {
				gapTimer.Stop()
			}
			return ctx.Err()
		}
		s.mu.Lock()
	}
	// Таймер — после ожидания: его мог взвести release удержания, а POST
	// ниже покрывает всё, что заказано до него.
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.firstAt = time.Time{}
	f := s.openFlightLocked()
	s.setStateLocked(SaveStateSaving, "")
	s.mu.Unlock()
	defer s.closeFlight(f)

	_, err := s.poster.Post(ctx, savePayload)
	if err == nil {
		<-f.ch // потолок полёта гарантирует возврат
	}

	s.mu.Lock()
	successful := err == nil
	if successful {
		s.settledLocked(f)
	} else {
		// Flush IS the explicit retry — failure is terminal, go straight
		// to Failed. Mark retry budget exhausted.
		s.retryCount = s.maxRetries + 1
		s.setStateLocked(SaveStateFailed, err.Error())
	}
	invalidator := s.invalidator
	s.mu.Unlock()

	// On Flush — invalidate WITHOUT sleep. Flush is a terminal operation
	// (shutdown or explicit UI retry); callers either don't read after
	// (shutdown) or are already waiting on a UI spinner (retry button).
	if successful && invalidator != nil {
		invalidator.InvalidateAll()
	}
	return err
}

// setStateLocked updates state + publishes a resource:invalidated hint.
// Must be called with mu held.
//
// Before the state-sync redesign (Task 13) this published the full
// SaveStatus as a "save:status" SSE event; the payload is now fetched
// on-demand via GET /api/ndms/save-status by a polling store. Emitting
// just the hint keeps the save indicator reactive without pushing full
// state over SSE.
func (s *SaveCoordinator) setStateLocked(next SaveState, errMsg string) {
	s.state = next
	s.lastError = errMsg
	events.PublishInvalidatedTo(s.publisher, events.ResourceSaveStatus, "state-change")
}

// Status returns a snapshot of the current SaveStatus. Intended for
// inclusion in the SSE reconnect snapshot so clients that open mid-save
// still see the right indicator.
func (s *SaveCoordinator) Status() SaveStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SaveStatus{
		State:        s.state,
		LastError:    s.lastError,
		LastSaveAt:   s.lastSaveAt,
		PendingCount: s.pendingCount,
	}
}
