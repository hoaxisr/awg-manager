package xhttpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/procnet"
)

type cDaemonHealthResp struct {
	Status           string   `json:"status"`
	Engine           string   `json:"engine"`
	Version          string   `json:"version"`
	PID              int      `json:"pid"`
	Capabilities     []string `json:"capabilities"`
	ActiveSessions   int      `json:"active_sessions"`
	PreAuthSessions  int      `json:"pre_auth_sessions"`
	BufferedBytes    int      `json:"buffered_bytes"`
	PreAuthBytes     int      `json:"pre_auth_bytes"`
	ActiveFPSessions int      `json:"fp_sessions"`
}

// Server manages the mandatory C daemon process (awgm-xhttpd).
// Go never serves VLESS/XHTTP/FiniteProto traffic directly.
type Server struct {
	mu           sync.RWMutex
	cfg          Config
	pathPrefixes []string
	allowed      map[[16]byte]struct{}
	trusted      []string
	configErr    error

	srvMu        sync.Mutex
	running      bool
	cBinPath     string
	cConfPath    string
	cArgs        []string
	cEnv         []string
	cReloadToken string
	cCmd         *exec.Cmd
	cAddr        string
	cVersion     string
	cExitCh      chan error
	onExit       func()
}

// New constructs a C-backed xhttpserver supervisor.
func New(cfg Config) *Server {
	s := &Server{
		cfg:       cfg,
		cBinPath:  cfg.BinaryPath,
		cConfPath: cfg.ConfigPath,
	}
	prefixes, allowed, trusted, err := validateAndNormalizeConfig(cfg)
	if err != nil {
		s.configErr = err
		s.pathPrefixes = normalizePrefixes(cfg)
	} else {
		s.pathPrefixes = prefixes
		s.allowed = allowed
		s.trusted = trusted
	}
	return s
}

// BinaryStatus inspects the configured awgm-xhttpd binary without starting a listener.
func (s *Server) BinaryStatus() (BinaryInfo, error) {
	s.srvMu.Lock()
	binPath := s.cBinPath
	hasCustomArgs := len(s.cArgs) > 0
	s.srvMu.Unlock()
	return inspectBinaryInternal(binPath, !hasCustomArgs)
}

// Addr returns the bound TCP address of the running awgm-xhttpd daemon.
func (s *Server) Addr() string {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	return s.cAddr
}

// IsRunning reports whether the managed awgm-xhttpd child process is currently alive and ready.
func (s *Server) IsRunning() bool {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	if !s.running || s.cCmd == nil || s.cCmd.Process == nil {
		return false
	}
	return true
}

// CDaemonPID returns the PID of the managed awgm-xhttpd child daemon, or 0 if not running.
func (s *Server) CDaemonPID() int {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	if s.cCmd != nil && s.cCmd.Process != nil {
		return s.cCmd.Process.Pid
	}
	return 0
}

// CDaemonVersion returns the verified version reported by awgm-xhttpd.
func (s *Server) CDaemonVersion() string {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	return s.cVersion
}

// SetOnUnexpectedExit registers fn, run in its own goroutine when the child
// exits without Stop (crash, OOM kill) — the supervisor never restarts it itself.
func (s *Server) SetOnUnexpectedExit(fn func()) {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	s.onExit = fn
}

// Start validates configuration and binary integrity, checks port availability safely,
// spawns awgm-xhttpd, and verifies health, capabilities, PID, and socket ownership.
func (s *Server) Start() error {
	s.mu.RLock()
	cfgErr := s.configErr
	cfg := s.cfg
	prefixes := append([]string(nil), s.pathPrefixes...)
	allowed := s.allowed
	trusted := append([]string(nil), s.trusted...)
	s.mu.RUnlock()

	if cfgErr != nil {
		return cfgErr
	}

	s.srvMu.Lock()
	defer s.srvMu.Unlock()

	if s.running && s.cCmd != nil && s.cCmd.Process != nil {
		return nil
	}

	binInfo, err := inspectBinaryInternal(s.cBinPath, len(s.cArgs) == 0)
	if err != nil {
		return err
	}

	listenAddr, allocatedEphemeral, err := resolveListenAddress(cfg.ListenAddr)
	if err != nil {
		return err
	}
	if !allocatedEphemeral {
		if err := ensurePortFreeOrReapVerifiedStaleCDaemon(listenAddr); err != nil {
			return err
		}
	}

	confPath := s.effectiveConfPathLocked(listenAddr)
	if err := writeCXHTTPDConfigFileAt(confPath, cfg, listenAddr, prefixes, allowed, trusted); err != nil {
		return fmt.Errorf("write awgm-xhttpd config: %w", err)
	}

	if s.cReloadToken == "" {
		tokBytes := make([]byte, 16)
		if _, err := rand.Read(tokBytes); err != nil {
			return fmt.Errorf("generate awgm-xhttpd reload token: %w", err)
		}
		s.cReloadToken = hex.EncodeToString(tokBytes)
	}

	args := append([]string(nil), s.cArgs...)
	args = append(args, "-config", confPath)
	cmd := exec.Command(binInfo.Path, args...)
	cmdEnv := append(os.Environ(), "AWGM_XHTTPD_RELOAD_TOKEN="+s.cReloadToken)
	if len(s.cEnv) > 0 {
		cmdEnv = append(cmdEnv, s.cEnv...)
	}
	cmd.Env = cmdEnv
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start awgm-xhttpd (%s): %w", binInfo.Path, err)
	}

	expectedPID := 0
	if cmd.Process != nil {
		expectedPID = cmd.Process.Pid
	}
	exitCh := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		exitCh <- waitErr
		close(exitCh)

		s.srvMu.Lock()
		var onExit func()
		if s.cCmd == cmd {
			s.running = false
			s.cCmd = nil
			s.cAddr = ""
			onExit = s.onExit
		}
		s.srvMu.Unlock()
		if onExit != nil {
			go onExit()
		}
	}()

	httpClient := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	var health cDaemonHealthResp
	ready := false
	var lastProbeErr error
	for time.Now().Before(deadline) {
		select {
		case waitErr := <-exitCh:
			if waitErr != nil {
				return fmt.Errorf("awgm-xhttpd exited prematurely: %w", waitErr)
			}
			return fmt.Errorf("awgm-xhttpd exited prematurely before health readiness")
		default:
		}
		h, probeErr := probeCDaemonHealth(httpClient, listenAddr, expectedPID)
		if probeErr == nil {
			if ownErr := verifySocketOwnership(listenAddr, expectedPID); ownErr == nil {
				health = h
				ready = true
				break
			} else {
				lastProbeErr = ownErr
			}
		} else {
			lastProbeErr = probeErr
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !ready {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-exitCh:
		case <-time.After(1 * time.Second):
		}
		if lastProbeErr != nil {
			return fmt.Errorf("awgm-xhttpd failed readiness check on %s (pid=%d): %w", listenAddr, expectedPID, lastProbeErr)
		}
		return fmt.Errorf("awgm-xhttpd failed readiness check on %s (pid=%d)", listenAddr, expectedPID)
	}

	s.cCmd = cmd
	s.cAddr = listenAddr
	s.cVersion = health.Version
	s.cConfPath = confPath
	s.cExitCh = exitCh
	s.running = true
	return nil
}

// Stop terminates the managed awgm-xhttpd child process and removes temporary runtime config.
func (s *Server) Stop() error {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	s.stopCDaemonLocked()
	s.running = false
	return nil
}

// UpdateConfig validates new configuration and atomically reloads the running awgm-xhttpd daemon.
func (s *Server) UpdateConfig(cfg Config) error {
	prefixes, allowed, trusted, err := validateAndNormalizeConfig(cfg)
	if err != nil {
		// Fail closed if trusted proxy validation fails on a live server:
		// push an empty client set so awgm-xhttpd immediately rejects requests with 403.
		if s.IsRunning() {
			_ = s.pushFailClosedEmptyClients()
		}
		return err
	}

	s.mu.Lock()
	if s.cBinPath == "" && cfg.BinaryPath != "" {
		s.cBinPath = cfg.BinaryPath
	}
	s.cfg = cfg
	s.pathPrefixes = prefixes
	s.allowed = allowed
	s.trusted = trusted
	s.configErr = nil
	s.mu.Unlock()

	return s.reloadCDaemonConfig()
}

// Reload triggers a configuration & FakeIP DNAT generation reload on the running C daemon.
func (s *Server) Reload() error {
	return s.reloadCDaemonConfig()
}

// Live session metrics queried from awgm-xhttpd /.xhttpd/health.
func (s *Server) queryLiveHealth() (cDaemonHealthResp, bool) {
	addr := s.Addr()
	pid := s.CDaemonPID()
	if addr == "" || pid <= 0 {
		return cDaemonHealthResp{}, false
	}
	hc := &http.Client{Timeout: 250 * time.Millisecond}
	h, err := probeCDaemonHealth(hc, addr, pid)
	if err != nil {
		return cDaemonHealthResp{}, false
	}
	return h, true
}

func (s *Server) SessionCount() int {
	if h, ok := s.queryLiveHealth(); ok {
		return h.ActiveSessions
	}
	return 0
}

func (s *Server) PreAuthSessionCount() int {
	if h, ok := s.queryLiveHealth(); ok {
		return h.PreAuthSessions
	}
	return 0
}

func (s *Server) TotalBufferedBytes() int {
	if h, ok := s.queryLiveHealth(); ok {
		return h.BufferedBytes
	}
	return 0
}

func (s *Server) TotalPreAuthBufferedBytes() int {
	if h, ok := s.queryLiveHealth(); ok {
		return h.PreAuthBytes
	}
	return 0
}

func (s *Server) FPSessionCount() int {
	if h, ok := s.queryLiveHealth(); ok {
		return h.ActiveFPSessions
	}
	return 0
}

func (s *Server) effectiveConfPathLocked(listenAddr string) string {
	if strings.TrimSpace(s.cConfPath) != "" {
		return strings.TrimSpace(s.cConfPath)
	}
	if env := strings.TrimSpace(os.Getenv("AWGM_C_XHTTPD_CONF")); env != "" {
		return env
	}
	// When running multiple ephemeral servers in parallel tests (:0), avoid clobbering /tmp/awgm-xhttpd.conf.
	if _, portStr, err := net.SplitHostPort(listenAddr); err == nil && portStr != "9008" {
		return filepath.Join(os.TempDir(), fmt.Sprintf("awgm-xhttpd-%d-%s.conf", os.Getpid(), portStr))
	}
	return DefaultConfigPath
}

func writeCXHTTPDConfigFileAt(confPath string, cfg Config, listenAddr string, prefixes []string, allowed map[[16]byte]struct{}, trusted []string) error {
	if err := os.MkdirAll(filepath.Dir(confPath), 0o700); err != nil {
		return err
	}
	content := renderCXHTTPDConfig(cfg, listenAddr, prefixes, allowed, trusted)
	tmp := fmt.Sprintf("%s.tmp.%d", confPath, time.Now().UnixNano())
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, confPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Server) pushFailClosedEmptyClients() error {
	s.srvMu.Lock()
	cmd := s.cCmd
	cAddr := s.cAddr
	cReloadToken := s.cReloadToken
	confPath := s.effectiveConfPathLocked(cAddr)
	s.srvMu.Unlock()
	if cmd == nil || cmd.Process == nil || cAddr == "" {
		return nil
	}
	s.mu.RLock()
	cfg := s.cfg
	prefixes := append([]string(nil), s.pathPrefixes...)
	trusted := append([]string(nil), s.trusted...)
	s.mu.RUnlock()

	if err := writeCXHTTPDConfigFileAt(confPath, cfg, cAddr, prefixes, nil, trusted); err != nil {
		return err
	}
	return sendReloadHTTP(cAddr, cReloadToken, cmd)
}

func (s *Server) reloadCDaemonConfig() error {
	s.srvMu.Lock()
	cmd := s.cCmd
	cAddr := s.cAddr
	cReloadToken := s.cReloadToken
	confPath := s.effectiveConfPathLocked(cAddr)
	s.srvMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	s.mu.RLock()
	cfg := s.cfg
	prefixes := append([]string(nil), s.pathPrefixes...)
	allowed := s.allowed
	trusted := append([]string(nil), s.trusted...)
	s.mu.RUnlock()

	if err := writeCXHTTPDConfigFileAt(confPath, cfg, cAddr, prefixes, allowed, trusted); err != nil {
		return err
	}
	return sendReloadHTTP(cAddr, cReloadToken, cmd)
}

func sendReloadHTTP(cAddr, cReloadToken string, cmd *exec.Cmd) error {
	if cAddr != "" && cReloadToken != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+cAddr+"/.xhttpd/reload", nil)
		if err != nil {
			return fmt.Errorf("c_daemon reload request create: %w", err)
		}
		req.Header.Set("X-Awgm-Reload-Token", cReloadToken)
		hc := &http.Client{Timeout: 2 * time.Second}
		resp, err := hc.Do(req)
		if err != nil {
			return fmt.Errorf("c_daemon reload transport error: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("c_daemon reload returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("c_daemon reload endpoint unavailable (addr=%q)", cAddr)
	}
	if err := cmd.Process.Signal(syscall.Signal(0x1)); err != nil {
		return err
	}
	return nil
}

func (s *Server) stopCDaemonLocked() {
	cmd := s.cCmd
	exitCh := s.cExitCh
	confPath := s.cConfPath
	s.cCmd = nil
	s.cAddr = ""
	s.cExitCh = nil
	if cmd != nil && cmd.Process != nil {
		if runtime.GOOS != "windows" {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			if exitCh != nil {
				select {
				case <-exitCh:
					goto cleanup
				case <-time.After(400 * time.Millisecond):
				}
			}
		}
		_ = cmd.Process.Kill()
		if exitCh != nil {
			select {
			case <-exitCh:
			case <-time.After(2 * time.Second):
			}
		}
	}
cleanup:
	// Конфиг несёт UUID клиентов и FP-секреты — после остановки ему незачем жить.
	if confPath == DefaultConfigPath || (confPath != "" && strings.HasPrefix(filepath.Base(confPath), fmt.Sprintf("awgm-xhttpd-%d-", os.Getpid()))) {
		_ = os.Remove(confPath)
	}
}

func resolveListenAddress(raw string) (addr string, allocated bool, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "127.0.0.1:9008", false, nil
	}
	host, portStr, err := net.SplitHostPort(trimmed)
	if err != nil {
		return "", false, fmt.Errorf("invalid listen address %q: %w", raw, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return "", false, fmt.Errorf("invalid listen port in %q", raw)
	}
	if port == 0 {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return "", false, fmt.Errorf("allocate free port on %s: %w", host, err)
		}
		resolved := ln.Addr().String()
		_ = ln.Close()
		return resolved, true, nil
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), false, nil
}

func probeCDaemonHealth(client *http.Client, addr string, expectedPID int) (cDaemonHealthResp, error) {
	var h cDaemonHealthResp
	resp, err := client.Get("http://" + addr + "/.xhttpd/health")
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("health status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return h, fmt.Errorf("decode health json: %w", err)
	}
	if h.Status != "ok" || h.Engine != ExpectedEngine {
		return h, fmt.Errorf("unexpected health status=%q engine=%q", h.Status, h.Engine)
	}
	if h.Version != ExpectedVersion {
		return h, fmt.Errorf("unexpected health version=%q (want %q)", h.Version, ExpectedVersion)
	}
	if expectedPID > 0 && h.PID != expectedPID {
		return h, fmt.Errorf("health PID mismatch: got %d, want %d", h.PID, expectedPID)
	}
	for _, wantCap := range RequiredCapabilities {
		found := false
		for _, gotCap := range h.Capabilities {
			if gotCap == wantCap {
				found = true
				break
			}
		}
		if !found {
			return h, fmt.Errorf("missing required capability %q in %v", wantCap, h.Capabilities)
		}
	}
	if expectedPID > 0 && !isProcessAlive(expectedPID) {
		return h, fmt.Errorf("process %d is not alive", expectedPID)
	}
	return h, nil
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "linux" {
		st, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
		return err == nil && st.IsDir()
	}
	proc, err := os.FindProcess(pid)
	return err == nil && proc != nil
}

func verifySocketOwnership(listenAddr string, expectedPID int) error {
	if runtime.GOOS != "linux" || expectedPID <= 0 {
		return nil
	}
	host, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}
	lookup, err := procnet.FindListeningProcess("/proc", host, port)
	if err != nil {
		return fmt.Errorf("verify socket owner on %s: %w", listenAddr, err)
	}
	if !lookup.SocketFound {
		return fmt.Errorf("listening socket %s not found in /proc/net/tcp", listenAddr)
	}
	if lookup.PID != expectedPID {
		return fmt.Errorf("listening socket %s owned by PID %d, want %d", listenAddr, lookup.PID, expectedPID)
	}
	return nil
}

func isVerifiedCDaemonProcessLinux(pid int) bool {
	if pid <= 1 || pid == os.Getpid() {
		return false
	}
	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		base := strings.TrimSuffix(filepath.Base(exe), " (deleted)")
		if base == "awgm-xhttpd" {
			return true
		}
	}
	if cmdBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil && len(cmdBytes) > 0 {
		arg0 := strings.SplitN(string(cmdBytes), "\x00", 2)[0]
		if filepath.Base(arg0) == "awgm-xhttpd" {
			return true
		}
	}
	return false
}

// ensurePortFreeOrReapVerifiedStaleCDaemon checks whether listenAddr is free WITHOUT connecting to or
// disrupting any foreign service. On Linux, only a verified stale awgm-xhttpd orphan may be reaped.
func ensurePortFreeOrReapVerifiedStaleCDaemon(listenAddr string) error {
	host, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}

	if runtime.GOOS == "linux" {
		lookup, lerr := procnet.FindListeningProcess("/proc", host, port)
		if lerr == nil && lookup.SocketFound {
			if isVerifiedCDaemonProcessLinux(lookup.PID) {
				if proc, perr := os.FindProcess(lookup.PID); perr == nil && proc != nil {
					_ = proc.Signal(syscall.SIGTERM)
				}
				deadline := time.Now().Add(1 * time.Second)
				for time.Now().Before(deadline) {
					lk, e := procnet.FindListeningProcess("/proc", host, port)
					if e == nil && !lk.SocketFound {
						return nil
					}
					time.Sleep(25 * time.Millisecond)
				}
				if proc, perr := os.FindProcess(lookup.PID); perr == nil && proc != nil {
					_ = proc.Kill()
				}
				deadline2 := time.Now().Add(500 * time.Millisecond)
				for time.Now().Before(deadline2) {
					lk, e := procnet.FindListeningProcess("/proc", host, port)
					if e == nil && !lk.SocketFound {
						return nil
					}
					time.Sleep(25 * time.Millisecond)
				}
			}
			return fmt.Errorf("%w: %s (pid=%d)", ErrPortConflict, listenAddr, lookup.PID)
		}
		if lerr != nil && lookup.SocketFound {
			return fmt.Errorf("%w: %s (%v)", ErrPortConflict, listenAddr, lerr)
		}
	}

	// Non-disruptive local bind check (never connects to or sends bytes to an existing listener).
	ln, berr := net.Listen("tcp", listenAddr)
	if berr != nil {
		return fmt.Errorf("%w: %s: %v", ErrPortConflict, listenAddr, berr)
	}
	_ = ln.Close()
	return nil
}
