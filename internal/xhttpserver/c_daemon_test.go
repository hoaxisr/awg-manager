package xhttpserver

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCDaemon_HelperProcess(t *testing.T) {
	mode := os.Getenv("GO_TEST_CDAEMON_HELPER_MODE")
	if mode == "" {
		return
	}
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-version" {
			switch mode {
			case "bad_version":
				fmt.Println("awgm-xhttpd 0.0.1-broken")
				os.Exit(0)
			case "bad_banner":
				fmt.Println("not-xhttpd 1.0.0")
				os.Exit(0)
			default:
				fmt.Printf("awgm-xhttpd %s\n", ExpectedVersion)
				os.Exit(0)
			}
		}
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

	switch mode {
	case "exit_early":
		os.Exit(1)
	case "wrong_pid":
		ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
		if err != nil {
			os.Exit(4)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/.xhttpd/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"ok","engine":"c/1","version":%q,"pid":999999,"capabilities":["vless-xhttp","finiteproto-v2"]}`+"\n", ExpectedVersion)
		})
		_ = http.Serve(ln, mux)
	case "missing_cap":
		ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
		if err != nil {
			os.Exit(4)
		}
		pid := os.Getpid()
		mux := http.NewServeMux()
		mux.HandleFunc("/.xhttpd/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"ok","engine":"c/1","version":%q,"pid":%d,"capabilities":["vless-xhttp"]}`+"\n", ExpectedVersion, pid)
		})
		_ = http.Serve(ln, mux)
	case "healthy":
		ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
		if err != nil {
			os.Exit(4)
		}
		pid := os.Getpid()
		mux := http.NewServeMux()
		mux.HandleFunc("/.xhttpd/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"ok","engine":"c/1","version":%q,"pid":%d,"capabilities":["vless-xhttp","finiteproto-v2"]}`+"\n", ExpectedVersion, pid)
		})
		mux.HandleFunc("/.xhttpd/reload", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
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
	}
	os.Exit(0)
}

func configureTestCDaemonHelper(t *testing.T, srv *Server, mode string) {
	t.Helper()
	testBin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	srv.cBinPath = testBin
	srv.cArgs = []string{"-test.run=^TestCDaemon_HelperProcess$", "--"}
	srv.cEnv = []string{"GO_TEST_CDAEMON_HELPER_MODE=" + mode}
	srv.cConfPath = filepath.Join(t.TempDir(), "awgm-xhttpd.conf")
}

func findFreeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func assertPortNotListening(t *testing.T, addr string) {
	t.Helper()
	if strings.HasSuffix(addr, ":0") || addr == "" {
		return
	}
	c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err == nil {
		_ = c.Close()
		t.Fatalf("expected no listener on %s after failed C start, but connection succeeded", addr)
	}
}

func TestCDaemon_MandatoryCBackendAndNoGoFallbackOnEnvOrMissingBin(t *testing.T) {
	tmpDir := t.TempDir()
	missingBin := filepath.Join(tmpDir, "missing-awgm-xhttpd")

	// 1. Regardless of AWGM_ENABLE_C_XHTTPD (unset, "", "0", "1"), C daemon is ALWAYS used.
	for _, enableVal := range []string{"<unset>", "", "0", "1"} {
		t.Run("enable_"+enableVal, func(t *testing.T) {
			if enableVal == "<unset>" {
				t.Setenv("AWGM_ENABLE_C_XHTTPD", "")
				_ = os.Unsetenv("AWGM_ENABLE_C_XHTTPD")
			} else {
				t.Setenv("AWGM_ENABLE_C_XHTTPD", enableVal)
			}
			t.Setenv("AWGM_DISABLE_C_XHTTPD", "")

			addr := findFreeLocalAddr(t)
			srv := New(Config{
				ListenAddr: addr,
				PathPrefix: "/cdn-bridge",
				Clients:    []string{"41b7ef34-6b19-48f3-8d95-82cf4474e109"},
			})
			configureTestCDaemonHelper(t, srv, "healthy")
			if err := srv.Start(); err != nil {
				t.Fatalf("Start() failed with AWGM_ENABLE_C_XHTTPD=%q: %v", enableVal, err)
			}
			defer srv.Stop()

			if srv.CDaemonPID() <= 0 {
				t.Fatalf("expected C child process PID > 0, got %d", srv.CDaemonPID())
			}
		})
	}

	// 2. AWGM_DISABLE_C_XHTTPD=1 MUST fail closed with ErrCDaemonDisabled and NEVER fall back to Go.
	for _, disableVal := range []string{"1", "true"} {
		t.Run("disable_"+disableVal, func(t *testing.T) {
			t.Setenv("AWGM_ENABLE_C_XHTTPD", "1")
			t.Setenv("AWGM_DISABLE_C_XHTTPD", disableVal)

			addr := findFreeLocalAddr(t)
			srv := New(Config{
				ListenAddr: addr,
				PathPrefix: "/cdn-bridge",
				Clients:    []string{"41b7ef34-6b19-48f3-8d95-82cf4474e109"},
			})
			configureTestCDaemonHelper(t, srv, "healthy")
			err := srv.Start()
			if !errors.Is(err, ErrCDaemonDisabled) {
				_ = srv.Stop()
				t.Fatalf("expected ErrCDaemonDisabled, got %v", err)
			}
			if srv.IsRunning() || srv.CDaemonPID() != 0 {
				t.Fatalf("expected server not running after ErrCDaemonDisabled")
			}
			assertPortNotListening(t, addr)
		})
	}

	// 3. Missing binary, directory path, or non-executable file MUST fail closed with ErrBinaryUnavailable.
	t.Setenv("AWGM_DISABLE_C_XHTTPD", "")
	for _, tc := range []struct {
		name string
		path string
	}{
		{"missing_file", missingBin},
		{"directory_path", tmpDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := findFreeLocalAddr(t)
			srv := New(Config{
				ListenAddr: addr,
				BinaryPath: tc.path,
				PathPrefix: "/cdn-bridge",
				Clients:    []string{"41b7ef34-6b19-48f3-8d95-82cf4474e109"},
			})
			err := srv.Start()
			if !errors.Is(err, ErrBinaryUnavailable) {
				_ = srv.Stop()
				t.Fatalf("expected ErrBinaryUnavailable for %s, got %v", tc.name, err)
			}
			assertPortNotListening(t, addr)
		})
	}

	if runtime.GOOS != "windows" {
		nonExec := filepath.Join(tmpDir, "nonexec-xhttpd")
		if err := os.WriteFile(nonExec, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
			t.Fatalf("write nonexec: %v", err)
		}
		addr := findFreeLocalAddr(t)
		srv := New(Config{
			ListenAddr: addr,
			BinaryPath: nonExec,
			PathPrefix: "/cdn-bridge",
			Clients:    []string{"41b7ef34-6b19-48f3-8d95-82cf4474e109"},
		})
		if err := srv.Start(); !errors.Is(err, ErrBinaryUnavailable) {
			_ = srv.Stop()
			t.Fatalf("expected ErrBinaryUnavailable for mode 0644 binary, got %v", err)
		}
		assertPortNotListening(t, addr)
	}
}

func TestCDaemon_PortConflictDoesNotDisruptForeignService(t *testing.T) {
	t.Setenv("AWGM_DISABLE_C_XHTTPD", "")

	// Start a foreign TCP service and establish an active connection to it.
	foreignLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("foreign listen: %v", err)
	}
	defer foreignLn.Close()

	go func() {
		for {
			c, err := foreignLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	activeConn, err := net.DialTimeout("tcp", foreignLn.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial foreign service: %v", err)
	}
	defer activeConn.Close()

	if _, err := activeConn.Write([]byte("before-start")); err != nil {
		t.Fatalf("write before-start: %v", err)
	}
	buf := make([]byte, len("before-start"))
	if _, err := io.ReadFull(activeConn, buf); err != nil || string(buf) != "before-start" {
		t.Fatalf("read before-start failed: %v %q", err, string(buf))
	}

	// Attempt to start xhttpserver on the same address -> MUST return ErrPortConflict without killing or disturbing foreignLn.
	srv := New(Config{
		ListenAddr: foreignLn.Addr().String(),
		PathPrefix: "/cdn-bridge",
		Clients:    []string{"41b7ef34-6b19-48f3-8d95-82cf4474e109"},
	})
	configureTestCDaemonHelper(t, srv, "healthy")
	startErr := srv.Start()
	if !errors.Is(startErr, ErrPortConflict) {
		_ = srv.Stop()
		t.Fatalf("expected ErrPortConflict when foreign listener owns %s, got %v", foreignLn.Addr(), startErr)
	}

	// Verify existing TCP connection AND new connections to foreignLn remain completely intact.
	if _, err := activeConn.Write([]byte("after-conflict")); err != nil {
		t.Fatalf("existing connection broken after conflict check: %v", err)
	}
	buf2 := make([]byte, len("after-conflict"))
	if _, err := io.ReadFull(activeConn, buf2); err != nil || string(buf2) != "after-conflict" {
		t.Fatalf("existing connection echo failed after conflict check: %v %q", err, string(buf2))
	}
}

func TestCDaemon_DeterministicClientOrder(t *testing.T) {
	u1, _ := ParseUUID("ffffffff-1111-2222-3333-444444444444")
	u2, _ := ParseUUID("11111111-2222-3333-4444-555555555555")
	u3, _ := ParseUUID("88888888-2222-3333-4444-555555555555")

	allowed := map[[16]byte]struct{}{
		u1: {},
		u2: {},
		u3: {},
	}
	cfg := Config{ListenAddr: "127.0.0.1:9008"}

	first := renderCXHTTPDConfig(cfg, "127.0.0.1:9008", []string{"/cdn-bridge"}, allowed, nil)
	for i := 0; i < 20; i++ {
		got := renderCXHTTPDConfig(cfg, "127.0.0.1:9008", []string{"/cdn-bridge"}, allowed, nil)
		if got != first {
			t.Fatalf("non-deterministic config render on iteration %d:\ngot:\n%s\nwant:\n%s", i, got, first)
		}
	}

	idx1 := strings.Index(first, "client=11111111-2222-3333-4444-555555555555")
	idx2 := strings.Index(first, "client=88888888-2222-3333-4444-555555555555")
	idx3 := strings.Index(first, "client=ffffffff-1111-2222-3333-444444444444")
	if idx1 < 0 || idx2 < 0 || idx3 < 0 || !(idx1 < idx2 && idx2 < idx3) {
		t.Fatalf("clients not sorted lexicographically:\n%s", first)
	}
}

func TestCDaemon_HealthPIDCapabilitiesAndCrashDetection(t *testing.T) {
	t.Setenv("AWGM_DISABLE_C_XHTTPD", "")
	addr := findFreeLocalAddr(t)

	srv := New(Config{
		ListenAddr: addr,
		PathPrefix: "/cdn-bridge",
		Clients:    []string{"41b7ef34-6b19-48f3-8d95-82cf4474e109"},
	})
	configureTestCDaemonHelper(t, srv, "healthy")

	if err := srv.Start(); err != nil {
		t.Fatalf("Start with healthy C daemon failed: %v", err)
	}
	defer srv.Stop()
	exited := make(chan struct{}, 1)
	srv.SetOnUnexpectedExit(func() { exited <- struct{}{} })

	if srv.CDaemonPID() <= 0 || !srv.IsRunning() {
		t.Fatalf("expected running C daemon, pid=%d running=%v", srv.CDaemonPID(), srv.IsRunning())
	}
	if srv.CDaemonVersion() != ExpectedVersion {
		t.Fatalf("expected version %q, got %q", ExpectedVersion, srv.CDaemonVersion())
	}

	// Simulate unexpected child crash -> waiter goroutine must mark IsRunning() == false
	srv.srvMu.Lock()
	proc := srv.cCmd.Process
	srv.srvMu.Unlock()
	_ = proc.Kill()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !srv.IsRunning() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if srv.IsRunning() || srv.CDaemonPID() != 0 {
		t.Fatalf("expected IsRunning()==false and PID==0 after crash, got running=%v pid=%d", srv.IsRunning(), srv.CDaemonPID())
	}
	// F824: о падении узнаёт супервизор (vlessserver), иначе демон лежал бы
	// до ручного пересохранения.
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("unexpected exit hook not called after crash")
	}

	// Штатный Stop — не падение: колбэк молчит.
	if err := srv.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	_ = srv.Stop()
	select {
	case <-exited:
		t.Fatal("exit hook fired on deliberate Stop")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestCDaemon_RejectEarlyExitForeignPIDAndMissingCapability(t *testing.T) {
	t.Setenv("AWGM_DISABLE_C_XHTTPD", "")

	for _, mode := range []string{"exit_early", "wrong_pid", "missing_cap"} {
		t.Run(mode, func(t *testing.T) {
			addr := findFreeLocalAddr(t)
			srv := New(Config{
				ListenAddr: addr,
				PathPrefix: "/cdn-bridge",
				Clients:    []string{"41b7ef34-6b19-48f3-8d95-82cf4474e109"},
			})
			configureTestCDaemonHelper(t, srv, mode)
			if err := srv.Start(); err == nil {
				_ = srv.Stop()
				t.Fatalf("expected Start() to fail for helper mode %q", mode)
			}
			assertPortNotListening(t, addr)
		})
	}
}

func TestCDaemon_ReloadErrorHandling(t *testing.T) {
	srv := New(Config{
		ListenAddr: "127.0.0.1:0",
		Clients:    []string{"41b7ef34-6b19-48f3-8d95-82cf4474e109"},
	})
	srv.cConfPath = filepath.Join(t.TempDir(), "reload.conf")
	srv.cAddr = "127.0.0.1:1"
	srv.cReloadToken = "test-token-12345"

	fakeCmd := exec.Command("cmd", "/c", "exit 0")
	if err := fakeCmd.Start(); err != nil {
		fakeCmd = exec.Command("sh", "-c", "exit 0")
		_ = fakeCmd.Start()
	}
	defer func() {
		if fakeCmd.Process != nil {
			_ = fakeCmd.Process.Kill()
		}
	}()
	srv.cCmd = fakeCmd

	if err := srv.reloadCDaemonConfig(); err == nil {
		t.Fatalf("expected reload error when cAddr is unreachable")
	}

	var returnStatus int
	var returnBody string
	var lastReqToken string
	var lastReqMethod string

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastReqMethod = r.Method
		lastReqToken = r.Header.Get("X-Awgm-Reload-Token")
		w.WriteHeader(returnStatus)
		_, _ = w.Write([]byte(returnBody))
	}))
	defer mockSrv.Close()

	srv.cAddr = mockSrv.Listener.Addr().String()

	returnStatus = http.StatusUnauthorized
	returnBody = "reload unauthorized"
	if err := srv.reloadCDaemonConfig(); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 reload error, got %v", err)
	}
	if lastReqToken != "test-token-12345" || lastReqMethod != http.MethodPost {
		t.Fatalf("unexpected reload request method=%q token=%q", lastReqMethod, lastReqToken)
	}

	returnStatus = http.StatusInternalServerError
	returnBody = "reload failed"
	if err := srv.reloadCDaemonConfig(); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 reload error, got %v", err)
	}

	returnStatus = http.StatusOK
	returnBody = "Reload OK"
	if err := srv.reloadCDaemonConfig(); err != nil {
		t.Fatalf("expected nil error on 200 OK, got %v", err)
	}
}
