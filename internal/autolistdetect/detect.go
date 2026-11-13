// Package autolistdetect discovers and drives the awgmautolist satellite.
//
// awgmautolist is a separate daemon (awgmautolist/ in the repository root,
// its own Go module). This package is the AWGM side of that contract:
//   - it reads the satellite's pid file and loopback status endpoint and
//     turns them into a Status snapshot for the UI;
//   - it enables and disables the satellite through its Entware init script,
//     persisting the user's intent in a separate state file;
//   - it proxies UI actions to the satellite's operational API.
//
// The satellite listens on loopback only (127.0.0.1:2226) and uses no Bearer
// auth: AWGM is the only door to it, so nothing secret leaves this machine
// and nothing is forwarded upstream.
//
// The Target/Candidate/HistoryEntry/Status types mirror the satellite's
// operational documents (its internal/model package, see TECHNICAL_SPEC
// sections 8 and 11). The satellite is a separate module and is never
// imported, so the wire types are duplicated here on purpose.
package autolistdetect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// satelliteAddress is the fixed loopback address of the satellite's
	// operational API. The Entware init script starts the daemon with
	// -listen 127.0.0.1:2226; there is no listen-file negotiation.
	satelliteAddress = "127.0.0.1:2226"

	// statusURL — the satellite's status endpoint on loopback.
	statusURL = "http://" + satelliteAddress + "/status"

	// pidFile is written by the init script (start-stop-daemon -m -p).
	pidFile = "/opt/var/run/awgmautolist.pid"

	// initScript is the Entware lifecycle script; enabledFile holds the
	// user's enable/disable intent as a shell assignment the script sources.
	initScript  = "/opt/etc/init.d/S99awgmautolist"
	enabledFile = "/opt/etc/awgmautolist.enabled"

	// httpTimeout — the status endpoint is local, nothing waits longer.
	httpTimeout = 5 * time.Second
	// commandHTTPTimeout — /add and /remove verify coverage, resolve the
	// outbound and apply the router configuration; on a router this is
	// legitimately slower than five seconds.
	commandHTTPTimeout = 75 * time.Second
	// initExecTimeout bounds one init start/stop regardless of the caller's
	// context; the script itself waits at most ~10s for graceful shutdown.
	initExecTimeout = 30 * time.Second
	// initWaitDelay caps how long exec.Wait blocks on output pipes held by
	// a daemonized child after the init script itself has exited.
	initWaitDelay = 5 * time.Second
	// confirmTimeout/confirmPoll bound the wait for the satellite's HTTP
	// state to actually match the requested transition.
	confirmTimeout = 8 * time.Second
	confirmPoll    = 100 * time.Millisecond

	// maxStatusBody caps the status document read from the satellite.
	maxStatusBody = 4 << 20
	// maxRequestBody caps a UI request body proxied to the satellite; the
	// satellite enforces the same limit on its side.
	maxRequestBody = 1 << 20

	// apiPrefix is the AWGM namespace the UI calls; Proxy strips it before
	// matching the whitelist. The API handler rewrites r.URL.Path into this
	// namespace before delegating.
	apiPrefix = "/api/autolist"
)

// verdictNotChecked is the evidence verdict when no check has run yet.
// Error never proves a block (TECHNICAL_SPEC section 2).
const verdictNotChecked = "not-checked"

// Target identifies an observed destination. Domain is the registered zone
// used by candidates and rules; Host is the exact SNI/hostname the probes
// talk to. Domain is empty for IP-only targets, where one confirmed blocked
// port covers the whole address.
type Target struct {
	Domain  string `json:"domain,omitempty"`
	Host    string `json:"host,omitempty"`
	IP      string `json:"ip,omitempty"`
	Network string `json:"network"`
	Port    uint16 `json:"port"`
}

// CheckEvidence is the recorded outcome of one active check.
type CheckEvidence struct {
	Verdict    string `json:"verdict"`
	Bytes      int64  `json:"bytes,omitempty"`
	DurationMS int64  `json:"durationMs,omitempty"`
	Detail     string `json:"detail,omitempty"`
	CheckedAt  string `json:"checkedAt,omitempty"`
}

// CoverageEvidence is the recorded outcome of the coverage/exclusion pass
// that runs before any active probe.
type CoverageEvidence struct {
	Covered    bool   `json:"covered,omitempty"`
	Excluded   bool   `json:"excluded,omitempty"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"durationMs,omitempty"`
}

// Candidate is one row of the satellite's candidate table with the full
// decision evidence: AF_PACKET reason, direct/WAN/tunnel verdicts with
// durations, coverage result, chosen interface/outbound, first/last
// observation times and the state (checking, waiting, added, rejected).
type Candidate struct {
	ID      string `json:"id"`
	Target  Target `json:"target"`
	Reason  string `json:"reason,omitempty"`
	State   string `json:"state"`
	FirstAt string `json:"firstAt,omitempty"`
	LastAt  string `json:"lastAt,omitempty"`

	Coverage CoverageEvidence `json:"coverage"`
	Direct   CheckEvidence    `json:"direct"`
	WAN      CheckEvidence    `json:"wan"`
	Tunnel   CheckEvidence    `json:"tunnel"`

	TargetInterface string `json:"targetInterface,omitempty"`
	TargetOutbound  string `json:"targetOutbound,omitempty"`

	CorrelationID string `json:"correlationID,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

// HistoryEntry is one recorded satellite action: when, manual or automatic,
// on which domain/IP with protocol and port, through which interface and
// outbound, why, and what the write or removal actually resulted in.
type HistoryEntry struct {
	At        string `json:"at,omitempty"`
	Domain    string `json:"domain,omitempty"`
	Host      string `json:"host,omitempty"`
	IP        string `json:"ip,omitempty"`
	Network   string `json:"network,omitempty"`
	Port      uint16 `json:"port,omitempty"`
	Action    string `json:"action,omitempty"`
	Source    string `json:"source,omitempty"`
	Interface string `json:"interface,omitempty"`
	Outbound  string `json:"outbound,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Result    string `json:"result,omitempty"`
	// Why is a legacy field of the old satellite history.
	Why string `json:"why,omitempty"`
}

// Status is the snapshot for the UI main screen: lifecycle (running/version/
// installed/enabled), capture state, observed protocols, chosen tunnel and
// DNS, last WAN verdict, check counters, rolling addition budget and the
// candidate/history tables. Absent or stopped satellite yields the empty
// status with Installed/Enabled read from the files — that is a normal
// state, not an error.
type Status struct {
	// Running — the satellite answered its status endpoint.
	Running bool   `json:"running"`
	Version string `json:"version,omitempty"`
	// Installed/Enabled distinguish a missing package, a deliberate
	// disable and a failure.
	Installed bool `json:"installed"`
	Enabled   bool `json:"enabled"`

	CaptureActive bool     `json:"captureActive,omitempty"`
	CapturePaused bool     `json:"capturePaused,omitempty"`
	Protocols     []string `json:"protocols"`

	// TargetInterface is the shared interface of tunnel probes and new
	// rules; TargetOutbound is kept for UI compatibility with older
	// satellite configs. ResolveVia is the selected sing-box DNS tag.
	TargetInterface string `json:"targetInterface,omitempty"`
	TargetOutbound  string `json:"targetOutbound,omitempty"`
	ResolveVia      string `json:"resolveVia,omitempty"`

	LastWAN CheckEvidence `json:"lastWAN"`

	// ActiveChecks/QueuedChecks — in-flight and waiting probe counts.
	ActiveChecks int `json:"activeChecks,omitempty"`
	QueuedChecks int `json:"queuedChecks,omitempty"`

	// Rolling hourly addition budget for the UI exhaustion indicator.
	AddsUsedHour int `json:"addsUsedHour,omitempty"`
	AddsLimit    int `json:"addsLimit,omitempty"`

	Candidates []Candidate    `json:"candidates"`
	History    []HistoryEntry `json:"history"`

	// LastError — why a process that should be alive did not answer.
	LastError string `json:"lastError,omitempty"`
}

// satelliteSnapshot is the operational status document the satellite serves
// on GET /status. Lifecycle fields (Installed/Enabled) live on the AWGM side
// and are deliberately absent here.
type satelliteSnapshot struct {
	Version         string         `json:"version"`
	CaptureActive   bool           `json:"captureActive"`
	CapturePaused   bool           `json:"capturePaused"`
	Protocols       []string       `json:"protocols"`
	TargetInterface string         `json:"targetInterface"`
	TargetOutbound  string         `json:"targetOutbound"`
	ResolveVia      string         `json:"resolveVia"`
	LastWAN         CheckEvidence  `json:"lastWAN"`
	ActiveChecks    int            `json:"activeChecks"`
	QueuedChecks    int            `json:"queuedChecks"`
	AddsUsedHour    int            `json:"addsUsedHour"`
	AddsLimit       int            `json:"addsLimit"`
	Candidates      []Candidate    `json:"candidates"`
	History         []HistoryEntry `json:"history"`
}

// Service detects and polls the satellite. It is safe for concurrent use
// and never waits on external processes longer than the timeouts above.
type Service struct {
	client    *http.Client
	installer *Installer

	controlMu sync.Mutex
	// Test seams: empty values fall back to the Entware conventions.
	initPath       string
	statePath      string
	pidPath        string
	statusAddr     string
	confirmTimeout time.Duration

	command func(ctx context.Context, name string, args ...string) ([]byte, error)
	confirm func(ctx context.Context, enabled bool) error
}

// ErrNotInstalled — there is no init script, so nothing to enable or disable.
var ErrNotInstalled = errors.New("awgmautolist satellite is not installed")

// NewService returns a service with production paths and timeouts.
func NewService() *Service {
	return &Service{client: &http.Client{Timeout: httpTimeout}}
}

func (s *Service) initScriptPath() string {
	if s.initPath != "" {
		return s.initPath
	}
	return initScript
}

func (s *Service) enabledStatePath() string {
	if s.statePath != "" {
		return s.statePath
	}
	return enabledFile
}

func (s *Service) pidFilePath() string {
	if s.pidPath != "" {
		return s.pidPath
	}
	return pidFile
}

// satelliteAddr returns the host:port of the satellite's loopback API.
func (s *Service) satelliteAddr() string {
	if s.statusAddr != "" {
		return s.statusAddr
	}
	return satelliteAddress
}

func (s *Service) statusEndpoint() string {
	return "http://" + s.satelliteAddr() + "/status"
}

func (s *Service) installed() bool {
	_, err := os.Stat(s.initScriptPath())
	return err == nil
}

// desiredEnabled reads the user's intent. A missing file means the package
// default: enabled. Anything other than an explicit "ENABLED=no" enables.
func (s *Service) desiredEnabled() bool {
	b, err := os.ReadFile(s.enabledStatePath())
	return err != nil || strings.TrimSpace(string(b)) != "ENABLED=no"
}

// writeEnabledState persists the intent atomically: temp file with tight
// permissions, then rename. The satellite's conf with its API key and
// arguments is never touched.
func (s *Service) writeEnabledState(state string) error {
	path := s.enabledStatePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(state), 0600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// runCommand is the bounded default command runner: context kills the init
// script, WaitDelay stops Wait from blocking forever on output pipes a
// daemonized child inherited and still holds.
func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = initWaitDelay
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), ctx.Err())
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The script itself exited; only a daemonized child holds the pipes.
		return out, nil
	}
	return out, err
}

// runInit calls the init script with a start/stop action and surfaces its
// raw output on failure.
func (s *Service) runInit(ctx context.Context, action string) error {
	run := s.command
	if run == nil {
		run = runCommand
	}
	out, err := run(ctx, s.initScriptPath(), action)
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return fmt.Errorf("init %s: %w: %s", action, err, detail)
		}
		return fmt.Errorf("init %s: %w", action, err)
	}
	return nil
}

// confirmState waits until the satellite's HTTP status actually matches the
// requested state: the pid file disappears before graceful shutdown finishes,
// and on start the HTTP server may come up after the init script returned.
func (s *Service) confirmState(ctx context.Context, enabled bool) error {
	timeout := s.confirmTimeout
	if timeout <= 0 {
		timeout = confirmTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if s.GetStatus(ctx).Running == enabled {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("satellite state after init was not confirmed")
		case <-time.After(confirmPoll):
		}
	}
}

// SetEnabled persists the user's intent atomically and runs the init script.
// The exec is bounded independently of the caller's context.
func (s *Service) SetEnabled(ctx context.Context, enabled bool) error {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if !s.installed() {
		return ErrNotInstalled
	}
	execCtx, cancel := context.WithTimeout(ctx, initExecTimeout)
	defer cancel()

	state, action := "ENABLED=no\n", "stop"
	if enabled {
		state, action = "ENABLED=yes\n", "start"
	}
	if err := s.writeEnabledState(state); err != nil {
		return err
	}
	if err := s.runInit(execCtx, action); err != nil {
		return err
	}
	confirm := s.confirm
	if confirm == nil {
		confirm = s.confirmState
	}
	return confirm(ctx, enabled)
}

// readPID returns the pid from the pid file. alive reports liveness; known
// is false only when the file cannot be interpreted at all, leaving the
// decision to the HTTP request.
func (s *Service) readPID() (pid int, alive, known bool) {
	b, err := os.ReadFile(s.pidFilePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, true
		}
		return 0, false, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		// A corrupt pid file is no process.
		return 0, false, true
	}
	return pid, processAlive(pid), true
}

// processAlive probes pid with signal 0. EPERM still proves the process
// exists (it belongs to another user); an unknown probe result claims alive
// and lets the HTTP request decide.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	switch {
	case err == nil:
		return true
	case errors.Is(err, os.ErrProcessDone), errors.Is(err, syscall.ESRCH):
		return false
	case errors.Is(err, syscall.EPERM):
		return true
	default:
		return true
	}
}

// fetchSnapshot reads and decodes the satellite's status document.
func (s *Service) fetchSnapshot(ctx context.Context) (satelliteSnapshot, error) {
	var snap satelliteSnapshot
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.statusEndpoint(), nil)
	if err != nil {
		return snap, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return snap, fmt.Errorf("satellite is not answering on %s: %w", s.statusEndpoint(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snap, fmt.Errorf("satellite %s returned HTTP %d", s.statusEndpoint(), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxStatusBody+1))
	if err != nil {
		return snap, fmt.Errorf("read satellite status: %w", err)
	}
	if len(body) > maxStatusBody {
		return snap, fmt.Errorf("satellite status exceeds %d bytes", maxStatusBody)
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		return snap, fmt.Errorf("decode satellite status: %w", err)
	}
	return snap, nil
}

// emptyStatus returns the normal status of an absent or stopped satellite:
// lifecycle flags from the files, everything else empty, no error.
func (s *Service) emptyStatus() Status {
	return Status{
		Installed:  s.installed(),
		Enabled:    s.installed() && s.desiredEnabled(),
		Protocols:  []string{},
		LastWAN:    CheckEvidence{Verdict: verdictNotChecked},
		Candidates: []Candidate{},
		History:    []HistoryEntry{},
	}
}

// GetStatus polls the satellite and assembles the snapshot for the UI.
// An HTTP answer is the proof of Running, regardless of the pid file.
// An absent or stopped satellite (including a stale pid file) is the normal
// empty status; LastError is set only when a process that should be alive
// per the pid file does not answer or answers with garbage.
func (s *Service) GetStatus(ctx context.Context) Status {
	pid, alive, known := s.readPID()
	snap, err := s.fetchSnapshot(ctx)
	if err == nil {
		// The satellite answered: that is the proof of Running, whatever
		// the pid file claims.
		st := s.emptyStatus()
		st.Running = true
		st.Version = snap.Version
		st.CaptureActive = snap.CaptureActive
		st.CapturePaused = snap.CapturePaused
		st.Protocols = nonNil(snap.Protocols)
		st.TargetInterface = snap.TargetInterface
		st.TargetOutbound = snap.TargetOutbound
		st.ResolveVia = snap.ResolveVia
		st.LastWAN = snap.LastWAN
		if st.LastWAN.Verdict == "" {
			st.LastWAN.Verdict = verdictNotChecked
		}
		st.ActiveChecks = snap.ActiveChecks
		st.QueuedChecks = snap.QueuedChecks
		st.AddsUsedHour = snap.AddsUsedHour
		st.AddsLimit = snap.AddsLimit
		st.Candidates = nonNil(snap.Candidates)
		st.History = nonNil(snap.History)
		return st
	}
	// No usable answer. Silence plus a stale or absent pid file is the
	// normal stopped state; silence with a live pid is a failure worth
	// reporting. A corrupt pid file (known=false) defers to the result
	// of the HTTP attempt as well.
	st := s.emptyStatus()
	if pid > 0 && alive && known {
		st.LastError = err.Error()
	}
	return st
}

func nonNil[T any](slice []T) []T {
	if slice == nil {
		return []T{}
	}
	return slice
}

// Proxy forwards UI calls to the satellite's operational API. It is a safe
// generic reverse proxy: exact path whitelist, bounded bodies, methods and
// headers fixed here, and upstream answers passed through raw — status
// codes, bodies and errors are never rewritten into envelopes.
type Proxy struct {
	svc *Service
}

// NewProxy returns a proxy for the satellite behind svc.
func NewProxy(svc *Service) *Proxy { return &Proxy{svc: svc} }

// allowedPaths — the satellite's operational endpoints available to the UI.
// The satellite has no /probe, /exclude or any diagnostics endpoint by
// design (TECHNICAL_SPEC sections 1 and 8): probes run on its own schedule,
// exclusions are edited in its config. /add and /remove change router rules
// and /config changes the satellite's mode; /history/clear clears only the
// journal. The API layer gates mutations with the expert guard.
var allowedPaths = map[string]bool{
	"/status":        true,
	"/config":        true,
	"/add":           true,
	"/dismiss":       true,
	"/forget":        true,
	"/restore":       true,
	"/remove":        true,
	"/history/clear": true,
}

// commandPaths need the long command timeout: they run coverage, resolve
// outbounds and apply the router configuration on the satellite side.
var commandPaths = map[string]bool{
	"/add":    true,
	"/remove": true,
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	if !allowedPaths[path] {
		http.Error(w, "unknown satellite path "+path, http.StatusNotFound)
		return
	}

	var body io.Reader
	if r.Method == http.MethodPost {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
		if err != nil {
			http.Error(w, "read request body: "+err.Error(), http.StatusBadGateway)
			return
		}
		if len(b) > maxRequestBody {
			http.Error(w, fmt.Sprintf("request body exceeds %d bytes", maxRequestBody), http.StatusRequestEntityTooLarge)
			return
		}
		body = bytes.NewReader(b)
	}

	// A fresh request carries no cookies or Authorization header on purpose:
	// the satellite's loopback API needs no Bearer auth, and AWGM is the
	// only door to it.
	target := "http://" + p.svc.satelliteAddr() + path
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := p.svc.client
	if commandPaths[path] {
		commandClient := *client
		commandClient.Timeout = commandHTTPTimeout
		client = &commandClient
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "satellite is not answering: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxStatusBody+1))
	if err != nil {
		http.Error(w, "read satellite response: "+err.Error(), http.StatusBadGateway)
		return
	}
	if len(respBody) > maxStatusBody {
		http.Error(w, fmt.Sprintf("satellite response exceeds %d bytes", maxStatusBody), http.StatusBadGateway)
		return
	}
	// AWGM's frontend transport unwraps {success,data}. Successful satellite
	// JSON is placed in that envelope; error statuses keep their raw body and
	// code, so writer/DNS details are not replaced by a generic AWGM message.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && json.Valid(respBody) {
		var data any
		if err := json.Unmarshal(respBody, &data); err != nil {
			http.Error(w, "decode satellite JSON: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}
