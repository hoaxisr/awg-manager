package firc

import (
	"context"
	"debug/elf"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeArchiveRejectsOwnershipSideEffects(t *testing.T) {
	i, _, _ := runtimeFixture(t)
	if err := i.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(i.layout.RootDir, "opt/var/cache/awg-manager/firc-runtime/verified.ipk")
	raw, err := os.ReadFile(pkg)
	if err != nil {
		t.Fatal(err)
	}
	outer, err := readRuntimeTar(raw, 2*maxRuntimeIPKBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Replaces", "Conflicts", "Provides", "conffiles"} {
		t.Run(field, func(t *testing.T) {
			control, err := readRuntimeTar(outer["control.tar.gz"].data, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			entry := control["control"]
			if field == "conffiles" {
				entry = control[field]
				entry.data = []byte("/opt/etc/firc/firc.conf\n")
				control[field] = entry
			} else {
				entry.data = append(entry.data, []byte(field+": awg-manager\n")...)
				control["control"] = entry
			}
			var members []tarMemoryEntry
			for name, entry := range control {
				members = append(members, tarMemoryEntry{name: name, mode: entry.header.Mode, data: entry.data})
			}
			ctrl, err := createTarGzFromEntries(members)
			if err != nil {
				t.Fatal(err)
			}
			data, err := createTarGzFromEntries([]tarMemoryEntry{
				{name: "debian-binary", mode: 0644, data: outer["debian-binary"].data},
				{name: "control.tar.gz", mode: 0644, data: ctrl},
				{name: "data.tar.gz", mode: 0644, data: outer["data.tar.gz"].data},
			})
			if err != nil {
				t.Fatal(err)
			}
			bad := filepath.Join(t.TempDir(), "bad.ipk")
			if err := os.WriteFile(bad, data, 0600); err != nil {
				t.Fatal(err)
			}
			err = VerifyRuntimeIPK(bad, i.arch, i.spec.Version)
			if err == nil || !(strings.Contains(err.Error(), "relationship") || strings.Contains(err.Error(), "configuration")) {
				t.Fatalf("expected ownership contract rejection, got %v", err)
			}
		})
	}
}

func TestRuntimeArchiveRejectsTraversalAndDuplicates(t *testing.T) {
	for _, names := range [][]string{{"../../outside"}, {"/absolute"}, {"opt/../outside"}, {"a", "a"}} {
		var files []tarMemoryEntry
		for _, name := range names {
			files = append(files, tarMemoryEntry{name: name, mode: 0644, data: []byte("x")})
		}
		data, err := createTarGzFromEntries(files)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := readRuntimeTar(data, 1024); err == nil {
			t.Fatalf("accepted unsafe archive %v", names)
		}
	}
}

func TestRuntimePackagesAllArchitectures(t *testing.T) {
	_, _, _ = runtimeFixture(t)
	source := os.Getenv("FIRC_SOURCE_ROOT")
	for _, tc := range []struct {
		arch    string
		class   elf.Class
		data    elf.Data
		machine elf.Machine
	}{
		{"aarch64-3.10", elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64},
		{"mipsel-3.4", elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS},
		{"mips-3.4", elf.ELFCLASS32, elf.ELFDATA2MSB, elf.EM_MIPS},
	} {
		t.Run(tc.arch, func(t *testing.T) {
			root := t.TempDir()
			daemon := filepath.Join(root, "fircd")
			tunnel := filepath.Join(root, "tunvless")
			for _, p := range []string{daemon, tunnel} {
				if err := os.WriteFile(p, makeMinimalELF(tc.class, tc.data, tc.machine, "fixture"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			pkg := filepath.Join(root, "runtime.ipk")
			if err := BuildRuntimeIPK(source, daemon, tunnel, tc.arch, "0.7.0-1", pkg); err != nil {
				t.Fatal(err)
			}
			if err := VerifyRuntimeIPK(pkg, tc.arch, "0.7.0-1"); err != nil {
				t.Fatal(err)
			}
			if err := VerifyRuntimeIPK(pkg, tc.arch, "0.7.0-2"); err == nil {
				t.Fatal("accepted wrong version")
			}
			entries, err := runtimePackageContents(pkg, tc.arch, "0.7.0-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, ownsConfig := entries["opt/etc/firc/firc.conf"]; ownsConfig {
				t.Fatal("package must not own user configuration")
			}
		})
	}
}

func TestRuntimeReinstallWorksFromVerifiedCache(t *testing.T) {
	i, _, _ := runtimeFixture(t)
	if err := i.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := i.Uninstall(context.Background(), nil, false, false); err != nil {
		t.Fatal(err)
	}
	i.SetDownloader(nil)
	if err := i.Install(context.Background()); err != nil {
		t.Fatalf("offline reinstall: %v", err)
	}
}

func TestInstalledRuntimeVerifiedAgainstArtifact(t *testing.T) {
	for _, rel := range []string{"opt/etc/firc/SHA256SUMS", "opt/etc/init.d/S99firc", "opt/share/licenses/awg-firc-runtime/LICENSE", "opt/etc/ndm/iflayerchanged.d/100-firc"} {
		t.Run(rel, func(t *testing.T) {
			i, _, _ := runtimeFixture(t)
			if err := i.Install(context.Background()); err != nil {
				t.Fatal(err)
			}
			pkg := filepath.Join(i.layout.RootDir, "opt/var/cache/awg-manager/firc-runtime/verified.ipk")
			if err := verifyInstalledRuntime(pkg, i.arch, i.spec.Version, i.layout); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(i.layout.RootDir, rel)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := verifyInstalledRuntime(pkg, i.arch, i.spec.Version, i.layout); err == nil {
				t.Fatal("accepted missing payload")
			}
			if err := os.WriteFile(p, []byte("unexpected replacement\n"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := verifyInstalledRuntime(pkg, i.arch, i.spec.Version, i.layout); err == nil {
				t.Fatal("accepted altered payload")
			}
		})
	}
}

func TestBuildRuntimeIPKUsesVendoredCCoreFirc(t *testing.T) {
	t.Setenv("FIRC_SOURCE_ROOT", "")
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	wantLicense, err := os.ReadFile(filepath.Join(repoRoot, "c-core", "firc", "LICENSE"))
	if err != nil {
		t.Fatalf("read vendored c-core/firc/LICENSE: %v", err)
	}

	root := t.TempDir()
	daemon := filepath.Join(root, "fircd")
	tunnel := filepath.Join(root, "tunvless")
	for _, p := range []string{daemon, tunnel} {
		if err := os.WriteFile(p, makeMinimalELF(elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS, "fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	const ver = "0.8.0-awgm.4"
	pkg := filepath.Join(root, "awg-firc-runtime.ipk")
	if err := BuildRuntimeIPK(repoRoot, daemon, tunnel, "mipsel-3.4", ver, pkg); err != nil {
		t.Fatalf("BuildRuntimeIPK with vendored c-core/firc: %v", err)
	}
	entries, err := runtimePackageContents(pkg, "mipsel-3.4", ver)
	if err != nil {
		t.Fatalf("runtimePackageContents: %v", err)
	}

	s99, ok := entries["opt/etc/init.d/S99firc"]
	if !ok || !strings.Contains(string(s99.data), ".awgm-disabled") {
		t.Fatalf("expected S99firc with .awgm-disabled from vendored c-core/firc")
	}
	for _, req := range []string{
		"opt/etc/ndm/netfilter.d/100-firc",
		"opt/etc/ndm/iflayerchanged.d/100-firc",
		"opt/share/awg-firc-runtime/defaults/firc.conf",
		"opt/share/awg-firc-runtime/defaults/groups.yaml",
	} {
		if _, ok := entries[req]; !ok {
			t.Fatalf("missing packaged asset %s", req)
		}
	}
	lic, ok := entries["opt/share/licenses/awg-firc-runtime/LICENSE"]
	if !ok || string(lic.data) != string(wantLicense) {
		t.Fatalf("packaged LICENSE does not match c-core/firc/LICENSE")
	}
}

// Откат обновления ставит IPK прежней сборки из кэша: он назван по-старому.
func TestVerifyRuntimeIPK_LegacyPackageName(t *testing.T) {
	_, ipk := buildTestRuntimeIPKVersion(t, t.TempDir(), "aarch64-3.10", "0.8.0-1", "https://example.invalid/r.ipk")
	for name, want := range map[string]bool{legacyRuntimePackageName: true, RuntimePackageName: true, "firc": false} {
		outer, err := readRuntimeTar(ipk, 2*maxRuntimeIPKBytes)
		if err != nil {
			t.Fatal(err)
		}
		ctrl, err := readRuntimeTar(outer["control.tar.gz"].data, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		var entries []tarMemoryEntry
		for _, f := range []string{"control", "conffiles", "postinst", "prerm"} {
			data := ctrl[f].data
			if f == "control" {
				data = []byte(strings.Replace(string(data), "Package: "+RuntimePackageName, "Package: "+name, 1))
			}
			entries = append(entries, tarMemoryEntry{name: f, mode: ctrl[f].header.Mode, data: data})
		}
		newCtrl, err := createTarGzFromEntries(entries)
		if err != nil {
			t.Fatal(err)
		}
		pkg, err := createTarGzFromEntries([]tarMemoryEntry{
			{name: "debian-binary", mode: 0o644, data: outer["debian-binary"].data},
			{name: "control.tar.gz", mode: 0o644, data: newCtrl},
			{name: "data.tar.gz", mode: 0o644, data: outer["data.tar.gz"].data},
		})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "r.ipk")
		if err := os.WriteFile(path, pkg, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := VerifyRuntimeIPK(path, "aarch64-3.10", "0.8.0-1"); (err == nil) != want {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
