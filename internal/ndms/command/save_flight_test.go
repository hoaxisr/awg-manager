package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// flightPoster — Poster с моментами вызовов; err — отказ первых errFor POST.
type flightPoster struct {
	mu     sync.Mutex
	times  []time.Time
	errFor int
	onPost func()
}

func (p *flightPoster) Post(context.Context, any) (json.RawMessage, error) {
	p.mu.Lock()
	p.times = append(p.times, time.Now())
	fail := p.errFor > 0
	if fail {
		p.errFor--
	}
	on := p.onPost
	p.mu.Unlock()
	if on != nil {
		on()
	}
	if fail {
		return nil, errors.New("NDMS отказал")
	}
	return json.RawMessage(`{}`), nil
}

func (p *flightPoster) calls() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.times...)
}

func (p *flightPoster) waitCalls(t *testing.T, n int) []time.Time {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c := p.calls(); len(c) >= n {
			return c
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("POST %d, ждали %d", len(p.calls()), n)
	return nil
}

type warnRec struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnRec) Warnf(format string, args ...any) {
	w.mu.Lock()
	w.msgs = append(w.msgs, fmt.Sprintf(format, args...))
	w.mu.Unlock()
}

func (w *warnRec) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.msgs...)
}

// noBusSave — координатор без шины событий (харнессы команд): fallback 0,
// иначе каждый полёт ждал бы saveFallback и горутина fire пережила бы тест.
func noBusSave(sc *SaveCoordinator) *SaveCoordinator {
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)
	return sc
}

// newFlightSC — координатор с подключённой шиной, uptime 100, debounce 0;
// потолки — 5 с (тест, ждущий потолка, ставит свои). Уборка сжимает потолки,
// гасит таймер и закрывает летящий полёт: горутина fire не переживает тест.
func newFlightSC(t *testing.T, p Poster) *SaveCoordinator {
	t.Helper()
	sc := NewSaveCoordinator(p, nil, 0, time.Hour, 0, nil)
	sc.SetSaveTimings(5*time.Second, 5*time.Second, 0)
	sc.SetUptimeReader(func() float64 { return 100 })
	sc.OnBusState(true)
	t.Cleanup(func() { drainSC(t, sc) })
	return sc
}

func drainSC(t *testing.T, sc *SaveCoordinator) {
	// Полёт, который откроет fire, уже снятый с таймера, кончится сразу.
	sc.SetSaveTimings(time.Millisecond, time.Millisecond, 0)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sc.mu.Lock()
		if sc.timer != nil {
			sc.timer.Stop()
			sc.timer = nil
		}
		if f := sc.cur; f != nil && !f.done {
			sc.completeLocked(f)
		}
		sc.mu.Unlock()
		select {
		case sc.saveSem <- struct{}{}:
			<-sc.saveSem
			return
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Errorf("координатор не освободился за 3 с")
}

// waitFlight ждёт открытого полёта.
func waitFlight(t *testing.T, sc *SaveCoordinator) *saveFlight {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sc.mu.Lock()
		f := sc.cur
		sc.mu.Unlock()
		if f != nil {
			return f
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("полёт не открылся")
	return nil
}

// holdAsync — HoldForRemoval в горутине; результат — в канал.
type holdResult struct {
	release func()
	err     error
	at      time.Time
}

func holdAsync(sc *SaveCoordinator, ctx context.Context) <-chan holdResult {
	ch := make(chan holdResult, 1)
	go func() {
		rel, err := sc.HoldForRemoval(ctx)
		ch <- holdResult{rel, err, time.Now()}
	}()
	return ch
}

func requireBlocked(t *testing.T, ch <-chan holdResult, d time.Duration, why string) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("%s: удержание прошло (err=%v)", why, r.err)
	case <-time.After(d):
	}
}

func requirePassed(t *testing.T, ch <-chan holdResult, d time.Duration, why string) holdResult {
	t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("%s: %v", why, r.err)
		}
		r.release()
		return r
	case <-time.After(d):
		t.Fatalf("%s: удержание не прошло за %s", why, d)
	}
	return holdResult{}
}

func (s *SaveCoordinator) holdsNow() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holds
}

// Полёт закрывает только событие не раньше POST (raise ≥ t0).
// Мутация: закрывать по любому событию → удержание проходит по raise 99, красный.
func TestSave_FlightClosesOnEventAtOrAfterPost(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	ch := holdAsync(sc, context.Background())
	sc.OnConfigurationSaved(99.5) // чужое сохранение до нашего POST
	requireBlocked(t, ch, 50*time.Millisecond, "событие раньше POST")
	sc.OnConfigurationSaved(100)
	requirePassed(t, ch, time.Second, "событие в момент POST")
}

// Удержание ждёт полёт и проходит по его событию.
func TestSave_HoldWaitsFlight(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	ch := holdAsync(sc, context.Background())
	requireBlocked(t, ch, 50*time.Millisecond, "полёт открыт")
	sc.OnConfigurationSaved(104)
	requirePassed(t, ch, time.Second, "событие пришло")
}

// Событие не пришло — потолок закрывает полёт с Warn.
// Мутация: без потолка → удержание висит, красный по таймауту.
func TestSave_FlightCapWarn(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	w := &warnRec{}
	sc.SetLogger(w)
	sc.SetSaveTimings(80*time.Millisecond, 5*time.Second, 0)
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	requirePassed(t, holdAsync(sc, context.Background()), time.Second, "потолок")
	if msgs := w.all(); len(msgs) != 1 || !strings.Contains(msgs[0], "ConfigurationSaved") {
		t.Fatalf("Warn %q", msgs)
	}
}

// Обрыв шины в полёте — закрытие через fallback от POST, не через потолок.
// Мутация: OnBusState не трогает полёт → ждёт потолок 5 с, красный.
func TestSave_BusDownDuringFlight_Fallback(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	sc.SetSaveTimings(5*time.Second, 100*time.Millisecond, 0)
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	ch := holdAsync(sc, context.Background())
	sc.OnBusState(false)
	requirePassed(t, ch, time.Second, "fallback после обрыва")
}

// Шина отключена при POST — полёт unknown сразу: событие (после
// переподключения) его не закрывает, закрывает fallback.
func TestSave_NoBusAtPost_Fallback(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	sc.SetSaveTimings(5*time.Second, 150*time.Millisecond, 0)
	sc.OnBusState(false)
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	ch := holdAsync(sc, context.Background())
	sc.OnBusState(true)
	sc.OnConfigurationSaved(200)
	requireBlocked(t, ch, 50*time.Millisecond, "unknown-полёт закрыт событием")
	requirePassed(t, ch, time.Second, "fallback")
}

// Б1: uptime не прочитан (t0 == 0) — полёт unknown, чужое событие raise 1 его
// не закрывает. Мутация: без проверки t0 == 0 → удержание проходит по
// первому событию, красный.
func TestSave_UptimeZero_Unknown(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	sc.SetUptimeReader(func() float64 { return 0 })
	sc.SetSaveTimings(5*time.Second, 150*time.Millisecond, 0)
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	ch := holdAsync(sc, context.Background())
	sc.OnConfigurationSaved(1)
	requireBlocked(t, ch, 50*time.Millisecond, "t0 == 0")
	requirePassed(t, ch, time.Second, "fallback")
}

// Н5: обрыв позже t0 + fallback — полёт закрыт сразу (отрицательная
// задержка). Мутация: AfterFunc только при d > 0 → ждёт потолок, красный.
func TestSave_BusDownLate_ClosesNow(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	sc.SetSaveTimings(5*time.Second, 30*time.Millisecond, 0)
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	time.Sleep(80 * time.Millisecond)
	ch := holdAsync(sc, context.Background())
	requireBlocked(t, ch, 20*time.Millisecond, "шина жива, события нет")
	sc.OnBusState(false)
	requirePassed(t, ch, 200*time.Millisecond, "срок fallback прошёл")
}

// Н13: отмена ctx в ожидании — ошибка, удержание снято, следующий Request
// стреляет. Мутация: без holds-- на отмене → fire отложен навсегда, красный.
func TestSave_HoldCtxCancel_ReleasesCount(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := sc.HoldForRemoval(ctx); err == nil {
		t.Fatal("удержание без ошибки при отменённом ctx")
	}
	if n := sc.holdsNow(); n != 0 {
		t.Fatalf("holds %d после отмены", n)
	}
	sc.OnConfigurationSaved(104)
	sc.Request()
	p.waitCalls(t, 2)
}

// Н14: событие без полёта (чужое сохранение в простое) — без паники и без
// побочек. Мутация: разыменовать cur без проверки → паника, красный.
func TestSave_EventWithoutFlight_NoOp(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	sc.OnConfigurationSaved(500)
	if req, saved := sc.Epoch(); req != 0 || saved != 0 {
		t.Fatalf("эпохи %d/%d после чужого события", req, saved)
	}
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	requireBlocked(t, holdAsync(sc, context.Background()), 30*time.Millisecond, "полёт после чужого события")
}

// Saved растёт при закрытии полёта, не при POST.
// Мутация: saved при POST → FlushPendingSave пропустит Flush при
// незаконченной записи, красный.
func TestSave_Epochs(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	if req, saved := sc.Epoch(); req != 1 || saved != 0 {
		t.Fatalf("в полёте: %d/%d, ждали 1/0", req, saved)
	}
	sc.OnConfigurationSaved(104)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, saved := sc.Epoch(); saved == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("saved не вырос после события")
}

// Пока удержание держится, fire не стреляет; release взводит его.
// Мутация: fire без проверки holds → POST под удержанием, красный.
func TestSave_HoldDefersFire(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	obs := make(chan bool, 4)
	sc.SetFireObserver(func(d bool) { obs <- d })
	rel, err := sc.HoldForRemoval(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sc.Request()
	if d := <-obs; !d {
		t.Fatal("fire под удержанием не отложен")
	}
	if n := len(p.calls()); n != 0 {
		t.Fatalf("POST под удержанием: %d", n)
	}
	rel()
	p.waitCalls(t, 1)
}

// Вложенные удержания: отпускание внутреннего fire не взводит.
// Мутация: release взводит таймер при любом отпускании → fire зовётся под
// внешним удержанием, красный.
func TestSave_NestedHolds(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	var fires atomic.Int32
	sc.SetFireObserver(func(bool) { fires.Add(1) })
	outer, _ := sc.HoldForRemoval(context.Background())
	inner, _ := sc.HoldForRemoval(context.Background())
	sc.Request()
	deadline := time.Now().Add(time.Second)
	for fires.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	inner()
	time.Sleep(50 * time.Millisecond)
	if n := fires.Load(); n != 1 {
		t.Fatalf("fire зван %d раз под внешним удержанием, ждали 1", n)
	}
	outer()
	p.waitCalls(t, 1)
}

// Наше сохранение не раньше afterRemoval после нашего сноса.
// Мутация: убрать проверку паузы в fire → POST сразу, красный.
func TestSave_FireNotBefore5sAfterRemoval(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	const gap = 150 * time.Millisecond
	sc.SetSaveTimings(5*time.Second, 5*time.Second, gap)
	sc.NoteRemoval()
	removed := time.Now()
	sc.Request()
	at := p.waitCalls(t, 1)[0]
	if d := at.Sub(removed); d < gap {
		t.Fatalf("POST через %s после сноса, ждали ≥ %s", d, gap)
	}
}

// Flush ждёт удержаний, затем паузу после сноса.
// Мутация: Flush не смотрит lastRemovalAt → POST раньше паузы, красный.
func TestSave_FlushWaitsHoldsThenRemovalGap(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	const gap = 150 * time.Millisecond
	sc.SetSaveTimings(5*time.Second, 5*time.Second, gap)
	rel, _ := sc.HoldForRemoval(context.Background())
	done := make(chan error, 1)
	go func() { done <- sc.Flush(context.Background()) }()
	time.Sleep(30 * time.Millisecond)
	if n := len(p.calls()); n != 0 {
		t.Fatalf("Flush под удержанием: POST %d", n)
	}
	sc.NoteRemoval()
	removed := time.Now()
	rel()
	at := p.waitCalls(t, 1)[0]
	if d := at.Sub(removed); d < gap {
		t.Fatalf("Flush POST через %s после сноса, ждали ≥ %s", d, gap)
	}
	sc.OnConfigurationSaved(104)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Дедлок трёх участников: Flush держит saveSem и ждёт удержаний, таймер fire
// стреляет, новый holder приходит. Верный fire стоит на saveSem без полёта —
// holder проходит, Flush идёт. Мутация: открыть полёт в fire до saveSem →
// holder ждёт полёт, Flush ждёт holder'а, fire ждёт Flush, красный по времени.
func TestSave_NoDeadlock_FlushHoldsSaveMu(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	p.onPost = func() { sc.OnConfigurationSaved(1e9) } // событие конца — на каждый POST
	start := time.Now()
	h1, _ := sc.HoldForRemoval(context.Background())
	done := make(chan error, 1)
	go func() { done <- sc.Flush(context.Background()) }()
	time.Sleep(20 * time.Millisecond) // Flush взял saveSem и ждёт удержаний
	sc.Request()
	time.Sleep(20 * time.Millisecond) // fire стоит на saveSem
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h2, err := sc.HoldForRemoval(ctx)
	if err != nil {
		t.Fatalf("holder ждал чужой полёт: %v", err)
	}
	h2()
	h1()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("три участника разошлись за %s", d)
	}
}

// Владение полётом: Flush, пришедший во время полёта fire, открывает свой
// только после saveSem — полёт fire закрыт его событием, cur указывает на
// полёт Flush, пока тот летит. Мутация: Flush открывает полёт до saveSem →
// событие закрывает полёт Flush, fire ждёт потолок, красный.
func TestSave_FlightOwnership_FireYieldsToFlush(t *testing.T) {
	p := &flightPoster{}
	sc := newFlightSC(t, p)
	sc.Request()
	p.waitCalls(t, 1)
	f1 := waitFlight(t, sc)
	done := make(chan error, 1)
	go func() { done <- sc.Flush(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	sc.mu.Lock()
	cur := sc.cur
	sc.mu.Unlock()
	if cur != f1 {
		t.Fatal("Flush открыл полёт, пока летит полёт fire")
	}
	sc.OnConfigurationSaved(104)
	p.waitCalls(t, 2)
	f2 := waitFlightOther(t, sc, f1)
	if !f1.done || f2.done {
		t.Fatalf("f1.done=%v f2.done=%v", f1.done, f2.done)
	}
	sc.OnConfigurationSaved(105)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.cur != nil {
		t.Fatal("cur не снят после Flush")
	}
}

func waitFlightOther(t *testing.T, sc *SaveCoordinator, not *saveFlight) *saveFlight {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sc.mu.Lock()
		f := sc.cur
		sc.mu.Unlock()
		if f != nil && f != not {
			return f
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Flush не открыл свой полёт")
	return nil
}

// Отказ POST закрывает полёт: удержание, ждавшее его во время POST, идёт
// сразу, а не по потолку. Мутация: не закрывать полёт на отказе → удержание
// ждёт потолок, красный.
func TestSave_PostRefused_ClosesFlight(t *testing.T) {
	gate := make(chan struct{})
	p := &flightPoster{errFor: 1, onPost: func() { <-gate }}
	sc := newFlightSC(t, p)
	sc.SetRetryPolicy(time.Hour, 3)
	sc.Request()
	p.waitCalls(t, 1)
	waitFlight(t, sc)
	ch := holdAsync(sc, context.Background())
	requireBlocked(t, ch, 20*time.Millisecond, "POST ещё летит")
	close(gate)
	requirePassed(t, ch, 200*time.Millisecond, "полёт после отказа")
}

// Полёт открывается только при holds == 0 — в той же секции mu, где
// проверены удержания. Инвариант проверяется в точке открытия через шов
// часов (uptime зовётся под mu при открытии); holder'ы крутятся без пауз,
// так что окно между проверкой и открытием, будь оно, занято удержанием.
// Детерминированно: у верного кода holds там 0 всегда, ложного красного нет.
// Мутация: Unlock/Lock между проверкой holds и открытием полёта → красный.
func TestSave_FireHoldRace(t *testing.T) {
	p := &flightPoster{}
	sc := NewSaveCoordinator(p, nil, 0, time.Hour, 0, nil)
	sc.SetSaveTimings(5*time.Second, 0, 0) // без шины: полёт кончается сразу
	var opens, bad atomic.Int32
	sc.SetUptimeReader(func() float64 {
		opens.Add(1)
		if sc.holds > 0 { // под mu
			bad.Add(1)
		}
		return 100
	})
	t.Cleanup(func() { drainSC(t, sc) })
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if rel, err := sc.HoldForRemoval(context.Background()); err == nil {
					rel()
				}
			}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for opens.Load() < 300 && time.Now().Before(deadline) {
		sc.Request()
		time.Sleep(100 * time.Microsecond)
	}
	close(stop)
	wg.Wait()
	if n := opens.Load(); n < 300 {
		t.Fatalf("полётов %d за 5 с, ждали 300", n)
	}
	if n := bad.Load(); n != 0 {
		t.Fatalf("полёт открыт при удержании %d раз", n)
	}
}
