package freeturn

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/sys/routerclock"
)

// startupGrace is how long Start waits before declaring success. If the
// binary exits before this elapses (bad flags, missing -peer, immediate
// auth failure), Start returns an error built from the captured stderr
// tail instead of reporting a false "started".
//
// freeturn does VK-auth + TURN allocation on startup (see docs/quickstart.md),
// which is slower than sing-box's local config validation, hence the wider
// grace period than internal/singbox.Process uses.
const startupGrace = 1500 * time.Millisecond

// drainGrace — сколько ждать естественного EOF пайпов после реапа ребёнка,
// прежде чем добить выживших членов группы и принудительно закрыть
// read-концы. Должно быть меньше startupGrace: иначе ребёнок, умерший
// мгновенно с выжившим хелпером, никогда не попадёт в ветку errCh раньше
// startupGrace — Start() вернёт ложный nil («успех»), а не ошибку старта.
const drainGrace = 1 * time.Second

// process manages a single long-running freeturn invocation — either the
// client or the server binary, distinguished by `name` ("client"/"server")
// which is also used to namespace the PID file.
//
// Platform-specific bits (process-group setup, SIGTERM/SIGKILL, liveness
// probe) live in internal/childproc so the package still compiles on a
// non-Linux dev box even though it only ever runs on the Linux/ARM router.
type process struct {
	name    string
	binary  string
	pidPath string

	startMu sync.Mutex // serializes Start so two concurrent calls can't both spawn

	mu            sync.Mutex
	startedAt     *time.Time
	lastErr       string
	stopRequested bool // set by Stop() so the exit-watcher goroutine knows this death was expected

	logTail *childproc.RingBuffer

	// Seam for tests.
	startCmd func(bin string, args ...string) *exec.Cmd

	// drainStartDelay искусственно задерживает старт чтения пайпа —
	// тест-seam для форсирования окна гонки «Wait закрыл пайп раньше drain».
	// В проде zero → без эффекта.
	drainStartDelay time.Duration
}

func newProcess(name, binary, runtimeDir string) *process {
	return &process{
		name:    name,
		binary:  binary,
		pidPath: filepath.Join(runtimeDir, "freeturn-"+name+".pid"),
		logTail: childproc.NewRingBuffer(processLogMaxLines),
		startCmd: func(bin string, args ...string) *exec.Cmd {
			return exec.Command(bin, args...)
		},
	}
}

// Start launches the binary with the given args. Returns nil once the
// process has survived startupGrace; returns an error (with stderr tail)
// if it exits before that. No-op if already running.
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
		return fmt.Errorf("freeturn %s: binary path not configured", p.name)
	}
	if !binaryPresent(p.binary) {
		return fmt.Errorf("бинарь %s не найден или не исполняем — установите freeturn из панели или переустановите awg-manager", p.binary)
	}
	if err := os.MkdirAll(filepath.Dir(p.pidPath), 0755); err != nil {
		return err
	}

	cmd := p.startCmd(p.binary, args...)
	cmd.Env = freeturnRuntimeEnv(os.Environ())
	childproc.SetProcessGroup(cmd) // platform-specific (Setsid on Linux, no-op elsewhere)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("freeturn %s: stdout pipe: %w", p.name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("freeturn %s: stderr pipe: %w", p.name, err)
	}

	p.logTail.Reset()
	p.mu.Lock()
	p.stopRequested = false
	p.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("freeturn %s: start: %w", p.name, err)
	}
	pid := cmd.Process.Pid

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
		return fmt.Errorf("freeturn %s: write pidfile: %w", p.name, err)
	}

	myPid := pid

	select {
	case waitErr := <-errCh:
		// Died before grace period — this is a startup failure.
		p.cleanupPidIfOurs(myPid)
		// К моменту получения из errCh drain'ы гарантированно завершены
		// (errCh получает значение только после <-done в горутине выше).
		msg := strings.TrimSpace(p.logTail.LastLines(30))
		if msg == "" && waitErr != nil {
			msg = waitErr.Error()
		}
		p.setLastErr(msg)
		return fmt.Errorf("freeturn %s exited during startup: %s", p.name, msg)

	case <-time.After(startupGrace):
		now := time.Now()
		p.mu.Lock()
		p.startedAt = &now
		p.lastErr = ""
		p.mu.Unlock()

		// Keep watching in the background so a later crash (after the
		// grace period) still updates status / cleans up the pidfile.
		go func() {
			waitErr := <-errCh
			p.cleanupPidIfOurs(myPid)

			p.mu.Lock()
			stopped := p.stopRequested
			p.stopRequested = false
			p.mu.Unlock()

			if stopped {
				// We asked for this (Stop()) — not an error, don't dump the
				// accumulated connect/disconnect log as "last error".
				p.setLastErr("")
			} else {
				// Unexpected exit. Only the last few lines, not the whole
				// buffer — after a long run that buffer is mostly benign
				// per-stream connect/disconnect noise, not the actual cause.
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

// Stop sends SIGTERM, waits up to 3s, then SIGKILL. No-op if not running.
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
	_ = childproc.TerminateGroup(pid) // SIGTERM to the group on Linux, Process.Kill elsewhere

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !childproc.IsAlive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if childproc.IsAlive(pid) {
		_ = childproc.KillGroup(pid) // SIGKILL to the group on Linux, Process.Kill elsewhere
	}
	_ = os.Remove(p.pidPath)

	p.mu.Lock()
	p.startedAt = nil
	p.mu.Unlock()
	return nil
}

// IsRunning probes the PID file + process existence.
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
	ensureExecutable(p.binary)
	st := ProcessStatus{
		Running:       running,
		LastError:     p.lastErr,
		Log:           p.logTail.String(),
		Binary:        p.binary,
		BinaryPresent: binaryPresent(p.binary),
	}
	if running {
		st.PID = pid
		st.StartedAt = p.startedAt
		st.DtlsConnections = countActiveDTLSConnections(st.Log)
		// Живой процесс без startedAt — унаследованный pid-файл (pidIsOurs
		// подтвердил его по /proc cmdline). Ровно это условие поднимает
		// супервизор на перезапуск, и наружу оно уходит отдельным признаком:
		// вычислять «running && !startedAt» на фронте — хрупко.
		st.OrphanedPID = p.startedAt == nil
	}
	return st
}

// binaryPresent reports whether an executable regular file exists at path.
// awg-manager does not ship the freeturn binaries, so the panel needs to
// distinguish «не установлен» from a real start failure.
func binaryPresent(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode().Perm()&0111 != 0
}

func freeturnRuntimeEnv(base []string) []string {
	return routerclock.WithTZFromRouter(base)
}

func (p *process) drain(r io.Reader) {
	if p.drainStartDelay > 0 {
		time.Sleep(p.drainStartDelay)
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		p.logTail.WriteLine(sc.Text())
	}
}

func (p *process) setLastErr(s string) {
	p.mu.Lock()
	p.lastErr = s
	p.mu.Unlock()
}

// cleanupPidIfOurs removes the pidfile only if it still names myPid —
// avoids a race where a fresh Start already wrote a new PID by the time an
// old process's death is observed.
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
