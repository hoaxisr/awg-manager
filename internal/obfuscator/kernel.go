package obfuscator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

const (
	procRelayAdd  = "/proc/awgm_relay/add"
	procRelayDel  = "/proc/awgm_relay/del"
	procRelayList = "/proc/awgm_relay/list"
)

type KernelDeps struct {
	Ensure    func(ctx context.Context) error
	ProcWrite func(path string, data []byte) error
	ProcRead  func(path string) ([]byte, error)
	Log       *logging.ScopedLogger
}

// KernelRunner — релей в awgm_relay.ko (спека §4.2). Слот переживает рестарт
// демона; строка add хранится в RunDir (tmpfs, 0600 — в ней ключ; слоты
// перезагрузку роутера тоже не переживают).
type KernelRunner struct {
	d  KernelDeps
	mu sync.Mutex
}

func NewKernelRunner(d KernelDeps) *KernelRunner { return &KernelRunner{d: d} }

func kmodLinePath(id string) string { return filepath.Join(RunDir, id+".kmod") }

func listen(port int) []byte { return []byte("127.0.0.1:" + strconv.Itoa(port)) }

func (k *KernelRunner) Start(ctx context.Context, tunnelID string, o *storage.Obfuscator, ip string) error {
	line, err := AddLine(o, ip)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if cur, err := os.ReadFile(kmodLinePath(tunnelID)); err == nil && string(cur) == line && k.live(o.LocalPort) {
		return nil
	}
	if err := k.d.Ensure(ctx); err != nil {
		return fmt.Errorf("awgm_relay: %w", err)
	}
	k.stopLocked(tunnelID)
	if k.live(o.LocalPort) { // слот прошлого поколения на том же порту
		_ = k.d.ProcWrite(procRelayDel, listen(o.LocalPort))
	}
	if err := k.d.ProcWrite(procRelayAdd, []byte(line)); err != nil {
		return fmt.Errorf("awgm_relay add 127.0.0.1:%d: %w", o.LocalPort, err)
	}
	if err := os.MkdirAll(RunDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(kmodLinePath(tunnelID), []byte(line), 0o600); err != nil {
		_ = k.d.ProcWrite(procRelayDel, listen(o.LocalPort))
		return err
	}
	if k.d.Log != nil {
		k.d.Log.Info("obfuscator", tunnelID, fmt.Sprintf("awgm_relay: 127.0.0.1:%d -> %s (%s)", o.LocalPort, ip, KernelMasking(o)))
	}
	return nil
}

func (k *KernelRunner) Stop(tunnelID string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stopLocked(tunnelID)
	return nil
}

func (k *KernelRunner) stopLocked(tunnelID string) {
	if port, ok := savedPort(tunnelID); ok && k.live(port) {
		_ = k.d.ProcWrite(procRelayDel, listen(port))
	}
	_ = os.Remove(kmodLinePath(tunnelID))
}

func (k *KernelRunner) Alive(tunnelID string) bool {
	port, ok := savedPort(tunnelID)
	return ok && k.live(port)
}

func (k *KernelRunner) Backend(tunnelID string) string {
	if k.Alive(tunnelID) {
		return BackendKernel
	}
	return ""
}

// Sweep — старт демона (§4.2): слоты, чей порт не нужен ни одному живому
// включённому туннелю, снять. Возвращает снятые порты.
func (k *KernelRunner) Sweep(keepPort func(port int) bool) []int {
	k.mu.Lock()
	defer k.mu.Unlock()
	var removed []int
	for _, port := range k.ports() {
		if !keepPort(port) {
			_ = k.d.ProcWrite(procRelayDel, listen(port))
			removed = append(removed, port)
		}
	}
	return removed
}

func (k *KernelRunner) live(port int) bool {
	for _, p := range k.ports() {
		if p == port {
			return true
		}
	}
	return false
}

func (k *KernelRunner) ports() []int {
	b, err := k.d.ProcRead(procRelayList)
	if err != nil {
		return nil
	}
	var out []int
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasPrefix(f[0], "127.0.0.1:") {
			continue
		}
		if p, err := strconv.Atoi(strings.TrimPrefix(f[0], "127.0.0.1:")); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func savedPort(tunnelID string) (int, bool) {
	b, err := os.ReadFile(kmodLinePath(tunnelID))
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, false
	}
	p, err := strconv.Atoi(strings.TrimPrefix(f[0], "127.0.0.1:"))
	return p, err == nil
}
