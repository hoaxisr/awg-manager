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

// TestSandboxSymlinkEscapeOutside verifies that a symlink placed inside a
// writable root pointing outside is denied (ErrPathDenied). The raw cleaned
// path is inside the root, but the resolved path is outside — both forms
// must lie inside the same root.
func TestSandboxSymlinkEscapeOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	allowed := filepath.Join(root, "etc")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a real file outside the root, then symlink to it from inside.
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "sneaky")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	s := NewSandbox([]Root{{Path: allowed, Label: "etc"}})

	_, _, err := s.Resolve(link)
	if err != ErrPathDenied {
		t.Fatalf("want ErrPathDenied for symlink escape, got %v", err)
	}
	if _, err := s.ResolveWrite(link); err != ErrPathDenied {
		t.Fatalf("ResolveWrite must deny symlink escape, got %v", err)
	}
}

// TestSandboxSymlinkInsideAllowed covers the legitimate case: a symlink that
// points to another location inside the same allowed root should resolve
// without error and return the resolved (real) path.
func TestSandboxSymlinkInsideAllowed(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "etc")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(allowed, "real.conf")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "link.conf")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s := NewSandbox([]Root{{Path: allowed, Label: "etc"}})

	abs, _, err := s.Resolve(link)
	if err != nil {
		t.Fatalf("intra-root symlink must be allowed, got %v", err)
	}
	if abs != real {
		t.Fatalf("want resolved=%q, got %q", real, abs)
	}
}

// На Keenetic /opt — симлинк на /tmp/mnt/<диск>/install. Корень, заданный
// через симлинк, должен работать: и для существующего файла, и для нового.
func TestResolve_RootBehindSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "mnt", "disk", "install")
	if err := os.MkdirAll(filepath.Join(real, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	opt := filepath.Join(base, "opt")
	if err := os.Symlink(real, opt); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(real, "etc", "conf.json")
	if err := os.WriteFile(existing, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb := NewSandbox([]Root{
		{Path: filepath.Join(opt, "bin"), Label: "bin", ReadOnly: true},
		{Path: opt, Label: "opt"},
	})

	abs, _, err := sb.Resolve(filepath.Join(opt, "etc", "conf.json"))
	if err != nil {
		t.Fatalf("существующий файл под корнем-симлинком отвергнут: %v", err)
	}
	if abs != existing {
		t.Errorf("abs = %q, want %q", abs, existing)
	}
	if _, err := sb.ResolveWrite(filepath.Join(opt, "etc", "new.conf")); err != nil {
		t.Errorf("новый файл под корнем-симлинком отвергнут: %v", err)
	}
	// Read-only корень держится и через реальный путь, и через симлинк.
	if _, err := sb.ResolveWrite(filepath.Join(real, "bin", "x")); err == nil {
		t.Error("запись в read-only корень по реальному пути разрешена")
	}
	if _, err := sb.ResolveWrite(filepath.Join(opt, "bin", "x")); err == nil {
		t.Error("запись в read-only корень через симлинк разрешена")
	}
}

// Симлинк, созданный внутри writable-корня и смотрящий наружу, — отказ.
// Отдельно: несуществующий файл ЗА таким симлинком тоже отказ.
func TestResolve_SymlinkEscapeDenied(t *testing.T) {
	base := t.TempDir()
	tmp := filepath.Join(base, "tmp")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "shadow")
	if err := os.WriteFile(secret, []byte("root:x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(tmp, "esc")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tmp, "dir")); err != nil {
		t.Fatal(err)
	}

	sb := NewSandbox([]Root{{Path: tmp, Label: "tmp"}})

	if abs, _, err := sb.Resolve(filepath.Join(tmp, "esc")); err == nil {
		t.Errorf("симлинк наружу пропущен: %s", abs)
	}
	if abs, err := sb.ResolveWrite(filepath.Join(tmp, "esc")); err == nil {
		t.Errorf("запись по симлинку наружу разрешена: %s", abs)
	}
	if abs, err := sb.ResolveWrite(filepath.Join(tmp, "dir", "new.txt")); err == nil {
		t.Errorf("создание файла за симлинком-каталогом наружу разрешено: %s", abs)
	}
}
