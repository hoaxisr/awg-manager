package wdtt

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

const startupGrace = 1500 * time.Millisecond

type process struct {
	name    string
	binary  string
	pidPath string

	startMu sync.Mutex // serializes Start so two concurrent calls can't both spawn

	mu                 sync.Mutex
	startedAt          *time.Time
	lastErr            string
	stopRequested      bool
	lastWgConfig       string // last CONFIG event seen in drain; survives log ring-buffer eviction
	lastRawConfPayload RawConfPayload
	logTail            *childproc.RingBuffer
	startCmd           func(bin string, args ...string) *exec.Cmd

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
		// Дать drain'ам дочитать хвост; если пайп держит выживший хелпер —
		// принудительно закрыть read-концы, чтобы не течь горутинами.
		done := make(chan struct{})
		go func() { drainWG.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = stdout.Close()
			_ = stderr.Close()
			<-done
		}
		errCh <- waitErr
	}()
	// cmd.Wait() после этого не вызывается нигде — cmd.Process.Wait() выше
	// уже реапнул ребёнка (двойной wait — ошибка).

	if err := p.writePID(cmd.Process.Pid); err != nil {
		_ = childproc.TerminateGroup(cmd.Process.Pid)
		<-errCh
		return fmt.Errorf("wdtt %s: pidfile: %w", p.name, err)
	}

	myPid := cmd.Process.Pid

	select {
	case waitErr := <-errCh:
		p.cleanupPidIfOurs(myPid)
		// К моменту получения из errCh drain'ы гарантированно завершены (Wait
		// вызывается после drainWG.Wait()), хвост stderr уже в logTail.
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

func (p *process) lastRawConf() (RawConfPayload, bool) {
	return p.lastRawConfLocked()
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
