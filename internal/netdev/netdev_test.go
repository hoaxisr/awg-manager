package netdev

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := SysClassNet
	SysClassNet = root
	t.Cleanup(func() { SysClassNet = old })
	return root
}

func TestAbsent_NoDevice(t *testing.T) {
	withRoot(t)
	f, err := Absent("opkgtun3")
	if err != nil {
		t.Fatalf("Absent: %v", err)
	}
	if f.Name() != "opkgtun3" {
		t.Fatalf("Name() = %q", f.Name())
	}
}

func TestAbsent_Present(t *testing.T) {
	root := withRoot(t)
	if err := os.Mkdir(filepath.Join(root, "opkgtun3"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := Absent("opkgtun3")
	if !errors.Is(err, ErrPresent) {
		t.Fatalf("err = %v, want ErrPresent", err)
	}
	if !strings.Contains(err.Error(), "opkgtun3") {
		t.Fatalf("в тексте нет имени: %v", err)
	}
	if f.Name() != "" {
		t.Fatalf("при отказе доказательство пустое, Name() = %q", f.Name())
	}
}
