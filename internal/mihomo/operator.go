package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/procnet"
	"gopkg.in/yaml.v3"
)

// Operator manages the Mihomo process and implements proxyengine.Engine
type Operator struct {
	mu               sync.Mutex
	binaryPath       string
	configDir        string
	logFilePath      string
	maxLogSize       int64
	gracefulTimeout  time.Duration
	controllerAddr   string
	controllerSecret string
	cmd              *exec.Cmd
	stopping         *exec.Cmd
	done             chan struct{}
	running          bool
	pid              int
	generation       uint64
	lastError        string
	commandFn        func(string, ...string) *exec.Cmd
	readyFn          func(context.Context) error
	afterWait        func()
	cleanupStaleFn   func(string, string) error
	onExit           func(uint64)
	logFn            func(level, action, message string)
	socketCheckFn    func(addr string, port int, expectedPID int) error
	procDir          string
	signalGracefulFn func(*os.Process) error
	killFn           func(*os.Process) error
	reapTimeout      time.Duration
}

func NewOperator(binaryPath, configDir string) *Operator {
	return &Operator{
		binaryPath:      binaryPath,
		configDir:       configDir,
		logFilePath:     "/tmp/mihomo.log",
		maxLogSize:      512 * 1024,
		gracefulTimeout: 3 * time.Second,
		commandFn:       exec.Command,
		cleanupStaleFn:  cleanupStaleManagedProcesses,
	}
}

// SetProcessSeam allows injecting deterministic process signaling, killing, and reap timeout for testing.
func (o *Operator) SetProcessSeam(sigFn func(*os.Process) error, killFn func(*os.Process) error, reapTimeout time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signalGracefulFn = sigFn
	o.killFn = killFn
	o.reapTimeout = reapTimeout
}

// SetLogPath configures a custom log file path.
func (o *Operator) SetLogPath(path string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.logFilePath = path
}

// SetMaxLogSize sets the maximum size in bytes before log file rotation.
func (o *Operator) SetMaxLogSize(size int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.maxLogSize = size
}

// SetGracefulTimeout sets the time to wait for SIGTERM before escalating to SIGKILL.
func (o *Operator) SetGracefulTimeout(d time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.gracefulTimeout = d
}

// SetController sets an explicit external-controller address and secret override.
func (o *Operator) SetController(addr, secret string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.controllerAddr = addr
	o.controllerSecret = secret
}

func (o *Operator) getControllerConfig() (addr string, secret string, host string, port int) {
	o.mu.Lock()
	addr = o.controllerAddr
	secret = o.controllerSecret
	configDir := o.configDir
	o.mu.Unlock()

	if addr == "" {
		cfgPath := filepath.Join(configDir, "config.yaml")
		if data, err := os.ReadFile(cfgPath); err == nil {
			var probe struct {
				ExternalCtl string `yaml:"external-controller"`
				Secret      string `yaml:"secret"`
			}
			if err := yaml.Unmarshal(data, &probe); err == nil {
				if probe.ExternalCtl != "" {
					addr = probe.ExternalCtl
				}
				if secret == "" && probe.Secret != "" {
					secret = probe.Secret
				}
			}
		}
	}

	if addr == "" {
		addr = "127.0.0.1:9090"
	}

	h, pStr, err := net.SplitHostPort(addr)
	if err != nil {
		host = "127.0.0.1"
		port = 9090
	} else {
		p, _ := strconv.Atoi(pStr)
		if p <= 0 {
			p = 9090
		}
		port = p
		if h == "" || h == "0.0.0.0" || h == "::" {
			h = "127.0.0.1"
		}
		host = h
	}

	return addr, secret, host, port
}

// ControllerTarget returns the configured controller address (host:port) and bearer secret.
func (o *Operator) ControllerTarget() (string, string) {
	addr, secret, host, port := o.getControllerConfig()
	if addr == "" {
		addr = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return addr, secret
}

// MihomoProviderRefresher triggers a runtime update for an external proxy provider.
type MihomoProviderRefresher interface {
	RefreshProvider(ctx context.Context, providerName string) error
}

// RefreshProvider requests the local Mihomo controller to refresh a named proxy provider.
func (o *Operator) RefreshProvider(ctx context.Context, providerName string) error {
	if strings.TrimSpace(providerName) == "" {
		return errors.New("provider name is required")
	}
	path := "/providers/proxies/" + url.PathEscape(providerName)
	req, err := o.newControllerRequest(ctx, http.MethodPut, path, nil)
	if err != nil {
		return fmt.Errorf("create provider refresh request: %w", err)
	}

	_, secret, _, _ := o.getControllerConfig()
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		if secret != "" && strings.Contains(msg, secret) {
			msg = strings.ReplaceAll(msg, secret, "[REDACTED]")
		}
		return fmt.Errorf("mihomo provider refresh request failed: %s", msg)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mihomo provider refresh returned %s", resp.Status)
	}
	return nil
}

func (o *Operator) newControllerRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	_, secret, host, port := o.getControllerConfig()
	baseURL := fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(port)))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	return req, nil
}

// SetLogger sets an optional logging callback for early startup and process errors.
func (o *Operator) SetLogger(fn func(level, action, message string)) {
	o.mu.Lock()
	o.logFn = fn
	o.mu.Unlock()
}

func (o *Operator) log(level, action, message string) {
	o.mu.Lock()
	fn := o.logFn
	o.mu.Unlock()
	if fn != nil {
		fn(level, action, message)
	}
}

// SetOnUnexpectedExit registers a fail-safe invoked after a non-intentional
// process exit. Production uses it to withdraw NDMS ProxyN exports immediately
// so they never keep routing into dead loopback listeners.
func (o *Operator) SetOnUnexpectedExit(fn func(generation uint64)) {
	o.mu.Lock()
	o.onExit = fn
	o.mu.Unlock()
}

// CurrentGeneration identifies the most recently started process. Exit hooks
// use it together with the bridge-runtime mutex to ignore a delayed callback
// from an older process after a successful restart.
func (o *Operator) CurrentGeneration() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.generation
}

func (o *Operator) configHasTunEnabled() bool {
	data, err := os.ReadFile(filepath.Join(o.configDir, "config.yaml"))
	if err != nil {
		return false
	}
	var probe struct {
		Tun struct {
			Enable bool `yaml:"enable"`
		} `yaml:"tun"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.Tun.Enable
}

// ResetCache explicitly wipes cache.db if needed for manual/administrative reset.
// Normal reload and start preserve cache.db to retain user selector choices and url-test latencies.
func (o *Operator) ResetCache() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	cachePath := filepath.Join(o.configDir, "cache.db")
	if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReloadConfig reloads Mihomo configuration via controller PUT /configs.
func (o *Operator) ReloadConfig(ctx context.Context, configPath string, force bool) error {
	if running, _ := o.IsRunning(); !running {
		return errors.New("cannot reload configuration: mihomo is not running")
	}

	payload := map[string]string{
		"path": configPath,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return o.recordError(err)
	}

	path := "/configs"
	if force {
		path = "/configs?force=true"
	}

	req, err := o.newControllerRequest(ctx, http.MethodPut, path, bytes.NewReader(body))
	if err != nil {
		return o.recordError(err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		_, secret, _, _ := o.getControllerConfig()
		msg := err.Error()
		if secret != "" && strings.Contains(msg, secret) {
			msg = strings.ReplaceAll(msg, secret, "[REDACTED]")
		}
		return o.recordError(fmt.Errorf("mihomo reload request failed: %s", msg))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return o.recordError(fmt.Errorf("mihomo reload failed with status %s: %s", resp.Status, strings.TrimSpace(string(respBody))))
	}
	o.clearError()
	go o.warmupGroups()
	return nil
}

func (o *Operator) Reload() error {
	if running, _ := o.IsRunning(); !running {
		return o.Start()
	}

	// Mihomo cannot dynamically bind or attach a Linux TUN device via hot reload
	// (PUT /configs). When TUN is enabled in config.yaml, Mihomo must restart cleanly.
	if o.configHasTunEnabled() {
		if err := o.Stop(); err != nil {
			return o.recordError(fmt.Errorf("mihomo stop before tun reload: %w", err))
		}
		return o.Start()
	}

	reqCtx, reqCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer reqCancel()
	return o.ReloadConfig(reqCtx, filepath.Join(o.configDir, "config.yaml"), true)
}

func (o *Operator) IsRunning() (bool, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// A process selected for shutdown must not be treated as a valid reload
	// target. Reload falls through to Start, which waits for done before
	// launching the replacement instead of PUTing into a dying controller.
	if o.running && o.stopping == nil {
		return true, o.pid
	}
	return false, 0
}

func (o *Operator) Start() error {
	stopDeadline := time.Now().Add(5 * time.Second)
	for {
		o.mu.Lock()
		if o.stopping == nil {
			break
		}
		done := o.done
		o.mu.Unlock()
		if done == nil {
			return o.recordError(fmt.Errorf("mihomo startup blocked by incomplete shutdown"))
		}
		remaining := time.Until(stopDeadline)
		if remaining <= 0 {
			return o.recordError(fmt.Errorf("mihomo startup timed out waiting for previous process shutdown"))
		}
		timer := time.NewTimer(remaining)
		select {
		case <-done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			return o.recordError(fmt.Errorf("mihomo startup timed out waiting for previous process shutdown"))
		}
	}

	if o.running {
		o.mu.Unlock()
		return nil // already running
	}
	if o.cleanupStaleFn != nil {
		if err := o.cleanupStaleFn(o.binaryPath, o.configDir); err != nil {
			o.lastError = fmt.Sprintf("cleanup stale Mihomo process: %v", err)
			o.mu.Unlock()
			return fmt.Errorf("cleanup stale Mihomo process: %w", err)
		}
	}

	cmd := o.commandFn(o.resolveBinary(), "-d", o.configDir)
	stdout := NewBoundedRingBuffer(64 * 1024)
	stderr := NewBoundedRingBuffer(64 * 1024)

	logFilePath := o.logFilePath
	if logFilePath == "" {
		logFilePath = "/tmp/mihomo.log"
	}
	maxLogSize := o.maxLogSize
	if maxLogSize <= 0 {
		maxLogSize = 512 * 1024
	}

	logWriter, logErr := NewRotatingLogWriter(logFilePath, maxLogSize)
	if logErr == nil {
		cmd.Stdout = io.MultiWriter(stdout, logWriter)
		cmd.Stderr = io.MultiWriter(stderr, logWriter)
	} else {
		cmd.Stdout = stdout
		cmd.Stderr = stderr
	}

	if err := cmd.Start(); err != nil {
		if logWriter != nil {
			_ = logWriter.Close()
		}
		o.lastError = err.Error()
		o.mu.Unlock()
		return err
	}
	o.cmd = cmd
	o.running = true
	o.pid = cmd.Process.Pid
	done := make(chan struct{})
	o.done = done
	o.generation++
	generation := o.generation
	o.lastError = ""

	readyFn := o.readyFn
	o.mu.Unlock()
	go o.wait(cmd, generation, stdout, stderr, logWriter, done)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if readyFn == nil {
		readyFn = o.waitForController
	}
	if err := readyFn(ctx); err != nil {
		_ = o.Stop()
		return o.recordError(fmt.Errorf("mihomo startup failed: %w", err))
	}
	// A stale controller could answer while the just-started process is about
	// to fail on a busy port. Require a short stable window before success.
	select {
	case <-ctx.Done():
		_ = o.Stop()
		return o.recordError(fmt.Errorf("mihomo startup failed: %w", ctx.Err()))
	case <-time.After(200 * time.Millisecond):
	}
	if running, _ := o.IsRunning(); !running {
		detail := o.LastError()
		if detail == "" {
			detail = "process exited during startup"
		}
		return o.recordError(fmt.Errorf("mihomo startup failed: %s", detail))
	}
	o.clearError()
	go o.warmupGroups()
	return nil
}

// SetSocketCheckFn injects an address/port/PID check callback for testing or customization.
func (o *Operator) SetSocketCheckFn(fn func(addr string, port int, expectedPID int) error) {
	o.mu.Lock()
	o.socketCheckFn = fn
	o.mu.Unlock()
}

// SetProcDir overrides the procfs root directory (used for testing with fake /proc).
func (o *Operator) SetProcDir(dir string) {
	o.mu.Lock()
	o.procDir = dir
	o.mu.Unlock()
}

func (o *Operator) verifyControllerSocket(expectedPID int) error {
	o.mu.Lock()
	fn := o.socketCheckFn
	procDir := o.procDir
	o.mu.Unlock()

	_, _, host, port := o.getControllerConfig()

	if fn != nil {
		return fn(host, port, expectedPID)
	}

	if procDir == "" {
		procDir = "/proc"
	}
	lookup, err := procnet.FindListeningProcess(procDir, host, port)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // On platforms/environments without proc tables, proceed
		}
		return err
	}
	endpointStr := net.JoinHostPort(host, strconv.Itoa(port))
	if !lookup.SocketFound {
		return fmt.Errorf("controller socket %s not yet open", endpointStr)
	}
	if lookup.PID != 0 && lookup.PID != expectedPID {
		return fmt.Errorf("%w: controller socket %s owned by pid %d, expected %d", ErrControllerSocketMismatch, endpointStr, lookup.PID, expectedPID)
	}
	return nil
}

func (o *Operator) waitForController(ctx context.Context) error {
	client := &http.Client{Timeout: 300 * time.Millisecond}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		running, pid := o.IsRunning()
		if !running {
			detail := o.LastError()
			if detail == "" {
				detail = "process exited before controller became ready"
			}
			return fmt.Errorf("%s", detail)
		}

		// Pre-HTTP socket ownership proof: verify controller socket belongs to this process before HTTP /version
		if err := o.verifyControllerSocket(pid); err != nil {
			if errors.Is(err, ErrControllerSocketMismatch) {
				return err
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("controller readiness (socket): %w", ctx.Err())
			case <-ticker.C:
				continue
			}
		}

		req, err := o.newControllerRequest(ctx, http.MethodGet, "/version", nil)
		if err == nil {
			if resp, requestErr := client.Do(req); requestErr == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("controller readiness: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (o *Operator) wait(cmd *exec.Cmd, generation uint64, stdout, stderr *BoundedRingBuffer, logCloser io.Closer, done chan struct{}) {
	defer func() {
		if logCloser != nil {
			_ = logCloser.Close()
		}
	}()

	err := cmd.Wait()
	if o.afterWait != nil {
		o.afterWait()
	}

	o.mu.Lock()
	intentionalStop := o.stopping == cmd
	if intentionalStop {
		o.stopping = nil
	}
	if o.cmd == cmd {
		o.cmd = nil
		o.running = false
		o.pid = 0
	}
	if o.done == done {
		o.done = nil
	}
	if intentionalStop {
		o.mu.Unlock()
		close(done)
		return
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			o.lastError = fmt.Sprintf("mihomo exited: %v: %s", err, detail)
		} else {
			o.lastError = fmt.Sprintf("mihomo exited: %v", err)
		}
	} else {
		o.lastError = "mihomo exited unexpectedly"
	}
	onExit := o.onExit
	o.mu.Unlock()
	close(done)
	if onExit != nil {
		go onExit(generation)
	}
}

func (o *Operator) ClearManualStop() error {
	return nil
}

func (o *Operator) ValidateConfigDir(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, o.binaryPath, "-d", o.configDir, "-t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mihomo validation failed: %s (%w)", string(out), err)
	}
	return nil
}

func (o *Operator) ConfigDir() string {
	return o.configDir
}

func (o *Operator) resolveBinary() string {
	if o.binaryPath == "ignored" {
		return "ignored"
	}
	if _, err := os.Stat(o.binaryPath); err == nil {
		return o.binaryPath
	}
	if _, err := os.Stat("/opt/bin/mihomo"); err == nil {
		return "/opt/bin/mihomo"
	}
	if lp, err := exec.LookPath("mihomo"); err == nil {
		return lp
	}
	return o.binaryPath
}

func (o *Operator) Binary() string {
	return o.resolveBinary()
}

func (o *Operator) LastError() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lastError
}

func (o *Operator) CrashStats() (int, string, time.Time) {
	return 0, "", time.Time{}
}

func (o *Operator) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return o.StopAndWait(ctx)
}

// StopAndWait gracefully stops Mihomo (SIGTERM with configurable timeout on Unix,
// kill on Windows) and escalates to SIGKILL if the process does not terminate.
// It guarantees the process is fully reaped before returning.
func (o *Operator) StopAndWait(ctx context.Context) error {
	o.mu.Lock()
	if !o.running || o.cmd == nil || o.cmd.Process == nil {
		done := o.done
		if done == nil && o.cleanupStaleFn != nil {
			if err := o.cleanupStaleFn(o.binaryPath, o.configDir); err != nil {
				o.lastError = fmt.Sprintf("cleanup stale Mihomo process during stop: %v", err)
				o.mu.Unlock()
				return fmt.Errorf("cleanup stale Mihomo process during stop: %w", err)
			}
		}
		reapTimeout := o.reapTimeout
		if reapTimeout <= 0 {
			reapTimeout = 5 * time.Second
		}
		o.mu.Unlock()
		if done != nil {
			reapTimer := time.NewTimer(reapTimeout)
			defer reapTimer.Stop()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				select {
				case <-done:
					return fmt.Errorf("wait for Mihomo shutdown: %w", ctx.Err())
				case <-reapTimer.C:
					return fmt.Errorf("%w: process failed to exit within %v after context cancel (%v)", ErrProcessNotReaped, reapTimeout, ctx.Err())
				}
			case <-reapTimer.C:
				return fmt.Errorf("%w: process failed to exit within %v", ErrProcessNotReaped, reapTimeout)
			}
		}
		return nil
	}

	cmd := o.cmd
	done := o.done
	if done == nil {
		o.mu.Unlock()
		return fmt.Errorf("%w: operator marked running but missing termination channel", ErrProcessNotReaped)
	}

	gracefulTimeout := o.gracefulTimeout
	if gracefulTimeout <= 0 {
		gracefulTimeout = 3 * time.Second
	}
	reapTimeout := o.reapTimeout
	if reapTimeout <= 0 {
		reapTimeout = 5 * time.Second
	}
	sigFn := o.signalGracefulFn
	if sigFn == nil {
		sigFn = sendGracefulStop
	}
	killFn := o.killFn
	if killFn == nil {
		killFn = func(p *os.Process) error { return p.Kill() }
	}

	var sigErr error
	if o.stopping == nil {
		o.stopping = cmd
		sigErr = sigFn(cmd.Process)
		if sigErr != nil && !errors.Is(sigErr, os.ErrProcessDone) {
			o.log("warn", "stop", fmt.Sprintf("graceful stop failed: %v; falling back to kill", sigErr))
			_ = killFn(cmd.Process)
		}
	}
	o.mu.Unlock()

	// Wait for graceful exit up to gracefulTimeout or ctx.Done()
	graceTimer := time.NewTimer(gracefulTimeout)
	defer graceTimer.Stop()

	needKill := false
	var initialErr error

	select {
	case <-done:
		return nil
	case <-graceTimer.C:
		// Process did not exit within graceful timeout: escalate to SIGKILL
		o.log("warn", "stop", fmt.Sprintf("process %d did not terminate within %v; sending SIGKILL", cmd.Process.Pid, gracefulTimeout))
		needKill = true
	case <-ctx.Done():
		initialErr = ctx.Err()
		needKill = true
	}

	if needKill {
		if err := killFn(cmd.Process); err != nil && !errors.Is(err, os.ErrProcessDone) {
			o.recordError(fmt.Errorf("escalate to SIGKILL: %w", err))
		}
	}

	// Always wait for reap using an internal bounded timeout (reapTimeout), NOT the already-canceled ctx
	reapTimer := time.NewTimer(reapTimeout)
	defer reapTimer.Stop()

	select {
	case <-done:
		if initialErr != nil {
			return fmt.Errorf("wait for Mihomo shutdown: %w", initialErr)
		}
		return nil
	case <-reapTimer.C:
		pid := 0
		if cmd.Process != nil {
			pid = cmd.Process.Pid
		}
		err := fmt.Errorf("%w: pid %d failed to exit within %v after SIGKILL", ErrProcessNotReaped, pid, reapTimeout)
		_ = o.recordError(err)
		return err
	}
}

func (o *Operator) recordError(err error) error {
	o.mu.Lock()
	o.lastError = err.Error()
	o.mu.Unlock()
	o.log("error", "runtime", err.Error())
	return err
}

func (o *Operator) clearError() {
	o.mu.Lock()
	o.lastError = ""
	o.mu.Unlock()
}

func (o *Operator) warmupGroups() {
	time.Sleep(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	req, err := o.newControllerRequest(ctx, http.MethodGet, "/proxies", nil)
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var data struct {
		Proxies map[string]struct {
			Type    string `json:"type"`
			TestURL string `json:"testUrl"`
		} `json:"proxies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return
	}
	for name, p := range data.Proxies {
		pType := strings.ToLower(p.Type)
		if pType == "fallback" || pType == "urltest" {
			testURL := p.TestURL
			if testURL == "" {
				testURL = "https://www.gstatic.com/generate_204"
			}
			endpoint := fmt.Sprintf("/group/%s/delay?url=%s&timeout=5000", url.PathEscape(name), url.QueryEscape(testURL))
			delayReq, err := o.newControllerRequest(ctx, http.MethodGet, endpoint, nil)
			if err == nil {
				r, err := client.Do(delayReq)
				if err == nil {
					_ = r.Body.Close()
				}
			}
		}
	}
}
