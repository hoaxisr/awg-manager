//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/testutil"
)

// Self-exec fake awgm-box/sing-box binary hook so Process.matchesBinary (/proc/<pid>/cmdline
// argv0 basename == "awgm-box"), `version`, `check`, and
// Clash API readiness probing work without an external awgm-box executable.
func init() {
	base := filepath.Base(os.Args[0])
	if base != "awgm-box" && base != "sing-box" {
		return
	}
	sub := ""
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}
	switch sub {
	case "version":
		fmt.Println("sing-box version 1.14.0")
		os.Exit(0)
	case "check":
		os.Exit(0)
	case "run":
		configDir := ""
		for i := 2; i < len(os.Args)-1; i++ {
			if os.Args[i] == "-C" || os.Args[i] == "-D" {
				configDir = os.Args[i+1]
				break
			}
		}
		clashAddr := strings.TrimSpace(os.Getenv("AWGM_TEST_SINGBOX_CLASH_ADDR"))
		if configDir != "" {
			if raw, err := os.ReadFile(filepath.Join(configDir, "00-base.json")); err == nil {
				var cfg struct {
					Experimental struct {
						ClashAPI struct {
							ExternalController string `json:"external_controller"`
						} `json:"clash_api"`
					} `json:"experimental"`
				}
				if json.Unmarshal(raw, &cfg) == nil && strings.TrimSpace(cfg.Experimental.ClashAPI.ExternalController) != "" {
					clashAddr = strings.TrimSpace(cfg.Experimental.ClashAPI.ExternalController)
				}
			}
		}
		var srv *http.Server
		if clashAddr != "" {
			ln, err := net.Listen("tcp", clashAddr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "FATAL[0000] clash listen %s: %v\n", clashAddr, err)
				os.Exit(1)
			}
			srv = &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"version":"1.14.0","proxies":{}}`))
				}),
			}
			go func() { _ = srv.Serve(ln) }()
		}
		_, _ = fmt.Fprintln(os.Stderr, "INFO[0000] sing-box started")

		sigCh := make(chan os.Signal, 4)
		signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
		for sig := range sigCh {
			if sig == syscall.SIGHUP {
				continue
			}
			if srv != nil {
				shutCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = srv.Shutdown(shutCtx)
				cancel()
			}
			os.Exit(0)
		}
	default:
		os.Exit(0)
	}
}

type lifecycleNoopWANCollector struct{}

func (lifecycleNoopWANCollector) Collect(_ context.Context) ([]string, error) {
	return []string{"203.0.113.2/32"}, nil
}

type faultyRouterCoord struct {
	inner      *router.ServiceImpl
	failPark   atomic.Bool
	failResume atomic.Bool
}

func (f *faultyRouterCoord) ParkForExternalEngine(ctx context.Context) error {
	if f.failPark.Load() {
		return errors.New("injected router park failure")
	}
	return f.inner.ParkForExternalEngine(ctx)
}

func (f *faultyRouterCoord) ResumeFromExternalEngine(ctx context.Context) error {
	if f.failResume.Load() {
		return errors.New("injected router resume failure")
	}
	return f.inner.ResumeFromExternalEngine(ctx)
}

type lifecycleHarness struct {
	t             *testing.T
	dataDir       string
	singboxDir    string
	clashPort     int
	app           *app
	singboxH      *api.SingboxHandler
	fircSrv       *httptest.Server
	fircRunning   *atomic.Bool
	failFircStart atomic.Bool
	routerFault   *faultyRouterCoord
	mux           *http.ServeMux
	closed        bool
}

func seedLifecycleDiskState(t *testing.T, rootDir string, clashPort int) (dataDir, singboxDir string) {
	t.Helper()
	dataDir = filepath.Join(rootDir, "data")
	singboxDir = filepath.Join(rootDir, "singbox")
	cfgDir := filepath.Join(singboxDir, "config.d")
	fircDir := filepath.Join(dataDir, "firc")
	for _, d := range []string{dataDir, cfgDir, fircDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	sbBin := filepath.Join(singboxDir, "awgm-box")
	if err := os.Symlink(selfExe, sbBin); err != nil {
		t.Fatalf("symlink stub awgm-box: %v", err)
	}

	store := storage.NewSettingsStore(dataDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("seed settings load: %v", err)
	}
	if err := store.Update(func(s *storage.Settings) error {
		s.CreateNDMSProxyForSingbox = false
		s.SingboxClashPort = clashPort
		s.SingboxRouter = storage.SingboxRouterSettings{
			Enabled:       true,
			PolicyName:    "Policy0",
			DeviceMode:    "all",
			RoutingMode:   "tproxy",
			WANAutoDetect: true,
			RoutingEngine: "sing-box",
		}
		return nil
	}); err != nil {
		t.Fatalf("seed settings update: %v", err)
	}

	tunnelsJSON := `{"outbounds":[{"type":"vless","tag":"tun-a","server":"198.51.100.10","server_port":443}]}` + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "10-tunnels.json"), []byte(tunnelsJSON), 0600); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fircDir, "tunnels.yaml"), []byte("[]\n"), 0600); err != nil {
		t.Fatalf("write empty firc tunnels.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fircDir, "groups.yaml"), []byte("[]\n"), 0600); err != nil {
		t.Fatalf("write empty firc groups.yaml: %v", err)
	}
	return dataDir, singboxDir
}

func startLifecycleApp(t *testing.T, dataDir, singboxDir string, clashPort int, sharedFircRunning *atomic.Bool) *lifecycleHarness {
	return startLifecycleAppWithNDMS(t, dataDir, singboxDir, clashPort, sharedFircRunning, nil)
}

func startLifecycleAppWithNDMS(t *testing.T, dataDir, singboxDir string, clashPort int, sharedFircRunning *atomic.Bool, fakeNDMS *query.FakeNDMS) *lifecycleHarness {
	t.Helper()
	t.Setenv("AWGM_TEST_SINGBOX_CLASH_ADDR", fmt.Sprintf("127.0.0.1:%d", clashPort))
	restorePaths(t)
	applyDataDir(dataDir)

	settingsStore := storage.NewSettingsStore(dataDir)
	snap, err := settingsStore.Load()
	if err != nil {
		t.Fatalf("settingsStore.Load: %v", err)
	}
	logSvc := logging.NewService(settingsStore)
	bootLog := logging.NewScopedLogger(logSvc, logging.GroupSystem, logging.SubBoot)

	a := &app{
		dataDir:        dataDir,
		singboxDir:     singboxDir,
		settingsStore:  settingsStore,
		settings:       snap,
		eventBus:       events.NewBus(),
		loggingService: logSvc,
		bootLog:        bootLog,
	}
	if fakeNDMS != nil {
		q := query.NewQueries(query.Deps{Getter: fakeNDMS, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
		c := command.NewCommands(command.Deps{
			Poster:  fakeNDMS,
			Queries: q,
			Save:    command.NewSaveCoordinator(fakeNDMS, nil, time.Hour, time.Hour, 0, q.RunningConfig),
			IsOS5:   func() bool { return true },
		})
		a.ndmsQueries = q
		a.ndmsCommands = c
	}
	a.setupSingboxRuntime()

	routerSvc := router.NewService(router.Deps{
		AppLog:             logSvc,
		Settings:           settingsStore,
		Singbox:            a.singboxOp,
		Orch:               a.sbOrch,
		Events:             a.eventBus,
		Bus:                a.eventBus,
		IPTables:           router.NewNoopIPTables(),
		WANIPCollector:     lifecycleNoopWANCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
		XtDscpProbe:        func(context.Context) bool { return false },
		ListeningProbe: func() bool {
			r, _ := a.singboxOp.IsRunning()
			return r
		},
	})
	a.routerSvc = routerSvc
	rc := &faultyRouterCoord{inner: routerSvc}
	a.routerCoord = rc

	if sharedFircRunning == nil {
		sharedFircRunning = &atomic.Bool{}
	}
	h := &lifecycleHarness{
		t:           t,
		dataDir:     dataDir,
		singboxDir:  singboxDir,
		clashPort:   clashPort,
		app:         a,
		singboxH:    api.NewSingboxHandler(a.singboxOp, a.eventBus, nil, nil, logSvc),
		fircRunning: sharedFircRunning,
		routerFault: rc,
	}

	var fircMu sync.Mutex
	var extCfg firc.ExtConfig
	h.fircSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.fircRunning.Load() {
			http.Error(w, "fircd stopped", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/system/netfilter":
			_, _ = w.Write([]byte(`{"available":true,"chainsPresent":true,"rulesCount":1}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/tunnels":
			_, _ = w.Write([]byte(`{"tunnels":[]}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/tunnels":
			_, _ = w.Write([]byte(`{"tunnels":[],"applied":0}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(`{"groups":[]}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(`{"groups":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/system/settings":
			_, _ = w.Write([]byte(`{"settings":{}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/system/settings":
			_, _ = w.Write([]byte(`{"applied":[],"pendingRestart":[]}`))
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/ext"):
			fircMu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&extCfg)
			fircMu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))

	a.fircOp = firc.NewOperator(firc.Options{
		ConfigDir:   filepath.Join(dataDir, "firc"),
		InboundPort: firc.DefaultMixedInboundPort,
	})
	a.fircOp.SetClient(firc.NewHTTPClient(h.fircSrv.URL, h.fircSrv.Client()))
	a.fircOp.SetLifecycleHooks(
		func(_ context.Context) error {
			if h.failFircStart.Load() {
				return errors.New("injected firc start failure")
			}
			h.fircRunning.Store(true)
			return nil
		},
		func(_ context.Context) error {
			h.fircRunning.Store(false)
			return nil
		},
	)

	a.fircHandler = api.NewFircHandler(a.fircOp)
	a.fircHandler.SetSettingsStore(settingsStore)
	a.fircHandler.SetEventBus(a.eventBus)
	a.fircHandler.SetSingboxConfigDir(filepath.Join(singboxDir, "config.d"))
	a.wireFircEngineCoordinator()
	a.completeFircStartup()

	h.mux = http.NewServeMux()
	passthrough := func(fn http.HandlerFunc) http.HandlerFunc { return fn }
	a.fircHandler.RegisterRoutes(h.mux, passthrough)
	h.mux.HandleFunc("POST /api/singbox/control", h.singboxH.Control)

	t.Cleanup(func() {
		h.Close(true)
	})
	return h
}

func (h *lifecycleHarness) Close(stopChild bool) {
	if h.closed {
		return
	}
	h.closed = true
	if h.app != nil && h.app.sbOrch != nil {
		h.app.sbOrch.Close()
	}
	if stopChild && h.app != nil && h.app.singboxOp != nil {
		_ = h.app.singboxOp.Stop()
		if proc := h.app.singboxOp.Process(); proc != nil {
			proc.Close()
		}
	}
	if h.app != nil && h.app.loggingService != nil {
		h.app.loggingService.Stop()
	}
	if h.fircSrv != nil {
		h.fircSrv.Close()
	}
}

func (h *lifecycleHarness) runWatchdogTick(ctx context.Context) {
	h.t.Helper()
	wd := singbox.NewWatchdog(h.app.singboxOp, nil, nil)
	wCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	wd.Run(wCtx)
}

func (h *lifecycleHarness) postSwitchEngine(engine string, standby bool) (int, string) {
	h.t.Helper()
	body, err := json.Marshal(map[string]any{
		"engine":         engine,
		"singboxStandby": standby,
	})
	if err != nil {
		h.t.Fatalf("marshal switch request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/firc/engine/switch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (h *lifecycleHarness) postSingboxControl(action string) (int, string) {
	h.t.Helper()
	body, err := json.Marshal(api.SingboxControlRequest{Action: action})
	if err != nil {
		h.t.Fatalf("marshal control request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/control", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (h *lifecycleHarness) loadSettings() *storage.Settings {
	h.t.Helper()
	s, err := h.app.settingsStore.Load()
	if err != nil {
		h.t.Fatalf("settingsStore.Load: %v", err)
	}
	return s
}

func (h *lifecycleHarness) bootstrapRunningSingboxWithActiveRouter(ctx context.Context) {
	h.t.Helper()
	if err := h.app.singboxOp.Start(); err != nil {
		h.t.Fatalf("singboxOp.Start: %v", err)
	}
	if err := h.app.routerSvc.Reconcile(ctx); err != nil {
		h.t.Fatalf("initial routerSvc.Reconcile: %v", err)
	}
	if running, _ := h.app.singboxOp.IsRunning(); !running {
		h.t.Fatalf("expected sing-box running after bootstrap")
	}
	st, err := h.app.routerSvc.GetStatus(ctx)
	if err != nil {
		h.t.Fatalf("initial routerSvc.GetStatus: %v", err)
	}
	if !st.Active || st.ParkedByExternalEngine {
		h.t.Fatalf("expected router active & unparked after bootstrap, got %+v", st)
	}
}

func TestFircEngineLifecycle_SwitchAndStop_SurvivesWatchdogTicksAndDaemonRestart(t *testing.T) {
	ctx := context.Background()
	clashPort := testutil.FreePort(t)
	dataDir, singboxDir := seedLifecycleDiskState(t, t.TempDir(), clashPort)

	sharedFirc := &atomic.Bool{}
	h := startLifecycleApp(t, dataDir, singboxDir, clashPort, sharedFirc)
	h.bootstrapRunningSingboxWithActiveRouter(ctx)

	if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
		t.Fatalf("switch_and_stop failed: HTTP %d: %s", code, body)
	}

	if !sharedFirc.Load() {
		t.Fatalf("expected fircd running after switch_and_stop")
	}
	if running, _ := h.app.singboxOp.IsRunning(); running {
		t.Fatalf("expected sing-box stopped after switch_and_stop")
	}
	if !h.app.singboxOp.IsStoppedByEngine() || h.app.singboxOp.IsManuallyStopped() || !h.app.singboxOp.IsAutoStartSuppressed() {
		t.Fatalf("unexpected singbox flags after switch_and_stop: byEngine=%v manual=%v suppressed=%v",
			h.app.singboxOp.IsStoppedByEngine(), h.app.singboxOp.IsManuallyStopped(), h.app.singboxOp.IsAutoStartSuppressed())
	}
	s := h.loadSettings()
	if s.SingboxRouter.RoutingEngine != "firc" || !s.SingboxStoppedByEngine || s.SingboxManuallyStopped {
		t.Fatalf("unexpected persisted settings after switch_and_stop: engine=%q byEngine=%v manual=%v",
			s.SingboxRouter.RoutingEngine, s.SingboxStoppedByEngine, s.SingboxManuallyStopped)
	}
	st, err := h.app.routerSvc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus after switch_and_stop: %v", err)
	}
	if !st.ParkedByExternalEngine || st.Active || st.RoutingEngine != "firc" {
		t.Fatalf("expected router parked on firc, got %+v", st)
	}

	// Watchdog ticks & router/singbox reconciles must not revive sing-box or unpark router.
	for i := 0; i < 3; i++ {
		h.runWatchdogTick(ctx)
		if err := h.app.singboxOp.Reconcile(ctx); err != nil {
			t.Fatalf("singbox Reconcile[%d]: %v", i, err)
		}
		if err := h.app.routerSvc.Reconcile(ctx); err != nil {
			t.Fatalf("router Reconcile[%d]: %v", i, err)
		}
		if running, _ := h.app.singboxOp.IsRunning(); running {
			t.Fatalf("watchdog/reconcile[%d] cold-started sing-box while stopped by engine", i)
		}
		st, err = h.app.routerSvc.GetStatus(ctx)
		if err != nil {
			t.Fatalf("GetStatus[%d]: %v", i, err)
		}
		if !st.ParkedByExternalEngine || st.Active {
			t.Fatalf("reconcile[%d] unparked router while engine is firc: %+v", i, st)
		}
	}

	// Simulate full awg-manager daemon restart on the same persisted dataDir/singboxDir.
	h.Close(false)
	h2 := startLifecycleApp(t, dataDir, singboxDir, clashPort, sharedFirc)
	if !h2.app.singboxOp.IsStoppedByEngine() || h2.app.singboxOp.IsManuallyStopped() || !h2.app.singboxOp.IsAutoStartSuppressed() {
		t.Fatalf("flags lost across daemon restart: byEngine=%v manual=%v suppressed=%v",
			h2.app.singboxOp.IsStoppedByEngine(), h2.app.singboxOp.IsManuallyStopped(), h2.app.singboxOp.IsAutoStartSuppressed())
	}
	h2.runWatchdogTick(ctx)
	if err := h2.app.singboxOp.Reconcile(ctx); err != nil {
		t.Fatalf("post-restart singbox Reconcile: %v", err)
	}
	if err := h2.app.routerSvc.Reconcile(ctx); err != nil {
		t.Fatalf("post-restart router Reconcile: %v", err)
	}
	if running, _ := h2.app.singboxOp.IsRunning(); running {
		t.Fatalf("post-restart watchdog cold-started sing-box while SingboxStoppedByEngine=true")
	}
	st2, err := h2.app.routerSvc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("post-restart router GetStatus: %v", err)
	}
	if !st2.ParkedByExternalEngine || st2.Active || st2.RoutingEngine != "firc" {
		t.Fatalf("expected post-restart router parked on firc, got %+v", st2)
	}
}

func TestFircEngineLifecycle_SwitchStandby_KeepsNonRouterSingboxWhileRouterParked(t *testing.T) {
	ctx := context.Background()
	clashPort := testutil.FreePort(t)
	dataDir, singboxDir := seedLifecycleDiskState(t, t.TempDir(), clashPort)

	sharedFirc := &atomic.Bool{}
	h := startLifecycleApp(t, dataDir, singboxDir, clashPort, sharedFirc)
	h.bootstrapRunningSingboxWithActiveRouter(ctx)

	if code, body := h.postSwitchEngine("firc", true); code != http.StatusOK {
		t.Fatalf("switch_standby failed: HTTP %d: %s", code, body)
	}

	if !sharedFirc.Load() {
		t.Fatalf("expected fircd running after switch_standby")
	}
	if running, _ := h.app.singboxOp.IsRunning(); !running {
		t.Fatalf("expected sing-box kept running in standby mode")
	}
	if h.app.singboxOp.IsStoppedByEngine() {
		t.Fatalf("expected SingboxStoppedByEngine=false in standby mode")
	}
	st, err := h.app.routerSvc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus in standby: %v", err)
	}
	if !st.ParkedByExternalEngine || st.Active || st.RoutingEngine != "firc" {
		t.Fatalf("expected router parked while sing-box in standby, got %+v", st)
	}

	// Reconcile/watchdog keep standby sing-box alive and keep router parked.
	h.runWatchdogTick(ctx)
	if err := h.app.routerSvc.Reconcile(ctx); err != nil {
		t.Fatalf("standby router Reconcile: %v", err)
	}
	st, _ = h.app.routerSvc.GetStatus(ctx)
	if !st.ParkedByExternalEngine || st.Active {
		t.Fatalf("router failed to stay parked on standby reconcile: %+v", st)
	}
	if running, _ := h.app.singboxOp.IsRunning(); !running {
		t.Fatalf("standby sing-box stopped unexpectedly")
	}

	// Same-engine convergence: switch to firc with singboxStandby=false stops sing-box.
	if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
		t.Fatalf("same-engine switch_and_stop failed: HTTP %d: %s", code, body)
	}
	if running, _ := h.app.singboxOp.IsRunning(); running {
		t.Fatalf("expected sing-box stopped after same-engine switch_and_stop")
	}
	if !h.app.singboxOp.IsStoppedByEngine() {
		t.Fatalf("expected SingboxStoppedByEngine=true after same-engine switch_and_stop")
	}
}

func TestFircEngineLifecycle_ReturnToSingbox_And_ManualControlIsolation(t *testing.T) {
	ctx := context.Background()
	clashPort := testutil.FreePort(t)
	dataDir, singboxDir := seedLifecycleDiskState(t, t.TempDir(), clashPort)

	sharedFirc := &atomic.Bool{}
	h := startLifecycleApp(t, dataDir, singboxDir, clashPort, sharedFirc)
	h.bootstrapRunningSingboxWithActiveRouter(ctx)

	// Part A: switch_and_stop -> firc, then switch back -> sing-box resumes both process and router.
	if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
		t.Fatalf("switch_and_stop to firc failed: HTTP %d: %s", code, body)
	}
	if code, body := h.postSwitchEngine("sing-box", false); code != http.StatusOK {
		t.Fatalf("return to sing-box failed: HTTP %d: %s", code, body)
	}
	if sharedFirc.Load() {
		t.Fatalf("expected fircd stopped after returning to sing-box")
	}
	if running, _ := h.app.singboxOp.IsRunning(); !running {
		t.Fatalf("expected sing-box resumed after returning from switch_and_stop")
	}
	if h.app.singboxOp.IsStoppedByEngine() || h.app.singboxOp.IsManuallyStopped() {
		t.Fatalf("unexpected stop flags after return to sing-box: byEngine=%v manual=%v",
			h.app.singboxOp.IsStoppedByEngine(), h.app.singboxOp.IsManuallyStopped())
	}
	st, err := h.app.routerSvc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus after return: %v", err)
	}
	if st.ParkedByExternalEngine || !st.Active || st.RoutingEngine != "sing-box" {
		t.Fatalf("expected router resumed and active after return to sing-box, got %+v", st)
	}

	// Part B: Manual stop isolation — manual stop must remain honored across firc round-trip,
	// while engine return must not clear SingboxManuallyStopped.
	if code, body := h.postSingboxControl("stop"); code != http.StatusOK {
		t.Fatalf("manual control stop failed: HTTP %d: %s", code, body)
	}
	if !h.app.singboxOp.IsManuallyStopped() || h.app.singboxOp.IsStoppedByEngine() {
		t.Fatalf("expected manual=true, byEngine=false after Control(stop); got manual=%v byEngine=%v",
			h.app.singboxOp.IsManuallyStopped(), h.app.singboxOp.IsStoppedByEngine())
	}
	if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
		t.Fatalf("switch to firc while manually stopped failed: HTTP %d: %s", code, body)
	}
	if !h.app.singboxOp.IsManuallyStopped() {
		t.Fatalf("switching to firc clobbered SingboxManuallyStopped")
	}
	if code, body := h.postSwitchEngine("sing-box", false); code != http.StatusOK {
		t.Fatalf("return to sing-box while manually stopped failed: HTTP %d: %s", code, body)
	}
	if !h.app.singboxOp.IsManuallyStopped() {
		t.Fatalf("returning to sing-box cleared SingboxManuallyStopped")
	}
	if h.app.singboxOp.IsStoppedByEngine() {
		t.Fatalf("expected SingboxStoppedByEngine=false after returning to sing-box")
	}
	h.runWatchdogTick(ctx)
	if running, _ := h.app.singboxOp.IsRunning(); running {
		t.Fatalf("manually stopped sing-box started despite SingboxManuallyStopped=true")
	}

	// Explicit manual start restores process and clears SingboxManuallyStopped.
	if code, body := h.postSingboxControl("start"); code != http.StatusOK {
		t.Fatalf("manual control start failed: HTTP %d: %s", code, body)
	}
	if running, _ := h.app.singboxOp.IsRunning(); !running {
		t.Fatalf("expected sing-box running after manual Control(start)")
	}
	if h.app.singboxOp.IsManuallyStopped() {
		t.Fatalf("expected SingboxManuallyStopped cleared after manual Control(start)")
	}
}

func TestFircEngineLifecycle_BidirectionalRollback_RestoresExactState(t *testing.T) {
	ctx := context.Background()
	clashPort := testutil.FreePort(t)
	dataDir, singboxDir := seedLifecycleDiskState(t, t.TempDir(), clashPort)

	sharedFirc := &atomic.Bool{}
	h := startLifecycleApp(t, dataDir, singboxDir, clashPort, sharedFirc)
	h.bootstrapRunningSingboxWithActiveRouter(ctx)

	// Direction 1A: sing-box -> firc fails after CommitSettings & sing-box stop when router ParkForExternalEngine fails.
	h.routerFault.failPark.Store(true)
	if code, _ := h.postSwitchEngine("firc", false); code == http.StatusOK {
		t.Fatalf("expected switch sing-box -> firc to fail on injected park error")
	}
	h.routerFault.failPark.Store(false)

	if sharedFirc.Load() {
		t.Fatalf("rollback failed to stop candidate fircd")
	}
	s := h.loadSettings()
	if s.SingboxRouter.RoutingEngine != "sing-box" || s.SingboxStoppedByEngine || s.SingboxManuallyStopped {
		t.Fatalf("rollback failed to restore sing-box settings: engine=%q byEngine=%v manual=%v",
			s.SingboxRouter.RoutingEngine, s.SingboxStoppedByEngine, s.SingboxManuallyStopped)
	}
	if running, _ := h.app.singboxOp.IsRunning(); !running {
		t.Fatalf("rollback failed to restore running sing-box")
	}
	st, err := h.app.routerSvc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus after sing-box -> firc rollback: %v", err)
	}
	if st.ParkedByExternalEngine || !st.Active {
		t.Fatalf("rollback failed to keep router active & unparked: %+v", st)
	}

	// Direction 1B: Manual-stop state preserved on rollback from sing-box -> firc.
	if code, body := h.postSingboxControl("stop"); code != http.StatusOK {
		t.Fatalf("manual stop before rollback check failed: HTTP %d: %s", code, body)
	}
	h.routerFault.failPark.Store(true)
	if code, _ := h.postSwitchEngine("firc", true); code == http.StatusOK {
		t.Fatalf("expected switch_standby to fail on injected park error")
	}
	h.routerFault.failPark.Store(false)

	s = h.loadSettings()
	if s.SingboxRouter.RoutingEngine != "sing-box" || s.SingboxStoppedByEngine || !s.SingboxManuallyStopped {
		t.Fatalf("rollback failed to preserve manual-stop state: engine=%q byEngine=%v manual=%v",
			s.SingboxRouter.RoutingEngine, s.SingboxStoppedByEngine, s.SingboxManuallyStopped)
	}
	if running, _ := h.app.singboxOp.IsRunning(); running {
		t.Fatalf("rollback unexpectedly started manually stopped sing-box")
	}

	// Restore running sing-box, switch cleanly to firc with switch_and_stop, then test Direction 2: firc -> sing-box rollback.
	if code, body := h.postSingboxControl("start"); code != http.StatusOK {
		t.Fatalf("control start failed: HTTP %d: %s", code, body)
	}
	if err := h.app.routerSvc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile before clean firc switch: %v", err)
	}
	if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
		t.Fatalf("clean switch_and_stop to firc failed: HTTP %d: %s", code, body)
	}

	h.routerFault.failResume.Store(true)
	if code, _ := h.postSwitchEngine("sing-box", false); code == http.StatusOK {
		t.Fatalf("expected switch firc -> sing-box to fail on injected resume error")
	}
	h.routerFault.failResume.Store(false)

	if !sharedFirc.Load() {
		t.Fatalf("rollback failed to restart previous fircd")
	}
	s = h.loadSettings()
	if s.SingboxRouter.RoutingEngine != "firc" || !s.SingboxStoppedByEngine || s.SingboxManuallyStopped {
		t.Fatalf("rollback failed to restore firc + stopped-by-engine state: engine=%q byEngine=%v manual=%v",
			s.SingboxRouter.RoutingEngine, s.SingboxStoppedByEngine, s.SingboxManuallyStopped)
	}
	if running, _ := h.app.singboxOp.IsRunning(); running {
		t.Fatalf("rollback left candidate sing-box running")
	}
	st, err = h.app.routerSvc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus after firc -> sing-box rollback: %v", err)
	}
	if !st.ParkedByExternalEngine || st.Active || st.RoutingEngine != "firc" {
		t.Fatalf("rollback failed to keep router parked on firc: %+v", st)
	}
}

func TestFircEngineLifecycle_StopEngineSerializesWithInflightColdStart(t *testing.T) {
	runScenario := func(t *testing.T, triggerStop func(ctx context.Context, h *lifecycleHarness)) {
		t.Helper()
		ctx := context.Background()
		clashPort := testutil.FreePort(t)
		dataDir, singboxDir := seedLifecycleDiskState(t, t.TempDir(), clashPort)

		sharedFirc := &atomic.Bool{}
		h := startLifecycleApp(t, dataDir, singboxDir, clashPort, sharedFirc)
		h.bootstrapRunningSingboxWithActiveRouter(ctx)

		if err := h.app.singboxOp.Stop(); err != nil {
			t.Fatalf("initial Stop: %v", err)
		}

		checked := make(chan struct{})
		proceed := make(chan struct{})
		h.app.sbOrch.SetShouldRun(func() bool {
			allowed := !h.app.singboxOp.IsAutoStartSuppressed()
			close(checked)
			<-proceed
			return allowed
		})

		reloadDone := make(chan error, 1)
		go func() {
			reloadDone <- h.app.sbOrch.Reload()
		}()

		select {
		case <-checked:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for in-flight sbOrch.Reload() shouldRun check")
		}

		triggerStop(ctx, h)
		close(proceed)

		select {
		case err := <-reloadDone:
			if err != nil {
				t.Fatalf("sbOrch.Reload() returned unexpected error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for sbOrch.Reload() completion")
		}

		if running, _ := h.app.singboxOp.IsRunning(); running {
			t.Fatalf("in-flight cold-start resurrected sing-box after engine stop (stoppedByEngine=%v)",
				h.app.singboxOp.IsStoppedByEngine())
		}
		if !h.app.singboxOp.IsStoppedByEngine() {
			t.Fatalf("expected SingboxStoppedByEngine=true")
		}
	}

	t.Run("paused_in_should_run", func(t *testing.T) {
		runScenario(t, func(ctx context.Context, h *lifecycleHarness) {
			hooks := h.app.buildProductionEngineSwitchHooks()
			if err := hooks.StopEngine(ctx, "sing-box"); err != nil {
				t.Fatalf("hooks.StopEngine(sing-box): %v", err)
			}
		})
	})

	t.Run("switch_and_stop_http_versus_inflight_cold_start", func(t *testing.T) {
		runScenario(t, func(_ context.Context, h *lifecycleHarness) {
			if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
				t.Fatalf("postSwitchEngine(firc, false) failed: HTTP %d: %s", code, body)
			}
			if !h.fircRunning.Load() {
				t.Fatalf("expected fircd started after engine switch")
			}
		})
	})
}

func TestFircEngineLifecycle_AwgOutboundsReloadDoesNotColdStartStoppedSingbox(t *testing.T) {
	ctx := context.Background()
	clashPort := testutil.FreePort(t)
	dataDir, singboxDir := seedLifecycleDiskState(t, t.TempDir(), clashPort)

	sharedFirc := &atomic.Bool{}
	h := startLifecycleApp(t, dataDir, singboxDir, clashPort, sharedFirc)
	h.bootstrapRunningSingboxWithActiveRouter(ctx)

	if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
		t.Fatalf("switch_and_stop failed: HTTP %d: %s", code, body)
	}
	if running, _ := h.app.singboxOp.IsRunning(); running {
		t.Fatalf("expected sing-box stopped after switch_and_stop")
	}

	// Check BOTH sbOrch-wired and fallback Operator.Reload paths, plus direct Operator.Start().
	h.Close(false)
	h2 := startLifecycleApp(t, dataDir, singboxDir, clashPort, sharedFirc)

	sbAdapterOrch := newAwgoutboundsSingboxAdapter(h2.app.singboxOp, h2.app.sbOrch)
	if err := sbAdapterOrch.Reload(); err != nil {
		t.Fatalf("post-restart awgoutboundsSingboxAdapter(orch).Reload() error: %v", err)
	}
	if running, pid := h2.app.singboxOp.IsRunning(); running {
		t.Fatalf("awgoutboundsSingboxAdapter(orch).Reload() cold-started stopped sing-box (pid=%d)", pid)
	}

	sbAdapterFallback := newAwgoutboundsSingboxAdapter(h2.app.singboxOp)
	if err := sbAdapterFallback.Reload(); err != nil {
		t.Fatalf("post-restart awgoutboundsSingboxAdapter(op).Reload() error: %v", err)
	}
	if running, pid := h2.app.singboxOp.IsRunning(); running {
		t.Fatalf("awgoutboundsSingboxAdapter(op).Reload() cold-started stopped sing-box (pid=%d)", pid)
	}

	if err := h2.app.singboxOp.Start(); err != nil {
		t.Fatalf("h2.app.singboxOp.Start() returned unexpected error: %v", err)
	}
	if running, pid := h2.app.singboxOp.IsRunning(); running {
		t.Fatalf("direct singboxOp.Start() bypassed StoppedByEngine and started sing-box (pid=%d)", pid)
	}
}

func TestFircEngineLifecycle_Proxy0AndConfigsPreservedAcrossStopStandbyAndReturn(t *testing.T) {
	ndmsinfo.Reset()
	t.Cleanup(ndmsinfo.Reset)
	infoStore := query.NewSystemInfoStore(nil, nil)
	infoStore.Adopt(ndms.Version{Components: []string{"proxy"}}, "test")
	if err := ndmsinfo.Init(context.Background(), infoStore, time.Second); err != nil {
		t.Fatalf("ndmsinfo.Init: %v", err)
	}

	ctx := context.Background()
	clashPort := testutil.FreePort(t)
	dataDir, singboxDir := seedLifecycleDiskState(t, t.TempDir(), clashPort)

	// Enable CreateNDMSProxyForSingbox and provide a tunnel referencing Proxy0 (`tun-a`).
	store := storage.NewSettingsStore(dataDir)
	_ = store.Update(func(s *storage.Settings) error {
		s.CreateNDMSProxyForSingbox = true
		return nil
	})
	tunnelsPath := filepath.Join(singboxDir, "config.d", "10-tunnels.json")
	sbCfg := singbox.NewConfig()
	if err := sbCfg.AddTunnelWithListenPort("tun-a", "vless", "198.51.100.10", 443, 0, json.RawMessage(`{"type":"vless","tag":"tun-a","server":"198.51.100.10","server_port":443}`)); err != nil {
		t.Fatalf("AddTunnelWithListenPort: %v", err)
	}
	if err := sbCfg.Save(tunnelsPath); err != nil {
		t.Fatalf("save 10-tunnels.json: %v", err)
	}

	fakeNDMS := query.NewFakeNDMS(
		ndms.Interface{ID: "Proxy0", Type: "Proxy", Description: "tun-a", State: "up"},
		ndms.Interface{ID: "Proxy5", Type: "Proxy", Description: "user-keenetic", State: "up"},
	)

	sharedFirc := &atomic.Bool{}
	h := startLifecycleAppWithNDMS(t, dataDir, singboxDir, clashPort, sharedFirc, fakeNDMS)
	h.bootstrapRunningSingboxWithActiveRouter(ctx)

	assertProxyState := func(q *query.Queries, id, wantState string) {
		t.Helper()
		if !fakeNDMS.Has(id) {
			t.Fatalf("%s was deleted instead of preserved!", id)
		}
		_, iface, ok, err := q.Interfaces.Confirm(ctx, id)
		if err != nil || !ok || iface == nil || iface.State != wantState {
			t.Fatalf("%s state = %+v (ok=%v err=%v), want %q", id, iface, ok, err, wantState)
		}
	}

	// 1. switch_and_stop -> sing-box stopped, Proxy0 parked down (config preserved), Proxy5 untouched, 10-tunnels.json preserved.
	if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
		t.Fatalf("switch_and_stop failed: HTTP %d: %s", code, body)
	}
	assertProxyState(h.app.ndmsQueries, "Proxy0", "down")
	assertProxyState(h.app.ndmsQueries, "Proxy5", "up")
	if _, err := os.Stat(tunnelsPath); err != nil {
		t.Fatalf("10-tunnels.json removed on switch_and_stop: %v", err)
	}
	h.runWatchdogTick(ctx)
	if running, _ := h.app.singboxOp.IsRunning(); running {
		t.Fatalf("watchdog restarted sing-box after switch_and_stop")
	}
	assertProxyState(h.app.ndmsQueries, "Proxy0", "down")

	// Restart daemon and run watchdog ticks: awgm-box stays off and Proxy0 stays down.
	h.Close(false)
	h2 := startLifecycleAppWithNDMS(t, dataDir, singboxDir, clashPort, sharedFirc, fakeNDMS)
	h2.runWatchdogTick(ctx)
	if running, _ := h2.app.singboxOp.IsRunning(); running {
		t.Fatalf("post-restart watchdog restarted sing-box after switch_and_stop")
	}
	assertProxyState(h2.app.ndmsQueries, "Proxy0", "down")

	// 2. switch_standby -> sing-box kept running for non-router work, router parked, Proxy0 STILL parked down across watchdog!
	if code, body := h2.postSwitchEngine("firc", true); code != http.StatusOK {
		t.Fatalf("switch_standby failed: HTTP %d: %s", code, body)
	}
	if running, _ := h2.app.singboxOp.IsRunning(); !running {
		t.Fatalf("expected sing-box running for non-router work in standby mode")
	}
	st, err := h2.app.routerSvc.GetStatus(ctx)
	if err != nil || !st.ParkedByExternalEngine || st.Active {
		t.Fatalf("expected router parked in standby (no double routing), got st=%+v err=%v", st, err)
	}
	h2.runWatchdogTick(ctx)
	_ = h2.app.singboxOp.Reconcile(ctx)
	assertProxyState(h2.app.ndmsQueries, "Proxy0", "down")
	assertProxyState(h2.app.ndmsQueries, "Proxy5", "up")

	// 3. Return to sing-box -> Proxy0 resumed UP, sing-box & router active, 10-tunnels.json intact.
	if code, body := h2.postSwitchEngine("sing-box", false); code != http.StatusOK {
		t.Fatalf("return to sing-box failed: HTTP %d: %s", code, body)
	}
	assertProxyState(h2.app.ndmsQueries, "Proxy0", "up")
	assertProxyState(h2.app.ndmsQueries, "Proxy5", "up")
	if running, _ := h2.app.singboxOp.IsRunning(); !running {
		t.Fatalf("expected sing-box running after return")
	}
	st, _ = h2.app.routerSvc.GetStatus(ctx)
	if st.ParkedByExternalEngine || !st.Active || st.RoutingEngine != "sing-box" {
		t.Fatalf("expected router resumed active on sing-box, got %+v", st)
	}
}

func TestFircSnapshotRestoreRollback_NoRecoveryRequired(t *testing.T) {
	ctx := context.Background()
	clashPort := testutil.FreePort(t)
	dataDir, singboxDir := seedLifecycleDiskState(t, t.TempDir(), clashPort)

	sharedFirc := &atomic.Bool{}
	h := startLifecycleAppWithNDMS(t, dataDir, singboxDir, clashPort, sharedFirc, nil)
	h.app.fircOp.SetSingboxBinary(filepath.Join(singboxDir, "awgm-box"))
	if err := os.WriteFile(filepath.Join(singboxDir, "config.d", "10-tunnels.json"),
		[]byte(`{"outbounds":[{"type":"vless","tag":"tun-a","server":"198.51.100.10","server_port":443,"uuid":"11111111-1111-1111-1111-111111111111"}]}`+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write valid 10-tunnels.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "firc", "tunnels.yaml"),
		[]byte("configVersion: 0.7.0\ntunnels:\n  - id: firc-tun\n    device: tunvless0\n    enable: true\n    sources:\n      - id: s1\n        kind: link\n        name: Node1\n        link: vless://11111111-2222-3333-4444-555555555555@198.51.100.20:443?security=tls&sni=example.com#Node1\n"),
		0o600,
	); err != nil {
		t.Fatalf("write valid firc tunnels.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "firc", "groups.yaml"),
		[]byte("configVersion: 0.7.0\ngroups:\n  - id: 11223344\n    name: Grp1\n    interface:\n      - tunvless0\n    enable: true\n    rules:\n      - id: r1\n        type: domain\n        rule: example.com\n        enable: true\n"),
		0o644,
	); err != nil {
		t.Fatalf("write valid firc groups.yaml: %v", err)
	}
	h.bootstrapRunningSingboxWithActiveRouter(ctx)

	// Capture a snapshot while activeEngine is "sing-box".
	createReq := httptest.NewRequest(http.MethodPost, "/api/firc/snapshots", bytes.NewReader([]byte(`{"label":"pre-firc-snap"}`)))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.mux.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create snapshot failed: HTTP %d: %s", createRec.Code, createRec.Body.String())
	}
	var createEnv struct {
		Success bool `json:"success"`
		Data    struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createEnv); err != nil || createEnv.Data.ID == "" {
		t.Fatalf("decode create snapshot response: err=%v body=%s", err, createRec.Body.String())
	}

	if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
		t.Fatalf("initial switch to firc failed: HTTP %d: %s", code, body)
	}
	if !sharedFirc.Load() {
		t.Fatal("expected firc active before snapshot restore test")
	}

	// Inject failure in ActivateTarget("sing-box") via router ResumeFromExternalEngine
	// after RestoreSystemSnapshot has already written RoutingEngine="sing-box" to disk.
	h.routerFault.failResume.Store(true)

	restoreBody, _ := json.Marshal(map[string]any{
		"id":           createEnv.Data.ID,
		"switchEngine": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/firc/snapshots/restore", bytes.NewReader(restoreBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on restore with rollback, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			PostActionError  string `json:"postActionError"`
			RecoveryRequired bool   `json:"recoveryRequired"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode restore response: %v", err)
	}
	if env.Data.PostActionError == "" {
		t.Fatalf("expected postActionError from injected resume failure, got %s", rec.Body.String())
	}
	if h.app.fircOp.IsRecoveryRequired() || env.Data.RecoveryRequired {
		t.Fatalf("expected clean rollback without recovery_required, err=%q", env.Data.PostActionError)
	}
	if !sharedFirc.Load() {
		t.Fatal("expected FIRC restored active after rollback")
	}
	if !h.app.singboxOp.IsStoppedByEngine() {
		t.Fatal("expected singboxStoppedByEngine restored to true after rollback")
	}
	loaded, err := h.app.settingsStore.Load()
	if err != nil {
		t.Fatalf("settingsStore.Load: %v", err)
	}
	if loaded.SingboxRouter.RoutingEngine != "firc" {
		t.Fatalf("expected settings RoutingEngine rolled back to firc, got %q", loaded.SingboxRouter.RoutingEngine)
	}
}

func TestFircSnapshotRestoreRollback_SingboxToFircStartFailure_NoRecoveryRequired(t *testing.T) {
	ctx := context.Background()
	clashPort := testutil.FreePort(t)
	dataDir, singboxDir := seedLifecycleDiskState(t, t.TempDir(), clashPort)

	sharedFirc := &atomic.Bool{}
	h := startLifecycleAppWithNDMS(t, dataDir, singboxDir, clashPort, sharedFirc, nil)
	h.app.fircOp.SetSingboxBinary(filepath.Join(singboxDir, "awgm-box"))
	writeTunnels := func(tag string) {
		if err := os.WriteFile(filepath.Join(singboxDir, "config.d", "10-tunnels.json"),
			[]byte(fmt.Sprintf(`{"outbounds":[{"type":"vless","tag":%q,"server":"198.51.100.10","server_port":443,"uuid":"11111111-1111-1111-1111-111111111111"}]}`+"\n", tag)),
			0o600,
		); err != nil {
			t.Fatalf("write 10-tunnels.json (%s): %v", tag, err)
		}
	}
	writeTunnels("tun-snap")
	if err := os.WriteFile(filepath.Join(dataDir, "firc", "tunnels.yaml"),
		[]byte("configVersion: 0.7.0\ntunnels:\n  - id: firc-tun\n    device: tunvless0\n    enable: true\n    sources:\n      - id: s1\n        kind: link\n        name: Node1\n        link: vless://11111111-2222-3333-4444-555555555555@198.51.100.20:443?security=tls&sni=example.com#Node1\n"),
		0o600,
	); err != nil {
		t.Fatalf("write valid firc tunnels.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "firc", "groups.yaml"),
		[]byte("configVersion: 0.7.0\ngroups:\n  - id: 11223344\n    name: Grp1\n    interface:\n      - tunvless0\n    enable: true\n    rules:\n      - id: r1\n        type: domain\n        rule: example.com\n        enable: true\n"),
		0o644,
	); err != nil {
		t.Fatalf("write valid firc groups.yaml: %v", err)
	}
	h.bootstrapRunningSingboxWithActiveRouter(ctx)

	// Switch to firc and capture a firc snapshot (whose settings.json has routingEngine="firc" and tun-snap).
	if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
		t.Fatalf("switch to firc failed: HTTP %d: %s", code, body)
	}
	createReq := httptest.NewRequest(http.MethodPost, "/api/firc/snapshots", bytes.NewReader([]byte(`{"label":"firc-snap"}`)))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.mux.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create firc snapshot failed: HTTP %d: %s", createRec.Code, createRec.Body.String())
	}
	var createEnv struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createEnv); err != nil || createEnv.Data.ID == "" {
		t.Fatalf("decode create snapshot response: err=%v body=%s", err, createRec.Body.String())
	}

	// Switch back to sing-box and mutate live sing-box config (tun-live) so we can verify
	// a failed snapshot restore neither loses live files nor leaves settings on "firc".
	if code, body := h.postSwitchEngine("sing-box", false); code != http.StatusOK {
		t.Fatalf("switch back to sing-box failed: HTTP %d: %s", code, body)
	}
	writeTunnels("tun-live")
	if err := h.app.sbOrch.ReloadNow(); err != nil {
		t.Fatalf("reload live sing-box config: %v", err)
	}

	// Inject FIRC startup failure (simulating e.g. fircd rejecting an unsupported tunnel scheme on start).
	h.failFircStart.Store(true)

	restoreBody, _ := json.Marshal(map[string]any{
		"id":           createEnv.Data.ID,
		"switchEngine": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/firc/snapshots/restore", bytes.NewReader(restoreBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on restore with rollback, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			PostActionError  string `json:"postActionError"`
			RecoveryRequired bool   `json:"recoveryRequired"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode restore response: %v", err)
	}
	if env.Data.PostActionError == "" {
		t.Fatalf("expected postActionError from injected firc start failure, got %s", rec.Body.String())
	}
	if h.app.fircOp.IsRecoveryRequired() || env.Data.RecoveryRequired {
		t.Fatalf("expected clean rollback without recovery_required, err=%q", env.Data.PostActionError)
	}
	if sharedFirc.Load() {
		t.Fatal("expected FIRC stopped/inactive after failed switch rollback")
	}
	loaded, err := h.app.settingsStore.Load()
	if err != nil {
		t.Fatalf("settingsStore.Load: %v", err)
	}
	if loaded.SingboxRouter.RoutingEngine != "sing-box" {
		t.Fatalf("expected settings RoutingEngine preserved/restored to sing-box, got %q", loaded.SingboxRouter.RoutingEngine)
	}
	rawTunnels, err := os.ReadFile(filepath.Join(singboxDir, "config.d", "10-tunnels.json"))
	if err != nil {
		t.Fatalf("read 10-tunnels.json after rollback: %v", err)
	}
	if !strings.Contains(string(rawTunnels), "tun-live") {
		t.Fatalf("expected pre-restore 10-tunnels.json restored after failed snapshot switch, got %s", string(rawTunnels))
	}
	if running, pid := h.app.singboxOp.IsRunning(); !running || pid <= 0 {
		t.Fatalf("expected sing-box still running after rollback: running=%v pid=%d", running, pid)
	}
	st, err := h.app.routerSvc.GetStatus(ctx)
	if err != nil || !st.Active || st.ParkedByExternalEngine || st.RoutingEngine != "sing-box" {
		t.Fatalf("expected router resumed and active for sing-box after rollback: st=%+v err=%v", st, err)
	}
}

func TestFircSnapshotRestore_SingboxSnapshotWhileFircActive_SkipsFircdSync(t *testing.T) {
	ctx := context.Background()
	clashPort := testutil.FreePort(t)
	dataDir, singboxDir := seedLifecycleDiskState(t, t.TempDir(), clashPort)

	sharedFirc := &atomic.Bool{}
	var rejectTunnelWrites atomic.Bool
	h := startLifecycleAppWithNDMS(t, dataDir, singboxDir, clashPort, sharedFirc, nil)
	baseHandler := h.fircSrv.Config.Handler
	h.fircSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectTunnelWrites.Load() && r.Method == http.MethodPut && r.URL.Path == "/api/v1/tunnels" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unsupported legacy tunnel scheme in fircd"}`))
			return
		}
		baseHandler.ServeHTTP(w, r)
	})
	h.app.fircOp.SetSingboxBinary(filepath.Join(singboxDir, "awgm-box"))
	if err := os.WriteFile(filepath.Join(singboxDir, "config.d", "10-tunnels.json"),
		[]byte(`{"outbounds":[{"type":"vless","tag":"tun-sb","server":"198.51.100.10","server_port":443,"uuid":"11111111-1111-1111-1111-111111111111"}]}`+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "firc", "tunnels.yaml"),
		[]byte("configVersion: 0.7.0\ntunnels:\n  - id: firc-tun\n    device: tunvless0\n    enable: true\n    sources:\n      - id: s1\n        kind: link\n        name: Node1\n        link: vless://11111111-2222-3333-4444-555555555555@198.51.100.20:443?security=tls&sni=example.com#Node1\n"),
		0o600,
	); err != nil {
		t.Fatalf("write firc tunnels.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "firc", "groups.yaml"),
		[]byte("configVersion: 0.7.0\ngroups:\n  - id: 11223344\n    name: Grp1\n    interface:\n      - tunvless0\n    enable: true\n    rules:\n      - id: r1\n        type: domain\n        rule: example.com\n        enable: true\n"),
		0o644,
	); err != nil {
		t.Fatalf("write firc groups.yaml: %v", err)
	}
	h.bootstrapRunningSingboxWithActiveRouter(ctx)

	// Capture a sing-box snapshot while activeEngine is "sing-box" (which also carries firc/tunnels.yaml).
	createReq := httptest.NewRequest(http.MethodPost, "/api/firc/snapshots", bytes.NewReader([]byte(`{"label":"sb-with-stale-firc"}`)))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.mux.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create snapshot failed: HTTP %d: %s", createRec.Code, createRec.Body.String())
	}
	var createEnv struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createEnv); err != nil || createEnv.Data.ID == "" {
		t.Fatalf("decode create snapshot response: err=%v body=%s", err, createRec.Body.String())
	}

	// Switch to firc, then simulate active fircd rejecting old firc tunnels from the sing-box snapshot.
	if code, body := h.postSwitchEngine("firc", false); code != http.StatusOK {
		t.Fatalf("switch to firc failed: HTTP %d: %s", code, body)
	}
	rejectTunnelWrites.Store(true)

	restoreBody, _ := json.Marshal(map[string]any{
		"id":           createEnv.Data.ID,
		"switchEngine": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/firc/snapshots/restore", bytes.NewReader(restoreBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 restoring sing-box snapshot while firc active, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			PostActionApplied string `json:"postActionApplied"`
			PostActionError   string `json:"postActionError"`
			RecoveryRequired  bool   `json:"recoveryRequired"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode restore response: %v", err)
	}
	if env.Data.PostActionError != "" || env.Data.RecoveryRequired || env.Data.PostActionApplied != "switch_to_sing-box" {
		t.Fatalf("unexpected restore response: %+v", env.Data)
	}
	if sharedFirc.Load() {
		t.Fatal("expected fircd stopped after switching to sing-box")
	}
	loaded, err := h.app.settingsStore.Load()
	if err != nil || loaded.SingboxRouter.RoutingEngine != "sing-box" {
		t.Fatalf("expected RoutingEngine=sing-box, loaded=%+v err=%v", loaded, err)
	}
}
