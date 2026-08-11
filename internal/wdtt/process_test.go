package wdtt

import (
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

// newTestProcess подменяет реальный бинарь shell-скриптом через seam startCmd;
// p.binary=/bin/sh проходит binaryPresent, реальная команда — из script.
func newTestProcess(t *testing.T, script string) *process {
	t.Helper()
	p := newProcess("client", "/bin/sh", t.TempDir())
	p.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}
	return p
}

// TestProcess_StartDoesNotLeakPipeFDs — регресс на утечку read-концов пайпов:
// cmd.Wait() (который раньше сам закрывал parent-концы) удалён вместе с
// гейтом на EOF; на штатном быстром пути (ребёнок умер, EOF раньше
// drainGrace — ветка <-done в жнеце) их теперь обязан закрывать сам жнец.
// GC отключён на время теста, чтобы финализатор *os.File не закрыл текущие
// fd за нас и не замаскировал баг.
func TestProcess_StartDoesNotLeakPipeFDs(t *testing.T) {
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)

	countFDs := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatalf("ReadDir /proc/self/fd: %v", err)
		}
		return len(entries)
	}

	p := newTestProcess(t, "exit 0")
	_ = p.Start(nil) // прогрев — первый Start заводит одноразовые ресурсы (буферы и т.п.)
	before := countFDs()

	const cycles = 20
	for i := 0; i < cycles; i++ {
		if err := p.Start(nil); err == nil {
			t.Fatalf("cycle %d: want startup error (скрипт выходит мгновенно)", i)
		}
	}

	after := countFDs()
	// Раньше каждый цикл тёк по 2 fd (read-концы stdout/stderr): было бы
	// +40 за 20 циклов. Небольшой допуск — под недетерминированные накладные
	// расходы рантайма, не открывающий дорогу настоящей утечке.
	if after-before > 4 {
		t.Fatalf("похоже на утечку fd: before=%d after=%d (+%d за %d циклов)", before, after, after-before, cycles)
	}
}

// TestProcess_StartupFailure_StderrCaptured_UnderDrainDelay — детерминированный
// регресс на гонку os/exec: Wait() не должен закрывать пайпы раньше drain.
func TestProcess_StartupFailure_StderrCaptured_UnderDrainDelay(t *testing.T) {
	p := newTestProcess(t, "echo boom >&2; exit 1")
	p.drainStartDelay = 50 * time.Millisecond
	err := p.Start(nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr должен быть в ошибке даже с задержкой drain, got: %v", err)
	}
}

// TestDrainCONFIGSurvivesEviction проверяет, что CONFIG-событие, пойманное в
// drain, остаётся доступным через Status() даже после того как исходная строка
// вытеснена из ring-буфера логов последующими STATS-строками.
func TestDrainCONFIGSurvivesEviction(t *testing.T) {
	p := newProcess("test", "", t.TempDir())

	var sb strings.Builder
	sb.WriteString(`__WDTT_EVENT__|CONFIG|{"config":"[Interface]\nPrivateKey = k\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = pk\nAllowedIPs = 0.0.0.0/0\n"}`)
	sb.WriteByte('\n')
	// Вытесняем CONFIG за пределы ring-буфера (processLogMaxLines=500).
	for i := 0; i < processLogMaxLines+100; i++ {
		sb.WriteString("__WDTT_EVENT__|STATS|{}\n")
	}

	p.drain(strings.NewReader(sb.String()))

	// CONFIG должен быть вытеснен из лога...
	if got := ExtractWGConfigFromLog(p.logTail.String()); got != "" {
		t.Fatalf("expected CONFIG evicted from log, got %q", got)
	}
	// ...но сохранён в поле процесса.
	if !strings.Contains(p.lastWgConfig, "PublicKey = pk") {
		t.Fatalf("lastWgConfig not retained: %q", p.lastWgConfig)
	}
	if got := p.Status().WgConfig; !strings.Contains(got, "PublicKey = pk") {
		t.Fatalf("Status().WgConfig=%q", got)
	}
}

// TestProcess_ReapsHelperOrphan_DoesNotGateOnPipeEOF — репро зомби-бага: ребёнок
// форкает фонового хелпера (sleep 60), унаследовавшего stderr, и сразу выходит.
// drainGrace (1с) < startupGrace (1.5с), поэтому errCh на исправленном коде
// успевает раньше внешнего грейса: Start() должен вернуть ошибку старта, а не
// ложный nil («успех» для уже мёртвого процесса). Дискриминатор бага — что
// происходит С ПРОЦЕССОМ:
//  1. сразу после возврата Start() прямой ребёнок не должен быть зомби —
//     старый код не реапает его, пока жив хелпер (реап гейтится на EOF пайпов
//     через drainWG внутри cmd.Wait());
//  2. IsRunning()/startedAt должны самоисправиться на «не работает» быстро
//     (в разумных секундах), а не только когда хелпер сам умрёт (60с) —
//     это и есть «усилитель» из root cause: супервизор считает мёртвый
//     процесс работающим.
//
// Таймаут-обёртки на select не дают прогону зависнуть на реальные 60с
// хелпера, если баг воспроизведётся.
func TestProcess_ReapsHelperOrphan_DoesNotGateOnPipeEOF(t *testing.T) {
	var capturedCmd *exec.Cmd
	p := newProcess("client", "/bin/sh", t.TempDir())
	p.startCmd = func(_ string, _ ...string) *exec.Cmd {
		capturedCmd = exec.Command("/bin/sh", "-c", "sleep 60 <&- >&2 2>&2 &\nexit 0")
		return capturedCmd
	}

	type result struct{ err error }
	done := make(chan result, 1)
	go func() { done <- result{p.Start(nil)} }()

	var r result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start() не вернулся за 5с — жнец гейтится на EOF пайпов орфан-хелпера (зомби-баг)")
	}

	if capturedCmd == nil || capturedCmd.Process == nil {
		t.Fatal("cmd.Process не установлен")
	}
	pid := capturedCmd.Process.Pid
	// Хелпер (sleep 60) — сирота в той же группе (Setsid лидер = наш прямой
	// ребёнок): -pid валит всю группу. Гигиена теста, не часть проверяемого
	// поведения (фикс уже должен был убить группу сам через drainGrace-фолбэк).
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	if r.err == nil || !strings.Contains(r.err.Error(), "exited during startup") {
		t.Fatalf("want ошибку «exited during startup» быстро (drainGrace < startupGrace), got %v", r.err)
	}

	if state, ok := procStatState(pid); ok && state == "Z" {
		t.Fatalf("pid %d остался зомби сразу после возврата Start() — реап гейтится на EOF пайпов", pid)
	}

	notRunning := make(chan struct{})
	go func() {
		for {
			if running, _ := p.IsRunning(); !running {
				close(notRunning)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	select {
	case <-notRunning:
	case <-time.After(5 * time.Second):
		t.Fatal("IsRunning() всё ещё true спустя 5с — не самоисправляется, пока жив орфан-хелпер (зомби-баг, супервизор не увидит смерть процесса)")
	}
}

// TestProcess_FallbackBranch_KillsSurvivingHelperGroup нацелен именно на
// drainGrace-фолбэк (сценарий выше уже его задевает, но не проверяет
// последствия для хелпера явно): ребёнок форкает хелпера, печатает его pid в
// stderr (чтобы тест мог его опознать) и сразу выходит. Хелпер держит stderr
// дольше drainGrace, поэтому фолбэк должен:
//   - убить всю группу (childproc.KillGroup) — хелпер не переживает Start();
//   - всё равно дать drain'у дочитать то, что успело прийти до форс-закрытия
//     (pid хелпера должен оказаться в логе).
func TestProcess_FallbackBranch_KillsSurvivingHelperGroup(t *testing.T) {
	var capturedCmd *exec.Cmd
	p := newProcess("client", "/bin/sh", t.TempDir())
	p.startCmd = func(_ string, _ ...string) *exec.Cmd {
		capturedCmd = exec.Command("/bin/sh", "-c",
			"sleep 60 <&- >&2 2>&2 &\necho \"HELPERPID=$!\" >&2\nexit 0")
		return capturedCmd
	}

	type result struct{ err error }
	done := make(chan result, 1)
	go func() { done <- result{p.Start(nil)} }()

	var r result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start() не вернулся за 5с")
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "exited during startup") {
		t.Fatalf("want ошибку «exited during startup», got %v", r.err)
	}

	if capturedCmd == nil || capturedCmd.Process == nil {
		t.Fatal("cmd.Process не установлен")
	}
	pid := capturedCmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }) // страховка

	if state, ok := procStatState(pid); ok && state == "Z" {
		t.Fatalf("pid %d остался зомби после возврата Start()", pid)
	}
	if running, _ := p.IsRunning(); running {
		t.Fatal("IsRunning() должен быть false после ошибки старта")
	}

	log := p.Status().Log
	const marker = "HELPERPID="
	i := strings.Index(log, marker)
	if i < 0 {
		t.Fatalf("хвост stderr хелпера не попал в лог — drain-горутина не успела/не вышла: %q", log)
	}
	helperPID, err := strconv.Atoi(strings.Fields(log[i+len(marker):])[0])
	if err != nil {
		t.Fatalf("не удалось распарсить HELPERPID из лога %q: %v", log, err)
	}

	// Не childproc.IsAlive: kill(pid,0) успешен и для зомби. И не голый
	// /proc/pid/stat: реап сироты — дело внешнего субридера (init), не
	// мгновенное, а pid к этому моменту мог быть переиспользован ДРУГИМ
	// процессом (гонка на нагруженной машине, не признак незаконченного
	// kill). MatchesBinary по cmdline: false и для зомби (cmdline пуст), и
	// для чужого процесса — true только если ЖИВОЙ pid всё ещё "sleep".
	//
	// SIGKILL уже отправлен (KillGroup внутри Start() синхронно завершился
	// до его возврата) — но доставка сигнала асинхронна: между kill() и
	// фактическим уходом жертвы из «живого» состояния есть окно в
	// миллисекунды (шире под нагрузкой). Поэтому — короткий bounded-poll, а
	// не одна синхронная проверка сразу после возврата Start().
	killed := make(chan struct{})
	go func() {
		for childproc.MatchesBinary(helperPID, "sleep") {
			time.Sleep(20 * time.Millisecond)
		}
		close(killed)
	}()
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatalf("хелпер (pid %d) всё ещё выполняется как sleep спустя 2с — group-kill не сработал", helperPID)
	}
}

// procStatState читает третье поле /proc/<pid>/stat (state). Возвращает
// ok=false, если процесс уже не существует (что тоже означает «не зомби»).
func procStatState(pid int) (string, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return "", false
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}
