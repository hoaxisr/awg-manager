package command

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// --- Test doubles ---

type fakePoster struct {
	mu       sync.Mutex
	calls    int32
	nextErr  error
	nextResp json.RawMessage
	sleep    time.Duration
	payloads []any
}

func (f *fakePoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	// Ошибка и слепок полезной нагрузки снимаются ДО инкремента счётчика.
	// Иначе `waitCalls` мог вернуться в зазоре между инкрементом и чтением
	// `nextErr`, тест успевал позвать SetError(nil), и попытка, которую он
	// считал отказавшей, проходила успешно (CF6). Направление было безопасное
	// — ложный красный, — но повод краснеть выдуманный.
	f.mu.Lock()
	err := f.nextErr
	sleep := f.sleep
	f.payloads = append(f.payloads, payload)
	f.mu.Unlock()
	// Инкремент ЗДЕСЬ, до сна: значит `waitCalls(…, 1)` возвращается, когда
	// вызов уже ВНУТРИ Post. Тестам, которым надо попасть внутрь медленного
	// Post, отдельного сигнала не нужно — хватает счётчика.
	atomic.AddInt32(&f.calls, 1)
	if sleep > 0 {
		time.Sleep(sleep)
	}
	f.mu.Lock()
	resp := f.nextResp
	f.mu.Unlock()
	if resp == nil {
		resp = json.RawMessage(`{}`)
	}
	return resp, err
}

// SetResponse задаёт тело ответа NDMS: мутации отвечают HTTP 200 и прячут
// отказ во вложенном status.
func (f *fakePoster) SetResponse(resp string) {
	f.mu.Lock()
	f.nextResp = json.RawMessage(resp)
	f.mu.Unlock()
}

func (f *fakePoster) Calls() int32 { return atomic.LoadInt32(&f.calls) }

func (f *fakePoster) SetError(err error) {
	f.mu.Lock()
	f.nextErr = err
	f.mu.Unlock()
}

// Payloads returns a snapshot of every payload Post() received, in order.
func (f *fakePoster) Payloads() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]any, len(f.payloads))
	copy(out, f.payloads)
	return out
}

// fakePublisher captures resource:invalidated hints emitted by
// SaveCoordinator. State-sync redesign (Task 13) replaced the former
// save:status SSE event with a hint — the SaveStatus payload is now
// read via GET /api/ndms/save-status. Tests snapshot the current state
// via sc.Status() and count hints to verify publish was invoked.
type fakePublisher struct {
	mu    sync.Mutex
	hints []events.ResourceInvalidatedEvent
}

func (p *fakePublisher) Publish(eventType string, data any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if eventType != "resource:invalidated" {
		return
	}
	if e, ok := data.(events.ResourceInvalidatedEvent); ok {
		p.hints = append(p.hints, e)
	}
}

// Hints returns every resource:invalidated hint emitted, in order.
func (p *fakePublisher) Hints() []events.ResourceInvalidatedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]events.ResourceInvalidatedEvent, len(p.hints))
	copy(out, p.hints)
	return out
}

// mockInvalidator counts InvalidateAll calls. Used in post-save settle
// tests. CalledAt records when invalidate fired so timing assertions
// can pin "sleep happened before invalidate".
type mockInvalidator struct {
	mu       sync.Mutex
	calls    int32
	calledAt time.Time
}

func (m *mockInvalidator) InvalidateAll() {
	atomic.AddInt32(&m.calls, 1)
	m.mu.Lock()
	m.calledAt = time.Now()
	m.mu.Unlock()
}

func (m *mockInvalidator) Calls() int32 { return atomic.LoadInt32(&m.calls) }

func (m *mockInvalidator) CalledAt() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calledAt
}

// --- Tests ---

func TestSaveCoordinator_SingleRequestTriggersSave(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 20*time.Millisecond, 100*time.Millisecond, 0, nil)

	sc.Request()
	time.Sleep(50 * time.Millisecond)

	if got := poster.Calls(); got != 1 {
		t.Errorf("Post calls: want 1, got %d", got)
	}
}

func TestSaveCoordinator_MultipleRequestsCoalesce(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 30*time.Millisecond, 500*time.Millisecond, 0, nil)

	for i := 0; i < 5; i++ {
		sc.Request()
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)

	if got := poster.Calls(); got != 1 {
		t.Errorf("Post calls: want 1 after burst, got %d", got)
	}
}

func TestSaveCoordinator_MaxWaitCapsDelay(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	// Tight maxWait; debounce is larger than the whole test window.
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 80*time.Millisecond, 0, nil)

	start := time.Now()
	// Issue Requests faster than debounce so debounce would never fire,
	// forcing maxWait to kick in.
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sc.Request()
			case <-stop:
				return
			}
		}
	}()

	// Wait a bit longer than maxWait.
	time.Sleep(140 * time.Millisecond)
	close(stop)

	got := poster.Calls()
	if got == 0 {
		t.Errorf("Post calls: want >=1 by maxWait, got 0 after %s", time.Since(start))
	}
	if got > 2 {
		t.Errorf("Post calls: want <=2 within %dms (maxWait=80ms bounded firing), got %d", 140, got)
	}
}

func TestSaveCoordinator_PublishesStatusTransitions(t *testing.T) {
	poster := &fakePoster{sleep: 20 * time.Millisecond}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 15*time.Millisecond, 100*time.Millisecond, 0, nil)

	sc.Request()
	time.Sleep(80 * time.Millisecond)

	// Expected sequence: pending -> saving -> idle. Each transition emits
	// a resource:invalidated hint with Resource="saveStatus" — we verify
	// at least three hints fired and that Status() lands on Idle.
	hints := pub.Hints()
	if len(hints) < 3 {
		t.Fatalf("hints: want >=3 (pending+saving+idle), got %d (%v)", len(hints), hints)
	}
	for i, h := range hints {
		if h.Resource != "saveStatus" {
			t.Errorf("hint[%d].Resource: want saveStatus, got %q", i, h.Resource)
		}
	}
	if st := sc.Status(); st.State != SaveStateIdle {
		t.Errorf("terminal state: want Idle, got %v", st.State)
	}
}

func TestSaveCoordinator_RetryOnFailure(t *testing.T) {
	var boom = errors.New("ndms timeout")

	poster := &fakePoster{}
	poster.SetError(boom)

	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 100*time.Millisecond, 0, nil)
	sc.SetRetryPolicy(20*time.Millisecond, 3) // 3 retries, 20ms apart

	sc.Request()
	// Первая попытка + 3 повтора = 4 вызова Post. Ждём СОБЫТИЕ, а не 130 мс:
	// под нагрузкой окно не выдерживалось, и тест краснел на «got 3».
	waitCalls(t, poster, 4, "повторы не состоялись")
	// Терминальное состояние приходит после последнего отказа, тоже не мгновенно.
	st := waitState(t, sc, SaveStateFailed)

	// Ровно 4: пятого повтора быть не должно — политика ограничена тремя.
	if got := poster.Calls(); got != 4 {
		t.Errorf("Post calls: want 4 (1 + 3 retries), got %d", got)
	}

	if st.LastError != boom.Error() {
		t.Errorf("LastError: want %q, got %q", boom.Error(), st.LastError)
	}
	if len(pub.Hints()) == 0 {
		t.Error("expected resource:invalidated hints to be published")
	}
}

// waitCalls ждёт, пока Post позовут не меньше n раз. Фиксированный sleep на
// этом месте — источник флейка: под нагрузкой (весь прогон CI идёт пакетами
// параллельно) первая попытка не успевает выстрелить в отведённое окно, тест
// снимает ошибку слишком рано, и вместо «отказ + повтор» получается один
// успешный вызов. Ожидание СОБЫТИЯ этого не знает.
func waitCalls(t *testing.T, p *fakePoster, n int32, why string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p.Calls() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s: вызовов Post %d, ждали >= %d", why, p.Calls(), n)
}

// waitState ждёт терминального состояния координатора. Как и waitCalls,
// заменяет фиксированный сон: состояние приходит ПОСЛЕ последней попытки, и
// «подождать миллисекунды» здесь ровно так же не выдерживает нагрузки.
func waitState(t *testing.T, sc *SaveCoordinator, want SaveState) SaveStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var st SaveStatus
	for time.Now().Before(deadline) {
		if st = sc.Status(); st.State == want {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("состояние %v не наступило, текущее %v", want, st.State)
	return st
}

func TestSaveCoordinator_RetrySucceedsClearsError(t *testing.T) {
	poster := &fakePoster{}
	// Fail first attempt, succeed on retry.
	poster.SetError(errors.New("first flake"))

	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 100*time.Millisecond, 0, nil)
	sc.SetRetryPolicy(20*time.Millisecond, 3)

	sc.Request()
	waitCalls(t, poster, 1, "первая попытка не состоялась")
	poster.SetError(nil) // следующая попытка проходит
	waitCalls(t, poster, 2, "повтора после отказа не было")

	// Терминальное состояние приходит ПОСЛЕ успешного повтора, тоже не мгновенно.
	waitState(t, sc, SaveStateIdle)
	if got := poster.Calls(); got != 2 {
		t.Errorf("Post calls: want 2 (1 fail + 1 success), got %d", got)
	}
}

func TestSaveCoordinator_FlushBypassesDebounce(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 1*time.Second, 0, nil)

	sc.Request()
	// Immediately Flush — debounce would otherwise keep Save pending.
	if err := sc.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := poster.Calls(); got != 1 {
		t.Errorf("Post calls after Flush: want 1, got %d", got)
	}
}

func TestSaveCoordinator_FlushClearsFailedState(t *testing.T) {
	poster := &fakePoster{}
	poster.SetError(errors.New("down"))

	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 50*time.Millisecond, 0, nil)
	sc.SetRetryPolicy(10*time.Millisecond, 1)

	sc.Request()
	time.Sleep(50 * time.Millisecond) // reach Failed state

	poster.SetError(nil)
	if err := sc.Flush(context.Background()); err != nil {
		t.Fatalf("Flush after Failed: %v", err)
	}

	if st := sc.Status(); st.State != SaveStateIdle {
		t.Errorf("state after Flush success: want Idle, got %v", st.State)
	}
}

func TestSaveCoordinator_FlushFailureGoesToFailed(t *testing.T) {
	poster := &fakePoster{}
	poster.SetError(errors.New("flash write failed"))
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 100*time.Millisecond, 500*time.Millisecond, 0, nil)

	err := sc.Flush(context.Background())
	if err == nil {
		t.Fatalf("Flush: want error, got nil")
	}

	st := sc.Status()
	if st.State != SaveStateFailed {
		t.Errorf("state after Flush failure from Idle: want Failed, got %v", st.State)
	}

	if len(pub.Hints()) == 0 {
		t.Error("expected at least one resource:invalidated hint")
	}
}

func TestSaveCoordinator_FlushConcurrentWithInFlightFire(t *testing.T) {
	// A fire() is mid-POST when Flush is called. saveMu serialises the
	// two POSTs but the state machine must not clobber itself, and the
	// terminal state must reflect Flush's outcome.
	//
	// Without the flushInProgress guard, fire()'s post-POST state write
	// would overwrite Flush's state.
	poster := &fakePoster{sleep: 60 * time.Millisecond}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 100*time.Millisecond, 0, nil)

	sc.Request()
	// Ждём ВХОДА fire в Post, а не «наверное, уже вошёл через 25 мс». Сон
	// здесь давал ложный ЗЕЛЁНЫЙ: под нагрузкой fire не успевал войти, Flush
	// шёл в одиночку, и сценарий «Flush поверх летящего fire» — ради которого
	// тест и написан — не воспроизводился вовсе. Счётчик растёт ВНУТРИ Post
	// до его сна, поэтому возврат waitCalls и означает «fire внутри».
	waitCalls(t, poster, 1, "fire не вошёл в Post — сценарий не воспроизведён")

	// Flush while fire is blocked on the slow Post.
	if err := sc.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Горутина fire дозавершается — ждём состояние, а не миллисекунды.
	st := waitState(t, sc, SaveStateIdle)

	// ГРАНИЦА ЭТОГО ТЕСТА: он НЕ пинит гард flushInProgress — снятие любой из
	// двух его точек оставляет тест зелёным, потому что исход сходится к Idle
	// несколькими путями. Он держит другое, и это тоже нужное: сценарий
	// «Flush поверх ЛЕТЯЩЕГО fire» действительно воспроизводится (ждём сигнал
	// входа в Post, а не спим наугад), состояние терминальное, pending снят.
	// Сами гарды пинуют TestSaveCoordinator_LateFireDoesNotResurrectRetry
	// (хвостовой) и TestSaveCoordinator_FireDispatchedDuringFlushYields
	// (входной) — каждому нужен СВОЙ порядок событий (CF11).
	if st.PendingCount != 0 {
		t.Errorf("pending after Flush: want 0, got %d", st.PendingCount)
	}

	// Hints are just invalidation nudges now; verify they were emitted
	// for both the Flush-driven transitions and the fire() path. The
	// flushInProgress guard in setStateLocked's caller prevents fire
	// from clobbering state after Flush — we rely on Status() above.
	if len(pub.Hints()) == 0 {
		t.Fatal("no hints published")
	}
}

func TestSaveCoordinator_StatusSnapshot(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 20*time.Millisecond, 100*time.Millisecond, 0, nil)

	// Fresh coordinator: Idle, 0 pending.
	if st := sc.Status(); st.State != SaveStateIdle || st.PendingCount != 0 {
		t.Errorf("fresh: want Idle/0, got %v/%d", st.State, st.PendingCount)
	}

	sc.Request()
	sc.Request()
	st := sc.Status()
	if st.State != SaveStatePending {
		t.Errorf("after Request: want Pending, got %v", st.State)
	}
	if st.PendingCount != 2 {
		t.Errorf("PendingCount: want 2, got %d", st.PendingCount)
	}

	// Let Save fire.
	time.Sleep(50 * time.Millisecond)
	if st := sc.Status(); st.State != SaveStateIdle {
		t.Errorf("after fire: want Idle, got %v", st.State)
	}
}

// --- Post-save settle tests (fire path) ---

func TestSaveCoordinator_fire_OnSuccess_InvalidatesRunningConfig(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	inv := &mockInvalidator{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 100*time.Millisecond,
		20*time.Millisecond, inv)

	sc.Request()
	// debounce 10ms + post 0ms + settle 20ms = ~30ms. Wait 100ms for safety.
	time.Sleep(100 * time.Millisecond)

	if got := inv.Calls(); got != 1 {
		t.Errorf("invalidator calls: want 1, got %d", got)
	}
}

func TestSaveCoordinator_fire_OnSuccess_SettlesBeforeInvalidate(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	inv := &mockInvalidator{}
	settle := 50 * time.Millisecond
	sc := NewSaveCoordinator(poster, pub, 5*time.Millisecond, 100*time.Millisecond,
		settle, inv)

	t0 := time.Now()
	sc.Request()
	time.Sleep(150 * time.Millisecond)

	if inv.Calls() != 1 {
		t.Fatalf("invalidator should be called once, got %d", inv.Calls())
	}
	elapsed := inv.CalledAt().Sub(t0)
	// debounce 5ms + (instant POST) + settle 50ms = >= 55ms.
	// Allow lower bound a few ms below to absorb scheduler jitter on slow CI.
	if elapsed < settle {
		t.Errorf("invalidate fired too early: elapsed %s, want >= %s", elapsed, settle)
	}
}

func TestSaveCoordinator_fire_OnSuccess_PublishesSettledHint(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	inv := &mockInvalidator{}
	sc := NewSaveCoordinator(poster, pub, 5*time.Millisecond, 100*time.Millisecond,
		10*time.Millisecond, inv)

	sc.Request()
	time.Sleep(80 * time.Millisecond)

	// Filter for save-settled hint specifically — fire() also publishes
	// state-change hints via setStateLocked.
	var settled int
	for _, h := range pub.Hints() {
		if h.Resource == "saveStatus" && h.Reason == "save-settled" {
			settled++
		}
	}
	if settled != 1 {
		t.Errorf("save-settled hints: want 1, got %d (all hints: %+v)",
			settled, pub.Hints())
	}
}

func TestSaveCoordinator_fire_OnFailure_DoesNotInvalidate(t *testing.T) {
	poster := &fakePoster{}
	poster.SetError(errors.New("boom"))
	pub := &fakePublisher{}
	inv := &mockInvalidator{}
	sc := NewSaveCoordinator(poster, pub, 5*time.Millisecond, 100*time.Millisecond,
		10*time.Millisecond, inv)
	sc.SetRetryPolicy(50*time.Millisecond, 0) // disable retry — single attempt then fail

	sc.Request()
	time.Sleep(100 * time.Millisecond)

	if got := inv.Calls(); got != 0 {
		t.Errorf("invalidator should not be called on failure, got %d calls", got)
	}
}

func TestSaveCoordinator_fire_ZeroSettleDelay_SkipsSettle(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	inv := &mockInvalidator{}
	sc := NewSaveCoordinator(poster, pub, 5*time.Millisecond, 100*time.Millisecond,
		0, inv) // settleDelay = 0

	sc.Request()
	time.Sleep(80 * time.Millisecond)

	if got := inv.Calls(); got != 0 {
		t.Errorf("settleDelay=0 should skip invalidate, got %d calls", got)
	}
	for _, h := range pub.Hints() {
		if h.Reason == "save-settled" {
			t.Errorf("settleDelay=0 should not publish save-settled hint, got %+v", h)
		}
	}
}

func TestSaveCoordinator_fire_NilInvalidator_SkipsSettle(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 5*time.Millisecond, 100*time.Millisecond,
		20*time.Millisecond, nil) // invalidator = nil

	sc.Request()
	time.Sleep(80 * time.Millisecond)

	// No invalidator, but also no panic — and no save-settled hint either.
	for _, h := range pub.Hints() {
		if h.Reason == "save-settled" {
			t.Errorf("nil invalidator should not publish save-settled hint, got %+v", h)
		}
	}
}

func TestSaveCoordinator_fire_Retry_InvalidatesOnceAfterFinalSuccess(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	inv := &mockInvalidator{}
	sc := NewSaveCoordinator(poster, pub, 5*time.Millisecond, 100*time.Millisecond,
		10*time.Millisecond, inv)
	sc.SetRetryPolicy(20*time.Millisecond, 3)

	// First attempt fails, second attempt succeeds.
	poster.SetError(errors.New("transient"))
	sc.Request()
	time.Sleep(30 * time.Millisecond) // first fire fails, retry scheduled

	poster.SetError(nil) // unblock retry
	time.Sleep(80 * time.Millisecond)

	if got := inv.Calls(); got != 1 {
		t.Errorf("invalidator should be called once after final success, got %d", got)
	}
}

// --- Post-save settle tests (Flush path) ---

func TestSaveCoordinator_Flush_OnSuccess_InvalidatesImmediately(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	inv := &mockInvalidator{}
	// settleDelay 1s, but Flush should invalidate WITHOUT sleep.
	sc := NewSaveCoordinator(poster, pub, 5*time.Millisecond, 100*time.Millisecond,
		1*time.Second, inv)

	t0 := time.Now()
	if err := sc.Flush(context.Background()); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}
	elapsed := time.Since(t0)

	if got := inv.Calls(); got != 1 {
		t.Errorf("invalidator calls: want 1, got %d", got)
	}
	if elapsed >= 500*time.Millisecond {
		t.Errorf("Flush should not sleep — elapsed %s, want < 500ms", elapsed)
	}
}

func TestSaveCoordinator_Flush_OnFailure_DoesNotInvalidate(t *testing.T) {
	poster := &fakePoster{}
	poster.SetError(errors.New("boom"))
	pub := &fakePublisher{}
	inv := &mockInvalidator{}
	sc := NewSaveCoordinator(poster, pub, 5*time.Millisecond, 100*time.Millisecond,
		10*time.Millisecond, inv)

	if err := sc.Flush(context.Background()); err == nil {
		t.Fatalf("Flush should have returned error")
	}

	if got := inv.Calls(); got != 0 {
		t.Errorf("invalidator should not be called on Flush failure, got %d", got)
	}
}

func TestSaveCoordinator_Flush_NilInvalidator_DoesNotPanic(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	// invalidator = nil
	sc := NewSaveCoordinator(poster, pub, 5*time.Millisecond, 100*time.Millisecond,
		10*time.Millisecond, nil)

	if err := sc.Flush(context.Background()); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}
	// Success — no panic.
}

// CF11: гард `flushInProgress` в ХВОСТЕ fire() — тот, что после POST. Смысл
// его в том, что терминальным состоянием владеет Flush, и опоздавший fire не
// имеет права ни переписать исход, ни ВОСКРЕСИТЬ цикл ретраев: Flush
// останавливает таймер в самом начале, а fire на своём отказе завёл бы новый
// уже после этого — и на роутер ушёл бы лишний Save, которого никто не просил.
//
// Прежний тест (`…FlushConcurrentWithInFlightFire`) гард не различал: с
// maxRetries=0 отказавший fire уходил в Failed без таймера, и оба исхода
// сходились к Idle. Различитель здесь — ТРЕТИЙ POST: он существует только
// без гарда.
//
// Порядок детерминирован: Flush блокируется на saveMu, пока летит POST от
// fire, поэтому хвост fire выполняется, когда `flushInProgress` уже взведён и
// ещё не снят (Flush в это время сидит в своём POST).
//
// Отрицательное утверждение на фиксированном окне: ждать нечего. Направление
// риска — ложный ЗЕЛЁНЫЙ, если Flush-POST растянется дольше `retryDelay`
// (150 мс) и воскрешённый ретрай заглушит входной гард; под нагрузкой на
// одном ядре мутант ловится 10/10 (2026-09-02). При флейке этого класса —
// первый подозреваемый. Часы координатора (`time.AfterFunc`,
// `save.go:150,221`) не инжектируются — детерминированный вариант требует
// шва таймера; решение — принять.
func TestSaveCoordinator_LateFireDoesNotResurrectRetry(t *testing.T) {
	poster := &fakePoster{sleep: 60 * time.Millisecond}
	poster.SetError(errors.New("NDMS отказал"))
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 100*time.Millisecond, 0, nil)
	// Ретрай ЕСТЬ (иначе гарду нечего защищать) и он ПОЗЖЕ конца Flush:
	// иначе воскрешённый ретрай приходит, пока Flush ещё в своём POST, и его
	// глушит ВХОДНОЙ гард — тогда снятие хвостового ничем не проявляется.
	sc.SetRetryPolicy(150*time.Millisecond, 3)

	sc.Request()
	waitCalls(t, poster, 1, "fire не вошёл в Post — сценарий не воспроизведён")
	// Ошибку первой попытки fakePoster снял ДО инкремента счётчика, поэтому
	// снимать её теперь безопасно: POST от Flush пройдёт успешно (CF6).
	poster.SetError(nil)

	if err := sc.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if st := waitState(t, sc, SaveStateIdle); st.PendingCount != 0 {
		t.Errorf("pending после Flush: %d, ждали 0", st.PendingCount)
	}

	// Отрицательное утверждение — ждать нечего, поэтому окно фиксированное:
	// воскрешённый ретрай пришёл бы на 150-й мс от хвоста fire, то есть
	// внутри окна с запасом.
	time.Sleep(400 * time.Millisecond)
	if got := poster.Calls(); got != 2 {
		t.Errorf("POST'ов %d, ждали 2 (fire + Flush): опоздавший fire воскресил ретрай", got)
	}
	if st := sc.Status(); st.State != SaveStateIdle {
		t.Errorf("итоговое состояние %v, ждали Idle: исход Flush переписан", st.State)
	}
}

// Второй гард — ВХОДНОЙ, в голове fire(). Он про другой порядок: fire
// диспетчеризован таймером, пока Flush уже начал свой POST. Без гарда fire
// возьмёт saveMu следом за Flush и отправит ВТОРОЙ Save, которого никто не
// просил, — на роутере это лишняя запись конфигурации.
//
// Отрицательное утверждение на фиксированном окне 200 мс: пока Flush держит
// POST, диспетчеризованный тем временем fire() обязан упереться во ВХОДНОЙ
// гард `flushInProgress` (save.go:169-171) и вернуться ДО `saveMu.Lock`/
// `Post` — второго POST быть не должно. Ретрая на этом пути нет:
// `SetRetryPolicy` не вызывается, а входной гард отсекает fire() раньше
// POST, до ветки, что заводит повторный таймер при ошибке. Риск у этого окна
// — направленный иначе, чем в соседнем тесте: если под нагрузкой таймер
// fire() (`time.AfterFunc`, 10 мс) реально диспетчеризуется ПОЗЖЕ конца
// Flush-POST (120 мс), `flushInProgress` к этому моменту уже снят, входной
// гард не срабатывает, и POST уходит по-настоящему — тест падает КРАСНЫМ на
// корректном коде (это уже не гонка «fire во время Flush», а независимый
// Request после его завершения). Часы координатора (`time.AfterFunc`) не
// инжектируются — шов таймера не заводим, решение принято.
func TestSaveCoordinator_FireDispatchedDuringFlushYields(t *testing.T) {
	poster := &fakePoster{sleep: 120 * time.Millisecond}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 100*time.Millisecond, 0, nil)

	// Flush идёт в фоне и держит POST 120 мс.
	done := make(chan error, 1)
	go func() { done <- sc.Flush(context.Background()) }()
	waitCalls(t, poster, 1, "Flush не вошёл в Post")

	// Пока Flush в POST — просим Save: его таймер (10 мс) сработает внутри
	// окна Flush, и fire обязан уступить.
	sc.Request()
	if err := <-done; err != nil {
		t.Fatalf("Flush: %v", err)
	}
	waitState(t, sc, SaveStateIdle)

	time.Sleep(200 * time.Millisecond)
	if got := poster.Calls(); got != 1 {
		t.Errorf("POST'ов %d, ждали 1 (только Flush): fire не уступил владение состоянием", got)
	}
}
