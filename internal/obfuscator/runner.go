package obfuscator

import (
	"bufio"
	"context"
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
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type RunnerDeps struct {
	// BinaryFor — путь к бинарю разновидности; докачивает по F98, если его нет.
	BinaryFor func(ctx context.Context, flavor string) (string, error)
	Log       *logging.ScopedLogger
}

// Runner — один процесс wg-obfuscator на туннель. Состояние вне памяти: pidfile
// в RunDir (tmpfs) и конфиг в ConfDir; процесс переживает рестарт демона и
// усыновляется по pidfile (AdoptAll). Автоперезапуск по crash-loop — не в
// первой версии (Q9): падение видно как «обфускатор не запущен».
type Runner struct {
	deps RunnerDeps
	mu   sync.Mutex
	// tails: tunnelID -> cancel хвоста stderr.
	tails map[string]context.CancelFunc
	// matchFn — «pid принадлежит нашему бинарю»; pidfile переживает рестарт
	// демона, а pid может достаться чужому процессу. Тесты подменяют.
	matchFn func(pid int) bool
}

var ourBinaries = []string{BinaryName(storage.ObfuscatorFlavorPhobos), BinaryName(storage.ObfuscatorFlavorClusterM)}

func NewRunner(d RunnerDeps) *Runner {
	return &Runner{
		deps:    d,
		tails:   map[string]context.CancelFunc{},
		matchFn: func(pid int) bool { return childproc.MatchesAnyBinary(pid, ourBinaries...) },
	}
}

func pidPath(id string) string { return filepath.Join(RunDir, id+".pid") }
func errPath(id string) string { return filepath.Join(RunDir, id+".err.log") }

// Start пишет конфиг и поднимает процесс. Живой процесс с тем же конфигом
// не трогает; с другим — перезапускает (Q21: правка параметров = рестарт
// только релея).
func (r *Runner) Start(ctx context.Context, tunnelID string, o *storage.Obfuscator) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := RenderConf(o)
	if cur, err := os.ReadFile(ConfPath(tunnelID)); err == nil && string(cur) == want && r.alive(tunnelID) {
		return nil
	}
	_ = r.stopLocked(tunnelID)
	if !PortFree(o.LocalPort) {
		return fmt.Errorf("loopback-порт %d занят другим процессом (родная установка Phobos?)", o.LocalPort)
	}
	bin, err := r.deps.BinaryFor(ctx, o.Flavor)
	if err != nil {
		return fmt.Errorf("бинарь обфускатора %s: %w", o.Flavor, err)
	}
	if err := WriteConf(tunnelID, o); err != nil {
		return err
	}
	if err := os.MkdirAll(RunDir, 0o755); err != nil {
		return err
	}
	// stderr — в файл на tmpfs, не в pipe: осиротевший ребёнок умер бы от
	// SIGPIPE при выходе демона (см. internal/singbox/process.go).
	errF, err := os.OpenFile(errPath(tunnelID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "--config", ConfPath(tunnelID))
	cmd.Stdout, cmd.Stderr = errF, errF
	childproc.SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = errF.Close()
		return fmt.Errorf("запуск %s: %w", filepath.Base(bin), err)
	}
	_ = errF.Close()
	go func() { _ = cmd.Wait() }() // не оставлять зомби; выход виден по Alive
	if err := os.WriteFile(pidPath(tunnelID), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		_ = childproc.KillGroup(cmd.Process.Pid)
		return err
	}
	r.startTailLocked(tunnelID, false)
	r.deps.Log.Info("obfuscator", tunnelID, fmt.Sprintf("%s запущен, 127.0.0.1:%d -> %s (%s)", o.Flavor, o.LocalPort, o.Target, o.Masking))
	return nil
}

func (r *Runner) Stop(tunnelID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopLocked(tunnelID)
}

func (r *Runner) stopLocked(tunnelID string) error {
	if c := r.tails[tunnelID]; c != nil {
		c()
		delete(r.tails, tunnelID)
	}
	pid, ok := readPID(tunnelID)
	if !ok {
		return nil
	}
	if childproc.IsAlive(pid) && r.matchFn(pid) {
		_ = childproc.TerminateGroup(pid)
		for deadline := time.Now().Add(3 * time.Second); childproc.IsAlive(pid) && time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
		}
		if childproc.IsAlive(pid) {
			_ = childproc.KillGroup(pid)
		}
	}
	_ = os.Remove(pidPath(tunnelID))
	return nil
}

// Alive намеренно без r.mu: разделяемого состояния раннера он не трогает —
// только pidfile, kill(0) и matchFn (ставится один раз при создании). Под
// локом он ждал бы до 3 с грейса Stop/AdoptAll по ЧУЖОМУ туннелю, а его
// зовёт опрос состояния.
func (r *Runner) Alive(tunnelID string) bool { return r.alive(tunnelID) }

func (r *Runner) alive(tunnelID string) bool {
	pid, ok := readPID(tunnelID)
	return ok && childproc.IsAlive(pid) && r.matchFn(pid)
}

func (r *Runner) pid(tunnelID string) int { p, _ := readPID(tunnelID); return p }

// AdoptAll после рестарта демона: pidfile живого нашего процесса — усыновить,
// если keep(id) (туннель существует и включён), иначе погасить как сироту;
// мёртвые/чужие pidfile'ы — убрать. Возвращает усыновлённые tunnelID.
// keep зовётся ПОД r.mu: методы Runner из него звать нельзя — дедлок.
func (r *Runner) AdoptAll(keep func(tunnelID string) bool) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	matches, _ := filepath.Glob(filepath.Join(RunDir, "*.pid"))
	var adopted []string
	for _, p := range matches {
		id := strings.TrimSuffix(filepath.Base(p), ".pid")
		if !r.alive(id) {
			// Через stopLocked, а не os.Remove: у мёртвого процесса мог
			// остаться хвост stderr — иначе горутина висит вечно.
			_ = r.stopLocked(id)
			continue
		}
		if !keep(id) {
			_ = r.stopLocked(id)
			r.deps.Log.Info("obfuscator", id, "сирота остановлен (туннель удалён или выключен)")
			continue
		}
		r.startTailLocked(id, true)
		adopted = append(adopted, id)
	}
	return adopted
}

func readPID(tunnelID string) (int, bool) {
	b, err := os.ReadFile(pidPath(tunnelID))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid, err == nil && pid > 0
}

// startTailLocked — хвост stderr-файла в app-журнал; poll 500 мс, как у
// sing-box. fromEnd — при усыновлении: историю прошлого поколения не повторять.
func (r *Runner) startTailLocked(tunnelID string, fromEnd bool) {
	if c := r.tails[tunnelID]; c != nil {
		c()
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.tails[tunnelID] = cancel
	path := errPath(tunnelID) // путь резолвим здесь: горутина живёт дольше Stop
	go func() {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		if fromEnd {
			_, _ = f.Seek(0, io.SeekEnd)
		}
		rd := bufio.NewReader(f)
		for {
			line, err := rd.ReadString('\n')
			if line = strings.TrimSpace(line); line != "" {
				switch {
				case strings.Contains(line, "ERROR"):
					r.deps.Log.Error("stderr", tunnelID, line)
				case strings.Contains(line, "WARN"):
					r.deps.Log.Warn("stderr", tunnelID, line)
				default:
					r.deps.Log.Info("stderr", tunnelID, line)
				}
			}
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(500 * time.Millisecond):
				}
			}
		}
	}()
}
