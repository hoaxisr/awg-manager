package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxResolve(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	awg := filepath.Join(etc, "awg-manager")
	if err := os.MkdirAll(awg, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSandbox([]Root{
		{Path: awg, Label: "AWG"},
		{Path: etc, Label: "etc"},
	})

	abs, _, err := s.Resolve(awg)
	if err != nil || abs != awg {
		t.Fatalf("resolve awg: abs=%q err=%v", abs, err)
	}

	_, _, err = s.Resolve(filepath.Join(root, "sibling-denied"))
	if err != ErrPathDenied {
		t.Fatalf("want ErrPathDenied, got %v", err)
	}

	_, err = s.ResolveWrite(filepath.Join(etc, "test.conf"))
	if err != nil {
		t.Fatalf("resolve write: %v", err)
	}
}

func TestSandboxTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSandbox([]Root{{Path: allowed, Label: "allowed"}})

	_, _, err := s.Resolve(filepath.Join(allowed, "..", "secret"))
	if err != ErrPathDenied {
		t.Fatalf("want denied for .. escape, got %v", err)
	}
}

func TestSandboxReadWrite(t *testing.T) {
	root := t.TempDir()
	s := NewSandbox([]Root{{Path: root, Label: "root"}})
	target := filepath.Join(root, "hello.txt")
	if err := s.WriteFile(target, "hi"); err != nil {
		t.Fatal(err)
	}
	content, _, err := s.ReadFile(target)
	if err != nil || content != "hi" {
		t.Fatalf("read: content=%q err=%v", content, err)
	}
}
