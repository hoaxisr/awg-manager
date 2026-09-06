package obfuscator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectForeign_InitScript(t *testing.T) {
	dir := t.TempDir()
	foreignInitScript = filepath.Join(dir, "S49wg-obfuscator")
	procRoot = t.TempDir()
	if f := DetectForeign(); f.InitScript || f.ProcessAlive || f.Warning() != "" {
		t.Fatalf("clean system: %+v", f)
	}
	_ = os.WriteFile(foreignInitScript, []byte("#!/bin/sh\n"), 0o755)
	_ = os.MkdirAll(filepath.Join(procRoot, "4242"), 0o755)
	_ = os.WriteFile(filepath.Join(procRoot, "4242", "cmdline"), []byte("/opt/bin/wg-obfuscator1\x00--config\x00x\x00"), 0o644)
	f := DetectForeign()
	if !f.InitScript || !f.ProcessAlive || f.Warning() == "" {
		t.Fatalf("%+v", f)
	}
}

func TestExternalKind(t *testing.T) {
	if ExternalKind("Phobos-router") != "phobos" || ExternalKind("Home") != "" {
		t.Fatal("ExternalKind")
	}
}
