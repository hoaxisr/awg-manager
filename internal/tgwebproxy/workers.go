package tgwebproxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

type WorkerDef struct {
	Name          string
	InitScript    string // Legacy / mock fallback
	BinPath       string
	StartArgs     []string
	StopArgs      []string
	PidFile       string
	StartTimeFile string
	ExeName       string
	CmdlineMatch  string
	Port          int
	IsHTTPMetrics bool
	MetricsURL    string
}

type CommandRunner interface {
	RunCommand(name string, args ...string) error
}

type DefaultCommandRunner struct{}

func (d DefaultCommandRunner) RunCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

type SocketOwnershipChecker func(pid int, port int) (bool, error)

type WorkerSupervisor struct {
	workers          []WorkerDef
	runner           CommandRunner
	dialer           func(network, addr string, timeout time.Duration) (net.Conn, error)
	httpGet          func(url string) (*http.Response, error)
	socketChecker    SocketOwnershipChecker
	readinessTimeout time.Duration
	mtg              *MtgEngine
	tproxy           *TproxyEngine
}

func atomicWriteMetadata(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func NewDefaultWorkerSupervisor() *WorkerSupervisor {
	workers := []WorkerDef{
		{
			Name:          "telemt-raw",
			InitScript:    "/opt/etc/init.d/S96telemt-raw",
			BinPath:       "/opt/bin/telemt",
			StartArgs:     []string{"start", "--pid-file", "/opt/var/run/telemt-raw.pid", "--log-file", "/opt/var/log/telemt-raw.log", "/opt/etc/awg-manager/telemt/raw.toml"},
			StopArgs:      []string{"stop", "--pid-file", "/opt/var/run/telemt-raw.pid"},
			PidFile:       "/opt/var/run/telemt-raw.pid",
			StartTimeFile: "/opt/var/run/telemt-raw.pid.start",
			ExeName:       "telemt",
			CmdlineMatch:  "raw.toml",
			Port:          DefaultRawPort,
		},
		{
			Name:          "telemt-direct",
			InitScript:    "/opt/etc/init.d/S99telemt",
			BinPath:       "/opt/bin/telemt",
			StartArgs:     []string{"start", "--pid-file", "/opt/var/run/telemt.pid", "--log-file", "/opt/var/log/telemt.log", "/opt/etc/awg-manager/telemt/config.toml"},
			StopArgs:      []string{"stop", "--pid-file", "/opt/var/run/telemt.pid"},
			PidFile:       "/opt/var/run/telemt.pid",
			StartTimeFile: "/opt/var/run/telemt.pid.start",
			ExeName:       "telemt",
			CmdlineMatch:  "config.toml",
			Port:          DefaultDirectPort,
		},
		{
			Name:          "tproxy-server",
			InitScript:    "/opt/etc/init.d/S95tproxy-server",
			BinPath:       "/opt/bin/tproxy-server",
			StartArgs:     []string{"-config", "/opt/etc/awg-manager/tproxy/config.json", "-profiles-file", "/opt/etc/awg-manager/tproxy/profiles.json"},
			PidFile:       "/opt/var/run/tproxy-server.pid",
			StartTimeFile: "/opt/var/run/tproxy-server.pid.start",
			ExeName:       "tproxy-server",
			CmdlineMatch:  "config.json",
			Port:          DefaultListenPort,
			IsHTTPMetrics: true,
			MetricsURL:    "http://127.0.0.1:8086/metrics",
		},
	}

	httpClient := &http.Client{Timeout: 1 * time.Second}

	return &WorkerSupervisor{
		workers: workers,
		runner:  DefaultCommandRunner{},
		dialer:  net.DialTimeout,
		httpGet: httpClient.Get,
		mtg:     NewMtgEngine(),
		tproxy:  NewTproxyEngine(defaultTproxyDir),
	}
}

func (ws *WorkerSupervisor) SetTproxyDir(dir string) {
	if ws != nil && ws.tproxy != nil && dir != "" {
		ws.tproxy.SetDir(dir)
	}
}

func (ws *WorkerSupervisor) isMock() bool {
	if ws == nil || ws.runner == nil {
		return false
	}
	_, isDefault := ws.runner.(DefaultCommandRunner)
	_, isDefaultPtr := ws.runner.(*DefaultCommandRunner)
	return !isDefault && !isDefaultPtr
}

func resolveWorkerBin(w WorkerDef) string {
	if w.BinPath != "" {
		if _, err := os.Stat(w.BinPath); err == nil {
			return w.BinPath
		}
	}
	if w.ExeName == "telemt" {
		for _, alt := range []string{"/opt/etc/awg-manager/telemt/telemt", "/opt/bin/telemt", "/opt/usr/bin/telemt"} {
			if _, err := os.Stat(alt); err == nil {
				return alt
			}
		}
	}
	if w.ExeName == "tproxy-server" {
		for _, alt := range []string{"/opt/bin/tproxy-server", "/opt/etc/awg-manager/tproxy/tproxy-server"} {
			if _, err := os.Stat(alt); err == nil {
				return alt
			}
		}
	}
	return w.BinPath
}

func (ws *WorkerSupervisor) CaptureWorkerStates() map[string]WorkerProcessState {
	if ws == nil {
		return make(map[string]WorkerProcessState)
	}
	states := make(map[string]WorkerProcessState, len(ws.workers))
	for _, w := range ws.workers {
		states[w.Name] = ws.GetWorkerState(w)
	}
	return states
}

func isInitScriptEnabled(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ENABLED=") {
			val := strings.ToLower(strings.TrimPrefix(line, "ENABLED="))
			val = strings.Trim(val, "\"'")
			return val == "yes" || val == "1" || val == "true"
		}
	}
	return false
}

func (ws *WorkerSupervisor) isWorkerEnabled(w WorkerDef) bool {
	if w.InitScript != "" {
		if _, err := os.Stat(w.InitScript); err == nil {
			return isInitScriptEnabled(w.InitScript)
		}
	}
	return true
}

func (ws *WorkerSupervisor) GetWorkerState(w WorkerDef) WorkerProcessState {
	if !ws.isMock() {
		if ws.mtg != nil {
			switch w.Name {
			case "telemt-direct":
				running := ws.mtg.IsDirectRunning()
				pid := 0
				if running {
					pid = os.Getpid()
				}
				return WorkerProcessState{
					Worker:  w.Name,
					Enabled: true,
					Running: running,
					PID:     pid,
				}
			case "telemt-raw":
				running := ws.mtg.IsRawRunning()
				pid := 0
				if running {
					pid = os.Getpid()
				}
				return WorkerProcessState{
					Worker:  w.Name,
					Enabled: true,
					Running: running,
					PID:     pid,
				}
			}
		}
		if ws.tproxy != nil && w.Name == "tproxy-server" {
			running := ws.tproxy.IsPrimaryRunning()
			pid := 0
			if running {
				pid = os.Getpid()
			}
			return WorkerProcessState{
				Worker:  w.Name,
				Enabled: true,
				Running: running,
				PID:     pid,
			}
		}
	}

	enabled := ws.isWorkerEnabled(w)
	pidData, err := os.ReadFile(w.PidFile)
	var pid int
	if err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(pidData)))
	}

	if pid <= 0 {
		// Try discovering orphan process by exe & cmdline
		foundPid, foundStartTime := findProcByPattern(w.ExeName, w.CmdlineMatch)
		if foundPid > 0 {
			if w.PidFile != "" {
				_ = atomicWriteMetadata(w.PidFile, []byte(strconv.Itoa(foundPid)), 0600)
			}
			if w.StartTimeFile != "" && foundStartTime > 0 {
				_ = atomicWriteMetadata(w.StartTimeFile, []byte(strconv.FormatInt(foundStartTime, 10)), 0600)
			}
			return WorkerProcessState{
				Worker:    w.Name,
				Enabled:   enabled,
				Running:   true,
				PID:       foundPid,
				StartTime: foundStartTime,
			}
		}
		return WorkerProcessState{Worker: w.Name, Running: false, Enabled: enabled}
	}

	ok, startTime := checkProcIdentity(pid, w.ExeName, w.CmdlineMatch)
	if !ok {
		// Stale PID in file, check if running under another PID
		foundPid, foundStartTime := findProcByPattern(w.ExeName, w.CmdlineMatch)
		if foundPid > 0 {
			if w.PidFile != "" {
				_ = atomicWriteMetadata(w.PidFile, []byte(strconv.Itoa(foundPid)), 0600)
			}
			if w.StartTimeFile != "" && foundStartTime > 0 {
				_ = atomicWriteMetadata(w.StartTimeFile, []byte(strconv.FormatInt(foundStartTime, 10)), 0600)
			}
			return WorkerProcessState{
				Worker:    w.Name,
				Enabled:   enabled,
				Running:   true,
				PID:       foundPid,
				StartTime: foundStartTime,
			}
		}
		return WorkerProcessState{Worker: w.Name, Running: false, Enabled: enabled}
	}

	// Verify PID reuse: compare actual proc startTime with recorded .pid.start
	if startData, err := os.ReadFile(w.StartTimeFile); err == nil {
		if st, err := strconv.ParseInt(strings.TrimSpace(string(startData)), 10, 64); err == nil && st > 0 {
			if st >= 1000000000 && startTime > 0 && startTime < 1000000000 {
				// Migrate legacy unix timestamp to proc stat starttime
				_ = atomicWriteMetadata(w.StartTimeFile, []byte(strconv.FormatInt(startTime, 10)), 0600)
				st = startTime
			}
			if startTime > 0 && startTime != st {
				// Process start time mismatch: PID was reused by another process!
				return WorkerProcessState{Worker: w.Name, Running: false, Enabled: enabled}
			}
			startTime = st
		}
	} else if startTime > 0 && w.StartTimeFile != "" {
		_ = atomicWriteMetadata(w.StartTimeFile, []byte(strconv.FormatInt(startTime, 10)), 0600)
	}

	return WorkerProcessState{
		Worker:    w.Name,
		Enabled:   enabled,
		Running:   true,
		PID:       pid,
		StartTime: startTime,
	}
}

func (ws *WorkerSupervisor) findWorker(name string) *WorkerDef {
	for i := range ws.workers {
		if ws.workers[i].Name == name {
			return &ws.workers[i]
		}
	}
	return nil
}

func (ws *WorkerSupervisor) startWorker(w WorkerDef) error {
	bin := resolveWorkerBin(w)
	if bin == "" {
		return fmt.Errorf("no binary path configured for %s", w.Name)
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("binary %s not found: %w", bin, err)
	}

	// Check if already running
	st := ws.GetWorkerState(w)
	if st.Running && st.PID > 0 {
		return nil
	}

	_ = os.MkdirAll("/opt/var/run", 0755)
	_ = os.MkdirAll("/opt/var/log", 0755)

	if w.Name == "telemt-raw" || w.Name == "telemt-direct" {
		cmd := exec.Command(bin, w.StartArgs...)
		devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err == nil {
			defer devnull.Close()
			cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
		}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("start %s: %w", w.Name, err)
		}
		// Poll for PID file and record start time
		for i := 0; i < 20; i++ {
			if data, err := os.ReadFile(w.PidFile); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
					if ok, startTime := checkProcIdentity(pid, w.ExeName, w.CmdlineMatch); ok {
						if startTime > 0 && w.StartTimeFile != "" {
							_ = atomicWriteMetadata(w.StartTimeFile, []byte(strconv.FormatInt(startTime, 10)), 0600)
						}
						return nil
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	}

	// Foreground daemon child process (tproxy-server)
	cmd := exec.Command(bin, w.StartArgs...)
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		defer devnull.Close()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	}
	childproc.SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", w.Name, err)
	}
	pid := cmd.Process.Pid
	go func() {
		_ = cmd.Wait()
	}()
	if w.PidFile != "" {
		_ = atomicWriteMetadata(w.PidFile, []byte(strconv.Itoa(pid)), 0600)
	}
	startTime := getProcStartTime(pid)
	if startTime > 0 && w.StartTimeFile != "" {
		_ = atomicWriteMetadata(w.StartTimeFile, []byte(strconv.FormatInt(startTime, 10)), 0600)
	}
	return nil
}

func (ws *WorkerSupervisor) stopWorker(w WorkerDef) error {
	bin := resolveWorkerBin(w)
	pid := 0
	if data, err := os.ReadFile(w.PidFile); err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}

	if w.Name == "telemt-raw" || w.Name == "telemt-direct" {
		if bin != "" && len(w.StopArgs) > 0 {
			stopCmd := exec.Command(bin, w.StopArgs...)
			_ = stopCmd.Run()
			for i := 0; i < 5; i++ {
				if pid > 0 && !childproc.IsAlive(pid) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}

	if pid > 0 && childproc.MatchesBinary(pid, w.ExeName) {
		_ = childproc.TerminateGroup(pid)
		for i := 0; i < 20; i++ {
			if !childproc.IsAlive(pid) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if childproc.IsAlive(pid) {
			_ = childproc.KillGroup(pid)
		}
	}

	if w.PidFile != "" {
		_ = os.Remove(w.PidFile)
	}
	if w.StartTimeFile != "" {
		_ = os.Remove(w.StartTimeFile)
	}
	return nil
}

func (ws *WorkerSupervisor) stopAllExtraEndpoints() {
	if matches, err := filepath.Glob("/opt/var/run/tproxy-config_*.pid"); err == nil {
		for _, pidFile := range matches {
			if data, err := os.ReadFile(pidFile); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
					if childproc.MatchesBinary(pid, "tproxy-server") {
						_ = childproc.TerminateGroup(pid)
						for i := 0; i < 15; i++ {
							if !childproc.IsAlive(pid) {
								break
							}
							time.Sleep(100 * time.Millisecond)
						}
						if childproc.IsAlive(pid) {
							_ = childproc.KillGroup(pid)
						}
					}
				}
			}
			_ = os.Remove(pidFile)
			_ = os.Remove(pidFile + ".start")
		}
	}
}

func (ws *WorkerSupervisor) reconcileExtraEndpoints(activeIDs map[string]bool) {
	bin := resolveWorkerBin(WorkerDef{ExeName: "tproxy-server", BinPath: "/opt/bin/tproxy-server"})
	profilesFile := "/opt/etc/awg-manager/tproxy/profiles.json"

	// 1. Clean up obsolete endpoints
	if matches, err := filepath.Glob("/opt/var/run/tproxy-config_*.pid"); err == nil {
		for _, pidFile := range matches {
			base := strings.TrimSuffix(filepath.Base(pidFile), ".pid")
			epID := strings.TrimPrefix(base, "tproxy-config_")
			if !activeIDs[epID] {
				if data, err := os.ReadFile(pidFile); err == nil {
					if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
						if childproc.MatchesBinary(pid, "tproxy-server") {
							_ = childproc.TerminateGroup(pid)
							for i := 0; i < 15; i++ {
								if !childproc.IsAlive(pid) {
									break
								}
								time.Sleep(100 * time.Millisecond)
							}
							if childproc.IsAlive(pid) {
								_ = childproc.KillGroup(pid)
							}
						}
					}
				}
				_ = os.Remove(pidFile)
				_ = os.Remove(pidFile + ".start")
			}
		}
	}

	// 2. Start missing active extra endpoints
	for epID := range activeIDs {
		cfgPath := fmt.Sprintf("/opt/etc/awg-manager/tproxy/config_%s.json", epID)
		if _, err := os.Stat(cfgPath); err != nil {
			continue
		}
		pidFile := fmt.Sprintf("/opt/var/run/tproxy-config_%s.pid", epID)
		startFile := fmt.Sprintf("/opt/var/run/tproxy-config_%s.pid.start", epID)

		running := false
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				if ok, _ := checkProcIdentity(pid, "tproxy-server", cfgPath); ok {
					running = true
				}
			}
		}
		if running {
			continue
		}

		epProfilesFile := fmt.Sprintf("/opt/etc/awg-manager/tproxy/profiles_%s.json", epID)
		if _, err := os.Stat(epProfilesFile); err != nil {
			epProfilesFile = profilesFile
		}

		cmd := exec.Command(bin, "-config", cfgPath, "-profiles-file", epProfilesFile)
		devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err == nil {
			defer devnull.Close()
			cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
		}
		childproc.SetProcessGroup(cmd)
		if err := cmd.Start(); err == nil {
			pid := cmd.Process.Pid
			go func() {
				_ = cmd.Wait()
			}()
			_ = atomicWriteMetadata(pidFile, []byte(strconv.Itoa(pid)), 0600)
			st := getProcStartTime(pid)
			if st > 0 {
				_ = atomicWriteMetadata(startFile, []byte(strconv.FormatInt(st, 10)), 0600)
			}
		}
	}
}

func (ws *WorkerSupervisor) RestartWorker(name string) error {
	w := ws.findWorker(name)
	if w == nil {
		return fmt.Errorf("worker %s not found", name)
	}
	if ws.isMock() {
		target := w.InitScript
		if target == "" {
			target = w.BinPath
		}
		return ws.runner.RunCommand(target, "restart")
	}
	if ws.mtg != nil && (name == "telemt-raw" || name == "telemt-direct") {
		return ws.mtg.Restart()
	}
	if ws.tproxy != nil && name == "tproxy-server" {
		return ws.tproxy.Restart()
	}
	_ = ws.stopWorker(*w)
	if name == "tproxy-server" {
		ws.stopAllExtraEndpoints()
	}
	time.Sleep(200 * time.Millisecond)
	return ws.startWorker(*w)
}

func (ws *WorkerSupervisor) ApplyWorkers(cfg Config) error {
	if ws == nil {
		return nil
	}

	if ws.isMock() {
		if !cfg.Enabled {
			for _, name := range []string{"tproxy-server", "telemt-raw", "telemt-direct"} {
				if w := ws.findWorker(name); w != nil {
					target := w.InitScript
					if target == "" {
						target = w.BinPath
					}
					_ = ws.runner.RunCommand(target, "stop")
				}
			}
			return nil
		}

		if !cfg.IsWebEnabled() {
			if w := ws.findWorker("tproxy-server"); w != nil {
				target := w.InitScript
				if target == "" {
					target = w.BinPath
				}
				_ = ws.runner.RunCommand(target, "stop")
			}
		}
		if !cfg.IsRawEnabled() {
			if w := ws.findWorker("telemt-raw"); w != nil {
				target := w.InitScript
				if target == "" {
					target = w.BinPath
				}
				_ = ws.runner.RunCommand(target, "stop")
			}
		}
		if !cfg.IsDirectEnabled() {
			if w := ws.findWorker("telemt-direct"); w != nil {
				target := w.InitScript
				if target == "" {
					target = w.BinPath
				}
				_ = ws.runner.RunCommand(target, "stop")
			}
		}

		type workerTask struct {
			name    string
			enabled bool
		}
		tasks := []workerTask{
			{"telemt-direct", cfg.IsDirectEnabled()},
			{"telemt-raw", cfg.IsRawEnabled()},
			{"tproxy-server", cfg.IsWebEnabled()},
		}

		for _, t := range tasks {
			if !t.enabled {
				continue
			}
			w := ws.findWorker(t.name)
			if w == nil {
				continue
			}
			target := w.InitScript
			if target == "" {
				target = w.BinPath
			}
			if err := ws.runner.RunCommand(target, "restart"); err != nil {
				if err2 := ws.runner.RunCommand(target, "start"); err2 != nil {
					return fmt.Errorf("worker %s start failed: %w", w.Name, err)
				}
			}
		}
		return nil
	}

	// Production: Direct process supervision with embedded Go mtg & tproxy engines

	if !cfg.Enabled {
		if ws.tproxy != nil {
			ws.tproxy.Stop()
		} else if w := ws.findWorker("tproxy-server"); w != nil {
			_ = ws.stopWorker(*w)
		}
		if ws.mtg != nil {
			ws.mtg.Stop()
		} else {
			if w := ws.findWorker("telemt-raw"); w != nil {
				_ = ws.stopWorker(*w)
			}
			if w := ws.findWorker("telemt-direct"); w != nil {
				_ = ws.stopWorker(*w)
			}
		}
		return nil
	}

	// 1. Apply embedded mtg engine (direct Fake-TLS + raw obfuscated2 backend)
	if ws.mtg != nil {
		if err := ws.mtg.Apply(cfg); err != nil {
			return err
		}
	} else {
		if !cfg.IsRawEnabled() {
			if w := ws.findWorker("telemt-raw"); w != nil {
				_ = ws.stopWorker(*w)
			}
		}
		if !cfg.IsDirectEnabled() {
			if w := ws.findWorker("telemt-direct"); w != nil {
				_ = ws.stopWorker(*w)
			}
		}
		if cfg.IsDirectEnabled() {
			if w := ws.findWorker("telemt-direct"); w != nil {
				if err := ws.startWorker(*w); err != nil {
					return err
				}
			}
		}
		if cfg.IsRawEnabled() {
			if w := ws.findWorker("telemt-raw"); w != nil {
				if err := ws.startWorker(*w); err != nil {
					return err
				}
			}
		}
	}

	// 2. Apply embedded tproxy engine (primary + extra endpoints)
	if ws.tproxy != nil {
		if err := ws.tproxy.Apply(cfg); err != nil {
			return err
		}
	} else {
		if !cfg.IsWebEnabled() {
			if w := ws.findWorker("tproxy-server"); w != nil {
				_ = ws.stopWorker(*w)
			}
			ws.stopAllExtraEndpoints()
		} else {
			if w := ws.findWorker("tproxy-server"); w != nil {
				if err := ws.startWorker(*w); err != nil {
					return err
				}
			}
			activeIDs := make(map[string]bool)
			for i, ep := range cfg.GetEndpoints() {
				if i > 0 && ep.ID != "" && ep.Enabled && !ep.ExternalBackend {
					activeIDs[ep.ID] = true
				}
			}
			ws.reconcileExtraEndpoints(activeIDs)
		}
	}

	return nil
}

func (ws *WorkerSupervisor) CheckReadiness(cfg Config) error {
	if ws == nil || !cfg.Enabled {
		return nil
	}
	for _, w := range ws.workers {
		if w.Name == "telemt-direct" && !cfg.IsDirectEnabled() {
			continue
		}
		if w.Name == "telemt-raw" && !cfg.IsRawEnabled() {
			continue
		}
		if w.Name == "tproxy-server" && !cfg.IsWebEnabled() {
			continue
		}

		if !ws.isMock() {
			if ws.mtg != nil {
				if w.Name == "telemt-direct" {
					if !ws.mtg.IsDirectRunning() {
						return fmt.Errorf("mtg direct listener is not running")
					}
					continue
				}
				if w.Name == "telemt-raw" {
					if !ws.mtg.IsRawRunning() {
						return fmt.Errorf("mtg raw listener is not running")
					}
					continue
				}
			}
			if ws.tproxy != nil && w.Name == "tproxy-server" {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := ws.tproxy.CheckPrimaryReady(ctx)
				cancel()
				if err != nil {
					return fmt.Errorf("readiness check failed for worker %s: %w", w.Name, err)
				}
				continue
			}
		}

		timeout := ws.readinessTimeout
		if timeout <= 0 {
			timeout = 90 * time.Second
		}
		deadline := time.Now().Add(timeout)
		ready := false
		var lastErr error
		for time.Now().Before(deadline) {
			st := ws.GetWorkerState(w)
			if !st.Running {
				lastErr = fmt.Errorf("worker %s is not running (pid %d)", w.Name, st.PID)
				time.Sleep(100 * time.Millisecond)
				continue
			}

			if w.IsHTTPMetrics {
				resp, err := ws.httpGet(w.MetricsURL)
				if err == nil {
					body, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					if resp.StatusCode == http.StatusOK && strings.Contains(string(body), "tproxy_") {
						ready = true
						break
					}
					lastErr = fmt.Errorf("http metrics returned status %d or unexpected body", resp.StatusCode)
				} else {
					lastErr = fmt.Errorf("http get metrics: %w", err)
				}
			} else {
				addr := fmt.Sprintf("127.0.0.1:%d", w.Port)
				conn, err := ws.dialer("tcp", addr, 300*time.Millisecond)
				if err == nil {
					_ = conn.Close()
					checker := ws.socketChecker
					if checker == nil {
						checker = checkSocketOwnership
					}
					owned, sErr := checker(st.PID, w.Port)
					if sErr != nil || !owned {
						lastErr = fmt.Errorf("socket ownership check failed on port %d (pid %d): err=%v, owned=%v", w.Port, st.PID, sErr, owned)
						time.Sleep(100 * time.Millisecond)
						continue
					}
					ready = true
					break
				} else {
					lastErr = fmt.Errorf("tcp dial %s: %w", addr, err)
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !ready {
			if lastErr != nil {
				return fmt.Errorf("readiness timeout for worker %s: %w", w.Name, lastErr)
			}
			return fmt.Errorf("readiness timeout for worker %s", w.Name)
		}
	}
	return nil
}

func (ws *WorkerSupervisor) RestoreWorkerStates(states map[string]WorkerProcessState) error {
	if ws == nil {
		return nil
	}
	if ws.isMock() {
		// Stop workers that were not running first (stop order)
		for _, name := range []string{"tproxy-server", "telemt-raw", "telemt-direct"} {
			if st, ok := states[name]; !ok || !st.Running {
				if w := ws.findWorker(name); w != nil {
					target := w.InitScript
					if target == "" {
						target = w.BinPath
					}
					_ = ws.runner.RunCommand(target, "stop")
				}
			}
		}
		// Start workers that were running (start order)
		for _, name := range []string{"telemt-direct", "telemt-raw", "tproxy-server"} {
			if st, ok := states[name]; ok && st.Running {
				if w := ws.findWorker(name); w != nil {
					target := w.InitScript
					if target == "" {
						target = w.BinPath
					}
					_ = ws.runner.RunCommand(target, "start")
				}
			}
		}
		return nil
	}

	var restoreErrs []error
	if ws.mtg != nil {
		directWasRunning := states["telemt-direct"].Running
		rawWasRunning := states["telemt-raw"].Running
		if !directWasRunning && !rawWasRunning {
			ws.mtg.Stop()
		} else {
			if err := ws.mtg.Restart(); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore mtg worker: %w", err))
			}
		}
	}
	if ws.tproxy != nil {
		if st, ok := states["tproxy-server"]; !ok || !st.Running {
			ws.tproxy.Stop()
		} else {
			if err := ws.tproxy.Restart(); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore tproxy worker: %w", err))
			}
		}
		if len(restoreErrs) > 0 {
			return fmt.Errorf("restore workers failed: %v", restoreErrs)
		}
		return nil
	}

	// Fallback if ws.tproxy is nil
	if st, ok := states["tproxy-server"]; !ok || !st.Running {
		if w := ws.findWorker("tproxy-server"); w != nil {
			if err := ws.stopWorker(*w); err != nil {
				restoreErrs = append(restoreErrs, err)
			}
		}
		ws.stopAllExtraEndpoints()
	} else {
		if w := ws.findWorker("tproxy-server"); w != nil {
			if err := ws.startWorker(*w); err != nil {
				restoreErrs = append(restoreErrs, err)
			}
		}
	}
	if len(restoreErrs) > 0 {
		return fmt.Errorf("restore workers failed: %v", restoreErrs)
	}
	return nil
}
