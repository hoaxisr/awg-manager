package services

import (
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

	// 3. Protection: disable awg-manager should fail
	awgScript := filepath.Join(tempDir, "S99awg-manager")
	if err := os.WriteFile(awgScript, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("failed to create awg-manager test script: %v", err)
	}
	_, err = scanner.ToggleEnable(awgScript, false)
	if err == nil {
		t.Errorf("expected error when disabling awg-manager autostart, got nil")
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
