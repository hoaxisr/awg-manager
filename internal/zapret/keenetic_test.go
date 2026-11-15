package zapret

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setupKeenetic собирает раскладку nfqws2-keenetic (пакет ground-zerro):
// демон в usr/bin, конфиг в etc/nfqws2, списки в etc/nfqws2/lists, init —
// S51nfqws2. Каталога с «zapret» в имени нет.
func setupKeenetic(t *testing.T) (root, base string) {
	t.Helper()
	root = t.TempDir()
	prevOpt := optRoot
	t.Cleanup(func() { optRoot = prevOpt })
	optRoot = filepath.Join(root, "opt")

	base = filepath.Join(optRoot, "etc", "nfqws2")
	for _, d := range []string{base, filepath.Join(base, "lists"), filepath.Join(optRoot, "usr", "bin"), filepath.Join(optRoot, "etc", "init.d")} {
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
	if flavor := flavorFor(got.base, got.optVar); flavor != flavorKeenetic {
		t.Fatalf("flavor = %q, want %q", flavor, flavorKeenetic)
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

// nfqws2-keenetic читает user.list/exclude.list/auto.list; запись в файлы
// zapret-hosts-*.txt не влияла бы на обход.
func TestKeeneticHostlistFilenames(t *testing.T) {
	specs := hostlistSpecs(flavorKeenetic)
	want := map[HostlistID]string{
		HostlistUser:    "user.list",
		HostlistExclude: "exclude.list",
		HostlistAuto:    "auto.list",
	}
	if len(specs) != len(want) {
		t.Fatalf("specs = %d, want %d", len(specs), len(want))
	}
	for _, sp := range specs {
		expected, ok := want[sp.id]
		if !ok {
			t.Fatalf("unexpected hostlist id %q", sp.id)
		}
		if sp.filename != expected {
			t.Fatalf("%s filename = %q, want %q", sp.id, sp.filename, expected)
		}
	}
	found, ok := findSpec(HostlistUser, flavorKeenetic)
	if !ok || found.filename != "user.list" {
		t.Fatalf("findSpec(user, keenetic) = %+v, %v", found, ok)
	}
	// Для zapret2 имена не должны меняться.
	if legacy, _ := findSpec(HostlistUser, "zapret2"); legacy.filename != "zapret-hosts-user.txt" {
		t.Fatalf("zapret2 user filename = %q", legacy.filename)
	}
}

func TestKeeneticPackageIsRecognised(t *testing.T) {
	out := "awg-manager - 2.20.3\nnfqws2-keenetic - 1.3.3\nzapret22 - 2.1.0\n"
	found := parseZapretPackages(out)
	if found["nfqws2-keenetic"] != "1.3.3" {
		t.Fatalf("nfqws2-keenetic not found: %+v", found)
	}
	if found["zapret22"] != "2.1.0" {
		t.Fatalf("zapret22 not found: %+v", found)
	}
	if _, ok := found["awg-manager"]; ok {
		t.Fatalf("awg-manager must not be treated as zapret: %+v", found)
	}
}
