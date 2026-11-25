package tgwebproxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Ключи воркеров. Это имена прежних внешних процессов: они остаются ключами
// состояния в манифесте транзакции на диске (ProcessStateBefore) и в
// RestartWorker, поэтому не переименовываются.
const (
	workerRaw    = "telemt-raw"
	workerDirect = "telemt-direct"
	workerTproxy = "tproxy-server"
)

var workerNames = []string{workerRaw, workerDirect, workerTproxy}

// mtgRuntime — то, что супервизору нужно от MtgEngine.
type mtgRuntime interface {
	Apply(cfg Config) error
	Restart() error
	Stop()
	IsDirectRunning() bool
	IsRawRunning() bool
}

// tproxyRuntime — то, что супервизору нужно от TproxyEngine.
type tproxyRuntime interface {
	Apply(cfg Config) error
	Restart() error
	Stop()
	IsPrimaryRunning() bool
	CheckPrimaryReady(ctx context.Context) error
}

// WorkerSupervisor drives the in-process mtg (direct Fake-TLS + raw
// obfuscated2) and tproxy engines.
type WorkerSupervisor struct {
	mtg    mtgRuntime
	tproxy tproxyRuntime
}

func atomicWriteMetadata(path string, content []byte, mode os.FileMode) error {
	// Каталог — 0700; AtomicWritePerm создал бы его с 0755.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return storage.AtomicWritePerm(path, content, mode)
}

func NewDefaultWorkerSupervisor(tproxyDir string) *WorkerSupervisor {
	return &WorkerSupervisor{
		mtg:    NewMtgEngine(),
		tproxy: NewTproxyEngine(tproxyDir),
	}
}

func (ws *WorkerSupervisor) CaptureWorkerStates() map[string]WorkerProcessState {
	if ws == nil {
		return make(map[string]WorkerProcessState)
	}
	states := make(map[string]WorkerProcessState, len(workerNames))
	for _, name := range workerNames {
		states[name] = ws.GetWorkerState(name)
	}
	return states
}

func (ws *WorkerSupervisor) GetWorkerState(name string) WorkerProcessState {
	var running bool
	switch name {
	case workerDirect:
		running = ws.mtg.IsDirectRunning()
	case workerRaw:
		running = ws.mtg.IsRawRunning()
	case workerTproxy:
		running = ws.tproxy.IsPrimaryRunning()
	}
	pid := 0
	if running {
		pid = os.Getpid()
	}
	return WorkerProcessState{Worker: name, Enabled: true, Running: running, PID: pid}
}

func (ws *WorkerSupervisor) RestartWorker(name string) error {
	switch name {
	case workerRaw, workerDirect:
		return ws.mtg.Restart()
	case workerTproxy:
		return ws.tproxy.Restart()
	}
	return fmt.Errorf("worker %s not found", name)
}

func (ws *WorkerSupervisor) ApplyWorkers(cfg Config) error {
	if ws == nil {
		return nil
	}
	if !cfg.Enabled {
		ws.tproxy.Stop()
		ws.mtg.Stop()
		return nil
	}
	if err := ws.mtg.Apply(cfg); err != nil {
		return err
	}
	return ws.tproxy.Apply(cfg)
}

func (ws *WorkerSupervisor) CheckReadiness(cfg Config) error {
	if ws == nil || !cfg.Enabled {
		return nil
	}
	if cfg.IsRawEnabled() && !ws.mtg.IsRawRunning() {
		return fmt.Errorf("mtg raw listener is not running")
	}
	if cfg.IsDirectEnabled() && !ws.mtg.IsDirectRunning() {
		return fmt.Errorf("mtg direct listener is not running")
	}
	if cfg.IsWebEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := ws.tproxy.CheckPrimaryReady(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("readiness check failed for worker %s: %w", workerTproxy, err)
		}
	}
	return nil
}

// runningOnly — cfg для движков, работавших в states; остальным — та же
// конфигурация выключенной, чтобы последующий Restart не поднял прежнюю.
func runningOnly(cfg Config, states map[string]WorkerProcessState) (mtgCfg, tproxyCfg Config) {
	mtgCfg, tproxyCfg = cfg, cfg
	if !states[workerDirect].Running && !states[workerRaw].Running {
		mtgCfg.Enabled = false
	}
	if !states[workerTproxy].Running {
		tproxyCfg.Enabled = false
	}
	return mtgCfg, tproxyCfg
}

// RestoreConfig возвращает движкам прежнюю конфигурацию. Движок, который до
// транзакции не работал, не запускается: его старт мог бы провалить откат.
// Apply, а не Restart: тот поднял бы последнюю, отвергнутую конфигурацию.
func (ws *WorkerSupervisor) RestoreConfig(prev Config, states map[string]WorkerProcessState) error {
	if ws == nil {
		return nil
	}
	mtgCfg, tproxyCfg := runningOnly(prev, states)
	return errors.Join(ws.mtg.Apply(mtgCfg), ws.tproxy.Apply(tproxyCfg))
}

// ApplyToRunning применяет cfg к работающим движкам, остальные не поднимает.
// Движок, не принявший cfg, останавливается.
func (ws *WorkerSupervisor) ApplyToRunning(cfg Config, tproxyChanged bool) error {
	if ws == nil {
		return nil
	}
	mtgCfg, tproxyCfg := runningOnly(cfg, ws.CaptureWorkerStates())
	mtgErr := ws.mtg.Apply(mtgCfg)
	if mtgErr != nil {
		ws.mtg.Stop()
	}
	tproxyErr := ws.tproxy.Apply(tproxyCfg)
	if tproxyErr == nil && tproxyChanged && ws.tproxy.IsPrimaryRunning() {
		tproxyErr = ws.tproxy.Restart()
	}
	if tproxyErr != nil {
		ws.tproxy.Stop()
	}
	return errors.Join(mtgErr, tproxyErr)
}

func (ws *WorkerSupervisor) RestoreWorkerStates(states map[string]WorkerProcessState) error {
	if ws == nil {
		return nil
	}
	var restoreErrs []error
	if !states[workerDirect].Running && !states[workerRaw].Running {
		ws.mtg.Stop()
	} else if err := ws.mtg.Restart(); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("restore mtg worker: %w", err))
	}
	if !states[workerTproxy].Running {
		ws.tproxy.Stop()
	} else if err := ws.tproxy.Restart(); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("restore tproxy worker: %w", err))
	}
	if len(restoreErrs) > 0 {
		return fmt.Errorf("restore workers failed: %v", restoreErrs)
	}
	return nil
}
