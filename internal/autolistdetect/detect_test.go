package autolistdetect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	s := NewService()
	s.initPath = filepath.Join(dir, "S99awgmautolist")
	s.statePath = filepath.Join(dir, "awgmautolist.enabled")
	s.pidPath = filepath.Join(dir, "awgmautolist.pid")
	s.confirm = func(context.Context, bool) error { return nil }
	return s, dir
}

// Absent satellite: no init script, no pid file, nothing answers.
// Normal empty status, nothing reported as an error.
func TestGetStatusAbsentSatelliteIsEmpty(t *testing.T) {
	s, _ := testService(t)
	st := s.GetStatus(context.Background())
	if st.Installed || st.Enabled || st.Running {
		t.Fatalf("absent satellite reported as present: %+v", st)
	}
	if st.LastError != "" {
		t.Fatalf("absence is not an error: %q", st.LastError)
	}
	if st.Candidates == nil || st.History == nil || st.Protocols == nil {
		t.Fatalf("tables must be non-nil for the UI: %+v", st)
	}
	if len(st.Candidates) != 0 || len(st.History) != 0 || len(st.Protocols) != 0 {
		t.Fatalf("tables must be empty: %+v", st)
	}
	if st.LastWAN.Verdict != verdictNotChecked {
		t.Fatalf("lastWAN = %q, want %q", st.LastWAN.Verdict, verdictNotChecked)
	}
}

// Stopped satellite: package installed, intent disabled, stale pid file.
// Still the normal empty status with Installed/Enabled from the files.
func TestGetStatusStoppedSatelliteReadsFiles(t *testing.T) {
	s, _ := testService(t)
	if err := os.WriteFile(s.initPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.statePath, []byte("ENABLED=no\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.pidPath, []byte("999999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st := s.GetStatus(context.Background())
	if !st.Installed {
		t.Fatal("installed init script was not detected")
	}
	if st.Enabled {
		t.Fatal("ENABLED=no must disable the intent flag")
	}
	if st.Running || st.LastError != "" {
		t.Fatalf("stale pid file must yield a normal empty status: %+v", st)
	}
	if st.LastWAN.Verdict != verdictNotChecked {
		t.Fatalf("lastWAN = %q, want %q", st.LastWAN.Verdict, verdictNotChecked)
	}
}

// Installed with no intent file: the package default is enabled.
func TestDesiredEnabledDefaultsToEnabled(t *testing.T) {
	s, _ := testService(t)
	if err := os.WriteFile(s.initPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	st := s.GetStatus(context.Background())
	if !st.Installed || !st.Enabled {
		t.Fatalf("missing intent file must mean enabled: %+v", st)
	}
}

// Corrupt pid file: no process claims it, status stays normal.
func TestGetStatusCorruptPIDFileIsEmpty(t *testing.T) {
	s, _ := testService(t)
	if err := os.WriteFile(s.pidPath, []byte("not-a-pid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st := s.GetStatus(context.Background())
	if st.Running || st.LastError != "" {
		t.Fatalf("corrupt pid file must be ignored: %+v", st)
	}
}

// A live pid with a silent HTTP endpoint: lastError explains why.
func TestGetStatusSilentProcessSetsLastError(t *testing.T) {
	s, _ := testService(t)
	// The test process itself is alive and will never serve statusURL.
	if err := os.WriteFile(s.pidPath, []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st := s.GetStatus(context.Background())
	if st.Running {
		t.Fatal("no answer must not produce Running")
	}
	if st.LastError == "" {
		t.Fatal("a silent live process must set lastError")
	}
	if st.LastWAN.Verdict != verdictNotChecked {
		t.Fatalf("lastWAN = %q, want %q", st.LastWAN.Verdict, verdictNotChecked)
	}
}

// Full live round trip: snapshot fields land in the Status document.
func TestGetStatusParsesSatelliteSnapshot(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"version": "0.9.0-1",
			"captureActive": true,
			"capturePaused": false,
			"protocols": ["tcp/443", "dns/53"],
			"targetInterface": "nwg0",
			"targetOutbound": "awg-main",
			"resolveVia": "dns.final",
			"lastWAN": {"verdict": "reachable", "durationMs": 570},
			"activeChecks": 2,
			"queuedChecks": 3,
			"addsUsedHour": 7,
			"addsLimit": 50,
			"candidates": [{
				"id": "domain|example.com|tcp|443",
				"target": {"domain": "example.com", "host": "www.example.com", "ip": "93.184.216.34", "network": "tcp", "port": 443},
				"reason": "tls-no-answer",
				"state": "waiting",
				"firstAt": "2026-11-12T10:00:00Z",
				"lastAt": "2026-11-12T10:05:00Z",
				"direct": {"verdict": "blocked", "bytes": 0, "durationMs": 5000, "detail": "timeout"},
				"wan": {"verdict": "reachable", "durationMs": 570},
				"tunnel": {"verdict": "not-checked"},
				"coverage": {"covered": false, "durationMs": 42},
				"targetInterface": "nwg0",
				"correlationID": "c-1"
			}],
			"history": [{
				"at": "2026-11-12T10:06:00Z",
				"domain": "example.com",
				"ip": "93.184.216.34",
				"network": "tcp",
				"port": 443,
				"action": "added",
				"source": "auto",
				"interface": "nwg0",
				"outbound": "awg-main",
				"result": "ok"
			}]
		}`))
	}))
	defer sat.Close()

	s, _ := testService(t)
	s.statusAddr = strings.TrimPrefix(sat.URL, "http://")
	if err := os.WriteFile(s.pidPath, []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st := s.GetStatus(context.Background())
	if !st.Running || st.Version != "0.9.0-1" || st.LastError != "" {
		t.Fatalf("lifecycle = %+v, err %q", st, st.LastError)
	}
	if !st.CaptureActive || st.CapturePaused {
		t.Fatalf("capture = %+v", st)
	}
	if strings.Join(st.Protocols, ",") != "tcp/443,dns/53" {
		t.Fatalf("protocols = %v", st.Protocols)
	}
	if st.TargetInterface != "nwg0" || st.TargetOutbound != "awg-main" || st.ResolveVia != "dns.final" {
		t.Fatalf("target/dns = %q %q %q", st.TargetInterface, st.TargetOutbound, st.ResolveVia)
	}
	if st.LastWAN.Verdict != "reachable" || st.LastWAN.DurationMS != 570 {
		t.Fatalf("lastWAN = %+v", st.LastWAN)
	}
	if st.ActiveChecks != 2 || st.QueuedChecks != 3 {
		t.Fatalf("checks = %d/%d", st.ActiveChecks, st.QueuedChecks)
	}
	if st.AddsUsedHour != 7 || st.AddsLimit != 50 {
		t.Fatalf("adds = %d/%d", st.AddsUsedHour, st.AddsLimit)
	}
	if len(st.Candidates) != 1 || len(st.History) != 1 {
		t.Fatalf("tables = %+v", st)
	}
	cand := st.Candidates[0]
	if cand.Target.Domain != "example.com" || cand.Target.Host != "www.example.com" ||
		cand.Target.IP != "93.184.216.34" || cand.Target.Network != "tcp" || cand.Target.Port != 443 {
		t.Fatalf("candidate target = %+v", cand.Target)
	}
	if cand.State != "waiting" || cand.Reason != "tls-no-answer" {
		t.Fatalf("candidate state/reason = %q %q", cand.State, cand.Reason)
	}
	if cand.Direct.Verdict != "blocked" || cand.WAN.Verdict != "reachable" || cand.Tunnel.Verdict != "not-checked" {
		t.Fatalf("candidate evidence = %+v", cand)
	}
	if cand.Coverage.Covered {
		t.Fatalf("coverage = %+v", cand.Coverage)
	}
	hist := st.History[0]
	if hist.Action != "added" || hist.Source != "auto" || hist.Interface != "nwg0" ||
		hist.Outbound != "awg-main" || hist.Result != "ok" || hist.Port != 443 {
		t.Fatalf("history entry = %+v", hist)
	}
}

// Non-200 from a live satellite: lastError, never a half-parsed status.
func TestGetStatusHTTPErrorSetsLastError(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer sat.Close()

	s, _ := testService(t)
	s.statusAddr = strings.TrimPrefix(sat.URL, "http://")
	if err := os.WriteFile(s.pidPath, []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st := s.GetStatus(context.Background())
	if st.Running || st.LastError == "" {
		t.Fatalf("HTTP 500 from a live satellite must set lastError: %+v", st)
	}
	if len(st.Candidates) != 0 || len(st.History) != 0 {
		t.Fatalf("failed poll must not emit table rows: %+v", st)
	}
}

// Garbage JSON from a live satellite: lastError.
func TestGetStatusBadJSONSetsLastError(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer sat.Close()

	s, _ := testService(t)
	s.statusAddr = strings.TrimPrefix(sat.URL, "http://")
	if err := os.WriteFile(s.pidPath, []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st := s.GetStatus(context.Background())
	if st.Running || st.LastError == "" {
		t.Fatalf("bad JSON from a live satellite must set lastError: %+v", st)
	}
}

func TestSetEnabledPersistsIntentAndCallsInit(t *testing.T) {
	s, _ := testService(t)
	if err := os.WriteFile(s.initPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	var mu []string
	s.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != s.initPath || len(args) != 1 {
			t.Fatalf("command = %s %v", name, args)
		}
		mu = append(mu, args[0])
		return nil, nil
	}
	if err := s.SetEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(s.statePath); err != nil || string(b) != "ENABLED=no\n" {
		t.Fatalf("disabled state = %q, %v", b, err)
	}
	if s.desiredEnabled() {
		t.Fatal("disabled intent was not retained")
	}
	if err := s.SetEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(s.statePath); err != nil || string(b) != "ENABLED=yes\n" {
		t.Fatalf("enabled state = %q, %v", b, err)
	}
	if strings.Join(mu, ",") != "stop,start" {
		t.Fatalf("init actions = %v", mu)
	}
}

// Init failure: the state file already persisted the intent, the error
// carries the script's raw output.
func TestSetEnabledSurfacesInitOutput(t *testing.T) {
	s, _ := testService(t)
	if err := os.WriteFile(s.initPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	s.command = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("PROG not found\n"), errors.New("exit status 1")
	}
	err := s.SetEnabled(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "PROG not found") {
		t.Fatalf("err = %v, want raw init output", err)
	}
	if b, _ := os.ReadFile(s.statePath); string(b) != "ENABLED=yes\n" {
		t.Fatalf("intent must persist before init runs: %q", b)
	}
}

func TestSetEnabledRequiresInstalledInitScript(t *testing.T) {
	s, _ := testService(t)
	s.initPath = filepath.Join(t.TempDir(), "missing")
	if err := s.SetEnabled(context.Background(), false); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("SetEnabled error = %v", err)
	}
	if _, err := os.Stat(s.statePath); !os.IsNotExist(err) {
		t.Fatal("state file created without an installed init script")
	}
}

// Confirm waits for the actual HTTP transition instead of trusting init return.
func TestConfirmStateWaitsForHTTPTransition(t *testing.T) {
	var calls atomic.Int32
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[],"history":[]}`))
	}))
	defer sat.Close()

	s, _ := testService(t)
	s.confirm = nil
	s.confirmTimeout = 2 * time.Second
	s.statusAddr = strings.TrimPrefix(sat.URL, "http://")
	if err := s.confirmState(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got < 3 {
		t.Fatalf("confirm returned before the satellite answered HTTP, calls=%d", got)
	}
}

// Confirm reports failure when the satellite never reaches the wanted state.
func TestConfirmStateFailsOnTimeout(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer sat.Close()

	s, _ := testService(t)
	s.confirm = nil
	s.confirmTimeout = 300 * time.Millisecond
	s.statusAddr = strings.TrimPrefix(sat.URL, "http://")
	if err := s.confirmState(context.Background(), true); err == nil {
		t.Fatal("confirm must fail when the state never arrives")
	}
}

// Context cancellation interrupts the confirm wait.
func TestConfirmStateRespectsContext(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer sat.Close()

	s, _ := testService(t)
	s.confirm = nil
	s.confirmTimeout = 5 * time.Second
	s.statusAddr = strings.TrimPrefix(sat.URL, "http://")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.confirmState(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestStatusEndpointIsLoopbackConstant(t *testing.T) {
	if statusURL != "http://127.0.0.1:2226/status" {
		t.Fatalf("statusURL = %q", statusURL)
	}
	s := NewService()
	if s.statusEndpoint() != statusURL {
		t.Fatalf("endpoint = %q", s.statusEndpoint())
	}
	if s.initScriptPath() != "/opt/etc/init.d/S99awgmautolist" {
		t.Fatalf("init path = %q", s.initScriptPath())
	}
	if s.enabledStatePath() != "/opt/etc/awgmautolist.enabled" {
		t.Fatalf("enabled path = %q", s.enabledStatePath())
	}
	if s.pidFilePath() != "/opt/var/run/awgmautolist.pid" {
		t.Fatalf("pid path = %q", s.pidFilePath())
	}
}
