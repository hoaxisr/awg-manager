package mihomo

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Helper process entrypoint for Stage 3 lifecycle tests
func TestStage3HelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_STAGE3_HELPER") != "1" {
		return
	}
	mode := os.Getenv("STAGE3_HELPER_MODE")
	switch mode {
	case "graceful_term":
		// Listen for SIGTERM and exit cleanly
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGTERM)
		<-sigChan
		os.Exit(0)

	case "ignore_term":
		// Ignore SIGTERM so process requires SIGKILL to terminate
		signal.Ignore(syscall.SIGTERM)
		for {
			time.Sleep(1 * time.Second)
		}

	case "spam_logs":
		// Write lots of data to stdout and stderr
		line := strings.Repeat("A", 1024) + "\n"
		for i := 0; i < 500; i++ {
			_, _ = os.Stdout.WriteString(line)
			_, _ = os.Stderr.WriteString(line)
		}
		os.Exit(0)

	case "instant_exit":
		os.Exit(0)

	case "quick_loop":
		time.Sleep(24 * time.Hour)
		os.Exit(0)

	default:
		os.Exit(1)
	}
}

func stage3HelperCommand(t *testing.T, mode string) func(string, ...string) *exec.Cmd {
	t.Helper()
	return func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStage3HelperProcess$")
		cmd.Env = append(os.Environ(), "GO_WANT_STAGE3_HELPER=1", "STAGE3_HELPER_MODE="+mode)
		return cmd
	}
}

// countOpenFDs counts open file descriptors in /proc/self/fd on Linux.
func countOpenFDs() (int, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

func TestStage3_BoundedRingBuffer_MemoryCeiling(t *testing.T) {
	capacity := 1024 // 1 KiB for test
	rb := NewBoundedRingBuffer(capacity)

	if rb.Cap() != capacity {
		t.Fatalf("Cap() = %d, want %d", rb.Cap(), capacity)
	}

	// Write small chunk
	chunk1 := []byte("hello world")
	n, err := rb.Write(chunk1)
	if err != nil || n != len(chunk1) {
		t.Fatalf("Write small chunk: n=%d err=%v", n, err)
	}
	if rb.Len() != len(chunk1) {
		t.Fatalf("Len() = %d, want %d", rb.Len(), len(chunk1))
	}
	if rb.String() != "hello world" {
		t.Fatalf("String() = %q, want 'hello world'", rb.String())
	}

	// Write more than capacity in one single write
	huge := make([]byte, 5000)
	for i := range huge {
		huge[i] = byte('A' + (i % 26))
	}
	n, err = rb.Write(huge)
	if err != nil || n != len(huge) {
		t.Fatalf("Write huge: n=%d err=%v", n, err)
	}
	if rb.Len() != capacity {
		t.Fatalf("Len() after huge = %d, want capacity %d", rb.Len(), capacity)
	}
	expectedTail := string(huge[len(huge)-capacity:])
	if rb.String() != expectedTail {
		t.Fatalf("String() tail mismatch after huge write")
	}

	// Incremental writes exceeding capacity
	rb.Reset()
	if rb.Len() != 0 {
		t.Fatalf("Len() after Reset = %d, want 0", rb.Len())
	}

	totalWritten := 0
	for i := 0; i < 50; i++ {
		block := []byte(fmt.Sprintf("block-%03d;", i))
		n, err := rb.Write(block)
		if err != nil || n != len(block) {
			t.Fatalf("Write block %d: err=%v", i, err)
		}
		totalWritten += len(block)
		if rb.Len() > capacity {
			t.Fatalf("Len() exceeded capacity: %d > %d", rb.Len(), capacity)
		}
	}
	if rb.Len() > capacity {
		t.Fatalf("Final Len() %d > capacity %d", rb.Len(), capacity)
	}

	// Concurrent writes with race detector
	var wg sync.WaitGroup
	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = rb.Write([]byte(fmt.Sprintf("worker-%d-chunk-%d\n", workerID, j)))
				_ = rb.String()
				_ = rb.Bytes()
				_ = rb.Len()
			}
		}(w)
	}
	wg.Wait()

	if rb.Len() > capacity {
		t.Fatalf("Concurrent Len() %d > capacity %d", rb.Len(), capacity)
	}
}

func TestStage3_RotatingLogWriter_SizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")
	maxSize := int64(1024) // 1 KiB

	writer, err := NewRotatingLogWriter(logPath, maxSize)
	if err != nil {
		t.Fatalf("NewRotatingLogWriter: %v", err)
	}
	defer writer.Close()

	// Write 800 bytes (under maxSize)
	p1 := bytes.Repeat([]byte("A"), 800)
	if _, err := writer.Write(p1); err != nil {
		t.Fatalf("Write p1: %v", err)
	}

	fi, err := os.Stat(logPath)
	if err != nil || fi.Size() != 800 {
		t.Fatalf("Initial file size = %v (err=%v), want 800", fi.Size(), err)
	}
	if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
		t.Fatalf("Backup .1 should not exist yet")
	}

	// Write another 400 bytes (total 1200 > 1024) -> triggers rotation
	p2 := bytes.Repeat([]byte("B"), 400)
	if _, err := writer.Write(p2); err != nil {
		t.Fatalf("Write p2: %v", err)
	}

	// Active file should now have size 400
	fiActive, err := os.Stat(logPath)
	if err != nil || fiActive.Size() != 400 {
		t.Fatalf("Rotated active file size = %v (err=%v), want 400", fiActive.Size(), err)
	}

	// Backup file should exist and have size 800
	fiBackup, err := os.Stat(logPath + ".1")
	if err != nil || fiBackup.Size() != 800 {
		t.Fatalf("Backup file size = %v (err=%v), want 800", fiBackup.Size(), err)
	}

	// Close cleanly
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Redundant close is safe
	if err := writer.Close(); err != nil {
		t.Fatalf("Double Close: %v", err)
	}
}

func TestStage3_LogFileDescriptorCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "mihomo_clean.log")

	// Case A: Startup fails because binary does not exist
	opFail := NewOperator("non-existent-binary-for-test", tmpDir)
	opFail.SetLogPath(logPath)
	opFail.commandFn = func(string, ...string) *exec.Cmd {
		return exec.Command("non-existent-binary-path-xyz")
	}

	err := opFail.Start()
	if err == nil {
		t.Fatal("Start() should have failed")
	}

	// Verify log file is writable and removable (proves FD is not kept open)
	if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Remove log after failed start: %v", err)
	}

	// Case B: Process starts, runs, and stops
	opSuccess := NewOperator("ignored", tmpDir)
	opSuccess.SetLogPath(logPath)
	opSuccess.commandFn = stage3HelperCommand(t, "quick_loop")
	opSuccess.readyFn = func(context.Context) error { return nil }

	if err := opSuccess.Start(); err != nil {
		t.Fatalf("Start() success case: %v", err)
	}

	if err := opSuccess.Stop(); err != nil {
		t.Fatalf("Stop() success case: %v", err)
	}

	// File should be removable now
	if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Remove log after clean stop: %v", err)
	}
}

func TestStage3_CacheDbPreservation(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "cache.db")
	initialContent := []byte("persisted-sqlite-proxy-group-selections")

	if err := os.WriteFile(cachePath, initialContent, 0644); err != nil {
		t.Fatalf("Write initial cache.db: %v", err)
	}

	op := NewOperator("ignored", tmpDir)
	op.commandFn = stage3HelperCommand(t, "quick_loop")
	op.readyFn = func(context.Context) error { return nil }

	// 1. Start preserves cache.db
	if err := op.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	dataAfterStart, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("cache.db missing after Start(): %v", err)
	}
	if !bytes.Equal(dataAfterStart, initialContent) {
		t.Fatalf("cache.db altered by Start(): got %q, want %q", dataAfterStart, initialContent)
	}

	// 2. Stopped process preserves cache.db
	if err := op.Stop(); err != nil {
		t.Fatalf("Stop(): %v", err)
	}
	dataAfterStop, err := os.ReadFile(cachePath)
	if err != nil || !bytes.Equal(dataAfterStop, initialContent) {
		t.Fatalf("cache.db altered by Stop(): err=%v", err)
	}

	// 3. Reload preserves cache.db
	if err := op.Reload(); err != nil {
		t.Fatalf("Reload(): %v", err)
	}
	defer op.Stop()

	dataAfterReload, err := os.ReadFile(cachePath)
	if err != nil || !bytes.Equal(dataAfterReload, initialContent) {
		t.Fatalf("cache.db altered by Reload(): err=%v", err)
	}

	// 4. ResetCache explicitly wipes cache.db
	if err := op.ResetCache(); err != nil {
		t.Fatalf("ResetCache(): %v", err)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("cache.db should have been deleted by ResetCache()")
	}
}

func TestStage3_GracefulTerminationSequence(t *testing.T) {
	// Case A: Graceful termination via SIGTERM
	tmpDir := t.TempDir()
	opGraceful := NewOperator("ignored", tmpDir)
	opGraceful.commandFn = stage3HelperCommand(t, "graceful_term")
	opGraceful.readyFn = func(context.Context) error { return nil }
	opGraceful.SetGracefulTimeout(2 * time.Second)

	if err := opGraceful.Start(); err != nil {
		t.Fatalf("opGraceful.Start(): %v", err)
	}

	start := time.Now()
	if err := opGraceful.Stop(); err != nil {
		t.Fatalf("opGraceful.Stop(): %v", err)
	}
	elapsed := time.Since(start)

	// Clean SIGTERM should finish well under the 2s timeout
	if elapsed >= 1800*time.Millisecond {
		t.Fatalf("Graceful stop took %v, should have terminated immediately on SIGTERM", elapsed)
	}
	if running, _ := opGraceful.IsRunning(); running {
		t.Fatal("Process still running after graceful stop")
	}

	// Case B: Process ignores SIGTERM -> falls back to SIGKILL
	opStuck := NewOperator("ignored", tmpDir)
	opStuck.commandFn = stage3HelperCommand(t, "ignore_term")
	opStuck.readyFn = func(context.Context) error { return nil }
	opStuck.SetGracefulTimeout(150 * time.Millisecond) // Short timeout for test

	if err := opStuck.Start(); err != nil {
		t.Fatalf("opStuck.Start(): %v", err)
	}

	start = time.Now()
	if err := opStuck.Stop(); err != nil {
		t.Fatalf("opStuck.Stop(): %v", err)
	}
	elapsed = time.Since(start)

	// Should have waited for ~150ms timeout then sent SIGKILL
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Stop finished too fast (%v), expected to wait for graceful timeout", elapsed)
	}
	if running, _ := opStuck.IsRunning(); running {
		t.Fatal("Process still running after SIGKILL fallback")
	}
}

func TestStage3_ReloadTunStopErrorHandling(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	tunConfig := []byte("tun:\n  enable: true\n")
	if err := os.WriteFile(cfgPath, tunConfig, 0644); err != nil {
		t.Fatalf("Write tun config: %v", err)
	}

	op := NewOperator("ignored", tmpDir)
	op.commandFn = stage3HelperCommand(t, "quick_loop")
	op.readyFn = func(context.Context) error { return nil }

	if err := op.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer op.Stop()

	if !op.configHasTunEnabled() {
		t.Fatal("configHasTunEnabled() = false, want true")
	}
}

func TestStage3_ControllerSecretAuthentication(t *testing.T) {
	receivedAuth := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		if r.URL.Path == "/version" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"version":"1.18.0"}`))
			return
		}
		if r.URL.Path == "/configs" && r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/proxies" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"proxies":{}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	addr := strings.TrimPrefix(server.URL, "http://")
	secretToken := "secret-test-token-12345"

	op := NewOperator("ignored", t.TempDir())
	op.SetController(addr, secretToken)

	// Test newControllerRequest produces correct headers
	ctx := context.Background()
	req, err := op.newControllerRequest(ctx, http.MethodGet, "/version", nil)
	if err != nil {
		t.Fatalf("newControllerRequest: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+secretToken {
		t.Fatalf("Authorization header = %q, want 'Bearer %s'", got, secretToken)
	}

	// Test Reload sends bearer token
	op.commandFn = stage3HelperCommand(t, "quick_loop")
	op.readyFn = func(context.Context) error { return nil }
	if err := op.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer op.Stop()

	// Hot reload (TUN disabled) issues PUT /configs?force=true
	receivedAuth = ""
	if err := op.Reload(); err != nil {
		t.Fatalf("Reload(): %v", err)
	}
	if receivedAuth != "Bearer "+secretToken {
		t.Fatalf("Reload sent Authorization header %q, want 'Bearer %s'", receivedAuth, secretToken)
	}
}

func TestStage3_ConsecutiveRestartStress_100Cycles(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "cache.db")
	_ = os.WriteFile(cachePath, []byte("selector-choice-stable"), 0644)

	op := NewOperator("ignored", tmpDir)
	op.commandFn = stage3HelperCommand(t, "instant_exit")
	op.readyFn = func(context.Context) error { return nil }

	// Track baseline FDs if on Linux
	initialFDs, fdErr := countOpenFDs()

	for cycle := 1; cycle <= 100; cycle++ {
		op.commandFn = stage3HelperCommand(t, "quick_loop")
		if err := op.Start(); err != nil {
			t.Fatalf("Cycle %d: Start failed: %v", cycle, err)
		}
		running, pid := op.IsRunning()
		if !running || pid <= 0 {
			t.Fatalf("Cycle %d: process not running or invalid pid %d", cycle, pid)
		}

		if err := op.Stop(); err != nil {
			t.Fatalf("Cycle %d: Stop failed: %v", cycle, err)
		}
		if running, _ := op.IsRunning(); running {
			t.Fatalf("Cycle %d: process still running after stop", cycle)
		}

		// Verify cache.db preserved
		data, err := os.ReadFile(cachePath)
		if err != nil || string(data) != "selector-choice-stable" {
			t.Fatalf("Cycle %d: cache.db corrupted or deleted: %v", cycle, err)
		}
	}

	if fdErr == nil {
		finalFDs, err := countOpenFDs()
		if err == nil {
			diff := finalFDs - initialFDs
			// A healthy loop should not grow FDs across 100 cycles
			if diff > 5 {
				t.Fatalf("FD leak detected across 100 cycles: initial=%d, final=%d (growth=%d)", initialFDs, finalFDs, diff)
			}
		}
	}
}
