package singbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// seedStartSeam wires the startFn test seam and returns counters for the
// two start flavours (waitClash=true — легаси-путь Reconcile, false —
// router-путь AutoRestartIfCrashed).
func seedStartSeam(op *Operator) (waitStarts, plainStarts *int) {
	w, p := 0, 0
	op.startFn = func(_ context.Context, waitClash bool) (bool, error) {
		if waitClash {
			w++
		} else {
			p++
		}
		return true, nil // spawned
	}
	return &w, &p
}

// THE bug (#456): мёртвый процесс + ПУСТОЙ 10-tunnels.json + активная
// работа оркестратора (router-режим) → Reconcile обязан перезапустить.
func TestOperator_Reconcile_RestartsOnActiveWorkWithoutTunnels(t *testing.T) {
	op := newTestOperator(t, nil)
	op.activeWorkFn = func() bool { return true }
	waitStarts, _ := seedStartSeam(op)

	// 10-tunnels.json отсутствует вовсе — самый жёсткий вариант бага
	// (loadConfig → os.IsNotExist), раньше Reconcile выходил ещё раньше.
	if err := op.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if *waitStarts != 1 {
		t.Fatalf("startAndWait calls = %d, want 1 (dead process + active work must restart)", *waitStarts)
	}
}

// Пустой tunnels-файл (существует, но без туннелей) + активная работа →
// тоже перезапуск.
func TestOperator_Reconcile_RestartsOnActiveWorkWithEmptyTunnelsFile(t *testing.T) {
	op := newTestOperator(t, nil)
	op.activeWorkFn = func() bool { return true }
	waitStarts, _ := seedStartSeam(op)
	empty := `{"inbounds":[],"outbounds":[],"route":{"rules":[]}}`
	if err := os.WriteFile(filepath.Join(op.configPath, "10-tunnels.json"), []byte(empty), 0o644); err != nil {
		t.Fatalf("write tunnels: %v", err)
	}

	if err := op.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if *waitStarts != 1 {
		t.Fatalf("startAndWait calls = %d, want 1", *waitStarts)
	}
}

// Старое поведение: нет туннелей и нет активной работы (или предикат не
// подключён — nil) → никакого перезапуска.
func TestOperator_Reconcile_NoRestartWithoutTunnelsAndWork(t *testing.T) {
	for name, fn := range map[string]func() bool{
		"activeWorkFalse": func() bool { return false },
		"nilPredicate":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			op := newTestOperator(t, nil)
			op.activeWorkFn = fn
			waitStarts, plainStarts := seedStartSeam(op)

			if err := op.Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if *waitStarts+*plainStarts != 0 {
				t.Fatalf("start calls = %d, want 0 (no tunnels, no active work)", *waitStarts+*plainStarts)
			}
		})
	}
}

// Ручной Stop свят: даже при активной работе Reconcile не воскрешает.
func TestOperator_Reconcile_ManualStopBeatsActiveWork(t *testing.T) {
	op := newTestOperator(t, nil)
	op.activeWorkFn = func() bool { return true }
	op.manuallyStopped.Store(true)
	waitStarts, plainStarts := seedStartSeam(op)

	if err := op.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if *waitStarts+*plainStarts != 0 {
		t.Fatalf("start calls = %d, want 0 (manual stop must win)", *waitStarts+*plainStarts)
	}
}

// Легаси-путь: туннели есть, процесс мёртв → перезапуск через
// startAndWait (waitClash=true), ошибка старта прокидывается как раньше.
func TestOperator_Reconcile_LegacyTunnelsRestartAndErrorShape(t *testing.T) {
	// Outbound типа "socks" — userOutbounds исключает системные direct/block/
	// dns/selector, поэтому «туннелем» считается только прокси-outbound.
	tunnels := `{
		"inbounds":  [{"type":"socks","tag":"t-in","listen":"127.0.0.1","listen_port":1080}],
		"outbounds": [{"type":"socks","tag":"t","server":"1.2.3.4","server_port":1080}],
		"route":     {"rules":[{"inbound":"t-in","outbound":"t"}]}
	}`

	t.Run("restarts", func(t *testing.T) {
		op := newTestOperator(t, nil)
		waitStarts, _ := seedStartSeam(op)
		if err := os.WriteFile(filepath.Join(op.configPath, "10-tunnels.json"), []byte(tunnels), 0o644); err != nil {
			t.Fatalf("write tunnels: %v", err)
		}
		// isNDMSProxyEnabled → ndmsProxyEnabledFn nil → enabled → SyncProxies
		// пойдёт в NDMS; отключаем, чтобы остаться в юните.
		op.ndmsProxyEnabledFn = func() bool { return false }

		if err := op.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if *waitStarts != 1 {
			t.Fatalf("startAndWait calls = %d, want 1", *waitStarts)
		}
	})

	t.Run("startErrorPropagates", func(t *testing.T) {
		op := newTestOperator(t, nil)
		op.startFn = func(context.Context, bool) (bool, error) { return false, errors.New("boom") }
		if err := os.WriteFile(filepath.Join(op.configPath, "10-tunnels.json"), []byte(tunnels), 0o644); err != nil {
			t.Fatalf("write tunnels: %v", err)
		}
		err := op.Reconcile(context.Background())
		if err == nil || err.Error() != "start: boom" {
			t.Fatalf("Reconcile err = %v, want start: boom (legacy error shape)", err)
		}
	})
}

// AutoRestartIfCrashed: бюджет → подавление; ручной Reset снимает паузу.
func TestOperator_AutoRestartIfCrashed_BackoffSuppression(t *testing.T) {
	op := newTestOperator(t, nil)
	_, plainStarts := seedStartSeam(op)

	ctx := context.Background()
	for i := 0; i < restartFreeBudget; i++ {
		restarted, suppressed, err := op.autoRestartIfCrashed(ctx, false)
		if err != nil || !restarted || suppressed {
			t.Fatalf("attempt %d: restarted=%v suppressed=%v err=%v, want restart", i+1, restarted, suppressed, err)
		}
	}
	restarted, suppressed, err := op.autoRestartIfCrashed(ctx, false)
	if err != nil || restarted || !suppressed {
		t.Fatalf("over-budget: restarted=%v suppressed=%v err=%v, want suppressed", restarted, suppressed, err)
	}
	if *plainStarts != restartFreeBudget {
		t.Fatalf("start calls = %d, want %d", *plainStarts, restartFreeBudget)
	}
	// Статус видит паузу.
	if _, _, until := op.CrashStats(); until.IsZero() {
		t.Fatalf("CrashStats: want non-zero restartSuppressedUntil while suppressed")
	}
	// Ручной сброс (как Control("restart")) → снова можно.
	op.restartBackoff.Reset()
	restarted, suppressed, err = op.autoRestartIfCrashed(ctx, false)
	if err != nil || !restarted || suppressed {
		t.Fatalf("after reset: restarted=%v suppressed=%v err=%v, want restart", restarted, suppressed, err)
	}
}

// AutoRestartIfCrashed: ручной Stop → no-op (защита, которой не было в
// fakeip drift-heal до #456).
func TestOperator_AutoRestartIfCrashed_ManualStopNoop(t *testing.T) {
	op := newTestOperator(t, nil)
	_, plainStarts := seedStartSeam(op)
	op.manuallyStopped.Store(true)

	restarted, suppressed, err := op.autoRestartIfCrashed(context.Background(), false)
	if err != nil || restarted || suppressed {
		t.Fatalf("manual stop: restarted=%v suppressed=%v err=%v, want all-false", restarted, suppressed, err)
	}
	if *plainStarts != 0 {
		t.Fatalf("start calls = %d, want 0", *plainStarts)
	}
}

// handleExit: SIGKILL без stderr + СВЕЖИЙ OOM-след в dmesg (метка внутри
// текущего запуска) → человекочитаемая причина OOM в LastError и в
// истории падений, без пометки о неподтверждённом времени.
func TestOperator_HandleExit_ClassifiesOOMKill(t *testing.T) {
	op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Процесс стартовал минуту назад; аптайм 12400с → строки свежее
	// ~12310с принадлежат текущему запуску.
	op.restartBackoff.NoteProcessStart(time.Now().Add(-time.Minute))
	op.uptimeFn = func() (float64, error) { return 12400, nil }
	op.dmesgFn = func(context.Context) (string, error) {
		return "[12395.6] Out of memory: Killed process 4242 (sing-box) total-vm:190000kB\n", nil
	}
	op.handleExit(errors.New("signal: killed"), "", false)

	if got := op.LastError(); got != oomKillLastError {
		t.Fatalf("LastError = %q, want OOM explanation", got)
	}
	n, reason, _ := op.CrashStats()
	if n != 1 || reason != oomKillLastError {
		t.Fatalf("CrashStats = (%d, %q), want (1, oom reason)", n, reason)
	}
}

// FIX-G: устаревший OOM-след (метка СТАРШЕ текущего запуска) больше не
// «отравляет» причину — сообщение остаётся generic; свежая строка среди
// старых по-прежнему распознаётся.
func TestOperator_HandleExit_StaleOOMTraceIgnored(t *testing.T) {
	newOp := func(dmesg string) *Operator {
		op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		op.restartBackoff.NoteProcessStart(time.Now().Add(-time.Minute))
		op.uptimeFn = func() (float64, error) { return 12400, nil } // старт ≈ 12340с
		op.dmesgFn = func(context.Context) (string, error) { return dmesg, nil }
		return op
	}

	t.Run("staleOnly", func(t *testing.T) {
		// OOM трёхчасовой давности (1234.5с << 12310с floor).
		op := newOp("[ 1234.567890] Out of memory: Killed process 111 (sing-box)\n")
		op.handleExit(errors.New("signal: killed"), "", false)
		if got := op.LastError(); got != "signal: killed" {
			t.Fatalf("LastError = %q, want generic (stale OOM must be ignored)", got)
		}
	})

	t.Run("staleThenFresh", func(t *testing.T) {
		op := newOp("[ 1234.567890] Out of memory: Killed process 111 (sing-box)\n" +
			"[12395.100000] oom_reaper: reaped process 4242 (sing-box)\n")
		op.handleExit(errors.New("signal: killed"), "", false)
		if got := op.LastError(); got != oomKillLastError {
			t.Fatalf("LastError = %q, want confirmed OOM reason", got)
		}
	})
}

// FIX-G: когда сверить метки нельзя (нет времени старта / нет /proc/uptime /
// строка без метки) — best-effort совпадение остаётся, но причина честно
// помечается «метка времени не подтверждена».
func TestOperator_HandleExit_OOMUnconfirmedTimestamp(t *testing.T) {
	wantUnconfirmed := oomKillLastError + " (метка времени не подтверждена)"

	t.Run("noProcessStartKnown", func(t *testing.T) {
		op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		op.dmesgFn = func(context.Context) (string, error) {
			return "[12345.6] Out of memory: Killed process 4242 (sing-box)\n", nil
		}
		op.handleExit(errors.New("signal: killed"), "", false)
		if got := op.LastError(); got != wantUnconfirmed {
			t.Fatalf("LastError = %q, want unconfirmed OOM reason", got)
		}
	})

	t.Run("lineWithoutTimestamp", func(t *testing.T) {
		op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		op.restartBackoff.NoteProcessStart(time.Now().Add(-time.Minute))
		op.uptimeFn = func() (float64, error) { return 12400, nil }
		op.dmesgFn = func(context.Context) (string, error) {
			return "Out of memory: Killed process 4242 (sing-box)\n", nil
		}
		op.handleExit(errors.New("signal: killed"), "", false)
		if got := op.LastError(); got != wantUnconfirmed {
			t.Fatalf("LastError = %q, want unconfirmed OOM reason", got)
		}
	})

	t.Run("uptimeUnavailable", func(t *testing.T) {
		op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		op.restartBackoff.NoteProcessStart(time.Now().Add(-time.Minute))
		op.uptimeFn = func() (float64, error) { return 0, errors.New("no procfs") }
		op.dmesgFn = func(context.Context) (string, error) {
			return "[12345.6] Out of memory: Killed process 4242 (sing-box)\n", nil
		}
		op.handleExit(errors.New("signal: killed"), "", false)
		if got := op.LastError(); got != wantUnconfirmed {
			t.Fatalf("LastError = %q, want unconfirmed OOM reason", got)
		}
	})
}

// handleExit: dmesg без OOM-следа (или недоступен) → прежнее generic-сообщение.
func TestOperator_HandleExit_KeepsGenericWithoutOOMTrace(t *testing.T) {
	t.Run("noTrace", func(t *testing.T) {
		op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		op.dmesgFn = func(context.Context) (string, error) {
			return "[1.0] usb 1-1: new high-speed USB device\n", nil
		}
		op.handleExit(errors.New("signal: killed"), "", false)
		if got := op.LastError(); got != "signal: killed" {
			t.Fatalf("LastError = %q, want raw wait error", got)
		}
	})
	t.Run("dmesgUnavailable", func(t *testing.T) {
		op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		op.dmesgFn = func(context.Context) (string, error) {
			return "", errors.New("dmesg: not found")
		}
		op.handleExit(errors.New("signal: killed"), "", false)
		if got := op.LastError(); got != "signal: killed" {
			t.Fatalf("LastError = %q, want raw wait error (best-effort fallback)", got)
		}
	})
}

// handleExit: преднамеренный Stop/Reload не считается падением — ни в
// истории, ни в backoff'е, и не запускает dmesg-классификацию.
func TestOperator_HandleExit_DeliberateStopNotCounted(t *testing.T) {
	op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	dmesgCalls := 0
	op.dmesgFn = func(context.Context) (string, error) {
		dmesgCalls++
		return "Out of memory: Killed process 1 (sing-box)", nil
	}
	op.handleExit(errors.New("signal: killed"), "", true)

	if n, _, _ := op.CrashStats(); n != 0 {
		t.Fatalf("CrashStats count = %d, want 0 for deliberate stop", n)
	}
	if dmesgCalls != 0 {
		t.Fatalf("dmesg probed %d times, want 0 for deliberate stop", dmesgCalls)
	}
}

// Ring истории падений ограничен crashHistoryCap; счётчик окна и причина
// берутся из свежих записей.
func TestOperator_CrashStats_RingAndWindow(t *testing.T) {
	op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for i := 0; i < crashHistoryCap+3; i++ {
		op.recordCrash(time.Now(), "reason")
	}
	op.crashMu.Lock()
	ringLen := len(op.crashes)
	op.crashMu.Unlock()
	if ringLen != crashHistoryCap {
		t.Fatalf("ring len = %d, want %d", ringLen, crashHistoryCap)
	}
	// Старые записи выпадают из окна подсчёта.
	op.crashMu.Lock()
	op.crashes = []crashRecord{{at: time.Now().Add(-restartCrashWindow - time.Minute), reason: "old"}}
	op.crashMu.Unlock()
	n, reason, _ := op.CrashStats()
	if n != 0 || reason != "" {
		t.Fatalf("CrashStats = (%d, %q), want (0, empty) for out-of-window crash", n, reason)
	}
}

// FIX-A: Control("stop") во время in-flight авто-старта. startFn имитирует
// гонку — выставляет ручную остановку, пока «процесс запускается»; после
// успешного старта autoRestartIfCrashed обязан перепроверить намерение,
// погасить только что поднятый процесс и вернуть suppressed=true.
func TestOperator_AutoRestart_ManualStopDuringStartWins(t *testing.T) {
	op := newTestOperator(t, nil)
	stopCalls := 0
	op.stopProcFn = func() error { stopCalls++; return nil }
	op.startFn = func(context.Context, bool) (bool, error) {
		// Между Allow и завершением Start пользователь нажал «Остановить».
		op.manuallyStopped.Store(true)
		return true, nil
	}

	restarted, suppressed, err := op.autoRestartIfCrashed(context.Background(), false)
	if err != nil || restarted || !suppressed {
		t.Fatalf("restarted=%v suppressed=%v err=%v, want suppressed (manual stop during start)", restarted, suppressed, err)
	}
	if stopCalls != 1 {
		t.Fatalf("proc.Stop calls = %d, want 1 (freshly started process must be stopped)", stopCalls)
	}
}

// FIX-A: Control("stop") делает СВЕЖУЮ проверку IsRunning после выставления
// намерения — устаревший снапшот сверху (сделанный, пока авто-старт ещё не
// записал pid) не должен приводить к «stop skipped» при уже работающем
// процессе. Моделируем гонку тумблером matchBinaryFn: первый пробинг
// (снапшот) — «не работает», второй (свежий) — «работает».
func TestOperator_Control_Stop_ReprobesAfterSettingIntent(t *testing.T) {
	op := newTestOperator(t, nil)

	// Живой дочерний процесс, которым «владеет» pid-файл. Фоновый Wait
	// сразу пожинает зомби после SIGTERM'а из stopLocked — иначе isAlive
	// видит зомби «живым» и Stop крутит все 3с SIGTERM-поллинга.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleep: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if err := op.proc.writePID(cmd.Process.Pid); err != nil {
		t.Fatalf("writePID: %v", err)
	}
	probe := 0
	op.proc.matchBinaryFn = func(int) bool {
		probe++
		// Первый вызов — верхний снапшот Control (гонка: процесс «ещё не
		// виден»); все последующие — честное «работает».
		return probe > 1
	}

	if err := op.Control(context.Background(), "stop"); err != nil {
		t.Fatalf("Control stop: %v", err)
	}
	if probe < 2 {
		t.Fatalf("IsRunning probes = %d, want ≥2 (fresh re-probe after setting intent)", probe)
	}
	// Свежий пробинг увидел процесс → proc.Stop() должен был его погасить.
	if running, _ := op.proc.IsRunning(); running {
		t.Fatalf("process still running after Control(stop) — stale snapshot won the race")
	}
}

// FIX-D: старт, упавший ДО startupGracePeriod (Start возвращает ошибку,
// OnExit не срабатывает), обязан записать падение в ring — иначе UI
// показывает подавление с «Падений: 0» и без причины.
func TestOperator_AutoRestart_StartFailureRecordsCrash(t *testing.T) {
	op := newTestOperator(t, nil)
	op.startFn = func(context.Context, bool) (bool, error) {
		return false, errors.New("sing-box exited during startup: FATAL boom")
	}

	_, _, err := op.autoRestartIfCrashed(context.Background(), false)
	if err == nil {
		t.Fatalf("want start error")
	}
	n, reason, _ := op.CrashStats()
	if n != 1 {
		t.Fatalf("CrashStats count = %d, want 1 (pre-grace failure must be visible)", n)
	}
	if !strings.Contains(reason, "FATAL boom") {
		t.Fatalf("reason = %q, want the start error text", reason)
	}
}

// FIX-D guard: если падение уже записано (пост-grace смерть во время
// ожидания Clash прошла через handleExit), ошибка старта не дублирует запись.
func TestOperator_AutoRestart_StartFailureNoDoubleCount(t *testing.T) {
	op := newTestOperator(t, nil)
	op.startFn = func(context.Context, bool) (bool, error) {
		// handleExit успел записать падение до возврата ошибки.
		op.recordCrash(time.Now(), "died while waiting for clash")
		return true, errors.New("clash API not ready after 60s")
	}

	_, _, err := op.autoRestartIfCrashed(context.Background(), false)
	if err == nil {
		t.Fatalf("want start error")
	}
	n, reason, _ := op.CrashStats()
	if n != 1 {
		t.Fatalf("CrashStats count = %d, want 1 (no double count)", n)
	}
	if reason != "died while waiting for clash" {
		t.Fatalf("reason = %q, want the handleExit record kept", reason)
	}
}

// FIX-E: no-op старт (процесс уже поднял конкурент по гонке watchdog/router)
// возвращает попытку в бюджет — бюджет жжётся только реальными спавнами.
func TestOperator_AutoRestart_NoSpawnRefundsBudget(t *testing.T) {
	op := newTestOperator(t, nil)
	spawned := false // все старты no-op'ятся
	starts := 0
	op.startFn = func(context.Context, bool) (bool, error) { starts++; return spawned, nil }

	ctx := context.Background()
	// Вдвое больше бюджета «проигранных гонок» — ни одна не должна жечь Allow.
	for i := 0; i < restartFreeBudget*2; i++ {
		restarted, suppressed, err := op.autoRestartIfCrashed(ctx, false)
		if err != nil || restarted || suppressed {
			t.Fatalf("no-op attempt %d: restarted=%v suppressed=%v err=%v, want all-false", i+1, restarted, suppressed, err)
		}
	}
	// Бюджет цел: настоящие спавны всё ещё разрешены в полном объёме.
	spawned = true
	for i := 0; i < restartFreeBudget; i++ {
		restarted, suppressed, err := op.autoRestartIfCrashed(ctx, false)
		if err != nil || !restarted || suppressed {
			t.Fatalf("real attempt %d: restarted=%v suppressed=%v err=%v, want restart (budget must be intact)", i+1, restarted, suppressed, err)
		}
	}
	if _, suppressed, _ := op.autoRestartIfCrashed(ctx, false); !suppressed {
		t.Fatalf("over-budget: want suppressed after %d real spawns", restartFreeBudget)
	}
	if starts != restartFreeBudget*3 {
		t.Fatalf("start calls = %d, want %d", starts, restartFreeBudget*3)
	}
}

// FIX-H: configDeclaresClashAPI — clash-гейт startAndWait включается только
// когда merged-конфиг реально объявляет experimental.clash_api (пользователь
// мог удалить блок намеренно; patchBaseClashPort это уважает).
func TestOperator_ConfigDeclaresClashAPI(t *testing.T) {
	write := func(t *testing.T, op *Operator, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(op.configPath, "00-base.json"), []byte(body), 0o644); err != nil {
			t.Fatalf("write base: %v", err)
		}
	}

	t.Run("declared", func(t *testing.T) {
		op := newTestOperator(t, nil)
		write(t, op, `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9099"}}}`)
		if !op.configDeclaresClashAPI() {
			t.Fatalf("want true when clash_api present")
		}
	})

	t.Run("absent", func(t *testing.T) {
		op := newTestOperator(t, nil)
		write(t, op, `{"log":{"level":"info"}}`)
		if op.configDeclaresClashAPI() {
			t.Fatalf("want false when user removed clash_api")
		}
	})

	t.Run("emptyExperimental", func(t *testing.T) {
		op := newTestOperator(t, nil)
		write(t, op, `{"experimental":{}}`)
		if op.configDeclaresClashAPI() {
			t.Fatalf("want false when experimental has no clash_api")
		}
	})

	t.Run("unreadableConservative", func(t *testing.T) {
		op := newTestOperator(t, nil)
		write(t, op, `{broken json`)
		if !op.configDeclaresClashAPI() {
			t.Fatalf("want true (conservative) on merge/parse error")
		}
	})
}

// Ручной Stop закрывает эпизод падений. Раньше тест звал restartBackoff.Reset()
// руками «как Control» — симуляция; Reset внутри Control не пиновался.
func TestOperator_Control_ResetsRestartBackoff(t *testing.T) {
	op := newTestOperator(t, func(bool) error { return nil })
	seedStartSeam(op)
	ctx := context.Background()
	for i := 0; i < restartFreeBudget+1; i++ {
		op.autoRestartIfCrashed(ctx, false)
	}
	if op.restartBackoff.SuppressedUntil(time.Now()).IsZero() {
		t.Fatal("фикстура: бюджет исчерпан, пауза обязана быть активна")
	}

	if err := op.Control(ctx, "stop"); err != nil {
		t.Fatalf("Control stop: %v", err)
	}
	if until := op.restartBackoff.SuppressedUntil(time.Now()); !until.IsZero() {
		t.Fatalf("после ручного Stop пауза авто-рестарта обязана сняться, until=%v", until)
	}

	// Control("start") при уже работающем процессе: Reset идёт ДО раннего выхода
	// «already running» — без fork/exec sing-box (стартовый путь не трогается;
	// install-гейт форкает только fakeBinary из TempDir). Работающий процесс
	// изображаем своим pid; matchBinaryFn не выставляется — в newTestOperator
	// Process.binary пуст, и matchesBinary принимает любой pid.
	//
	// Control("stop") взвёл manuallyStopped, а autoRestartIfCrashed при нём
	// выходит первой же строкой и бюджет не жжёт — снимаем флаг, иначе вторая
	// половина теста проверяла бы уже снятую паузу.
	if err := op.setManualStop(false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < restartFreeBudget+1; i++ {
		op.autoRestartIfCrashed(ctx, false)
	}
	if op.restartBackoff.SuppressedUntil(time.Now()).IsZero() {
		t.Fatal("фикстура: бюджет исчерпан повторно, пауза обязана быть активна")
	}
	if err := os.WriteFile(op.pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := op.Control(ctx, "start"); err != nil {
		t.Fatalf("Control start: %v", err)
	}
	if until := op.restartBackoff.SuppressedUntil(time.Now()); !until.IsZero() {
		t.Fatalf("после ручного Start пауза авто-рестарта обязана сняться, until=%v", until)
	}
}
