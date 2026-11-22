package telemt

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

// Files under <dataDir>/telemt that belong to the Telegram WebProxy
// (internal/tgwebproxy) telemt-direct / telemt-raw workers and their init
// scripts (S99telemt, S96telemt-raw). The standalone telemt service must never
// write, delete or signal through them.
var tgWebProxyOwnedTelemtFiles = []string{"config.toml", "raw.toml", "telemt.pid"}

func seedTgWebProxyTelemtFiles(t *testing.T, telemtDir string) map[string]string {
	t.Helper()
	if err := os.MkdirAll(telemtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, name := range tgWebProxyOwnedTelemtFiles {
		content := "owned-by-tgwebproxy:" + name
		if err := os.WriteFile(filepath.Join(telemtDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		want[name] = content
	}
	return want
}

func assertTgWebProxyTelemtFilesIntact(t *testing.T, telemtDir string, want map[string]string) {
	t.Helper()
	for name, content := range want {
		p := filepath.Join(telemtDir, name)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("tgwebproxy-owned %s removed by standalone telemt: %v", name, err)
			continue
		}
		if string(got) != content {
			t.Errorf("tgwebproxy-owned %s overwritten by standalone telemt:\n%s", name, got)
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().Perm() != 0o600 {
			t.Errorf("tgwebproxy-owned %s mode changed to %o", name, fi.Mode().Perm())
		}
	}
}

func TestStandalonePaths_DisjointFromTgWebProxy(t *testing.T) {
	svc := New(t.TempDir(), "x86_64")
	for _, name := range tgWebProxyOwnedTelemtFiles {
		owned := filepath.Join(svc.telemtDir, name)
		if svc.cfgPath == owned || svc.pidPath == owned || svc.settingsPath == owned {
			t.Errorf("standalone telemt uses tgwebproxy-owned path %s", owned)
		}
	}

	// Production paths: tgwebproxy workers use these pid files, and
	// tgwebproxy.FactoryReset globs /opt/var/run/telemt*.pid* before the
	// coordinator asks standalone telemt to stop its own process.
	for _, p := range []string{"/opt/var/run/telemt.pid", "/opt/var/run/telemt-raw.pid"} {
		if ManagedTelemtPIDPath == p {
			t.Errorf("ManagedTelemtPIDPath collides with tgwebproxy worker pid %s", p)
		}
	}
	if ok, _ := filepath.Match("/opt/var/run/telemt*.pid*", ManagedTelemtPIDPath); ok {
		t.Errorf("ManagedTelemtPIDPath %s is wiped by tgwebproxy factory reset glob", ManagedTelemtPIDPath)
	}
	if ManagedTelemtConfigPath == "/opt/etc/awg-manager/telemt/config.toml" ||
		ManagedTelemtConfigPath == "/opt/etc/awg-manager/telemt/raw.toml" {
		t.Errorf("ManagedTelemtConfigPath collides with tgwebproxy telemt config %s", ManagedTelemtConfigPath)
	}
	if filepath.Dir(ManagedTelemtConfigPath) != ManagedTelemtDir {
		t.Errorf("ManagedTelemtConfigPath %s outside ManagedTelemtDir", ManagedTelemtConfigPath)
	}
}

// Saving standalone settings used to rewrite <telemtDir>/config.toml — the
// live config of the tgwebproxy telemt-direct worker — with a different
// secret/port and mode 0644, even when standalone telemt is disabled.
func TestSaveConfig_DoesNotOverwriteTgWebProxyConfig(t *testing.T) {
	svc := New(t.TempDir(), "x86_64")
	want := seedTgWebProxyTelemtFiles(t, svc.telemtDir)

	if err := svc.SaveConfig(context.Background(), Config{Enabled: false, Port: 9443}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	assertTgWebProxyTelemtFilesIntact(t, svc.telemtDir, want)
	if _, err := os.Stat(svc.cfgPath); err != nil {
		t.Errorf("standalone config not written to its own path %s: %v", svc.cfgPath, err)
	}
}

func TestStart_DoesNotOverwriteTgWebProxyConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture")
	}
	svc := New(t.TempDir(), "x86_64")
	want := seedTgWebProxyTelemtFiles(t, svc.telemtDir)
	// Fake binary that "starts" without daemonizing; Start then fails its
	// readiness poll, but config writes happen before that.
	if err := os.WriteFile(svc.binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_ = svc.Start(context.Background())
	assertTgWebProxyTelemtFilesIntact(t, svc.telemtDir, want)
}

// ResetConfig runs during the Telegram factory reset right after
// tgwebproxy.FactoryReset; it must only clear standalone state.
func TestResetConfig_OnlyRemovesStandaloneFiles(t *testing.T) {
	svc := New(t.TempDir(), "x86_64")
	want := seedTgWebProxyTelemtFiles(t, svc.telemtDir)
	for _, p := range []string{svc.settingsPath, svc.cfgPath, svc.pidPath} {
		if err := os.WriteFile(p, []byte("standalone"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.ResetConfig(context.Background()); err != nil {
		t.Fatalf("ResetConfig: %v", err)
	}
	assertTgWebProxyTelemtFilesIntact(t, svc.telemtDir, want)
	for _, p := range []string{svc.settingsPath, svc.cfgPath, svc.pidPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("standalone file %s not removed by ResetConfig", p)
		}
	}
}

const telemtHelperEnv = "AWGM_TELEMT_TEST_HELPER"

// TestHelperTelemtProcess is not a test: it is the body of the fake telemt
// process spawned by TestStop_DoesNotKillTgWebProxyTelemt.
func TestHelperTelemtProcess(t *testing.T) {
	if os.Getenv(telemtHelperEnv) != "1" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// Linux-only: process identity is resolved through /proc. A telemt process
// recorded in the tgwebproxy-owned pid file must survive standalone Stop.
func TestStop_DoesNotKillTgWebProxyTelemt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process identity check requires /proc")
	}
	svc := New(t.TempDir(), "x86_64")
	_ = seedTgWebProxyTelemtFiles(t, svc.telemtDir)

	// Helper = this test binary re-executed with argv0 ".../telemt", so its
	// /proc cmdline matches the tgwebproxy worker. A copied system `sleep` is
	// not usable: multicall coreutils (uutils on Ubuntu 26.04) dispatch on
	// argv0 and exit immediately when invoked as "telemt".
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestHelperTelemtProcess$")
	cmd.Args[0] = filepath.Join(t.TempDir(), "telemt")
	cmd.Env = append(os.Environ(), telemtHelperEnv+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var waitErr error
	done := make(chan struct{})
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	pid := cmd.Process.Pid

	// Fixture sanity: the helper must be alive and identified as "telemt"
	// before Stop, otherwise a death below would prove nothing about Stop.
	deadline := time.Now().Add(5 * time.Second)
	for !childproc.MatchesBinary(pid, "telemt") {
		select {
		case <-done:
			t.Fatalf("helper exited before Stop: %v; stderr: %s", waitErr, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper pid %d never identified as telemt; stderr: %s", pid, stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	tgPid := filepath.Join(svc.telemtDir, "telemt.pid")
	if err := os.WriteFile(tgPid, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatal(err)
	}

	if running, _ := svc.GetRunningState(); running {
		t.Errorf("standalone telemt reports tgwebproxy's telemt process as its own")
	}
	_ = svc.Stop(context.Background())

	select {
	case <-done:
		t.Fatalf("tgwebproxy telemt process died during standalone Stop: %v; stderr: %s", waitErr, stderr.String())
	case <-time.After(300 * time.Millisecond):
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("tgwebproxy telemt process gone after standalone Stop: %v", err)
	}
	if _, err := os.Stat(tgPid); err != nil {
		t.Errorf("tgwebproxy pid file removed by standalone Stop: %v", err)
	}
}
