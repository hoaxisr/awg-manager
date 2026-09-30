package aiassistant

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type EmbeddedStatus struct {
	Available       bool   `json:"available"`
	Running         bool   `json:"running"`
	Managed         bool   `json:"managed"`
	PID             int    `json:"pid,omitempty"`
	Port            int    `json:"port"`
	BinaryExists    bool   `json:"binaryExists"`
	BinaryPath      string `json:"binaryPath,omitempty"`
	ModelExists     bool   `json:"modelExists"`
	ModelPath       string `json:"modelPath,omitempty"`
	MemAvailableMB  int    `json:"memAvailableMB"`
	LastActive      string `json:"lastActive,omitempty"`
	AutoStopMinutes int    `json:"autoStopMinutes"`
	Error           string `json:"error,omitempty"`
}

type EmbeddedManager struct {
	mu         sync.Mutex
	config     *ConfigStore
	cmd        *exec.Cmd
	lastActive time.Time
	stopTimer  *time.Timer
	httpClient *http.Client
}

func NewEmbeddedManager(config *ConfigStore) *EmbeddedManager {
	return &EmbeddedManager{
		config:     config,
		httpClient: &http.Client{Timeout: 750 * time.Millisecond},
	}
}

// GetMemAvailableMB reads /proc/meminfo and returns available memory in megabytes.
func GetMemAvailableMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.Atoi(fields[1])
				return kb / 1024
			}
		}
	}
	return 0
}

// FindDefaultBinary looks for llama-server in standard router paths.
func FindDefaultBinary() string {
	candidates := []string{
		"/opt/bin/llama-server",
		"/opt/usr/bin/llama-server",
		"/opt/etc/awg-manager/ai/llama-server",
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// FindDefaultModel looks for .gguf models in standard router storage paths.
func FindDefaultModel() string {
	candidates := []string{
		"/opt/storage/ai",
		"/opt/etc/awg-manager/ai",
		"/opt/var/ai",
	}
	for _, dir := range candidates {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".gguf") {
				return filepath.Join(dir, entry.Name())
			}
		}
	}
	return ""
}

// Status returns the current runtime status of the embedded LLM engine.
func (m *EmbeddedManager) Status() EmbeddedStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg := m.config.Get().LocalEngine
	binaryPath := cfg.BinaryPath
	if binaryPath == "" {
		binaryPath = FindDefaultBinary()
	}
	modelPath := cfg.ModelPath
	if modelPath == "" {
		modelPath = FindDefaultModel()
	}

	binExists := false
	if binaryPath != "" {
		if _, err := os.Stat(binaryPath); err == nil {
			binExists = true
		}
	}

	modelExists := false
	if modelPath != "" {
		if _, err := os.Stat(modelPath); err == nil {
			modelExists = true
		}
	}

	port := cfg.Port
	if port <= 0 {
		port = 11435
	}

	managed := m.cmd != nil && m.cmd.Process != nil
	running := managed || m.serverReady(port)
	pid := 0
	if managed {
		pid = m.cmd.Process.Pid
	}

	memAvailable := GetMemAvailableMB()

	return EmbeddedStatus{
		Available:       binExists && modelExists,
		Running:         running,
		Managed:         managed,
		PID:             pid,
		Port:            port,
		BinaryExists:    binExists,
		BinaryPath:      binaryPath,
		ModelExists:     modelExists,
		ModelPath:       modelPath,
		MemAvailableMB:  memAvailable,
		LastActive:      m.lastActive.Format(time.RFC3339),
		AutoStopMinutes: cfg.AutoStopMinutes,
	}
}

// EnsureRunning starts llama-server on-demand if not already running.
func (m *EmbeddedManager) EnsureRunning(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg := m.config.Get().LocalEngine
	port := cfg.Port
	if port <= 0 {
		port = 11435
	}

	// A server may survive an AWG Manager restart or be supervised externally.
	// Reuse a healthy loopback instance instead of spawning a second process on
	// the same port. Such an instance is reported as running but unmanaged.
	if m.serverReady(port) {
		m.touch()
		return nil
	}

	// If our child is still loading, wait for that child instead of starting a
	// duplicate. This path also covers concurrent readiness checks.
	if m.cmd != nil && m.cmd.Process != nil {
		if err := m.waitReady(ctx, port, 45*time.Second); err != nil {
			return err
		}
		m.touch()
		return nil
	}

	binaryPath := cfg.BinaryPath
	if binaryPath == "" {
		binaryPath = FindDefaultBinary()
	}
	if binaryPath == "" {
		return errors.New("llama-server binary not found on router")
	}

	modelPath := cfg.ModelPath
	if modelPath == "" {
		modelPath = FindDefaultModel()
	}
	if modelPath == "" {
		return errors.New("GGUF model not found on router")
	}

	// Safety check: ensure at least 450 MB RAM available to protect router network services
	mem := GetMemAvailableMB()
	if mem > 0 && mem < 450 {
		return fmt.Errorf("insufficient memory: %d MB available (minimum 450 MB required)", mem)
	}

	ctxSize := cfg.ContextSize
	if ctxSize <= 0 {
		ctxSize = 1536
	}
	threads := cfg.Threads
	if threads <= 0 {
		threads = 2
	}

	args := []string{
		"-m", modelPath,
		"-c", strconv.Itoa(ctxSize),
		"-t", strconv.Itoa(threads),
		"-b", "64",
		"--parallel", "1",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
	}

	var cmd *exec.Cmd
	if _, err := exec.LookPath("taskset"); err == nil {
		cmdArgs := append([]string{"-c", "1,2", binaryPath}, args...)
		cmd = exec.Command("taskset", cmdArgs...)
	} else {
		cmd = exec.Command(binaryPath, args...)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start llama-server: %w", err)
	}

	m.cmd = cmd
	m.touch()

	go func() {
		_ = cmd.Wait()
		m.mu.Lock()
		if m.cmd == cmd {
			m.cmd = nil
		}
		m.mu.Unlock()
	}()

	if err := m.waitReady(ctx, port, 45*time.Second); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		if m.cmd == cmd {
			m.cmd = nil
		}
		return err
	}
	return nil
}

func (m *EmbeddedManager) serverReady(port int) bool {
	readyURL := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	client := m.httpClient
	if client == nil {
		client = &http.Client{Timeout: 750 * time.Millisecond}
	}
	resp, err := client.Get(readyURL)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (m *EmbeddedManager) waitReady(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.serverReady(port) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return errors.New("llama-server startup timed out")
}

// Stop terminates the running llama-server process to free router memory.
func (m *EmbeddedManager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Signal(syscall.SIGTERM)
		m.cmd = nil
	}
	if m.stopTimer != nil {
		m.stopTimer.Stop()
		m.stopTimer = nil
	}
	return nil
}

func (m *EmbeddedManager) touch() {
	m.lastActive = time.Now()
	cfg := m.config.Get().LocalEngine
	autoStop := cfg.AutoStopMinutes
	if autoStop <= 0 {
		autoStop = 10
	}
	if m.stopTimer != nil {
		m.stopTimer.Stop()
	}
	m.stopTimer = time.AfterFunc(time.Duration(autoStop)*time.Minute, func() {
		_ = m.Stop()
	})
}
