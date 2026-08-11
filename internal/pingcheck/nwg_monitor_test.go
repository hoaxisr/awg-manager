package pingcheck

import (
	"context"
	"testing"
	"time"
)

func newTestNwgMonitor(buf *LogBuffer) *nwgMonitor {
	return &nwgMonitor{
		tunnelID:   "tun-nwg-1",
		tunnelName: "NWG Test",
		threshold:  3,
		logBuffer:  buf,
	}
}

func TestNwgDelta_SuccessIncrement(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// First poll emits INIT.
	m.processDelta(0, 0, "pass", true)
	if buf.Len() != 1 {
		t.Fatalf("after baseline: got %d entries, want 1 (INIT)", buf.Len())
	}

	// Second poll: 3 new successes.
	m.processDelta(0, 3, "pass", true)
	if buf.Len() != 4 {
		t.Fatalf("after success increment: got %d entries, want 4 (INIT + 3)", buf.Len())
	}

	entries := buf.GetAll()
	for i, e := range entries {
		if !e.Success {
			t.Errorf("entry[%d].Success = false, want true", i)
		}
		if e.Latency != -1 {
			t.Errorf("entry[%d].Latency = %d, want -1", i, e.Latency)
		}
		if e.Backend != "nativewg" {
			t.Errorf("entry[%d].Backend = %q, want %q", i, e.Backend, "nativewg")
		}
		if e.TunnelID != "tun-nwg-1" {
			t.Errorf("entry[%d].TunnelID = %q, want %q", i, e.TunnelID, "tun-nwg-1")
		}
	}
}

func TestNwgDelta_FailIncrement(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline with status "fail"/0/0 — a warmup poll that emits NO initial
	// entry (provisional NDMS fail before the interval ticks).
	m.processDelta(0, 0, "fail", true)

	// 2 new failures, same status — no state change entry.
	m.processDelta(2, 0, "fail", true)
	if buf.Len() != 2 {
		t.Fatalf("got %d entries, want 2 (warmup suppressed + 2 fails)", buf.Len())
	}

	entries := buf.GetAll()
	for i, e := range entries {
		if e.Success {
			t.Errorf("entry[%d].Success = true, want false", i)
		}
		if e.Backend != "nativewg" {
			t.Errorf("entry[%d].Backend = %q, want %q", i, e.Backend, "nativewg")
		}
	}
}

func TestNwgDelta_CounterReset(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline with 10 successes.
	m.processDelta(0, 10, "pass", true)
	if buf.Len() != 1 {
		t.Fatalf("after baseline: got %d entries, want 1 (INIT)", buf.Len())
	}

	// Counter reset: success went from 10 down to 2.
	// Should treat 2 as the delta (counter was reset).
	m.processDelta(0, 2, "pass", true)
	if buf.Len() != 3 {
		t.Fatalf("after counter reset: got %d entries, want 3 (INIT + 2)", buf.Len())
	}
}

func TestNwgDelta_StatusChange(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline: 5 successes, status pass.
	m.processDelta(0, 5, "pass", true)

	// 3 new failures, status changes to fail.
	m.processDelta(3, 5, "fail", true)

	// Expect: INIT + 3 fail entries + 1 state change entry = 5.
	if buf.Len() != 5 {
		t.Fatalf("got %d entries, want 5", buf.Len())
	}

	entries := buf.GetAll()
	// Entries are newest-first. The state change entry is the last one added.
	stateEntry := entries[0] // newest = state change
	if stateEntry.StateChange != "status_fail" {
		t.Errorf("StateChange = %q, want %q", stateEntry.StateChange, "status_fail")
	}
	if stateEntry.Success {
		t.Errorf("state change entry Success = true, want false")
	}
}

func TestNwgDelta_MixedFailAndSuccess(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline.
	m.processDelta(0, 0, "pass", true)

	// 2 fails + 1 success in startup phase.
	// Startup filter suppresses the transient fail burst, keeping only success.
	m.processDelta(2, 1, "pass", true)
	if buf.Len() != 2 {
		t.Fatalf("got %d entries, want 2 (INIT + 1 success)", buf.Len())
	}

	entries := buf.GetAll()
	// Order in buffer after suppression: INIT then success. GetAll reverses.
	if !entries[0].Success {
		t.Errorf("entries[0] (newest) should be success")
	}
	if entries[1].StateChange != "initial" {
		t.Errorf("entries[1] should be INIT")
	}
}

func TestNwgDelta_NoDelta_NoEntries(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline.
	m.processDelta(0, 5, "pass", true)

	// Same counters, no change.
	m.processDelta(0, 5, "pass", true)
	if buf.Len() != 1 {
		t.Fatalf("got %d entries, want 1 (INIT only)", buf.Len())
	}
}

func TestNwgDelta_RestartDetected_CounterReset(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline: tunnel running, checks passing.
	m.processDelta(0, 5, "pass", true)
	if m.restartDetected {
		t.Fatal("restartDetected should be false at init")
	}

	// Failures accumulate.
	m.processDelta(3, 5, "fail", true)
	if m.restartDetected {
		t.Fatal("restartDetected should be false during active failures")
	}

	// NDMS restarts interface: counters zeroed, status still "fail".
	m.processDelta(0, 0, "fail", true)
	if !m.restartDetected {
		t.Fatal("restartDetected should be true after counter reset")
	}

	// First success after restart clears the flag.
	m.processDelta(0, 1, "pass", true)
	if m.restartDetected {
		t.Fatal("restartDetected should be cleared after first success")
	}
}

func TestNwgDelta_RestartDetected_BoundTransition(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline: tunnel running.
	m.processDelta(0, 5, "pass", true)

	// Failures.
	m.processDelta(3, 5, "fail", true)

	// Interface goes down (bound=false).
	m.processDelta(0, 0, "fail", false)
	// restartDetected may or may not be set here (counters zeroed + prevFail>0),
	// but the key transition is the next poll:

	// Interface comes back up (bound transition false→true), counters zeroed.
	m.processDelta(0, 0, "fail", true)
	if !m.restartDetected {
		t.Fatal("restartDetected should be true after bound transition with zeroed counters")
	}
}

func TestNwgDelta_FreshStart_NoRestartDetected(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Fresh start: first poll with fail/0/0 — should NOT be detected as restart.
	m.processDelta(0, 0, "fail", true)
	if m.restartDetected {
		t.Fatal("restartDetected should be false on fresh start (no previous state)")
	}
}

func TestNwgDelta_StartupMixedDelta_SuppressesTransientFail(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// First poll emits INIT and enables startup phase.
	m.processDelta(0, 0, "pass", true)
	buf.Clear()

	// Transitional NDMS window: both counters incremented in one poll,
	// but current state is already healthy (status=pass).
	m.processDelta(1, 1, "pass", true)
	if buf.Len() != 1 {
		t.Fatalf("got %d entries, want 1 (success only)", buf.Len())
	}
	entries := buf.GetAll()
	if !entries[0].Success {
		t.Fatalf("got transient fail, want success-only entry")
	}
}

// TestNwgDelta_WarmupFirstPoll_NoFailEntry verifies that the very first poll of
// a freshly started tunnel — NDMS reports the provisional fail/0/0 before the
// interval has ticked — does NOT emit a fail log entry. A bogus "✗" entry would
// otherwise drive the UI to 100% loss and a red history bar on a healthy tunnel.
func TestNwgDelta_WarmupFirstPoll_NoFailEntry(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	m.processDelta(0, 0, "fail", true) // fresh-start warmup

	if buf.Len() != 0 {
		t.Fatalf("warmup first poll must emit no log entry, got %d: %+v", buf.Len(), buf.GetAll())
	}
	if !m.initialized {
		t.Error("baseline must still be initialized after warmup poll")
	}
}

// TestNwgDelta_NonWarmupFirstPoll_EmitsInitial confirms a first poll that is
// already healthy still emits its initial entry (unchanged behavior).
func TestNwgDelta_NonWarmupFirstPoll_EmitsInitial(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	m.processDelta(0, 1, "pass", true)

	if buf.Len() != 1 {
		t.Fatalf("healthy first poll must emit 1 initial entry, got %d", buf.Len())
	}
}

// countRestarts подставляет стаб вместо фасадного счётчика: считать рестарты
// монитору не положено, он только сообщает о них.
func countRestarts(m *nwgMonitor) *int {
	seen := new(int)
	m.onFruitlessRestart = func(string) int { *seen++; return *seen }
	return seen
}

// Рестарты интерфейса силами NDMS должны считаться: до #702 наружу
// отдавалась константа, и пользователь видел «рестартов ноль».
func TestNwgDelta_RestartCountGrows(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)
	seen := countRestarts(m)

	m.processDelta(0, 5, "pass", true) // базовая линия: туннель живой
	m.processDelta(3, 5, "fail", true) // копятся отказы
	m.processDelta(0, 0, "fail", true) // NDMS перезапустил: счётчики обнулены
	if *seen != 1 {
		t.Fatalf("после первого рестарта зафиксировано %d, want 1", *seen)
	}

	m.processDelta(3, 0, "fail", true) // снова отказы
	m.processDelta(0, 0, "fail", true) // снова рестарт
	if *seen != 2 {
		t.Fatalf("после второго рестарта зафиксировано %d, want 2", *seen)
	}
}

// Успешные проверки счётчик не трогают.
func TestNwgDelta_RestartCountStableWhileHealthy(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)
	seen := countRestarts(m)

	m.processDelta(0, 5, "pass", true)
	m.processDelta(0, 9, "pass", true)
	if *seen != 0 {
		t.Fatalf("на живом туннеле зафиксировано %d рестартов, want 0", *seen)
	}
}

// Серия живёт в фасаде и переживает пересоздание монитора, а restartDetected —
// нет. Успешная проверка обязана обнулять серию и у нового монитора, который
// про прошлый рестарт ничего не знает (#702).
func TestNwgDelta_SuccessResetsSeriesAfterMonitorRecreate(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	series := new(int)
	wire := func(m *nwgMonitor) {
		m.onFruitlessRestart = func(string) int { *series++; return *series }
		m.onCheckSuccess = func(string) { *series = 0 }
	}

	m := newTestNwgMonitor(buf)
	wire(m)
	m.processDelta(0, 5, "pass", true)
	m.processDelta(3, 5, "fail", true)
	m.processDelta(0, 0, "fail", true) // рестарт NDMS: серия = 1
	if *series != 1 {
		t.Fatalf("серия после рестарта = %d, want 1", *series)
	}

	// Монитор пересоздан не эскалацией, а сохранением настроек мониторинга:
	// restartDetected потерян, серия в фасаде — нет.
	m2 := newTestNwgMonitor(buf)
	wire(m2)
	m2.processDelta(0, 0, "fail", true) // базовая линия нового монитора
	m2.processDelta(0, 2, "pass", true) // туннель ожил
	if *series != 0 {
		t.Fatalf("после успешной проверки серия = %d, want 0", *series)
	}
}

// Хелпер: серия считается в замыкании, backoff разрешает первую эскалацию
// и запрещает последующие — ровно то, что делает фасад.
func wireEscalationStubs(m *nwgMonitor) *int {
	series := new(int)
	escalated := false
	m.onFruitlessRestart = func(string) int { *series++; return *series }
	m.onCheckSuccess = func(string) { *series = 0 }
	m.canEscalate = func(string) bool {
		if escalated {
			return false
		}
		escalated = true
		*series = 0
		return true
	}
	return series
}

// Три бесплодных рестарта NDMS подряд → один полный перезапуск (#702).
func TestNwgDelta_EscalatesAfterFruitlessRestarts(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)
	m.allowRestart = true
	wireEscalationStubs(m)
	restarted := make(chan string, 4)
	m.restarter = func(_ context.Context, id string) error {
		restarted <- id
		return nil
	}

	m.processDelta(0, 5, "pass", true) // базовая линия
	for i := 0; i < 3; i++ {
		m.processDelta(3, 0, "fail", true) // отказы
		m.processDelta(0, 0, "fail", true) // рестарт NDMS, успеха не было
	}

	select {
	case id := <-restarted:
		if id != m.tunnelID {
			t.Fatalf("перезапущен не тот туннель: %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("эскалации не произошло")
	}
	select {
	case id := <-restarted:
		t.Fatalf("эскалация должна быть одна, пришла вторая: %s", id)
	case <-time.After(200 * time.Millisecond):
	}
}

// Между рестартами проверки проходят — туннель живой, лечить нечего.
func TestNwgDelta_NoEscalationWhenChecksRecover(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)
	m.allowRestart = true
	wireEscalationStubs(m)
	restarted := make(chan string, 4)
	m.restarter = func(_ context.Context, id string) error {
		restarted <- id
		return nil
	}

	m.processDelta(0, 5, "pass", true)
	for i := 0; i < 3; i++ {
		m.processDelta(3, 5, "fail", true)
		m.processDelta(0, 0, "fail", true)
		m.processDelta(0, 1, "pass", true) // успех сбрасывает серию
	}

	select {
	case id := <-restarted:
		t.Fatalf("эскалации быть не должно, а туннель перезапустили: %s", id)
	case <-time.After(300 * time.Millisecond):
	}
}

// Пользователь запретил лечить — не лечим ничем.
func TestNwgDelta_NoEscalationWhenRestartDisabled(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)
	m.allowRestart = false
	wireEscalationStubs(m)
	restarted := make(chan string, 4)
	m.restarter = func(_ context.Context, id string) error {
		restarted <- id
		return nil
	}

	m.processDelta(0, 5, "pass", true)
	for i := 0; i < 3; i++ {
		m.processDelta(3, 0, "fail", true)
		m.processDelta(0, 0, "fail", true)
	}

	select {
	case id := <-restarted:
		t.Fatalf("настройка выключена, а туннель перезапустили: %s", id)
	case <-time.After(300 * time.Millisecond):
	}
}
