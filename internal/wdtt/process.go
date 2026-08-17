package wdtt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/sys/routerclock"
)

const startupGrace = 1500 * time.Millisecond

// drainGrace — сколько ждать естественного EOF пайпов после реапа ребёнка,
// прежде чем добить выживших членов группы и принудительно закрыть
// read-концы. Должно быть меньше startupGrace: иначе ребёнок, умерший
// мгновенно с выжившим хелпером, никогда не попадёт в ветку errCh раньше
// startupGrace — Start() вернёт ложный nil («успех»), а не ошибку старта.
const drainGrace = 1 * time.Second

type process struct {
	name    string
	binary  string
	pidPath string

	startMu sync.Mutex // serializes Start so two concurrent calls can't both spawn

	mu sync.Mutex
	// startInFlight — свой процесс уже запущен и записан в pid-файл, но
	// startupGrace ещё не истёк, поэтому startedAt пуст. Признак существует
	// ровно ради OrphanedPID: без него Status() в этом окне выдавал бы своего
	// свежепущенного ребёнка за унаследованный pid-файл.
	startInFlight      bool
	startedAt          *time.Time
	lastErr            string
	stopRequested      bool
	lastWgConfig       string // last CONFIG event seen in drain; survives log ring-buffer eviction
	lastRawConfPayload RawConfPayload
	logTail            *childproc.RingBuffer
	startCmd           func(bin string, args ...string) *exec.Cmd
	// signalProc — тот же приём шва, что startCmd: без него провал доставки
	// SIGHUP живому процессу не воспроизвести (kill своему ребёнку не
	// отказывает), а «не применилось» — один из трёх исходов, которые ручка
	// абонентов отдаёт наружу.
	signalProc func(pid int, sig syscall.Signal) error

	// drainStartDelay искусственно задерживает старт чтения пайпа —
	// тест-seam для форсирования окна гонки «Wait закрыл пайп раньше drain».
	// В проде zero → без эффекта.
	drainStartDelay time.Duration
}

func newProcess(name, binary, runtimeDir string) *process {
	return &process{
		name:    name,
		binary:  binary,
		pidPath: filepath.Join(runtimeDir, "wdtt-"+name+".pid"),
		logTail: childproc.NewRingBuffer(processLogMaxLines),
		startCmd: func(bin string, args ...string) *exec.Cmd {
			return exec.Command(bin, args...)
		},
		signalProc: childproc.Signal,
	}
}

func (p *process) Start(args []string) error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if running, _ := p.IsRunning(); running {
		p.mu.Lock()
		tracked := p.startedAt != nil
		p.mu.Unlock()
		if tracked {
			return nil
		}
		// PID file from a previous awg-manager run — no log capture in this process.
		_ = p.Stop()
	}
	if p.binary == "" {
		return fmt.Errorf("wdtt %s: путь к бинарю не задан", p.name)
	}
	if !binaryPresent(p.binary) {
		role := "wdtt"
		if strings.Contains(filepath.Base(p.binary), "server") {
			role = "wdtt-server"
		} else if strings.Contains(filepath.Base(p.binary), "client") {
			role = "wdtt-client"
		}
		return fmt.Errorf("бинарь %s не найден — установите %s", p.binary, role)
	}
	if err := os.MkdirAll(filepath.Dir(p.pidPath), 0755); err != nil {
		return err
	}

	cmd := p.startCmd(p.binary, args...)
	cmd.Env = wdttRuntimeEnv(os.Environ())
	childproc.SetProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("wdtt %s: stdout: %w", p.name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("wdtt %s: stderr: %w", p.name, err)
	}

	p.logTail.Reset()
	p.mu.Lock()
	p.stopRequested = false
	p.lastWgConfig = ""
	p.lastRawConfPayload = RawConfPayload{}
	p.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("wdtt %s: start: %w", p.name, err)
	}
	pid := cmd.Process.Pid
	// Метку ставим ДО writePID: наружу процесс появляется вместе с pid-файлом,
	// и окно «живой, но startedAt нет» открывается именно там.
	p.setStartInFlight(true)
	defer p.setStartInFlight(false)

	var drainWG sync.WaitGroup
	drainWG.Add(2)
	go func() { defer drainWG.Done(); p.drain(stdout) }()
	go func() { defer drainWG.Done(); p.drain(stderr) }()

	errCh := make(chan error, 1)
	go func() {
		// Reap НЕМЕДЛЕННО по смерти ребёнка, не дожидаясь EOF пайпов:
		// хелпер, унаследовавший stdout/stderr, раньше держал cmd.Wait
		// заложником → зомби + IsRunning()==true для мёртвого процесса.
		state, waitErr := cmd.Process.Wait()
		if waitErr == nil && state != nil && !state.Success() {
			waitErr = &exec.ExitError{ProcessState: state}
		}
		// Дать drain'ам дочитать хвост естественным EOF; если пайп держит
		// выживший хелпер — добить всю группу (pgid ребёнка не
		// переиспользуется, пока в ней есть живые члены — заодно даёт
		// честный EOF) и принудительно закрыть read-концы, чтобы не течь
		// горутинами drain.
		done := make(chan struct{})
		go func() { drainWG.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(drainGrace):
			_ = childproc.KillGroup(pid)
			_ = stdout.Close()
			_ = stderr.Close()
			<-done
		}
		// cmd.Wait() раньше закрывал parent-концы пайпов сам; теперь его
		// нет (двойной wait — ошибка), закрываем явно на ОБЕИХ ветках —
		// иначе на штатном пути (EOF раньше drainGrace) read-концы никто
		// не закрывает, и они текут до GC.
		_ = stdout.Close()
		_ = stderr.Close()
		errCh <- waitErr
	}()

	if err := p.writePID(pid); err != nil {
		_ = childproc.TerminateGroup(pid)
		<-errCh
		return fmt.Errorf("wdtt %s: pidfile: %w", p.name, err)
	}

	myPid := pid

	select {
	case waitErr := <-errCh:
		p.cleanupPidIfOurs(myPid)
		// К моменту получения из errCh drain'ы гарантированно завершены
		// (errCh получает значение только после <-done в горутине выше).
		msg := strings.TrimSpace(p.logTail.LastLines(30))
		if msg == "" && waitErr != nil {
			msg = waitErr.Error()
		}
		p.setLastErr(msg)
		return fmt.Errorf("wdtt %s exited during startup: %s", p.name, msg)
	case <-time.After(startupGrace):
		now := time.Now()
		p.mu.Lock()
		p.startedAt = &now
		p.lastErr = ""
		p.mu.Unlock()
		go func() {
			waitErr := <-errCh
			p.cleanupPidIfOurs(myPid)
			p.mu.Lock()
			stopped := p.stopRequested
			p.stopRequested = false
			p.mu.Unlock()
			if stopped {
				p.setLastErr("")
			} else {
				tail := strings.TrimSpace(p.logTail.LastLines(10))
				if tail == "" && waitErr != nil {
					tail = waitErr.Error()
				}
				p.setLastErr(tail)
			}
			p.mu.Lock()
			p.startedAt = nil
			p.mu.Unlock()
		}()
		return nil
	}
}

func (p *process) Stop() error {
	pid, err := p.readPID()
	if err != nil {
		return nil
	}
	if !p.pidIsOurs(pid) {
		_ = os.Remove(p.pidPath) // протухший pid-файл, чужой процесс не трогаем
		return nil
	}
	p.mu.Lock()
	p.stopRequested = true
	p.mu.Unlock()
	_ = childproc.TerminateGroup(pid)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !childproc.IsAlive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if childproc.IsAlive(pid) {
		_ = childproc.KillGroup(pid)
	}
	_ = os.Remove(p.pidPath)
	p.mu.Lock()
	p.startedAt = nil
	p.mu.Unlock()
	return nil
}

// Reload просит запущенный процесс перечитать конфигурацию (SIGHUP).
// Сигнал уходит ТОЛЬКО своему процессу: pid берётся из pid-файла и проверяется
// pidIsOurs — pid-файл переживает ребут, и после него номер мог достаться
// постороннему. Сигнал по группе (-pid) не годится: в группе живут помощники.
//
// Первое значение — «сигнал отправлен живому процессу». Остановленный сервер
// отказом не считается (перечитывать некому и незачем), но и успехом
// доставки — тоже: вызывающий обязан различать эти исходы, иначе «применено
// сейчас» станет обещанием, которого никто не давал. Проверять «запущен ли»
// вторым вызовом IsRunning нельзя — между вызовами процесс успевает умереть.
func (p *process) Reload() (bool, error) {
	running, pid := p.IsRunning()
	if !running {
		return false, nil
	}
	if err := p.signalProc(pid, syscall.SIGHUP); err != nil {
		// Процесс умер между проверкой живости и сигналом: перечитывать некому,
		// и это ровно исход «сервер остановлен», а не отказ доставки. Файл уже
		// записан, состав вступит в силу при следующем запуске.
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (p *process) IsRunning() (bool, int) {
	pid, err := p.readPID()
	if err != nil {
		return false, 0
	}
	if !childproc.IsAlive(pid) {
		return false, pid
	}
	if !p.pidIsOurs(pid) {
		return false, pid
	}
	return true, pid
}

// pidIsOurs подтверждает, что pid из pid-файла — действительно наш процесс.
// Свой ребёнок (startedAt != nil) не может быть переиспользован, пока мы его
// не схоронили. Для унаследованного pid-файла (пережил ребут или рестарт
// демона) единственное доказательство — /proc cmdline: файл лежит на флешке,
// а PID после ребута мог достаться постороннему процессу.
func (p *process) pidIsOurs(pid int) bool {
	p.mu.Lock()
	tracked := p.startedAt != nil
	p.mu.Unlock()
	return tracked || childproc.MatchesBinary(pid, p.binary)
}

func (p *process) Status() ProcessStatus {
	running, pid := p.IsRunning()
	p.mu.Lock()
	defer p.mu.Unlock()
	st := ProcessStatus{
		Running:       running,
		LastError:     p.lastErr,
		Log:           p.logTail.String(),
		Binary:        p.binary,
		BinaryPresent: binaryPresent(p.binary),
	}
	if p.lastWgConfig != "" {
		st.WgConfig = p.lastWgConfig
	} else if wg := ExtractWGConfigFromLog(st.Log); wg != "" {
		st.WgConfig = wg
	}
	if conf, ok := p.lastRawConfLocked(); ok {
		st.RawClientIP = conf.ClientIP
	} else if conf, ok := ExtractRawConfFromLog(st.Log); ok {
		st.RawClientIP = conf.ClientIP
	}
	if running {
		st.DtlsConnections = ExtractActiveConnectionsFromLog(st.Log)
	}
	if running {
		st.PID = pid
		st.StartedAt = p.startedAt
		// Живой процесс без startedAt — унаследованный pid-файл (pidIsOurs
		// подтвердил его по /proc cmdline). Ровно это условие поднимает
		// супервизор на перезапуск, и наружу оно уходит отдельным признаком:
		// вычислять «running && !startedAt» на фронте — хрупко.
		// startInFlight отсекает своего ребёнка в окне startupGrace: у него
		// startedAt тоже пуст, но унаследованным он не является.
		st.OrphanedPID = p.startedAt == nil && !p.startInFlight
	}
	return st
}

func binaryPresent(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode().Perm()&0111 != 0
}

func wdttRuntimeEnv(base []string) []string {
	base = routerclock.WithTZFromRouter(base)
	has := false
	for _, e := range base {
		if strings.HasPrefix(e, "WDTT_EVENTS=") {
			has = true
			break
		}
	}
	if !has {
		base = append(base, "WDTT_EVENTS=1")
	}
	return base
}

func (p *process) drain(r io.Reader) {
	if p.drainStartDelay > 0 {
		time.Sleep(p.drainStartDelay)
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		p.logTail.WriteLine(line)
		if conf := parseWGConfigEvent(line); conf != "" {
			p.mu.Lock()
			p.lastWgConfig = conf
			p.mu.Unlock()
		}
		if conf, ok := parseRawConfLine(line); ok {
			p.mu.Lock()
			p.lastRawConfPayload = conf
			p.mu.Unlock()
		}
	}
}

func (p *process) lastRawConfLocked() (RawConfPayload, bool) {
	if strings.TrimSpace(p.lastRawConfPayload.ClientIP) == "" {
		return RawConfPayload{}, false
	}
	return p.lastRawConfPayload, true
}

// lastRawConf — вход извне: замок берёт он, а не lastRawConfLocked (та зовётся
// из Status уже под замком). Поле пишет drain из своей горутины, так что чтение
// без замка — настоящая гонка, а не формальность.
func (p *process) lastRawConf() (RawConfPayload, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastRawConfLocked()
}

func (p *process) setStartInFlight(v bool) {
	p.mu.Lock()
	p.startInFlight = v
	p.mu.Unlock()
}

func (p *process) setLastErr(s string) {
	p.mu.Lock()
	p.lastErr = s
	p.mu.Unlock()
}

func (p *process) cleanupPidIfOurs(myPid int) {
	cur, err := p.readPID()
	if err != nil || cur != myPid {
		return
	}
	_ = os.Remove(p.pidPath)
}

func (p *process) readPID() (int, error) {
	b, err := os.ReadFile(p.pidPath)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func (p *process) writePID(pid int) error {
	return os.WriteFile(p.pidPath, []byte(strconv.Itoa(pid)), 0644)
}
