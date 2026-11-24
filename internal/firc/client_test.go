package firc

import (
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockFircServer struct {
	mu               sync.Mutex
	tunnels          []Tunnel
	groups           []Group
	failTunnels      bool
	failTunnelsOnce  bool
	failGetTunnels   bool
	failGroups       bool
	failGroupsOnce   bool
	failGetGroups    bool
	failHealth       bool
	uplinkDNSVersion int
	putTunnelsCount  int
	putGroupsCount   int
	getGroupsCount   int
	onPutTunnels     func()
	onGetGroups      func()
}

func newMockFircHandler() (*mockFircServer, http.Handler) {
	m := &mockFircServer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		fail := m.failHealth
		dnsVersion := m.uplinkDNSVersion
		m.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(NetfilterHealth{Ok: true, UplinkDNSVersion: dnsVersion})
	})

	mux.HandleFunc("/api/v1/tunnels", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			m.mu.Lock()
			failGet := m.failGetTunnels
			tuns := m.tunnels
			m.mu.Unlock()
			if failGet {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "injected GET tunnels failure"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"tunnels": tuns})
		case http.MethodPut:
			m.mu.Lock()
			hook := m.onPutTunnels
			m.mu.Unlock()
			if hook != nil {
				hook()
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			m.putTunnelsCount++
			var req struct {
				Tunnels []Tunnel `json:"tunnels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			if m.failTunnelsOnce {
				m.failTunnelsOnce = false
				// Simulate partial runtime mutation before returning 500
				m.tunnels = req.Tunnels
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "injected one-shot tunnel sync failure"})
				return
			}
			if m.failTunnels {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "injected persistent tunnel sync failure"})
				return
			}
			m.tunnels = req.Tunnels
			var restarted []string
			for _, t := range m.tunnels {
				restarted = append(restarted, t.Device)
			}
			_ = json.NewEncoder(w).Encode(PutTunnelsResponse{
				Tunnels:   m.tunnels,
				Restarted: restarted,
			})
		}
	})

	mux.HandleFunc("/api/v1/tunnels/state", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var states []TunnelState
		for _, t := range m.tunnels {
			states = append(states, TunnelState{
				ID:       t.ID,
				Device:   t.Device,
				Status:   "up",
				Active:   []string{"node-1"},
				UplinkOk: true,
				TCPPort:  41000,
				RxBps:    125000,
				TxBps:    64000,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"tunnels": states})
	})

	mux.HandleFunc("/api/v1/tunnels/probe", func(w http.ResponseWriter, r *http.Request) {
		hs := 24
		fb := 58
		_ = json.NewEncoder(w).Encode(ProbeResponse{
			Nodes: []ProbeNodeResult{
				{Key: "node-tt", Ok: true, HandshakeMs: &hs, FirstByteMs: &fb, Why: "ok, VLESS response (2 bytes)"},
			},
		})
	})

	mux.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			m.mu.Lock()
			getHook := m.onGetGroups
			m.mu.Unlock()
			if getHook != nil {
				getHook()
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			m.getGroupsCount++
			if m.failGetGroups {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "injected GET groups failure"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"groups": cloneGroups(m.groups)})
		case http.MethodPut:
			m.putGroupsCount++
			var req struct {
				Groups []Group `json:"groups"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if m.failGroupsOnce {
				m.failGroupsOnce = false
				// Simulate partial runtime mutation before returning 500
				m.groups = cloneGroups(req.Groups)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "injected one-shot group sync failure"})
				return
			}
			if m.failGroups {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "injected persistent group sync failure"})
				return
			}
			m.groups = cloneGroups(req.Groups)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"groups": cloneGroups(m.groups)})
		}
	})

	return m, mux
}

func TestClientAndOperatorSync(t *testing.T) {
	_, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tmpDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:   tmpDir,
		InboundPort: 1099,
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))

	if err := op.EnsureConfigFiles(); err != nil {
		t.Fatalf("EnsureConfigFiles: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "firc.conf")); err != nil {
		t.Fatalf("expected firc.conf to exist: %v", err)
	}

	ctx := context.Background()
	if !op.IsRunning(ctx) {
		t.Fatalf("expected mock fircd to report running")
	}

	tunnels := []Tunnel{
		{
			ID:          "tun-main",
			Device:      "tunvless0",
			Description: "Main VLESS + TrustTunnel Pool",
			Enable:      true,
			Uplink:      TunnelUplink{Kind: "auto"},
			Sources: []TunnelSource{
				{ID: "s1", Kind: "link", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:443?security=reality&sni=a.com&pbk=K#VLESS-1"},
				{ID: "s2", Kind: "link", Link: "tt://alice:secret@tt.example.com:8443?sni=cover.example.com&record_fragment=1#TrustTunnel-1"},
				{ID: "s3", Kind: "link", Link: "socks5://bob:pass@127.0.0.1:1080#LocalSOCKS"},
			},
			Active:   1,
			By:       "latency",
			Interval: 30,
			Silence:  15,
			Advanced: TunnelAdvanced{
				Timeout:   8,
				KernelTCP: true,
			},
		},
	}

	putResp, err := op.SyncTunnels(ctx, tunnels, true)
	if err != nil {
		t.Fatalf("SyncTunnels: %v", err)
	}
	if len(putResp.Tunnels) != 1 {
		t.Fatalf("expected 1 tunnel, got %d", len(putResp.Tunnels))
	}
	if len(putResp.Tunnels[0].Inbounds) != 1 || putResp.Tunnels[0].Inbounds[0].Port != 1099 {
		t.Fatalf("expected port 1099 inbound to be automatically attached, got %+v", putResp.Tunnels[0].Inbounds)
	}

	if _, err := op.Client().SelectNode(ctx, "tun-main", "TrustTunnel-1"); err != nil {
		t.Fatalf("SelectNode: %v", err)
	}
	savedTunnels, err := op.Client().GetTunnels(ctx)
	if err != nil {
		t.Fatalf("GetTunnels: %v", err)
	}
	if len(savedTunnels[0].Order) == 0 || savedTunnels[0].Order[0] != "TrustTunnel-1" {
		t.Fatalf("expected TrustTunnel-1 at front of Order, got %+v", savedTunnels[0].Order)
	}

	states, err := op.Client().GetTunnelsState(ctx)
	if err != nil {
		t.Fatalf("GetTunnelsState: %v", err)
	}
	if len(states) != 1 || states[0].Status != "up" || states[0].TCPPort != 41000 {
		t.Fatalf("unexpected tunnel state: %+v", states)
	}

	probeResp, err := op.Client().ProbeTunnel(ctx, "tun-main", nil)
	if err != nil {
		t.Fatalf("ProbeTunnel: %v", err)
	}
	if len(probeResp.Nodes) != 1 || !probeResp.Nodes[0].Ok {
		t.Fatalf("unexpected probe response: %+v", probeResp)
	}

	groups := []Group{
		{
			ID:        "01020304",
			Name:      "YouTube & AI",
			Interface: GroupInterface{"tunvless0", "nwg0"},
			Enable:    true,
			Resolve:   GroupResolve{Tunnel: true},
			Rules: []Rule{
				{Type: "DotWild", Rule: "youtube.com", Enable: true},
				{Type: "DotWild", Rule: "openai.com", Enable: true},
			},
		},
	}
	syncedGroups, err := op.SyncGroups(ctx, groups)
	if err != nil {
		t.Fatalf("SyncGroups: %v", err)
	}
	if len(syncedGroups) != 1 || len(syncedGroups[0].Interface) != 2 {
		t.Fatalf("unexpected synced groups: %+v", syncedGroups)
	}
}

func TestTransactionalRollbackAndSwitchEngine(t *testing.T) {
	mockSrv, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tmpDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:   tmpDir,
		FircdBinary: filepath.Join(tmpDir, "missing-fircd"),
		TunvlessBin: filepath.Join(tmpDir, "missing-tunvless"),
		InitScript:  filepath.Join(tmpDir, "missing-init"),
		InboundPort: 1099,
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	ctx := context.Background()

	// 1. ApplyTunnels rolls back BOTH tunnels.yaml AND live runtime when SyncTunnels fails once
	initialTunnels := []Tunnel{{ID: "initial-tun", Device: "firc0", Enable: true}}
	if _, err := op.ApplyTunnels(ctx, initialTunnels, true); err != nil {
		t.Fatalf("initial ApplyTunnels: %v", err)
	}
	mockSrv.mu.Lock()
	mockSrv.failTunnelsOnce = true
	mockSrv.mu.Unlock()

	badTunnels := []Tunnel{{ID: "broken-tun", Device: "firc9", Enable: true}}
	if _, err := op.ApplyTunnels(ctx, badTunnels, true); err == nil {
		t.Fatal("expected ApplyTunnels to fail when fircd returns 500")
	}
	diskTunnels, err := op.LoadTunnelsFile()
	if err != nil || len(diskTunnels) != 1 || diskTunnels[0].ID != "initial-tun" {
		t.Fatalf("expected tunnels.yaml to roll back to initial-tun, got: %+v (err=%v)", diskTunnels, err)
	}
	mockSrv.mu.Lock()
	if len(mockSrv.tunnels) != 1 || mockSrv.tunnels[0].ID != "initial-tun" {
		mTuns := mockSrv.tunnels
		mockSrv.mu.Unlock()
		t.Fatalf("expected runtime tunnels in fircd to roll back to initial-tun, got: %+v", mTuns)
	}
	mockSrv.mu.Unlock()

	// 1b. If runtime rollback itself fails (persistent failure), ApplyTunnels sets recovery_required
	mockSrv.mu.Lock()
	mockSrv.failTunnels = true
	mockSrv.mu.Unlock()
	if _, err := op.ApplyTunnels(ctx, badTunnels, true); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired when runtime rollback fails, got: %v", err)
	}
	if req, _ := op.RecoveryInfo(); !req {
		t.Fatal("expected RecoveryInfo() == true after failed runtime rollback")
	}
	// Further mutations must be blocked while recovery_required is set
	mockSrv.mu.Lock()
	mockSrv.failTunnels = false
	mockSrv.mu.Unlock()
	if _, err := op.ApplyTunnels(ctx, initialTunnels, true); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ApplyTunnels to be blocked by ErrRecoveryRequired, got: %v", err)
	}
	op.ClearRecovery()

	// 2. ApplyGroups rolls back BOTH groups.yaml AND live runtime when SyncGroups fails once
	initialGroups := []Group{{ID: "initial-grp", Name: "Initial", Interface: GroupInterface{"firc0"}, Enable: true}}
	if _, err := op.ApplyGroups(ctx, initialGroups); err != nil {
		t.Fatalf("initial ApplyGroups: %v", err)
	}
	mockSrv.mu.Lock()
	mockSrv.failGroupsOnce = true
	mockSrv.mu.Unlock()

	badGroups := []Group{{ID: "broken-grp", Name: "Broken", Interface: GroupInterface{"firc9"}, Enable: true}}
	if _, err := op.ApplyGroups(ctx, badGroups); err == nil {
		t.Fatal("expected ApplyGroups to fail when fircd returns 500")
	}
	diskGroups, err := op.LoadGroupsFile()
	if err != nil || len(diskGroups) != 1 || diskGroups[0].ID != "initial-grp" {
		t.Fatalf("expected groups.yaml to roll back to initial-grp, got: %+v (err=%v)", diskGroups, err)
	}
	mockSrv.mu.Lock()
	if len(mockSrv.groups) != 1 || mockSrv.groups[0].ID != "initial-grp" {
		mGrps := mockSrv.groups
		mockSrv.mu.Unlock()
		t.Fatalf("expected runtime groups in fircd to roll back to initial-grp, got: %+v", mGrps)
	}
	mockSrv.mu.Unlock()

	// 4. SwitchEngine saga fault injection:
	mockSrv.mu.Lock()
	mockSrv.failHealth = true
	mockSrv.mu.Unlock()

	currentEngine := "sing-box"
	singboxRunning := true
	var failStopSingbox bool
	var failStartSingbox bool

	hooks := EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) {
			return currentEngine, nil
		},
		StartTarget: func(_ context.Context, target string) error {
			if target == "sing-box" {
				if failStartSingbox {
					return errors.New("failed to restart sing-box")
				}
				singboxRunning = true
			}
			return nil
		},
		StopEngine: func(_ context.Context, engine string) error {
			if engine == "sing-box" {
				singboxRunning = false
				if failStopSingbox {
					return errors.New("failed to stop sing-box cleanly")
				}
			}
			return nil
		},
		CommitSettings: func(_ context.Context, target string) error {
			currentEngine = target
			return nil
		},
	}
	op.SetDefaultEngineHooks(hooks)

	// 4a. GetCurrentEngine error fails closed without guessing sing-box
	if err := op.SwitchEngine(ctx, "firc", EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) {
			return "", errors.New("settings store corrupted")
		},
	}); err == nil {
		t.Fatal("expected SwitchEngine to fail closed when GetCurrentEngine returns error")
	}

	// 4b. Missing binary when daemon is down -> fails closed before committing settings
	if err := op.SwitchEngine(ctx, "firc"); err == nil {
		t.Fatal("expected SwitchEngine to fail when fircd is down and binaries are missing")
	}
	if currentEngine != "sing-box" || !singboxRunning {
		t.Fatalf("expected sing-box to remain active and running, got engine=%q running=%v", currentEngine, singboxRunning)
	}

	// 4c. Candidate starts, but SyncTunnels fails -> candidate is stopped and settings stay sing-box
	var stopCalls int
	op.SetLifecycleHooks(
		func(_ context.Context) error {
			mockSrv.mu.Lock()
			mockSrv.failHealth = false
			mockSrv.mu.Unlock()
			return nil
		},
		func(_ context.Context) error {
			stopCalls++
			mockSrv.mu.Lock()
			mockSrv.failHealth = true
			mockSrv.mu.Unlock()
			return nil
		},
	)
	mockSrv.mu.Lock()
	mockSrv.failTunnelsOnce = true
	mockSrv.mu.Unlock()

	if err := op.SwitchEngine(ctx, "firc"); err == nil {
		t.Fatal("expected SwitchEngine to fail when SyncTunnels fails")
	}
	if currentEngine != "sing-box" || !singboxRunning {
		t.Fatalf("expected engine to remain sing-box when SyncTunnels fails, got %q", currentEngine)
	}
	if stopCalls != 1 {
		t.Fatalf("expected candidate fircd to be stopped on SyncTunnels failure, stopCalls=%d", stopCalls)
	}

	// 4d. StopEngine("sing-box") fails after CommitSettings -> saga restarts sing-box, restores settings, and stops candidate fircd
	failStopSingbox = true
	if err := op.SwitchEngine(ctx, "firc"); err == nil {
		t.Fatal("expected SwitchEngine to fail when StopEngine(sing-box) fails")
	}
	failStopSingbox = false
	if currentEngine != "sing-box" {
		t.Fatalf("expected settings to roll back to sing-box when StopEngine fails, got %q", currentEngine)
	}
	if !singboxRunning {
		t.Fatal("expected saga rollback to restart sing-box after failed StopEngine")
	}
	if stopCalls != 2 {
		t.Fatalf("expected saga rollback to stop candidate fircd after failed StopEngine, stopCalls=%d", stopCalls)
	}

	// 4e. If restarting previous engine fails during rollback -> sets ErrRecoveryRequired
	failStopSingbox = true
	failStartSingbox = true
	err = op.SwitchEngine(ctx, "firc")
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired when rollback fails to restart sing-box, got: %v", err)
	}
	failStopSingbox = false
	failStartSingbox = false
	if req, _ := op.RecoveryInfo(); !req {
		t.Fatal("expected RecoveryInfo() == true after failed saga rollback")
	}
	op.ClearRecovery()
	singboxRunning = true

	// 4f. Healthy switch sing-box -> firc -> sing-box succeeds and starts/stops both engines
	if err := op.SwitchEngine(ctx, "firc"); err != nil {
		t.Fatalf("expected healthy SwitchEngine to firc to succeed, got: %v", err)
	}
	if currentEngine != "firc" || singboxRunning {
		t.Fatalf("expected currentEngine == firc and singboxRunning == false, got %q / %v", currentEngine, singboxRunning)
	}
	if err := op.SwitchEngine(ctx, "sing-box"); err != nil {
		t.Fatalf("expected healthy SwitchEngine back to sing-box to succeed, got: %v", err)
	}
	if currentEngine != "sing-box" || !singboxRunning {
		t.Fatalf("expected currentEngine == sing-box and singboxRunning == true, got %q / %v", currentEngine, singboxRunning)
	}
}

func TestCandidatePartialStartCleanupAndSafeRollbackOrder(t *testing.T) {
	mockSrv, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tmpDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:   tmpDir,
		FircdBinary: filepath.Join(tmpDir, "fircd"),
		TunvlessBin: filepath.Join(tmpDir, "tunvless"),
		InitScript:  filepath.Join(tmpDir, "S99firc"),
		InboundPort: 1099,
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	ctx := context.Background()

	// Seed a tunnel so startAndSyncFirc performs SyncTunnels
	_ = op.SaveTunnelsFile([]Tunnel{{ID: "t1", Device: "firc0", Enable: true}})

	currentEngine := "sing-box"
	singboxRunning := true
	singboxPartiallyStarted := false
	fircRunning := false
	var failStartSingbox bool
	var failStopSingbox bool
	var failCommit bool
	var trace []string

	op.SetLifecycleHooks(
		func(_ context.Context) error {
			fircRunning = true
			mockSrv.mu.Lock()
			mockSrv.failHealth = false
			mockSrv.mu.Unlock()
			trace = append(trace, "start_candidate_firc")
			return nil
		},
		func(_ context.Context) error {
			fircRunning = false
			mockSrv.mu.Lock()
			mockSrv.failHealth = true
			mockSrv.mu.Unlock()
			trace = append(trace, "stop_candidate_firc")
			return nil
		},
	)

	op.SetDefaultEngineHooks(EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) {
			return currentEngine, nil
		},
		StartTarget: func(_ context.Context, target string) error {
			if target == "sing-box" {
				if failStartSingbox {
					// Simulate partial start (daemon spawned, then readiness / sync failed)
					singboxPartiallyStarted = true
					trace = append(trace, "partial_start_sing-box")
					return errors.New("sing-box readiness probe failed after process spawn")
				}
				singboxRunning = true
				trace = append(trace, "restart_previous_sing-box")
			}
			return nil
		},
		StopEngine: func(_ context.Context, engine string) error {
			if engine == "sing-box" {
				if singboxPartiallyStarted {
					trace = append(trace, "stop_candidate_sing-box")
					if failStopSingbox {
						return errors.New("stop sing-box failed during partial-start cleanup")
					}
					singboxPartiallyStarted = false
					return nil
				}
				singboxRunning = false
				trace = append(trace, "stop_previous_sing-box")
				if failStopSingbox {
					return errors.New("stop sing-box failed")
				}
			}
			return nil
		},
		CommitSettings: func(_ context.Context, target string) error {
			if failCommit && target != currentEngine {
				trace = append(trace, "fail_commit_"+target)
				return errors.New("disk full writing settings.json")
			}
			if target == "sing-box" && currentEngine != "sing-box" {
				trace = append(trace, "restore_settings_sing-box")
			} else {
				trace = append(trace, "commit_settings_"+target)
			}
			currentEngine = target
			return nil
		},
	})

	// 0. Target "mihomo" MUST be rejected immediately as an unsupported target routing engine
	if err := op.SwitchEngine(ctx, "mihomo"); err == nil {
		t.Fatal("expected SwitchEngine(mihomo) to be rejected as unsupported target engine")
	}

	// 1. P0.2: StartTarget("sing-box") partially starts sing-box when switching firc -> sing-box and returns error ->
	//    StopEngine("sing-box") MUST run to clean up the partially started candidate!
	currentEngine = "firc"
	failStartSingbox = true
	trace = nil
	if err := op.SwitchEngine(ctx, "sing-box"); err == nil {
		t.Fatal("expected SwitchEngine(sing-box) to fail on StartTarget error")
	}
	if singboxPartiallyStarted {
		t.Fatal("P0.2 regression: partially started sing-box candidate was NOT stopped on StartTarget error")
	}
	wantTrace1 := []string{"partial_start_sing-box", "stop_candidate_sing-box"}
	if !reflect.DeepEqual(trace, wantTrace1) {
		t.Fatalf("expected partial-start cleanup trace %v, got %v", wantTrace1, trace)
	}

	// 2. P0.2: If StopEngine("sing-box") ALSO fails during partial-start cleanup -> sets recovery_required
	failStopSingbox = true
	err := op.SwitchEngine(ctx, "sing-box")
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired when candidate partial-start cleanup fails, got: %v", err)
	}
	failStartSingbox = false
	failStopSingbox = false
	singboxPartiallyStarted = false
	currentEngine = "sing-box"
	op.ClearRecovery()

	// 3. Step 2 fault injection: CommitSettings fails after candidate firc started -> Phase R1 stops candidate firc
	trace = nil
	failCommit = true
	if err := op.SwitchEngine(ctx, "firc"); err == nil {
		t.Fatal("expected SwitchEngine(firc) to fail when CommitSettings fails")
	}
	failCommit = false
	if fircRunning {
		t.Fatal("expected candidate firc to be stopped when CommitSettings fails")
	}
	wantTrace2 := []string{"start_candidate_firc", "fail_commit_firc", "stop_candidate_firc"}
	if !reflect.DeepEqual(trace, wantTrace2) {
		t.Fatalf("expected CommitSettings failure trace %v, got %v", wantTrace2, trace)
	}

	// 4. P1.3: Step 3 fault injection: StopEngine(sing-box) fails after candidate firc started and settings committed ->
	//    Rollback MUST execute in deterministic safe order:
	//    Phase R1 (stop_candidate_firc) -> Phase R2 (restore_settings_sing-box) -> Phase R3 (restart_previous_sing-box)
	trace = nil
	failStopSingbox = true
	if err := op.SwitchEngine(ctx, "firc"); err == nil {
		t.Fatal("expected SwitchEngine(firc) to fail when StopEngine(sing-box) fails")
	}
	failStopSingbox = false
	wantTrace3 := []string{
		"start_candidate_firc",
		"commit_settings_firc",
		"stop_previous_sing-box",
		"stop_candidate_firc",
		"restore_settings_sing-box",
		"restart_previous_sing-box",
	}
	if !reflect.DeepEqual(trace, wantTrace3) {
		t.Fatalf("P1.3 rollback order mismatch:\ngot:  %v\nwant: %v", trace, wantTrace3)
	}
	if fircRunning || !singboxRunning || currentEngine != "sing-box" {
		t.Fatalf("unexpected post-rollback state: fircRunning=%v singboxRunning=%v currentEngine=%q", fircRunning, singboxRunning, currentEngine)
	}
}

func TestDurableRecoveryStateAndVerifiedReconcile(t *testing.T) {
	mockSrv, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tmpDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:   tmpDir,
		FircdBinary: filepath.Join(tmpDir, "fircd"),
		TunvlessBin: filepath.Join(tmpDir, "tunvless"),
		InitScript:  filepath.Join(tmpDir, "S99firc"),
		InboundPort: 1099,
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	ctx := context.Background()

	_ = op.SaveTunnelsFile([]Tunnel{{ID: "t-durable", Device: "firc0", Enable: true}})
	_ = op.SaveGroupsFile([]Group{{ID: "g-durable", Name: "Durable", Interface: GroupInterface{"firc0"}, Enable: true}})

	currentEngine := "sing-box"
	op.SetLifecycleHooks(
		func(_ context.Context) error { return nil },
		func(_ context.Context) error { return errors.New("stop candidate firc failed") },
	)
	op.SetDefaultEngineHooks(EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) { return currentEngine, nil },
		StartTarget:      func(_ context.Context, _ string) error { return nil },
		StopEngine:       func(_ context.Context, _ string) error { return nil },
		CommitSettings: func(_ context.Context, _ string) error {
			return errors.New("commit failed")
		},
	})

	// Trigger rollback failure -> persists recovery-state.json
	err := op.SwitchEngine(ctx, "firc")
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got: %v", err)
	}
	if _, statErr := os.Stat(op.RecoveryMarkerPath()); statErr != nil {
		t.Fatalf("expected durable recovery-state.json at %s, statErr=%v", op.RecoveryMarkerPath(), statErr)
	}

	// Simulate process restart by constructing a brand-new Operator over the same ConfigDir (P0.4)
	opRestarted := NewOperator(Options{
		ConfigDir:   tmpDir,
		FircdBinary: filepath.Join(tmpDir, "fircd"),
		TunvlessBin: filepath.Join(tmpDir, "tunvless"),
		InitScript:  filepath.Join(tmpDir, "S99firc"),
		InboundPort: 1099,
	})
	opRestarted.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	if !opRestarted.IsRecoveryRequired() {
		t.Fatal("P0.4 regression: NewOperator did not load persisted recovery-state.json on startup")
	}
	marker := opRestarted.RecoveryMarkerInfo()
	if marker == nil || marker.TargetEngine != "firc" || marker.PreviousEngine != "sing-box" || marker.TransitionID == "" {
		t.Fatalf("unexpected persisted RecoveryMarker after restart: %+v", marker)
	}

	// Verify all mutations remain blocked after restart while recovery_required is set
	if _, err := opRestarted.ApplyTunnels(ctx, nil, false); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ApplyTunnels blocked after restart, got: %v", err)
	}
	if err := opRestarted.SwitchEngine(ctx, "sing-box"); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected SwitchEngine blocked after restart, got: %v", err)
	}

	// P0.3: Test ReconcileRecovery when non-target stop or target verification fails -> MUST NOT clear recovery_required
	var stoppedNonTargets []string
	failStopOther := true
	opRestarted.SetLifecycleHooks(
		func(_ context.Context) error { return nil },
		func(_ context.Context) error { return nil },
	)
	opRestarted.SetDefaultEngineHooks(EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) { return currentEngine, nil },
		StartTarget:      func(_ context.Context, _ string) error { return nil },
		StopEngine: func(_ context.Context, eng string) error {
			if failStopOther {
				return errors.New("cannot stop " + eng)
			}
			stoppedNonTargets = append(stoppedNonTargets, eng)
			return nil
		},
		CommitSettings: func(_ context.Context, eng string) error {
			currentEngine = eng
			return nil
		},
	})

	if _, err := opRestarted.ReconcileRecovery(ctx, "firc"); err == nil {
		t.Fatal("expected ReconcileRecovery to fail when StopEngine fails")
	}
	if !opRestarted.IsRecoveryRequired() {
		t.Fatal("P0.3 regression: failed ReconcileRecovery cleared recovery_required")
	}

	// Now allow StopEngine and firc start/sync to succeed -> ReconcileRecovery verifies runtime and clears marker
	failStopOther = false
	rep, err := opRestarted.ReconcileRecovery(ctx, "firc")
	if err != nil {
		t.Fatalf("expected ReconcileRecovery to succeed, got: %v", err)
	}
	if !rep.Verified || !rep.ClearedRecovery || rep.TargetEngine != "firc" {
		t.Fatalf("unexpected ReconcileReport: %+v", rep)
	}
	if opRestarted.IsRecoveryRequired() {
		t.Fatal("expected recovery_required to be cleared after successful ReconcileRecovery")
	}
	if _, statErr := os.Stat(opRestarted.RecoveryMarkerPath()); !os.IsNotExist(statErr) {
		t.Fatalf("expected recovery-state.json to be deleted after successful ReconcileRecovery, statErr=%v", statErr)
	}
	if currentEngine != "firc" {
		t.Fatalf("expected settings committed to firc after ReconcileRecovery, got %q", currentEngine)
	}
	mockSrv.mu.Lock()
	tunCount := len(mockSrv.tunnels)
	grpCount := len(mockSrv.groups)
	mockSrv.mu.Unlock()
	if tunCount != 1 || grpCount != 1 {
		t.Fatalf("expected ReconcileRecovery to sync 1 tunnel and 1 group to fircd, got tunnels=%d groups=%d", tunCount, grpCount)
	}
}

func TestFailClosedLiveSnapshotAndUnifiedMutationLock(t *testing.T) {
	mockSrv, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tmpDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:   tmpDir,
		FircdBinary: filepath.Join(tmpDir, "fircd"),
		TunvlessBin: filepath.Join(tmpDir, "tunvless"),
		InitScript:  filepath.Join(tmpDir, "S99firc"),
		InboundPort: 1099,
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	ctx := context.Background()

	// Seed initial valid state
	initialTunnels := []Tunnel{{ID: "orig-tun", Device: "firc0", Enable: true}}
	initialGroups := []Group{{ID: "orig-grp", Name: "Orig", Interface: GroupInterface{"firc0"}, Enable: true}}
	if _, err := op.ApplyTunnels(ctx, initialTunnels, true); err != nil {
		t.Fatalf("seed ApplyTunnels: %v", err)
	}
	if _, err := op.ApplyGroups(ctx, initialGroups); err != nil {
		t.Fatalf("seed ApplyGroups: %v", err)
	}

	// 1. P1.1: When fircd is running and GET /api/v1/tunnels or GET /api/v1/groups fails,
	//    ApplyTunnels, ApplyGroups, and MigrateFromSingbox MUST fail closed BEFORE writing disk or calling PUT!
	mockSrv.mu.Lock()
	mockSrv.failGetTunnels = true
	mockSrv.failGetGroups = true
	beforePutTunnels := mockSrv.putTunnelsCount
	beforePutGroups := mockSrv.putGroupsCount
	mockSrv.mu.Unlock()

	if _, err := op.ApplyTunnels(ctx, []Tunnel{{ID: "should-not-write", Device: "firc1", Enable: true}}, true); err == nil {
		t.Fatal("P1.1 regression: expected ApplyTunnels to fail closed when live GetTunnels snapshot fails")
	}
	if _, err := op.ApplyGroups(ctx, []Group{{ID: "should-not-write", Name: "Bad", Interface: GroupInterface{"firc1"}, Enable: true}}); err == nil {
		t.Fatal("P1.1 regression: expected ApplyGroups to fail closed when live GetGroups snapshot fails")
	}

	mockSrv.mu.Lock()
	afterPutTunnels := mockSrv.putTunnelsCount
	afterPutGroups := mockSrv.putGroupsCount
	mockSrv.failGetTunnels = false
	mockSrv.failGetGroups = false
	mockSrv.mu.Unlock()

	if afterPutTunnels != beforePutTunnels || afterPutGroups != beforePutGroups {
		t.Fatalf("P1.1 regression: PUT was called despite failed live snapshot (putTunnels %d->%d, putGroups %d->%d)",
			beforePutTunnels, afterPutTunnels, beforePutGroups, afterPutGroups)
	}
	diskTuns, _ := op.LoadTunnelsFile()
	if len(diskTuns) != 1 || diskTuns[0].ID != "orig-tun" {
		t.Fatalf("P1.1 regression: disk tunnels.yaml was modified despite failed live snapshot: %+v", diskTuns)
	}

	// 2. P1.2: Concurrent SwitchEngine, ApplyTunnels, ApplyGroups, and MigrateFromSingbox MUST serialize under mutationMu
	var inFlight atomic.Int32
	var maxConcurrent atomic.Int32
	recordEnter := func() {
		cur := inFlight.Add(1)
		for {
			prevMax := maxConcurrent.Load()
			if cur <= prevMax || maxConcurrent.CompareAndSwap(prevMax, cur) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		inFlight.Add(-1)
	}

	mockSrv.mu.Lock()
	mockSrv.onPutTunnels = recordEnter
	mockSrv.mu.Unlock()
	defer func() {
		mockSrv.mu.Lock()
		mockSrv.onPutTunnels = nil
		mockSrv.mu.Unlock()
	}()

	currentEngine := "firc"
	op.SetLifecycleHooks(
		func(_ context.Context) error { return nil },
		func(_ context.Context) error { return nil },
	)
	op.SetDefaultEngineHooks(EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) { return currentEngine, nil },
		StartTarget: func(_ context.Context, _ string) error {
			recordEnter()
			return nil
		},
		StopEngine: func(_ context.Context, _ string) error { return nil },
		CommitSettings: func(_ context.Context, eng string) error {
			currentEngine = eng
			return nil
		},
	})

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(3)
		go func(idx int) {
			defer wg.Done()
			_, _ = op.ApplyTunnels(ctx, []Tunnel{{ID: "conc-tun", Device: "firc0", Enable: true}}, true)
		}(i)
		go func(idx int) {
			defer wg.Done()
			_, _ = op.ApplyGroups(ctx, []Group{{ID: "conc-grp", Name: "Conc", Interface: GroupInterface{"firc0"}, Enable: true}})
		}(i)
		go func(idx int) {
			defer wg.Done()
			_ = op.SwitchEngine(ctx, "firc")
		}(i)
	}
	wg.Wait()

	if maxConcurrent.Load() > 1 {
		t.Fatalf("P1.2 regression: detected concurrent overlapping mutations (maxConcurrent=%d, want 1)", maxConcurrent.Load())
	}
}

func TestRecoveryMarkerPersistenceAndClearFaultInjection(t *testing.T) {
	mockSrv, handler := newMockFircHandler()
	_ = mockSrv
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tmpDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:   tmpDir,
		FircdBinary: filepath.Join(tmpDir, "fircd"),
		TunvlessBin: filepath.Join(tmpDir, "tunvless"),
		InitScript:  filepath.Join(tmpDir, "S99firc"),
		InboundPort: 1099,
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	ctx := context.Background()

	// 1. P0.1: Fault injection on SetRecoveryRequiredWithMeta (make recovery-state.json a non-empty directory so rename fails)
	blockerDir := op.RecoveryMarkerPath()
	if err := os.MkdirAll(filepath.Join(blockerDir, "sub"), 0o755); err != nil {
		t.Fatalf("create blocker dir: %v", err)
	}

	op.SetLifecycleHooks(
		func(_ context.Context) error { return nil },
		func(_ context.Context) error { return errors.New("stop candidate failed") },
	)
	op.SetDefaultEngineHooks(EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) { return "sing-box", nil },
		StartTarget:      func(_ context.Context, _ string) error { return nil },
		StopEngine:       func(_ context.Context, _ string) error { return nil },
		CommitSettings:   func(_ context.Context, _ string) error { return errors.New("commit failed") },
	})

	swErr := op.SwitchEngine(ctx, "firc")
	if !errors.Is(swErr, ErrRecoveryRequired) || !errors.Is(swErr, ErrRecoveryPersistFailed) {
		t.Fatalf("P0.1 regression: expected joined ErrRecoveryRequired + ErrRecoveryPersistFailed when marker write fails, got: %v", swErr)
	}
	// Even when disk persistence fails, in-memory recovery_required MUST stay true to block all further mutations
	if !op.IsRecoveryRequired() {
		t.Fatal("P0.1 regression: in-memory recovery_required was not set after persistence failure")
	}
	if _, err := op.ApplyTunnels(ctx, nil, false); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ApplyTunnels blocked in memory after persist failure, got: %v", err)
	}

	// Remove blocker directory and persist a real marker
	_ = os.RemoveAll(blockerDir)
	if err := op.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Reason:         "simulated rollback failure",
		TransitionID:   "tx-persist-test-1",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
		FailedStep:     "stop_candidate_firc",
	}); err != nil {
		t.Fatalf("SetRecoveryRequiredWithMeta on writable dir failed: %v", err)
	}

	// 2. P0.1: Fault injection on ClearRecovery / ReconcileRecovery when os.Remove fails
	// Replace recovery-state.json with a non-empty directory so os.Remove(markerPath) fails with ENOTEMPTY
	_ = os.Remove(blockerDir)
	if err := os.MkdirAll(filepath.Join(blockerDir, "keep"), 0o755); err != nil {
		t.Fatalf("create non-empty dir at marker path: %v", err)
	}

	currentEngine := "sing-box"
	op.SetLifecycleHooks(
		func(_ context.Context) error { return nil },
		func(_ context.Context) error { return nil },
	)
	op.SetDefaultEngineHooks(EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) { return currentEngine, nil },
		StartTarget:      func(_ context.Context, _ string) error { return nil },
		StopEngine:       func(_ context.Context, _ string) error { return nil },
		CommitSettings: func(_ context.Context, eng string) error {
			currentEngine = eng
			return nil
		},
	})

	rep, recErr := op.ReconcileRecovery(ctx, "sing-box")
	if !errors.Is(recErr, ErrRecoveryPersistFailed) {
		t.Fatalf("P0.1 regression: expected ErrRecoveryPersistFailed when removing recovery-state.json fails, got: %v", recErr)
	}
	if rep != nil {
		t.Fatalf("P0.1 regression: expected nil ReconcileReport when ClearRecovery fails, got: %+v", rep)
	}
	if !op.IsRecoveryRequired() {
		t.Fatal("P0.1 regression: ClearRecovery cleared in-memory recovery_required despite failed disk removal")
	}

	// Restore valid marker file and verify ReconcileRecovery now removes disk marker and clears memory
	_ = os.RemoveAll(blockerDir)
	if err := op.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Reason:         "simulated rollback failure",
		TransitionID:   "tx-persist-test-2",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
		FailedStep:     "stop_candidate_firc",
	}); err != nil {
		t.Fatalf("re-seed marker: %v", err)
	}

	rep, recErr = op.ReconcileRecovery(ctx, "sing-box")
	if recErr != nil {
		t.Fatalf("expected ReconcileRecovery to succeed after restoring writable marker path, got: %v", recErr)
	}
	if !rep.Verified || !rep.ClearedRecovery || op.IsRecoveryRequired() {
		t.Fatalf("unexpected state after successful ReconcileRecovery: rep=%+v recovery=%v", rep, op.IsRecoveryRequired())
	}
	opRestarted := NewOperator(Options{ConfigDir: tmpDir})
	if opRestarted.IsRecoveryRequired() {
		t.Fatal("expected restarted operator to have recovery_required=false after successful ClearRecovery")
	}

	// 3. P0.1: Fault injection on directory fsync during SetRecoveryRequiredWithMeta (rename succeeds, dir fsync fails)
	op.SetDirSyncHook(func(_ string) error {
		return errors.New("injected directory fsync failure on write")
	})
	dirSyncWriteErr := op.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Reason:         "dir fsync failure on write",
		TransitionID:   "tx-dir-fsync-write-fail",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
		FailedStep:     "stop_candidate_firc",
	})
	if !errors.Is(dirSyncWriteErr, ErrRecoveryPersistFailed) {
		t.Fatalf("P0.1 regression: expected ErrRecoveryPersistFailed when directory fsync fails on write, got: %v", dirSyncWriteErr)
	}
	if !op.IsRecoveryRequired() {
		t.Fatal("P0.1 regression: expected in-memory recovery_required=true even when directory fsync fails on write")
	}
	op.SetDirSyncHook(nil)

	// Persist a clean durable marker for ClearRecovery directory fsync fault injection and P1.2 tests
	if err := op.ClearRecovery(); err != nil {
		t.Fatalf("ClearRecovery reset: %v", err)
	}
	if err := op.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Reason:         "initial durable rollback failure",
		TransitionID:   "tx-initial-durable",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
		FailedStep:     "stop_candidate_firc",
	}); err != nil {
		t.Fatalf("seed durable marker tx-initial-durable: %v", err)
	}

	// 4. P1.2: When an active durable marker already exists ("tx-initial-durable") and updating it fails to persist,
	//    in-memory RecoveryMarkerInfo() MUST retain the durable marker's identity ("tx-initial-durable") with LastPersistError
	//    populated so that in-memory state and post-restart state match!
	op.SetDirSyncHook(func(_ string) error {
		return errors.New("injected read-only filesystem during marker update")
	})
	updatePersistErr := op.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Reason:         "subsequent preflight failure",
		TransitionID:   "tx-unpersisted-update",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
		FailedStep:     "preflight_load_tunnels_yaml",
	})
	if !errors.Is(updatePersistErr, ErrRecoveryPersistFailed) {
		t.Fatalf("P1.2: expected ErrRecoveryPersistFailed on failed marker update, got: %v", updatePersistErr)
	}
	memMarker := op.RecoveryMarkerInfo()
	if memMarker == nil || memMarker.TransitionID != "tx-initial-durable" {
		t.Fatalf("P1.2 regression: expected in-memory marker to preserve durable TransitionID \"tx-initial-durable\", got: %+v", memMarker)
	}
	if memMarker.LastPersistError == "" {
		t.Fatalf("P1.2 regression: expected LastPersistError to be populated on preserved marker, got: %+v", memMarker)
	}
	// Restore original file on disk to simulate rename failure before checking restart consistency
	op.SetDirSyncHook(nil)
	_ = op.ClearRecovery()
	_ = op.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Reason:         "initial durable rollback failure",
		TransitionID:   "tx-initial-durable",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
		FailedStep:     "stop_candidate_firc",
	})
	opRestartCheck := NewOperator(Options{ConfigDir: tmpDir})
	if opRestartCheck.RecoveryMarkerInfo() == nil || opRestartCheck.RecoveryMarkerInfo().TransitionID != memMarker.TransitionID {
		t.Fatalf("P1.2 regression: post-restart TransitionID (%+v) does not match preserved in-memory TransitionID (%q)",
			opRestartCheck.RecoveryMarkerInfo(), memMarker.TransitionID)
	}

	// Verify successful update of existing marker records ParentTransitionID and PreviousReason (P1.2)
	if err := op.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Reason:         "updated durable failure",
		TransitionID:   "tx-updated-durable",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
		FailedStep:     "verify_target_firc",
	}); err != nil {
		t.Fatalf("expected marker update to succeed when disk is writable: %v", err)
	}
	updatedMarker := op.RecoveryMarkerInfo()
	if updatedMarker == nil || updatedMarker.TransitionID != "tx-updated-durable" || updatedMarker.ParentTransitionID != "tx-initial-durable" {
		t.Fatalf("P1.2 regression: expected ParentTransitionID=\"tx-initial-durable\" on updated marker, got: %+v", updatedMarker)
	}

	// 5. P0.1 Two-Phase Durable Clear + Restart Regression Test (both Phase 1 and Phase 2 directory fsync failures):
	//    5a. Inject directory fsync failure on Phase 1 (tombstone rename sync):
	op.SetDirSyncHook(func(_ string) error {
		return errors.New("injected phase 1 tombstone directory fsync failure")
	})
	repPhase1Fail, recPhase1Err := op.ReconcileRecovery(ctx, "sing-box")
	if !errors.Is(recPhase1Err, ErrRecoveryPersistFailed) {
		t.Fatalf("P0.1 regression (Phase 1): expected ErrRecoveryPersistFailed when Phase 1 dir fsync fails, got: %v", recPhase1Err)
	}
	if repPhase1Fail != nil {
		t.Fatalf("P0.1 regression (Phase 1): expected nil ReconcileReport when Phase 1 dir fsync fails, got: %+v", repPhase1Fail)
	}
	if !op.IsRecoveryRequired() {
		t.Fatal("P0.1 regression (Phase 1): ClearRecovery cleared in-memory recovery_required despite Phase 1 dir fsync failure")
	}
	// Verify fail-closed tombstone artifact exists on disk after Phase 1 failure
	if _, statErr := os.Stat(op.RecoveryTombstonePath()); statErr != nil {
		t.Fatalf("P0.1 regression (Phase 1): expected recovery tombstone at %s after Phase 1 dir fsync failure, statErr=%v",
			op.RecoveryTombstonePath(), statErr)
	}
	// Construct a brand-new Operator over the same ConfigDir and verify it stays fail-closed after restart!
	restartedAfterPhase1 := NewOperator(Options{ConfigDir: tmpDir})
	if !restartedAfterPhase1.IsRecoveryRequired() {
		t.Fatal("P0.1 restart regression (Phase 1): new Operator after failed Phase 1 ClearRecovery lost recovery_required!")
	}
	if restartedAfterPhase1.RecoveryMarkerInfo() == nil || restartedAfterPhase1.RecoveryMarkerInfo().TransitionID != "tx-updated-durable" {
		t.Fatalf("P0.1 restart regression (Phase 1): unexpected marker after restart: %+v", restartedAfterPhase1.RecoveryMarkerInfo())
	}

	//    5b. Fault-inject tombstone rematerialization errors after Phase 2 final dir sync failure:
	op.SetDirSyncHook(nil)
	errPhase2Final := errors.New("injected phase 2 final unlink directory fsync failure")

	//    5b-1: RestoreTombstoneWrite fails -> returns joined error (Phase 2 + write failure), memory stays fail-closed
	errRestoreWrite := errors.New("injected restore tombstone write failure")
	op.SetRecoveryClearFaultHooks(RecoveryClearFaultHooks{
		Phase2FinalDirSync:    func(_ string) error { return errPhase2Final },
		RestoreTombstoneWrite: func(_ string) error { return errRestoreWrite },
	})
	repRematWrite, errRematWrite := op.ReconcileRecovery(ctx, "sing-box")
	if !errors.Is(errRematWrite, ErrRecoveryPersistFailed) ||
		!strings.Contains(errRematWrite.Error(), errPhase2Final.Error()) ||
		!strings.Contains(errRematWrite.Error(), errRestoreWrite.Error()) {
		t.Fatalf("P0.1 regression (5b-1 write fail): expected joined Phase 2 + restore write error, got: %v", errRematWrite)
	}
	if repRematWrite != nil || !op.IsRecoveryRequired() {
		t.Fatalf("P0.1 regression (5b-1 write fail): expected nil report and recovery_required=true, got rep=%+v req=%v",
			repRematWrite, op.IsRecoveryRequired())
	}

	//    5b-2: RestoreTombstoneSync (file.Sync) fails -> returns joined error (Phase 2 + file.Sync failure), memory stays fail-closed
	errRestoreSync := errors.New("injected restore tombstone file.Sync failure")
	op.SetRecoveryClearFaultHooks(RecoveryClearFaultHooks{
		Phase2FinalDirSync:   func(_ string) error { return errPhase2Final },
		RestoreTombstoneSync: func(_ string) error { return errRestoreSync },
	})
	repRematSync, errRematSync := op.ReconcileRecovery(ctx, "sing-box")
	if !errors.Is(errRematSync, ErrRecoveryPersistFailed) ||
		!strings.Contains(errRematSync.Error(), errPhase2Final.Error()) ||
		!strings.Contains(errRematSync.Error(), errRestoreSync.Error()) {
		t.Fatalf("P0.1 regression (5b-2 file.Sync fail): expected joined Phase 2 + restore file.Sync error, got: %v", errRematSync)
	}
	if repRematSync != nil || !op.IsRecoveryRequired() {
		t.Fatalf("P0.1 regression (5b-2 file.Sync fail): expected nil report and recovery_required=true, got rep=%+v req=%v",
			repRematSync, op.IsRecoveryRequired())
	}

	//    5b-3: RestoreTombstoneDirSync fails -> returns joined error (Phase 2 + restore dir fsync failure), memory stays fail-closed
	errRestoreDirSync := errors.New("injected restore tombstone directory fsync failure")
	op.SetRecoveryClearFaultHooks(RecoveryClearFaultHooks{
		Phase2FinalDirSync:      func(_ string) error { return errPhase2Final },
		RestoreTombstoneDirSync: func(_ string) error { return errRestoreDirSync },
	})
	repRematDirSync, errRematDirSync := op.ReconcileRecovery(ctx, "sing-box")
	if !errors.Is(errRematDirSync, ErrRecoveryPersistFailed) ||
		!strings.Contains(errRematDirSync.Error(), errPhase2Final.Error()) ||
		!strings.Contains(errRematDirSync.Error(), errRestoreDirSync.Error()) {
		t.Fatalf("P0.1 regression (5b-3 dir fsync fail): expected joined Phase 2 + restore dir fsync error, got: %v", errRematDirSync)
	}
	if repRematDirSync != nil || !op.IsRecoveryRequired() {
		t.Fatalf("P0.1 regression (5b-3 dir fsync fail): expected nil report and recovery_required=true, got rep=%+v req=%v",
			repRematDirSync, op.IsRecoveryRequired())
	}
	// Remove un-synced tombstone before testing clean durable rematerialization in 5b-4
	_ = os.Remove(op.RecoveryTombstonePath())

	//    5b-4: Phase 2 final dir sync fails, and restoreRecoveryTombstone succeeds durably ->
	//          verify 0600 permissions on rematerialized tombstone and verify a new Operator sees recovery_required=true!
	op.SetRecoveryClearFaultHooks(RecoveryClearFaultHooks{
		Phase2FinalDirSync: func(_ string) error { return errPhase2Final },
	})
	repPhase2Fail, recPhase2Err := op.ReconcileRecovery(ctx, "sing-box")
	if !errors.Is(recPhase2Err, ErrRecoveryPersistFailed) {
		t.Fatalf("P0.1 regression (Phase 2): expected ErrRecoveryPersistFailed when Phase 2 dir fsync fails, got: %v", recPhase2Err)
	}
	if repPhase2Fail != nil {
		t.Fatalf("P0.1 regression (Phase 2): expected nil ReconcileReport when Phase 2 dir fsync fails, got: %+v", repPhase2Fail)
	}
	if !op.IsRecoveryRequired() {
		t.Fatal("P0.1 regression (Phase 2): ClearRecovery cleared in-memory recovery_required despite Phase 2 dir fsync failure")
	}
	tombInfo, statErr := os.Stat(op.RecoveryTombstonePath())
	if statErr != nil {
		t.Fatalf("P0.1 regression (Phase 2): expected recovery tombstone at %s after Phase 2 dir fsync failure, statErr=%v",
			op.RecoveryTombstonePath(), statErr)
	}
	if runtime.GOOS != "windows" && tombInfo.Mode().Perm() != 0o600 {
		t.Fatalf("P0.1 regression (Phase 2): expected rematerialized tombstone permissions 0600, got %04o", tombInfo.Mode().Perm())
	}
	restartedAfterPhase2 := NewOperator(Options{ConfigDir: tmpDir})
	if !restartedAfterPhase2.IsRecoveryRequired() {
		t.Fatal("P0.1 restart regression (Phase 2): new Operator after failed Phase 2 ClearRecovery lost recovery_required!")
	}
	if restartedAfterPhase2.RecoveryMarkerInfo() == nil || restartedAfterPhase2.RecoveryMarkerInfo().TransitionID != "tx-updated-durable" {
		t.Fatalf("P0.1 restart regression (Phase 2): unexpected marker after restart: %+v", restartedAfterPhase2.RecoveryMarkerInfo())
	}

	//    5c. Remove fault injection, complete recovery clear, and verify both marker & tombstone are absent and new Operator starts clean:
	op.SetDirSyncHook(nil)
	op.SetRecoveryClearFaultHooks(RecoveryClearFaultHooks{})
	if repOK, err := op.ReconcileRecovery(ctx, "sing-box"); err != nil || !repOK.Verified || op.IsRecoveryRequired() {
		t.Fatalf("expected ReconcileRecovery to succeed after clearing fault hooks: rep=%+v err=%v", repOK, err)
	}
	if _, statErr := os.Stat(op.RecoveryMarkerPath()); !os.IsNotExist(statErr) {
		t.Fatalf("expected primary recovery-state.json to be absent after successful clear, statErr=%v", statErr)
	}
	if _, statErr := os.Stat(op.RecoveryTombstonePath()); !os.IsNotExist(statErr) {
		t.Fatalf("expected tombstone recovery-state.json.clearing to be absent after successful clear, statErr=%v", statErr)
	}
	restartedAfterCleanClear := NewOperator(Options{ConfigDir: tmpDir})
	if restartedAfterCleanClear.IsRecoveryRequired() {
		t.Fatal("expected new Operator after successful two-phase clear to have IsRecoveryRequired() == false")
	}
}

func TestReconcileRecoveryPreflightCompensationAndTargetResolution(t *testing.T) {
	mockSrv, handler := newMockFircHandler()
	_ = mockSrv
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tmpDir := t.TempDir()
	fircdBin := filepath.Join(tmpDir, "fircd")
	tunvlessBin := filepath.Join(tmpDir, "tunvless")
	_ = os.WriteFile(fircdBin, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64, "fircd"), 0o755)
	_ = os.WriteFile(tunvlessBin, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64, "tunvless"), 0o755)

	newTestOp := func() *Operator {
		op := NewOperator(Options{
			ConfigDir:   tmpDir,
			FircdBinary: fircdBin,
			TunvlessBin: tunvlessBin,
			InitScript:  filepath.Join(tmpDir, "nonexistent-S99firc"),
			InboundPort: 1099,
		})
		op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
		return op
	}

	op := newTestOp()
	ctx := context.Background()

	runningEngines := map[string]bool{
		"sing-box": true,
		"firc":     false,
	}
	// Simulate partial mutation where settings.json was changed to "firc", but rollback failed so PreviousEngine is "sing-box"
	currentEngine := "firc"
	xhttpEnabled := true

	if err := op.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Reason:         "switch sing-box -> firc failed after settings commit",
		TransitionID:   "tx-p11-1",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
		FailedStep:     "stop_previous_sing-box",
	}); err != nil {
		t.Fatalf("seed recovery marker: %v", err)
	}

	wireHooks := func(targetOp *Operator, failStartFirc, failCommitFirc bool) {
		targetOp.SetLifecycleHooks(
			func(_ context.Context) error {
				if failStartFirc {
					runningEngines["firc"] = true // simulate partial firc start before readiness failure
					return errors.New("fircd readiness failed")
				}
				runningEngines["firc"] = true
				return nil
			},
			func(_ context.Context) error {
				runningEngines["firc"] = false
				return nil
			},
		)
		targetOp.SetDefaultEngineHooks(EngineSwitchHooks{
			GetCurrentEngine: func(_ context.Context) (string, error) { return currentEngine, nil },
			StartTarget: func(_ context.Context, eng string) error {
				runningEngines[eng] = true
				return nil
			},
			StopEngine: func(_ context.Context, eng string) error {
				runningEngines[eng] = false
				return nil
			},
			CommitSettings: func(_ context.Context, eng string) error {
				if failCommitFirc && eng == "firc" {
					return errors.New("disk full committing firc")
				}
				currentEngine = eng
				return nil
			},
			OnSwitched: func(fircSelected bool) {
				xhttpEnabled = fircSelected
			},
		})
	}

	// 1. P1.1: ReconcileRecovery with empty preferredEngine MUST prefer RecoveryMarker.PreviousEngine ("sing-box")
	// over partially mutated currentEngine ("firc")!
	wireHooks(op, false, false)
	rep, err := op.ReconcileRecovery(ctx, "")
	if err != nil {
		t.Fatalf("P1.1 ReconcileRecovery failed: %v", err)
	}
	if rep.TargetEngine != "sing-box" || rep.TargetResolutionSource != "recovery_marker_previous" {
		t.Fatalf("P1.1 regression: expected targetEngine=sing-box and source=recovery_marker_previous, got: %+v", rep)
	}
	if currentEngine != "sing-box" || !runningEngines["sing-box"] || runningEngines["firc"] || xhttpEnabled {
		t.Fatalf("P1.1 regression: unexpected post-reconcile state: current=%s running=%v xhttp=%v", currentEngine, runningEngines, xhttpEnabled)
	}

	// Re-seed recovery marker for P0.2 tests
	reseedMarker := func() {
		if err := op.SetRecoveryRequiredWithMeta(RecoveryMarker{
			Reason:         "pending recovery",
			TransitionID:   "tx-p02",
			TargetEngine:   "firc",
			PreviousEngine: "sing-box",
			FailedStep:     "start_candidate_firc",
		}); err != nil {
			t.Fatalf("reseedMarker: %v", err)
		}
	}

	// 2. P0.2: Invalid YAML preflight failure MUST NOT stop running sing-box engine!
	reseedMarker()
	_ = os.WriteFile(op.TunnelsPath(), []byte("tunnels: [corrupted_yaml"), 0o600)
	if _, err := op.ReconcileRecovery(ctx, "firc"); err == nil || !strings.Contains(err.Error(), "preflight_load_tunnels_yaml") {
		t.Fatalf("P0.2 regression: expected preflight_load_tunnels_yaml error, got: %v", err)
	}
	if !runningEngines["sing-box"] {
		t.Fatal("P0.2 regression: running sing-box was stopped despite invalid tunnels.yaml in preflight!")
	}
	if currentEngine != "sing-box" {
		t.Fatalf("P0.2 regression: currentEngine changed to %q on preflight failure", currentEngine)
	}
	opAfterYAML := newTestOp()
	if !opAfterYAML.IsRecoveryRequired() || opAfterYAML.RecoveryMarkerInfo().PreviousEngine != "sing-box" {
		t.Fatalf("expected recovery marker preserved across restart after preflight YAML failure: %+v", opAfterYAML.RecoveryMarkerInfo())
	}

	// Fix tunnels.yaml
	_ = op.SaveTunnelsFile([]Tunnel{{ID: "t1", Device: "firc0", Enable: true}})
	_ = op.SaveGroupsFile([]Group{{ID: "g1", Name: "G1", Interface: GroupInterface{"firc0"}, Enable: true}})

	// 3. P0.2: Missing binary preflight failure (when no custom startHook is set) MUST NOT stop running sing-box!
	opNoHook := newTestOp()
	opNoHook.fircdBinary = filepath.Join(tmpDir, "missing-fircd")
	opNoHook.SetDefaultEngineHooks(op.DefaultEngineHooks())
	if _, err := opNoHook.ReconcileRecovery(ctx, "firc"); err == nil || !strings.Contains(err.Error(), "preflight_validate_binaries") {
		t.Fatalf("P0.2 regression: expected preflight_validate_binaries error, got: %v", err)
	}
	if !runningEngines["sing-box"] {
		t.Fatal("P0.2 regression: running sing-box was stopped despite missing fircd binary in preflight!")
	}

	// 3b. P0.2 Production-shaped preflight: S99firc initScript EXISTS on disk (0755), startHook == nil,
	//     and fircd / tunvless binary is missing or corrupted -> preflight MUST STILL fail and MUST NOT stop sing-box!
	existingInitScript := filepath.Join(tmpDir, "S99firc-existing")
	if err := os.WriteFile(existingInitScript, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write existing S99firc: %v", err)
	}
	opProdMissingBin := NewOperator(Options{
		ConfigDir:   tmpDir,
		FircdBinary: filepath.Join(tmpDir, "missing-prod-fircd"),
		TunvlessBin: tunvlessBin,
		InitScript:  existingInitScript,
		InboundPort: 1099,
	})
	opProdMissingBin.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	opProdMissingBin.SetDefaultEngineHooks(op.DefaultEngineHooks())
	if _, err := opProdMissingBin.ReconcileRecovery(ctx, "firc"); err == nil || !strings.Contains(err.Error(), "preflight_validate_binaries") {
		t.Fatalf("P0.2 production preflight regression: expected preflight_validate_binaries when S99firc exists and fircd is missing, got: %v", err)
	}
	if !runningEngines["sing-box"] {
		t.Fatal("P0.2 production preflight regression: running sing-box was stopped when S99firc existed and fircd was missing!")
	}

	corruptELF := filepath.Join(tmpDir, "corrupt-fircd")
	if err := os.WriteFile(corruptELF, []byte("not-an-elf-binary-at-all"), 0o755); err != nil {
		t.Fatalf("write corruptELF: %v", err)
	}
	opProdBadELF := NewOperator(Options{
		ConfigDir:   tmpDir,
		FircdBinary: corruptELF,
		TunvlessBin: tunvlessBin,
		InitScript:  existingInitScript,
		InboundPort: 1099,
	})
	opProdBadELF.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	opProdBadELF.SetDefaultEngineHooks(op.DefaultEngineHooks())
	if _, err := opProdBadELF.ReconcileRecovery(ctx, "firc"); err == nil || !strings.Contains(err.Error(), "preflight_validate_elf") {
		t.Fatalf("P0.2 production preflight regression: expected preflight_validate_elf when S99firc exists and fircd is non-ELF, got: %v", err)
	}
	if !runningEngines["sing-box"] {
		t.Fatal("P0.2 production preflight regression: running sing-box was stopped when S99firc existed and fircd was non-ELF!")
	}

	// 4. P0.2: Destructive phase failure during target start/readiness MUST compensate by stopping partial target and restarting sing-box!
	op = newTestOp()
	wireHooks(op, true, false)
	if _, err := op.ReconcileRecovery(ctx, "firc"); err == nil {
		t.Fatal("expected ReconcileRecovery to fail when firc readiness fails")
	}
	if !runningEngines["sing-box"] || runningEngines["firc"] {
		t.Fatalf("P0.2 regression: compensation did not restore sing-box and stop partial firc: running=%v", runningEngines)
	}
	if currentEngine != "sing-box" {
		t.Fatalf("P0.2 regression: expected currentEngine=sing-box after compensation, got %q", currentEngine)
	}
	opAfterStartFail := newTestOp()
	if !opAfterStartFail.IsRecoveryRequired() || opAfterStartFail.RecoveryMarkerInfo().PreviousEngine != "sing-box" {
		t.Fatalf("expected durable marker with PreviousEngine=sing-box after compensated start failure: %+v", opAfterStartFail.RecoveryMarkerInfo())
	}

	// 5. P0.2: Destructive phase failure during CommitSettings MUST compensate by stopping target firc and restarting sing-box!
	op = newTestOp()
	wireHooks(op, false, true)
	if _, err := op.ReconcileRecovery(ctx, "firc"); err == nil {
		t.Fatal("expected ReconcileRecovery to fail when CommitSettings fails")
	}
	if !runningEngines["sing-box"] || runningEngines["firc"] {
		t.Fatalf("P0.2 regression: compensation after CommitSettings failure did not restore sing-box and stop firc: running=%v", runningEngines)
	}
	if currentEngine != "sing-box" {
		t.Fatalf("P0.2 regression: expected currentEngine=sing-box after CommitSettings compensation, got %q", currentEngine)
	}
	opAfterCommitFail := newTestOp()
	if !opAfterCommitFail.IsRecoveryRequired() || opAfterCommitFail.RecoveryMarkerInfo().PreviousEngine != "sing-box" {
		t.Fatalf("expected durable marker with PreviousEngine=sing-box after compensated commit failure: %+v", opAfterCommitFail.RecoveryMarkerInfo())
	}

	// 6. P0.3: Partially successful CommitSettings (mutates currentEngine = "firc" on disk/memory and then returns an error)
	//    MUST be compensated in BOTH ReconcileRecovery and SwitchEngine by re-reading GetCurrentEngine and restoring "sing-box"!
	partialCommitCalls := 0
	opPartial := newTestOp()
	opPartial.SetLifecycleHooks(
		func(_ context.Context) error {
			runningEngines["firc"] = true
			return nil
		},
		func(_ context.Context) error {
			runningEngines["firc"] = false
			return nil
		},
	)
	opPartial.SetDefaultEngineHooks(EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) { return currentEngine, nil },
		StartTarget: func(_ context.Context, eng string) error {
			runningEngines[eng] = true
			return nil
		},
		StopEngine: func(_ context.Context, eng string) error {
			runningEngines[eng] = false
			return nil
		},
		CommitSettings: func(_ context.Context, eng string) error {
			partialCommitCalls++
			currentEngine = eng // Mutates settings BEFORE returning error!
			if eng == "firc" {
				return errors.New("partial commit: settings written to firc but post-commit sync failed")
			}
			return nil
		},
		OnSwitched: func(fircSelected bool) {
			xhttpEnabled = fircSelected
		},
	})

	// 6a. ReconcileRecovery with partially successful CommitSettings("firc")
	if _, err := opPartial.ReconcileRecovery(ctx, "firc"); err == nil {
		t.Fatal("P0.3: expected ReconcileRecovery to fail on partial CommitSettings error")
	}
	if currentEngine != "sing-box" {
		t.Fatalf("P0.3 regression in ReconcileRecovery: partially mutated currentEngine was NOT restored to sing-box (got %q)", currentEngine)
	}
	if !runningEngines["sing-box"] || runningEngines["firc"] {
		t.Fatalf("P0.3 regression in ReconcileRecovery: unexpected runningEngines=%v", runningEngines)
	}

	// 6b. SwitchEngine with partially successful CommitSettings("firc")
	if err := opPartial.ClearRecovery(); err != nil {
		t.Fatalf("ClearRecovery before 6b: %v", err)
	}
	if err := opPartial.SwitchEngine(ctx, "firc"); err == nil {
		t.Fatal("P0.3: expected SwitchEngine to fail on partial CommitSettings error")
	}
	if currentEngine != "sing-box" {
		t.Fatalf("P0.3 regression in SwitchEngine: partially mutated currentEngine was NOT restored to sing-box (got %q)", currentEngine)
	}
	if !runningEngines["sing-box"] || runningEngines["firc"] {
		t.Fatalf("P0.3 regression in SwitchEngine: unexpected runningEngines=%v", runningEngines)
	}
}

func TestNativeYAMLFormatCompatibility(t *testing.T) {
	tmpDir := t.TempDir()
	op := NewOperator(Options{ConfigDir: tmpDir})

	nativeTunnelsYAML := `tunnels:
- id: "tunnel1"
  device: "tunvless0"
  enable: true
  uplink: "auto"
  sources:
  - id: "498b205d"
    subscription:
      name: "ABV"
      url: "https://example.com/sub.json"
      interval: 21600s
  active: 1
  by: "connection"
  interval: 60s
  silence: 20s
  advanced:
    timeout: 8s
    insecure: false
    ca: ""
    fingerprint: ""
    kernelTcp: false
`
	if err := os.WriteFile(op.TunnelsPath(), []byte(nativeTunnelsYAML), 0o600); err != nil {
		t.Fatalf("write tunnels.yaml: %v", err)
	}
	tuns, err := op.LoadTunnelsFile()
	if err != nil {
		t.Fatalf("LoadTunnelsFile failed on native fircd YAML: %v", err)
	}
	if len(tuns) != 1 || tuns[0].ID != "tunnel1" || tuns[0].Uplink.Kind != "auto" ||
		len(tuns[0].Sources) != 1 || tuns[0].Sources[0].Kind != "subscription" ||
		tuns[0].Sources[0].Interval != 21600 || tuns[0].Interval != 60 ||
		tuns[0].Silence != 20 || tuns[0].Advanced.Timeout != 8 {
		t.Fatalf("unexpected parsed tunnel from native YAML: %+v", tuns)
	}

	nativeGroupsYAML := `configVersion: 0.7.0
groups:
  - id: d663876a
    name: Example
    interface: nwg0
    enable: false
    rules:
      - id: 6f34ee91
        type: wildcard
        rule: '*wildcard.example.com'
        enable: true
`
	if err := os.WriteFile(op.GroupsPath(), []byte(nativeGroupsYAML), 0o644); err != nil {
		t.Fatalf("write groups.yaml: %v", err)
	}
	grps, err := op.LoadGroupsFile()
	if err != nil {
		t.Fatalf("LoadGroupsFile failed on native fircd YAML: %v", err)
	}
	if len(grps) != 1 || grps[0].ID != "d663876a" || len(grps[0].Interface) != 1 || grps[0].Interface[0] != "nwg0" {
		t.Fatalf("unexpected parsed group from native YAML: %+v", grps)
	}

	// Empty lists must also round-trip cleanly without unmarshal error
	if err := op.SaveTunnelsFile([]Tunnel{}); err != nil {
		t.Fatalf("SaveTunnelsFile(empty): %v", err)
	}
	if emptyTuns, err := op.LoadTunnelsFile(); err != nil || len(emptyTuns) != 0 {
		t.Fatalf("LoadTunnelsFile(empty) failed: tuns=%v err=%v", emptyTuns, err)
	}
	if err := op.SaveGroupsFile([]Group{}); err != nil {
		t.Fatalf("SaveGroupsFile(empty): %v", err)
	}
	if emptyGrps, err := op.LoadGroupsFile(); err != nil || len(emptyGrps) != 0 {
		t.Fatalf("LoadGroupsFile(empty) failed: grps=%v err=%v", emptyGrps, err)
	}
}

func TestTunnelUplinkDefaultsToAutoWhenOmitted(t *testing.T) {
	tmpDir := t.TempDir()
	op := NewOperator(Options{ConfigDir: tmpDir})

	tunnelsYAML := `tunnels:
- id: "tunnel-omitted"
  device: "tunvless0"
  enable: true
  sources: []
  advanced: {}
`
	if err := os.WriteFile(op.TunnelsPath(), []byte(tunnelsYAML), 0o600); err != nil {
		t.Fatalf("write tunnels.yaml: %v", err)
	}
	tuns, err := op.LoadTunnelsFile()
	if err != nil {
		t.Fatalf("LoadTunnelsFile: %v", err)
	}
	if len(tuns) != 1 || tuns[0].Uplink.Kind != "auto" || tuns[0].Uplink.Ref != "" {
		t.Fatalf("expected omitted YAML uplink to default to auto, got %+v", tuns)
	}

	rawJSON, err := json.Marshal(Tunnel{ID: "zero-uplink", Device: "tunvless1", Enable: true})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(rawJSON), `"uplink":{"kind":"auto"}`) {
		t.Fatalf("expected zero TunnelUplink to serialize as auto in JSON, got %s", rawJSON)
	}
}

func TestFirc060ModelsAutolistAndCanonicalPersistence(t *testing.T) {
	var (
		recheckCalled    int32
		lastProbedHost   string
		lastAddedDomain  string
		lastDismissedDom string
		lastDismissedIP  string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(NetfilterHealth{Ok: true})
	})

	// Simulate fircd 0.6.0 pruning stale node keys from order/exclude on PUT /api/v1/tunnels
	mux.HandleFunc("/api/v1/tunnels", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"tunnels": []Tunnel{}})
		case http.MethodPut:
			var req struct {
				Tunnels []Tunnel `json:"tunnels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for i := range req.Tunnels {
				// Prune "sub1:stale-node" just like fircd 0.6.0 prune_tunnel_node_keys
				var prunedOrder []string
				for _, k := range req.Tunnels[i].Order {
					if k != "sub1:stale-node" {
						prunedOrder = append(prunedOrder, k)
					}
				}
				req.Tunnels[i].Order = prunedOrder
			}
			_ = json.NewEncoder(w).Encode(PutTunnelsResponse{
				Tunnels:   req.Tunnels,
				Restarted: []string{"tunvless0"},
			})
		}
	})

	mux.HandleFunc("/api/v1/tunnels/state", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"tunnels": []TunnelState{
				{
					ID:     "tun1",
					Device: "tunvless0",
					Status: "up",
					Nodes: []NodeState{
						{Key: "sub1:nl-1", Name: "NL-1", Source: "Sub", State: "active", V6: true},
					},
					Subscriptions: []SubState{
						{
							Name:  "Sub",
							Nodes: 1,
							Usage: &SubUsage{
								Used:   629145600,
								Total:  10737418240,
								Expire: 1893456000,
							},
						},
					},
				},
			},
		})
	})

	// Simulate fircd 0.6.0 carrying over autolist / autolistByDomain on PUT /api/v1/groups
	mux.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"groups": []Group{}})
		case http.MethodPut:
			var req struct {
				Groups []Group `json:"groups"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for gi := range req.Groups {
				for ri := range req.Groups[gi].Rules {
					if req.Groups[gi].Rules[ri].Rule == "blocked.example.com" {
						req.Groups[gi].Rules[ri].Autolist = "assist"
						req.Groups[gi].Rules[ri].AutolistByDomain = true
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"groups": req.Groups})
		}
	})

	buildAutolistResp := func() AutolistStatus {
		return AutolistStatus{
			Enabled:     true,
			Running:     true,
			Group:       "g1",
			Mode:        "assist",
			FailuresDay: 7,
			Listed:      1,
			Candidates: []AutolistCandidate{
				{
					Domain: "blocked.example.com",
					Host:   "api.blocked.example.com",
					Fails:  3,
					State:  "failed",
					Why:    "tls_reset",
				},
			},
			Addresses: []AutolistAddress{
				{
					Address: "203.0.113.10",
					Proto:   "tcp",
					Port:    443,
					Name:    "api.blocked.example.com",
					Why:     "syn_timeout",
					Outcome: "bypass",
					Fails:   3,
				},
			},
		}
	}

	mux.HandleFunc("/api/v1/system/autolist", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(buildAutolistResp())
	})
	mux.HandleFunc("/api/v1/system/autolist/check", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&recheckCalled, 1)
		_ = json.NewEncoder(w).Encode(buildAutolistResp())
	})
	mux.HandleFunc("/api/v1/system/autolist/probe", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Host string `json:"host"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		lastProbedHost = body.Host
		_ = json.NewEncoder(w).Encode(buildAutolistResp())
	})
	mux.HandleFunc("/api/v1/system/autolist/candidates/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/system/autolist/candidates/")
		if strings.HasSuffix(rest, "/add") {
			lastAddedDomain = strings.TrimSuffix(rest, "/add")
			_ = json.NewEncoder(w).Encode(buildAutolistResp())
			return
		}
		if strings.HasSuffix(rest, "/dismiss") {
			lastDismissedDom = strings.TrimSuffix(rest, "/dismiss")
			_ = json.NewEncoder(w).Encode(buildAutolistResp())
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/api/v1/system/autolist/addresses/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/system/autolist/addresses/")
		if strings.HasSuffix(rest, "/dismiss") {
			lastDismissedIP = strings.TrimSuffix(rest, "/dismiss")
			_ = json.NewEncoder(w).Encode(buildAutolistResp())
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmpDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:   tmpDir,
		InboundPort: 1099,
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	ctx := context.Background()

	// 1. Verify NodeState.V6 and SubState.Usage from /api/v1/tunnels/state
	states, err := op.Client().GetTunnelsState(ctx)
	if err != nil {
		t.Fatalf("GetTunnelsState: %v", err)
	}
	if len(states) != 1 || len(states[0].Nodes) != 1 || !states[0].Nodes[0].V6 {
		t.Fatalf("expected NodeState.V6=true, got %+v", states)
	}
	if len(states[0].Subscriptions) != 1 || states[0].Subscriptions[0].Usage == nil || states[0].Subscriptions[0].Usage.Used != 629145600 {
		t.Fatalf("expected SubState.Usage with Used=629145600, got %+v", states[0].Subscriptions)
	}

	// 2. Verify ApplyTunnels persists pruned order and whole-group inbound "*"
	appliedTuns, err := op.ApplyTunnels(ctx, []Tunnel{
		{
			ID:     "tun1",
			Device: "tunvless0",
			Enable: true,
			Order:  []string{"sub1:nl-1", "sub1:stale-node"},
			Inbounds: []TunnelInbound{
				{Port: 1099, Listen: "127.0.0.1", Node: TunnelNodePool},
			},
		},
	}, false)
	if err != nil {
		t.Fatalf("ApplyTunnels: %v", err)
	}
	if !reflect.DeepEqual(appliedTuns.Tunnels[0].Order, []string{"sub1:nl-1"}) {
		t.Fatalf("expected pruned order [sub1:nl-1] in ApplyTunnels response, got %+v", appliedTuns.Tunnels[0].Order)
	}
	diskTuns, err := op.LoadTunnelsFile()
	if err != nil {
		t.Fatalf("LoadTunnelsFile: %v", err)
	}
	if !reflect.DeepEqual(diskTuns[0].Order, []string{"sub1:nl-1"}) {
		t.Fatalf("expected pruned order [sub1:nl-1] persisted on disk, got %+v", diskTuns[0].Order)
	}
	if len(diskTuns[0].Inbounds) == 0 || diskTuns[0].Inbounds[0].Node != "*" {
		t.Fatalf("expected whole-group inbound node '*' persisted on disk, got %+v", diskTuns[0].Inbounds)
	}

	// 3. Verify ApplyGroups persists autolist / autolistByDomain returned by fircd
	appliedGrps, err := op.ApplyGroups(ctx, []Group{
		{
			ID:        "g1",
			Name:      "Proxy",
			Interface: GroupInterface{"tunvless0"},
			Enable:    true,
			Rules: []Rule{
				{ID: "r1", Type: "namespace", Rule: "blocked.example.com", Enable: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("ApplyGroups: %v", err)
	}
	if len(appliedGrps) != 1 || appliedGrps[0].Rules[0].Autolist != "assist" || !appliedGrps[0].Rules[0].AutolistByDomain {
		t.Fatalf("expected autolist markers in ApplyGroups response, got %+v", appliedGrps)
	}
	diskGrps, err := op.LoadGroupsFile()
	if err != nil {
		t.Fatalf("LoadGroupsFile: %v", err)
	}
	if diskGrps[0].Rules[0].Autolist != "assist" || !diskGrps[0].Rules[0].AutolistByDomain {
		t.Fatalf("expected autolist markers persisted on disk, got %+v", diskGrps[0].Rules[0])
	}

	// 4. Verify Autolist client methods
	st, err := op.Client().GetAutolist(ctx)
	if err != nil || !st.Enabled || len(st.Candidates) != 1 || st.Candidates[0].Domain != "blocked.example.com" {
		t.Fatalf("GetAutolist failed: st=%+v err=%v", st, err)
	}
	if err := op.Client().RecheckAutolist(ctx); err != nil || atomic.LoadInt32(&recheckCalled) != 1 {
		t.Fatalf("RecheckAutolist failed: err=%v called=%d", err, recheckCalled)
	}
	if _, err := op.Client().ProbeAutolistHost(ctx, "test.example.org"); err != nil || lastProbedHost != "test.example.org" {
		t.Fatalf("ProbeAutolistHost failed: err=%v host=%q", err, lastProbedHost)
	}
	if err := op.Client().AddAutolistCandidate(ctx, "blocked.example.com"); err != nil || lastAddedDomain != "blocked.example.com" {
		t.Fatalf("AddAutolistCandidate failed: err=%v dom=%q", err, lastAddedDomain)
	}
	if err := op.Client().DismissAutolistCandidate(ctx, "blocked.example.com"); err != nil || lastDismissedDom != "blocked.example.com" {
		t.Fatalf("DismissAutolistCandidate failed: err=%v dom=%q", err, lastDismissedDom)
	}
	if err := op.Client().DismissAutolistAddress(ctx, "203.0.113.10", "tcp", 443); err != nil || lastDismissedIP != "203.0.113.10" {
		t.Fatalf("DismissAutolistAddress failed: err=%v ip=%q", err, lastDismissedIP)
	}
}

func TestAddConfirmedCandidateTransactionAndConcurrency(t *testing.T) {
	mock, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tmpDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:   tmpDir,
		InboundPort: 1099,
	})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	ctx := context.Background()

	var nfMu sync.Mutex
	var nfState []Group
	var nfCalls int
	var failNfOnce bool
	op.SetNetfilterStrictHook(func(_ context.Context, _ ExtConfig, groups []Group) error {
		nfMu.Lock()
		defer nfMu.Unlock()
		nfCalls++
		if failNfOnce {
			failNfOnce = false
			return errors.New("injected strict netfilter failure")
		}
		nfState = cloneGroups(groups)
		return nil
	})

	initialGroups := []Group{
		{
			ID:        "g1",
			Name:      "Proxy",
			Interface: GroupInterface{"tunvless0"},
			Enable:    true,
			Rules: []Rule{
				{Type: "domain", Rule: "initial.example.com", Enable: true},
			},
		},
	}
	if _, err := op.ApplyGroups(ctx, initialGroups); err != nil {
		t.Fatalf("seed ApplyGroups: %v", err)
	}

	assertRulesEqual := func(step string, wantRules []string) {
		t.Helper()
		diskGrps, err := op.LoadGroupsFile()
		if err != nil {
			t.Fatalf("%s: LoadGroupsFile: %v", step, err)
		}
		mock.mu.Lock()
		rtGrps := cloneGroups(mock.groups)
		mock.mu.Unlock()
		nfMu.Lock()
		curNf := cloneGroups(nfState)
		nfMu.Unlock()

		extract := func(grps []Group) []string {
			if len(grps) != 1 {
				t.Fatalf("%s: expected 1 group, got %d", step, len(grps))
			}
			out := make([]string, 0, len(grps[0].Rules))
			for _, r := range grps[0].Rules {
				out = append(out, r.Rule)
			}
			return out
		}
		if got := extract(diskGrps); !reflect.DeepEqual(got, wantRules) {
			t.Fatalf("%s: disk rules=%v, want %v", step, got, wantRules)
		}
		if got := extract(rtGrps); !reflect.DeepEqual(got, wantRules) {
			t.Fatalf("%s: runtime rules=%v, want %v", step, got, wantRules)
		}
		if got := extract(curNf); !reflect.DeepEqual(got, wantRules) {
			t.Fatalf("%s: netfilter rules=%v, want %v", step, got, wantRules)
		}
	}

	// 1. Validation & recovery_required guards must not mutate disk, runtime, or netfilter.
	if _, _, err := op.AddConfirmedCandidate(ctx, "", "g1"); !errors.Is(err, ErrCandidateDomainRequired) {
		t.Fatalf("expected ErrCandidateDomainRequired, got %v", err)
	}
	if _, _, err := op.AddConfirmedCandidate(ctx, "no-group.example.com", ""); !errors.Is(err, ErrCandidateGroupRequired) {
		t.Fatalf("expected ErrCandidateGroupRequired, got %v", err)
	}
	if _, _, err := op.AddConfirmedCandidate(ctx, "unknown-group.example.com", "missing"); !errors.Is(err, ErrCandidateGroupNotFound) {
		t.Fatalf("expected ErrCandidateGroupNotFound, got %v", err)
	}
	_ = op.SetRecoveryRequired("simulated recovery lock")
	if _, _, err := op.AddConfirmedCandidate(ctx, "blocked-by-recovery.example.com", "g1"); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got %v", err)
	}
	if err := op.ClearRecovery(); err != nil {
		t.Fatalf("ClearRecovery: %v", err)
	}
	assertRulesEqual("after_validation_and_recovery_guards", []string{"initial.example.com"})

	// 2. Runtime GetGroups read failure must fail immediately and never fall back to stale disk.
	mock.mu.Lock()
	mock.failGetGroups = true
	mock.mu.Unlock()
	if _, _, err := op.AddConfirmedCandidate(ctx, "read-fail.example.com", "g1"); err == nil {
		t.Fatalf("expected error on runtime GetGroups failure")
	}
	mock.mu.Lock()
	mock.failGetGroups = false
	mock.mu.Unlock()
	assertRulesEqual("after_get_groups_failure", []string{"initial.example.com"})

	// 3. Disk SaveGroupsFile failure must return error and leave disk, runtime, and netfilter untouched.
	op.SetGroupsSaveFaultHook(func(_ []Group) error {
		return errors.New("injected disk write failure")
	})
	if _, _, err := op.AddConfirmedCandidate(ctx, "disk-fail.example.com", "g1"); err == nil {
		t.Fatalf("expected error on SaveGroupsFile failure")
	}
	op.SetGroupsSaveFaultHook(nil)
	assertRulesEqual("after_disk_save_failure", []string{"initial.example.com"})

	// 4. Runtime PutGroups failure must roll back disk, runtime, and netfilter to previous state.
	mock.mu.Lock()
	mock.failGroupsOnce = true
	mock.mu.Unlock()
	if _, _, err := op.AddConfirmedCandidate(ctx, "put-fail.example.com", "g1"); err == nil {
		t.Fatalf("expected error on PutGroups failure")
	}
	assertRulesEqual("after_put_groups_failure_rollback", []string{"initial.example.com"})

	// 5. Strict netfilter failure must roll back disk, runtime, and netfilter to previous state.
	nfMu.Lock()
	failNfOnce = true
	nfMu.Unlock()
	if _, _, err := op.AddConfirmedCandidate(ctx, "nf-fail.example.com", "g1"); err == nil {
		t.Fatalf("expected error on netfilter strict failure")
	}
	assertRulesEqual("after_netfilter_failure_rollback", []string{"initial.example.com"})

	// 6. Deterministic barrier test for two concurrent AddConfirmedCandidate calls into the same group:
	// First call pauses inside GetGroups while holding mutationMu; second call is launched while first is
	// paused and must serialize behind mutationMu so both rules are preserved.
	firstInGetGroups := make(chan struct{})
	releaseFirstGetGroups := make(chan struct{})
	var getHookOnce sync.Once
	var inFlightGetGroups int32
	var maxInFlightGetGroups int32

	mock.mu.Lock()
	mock.onGetGroups = func() {
		cur := atomic.AddInt32(&inFlightGetGroups, 1)
		for {
			prev := atomic.LoadInt32(&maxInFlightGetGroups)
			if cur <= prev || atomic.CompareAndSwapInt32(&maxInFlightGetGroups, prev, cur) {
				break
			}
		}
		getHookOnce.Do(func() {
			close(firstInGetGroups)
			<-releaseFirstGetGroups
		})
		atomic.AddInt32(&inFlightGetGroups, -1)
	}
	mock.mu.Unlock()

	errCh := make(chan error, 2)
	secondStarted := make(chan struct{})
	go func() {
		_, _, err := op.AddConfirmedCandidate(ctx, "alpha.example.com", "g1")
		errCh <- err
	}()
	<-firstInGetGroups
	go func() {
		close(secondStarted)
		_, _, err := op.AddConfirmedCandidate(ctx, "beta.example.com", "g1")
		errCh <- err
	}()
	<-secondStarted
	// Yield briefly while second goroutine is parked on mutationMu before releasing the first GetGroups barrier.
	time.Sleep(25 * time.Millisecond)
	close(releaseFirstGetGroups)

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent AddConfirmedCandidate #%d failed: %v", i+1, err)
		}
	}
	mock.mu.Lock()
	mock.onGetGroups = nil
	mock.mu.Unlock()

	if gotMax := atomic.LoadInt32(&maxInFlightGetGroups); gotMax != 1 {
		t.Fatalf("expected serialized GetGroups under mutationMu (maxInFlight=1), got %d", gotMax)
	}
	assertRulesEqual("after_concurrent_adds", []string{
		"initial.example.com",
		"alpha.example.com",
		"beta.example.com",
	})
}

func TestClientProbeTunnelKeysAndProgress(t *testing.T) {
	var seenMethod string
	var seenKeys []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/tunnels/probe", func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			hs := 12
			fb := 28
			_ = json.NewEncoder(w).Encode(ProbeProgressResponse{
				Total: 4,
				Nodes: []ProbeNodeResult{
					{Key: "0000000a:N1", Ok: true, HandshakeMs: &hs, FirstByteMs: &fb},
				},
			})
			return
		}
		var req struct {
			ID     string   `json:"id"`
			Tunnel *Tunnel  `json:"tunnel"`
			Keys   []string `json:"keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		seenKeys = append([]string(nil), req.Keys...)
		hs := 15
		fb := 35
		_ = json.NewEncoder(w).Encode(ProbeResponse{
			Nodes: []ProbeNodeResult{
				{Key: "0000000a:N2", Ok: true, HandshakeMs: &hs, FirstByteMs: &fb},
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewHTTPClient(srv.URL, srv.Client())
	ctx := context.Background()

	prog, err := c.GetTunnelProbeProgress(ctx)
	if err != nil {
		t.Fatalf("GetTunnelProbeProgress: %v", err)
	}
	if seenMethod != http.MethodGet || prog.Total != 4 || len(prog.Nodes) != 1 || prog.Nodes[0].Key != "0000000a:N1" {
		t.Fatalf("unexpected progress: method=%s prog=%+v", seenMethod, prog)
	}

	res, err := c.ProbeTunnelKeys(ctx, "tun-1", nil, []string{"0000000a:N2"})
	if err != nil {
		t.Fatalf("ProbeTunnelKeys: %v", err)
	}
	if seenMethod != http.MethodPost || !reflect.DeepEqual(seenKeys, []string{"0000000a:N2"}) {
		t.Fatalf("unexpected probe POST keys: method=%s seenKeys=%+v", seenMethod, seenKeys)
	}
	if len(res.Nodes) != 1 || res.Nodes[0].Key != "0000000a:N2" {
		t.Fatalf("unexpected probe result: %+v", res)
	}
}

func TestGroupsFile_KeepsCloudflare(t *testing.T) {
	op := NewOperator(Options{ConfigDir: filepath.Join(t.TempDir(), "firc")})
	if err := os.MkdirAll(op.configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yamlIn := "configVersion: 0.7.0\ngroups:\n  - id: g1\n    name: CF\n    interface: nwg0\n    enable: true\n    cloudflare: true\n"
	if err := os.WriteFile(op.GroupsPath(), []byte(yamlIn), 0o644); err != nil {
		t.Fatal(err)
	}
	grps, err := op.LoadGroupsFile()
	if err != nil || len(grps) != 1 || !grps[0].Cloudflare {
		t.Fatalf("load: grps=%+v err=%v", grps, err)
	}
	if err := op.SaveGroupsFile(grps); err != nil {
		t.Fatal(err)
	}
	if again, err := op.LoadGroupsFile(); err != nil || len(again) != 1 || !again[0].Cloudflare {
		t.Fatalf("after save: grps=%+v err=%v", again, err)
	}

	var g Group
	if err := json.Unmarshal([]byte(`{"id":"g1","name":"CF","interface":"nwg0","enable":true,"cloudflare":true}`), &g); err != nil || !g.Cloudflare {
		t.Fatalf("json in: %+v err=%v", g, err)
	}
	if out, _ := json.Marshal(g); !strings.Contains(string(out), `"cloudflare":true`) {
		t.Fatalf("json out lost cloudflare: %s", out)
	}
}
