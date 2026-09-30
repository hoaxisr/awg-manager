package tgwebproxy

import (
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
)

type WorkerDef struct {
	Name          string
	InitScript    string
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
			PidFile:       "/opt/var/run/telemt-raw.pid",
			StartTimeFile: "/opt/var/run/telemt-raw.pid.start",
			ExeName:       "telemt",
			CmdlineMatch:  "raw.toml",
			Port:          DefaultRawPort,
		},
		{
			Name:          "telemt-direct",
			InitScript:    "/opt/etc/init.d/S99telemt",
			PidFile:       "/opt/var/run/telemt.pid",
			StartTimeFile: "/opt/var/run/telemt.pid.start",
			ExeName:       "telemt",
			CmdlineMatch:  "config.toml",
			Port:          DefaultDirectPort,
		},
		{
			Name:          "tproxy-server",
			InitScript:    "/opt/etc/init.d/S95tproxy-server",
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
	}
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

func (ws *WorkerSupervisor) GetWorkerState(w WorkerDef) WorkerProcessState {
	pidData, err := os.ReadFile(w.PidFile)
	var pid int
	if err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(pidData)))
	}

	if pid <= 0 {
		// Try discovering orphan process by exe & cmdline
		foundPid, foundStartTime := findProcByPattern(w.ExeName, w.CmdlineMatch)
		if foundPid > 0 {
			_ = atomicWriteMetadata(w.PidFile, []byte(strconv.Itoa(foundPid)), 0600)
			_ = atomicWriteMetadata(w.StartTimeFile, []byte(strconv.FormatInt(foundStartTime, 10)), 0600)
			return WorkerProcessState{
				Worker:    w.Name,
				Enabled:   isInitScriptEnabled(w.InitScript),
				Running:   true,
				PID:       foundPid,
				StartTime: foundStartTime,
			}
		}
		return WorkerProcessState{Worker: w.Name, Running: false, Enabled: isInitScriptEnabled(w.InitScript)}
	}

	ok, startTime := checkProcIdentity(pid, w.ExeName, w.CmdlineMatch)
	if !ok {
		// Stale PID in file, check if running under another PID
		foundPid, foundStartTime := findProcByPattern(w.ExeName, w.CmdlineMatch)
		if foundPid > 0 {
			_ = atomicWriteMetadata(w.PidFile, []byte(strconv.Itoa(foundPid)), 0600)
			_ = atomicWriteMetadata(w.StartTimeFile, []byte(strconv.FormatInt(foundStartTime, 10)), 0600)
			return WorkerProcessState{
				Worker:    w.Name,
				Enabled:   isInitScriptEnabled(w.InitScript),
				Running:   true,
				PID:       foundPid,
				StartTime: foundStartTime,
			}
		}
		return WorkerProcessState{Worker: w.Name, Running: false, Enabled: isInitScriptEnabled(w.InitScript)}
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
				return WorkerProcessState{Worker: w.Name, Running: false, Enabled: isInitScriptEnabled(w.InitScript)}
			}
			startTime = st
		}
	} else if startTime > 0 {
		_ = atomicWriteMetadata(w.StartTimeFile, []byte(strconv.FormatInt(startTime, 10)), 0600)
	}

	return WorkerProcessState{
		Worker:    w.Name,
		Enabled:   isInitScriptEnabled(w.InitScript),
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

func (ws *WorkerSupervisor) ApplyWorkers(cfg Config) error {
	if ws == nil {
		return nil
	}
	if !cfg.Enabled {
		// Stop in reverse dependency order: tproxy-server -> telemt-raw -> telemt-direct
		for _, name := range []string{"tproxy-server", "telemt-raw", "telemt-direct"} {
			if w := ws.findWorker(name); w != nil {
				_ = ws.runner.RunCommand(w.InitScript, "stop")
			}
		}
		return nil
	}

	// 1. Stop workers disabled by scenario in stop order
	if !cfg.IsWebEnabled() {
		if w := ws.findWorker("tproxy-server"); w != nil {
			_ = ws.runner.RunCommand(w.InitScript, "stop")
		}
	}
	if !cfg.IsRawEnabled() {
		if w := ws.findWorker("telemt-raw"); w != nil {
			_ = ws.runner.RunCommand(w.InitScript, "stop")
		}
	}
	if !cfg.IsDirectEnabled() {
		if w := ws.findWorker("telemt-direct"); w != nil {
			_ = ws.runner.RunCommand(w.InitScript, "stop")
		}
	}

	// 2. Start workers enabled by scenario in start order: telemt-direct -> telemt-raw -> tproxy-server
	_ = os.MkdirAll("/opt/var/run", 0755)
	_ = os.Chmod("/opt/var/run", 0755)
	_ = os.Chmod("/opt/var", 0755)

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
		if err := ws.runner.RunCommand(w.InitScript, "restart"); err != nil {
			if err2 := ws.runner.RunCommand(w.InitScript, "start"); err2 != nil {
				return fmt.Errorf("worker %s start failed: %w", w.Name, err)
			}
		}
		time.Sleep(150 * time.Millisecond)
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
	// Stop workers that were not running first (stop order)
	for _, name := range []string{"tproxy-server", "telemt-raw", "telemt-direct"} {
		if st, ok := states[name]; !ok || !st.Running {
			if w := ws.findWorker(name); w != nil {
				_ = ws.runner.RunCommand(w.InitScript, "stop")
			}
		}
	}
	// Start workers that were running (start order)
	for _, name := range []string{"telemt-direct", "telemt-raw", "tproxy-server"} {
		if st, ok := states[name]; ok && st.Running {
			if w := ws.findWorker(name); w != nil {
				_ = ws.runner.RunCommand(w.InitScript, "start")
			}
		}
	}
	return nil
}
