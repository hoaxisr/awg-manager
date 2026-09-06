package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteCreatesParentDirectoriesAndWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "file.txt")

	err := AtomicWrite(path, []byte("hello"))
	if err != nil {
		t.Fatalf("AtomicWrite() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("file content = %q, want %q", got, "hello")
	}
}

func TestAtomicWriteOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")

	if err := AtomicWrite(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, []byte("new")); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("file content = %q, want new", got)
	}
}

func TestAtomicWritePermUsesRequestedPermission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")

	if err := AtomicWritePerm(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("perm = %v, want 0600", got)
	}
}

func TestAtomicWriteReturnsErrorWhenParentIsFile(t *testing.T) {
	dir := t.TempDir()
	parentFile := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(parentFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	err := AtomicWrite(filepath.Join(parentFile, "child.txt"), []byte("data"))
	if err == nil {
		t.Fatal("AtomicWrite() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create directory") {
		t.Fatalf("error = %q, want create directory", err)
	}
}

func TestAtomicWriteRemovesTempFileOnRenameError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}

	err := AtomicWrite(target, []byte("data"))
	if err == nil {
		t.Fatal("AtomicWrite() error = nil, want rename error")
	}

	matches, globErr := filepath.Glob(target + ".tmp.*")
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files left after rename error: %v", matches)
	}
}

// Read-only каталог + существующий 0644-файл: tmp+rename обязан отказать
// (нельзя создать tmp), прямая запись тихо переписала бы файл. Старое содержимое цело.
func TestAtomicWrite_ReadOnlyDirRefusesAndKeepsOld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	failWrites(t, dir) // root-skip внутри
	err := AtomicWrite(path, []byte("NEW"))
	if err == nil || !strings.Contains(err.Error(), "write temp file") {
		t.Fatalf("err = %v, want write temp file", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "OLD" {
		t.Fatalf("старое содержимое затёрто: %q", b)
	}
}
