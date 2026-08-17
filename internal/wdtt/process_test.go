package wdtt

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
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

// hupTrapScript — скрипт, дописывающий строку в маркер на каждый SIGHUP.
// Цикл с коротким sleep, потому что trap в sh срабатывает только между
// командами: с sleep 0.1 задержка доставки не превышает 100 мс.
func hupTrapScript(mark string) string {
	return "trap 'echo hup >> \"" + mark + "\"' HUP\nwhile :; do sleep 0.1; done"
}

// waitForHupCount ждёт, пока в маркере не окажется не меньше want строк.
// Ожидание ограничено сроком: тест, который вместо падения виснет, бесполезен.
func waitForHupCount(t *testing.T, mark string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		got := 0
		if b, err := os.ReadFile(mark); err == nil {
			got = len(strings.Fields(string(b)))
		}
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("маркер %s: получено SIGHUP %d, ожидалось не меньше %d", mark, got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// watchOwnSIGHUP перехватывает SIGHUP, адресованный САМОМУ тестовому бинарю.
// Нужен там, где мутант мог бы послать сигнал не туда: без перехвата дефолтная
// диспозиция SIGHUP убила бы прогон, и отказ теста выглядел бы аварией
// рантайма, а не провалом проверки.
func watchOwnSIGHUP(t *testing.T) chan os.Signal {
	t.Helper()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	t.Cleanup(func() { signal.Stop(ch) })
	return ch
}

// TestProcessReload_SignalsOwnChild — целевая семантика: живой процесс получает
// SIGHUP и остаётся жив (на железе это и есть «База паролей перезагружена»).
func TestProcessReload_SignalsOwnChild(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "hup.mark")
	p := newTestProcess(t, hupTrapScript(mark))
	if err := p.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	delivered, err := p.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// Признак доставки — то, из чего ручка абонентов делает «применено сейчас».
	if !delivered {
		t.Fatal("Reload живому процессу доложил, что сигнал не отправлен")
	}
	waitForHupCount(t, mark, 1, 3*time.Second)

	// Перезагрузка не должна подменять перезапуском: pid обязан остаться тем же.
	running, pid := p.IsRunning()
	if !running {
		t.Fatal("процесс не пережил SIGHUP")
	}
	if _, err := p.Reload(); err != nil {
		t.Fatalf("повторный Reload: %v", err)
	}
	waitForHupCount(t, mark, 2, 3*time.Second)
	if _, pid2 := p.IsRunning(); pid2 != pid {
		t.Fatalf("pid сменился после SIGHUP: было %d, стало %d", pid, pid2)
	}
}

// TestProcessReload_DoesNotSignalForeignPID — pid-файл пережил ребут, и номер
// достался постороннему процессу. Здесь «посторонний» — сам тестовый бинарь:
// p.binary=/bin/sh, а тест — не sh, поэтому pidIsOurs обязан ответить «нет».
func TestProcessReload_DoesNotSignalForeignPID(t *testing.T) {
	ch := watchOwnSIGHUP(t)

	p := newProcess("server", "/bin/sh", t.TempDir())
	if err := p.writePID(os.Getpid()); err != nil {
		t.Fatalf("writePID: %v", err)
	}
	if running, _ := p.IsRunning(); running {
		t.Fatal("предпосылка теста нарушена: pidIsOurs признал чужой pid своим")
	}

	delivered, err := p.Reload()
	if err != nil {
		t.Fatalf("Reload на чужом pid должен быть тихим no-op, got: %v", err)
	}
	if delivered {
		t.Fatal("Reload доложил о доставке сигнала по чужому pid")
	}
	select {
	case sig := <-ch:
		t.Fatalf("SIGHUP ушёл постороннему процессу по протухшему pid-файлу: %v", sig)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestProcessReload_StoppedServerIsNoop — pid-файла нет вовсе. Проверка «не
// работает» обязана стоять ДО сигнала: без неё pid равен нулю, а kill(0, sig)
// бьёт по всей группе процессов вызывающего.
func TestProcessReload_StoppedServerIsNoop(t *testing.T) {
	ch := watchOwnSIGHUP(t)

	p := newProcess("server", "/bin/sh", t.TempDir())
	delivered, err := p.Reload()
	if err != nil {
		t.Fatalf("Reload остановленного сервера должен быть тихим no-op, got: %v", err)
	}
	// Остановленный сервер — не ошибка и не доставка: ровно на этом различии
	// стоит «применится при следующем запуске».
	if delivered {
		t.Fatal("Reload остановленного сервера доложил о доставке сигнала")
	}
	select {
	case sig := <-ch:
		t.Fatalf("SIGHUP ушёл при отсутствии pid-файла (kill(0) бьёт по всей группе): %v", sig)
	case <-time.After(300 * time.Millisecond):
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

// procStatPgrp читает пятое поле /proc/<pid>/stat (pgrp — группа процессов).
// Поля после закрывающей скобки: state, ppid, pgrp — то есть индекс 2.
func procStatPgrp(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return 0, false
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 3 {
		return 0, false
	}
	pgrp, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, false
	}
	return pgrp, true
}

// TestProcessReload_DoesNotSignalProcessGroup — запрет «сигнал по группе (-pid)
// не годится: в группе живут помощники». Прямой ребёнок запускается с
// Setsid и потому САМ лидер своей группы: маркер подписанта пишется и при
// kill(-pid), поэтому один только он мутанта не ловит. Нужен второй свидетель —
// помощник в той же группе, у которого свой маркер и свой trap.
//
// Помощник обязан быть ЖИВЫМ и именно в группе подписанта к моменту сигнала:
// без этой предпосылки страж вырождается в вечно-зелёный. Поэтому pgrp
// помощника сверяется с pid подписанта по /proc до вызова Reload.
func TestProcessReload_DoesNotSignalProcessGroup(t *testing.T) {
	dir := t.TempDir()
	mainMark := filepath.Join(dir, "main.mark")
	helperMark := filepath.Join(dir, "helper.mark")
	helperPidFile := filepath.Join(dir, "helper.pid")
	helperScript := filepath.Join(dir, "helper.sh")

	if err := os.WriteFile(helperScript, []byte(
		"echo $$ > \""+helperPidFile+"\"\n"+hupTrapScript(helperMark)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p := newTestProcess(t, "sh \""+helperScript+"\" &\n"+hupTrapScript(mainMark)+"\n")
	if err := p.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	running, pid := p.IsRunning()
	if !running {
		t.Fatal("подписант не запущен")
	}
	helperPID := waitForGroupHelper(t, helperPidFile, pid, 3*time.Second)

	if _, err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	waitForHupCount(t, mainMark, 1, 3*time.Second)
	// Помощник получил бы сигнал ОДНОВРЕМЕННО с подписантом; пауза — запас на
	// планировщик, а не на доставку.
	time.Sleep(300 * time.Millisecond)
	if b, err := os.ReadFile(helperMark); err == nil && len(strings.Fields(string(b))) > 0 {
		t.Fatalf("SIGHUP ушёл по группе: помощник pid %d (группа %d) тоже получил сигнал: %q", helperPID, pid, b)
	}
	if _, ok := procStatPgrp(helperPID); !ok {
		t.Fatalf("помощник pid %d исчез до конца проверки — свидетель мёртв, страж вырожден", helperPID)
	}
}

// waitForGroupHelper дожидается помощника и доказывает, что он в группе
// подписанта. Возвращает его pid.
func waitForGroupHelper(t *testing.T, pidFile string, leaderPID int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if b, err := os.ReadFile(pidFile); err == nil {
			if hpid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && hpid > 0 {
				if pgrp, ok := procStatPgrp(hpid); ok && pgrp == leaderPID {
					return hpid
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("помощник не появился в группе %d за %s — предпосылка теста не выполнена", leaderPID, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLastRawConf_ReadsUnderLock — репро гонки: drain пишет lastRawConfPayload
// из своей горутины, а lastRawConf читает поле из чужой (waitForClientRawConf,
// пул статусов сервиса). Без замка -race ловит запись/чтение; тест нужен именно
// потому, что в проде конкурирующих вызывающих мало и гонка не воспроизводится
// сама. Мутация «снять p.mu.Lock() в lastRawConf» роняет тест под -race.
func TestLastRawConf_ReadsUnderLock(t *testing.T) {
	p := newProcess("test", "", t.TempDir())
	pr, pw := io.Pipe()

	done := make(chan struct{})
	go func() {
		p.drain(pr)
		close(done)
	}()

	stop := make(chan struct{})
	readers := sync.WaitGroup{}
	for i := 0; i < 2; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					p.lastRawConf()
				}
			}
		}()
	}

	for i := 0; i < 200; i++ {
		if _, err := fmt.Fprintf(pw, "RAWCONF|10.70.0.%d|1.1.1.1|1300\n", i%200+2); err != nil {
			t.Fatal(err)
		}
	}
	_ = pw.Close()
	<-done
	close(stop)
	readers.Wait()

	if conf, ok := p.lastRawConf(); !ok || conf.ClientIP == "" {
		t.Fatalf("конфиг не сохранён: %+v ok=%v", conf, ok)
	}
}

// TestProcessStatus_OrphanedPIDForInheritedPidFile — признак «унаследованный
// pid-файл» наружу: процесс НАШ и живой, но запускал его прошлый экземпляр
// демона, поэтому startedAt пуст, лога и телеметрии по нему нет. Ровно это
// условие поднимает супервизор на перезапуск; вычислять его на фронте как
// «running && !startedAt» — хрупко, поэтому оно уезжает отдельным полем.
//
// Бинарь = /bin/sleep, чтобы /proc cmdline унаследованного процесса совпал с
// нашим: иначе pidIsOurs признает pid чужим (TestProcess_StartKeepsForeignPID).
func TestProcessStatus_OrphanedPIDForInheritedPidFile(t *testing.T) {
	dir := t.TempDir()
	p1 := newProcess("server", "/bin/sleep", dir)
	p1.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sleep", "30")
	}
	if err := p1.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p1.Stop() })

	if st := p1.Status(); !st.Running || st.OrphanedPID {
		t.Fatalf("свой ребёнок помечен унаследованным: %+v", st)
	}

	// Новый экземпляр демона: тот же pid-файл, startedAt пуст.
	p2 := newProcess("server", "/bin/sleep", dir)
	st := p2.Status()
	if !st.Running {
		t.Fatalf("предпосылка теста нарушена: унаследованный процесс не признан живым: %+v", st)
	}
	if st.StartedAt != nil {
		t.Fatalf("предпосылка теста нарушена: у унаследованного процесса есть startedAt: %+v", st)
	}
	if !st.OrphanedPID {
		t.Fatalf("унаследованный pid-файл не помечен: %+v", st)
	}

	// Остановленный процесс унаследованным не считается: признак существует
	// только вместе с running, иначе он значил бы «pid-файла нет».
	if err := p1.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if st := p2.Status(); st.Running || st.OrphanedPID {
		t.Fatalf("остановленный процесс помечен унаследованным: %+v", st)
	}
}

// TestProcessStatus_NoOrphanedPIDDuringStart — вторая граница того же признака:
// между записью pid-файла и концом startupGrace процесс уже НАШ и живой, а
// startedAt ещё нет. Считать это унаследованным pid-файлом нельзя: бейдж
// «устаревший процесс» загорался бы на 1.5 с при КАЖДОМ штатном старте (фронт
// опрашивает статус чаще, чем длится окно).
func TestProcessStatus_NoOrphanedPIDDuringStart(t *testing.T) {
	dir := t.TempDir()
	p := newProcess("server", "/bin/sleep", dir)
	p.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sleep", "30")
	}
	done := make(chan error, 1)
	go func() { done <- p.Start(nil) }()
	t.Cleanup(func() { _ = p.Stop() })

	// Окно ловим по первому же «живой»: раньше pid-файла процесса наружу нет,
	// позже startupGrace — startedAt уже стоит и проверять нечего.
	deadline := time.Now().Add(startupGrace / 2)
	var st ProcessStatus
	for {
		st = p.Status()
		if st.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("предпосылка теста нарушена: процесс не стал живым за половину startupGrace")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st.StartedAt != nil {
		t.Fatalf("предпосылка теста нарушена: startedAt выставлен до конца startupGrace: %+v", st)
	}
	if st.OrphanedPID {
		t.Fatalf("свой процесс в окне старта помечен унаследованным: %+v", st)
	}

	if err := <-done; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st := p.Status(); !st.Running || st.StartedAt == nil || st.OrphanedPID {
		t.Fatalf("после успешного старта признак не снят: %+v", st)
	}
}
