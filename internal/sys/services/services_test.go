package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestScanner_ToggleEnable(t *testing.T) {
	tempDir := t.TempDir()
	scanner := &Scanner{InitDir: tempDir}

	// Create test S90myservice
	sScript := filepath.Join(tempDir, "S90myservice")
	if err := os.WriteFile(sScript, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("failed to create test script: %v", err)
	}

	// 1. Disable autostart: S90myservice -> K90myservice
	kScript, err := scanner.ToggleEnable(sScript, false)
	if err != nil {
		t.Fatalf("ToggleEnable(false) failed: %v", err)
	}
	if filepath.Base(kScript) != "K90myservice" {
		t.Errorf("expected K90myservice, got %s", filepath.Base(kScript))
	}
	if _, err := os.Stat(kScript); err != nil {
		t.Errorf("new file %s should exist: %v", kScript, err)
	}
	if _, err := os.Stat(sScript); !os.IsNotExist(err) {
		t.Errorf("old file %s should not exist", sScript)
	}

	// 2. Enable autostart: K90myservice -> S90myservice
	newSScript, err := scanner.ToggleEnable(kScript, true)
	if err != nil {
		t.Fatalf("ToggleEnable(true) failed: %v", err)
	}
	if filepath.Base(newSScript) != "S90myservice" {
		t.Errorf("expected S90myservice, got %s", filepath.Base(newSScript))
	}
	if _, err := os.Stat(newSScript); err != nil {
		t.Errorf("new file %s should exist: %v", newSScript, err)
	}

	// 3. Idempotent request still requires the script to exist.
	unchanged, err := scanner.ToggleEnable(newSScript, true)
	if err != nil {
		t.Fatalf("ToggleEnable(true) for enabled script failed: %v", err)
	}
	if unchanged != newSScript {
		t.Errorf("expected unchanged path %s, got %s", newSScript, unchanged)
	}
	if _, err := scanner.ToggleEnable(filepath.Join(tempDir, "S90ghost"), true); err == nil {
		t.Error("expected error for a missing script in the desired state")
	}

	// 4. Never overwrite the opposite S/K script.
	conflictS := filepath.Join(tempDir, "S70conflict")
	conflictK := filepath.Join(tempDir, "K70conflict")
	if err := os.WriteFile(conflictS, []byte("enabled\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflictK, []byte("disabled\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.ToggleEnable(conflictS, false); err == nil {
		t.Error("expected conflict when target script already exists")
	}
	data, err := os.ReadFile(conflictK)
	if err != nil || string(data) != "disabled\n" {
		t.Fatalf("target script was modified: data=%q err=%v", data, err)
	}
}

func TestScanner_ToggleEnable_ProtectsManagedServices(t *testing.T) {
	tempDir := t.TempDir()
	scanner := &Scanner{InitDir: tempDir}

	for _, name := range []string{"awg-manager", "ttyd", "sing-box", "dropbear"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(tempDir, "S99"+name)
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
				t.Fatal(err)
			}
			_, err := scanner.ToggleEnable(path, false)
			if err == nil {
				t.Fatalf("expected disabling %s to fail", name)
			}
			if !errors.Is(err, ErrManagedService) {
				t.Fatalf("expected ErrManagedService, got %v", err)
			}
		})
	}
}

func TestScanner_ToggleEnable_RejectsInvalidNames(t *testing.T) {
	scanner := &Scanner{InitDir: t.TempDir()}
	for _, name := range []string{"service", "S9short", "X90service", "S90bad/name"} {
		t.Run(name, func(t *testing.T) {
			if _, err := scanner.ToggleEnable(name, true); err == nil {
				t.Fatalf("expected invalid name %q to fail", name)
			}
		})
	}
}

func TestScanner_List_WithSAndK(t *testing.T) {
	tempDir := t.TempDir()
	scanner := &Scanner{InitDir: tempDir}

	_ = os.WriteFile(filepath.Join(tempDir, "S50enabled-svc"), []byte("#!/bin/sh\n"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "K60disabled-svc"), []byte("#!/bin/sh\n"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "ignore-me.txt"), []byte("txt"), 0644)

	items, err := scanner.List()
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	for _, item := range items {
		if item.Name == "disabled-svc" {
			if item.Enabled {
				t.Errorf("expected disabled-svc to have Enabled=false")
			}
		} else if item.Name == "enabled-svc" {
			if !item.Enabled {
				t.Errorf("expected enabled-svc to have Enabled=true")
			}
		}
	}
}
