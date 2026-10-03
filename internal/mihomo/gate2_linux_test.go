//go:build linux

package mihomo

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestProcessVerifier_ExecutableMismatch_Linux tests that CanonicalPath and VerifyIdentity
// strictly detect executable mismatch between expected binary and actual running binary.
func TestProcessVerifier_ExecutableMismatch_Linux(t *testing.T) {
	tmpDir := t.TempDir()
	procDir := filepath.Join(tmpDir, "proc")
	pid := 7777
	pidDir := filepath.Join(procDir, strconv.Itoa(pid))
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatalf("mkdir pidDir: %v", err)
	}

	binA := filepath.Join(tmpDir, "mihomo-real")
	if err := os.WriteFile(binA, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write binA: %v", err)
	}
	binB := filepath.Join(tmpDir, "mihomo-swapped")
	if err := os.WriteFile(binB, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write binB: %v", err)
	}

	// Create symlink for binA
	binASym := filepath.Join(tmpDir, "mihomo-symlink")
	if err := os.Symlink(binA, binASym); err != nil {
		t.Fatalf("symlink binASym: %v", err)
	}

	// Setup fake /proc/<pid>/exe pointing to binA
	exeLink := filepath.Join(pidDir, "exe")
	if err := os.Symlink(binA, exeLink); err != nil {
		t.Fatalf("symlink exe: %v", err)
	}

	// Setup fake /proc/<pid>/stat with start ticks 654321
	statContent := fmt.Sprintf("%d (mihomo-real) S 1 %d %d 0 -1 4194304 100 0 0 0 10 20 0 0 20 0 1 0 654321 12345678", pid, pid, pid)
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(statContent), 0644); err != nil {
		t.Fatalf("write stat: %v", err)
	}

	// Setup fake /proc/<pid>/cmdline: binA\0-d\0/tmp/cfg\0
	configDir := filepath.Join(tmpDir, "config")
	_ = os.MkdirAll(configDir, 0755)
	cmdlineContent := binA + "\x00-d\x00" + configDir + "\x00"
	if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte(cmdlineContent), 0644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}

	verifier := &LinuxProcessVerifier{}

	// 1. Swapped executable (binB) must fail with ErrExecutableMismatch
	err := verifier.VerifyIdentity(procDir, pid, 654321, binB, configDir)
	if err == nil || !errors.Is(err, ErrExecutableMismatch) {
		t.Fatalf("expected ErrExecutableMismatch, got %v", err)
	}

	// 2. Exact match (binA) must succeed
	if err := verifier.VerifyIdentity(procDir, pid, 654321, binA, configDir); err != nil {
		t.Fatalf("expected VerifyIdentity to succeed with binA, got: %v", err)
	}

	// 3. Symlink to exact match (binASym -> binA) must succeed via CanonicalPath resolution
	if err := verifier.VerifyIdentity(procDir, pid, 654321, binASym, configDir); err != nil {
		t.Fatalf("expected VerifyIdentity to succeed with canonical symlink, got: %v", err)
	}
}

// TestProcessVerifier_CmdlineVerification_Linux tests LinuxProcessVerifier against cmdline tampering.
func TestProcessVerifier_CmdlineVerification_Linux(t *testing.T) {
	tmpDir := t.TempDir()
	procDir := filepath.Join(tmpDir, "proc")
	pid := 8888
	pidDir := filepath.Join(procDir, strconv.Itoa(pid))
	_ = os.MkdirAll(pidDir, 0755)

	binPath := filepath.Join(tmpDir, "mihomo")
	_ = os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755)
	_ = os.Symlink(binPath, filepath.Join(pidDir, "exe"))

	statContent := fmt.Sprintf("%d (mihomo) S 1 %d %d 0 -1 4194304 100 0 0 0 10 20 0 0 20 0 1 0 112233 12345678", pid, pid, pid)
	_ = os.WriteFile(filepath.Join(pidDir, "stat"), []byte(statContent), 0644)

	configDir := filepath.Join(tmpDir, "cfg")
	_ = os.MkdirAll(configDir, 0755)

	verifier := &LinuxProcessVerifier{}

	// Subtest 1: Alien config dir in cmdline
	alienCmd := binPath + "\x00-d\x00/other/alien/cfg\x00"
	_ = os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte(alienCmd), 0644)
	err := verifier.VerifyIdentity(procDir, pid, 112233, binPath, configDir)
	if err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Fatalf("expected ErrProcessProofFailed on alien configDir, got: %v", err)
	}

	// Subtest 2: Missing -d argument
	missingFlagCmd := binPath + "\x00-c\x00" + configDir + "/config.yaml\x00"
	_ = os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte(missingFlagCmd), 0644)
	err = verifier.VerifyIdentity(procDir, pid, 112233, binPath, configDir)
	if err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Fatalf("expected ErrProcessProofFailed on missing -d flag, got: %v", err)
	}

	// Subtest 3: Correct -d argument
	validCmd := binPath + "\x00-d\x00" + configDir + "\x00"
	_ = os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte(validCmd), 0644)
	if err := verifier.VerifyIdentity(procDir, pid, 112233, binPath, configDir); err != nil {
		t.Fatalf("expected valid cmdline to succeed, got: %v", err)
	}
}

// TestProcessVerifier_StartTicksVerification_Linux tests detection of PID recycling via start ticks.
func TestProcessVerifier_StartTicksVerification_Linux(t *testing.T) {
	tmpDir := t.TempDir()
	procDir := filepath.Join(tmpDir, "proc")
	pid := 9999
	pidDir := filepath.Join(procDir, strconv.Itoa(pid))
	_ = os.MkdirAll(pidDir, 0755)

	binPath := filepath.Join(tmpDir, "mihomo")
	_ = os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755)
	_ = os.Symlink(binPath, filepath.Join(pidDir, "exe"))

	configDir := filepath.Join(tmpDir, "cfg")
	_ = os.MkdirAll(configDir, 0755)
	_ = os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte(binPath+"\x00-d\x00"+configDir+"\x00"), 0644)

	// Stat has ticks = 999999
	statContent := fmt.Sprintf("%d (mihomo) S 1 %d %d 0 -1 4194304 100 0 0 0 10 20 0 0 20 0 1 0 999999 12345678", pid, pid, pid)
	_ = os.WriteFile(filepath.Join(pidDir, "stat"), []byte(statContent), 0644)

	verifier := &LinuxProcessVerifier{}

	// Expected ticks = 111111 (different from stat 999999)
	err := verifier.VerifyIdentity(procDir, pid, 111111, binPath, configDir)
	if err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Fatalf("expected ErrProcessProofFailed on ticks mismatch, got: %v", err)
	}

	// Expected ticks = 999999 (matching stat)
	if err := verifier.VerifyIdentity(procDir, pid, 999999, binPath, configDir); err != nil {
		t.Fatalf("expected match on ticks 999999, got: %v", err)
	}
}

// TestLinuxProbeIntegration tests live Linux kernel procfs and socket tables.
func TestLinuxProbeIntegration(t *testing.T) {
	// Bind a real TCP listener on localhost ephemeral port
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	pid := os.Getpid()

	verifier := &LinuxProcessVerifier{}

	// 1. Verify that this test process owns the listener on 127.0.0.1:<port>
	err = verifier.VerifySocketOwnership("/proc", "127.0.0.1", port, "tcp", pid)
	if err != nil {
		t.Fatalf("VerifySocketOwnership failed on live socket %d: %v", port, err)
	}

	// 2. Verify that an alien PID is rejected with ErrControllerSocketMismatch
	alienPID := pid + 100000
	err = verifier.VerifySocketOwnership("/proc", "127.0.0.1", port, "tcp", alienPID)
	if err == nil || !errors.Is(err, ErrControllerSocketMismatch) {
		t.Fatalf("expected ErrControllerSocketMismatch for alien PID %d, got: %v", alienPID, err)
	}

	// 3. Verify that an unallocated port fails with ErrListenerUnavailable
	unusedPort := 59999
	err = verifier.VerifySocketOwnership("/proc", "127.0.0.1", unusedPort, "tcp", pid)
	if err == nil || !errors.Is(err, ErrListenerUnavailable) {
		t.Fatalf("expected ErrListenerUnavailable for unused port %d, got: %v", unusedPort, err)
	}
}
