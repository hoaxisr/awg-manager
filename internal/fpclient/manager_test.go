package fpclient

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/testutil"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver/xhttptest"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("GO_TEST_FPC_HELPER_MODE"); mode != "" {
		runFPCHelperProcess(mode)
		return
	}
	os.Exit(m.Run())
}

func runFPCHelperProcess(mode string) {
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-version" {
			switch mode {
			case "bad_version":
				fmt.Println("awgm-fpc 0.0.1-broken")
				os.Exit(0)
			case "bad_banner":
				fmt.Println("something-else 1.0.0-c")
				os.Exit(0)
			default:
				fmt.Printf("awgm-fpc %s\n", ExpectedVersion)
				os.Exit(0)
			}
		}
	}
	if mode == "exit_early" {
		fmt.Fprintln(os.Stderr, "simulated awgm-fpc early startup failure")
		os.Exit(2)
	}

	listenAddr := "127.0.0.1:0"
	for i := 1; i < len(os.Args)-1; i++ {
		if os.Args[i] == "-listen" {
			listenAddr = os.Args[i+1]
		}
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper listen %s: %v\n", listenAddr, err)
		os.Exit(1)
	}
	defer ln.Close()
	for {
		c, err := ln.Accept()
		if err != nil {
			os.Exit(0)
		}
		_ = c.Close()
	}
}

func makeFakeFPCBinary(t *testing.T, mode string) string {
	t.Helper()
	testExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv("GO_TEST_FPC_HELPER_MODE", mode)
	dir := t.TempDir()
	name := "awgm-fpc"
	if runtime.GOOS == "windows" {
		name = "awgm-fpc.exe"
	}
	dst := filepath.Join(dir, name)
	raw, err := os.ReadFile(testExe)
	if err != nil {
		t.Fatalf("read testExe: %v", err)
	}
	if err := os.WriteFile(dst, raw, 0o755); err != nil {
		t.Fatalf("write fake awgm-fpc: %v", err)
	}
	return dst
}

func TestParseAndEnsureVLESSFiniteProtoV2AndLegacyRejection(t *testing.T) {
	SetStorePath(filepath.Join(t.TempDir(), "fp-clients.json"))
	t.Cleanup(stopAllEndpoints)

	uuidStr := "01234567-89ab-4def-8123-456789abcdef"
	cleanUUID := strings.ReplaceAll(uuidStr, "-", "")
	rawUUIDBytes, _ := hex.DecodeString(cleanUUID)
	v1Sum := sha256.Sum256([]byte("awgm-fp-v1:" + uuidStr))
	rawSum := sha256.Sum256(rawUUIDBytes)

	// 1. Legacy v1 UUID-only link MUST be rejected with ErrLegacyFPMigrationRequired
	legacyURI := "vless://" + uuidStr + "@cdn.example.com:443?encryption=none&security=tls&sni=cdn.example.com&type=xhttp&host=cdn.example.com&path=%2Fcdn-bridge&mode=finiteproto#Home-FP"
	if spec, isFP, err := ParseVLESSFiniteProto(legacyURI); !isFP || spec != nil || !errors.Is(err, ErrLegacyFPMigrationRequired) {
		t.Fatalf("expected ErrLegacyFPMigrationRequired for legacy v1 link, got isFP=%v spec=%+v err=%v", isFP, spec, err)
	}

	// 2. Legacy fp:// without secret MUST be rejected with ErrLegacyFPMigrationRequired
	legacyDirect := "fp://" + uuidStr + "@cdn.example.com:443?path=%2Fcdn-bridge#Legacy-Direct"
	if spec, isFP, err := ParseVLESSFiniteProto(legacyDirect); !isFP || spec != nil || !errors.Is(err, ErrLegacyFPMigrationRequired) {
		t.Fatalf("expected ErrLegacyFPMigrationRequired for legacy fp link, got isFP=%v spec=%+v err=%v", isFP, spec, err)
	}

	// 3. All UUID-derived secret variants MUST be rejected (with fp_v=2 or omitted)
	uuidDerivedHexVariants := []string{
		hex.EncodeToString(v1Sum[:]),
		hex.EncodeToString(rawSum[:]),
		hex.EncodeToString([]byte(cleanUUID)),
	}
	for i, badHex := range uuidDerivedHexVariants {
		u := fmt.Sprintf("vless://%s@cdn.example.com:443?security=tls&type=xhttp&path=%%2Fcdn-bridge&mode=finiteproto&fp_v=2&fp_id=fp_test_%d&fp_secret=%s",
			uuidStr, i, badHex)
		if spec, isFP, err := ParseVLESSFiniteProto(u); !isFP || spec != nil || !errors.Is(err, ErrLegacyFPMigrationRequired) {
			t.Fatalf("variant %d with fp_v=2: expected ErrLegacyFPMigrationRequired, got spec=%+v err=%v", i, spec, err)
		}
		uNoV := fmt.Sprintf("vless://%s@cdn.example.com:443?security=tls&type=xhttp&path=%%2Fcdn-bridge&mode=finiteproto&fp_id=fp_test_%d&fp_secret=%s",
			uuidStr, i, badHex)
		if spec, isFP, err := ParseVLESSFiniteProto(uNoV); !isFP || spec != nil || !errors.Is(err, ErrLegacyFPMigrationRequired) {
			t.Fatalf("variant %d without fp_v: expected ErrLegacyFPMigrationRequired, got spec=%+v err=%v", i, spec, err)
		}
	}

	// 3b. Non-v2 explicit fp_v values MUST be rejected with ErrLegacyFPMigrationRequired even with valid credentials
	goodID, goodSecretHex, _, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatalf("GenerateFPCredentials: %v", err)
	}
	for _, badVer := range []string{"1", "0", "3", "legacy"} {
		uBadVer := fmt.Sprintf("vless://%s@cdn.example.com:443?security=tls&type=xhttp&path=%%2Fcdn-bridge&mode=finiteproto&fp_v=%s&fp_id=%s&fp_secret=%s",
			uuidStr, badVer, goodID, goodSecretHex)
		if spec, isFP, err := ParseVLESSFiniteProto(uBadVer); !isFP || spec != nil || !errors.Is(err, ErrLegacyFPMigrationRequired) {
			t.Fatalf("fp_v=%s: expected ErrLegacyFPMigrationRequired, got isFP=%v spec=%+v err=%v", badVer, isFP, spec, err)
		}
	}

	// 4. Non-64-hex fp_secret MUST be rejected with ErrInvalidFPCredentials
	shortSecretURI := fmt.Sprintf("vless://%s@cdn.example.com:443?security=tls&type=xhttp&path=%%2Fcdn-bridge&mode=finiteproto&fp_v=2&fp_id=fp_short&fp_secret=%s",
		uuidStr, hex.EncodeToString(rawUUIDBytes))
	if spec, isFP, err := ParseVLESSFiniteProto(shortSecretURI); !isFP || spec != nil || !errors.Is(err, ErrInvalidFPCredentials) {
		t.Fatalf("expected ErrInvalidFPCredentials for 32-hex secret, got spec=%+v err=%v", spec, err)
	}

	// 5. Valid v2 random credentials succeed and round-trip cleanly via ExportVLESSFiniteProtoURI and ExportDirectFPURI
	fpID, fpSecretHex, _, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatalf("GenerateFPCredentials: %v", err)
	}
	validV2URI := fmt.Sprintf(
		"vless://%s@cdn.example.com:443?encryption=none&security=tls&sni=cdn.example.com&type=xhttp&host=cdn.example.com&path=%%2Fcdn-bridge&mode=finiteproto&fp_v=2&fp_id=%s&fp_secret=%s#Home-FP",
		uuidStr, fpID, fpSecretHex,
	)
	spec, isFP, err := ParseVLESSFiniteProto(validV2URI)
	if err != nil {
		t.Fatalf("ParseVLESSFiniteProto v2 error: %v", err)
	}
	if !isFP || spec == nil {
		t.Fatalf("expected isFP=true and non-nil spec")
	}
	if spec.FPClientID != fpID || spec.FPSecretHex != fpSecretHex {
		t.Fatalf("credential mismatch in spec")
	}

	exportedVLESS, err := ExportVLESSFiniteProtoURI(*spec, "Exported-VLESS")
	if err != nil {
		t.Fatalf("ExportVLESSFiniteProtoURI: %v", err)
	}
	reSpec1, isFP1, err := ParseVLESSFiniteProto(exportedVLESS)
	if err != nil || !isFP1 || reSpec1 == nil || reSpec1.FPClientID != fpID || reSpec1.FPSecretHex != fpSecretHex {
		t.Fatalf("round-trip VLESS v2 failed: err=%v", err)
	}

	exportedDirect, err := ExportDirectFPURI(*spec, "Exported-Direct")
	if err != nil {
		t.Fatalf("ExportDirectFPURI: %v", err)
	}
	reSpec2, isFP2, err := ParseVLESSFiniteProto(exportedDirect)
	if err != nil || !isFP2 || reSpec2 == nil || reSpec2.FPClientID != fpID || reSpec2.FPSecretHex != fpSecretHex {
		t.Fatalf("round-trip fp:// v2 failed: err=%v", err)
	}
}

func TestMandatoryCFPCAndNoGoFallback(t *testing.T) {
	SetStorePath(filepath.Join(t.TempDir(), "fp-mandatory.json"))
	t.Cleanup(stopAllEndpoints)

	fpID, fpSecretHex, _, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatalf("GenerateFPCredentials: %v", err)
	}
	spec := EndpointSpec{
		Key:         "mandatory-c-fpc",
		Server:      "127.0.0.1",
		ServerPort:  443,
		UUID:        "01234567-89ab-4def-8123-456789abcdef",
		FPClientID:  fpID,
		FPSecretHex: fpSecretHex,
		Host:        "cdn.example.com",
		Path:        "/cdn-bridge",
		TLS:         false,
		SocksPort:   0,
	}

	t.Run("missing_binary_fails_closed_without_go_fallback", func(t *testing.T) {
		t.Setenv("AWGM_DISABLE_C_FPC", "0")
		t.Setenv("AWGM_C_FPC_BIN", filepath.Join(t.TempDir(), "nonexistent-awgm-fpc"))
		if _, err := EnsureEndpoint(spec); !errors.Is(err, ErrCDaemonUnavailable) {
			t.Fatalf("expected ErrCDaemonUnavailable when binary missing, got %v", err)
		}
	})

	t.Run("disable_env_fails_closed", func(t *testing.T) {
		fakeBin := makeFakeFPCBinary(t, "ok")
		t.Setenv("AWGM_C_FPC_BIN", fakeBin)
		t.Setenv("AWGM_DISABLE_C_FPC", "1")
		if _, err := EnsureEndpoint(spec); !errors.Is(err, ErrCDaemonDisabled) {
			t.Fatalf("expected ErrCDaemonDisabled when AWGM_DISABLE_C_FPC=1, got %v", err)
		}
	})

	t.Run("version_mismatch_fails_closed", func(t *testing.T) {
		badBin := makeFakeFPCBinary(t, "bad_version")
		t.Setenv("AWGM_DISABLE_C_FPC", "0")
		t.Setenv("AWGM_C_FPC_BIN", badBin)
		if _, err := EnsureEndpoint(spec); !errors.Is(err, ErrCDaemonUnavailable) {
			t.Fatalf("expected ErrCDaemonUnavailable on version mismatch, got %v", err)
		}
	})

	t.Run("early_exit_fails_closed", func(t *testing.T) {
		exitBin := makeFakeFPCBinary(t, "exit_early")
		t.Setenv("AWGM_DISABLE_C_FPC", "0")
		t.Setenv("AWGM_C_FPC_BIN", exitBin)
		if _, err := EnsureEndpoint(spec); err == nil || !strings.Contains(err.Error(), "exited during startup") {
			t.Fatalf("expected early exit startup error, got %v", err)
		}
	})

	t.Run("verified_c_binary_spawns_and_avoids_occupied_foreign_port", func(t *testing.T) {
		foreignLn, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("foreign listen: %v", err)
		}
		defer foreignLn.Close()
		foreignPort := foreignLn.Addr().(*net.TCPAddr).Port
		foreignTouched := make(chan struct{}, 1)
		go func() {
			c, err := foreignLn.Accept()
			if err == nil {
				_ = c.Close()
				select {
				case foreignTouched <- struct{}{}:
				default:
				}
			}
		}()

		okBin := makeFakeFPCBinary(t, "ok")
		t.Setenv("AWGM_DISABLE_C_FPC", "0")
		t.Setenv("AWGM_C_FPC_BIN", okBin)
		conflictSpec := spec
		conflictSpec.Key = "port-conflict-safe"
		conflictSpec.SocksPort = foreignPort

		gotPort, err := EnsureEndpoint(conflictSpec)
		if err != nil {
			t.Fatalf("EnsureEndpoint with verified C binary: %v", err)
		}
		defer stopAllEndpoints()
		if gotPort <= 0 || gotPort == foreignPort {
			t.Fatalf("expected relocated port != %d, got %d", foreignPort, gotPort)
		}
		select {
		case <-foreignTouched:
			t.Fatalf("EnsureEndpoint probed or disrupted foreign listener on port %d!", foreignPort)
		case <-time.After(100 * time.Millisecond):
		}
	})

	t.Run("repeated_isAlive_and_fast_restart_after_daemon_crash", func(t *testing.T) {
		crashBin := makeFakeFPCBinary(t, "ok")
		t.Setenv("AWGM_DISABLE_C_FPC", "0")
		t.Setenv("AWGM_C_FPC_BIN", crashBin)
		crashSpec := spec
		crashSpec.Key = "crash-restart-safe"
		crashSpec.SocksPort = 0

		port1, err := EnsureEndpoint(crashSpec)
		if err != nil {
			t.Fatalf("initial EnsureEndpoint: %v", err)
		}
		if port1 <= 0 {
			t.Fatalf("invalid initial port %d", port1)
		}

		globalMu.Lock()
		inst := globalInstances[crashSpec.Key]
		globalMu.Unlock()
		if inst == nil {
			t.Fatalf("expected instance in globalInstances")
		}
		// Крах по команде теста, а не по часам: демон, умирающий сам, под
		// нагрузкой успевает умереть ещё до готовности.
		if err := inst.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		testutil.WaitFor(t, func() bool { return !inst.isAlive() }, "isAlive() не заметил краха демона")
		if inst.isAlive() {
			t.Fatalf("expected second isAlive() after crash to still return false (closed doneCh)")
		}

		// Verify stop() on already-exited daemon returns immediately without 1s channel stall
		tStop := time.Now()
		inst.stop()
		if stopElapsed := time.Since(tStop); stopElapsed >= time.Second {
			t.Fatalf("inst.stop() stalled %v cleaning up exited daemon", stopElapsed)
		}

		// Switch to healthy binary and ensure EnsureEndpoint replaces the exited instance cleanly
		okBin := makeFakeFPCBinary(t, "ok")
		t.Setenv("AWGM_C_FPC_BIN", okBin)
		port2, err := EnsureEndpoint(crashSpec)
		if err != nil {
			t.Fatalf("restart EnsureEndpoint: %v", err)
		}
		defer stopAllEndpoints()
		if port2 <= 0 {
			t.Fatalf("invalid restarted port %d", port2)
		}
	})
}

func dialSocks5Echo(socksPort, echoPort int, payload string) error {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	var greet [2]byte
	if _, err := io.ReadFull(c, greet[:]); err != nil || greet[0] != 0x05 || greet[1] != 0x00 {
		return fmt.Errorf("socks greet failed: %v %x", err, greet)
	}
	var req [10]byte
	req[0] = 0x05
	req[1] = 0x01
	req[2] = 0x00
	req[3] = 0x01
	copy(req[4:8], net.ParseIP("127.0.0.1").To4())
	binary.BigEndian.PutUint16(req[8:10], uint16(echoPort))
	if _, err := c.Write(req[:]); err != nil {
		return err
	}
	var rep [10]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		return err
	}
	if rep[1] != 0x00 {
		return fmt.Errorf("socks connect failed with rep=%d", rep[1])
	}
	if _, err := c.Write([]byte(payload)); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		return err
	}
	if string(got) != payload {
		return fmt.Errorf("echo mismatch: got %q, want %q", string(got), payload)
	}
	return nil
}

func TestCClientToCBackendEndToEndAndRevocation(t *testing.T) {
	cServerBin := xhttptest.RequireCXHTTPDBinary(t)
	cClientBin := xhttptest.RequireCFPCBinary(t)

	t.Setenv("AWGM_DISABLE_C_XHTTPD", "0")
	t.Setenv("AWGM_DISABLE_C_FPC", "0")
	t.Setenv("AWGM_C_XHTTPD_BIN", cServerBin)
	t.Setenv("AWGM_C_FPC_BIN", cClientBin)

	SetStorePath(filepath.Join(t.TempDir(), "fp-c-to-c-e2e.json"))
	t.Cleanup(stopAllEndpoints)

	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	echoPort := echoLn.Addr().(*net.TCPAddr).Port

	const uuidStr = "b831381d-6324-4d53-ad4f-8cda48b30811"
	fpID, secretHex, secretBytes, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatalf("GenerateFPCredentials: %v", err)
	}

	srv := xhttpserver.New(xhttpserver.Config{
		ListenAddr: testutil.FreeAddr(t),
		BinaryPath: cServerBin,
		ConfigPath: filepath.Join(t.TempDir(), "awgm-xhttpd-e2e.conf"),
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidStr},
		FiniteCredentials: map[string][]byte{
			fpID: secretBytes,
		},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start (C backend): %v", err)
	}
	defer srv.Stop()
	defer stopAllEndpoints()

	if srv.CDaemonPID() <= 0 {
		t.Fatalf("expected C daemon PID > 0, got %d", srv.CDaemonPID())
	}

	_, portStr, _ := net.SplitHostPort(srv.Addr())
	srvPort, _ := strconv.Atoi(portStr)

	socksPort, err := EnsureEndpoint(EndpointSpec{
		Server:      "127.0.0.1",
		ServerPort:  srvPort,
		UUID:        uuidStr,
		FPClientID:  fpID,
		FPSecretHex: secretHex,
		Host:        "cdn.example.com",
		Path:        "/cdn-bridge",
		TLS:         false,
	})
	if err != nil {
		t.Fatalf("EnsureEndpoint (C client): %v", err)
	}

	if err := dialSocks5Echo(socksPort, echoPort, "finiteproto-v2-c-client-to-c-backend"); err != nil {
		t.Fatalf("C client -> C backend E2E failed: %v", err)
	}

	// Open a persistent SOCKS5 session on C backend before credential rotation
	liveConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), 5*time.Second)
	if err != nil {
		t.Fatalf("liveConn dial: %v", err)
	}
	defer liveConn.Close()
	_ = liveConn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := liveConn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("liveConn greet write: %v", err)
	}
	var greet [2]byte
	if _, err := io.ReadFull(liveConn, greet[:]); err != nil || greet[0] != 0x05 || greet[1] != 0x00 {
		t.Fatalf("liveConn greet read: %v %x", err, greet)
	}
	var req [10]byte
	req[0] = 0x05
	req[1] = 0x01
	req[2] = 0x00
	req[3] = 0x01
	copy(req[4:8], net.ParseIP("127.0.0.1").To4())
	binary.BigEndian.PutUint16(req[8:10], uint16(echoPort))
	if _, err := liveConn.Write(req[:]); err != nil {
		t.Fatalf("liveConn req write: %v", err)
	}
	var rep [10]byte
	if _, err := io.ReadFull(liveConn, rep[:]); err != nil || rep[1] != 0x00 {
		t.Fatalf("liveConn rep read: %v %x", err, rep)
	}
	if _, err := liveConn.Write([]byte("c-live-pre")); err != nil {
		t.Fatalf("liveConn pre write: %v", err)
	}
	preBuf := make([]byte, 10)
	if _, err := io.ReadFull(liveConn, preBuf); err != nil || string(preBuf) != "c-live-pre" {
		t.Fatalf("liveConn pre read: %v %q", err, string(preBuf))
	}

	// Rotate credentials on the C backend via UpdateConfig (rewrites conf + SIGHUP)
	_, _, newSecret, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatalf("GenerateFPCredentials: %v", err)
	}
	if err := srv.UpdateConfig(xhttpserver.Config{
		ListenAddr: srv.Addr(),
		BinaryPath: cServerBin,
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidStr},
		FiniteCredentials: map[string][]byte{
			fpID: newSecret,
		},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	}); err != nil {
		t.Fatalf("UpdateConfig (C backend): %v", err)
	}
	time.Sleep(250 * time.Millisecond)

	_ = liveConn.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = liveConn.Write([]byte("c-live-post"))
	postBuf := make([]byte, 11)
	if _, err := io.ReadFull(liveConn, postBuf); err == nil {
		t.Fatalf("expected already-open C backend SOCKS stream to be closed after credential rotation, but read %q", string(postBuf))
	}

	if err := dialSocks5Echo(socksPort, echoPort, "should-fail-on-c-backend-after-rotation"); err == nil {
		t.Fatalf("expected SOCKS connect with rotated-out client secret on C backend to fail, but succeeded")
	}
}

func TestUniversalHTTPMethodsAndAutoFallback_CClientToCBackend(t *testing.T) {
	cServerBin := xhttptest.RequireCXHTTPDBinary(t)
	cClientBin := xhttptest.RequireCFPCBinary(t)

	t.Setenv("AWGM_DISABLE_C_XHTTPD", "0")
	t.Setenv("AWGM_DISABLE_C_FPC", "0")
	t.Setenv("AWGM_C_XHTTPD_BIN", cServerBin)
	t.Setenv("AWGM_C_FPC_BIN", cClientBin)

	SetStorePath(filepath.Join(t.TempDir(), "fp-universal-c.json"))
	t.Cleanup(stopAllEndpoints)

	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer echoLn.Close()
	echoPort := echoLn.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	uuidStr := "41b7ef34-6b19-48f3-8d95-82cf4474e109"
	fpID, fpSecretHex, fpSecret, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatalf("GenerateFPCredentials: %v", err)
	}

	srv := xhttpserver.New(xhttpserver.Config{
		ListenAddr: testutil.FreeAddr(t),
		BinaryPath: cServerBin,
		ConfigPath: filepath.Join(t.TempDir(), "awgm-xhttpd-universal.conf"),
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidStr},
		FiniteCredentials: map[string][]byte{
			fpID: fpSecret,
		},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	backendURL, err := url.Parse("http://" + srv.Addr())
	if err != nil {
		t.Fatalf("parse backend url: %v", err)
	}
	revProxy := httputil.NewSingleHostReverseProxy(backendURL)

	// Simulate a strict CDN that blocks POST with 405 Method Not Allowed in front of real C awgm-xhttpd
	cdnGate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte("405 Method Not Allowed by CDN"))
			return
		}
		revProxy.ServeHTTP(w, r)
	}))
	defer cdnGate.Close()

	tsURL, err := url.Parse(cdnGate.URL)
	if err != nil {
		t.Fatalf("parse ts URL: %v", err)
	}
	srvHost, srvPortStr, _ := net.SplitHostPort(tsURL.Host)
	srvPort, _ := strconv.Atoi(srvPortStr)

	for _, mode := range []string{"auto", "get", "head_get", "options", "delete", "head"} {
		stopAllEndpoints()
		uri := fmt.Sprintf(
			"vless://%s@%s:%d?security=none&type=xhttp&host=%s&path=%%2Fcdn-bridge&mode=finiteproto&fp_v=2&fp_id=%s&fp_secret=%s&fp_method=%s#Mode-%s",
			uuidStr, srvHost, srvPort, srvHost, fpID, fpSecretHex, mode, mode,
		)
		spec, isFP, err := ParseVLESSFiniteProto(uri)
		if err != nil || !isFP || spec == nil {
			t.Fatalf("mode %s: ParseVLESSFiniteProto err=%v", mode, err)
		}
		if spec.Method != mode {
			t.Fatalf("mode %s: expected spec.Method=%q, got %q", mode, mode, spec.Method)
		}
		socksPort, err := EnsureEndpoint(*spec)
		if err != nil {
			t.Fatalf("mode %s: EnsureEndpoint err=%v", mode, err)
		}
		msg := "universal-cdn-mode-" + mode
		if err := dialSocks5Echo(socksPort, echoPort, msg); err != nil {
			t.Fatalf("mode %s: echo over C awgm-fpc -> CDN gate -> C awgm-xhttpd failed: %v", mode, err)
		}
	}
}

func stopAllEndpoints() {
	globalMu.Lock()
	defer globalMu.Unlock()
	for k, inst := range globalInstances {
		delete(globalInstances, k)
		inst.stop()
	}
	clear(globalDormant)
}

// F823: сверка усыпляет endpoint без ссылок и поднимает его на прежнем
// порту, когда ссылка вернулась; ссылка из YAML FIRC тоже держит; свежий
// EnsureEndpoint защищён minAge; битый JSON отменяет сверку.
func TestReconcileReferenced(t *testing.T) {
	SetStorePath(filepath.Join(t.TempDir(), "fp-clients.json"))
	t.Cleanup(stopAllEndpoints)
	t.Setenv("AWGM_DISABLE_C_FPC", "0")
	t.Setenv("AWGM_C_FPC_BIN", makeFakeFPCBinary(t, "ok"))
	fpID, fpSecretHex, _, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatal(err)
	}
	spec := EndpointSpec{Key: "gc-a", Server: "127.0.0.1", ServerPort: 443, UUID: "01234567-89ab-4def-8123-456789abcdef",
		FPClientID: fpID, FPSecretHex: fpSecretHex, Host: "cdn.example.com", Path: "/cdn-bridge"}
	port, err := EnsureEndpoint(spec)
	if err != nil {
		t.Fatal(err)
	}
	sbDir, fircDir := t.TempDir(), t.TempDir()
	roots := []string{sbDir, fircDir, filepath.Join(t.TempDir(), "missing")}
	alive := func() bool {
		globalMu.Lock()
		defer globalMu.Unlock()
		inst, ok := globalInstances["gc-a"]
		return ok && inst.isAlive()
	}

	// Свежий — не трогаем, даже без ссылок.
	if err := ReconcileReferenced(roots, time.Hour); err != nil || !alive() {
		t.Fatalf("fresh endpoint pruned: err=%v alive=%v", err, alive())
	}
	// Без ссылок и не свежий — спит.
	if err := ReconcileReferenced(roots, 0); err != nil || alive() {
		t.Fatalf("unreferenced endpoint still running: err=%v", err)
	}
	// Ссылка вернулась во вложенном JSON-слоте — поднят на том же порту.
	slot := fmt.Sprintf(`{"route":{},"outbounds":[{"type":"selector"},{"type":"socks","server":"127.0.0.1","server_port":%d}]}`, port)
	if err := os.MkdirAll(filepath.Join(sbDir, "disabled"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "disabled", "30-x.json"), []byte(slot), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileReferenced(roots, 0); err != nil || !alive() {
		t.Fatalf("referenced dormant endpoint not revived: err=%v", err)
	}
	globalMu.Lock()
	gotPort := globalInstances["gc-a"].spec.SocksPort
	globalMu.Unlock()
	if gotPort != port {
		t.Fatalf("revived on port %d, want %d", gotPort, port)
	}
	// Ссылка только из FIRC (socks5://127.0.0.1:P в YAML) тоже держит.
	_ = os.Remove(filepath.Join(sbDir, "disabled", "30-x.json"))
	yaml := fmt.Sprintf("tunnels:\n  - name: fp\n    outbound: socks5://127.0.0.1:%d\n", port)
	if err := os.WriteFile(filepath.Join(fircDir, "tunnels.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileReferenced(roots, 0); err != nil || !alive() {
		t.Fatalf("FIRC-referenced endpoint pruned: err=%v", err)
	}
	// Битый JSON — сверка отменена, ничего не тронуто.
	_ = os.Remove(filepath.Join(fircDir, "tunnels.yaml"))
	if err := os.WriteFile(filepath.Join(sbDir, "31-bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileReferenced(roots, 0); err == nil || !alive() {
		t.Fatalf("broken slot must abort reconcile: err=%v alive=%v", err, alive())
	}
}
