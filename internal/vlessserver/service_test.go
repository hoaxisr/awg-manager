package vlessserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
)

func TestMain(m *testing.M) {
	base := strings.ToLower(filepath.Base(os.Args[0]))
	if strings.HasPrefix(base, "awgm-xhttpd") {
		runFakeXHTTPDHelper()
		return
	}
	os.Exit(m.Run())
}

func runFakeXHTTPDHelper() {
	mode := os.Getenv("GO_TEST_FAKE_XHTTPD_MODE")
	if mode == "" {
		mode = "healthy"
	}
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-version" || os.Args[i] == "-v" {
			if mode == "bad_version" {
				fmt.Println("awgm-xhttpd 0.0.1-broken")
				os.Exit(0)
			}
			fmt.Printf("awgm-xhttpd %s\n", xhttpserver.ExpectedVersion)
			os.Exit(0)
		}
	}

	if mode == "exit_early" {
		os.Exit(1)
	}

	var confPath string
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-config" && i+1 < len(os.Args) {
			confPath = os.Args[i+1]
			break
		}
	}
	if confPath == "" {
		os.Exit(2)
	}
	raw, err := os.ReadFile(confPath)
	if err != nil {
		os.Exit(3)
	}
	host := "127.0.0.1"
	port := "9008"
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "listen_host=") {
			host = strings.TrimPrefix(line, "listen_host=")
		} else if strings.HasPrefix(line, "listen_port=") {
			port = strings.TrimPrefix(line, "listen_port=")
		}
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		os.Exit(4)
	}
	pid := os.Getpid()
	mux := http.NewServeMux()
	mux.HandleFunc("/.xhttpd/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","engine":"c/1","version":%q,"pid":%d,"capabilities":["vless-xhttp","finiteproto-v2"]}`+"\n",
			xhttpserver.ExpectedVersion, pid)
	})
	mux.HandleFunc("/.xhttpd/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Awgm-Reload-Token") != os.Getenv("AWGM_XHTTPD_RELOAD_TOKEN") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Reload OK"))
	})
	_ = http.Serve(ln, mux)
	os.Exit(0)
}

func stageFakeXHTTPDBinary(t *testing.T, dir string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read test executable: %v", err)
	}
	name := "awgm-xhttpd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(dir, name)
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("write fake awgm-xhttpd: %v", err)
	}
	return dst
}

func findFreeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestService_StatusAndLifecycle_CBackendOnly(t *testing.T) {
	t.Run("missing_binary_fails_closed", func(t *testing.T) {
		dataDir := t.TempDir()
		missingBin := filepath.Join(dataDir, "missing-awgm-xhttpd")
		t.Setenv("AWGM_C_XHTTPD_BIN", missingBin)
		t.Setenv("AWGM_C_XHTTPD_CONF", filepath.Join(dataDir, "awgm-xhttpd.conf"))
		t.Setenv("AWGM_DISABLE_C_XHTTPD", "")

		svc := New(dataDir, nil)
		port := findFreeLoopbackPort(t)
		if err := svc.UpdateConfig(Config{
			Enabled:       false,
			ListenAddress: "127.0.0.1",
			ListenPort:    port,
			PublicDomain:  "cdn.example.com",
			Path:          "/cdn-bridge",
		}); err != nil {
			t.Fatalf("UpdateConfig(disabled): %v", err)
		}
		if _, err := svc.AddClient("Alice"); err != nil {
			t.Fatalf("AddClient: %v", err)
		}

		st := svc.GetStatus()
		if st.Backend != "awgm-xhttpd" {
			t.Fatalf("Backend = %q, want awgm-xhttpd", st.Backend)
		}
		if st.Installed {
			t.Fatalf("Installed = true for missing binary")
		}
		if st.Running || st.PID != 0 {
			t.Fatalf("unexpected running/pid state: running=%v pid=%d", st.Running, st.PID)
		}
		if st.UnavailableReason == "" {
			t.Fatalf("expected non-empty UnavailableReason for missing binary")
		}

		if err := svc.Start(); err == nil {
			_ = svc.Stop()
			t.Fatalf("Start() succeeded with missing C binary; expected fail-closed error")
		}
	})

	t.Run("disabled_env_fails_closed", func(t *testing.T) {
		dataDir := t.TempDir()
		fakeBin := stageFakeXHTTPDBinary(t, dataDir)
		t.Setenv("AWGM_C_XHTTPD_BIN", fakeBin)
		t.Setenv("AWGM_C_XHTTPD_CONF", filepath.Join(dataDir, "awgm-xhttpd.conf"))
		t.Setenv("AWGM_DISABLE_C_XHTTPD", "1")

		svc := New(dataDir, nil)
		st := svc.GetStatus()
		if st.Installed {
			t.Fatalf("expected Installed=false when AWGM_DISABLE_C_XHTTPD=1")
		}
		if !strings.Contains(st.UnavailableReason, "disabled") {
			t.Fatalf("unexpected UnavailableReason: %q", st.UnavailableReason)
		}
		if err := svc.Start(); !errors.Is(err, xhttpserver.ErrCDaemonDisabled) {
			_ = svc.Stop()
			t.Fatalf("Start() error = %v, want ErrCDaemonDisabled", err)
		}
	})

	t.Run("healthy_c_daemon_reports_real_child_pid_and_reloads", func(t *testing.T) {
		dataDir := t.TempDir()
		binDir := t.TempDir()
		fakeBin := stageFakeXHTTPDBinary(t, binDir)
		t.Setenv("AWGM_C_XHTTPD_BIN", fakeBin)
		t.Setenv("AWGM_C_XHTTPD_CONF", filepath.Join(binDir, "awgm-xhttpd.conf"))
		t.Setenv("AWGM_DISABLE_C_XHTTPD", "")
		t.Setenv("GO_TEST_FAKE_XHTTPD_MODE", "healthy")

		svc := New(dataDir, nil)
		defer svc.Stop()

		port := findFreeLoopbackPort(t)
		if err := svc.UpdateConfig(Config{
			Enabled:       false,
			ListenAddress: "127.0.0.1",
			ListenPort:    port,
			PublicDomain:  "cdn.example.com",
			Path:          "/cdn-bridge",
		}); err != nil {
			t.Fatalf("UpdateConfig: %v", err)
		}
		if _, err := svc.AddClient("Phone"); err != nil {
			t.Fatalf("AddClient: %v", err)
		}

		if err := svc.Start(); err != nil {
			t.Fatalf("Start() failed: %v", err)
		}

		st := svc.GetStatus()
		if st.Backend != "awgm-xhttpd" || !st.Installed || !st.Running {
			t.Fatalf("unexpected status: %+v", st)
		}
		if st.PID <= 0 || st.PID == os.Getpid() {
			t.Fatalf("PID = %d, want child process PID != %d", st.PID, os.Getpid())
		}
		if st.Version != "awgm-xhttpd "+xhttpserver.ExpectedVersion {
			t.Fatalf("Version = %q, want %q", st.Version, "awgm-xhttpd "+xhttpserver.ExpectedVersion)
		}
		if st.UnavailableReason != "" {
			t.Fatalf("UnavailableReason = %q, want empty", st.UnavailableReason)
		}

		initialPID := st.PID
		if _, err := svc.AddClient("Laptop"); err != nil {
			t.Fatalf("AddClient live reload: %v", err)
		}
		stAfter := svc.GetStatus()
		if stAfter.PID != initialPID {
			t.Fatalf("expected hot reload to keep child PID %d, got %d", initialPID, stAfter.PID)
		}

		if err := svc.Stop(); err != nil {
			t.Fatalf("Stop() failed: %v", err)
		}
		stStopped := svc.GetStatus()
		if stStopped.Running || stStopped.PID != 0 {
			t.Fatalf("expected stopped status, got running=%v pid=%d", stStopped.Running, stStopped.PID)
		}
	})
}

func TestService_ClientCRUD_ShareLinks_AndUnsupportedWS(t *testing.T) {
	dataDir := t.TempDir()
	svc := New(dataDir, nil)

	if err := svc.UpdateConfig(Config{
		Enabled:       false,
		ListenAddress: "127.0.0.1",
		ListenPort:    9008,
		PublicDomain:  "edge.example.com",
		PublicPort:    443,
		Path:          "/cdn-bridge",
		Transport:     "xhttp",
		Mode:          "packet-up",
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	c1, err := svc.AddClient("Work Laptop")
	if err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	if c1.ID == "" || c1.FPClientID == "" || len(c1.FPSecretHex) != 64 {
		t.Fatalf("invalid generated client credentials: %+v", c1)
	}

	links, err := svc.GenerateLinks(c1.ID)
	if err != nil {
		t.Fatalf("GenerateLinks: %v", err)
	}
	if !strings.HasPrefix(links.VlessURL, "vless://"+c1.ID+"@edge.example.com:443?") {
		t.Fatalf("unexpected VlessURL: %s", links.VlessURL)
	}
	if !strings.Contains(links.FiniteProtoURL, "mode=finiteproto&fp_v=2") {
		t.Fatalf("unexpected FiniteProtoURL: %s", links.FiniteProtoURL)
	}
	if !strings.Contains(links.MihomoYAML, "network: xhttp") || !strings.Contains(links.SingboxJSON, `"type": "xhttp"`) {
		t.Fatalf("unexpected external client configs")
	}

	oldFPID := c1.FPClientID
	rotated, err := svc.RotateClientFPCredentials(c1.ID)
	if err != nil {
		t.Fatalf("RotateClientFPCredentials: %v", err)
	}
	if rotated.FPClientID == oldFPID {
		t.Fatalf("expected rotated FPClientID to change")
	}

	if err := svc.ToggleClient(c1.ID, false); err != nil {
		t.Fatalf("ToggleClient: %v", err)
	}

	// Reload from disk and verify persistence in dataDir/xray/xray-server-settings.json
	reloaded := New(dataDir, nil)
	cfg := reloaded.GetConfig()
	if len(cfg.Clients) != 1 || cfg.Clients[0].ID != c1.ID || cfg.Clients[0].Enabled {
		t.Fatalf("persisted client state mismatch: %+v", cfg.Clients)
	}
	if cfg.Clients[0].FPClientID != rotated.FPClientID {
		t.Fatalf("persisted FPClientID mismatch: got %q want %q", cfg.Clients[0].FPClientID, rotated.FPClientID)
	}

	// Reject unsupported legacy WebSocket transport
	wsCfg := cfg
	wsCfg.Transport = "ws"
	if err := reloaded.UpdateConfig(wsCfg); !errors.Is(err, ErrUnsupportedTransport) {
		t.Fatalf("UpdateConfig(ws) error = %v, want ErrUnsupportedTransport", err)
	}
	if _, err := BuildShareLinks(wsCfg, cfg.Clients[0]); !errors.Is(err, ErrUnsupportedTransport) {
		t.Fatalf("BuildShareLinks(ws) error = %v, want ErrUnsupportedTransport", err)
	}

	if err := reloaded.DeleteClient(c1.ID); err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}
	if len(reloaded.GetConfig().Clients) != 0 {
		t.Fatalf("expected 0 clients after DeleteClient")
	}
}

func TestService_Transactions_CAS_Rollback_AndSlotCleanup(t *testing.T) {
	dataDir := t.TempDir()
	slotDir := filepath.Join(dataDir, "singbox", "config.d")
	if err := os.MkdirAll(filepath.Join(slotDir, "disabled"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacySlot := filepath.Join(slotDir, "22-server-ingress.json")
	if err := os.WriteFile(legacySlot, []byte(`{"inbounds":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := New(dataDir, nil)
	if _, err := os.Stat(legacySlot); !os.IsNotExist(err) {
		t.Fatalf("expected legacy 22-server-ingress.json slot to be removed by New()")
	}

	baseCfg := Config{
		Enabled:       true,
		ListenAddress: "127.0.0.1",
		ListenPort:    9008,
		PublicDomain:  "first.example.com",
		Path:          "/cdn-bridge",
	}
	runtimeErr := error(nil)
	svc.SetTestRuntimeHook(func(cfg xhttpserver.Config) error {
		return runtimeErr
	})
	if err := svc.UpdateConfig(baseCfg); err != nil {
		t.Fatalf("initial UpdateConfig: %v", err)
	}

	// 1. Stale transaction CAS conflict
	cand := svc.GetConfig()
	cand.PublicDomain = "second.example.com"
	txID, err := svc.PrepareCandidate("tx-stale-1", cand)
	if err != nil {
		t.Fatalf("PrepareCandidate: %v", err)
	}
	if _, err := svc.AddClient("Concurrent Mutation"); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	if err := svc.CommitPrepared(txID); !errors.Is(err, ErrStaleTransactionConflict) {
		t.Fatalf("CommitPrepared stale error = %v, want ErrStaleTransactionConflict", err)
	}

	// 2. Rollback when runtime fails during commit
	cand2 := svc.GetConfig()
	cand2.PublicDomain = "broken.example.com"
	txID2, err := svc.PrepareCandidate("tx-fail-runtime", cand2)
	if err != nil {
		t.Fatalf("PrepareCandidate 2: %v", err)
	}
	runtimeErr = errors.New("simulated awgm-xhttpd failure")
	err = svc.CommitPrepared(txID2)
	if err == nil {
		t.Fatalf("expected CommitPrepared failure when runtime hook fails")
	}
	runtimeErr = nil
	if got := svc.GetConfig().PublicDomain; got != "first.example.com" {
		t.Fatalf("expected rollback to restore domain first.example.com, got %q", got)
	}

	// 3. FIRC routing sync
	svc.SyncFircServerRouting(true)
	if got := svc.GetConfig().OutboundMode; got != "router" {
		t.Fatalf("OutboundMode after SyncFircServerRouting(true) = %q, want router", got)
	}
	svc.SyncFircServerRouting(false)
	if got := svc.GetConfig().OutboundMode; got != "direct" {
		t.Fatalf("OutboundMode after SyncFircServerRouting(false) = %q, want direct", got)
	}
}

func TestService_LiveRealCDaemon(t *testing.T) {
	requireC := os.Getenv("AWGM_REQUIRE_C_TESTS") == "1"
	if runtime.GOOS == "windows" {
		if requireC {
			t.Fatalf("AWGM_REQUIRE_C_TESTS=1 cannot run on Windows")
		}
		t.Skip("skipping live POSIX C awgm-xhttpd test: Windows host environment limitation (POSIX sockets/signals required)")
	}

	cSrcDir := filepath.Clean(filepath.Join("..", "..", "c-core", "xhttpd"))
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "awgm-xhttpd")
	cc, err := exec.LookPath("cc")
	if err != nil {
		cc, err = exec.LookPath("gcc")
	}
	if err != nil {
		t.Fatalf("C compiler not found on %s (AWGM_REQUIRE_C_TESTS=%q): %v", runtime.GOOS, os.Getenv("AWGM_REQUIRE_C_TESTS"), err)
	}

	cmd := exec.Command(cc, "-O2", "-Wall", "-Wextra", "-std=gnu11", "-pthread",
		filepath.Join(cSrcDir, "main.c"),
		filepath.Join(cSrcDir, "crypto.c"),
		filepath.Join(cSrcDir, "finiteproto.c"),
		filepath.Join(cSrcDir, "sniff_and_dial.c"),
		filepath.Join(cSrcDir, "vless_xhttp.c"),
		"-o", binPath,
		"-lpthread",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile awgm-xhttpd failed: %v\n%s", err, string(out))
	}

	t.Setenv("AWGM_C_XHTTPD_BIN", binPath)
	t.Setenv("AWGM_C_XHTTPD_CONF", filepath.Join(binDir, "awgm-xhttpd.conf"))
	t.Setenv("AWGM_DISABLE_C_XHTTPD", "")

	dataDir := t.TempDir()
	svc := New(dataDir, nil)
	defer svc.Stop()

	port := findFreeLoopbackPort(t)
	if err := svc.UpdateConfig(Config{
		Enabled:       false,
		ListenAddress: "127.0.0.1",
		ListenPort:    port,
		PublicDomain:  "live.example.com",
		Path:          "/cdn-bridge",
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if _, err := svc.AddClient("Live C Client"); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start real C awgm-xhttpd: %v", err)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/.xhttpd/health", port))
	if err != nil {
		t.Fatalf("GET /.xhttpd/health: %v", err)
	}
	defer resp.Body.Close()
	var health struct {
		Status  string `json:"status"`
		Engine  string `json:"engine"`
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	st := svc.GetStatus()
	if health.Engine != "c/1" || health.PID != st.PID || st.PID == os.Getpid() {
		t.Fatalf("health/status mismatch: health=%+v status=%+v", health, st)
	}
}

func TestBuildShareLinks_CompactExtraAndVlinkRoundTrip(t *testing.T) {
	client := Client{
		ID:      "00000000-1111-4222-8333-444444444444",
		Remark:  "Phone",
		Enabled: true,
	}

	t.Run("stream_one_balanced_drops_packet_up_fields", func(t *testing.T) {
		tun := DefaultBalancedTuning()
		links, err := BuildShareLinks(Config{
			PublicDomain: "connect.example.netcraze.club",
			PublicPort:   443,
			Path:         "/cdn-bridge",
			Transport:    "xhttp",
			Mode:         "stream-one",
			XHTTPTuning:  &tun,
		}, client)
		if err != nil {
			t.Fatalf("BuildShareLinks: %v", err)
		}
		u, err := url.Parse(links.VlessURL)
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		extra := u.Query().Get("extra")
		if extra == "" {
			t.Fatalf("expected non-empty extra for stream-one balanced with xmux/padding")
		}
		for _, forbidden := range []string{
			"sessionPlacement", "sessionKey", "sessionIDPlacement", "sessionIDKey",
			"seqPlacement", "seqKey", "scMaxEachPostBytes", "scMaxBufferedPosts",
			"streamUpServerSecs", "scStreamUpServerSecs", "enableXmux", "cMaxReuseTimes",
			"uplinkHTTPMethod",
		} {
			if strings.Contains(extra, forbidden) {
				t.Errorf("stream-one extra unexpectedly contains %q: %s", forbidden, extra)
			}
		}
		if _, err := vlink.ParseLink(links.VlessURL); err != nil {
			t.Fatalf("vlink.ParseLink failed on compact stream-one link: %v\nurl=%s", err, links.VlessURL)
		}
	})

	t.Run("packet_up_cdn_get_keeps_canonical_session_id_keys", func(t *testing.T) {
		tun := DefaultCDNGetTuning()
		links, err := BuildShareLinks(Config{
			PublicDomain: "edge.example.com",
			PublicPort:   443,
			Path:         "/cdn-bridge",
			Transport:    "xhttp",
			Mode:         "packet-up",
			XHTTPTuning:  &tun,
		}, client)
		if err != nil {
			t.Fatalf("BuildShareLinks: %v", err)
		}
		u, err := url.Parse(links.VlessURL)
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		extra := u.Query().Get("extra")
		var decoded map[string]any
		if err := json.Unmarshal([]byte(extra), &decoded); err != nil {
			t.Fatalf("unmarshal extra JSON: %v\nextra=%s", err, extra)
		}
		if _, ok := decoded["sessionPlacement"]; ok {
			t.Errorf("legacy sessionPlacement must be omitted: %s", extra)
		}
		if _, ok := decoded["sessionKey"]; ok {
			t.Errorf("legacy sessionKey must be omitted: %s", extra)
		}
		if decoded["sessionIDPlacement"] != "query" || decoded["sessionIDKey"] != "sid" {
			t.Errorf("expected canonical sessionIDPlacement/sessionIDKey, got %+v", decoded)
		}
		if _, err := vlink.ParseLink(links.VlessURL); err != nil {
			t.Fatalf("vlink.ParseLink failed on packet-up cdn_get link: %v\nurl=%s", err, links.VlessURL)
		}
	})

	t.Run("stream_up_uses_canonical_scStreamUpServerSecs", func(t *testing.T) {
		tun := DefaultBalancedTuning()
		links, err := BuildShareLinks(Config{
			PublicDomain: "up.example.com",
			PublicPort:   443,
			Path:         "/cdn-bridge",
			Transport:    "xhttp",
			Mode:         "stream-up",
			XHTTPTuning:  &tun,
		}, client)
		if err != nil {
			t.Fatalf("BuildShareLinks: %v", err)
		}
		u, err := url.Parse(links.VlessURL)
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		extra := u.Query().Get("extra")
		if !strings.Contains(extra, "scStreamUpServerSecs") {
			t.Errorf("expected canonical scStreamUpServerSecs in stream-up extra: %s", extra)
		}
		if _, err := vlink.ParseLink(links.VlessURL); err != nil {
			t.Fatalf("vlink.ParseLink failed on stream-up link: %v\nurl=%s", err, links.VlessURL)
		}
	})
}

func TestService_FactoryReset(t *testing.T) {
	tmpDir := t.TempDir()
	svc := New(tmpDir, nil)

	var fwCalls [][2]any
	svc.mu.Lock()
	svc.realityFirewall = func(port int, open bool) {
		fwCalls = append(fwCalls, [2]any{port, open})
	}
	svc.realityFWPort = 2053
	svc.realityFWSynced = true
	svc.recoveryRequired = true
	svc.recoveryReason = "corrupt state"
	svc.mu.Unlock()

	xrayDir := filepath.Join(tmpDir, "xray")
	if err := os.MkdirAll(filepath.Join(xrayDir, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(xrayDir, "transactions", "tx-1"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"xray-server-settings.json",
		"reality-settings.json",
		"migration-marker.json",
		"runtime-decision.json",
		filepath.Join("secrets", "client.key"),
		filepath.Join("transactions", "tx-1", "snapshot-manifest.json"),
		"server-ingress.lock",
		"server-ingress-transaction.json",
	} {
		if err := os.WriteFile(filepath.Join(xrayDir, rel), []byte(`{"test":true}`), 0600); err != nil {
			t.Fatal(err)
		}
	}

	svc.mu.Lock()
	svc.config.Enabled = true
	svc.config.PublicDomain = "vless.example.com"
	svc.config.Clients = []Client{{ID: "uuid-1", Remark: "Phone", Enabled: true}}
	svc.reality.Enabled = true
	svc.reality.PrivateKey = "priv"
	svc.reality.PublicKey = "pub"
	svc.mu.Unlock()

	if err := svc.FactoryReset(context.Background()); err != nil {
		t.Fatalf("FactoryReset failed: %v", err)
	}

	// Config & reality state reset to defaults and recovery cleared
	cfg := svc.GetConfig()
	if cfg.Enabled || cfg.PublicDomain != "" || len(cfg.Clients) != 0 {
		t.Fatalf("expected clean default config after FactoryReset, got %+v", cfg)
	}
	if svc.IsRecoveryRequired() {
		t.Fatal("expected recoveryRequired to be cleared")
	}
	rs := svc.GetRealityState(context.Background(), false)
	if rs.Settings.Enabled || rs.Settings.PublicKey != "" {
		t.Fatalf("expected clean default reality state after FactoryReset, got %+v", rs.Settings)
	}

	// VLESS files must be removed; coordinator files must remain intact
	for _, gone := range []string{
		"xray-server-settings.json",
		"reality-settings.json",
		"migration-marker.json",
		"runtime-decision.json",
		filepath.Join("secrets", "client.key"),
		"transactions",
	} {
		if _, err := os.Stat(filepath.Join(xrayDir, gone)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be deleted during FactoryReset", gone)
		}
	}
	for _, kept := range []string{"server-ingress.lock", "server-ingress-transaction.json"} {
		if _, err := os.Stat(filepath.Join(xrayDir, kept)); err != nil {
			t.Fatalf("expected coordinator file %s to be preserved: %v", kept, err)
		}
	}
	if len(fwCalls) == 0 || fwCalls[len(fwCalls)-1][0] != 0 || fwCalls[len(fwCalls)-1][1] != false {
		t.Fatalf("expected realityFirewall(0, false) call, got %+v", fwCalls)
	}
}
