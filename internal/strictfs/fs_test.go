package strictfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateTxID(t *testing.T) {
	valid := []string{
		"20260915120000",
		"20260915120000123456",
		"123456789012345678901234",
	}
	for _, id := range valid {
		if err := ValidateTxID(id); err != nil {
			t.Errorf("expected valid for %s, got: %v", id, err)
		}
	}

	invalid := []string{
		"",
		"123",
		"abc",
		"2026091512000a",
		"../20260915120000",
		"1234567890123456789012345", // > 24
	}
	for _, id := range invalid {
		if err := ValidateTxID(id); err == nil {
			t.Errorf("expected invalid for %s, got nil", id)
		}
	}
}

func TestValidateBasename(t *testing.T) {
	valid := []string{
		"config.yaml",
		"store.snapshot.20260915120000.json",
		"config.yaml.lkg",
		"recovery.marker",
	}
	for _, name := range valid {
		if err := ValidateBasename(name); err != nil {
			t.Errorf("expected valid basename for %s, got: %v", name, err)
		}
	}

	invalid := []string{
		"",
		".",
		"..",
		"dir/file.yaml",
		"../file.yaml",
		"file*.yaml",
		"file?.yaml",
	}
	for _, name := range invalid {
		if err := ValidateBasename(name); err == nil {
			t.Errorf("expected invalid basename for %s, got nil", name)
		}
	}
}

func TestStrictWriteAtomicAndUnlink(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "testfile.txt")
	content := []byte("hello world")

	// 1. StrictWriteAtomic
	if err := StrictWriteAtomic(target, content, 0644); err != nil {
		t.Fatalf("StrictWriteAtomic failed: %v", err)
	}

	read, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(read) != string(content) {
		t.Fatalf("content mismatch: got %q, want %q", string(read), string(content))
	}

	digest, err := ComputeFileDigest(target)
	if err != nil {
		t.Fatalf("ComputeFileDigest failed: %v", err)
	}
	if digest != ComputeBytesDigest(content) {
		t.Fatalf("digest mismatch: got %s, want %s", digest, ComputeBytesDigest(content))
	}

	// 2. StrictUnlink existing
	if err := StrictUnlink(target); err != nil {
		t.Fatalf("StrictUnlink failed: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file should not exist after unlink")
	}

	// 3. StrictUnlink non-existent (ENOENT) - must succeed and fsync directory (P0-9)
	if err := StrictUnlink(target); err != nil {
		t.Fatalf("StrictUnlink on ENOENT failed: %v", err)
	}
}

func TestStrictRenameAndQuarantine(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")
	content := []byte("quarantine me")

	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatalf("write src failed: %v", err)
	}

	// StrictRename
	if err := StrictRename(src, dst); err != nil {
		t.Fatalf("StrictRename failed: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("src still exists after rename")
	}
	read, err := os.ReadFile(dst)
	if err != nil || string(read) != string(content) {
		t.Fatalf("dst read mismatch: %v", err)
	}

	// StrictQuarantine
	quarantineDir := filepath.Join(tmpDir, "quarantine")
	qPath, err := StrictQuarantine(dst, quarantineDir, "corrupt_manifest")
	if err != nil {
		t.Fatalf("StrictQuarantine failed: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dst still exists after quarantine")
	}
	if _, err := os.Stat(qPath); err != nil {
		t.Fatalf("quarantined file does not exist at %s: %v", qPath, err)
	}
}

func TestInspectOutcome(t *testing.T) {
	tmpDir := t.TempDir()
	oldFile := filepath.Join(tmpDir, "old.txt")
	newFile := filepath.Join(tmpDir, "new.txt")
	oldData := []byte("old content")
	newData := []byte("new content")

	oldDigest := ComputeBytesDigest(oldData)
	newDigest := ComputeBytesDigest(newData)

	// Case 1: Destination has new content -> OutcomeNewApplied
	if err := os.WriteFile(newFile, newData, 0644); err != nil {
		t.Fatal(err)
	}
	outcome, err := InspectOutcome(oldFile, newFile, oldDigest, newDigest)
	if err != nil || outcome != OutcomeNewApplied {
		t.Fatalf("expected OutcomeNewApplied, got %v (err: %v)", outcome, err)
	}

	// Case 2: Destination has old content -> OutcomeOldRetained
	if err := os.WriteFile(newFile, oldData, 0644); err != nil {
		t.Fatal(err)
	}
	outcome, err = InspectOutcome(oldFile, newFile, oldDigest, newDigest)
	if err != nil || outcome != OutcomeOldRetained {
		t.Fatalf("expected OutcomeOldRetained, got %v (err: %v)", outcome, err)
	}

	// Case 3: Destination does not exist, old exists at src -> OutcomeOldRetained
	_ = os.Remove(newFile)
	if err := os.WriteFile(oldFile, oldData, 0644); err != nil {
		t.Fatal(err)
	}
	outcome, err = InspectOutcome(oldFile, newFile, oldDigest, newDigest)
	if err != nil || outcome != OutcomeOldRetained {
		t.Fatalf("expected OutcomeOldRetained, got %v (err: %v)", outcome, err)
	}

	// Case 4: Corrupt / unexpected content -> OutcomeAmbiguous
	if err := os.WriteFile(newFile, []byte("garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	outcome, err = InspectOutcome(oldFile, newFile, oldDigest, newDigest)
	if outcome != OutcomeAmbiguous || err == nil {
		t.Fatalf("expected OutcomeAmbiguous, got %v (err: %v)", outcome, err)
	}
}

func TestWriteAndResolve_WithFailpoint(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "atomic_resolve.txt")
	data := []byte("atomic payload")

	// Inject failpoint after rename pre sync (simulates sync failure after file is in place)
	SetFailpoint(FPAfterRenamePreSync, errors.New("simulated dir sync error"))
	defer ClearFailpoints()

	outcome, err := WriteAndResolve(target, data, 0644, "")
	// Should identify that new is applied on disk even though sync failed
	if outcome != OutcomeNewApplied {
		t.Fatalf("expected OutcomeNewApplied despite sync error, got: %v (err: %v)", outcome, err)
	}
}
