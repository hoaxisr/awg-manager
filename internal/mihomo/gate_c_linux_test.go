//go:build linux

package mihomo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGateC1_LinuxProcessVerifier_StrictIdentity(t *testing.T) {
	procDir := t.TempDir()
	pid := 4242
	pidDir := filepath.Join(procDir, "4242")
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatalf("mkdir pidDir: %v", err)
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "mihomo")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	if err := os.Symlink(binPath, filepath.Join(pidDir, "exe")); err != nil {
		t.Fatalf("symlink exe: %v", err)
	}

	cfgDir := filepath.Clean(t.TempDir())
	cmdline := []byte(binPath + "\x00-d\x00" + cfgDir + "\x00")
	if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), cmdline, 0644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}

	// Field 22 in /proc/<pid>/stat is starttime (ticks)
	// Example stat with 22 fields where 22nd field is 998877
	statContent := "4242 (mihomo) S 1 4242 4242 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 998877 0 0 0 0\n"
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(statContent), 0644); err != nil {
		t.Fatalf("write stat: %v", err)
	}

	verifier := &LinuxProcessVerifier{}

	// 1. Valid identity capture
	identity, err := verifier.CaptureIdentity(procDir, pid, binPath, cfgDir, 7)
	if err != nil {
		t.Fatalf("expected CaptureIdentity to succeed, got: %v", err)
	}
	if identity.Generation != 7 {
		t.Errorf("expected Generation == 7, got %d", identity.Generation)
	}
	if identity.ProcStartTicks != 998877 {
		t.Errorf("expected ProcStartTicks == 998877, got %d", identity.ProcStartTicks)
	}

	// 2. Verify identity passes with exact ticks
	if err := verifier.VerifyIdentity(procDir, pid, 998877, binPath, cfgDir); err != nil {
		t.Errorf("expected VerifyIdentity to pass with exact ticks, got: %v", err)
	}

	// 3. Verify identity FAILS if start ticks mismatch (e.g. process was restarted with same PID)
	if err := verifier.VerifyIdentity(procDir, pid, 111111, binPath, cfgDir); err == nil {
		t.Errorf("expected VerifyIdentity to fail on mismatched ticks")
	}

	// 4. Verify identity FAILS if cmdline does not match -d <configDir>
	wrongCmdline := []byte(binPath + "\x00-d\x00/other/dir\x00")
	_ = os.WriteFile(filepath.Join(pidDir, "cmdline"), wrongCmdline, 0644)
	if err := verifier.VerifyIdentity(procDir, pid, 998877, binPath, cfgDir); err == nil {
		t.Errorf("expected VerifyIdentity to fail on mismatched cmdline")
	}
	if _, err := verifier.CaptureIdentity(procDir, pid, binPath, cfgDir, 7); err == nil {
		t.Errorf("expected CaptureIdentity to fail closed on mismatched cmdline")
	}
}
