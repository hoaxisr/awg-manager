package zapret

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setupKeenetic собирает раскладку nfqws2-keenetic (её ставит пакет
// nfqws2-keenetic из ground-zerro): демон в usr/bin, конфиг в etc/nfqws2,
// init — S51nfqws2. Каталога с «zapret» в имени нет.
func setupKeenetic(t *testing.T) (root, base string) {
	t.Helper()
	root = t.TempDir()
	prevOpt := optRoot
	t.Cleanup(func() { optRoot = prevOpt })
	optRoot = filepath.Join(root, "opt")

	base = filepath.Join(optRoot, "etc", "nfqws2")
	for _, d := range []string{base, filepath.Join(optRoot, "usr", "bin"), filepath.Join(optRoot, "etc", "init.d")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := "NFQWS_BASE_ARGS=--qnum=300\nNFQWS_ARGS=--filter-tcp=443 --lua-desync=fake\nMODE_AUTO=1\nNFQUEUE_NUM=300\n"
	if err := os.WriteFile(filepath.Join(base, "nfqws2.conf"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(optRoot, "usr", "bin", "nfqws2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(optRoot, "etc", "init.d", "S51nfqws2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, base
}

func TestKeeneticLayoutIsDiscovered(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Проверка опирается на бит исполнения, который Windows не выставляет.
		t.Skip("exec bit is not modelled on Windows; the suite runs on Linux")
	}
	_, base := setupKeenetic(t)

	layouts := discoverLayouts()
	if len(layouts) != 1 {
		t.Fatalf("layouts = %d, want 1", len(layouts))
	}
	got := layouts[0]
	if filepath.Clean(got.base) != filepath.Clean(base) {
		t.Fatalf("base = %q, want %q", got.base, base)
	}
	if got.optVar != "" {
		t.Fatalf("optVar = %q, want empty", got.optVar)
	}
	if got.binary == "" {
		t.Fatal("binary was not detected")
	}
	if got.init == "" {
		t.Fatal("init script was not detected")
	}
	if flavor := flavorFor(got.base, got.optVar); flavor != "keenetic" {
		t.Fatalf("flavor = %q, want keenetic", flavor)
	}
}

func TestKeeneticBinaryMapsToConfDir(t *testing.T) {
	_, base := setupKeenetic(t)
	exe := filepath.Join(optRoot, "usr", "bin", "nfqws2")

	got := baseOfExe(exe)
	if filepath.Clean(got) != filepath.Clean(base) {
		t.Fatalf("baseOfExe(%q) = %q, want %q", exe, got, base)
	}
}

func TestKeeneticNotDetectedWithoutConfig(t *testing.T) {
	_, base := setupKeenetic(t)
	if err := os.Remove(filepath.Join(base, "nfqws2.conf")); err != nil {
		t.Fatal(err)
	}
	if layouts := discoverLayouts(); len(layouts) != 0 {
		t.Fatalf("layouts = %d, want 0 without config", len(layouts))
	}
}
