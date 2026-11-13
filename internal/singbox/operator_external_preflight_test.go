package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
)

func TestStartAndWaitPinnedHealsExternalBeforeSpawn(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sing-box")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeSidecar(bin, installer.RequiredVersion); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: bin})
	slot := filepath.Join(op.configPath, "20-router.json")
	seed := []byte(`{"inbounds":[{"type":"tun","tag":"tun-in","interface_name":"opkgtun0","address":["172.18.0.1/30","fdfe:dcba:9876::1/126"],"auto_route":false}],"route":{"final":"direct"}}`)
	if err := os.WriteFile(slot, seed, 0o644); err != nil {
		t.Fatal(err)
	}

	stopBeforeSpawn := errors.New("stop before spawn")
	externalAtSpawn := false
	op.proc.startCmd = func(string, ...string) (*exec.Cmd, error) {
		raw, err := os.ReadFile(slot)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Inbounds []struct {
				External bool `json:"external_configuration"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		externalAtSpawn = len(doc.Inbounds) == 1 && doc.Inbounds[0].External
		return nil, stopBeforeSpawn
	}
	if _, err := op.startAndWait(context.Background()); !errors.Is(err, stopBeforeSpawn) {
		t.Fatalf("startAndWait error = %v, want sentinel", err)
	}
	if !externalAtSpawn {
		t.Fatal("sing-box was offered a policy-tun config without external_configuration")
	}

	before, err := os.Stat(slot)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := op.startAndWait(context.Background()); !errors.Is(err, stopBeforeSpawn) {
		t.Fatalf("second startAndWait error = %v, want sentinel", err)
	}
	after, err := os.Stat(slot)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("healthy pinned preflight rewrote the slot instead of being idempotent")
	}
}

func TestPreflightUnknownVersionLeavesExternalUntouched(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sing-box")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: bin})
	slot := filepath.Join(op.configPath, "20-router.json")
	seed := []byte(`{"inbounds":[{"type":"tun","tag":"tun-in","auto_route":false}],"route":{"final":"direct"}}`)
	if err := os.WriteFile(slot, seed, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(slot)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := op.preflightStatic(); err != nil {
		t.Fatalf("preflightStatic: %v", err)
	}
	after, err := os.Stat(slot)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("unknown binary version changed the policy-tun slot")
	}
}
