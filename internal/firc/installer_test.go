package firc

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func ensureTestFircSourceRoot(t *testing.T, repoRoot string) {
	t.Helper()
	if envRoot := os.Getenv("FIRC_SOURCE_ROOT"); envRoot != "" {
		if _, err := os.Stat(filepath.Join(envRoot, "files", "entware_kn", "etc", "init.d", "S99firc")); err == nil {
			return
		}
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "c-core", "firc", "files", "entware_kn", "etc", "init.d", "S99firc")); err == nil {
		return
	}
	if sibling, err := filepath.Abs(filepath.Join(repoRoot, "..", "firc-upstream")); err == nil {
		if _, err := os.Stat(filepath.Join(sibling, "files", "entware_kn", "etc", "init.d", "S99firc")); err == nil {
			t.Setenv("FIRC_SOURCE_ROOT", sibling)
			return
		}
	}
	srcRoot := t.TempDir()
	files := map[string]struct {
		content string
		mode    os.FileMode
	}{
		"LICENSE":                             {"FIRC test fixture license\n", 0o644},
		"files/entware_kn/etc/init.d/S99firc": {"#!/bin/sh\n[ -e /opt/etc/firc/.awgm-disabled ] && exit 0\nexit 0\n", 0o755},
		"files/entware_kn/etc/ndm/netfilter.d/100-firc": {"#!/bin/sh\nexit 0\n", 0o755},
		"files/common/etc/firc/firc.conf":               {"# firc.conf\n", 0o644},
		"files/common/etc/firc/groups.yaml":             {"groups: []\n", 0o644},
	}
	for rel, spec := range files {
		p := filepath.Join(srcRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(spec.content), spec.mode); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	t.Setenv("FIRC_SOURCE_ROOT", srcRoot)
}

func TestStageAndVerifyPackageLayout(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	ensureTestFircSourceRoot(t, repoRoot)

	tmpSrc := t.TempDir()
	fircdSrc := filepath.Join(repoRoot, "c-core", "firc", "src", "backend-c", "build", "host", "fircd")
	if info, err := os.Stat(fircdSrc); err != nil || info.Size() == 0 {
		fircdSrc = filepath.Join(tmpSrc, "fircd")
		if err := os.WriteFile(fircdSrc, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64, "fircd-host"), 0o755); err != nil {
			t.Fatalf("write stub fircd: %v", err)
		}
	}

	tunvlessSrc := filepath.Join(repoRoot, "c-core", "firc", "src", "tunvless", "out", "tunvless")
	if info, err := os.Stat(tunvlessSrc); err != nil || info.Size() == 0 {
		tunvlessSrc = filepath.Join(tmpSrc, "tunvless")
		if err := os.WriteFile(tunvlessSrc, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64, "tunvless-host"), 0o755); err != nil {
			t.Fatalf("write stub tunvless: %v", err)
		}
	}

	targetRoot := t.TempDir()
	layout, err := StagePackageAssets(repoRoot, fircdSrc, tunvlessSrc, targetRoot, func(string) (int64, bool) {
		return 128 << 20, true // 128 MiB free
	})
	if err != nil {
		t.Fatalf("StagePackageAssets failed: %v", err)
	}

	if err := VerifyPackageLayout(layout); err != nil {
		t.Fatalf("VerifyPackageLayout failed: %v", err)
	}

	op := NewOperator(Options{
		ConfigDir:   filepath.Dir(layout.ConfigFile),
		FircdBinary: layout.FircdBin,
		TunvlessBin: layout.TunvlessBin,
		InitScript:  layout.InitScript,
	})
	if err := op.ValidateBinaries(); err != nil {
		t.Fatalf("ValidateBinaries failed on valid staged layout: %v", err)
	}

	// Tamper with tunvless binary and verify SHA256SUMS catches it
	if err := os.WriteFile(layout.TunvlessBin, []byte("corrupted-binary"), 0o755); err != nil {
		t.Fatalf("tamper tunvless: %v", err)
	}
	if err := VerifyPackageLayout(layout); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected sha256 mismatch error from VerifyPackageLayout, got: %v", err)
	}
	if err := op.ValidateBinaries(); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected sha256 mismatch error from ValidateBinaries, got: %v", err)
	}
}

func TestStagePackageAssets_InsufficientDiskSpace(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	tmpSrc := t.TempDir()
	fircdSrc := filepath.Join(tmpSrc, "fircd")
	tunvlessSrc := filepath.Join(tmpSrc, "tunvless")
	_ = os.WriteFile(fircdSrc, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64, "fircd"), 0o755)
	_ = os.WriteFile(tunvlessSrc, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64, "tunvless"), 0o755)

	targetRoot := t.TempDir()
	_, err = StagePackageAssets(repoRoot, fircdSrc, tunvlessSrc, targetRoot, func(string) (int64, bool) {
		return 1024, true // 1 KiB free < 5 MiB safety margin
	})
	if err == nil || !strings.Contains(err.Error(), "insufficient disk space") {
		t.Fatalf("expected insufficient disk space error, got: %v", err)
	}
}

func TestTargetELFVerificationAndIPKAcceptance(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	ensureTestFircSourceRoot(t, repoRoot)

	tmpDir := t.TempDir()
	hostFircd := filepath.Join(tmpDir, "fircd-x86_64")
	hostTunvless := filepath.Join(tmpDir, "tunvless-x86_64")
	aarch64Fircd := filepath.Join(tmpDir, "fircd-aarch64")
	aarch64Tunvless := filepath.Join(tmpDir, "tunvless-aarch64")
	mipselFircd := filepath.Join(tmpDir, "fircd-mipsel")
	mipselTunvless := filepath.Join(tmpDir, "tunvless-mipsel")

	_ = os.WriteFile(hostFircd, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64, "fircd-x86_64"), 0o755)
	_ = os.WriteFile(hostTunvless, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64, "tunvless-x86_64"), 0o755)
	_ = os.WriteFile(aarch64Fircd, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64, "fircd-aarch64"), 0o755)
	_ = os.WriteFile(aarch64Tunvless, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64, "tunvless-aarch64"), 0o755)
	_ = os.WriteFile(mipselFircd, makeMinimalELF(elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS, "fircd-mipsel"), 0o755)
	_ = os.WriteFile(mipselTunvless, makeMinimalELF(elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS, "tunvless-mipsel"), 0o755)

	// 1. Attempting to package x86_64 host binaries into an aarch64-3.10 or mipsel-3.4 IPK MUST fail closed
	badIPK := filepath.Join(tmpDir, "bad-host-in-aarch64.ipk")
	if err := BuildTargetIPK(repoRoot, hostFircd, hostTunvless, "aarch64-3.10", "0.7.0-test", badIPK); err == nil || !strings.Contains(err.Error(), "architecture mismatch") {
		t.Fatalf("expected architecture mismatch when packaging x86_64 host binaries for aarch64-3.10, got: %v", err)
	}
	if err := BuildTargetIPK(repoRoot, hostFircd, mipselTunvless, "mipsel-3.4", "0.7.0-test", badIPK); err == nil || !strings.Contains(err.Error(), "architecture mismatch") {
		t.Fatalf("expected architecture mismatch when fircd is x86_64 for mipsel-3.4, got: %v", err)
	}

	// 2. Build and verify a valid aarch64-3.10 IPK
	aarch64IPK := filepath.Join(tmpDir, "awg-manager_0.7.0_aarch64-3.10.ipk")
	if err := BuildTargetIPK(repoRoot, aarch64Fircd, aarch64Tunvless, "aarch64-3.10", "0.7.0-test", aarch64IPK); err != nil {
		t.Fatalf("BuildTargetIPK(aarch64-3.10) failed: %v", err)
	}
	if err := VerifyIPKArchive(aarch64IPK, "aarch64-3.10"); err != nil {
		t.Fatalf("VerifyIPKArchive(aarch64-3.10) failed: %v", err)
	}
	// Verifying the aarch64 IPK against mipsel-3.4 must fail
	if err := VerifyIPKArchive(aarch64IPK, "mipsel-3.4"); err == nil {
		t.Fatalf("expected VerifyIPKArchive to reject aarch64 IPK when expecting mipsel-3.4")
	}

	// 3. Build and verify a valid mipsel-3.4 IPK
	mipselIPK := filepath.Join(tmpDir, "awg-manager_0.7.0_mipsel-3.4.ipk")
	if err := BuildTargetIPK(repoRoot, mipselFircd, mipselTunvless, "mipsel-3.4", "0.7.0-test", mipselIPK); err != nil {
		t.Fatalf("BuildTargetIPK(mipsel-3.4) failed: %v", err)
	}
	if err := VerifyIPKArchive(mipselIPK, "mipsel-3.4"); err != nil {
		t.Fatalf("VerifyIPKArchive(mipsel-3.4) failed: %v", err)
	}
}

func TestBuildIPKScriptPackagingAndArchContract(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	// Execute scripts/build-ipk.sh across all 3 target architectures (aarch64-3.10, mipsel-3.4, mips-3.4)
	// to verify shell packaging orchestration and architecture mismatch rejection (P0.1).
	if runtime.GOOS != "linux" {
		t.Skip("scripts/build-ipk.sh execution requires Linux/WSL bash environment")
	}

	tmpDir := t.TempDir()
	archSpecs := []struct {
		pkgArch string
		awgArch string
		class   elf.Class
		data    elf.Data
		machine elf.Machine
	}{
		{"aarch64-3.10", "arm64", elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64},
		{"mipsel-3.4", "mipsle", elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS},
		{"mips-3.4", "mips", elf.ELFCLASS32, elf.ELFDATA2MSB, elf.EM_MIPS},
	}

	hostBin := filepath.Join(tmpDir, "host-x86_64")
	_ = os.WriteFile(hostBin, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64, "host-x86_64"), 0o755)

	for _, spec := range archSpecs {
		awgMgrBin := filepath.Join(tmpDir, "awg-manager-"+spec.awgArch)
		fircdBin := filepath.Join(tmpDir, "fircd-"+spec.awgArch)
		tunvlessBin := filepath.Join(tmpDir, "tunvless-"+spec.awgArch)
		_ = os.WriteFile(awgMgrBin, makeMinimalELF(spec.class, spec.data, spec.machine, "awg-manager-"+spec.awgArch), 0o755)
		_ = os.WriteFile(fircdBin, makeMinimalELF(spec.class, spec.data, spec.machine, "fircd-"+spec.awgArch), 0o755)
		_ = os.WriteFile(tunvlessBin, makeMinimalELF(spec.class, spec.data, spec.machine, "tunvless-"+spec.awgArch), 0o755)

		prebuiltAwg := filepath.Join(repoRoot, "prebuilt", "bin", "awg-"+spec.awgArch)
		createdPrebuiltAwg := false
		if _, err := os.Stat(prebuiltAwg); os.IsNotExist(err) {
			_ = os.MkdirAll(filepath.Dir(prebuiltAwg), 0o755)
			_ = os.WriteFile(prebuiltAwg, makeMinimalELF(spec.class, spec.data, spec.machine, "awg-cli-"+spec.awgArch), 0o755)
			createdPrebuiltAwg = true
		}
		if createdPrebuiltAwg {
			defer os.Remove(prebuiltAwg)
		}

		frontendIdx := filepath.Join(repoRoot, "frontend", "build", "index.html")
		createdFrontendIdx := false
		if _, err := os.Stat(frontendIdx); os.IsNotExist(err) {
			if _, gzErr := os.Stat(frontendIdx + ".gz"); os.IsNotExist(gzErr) {
				_ = os.MkdirAll(filepath.Dir(frontendIdx), 0o755)
				_ = os.WriteFile(frontendIdx, []byte("<!doctype html><html></html>"), 0o644)
				createdFrontendIdx = true
			}
		}
		if createdFrontendIdx {
			defer os.Remove(frontendIdx)
		}

		// Negative test: the manager package must reject a host executable.
		badCmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "build-ipk.sh"), "0.9.9-contract", spec.pkgArch)
		badCmd.Dir = repoRoot
		badCmd.Env = append(os.Environ(),
			"SKIP_FRONTEND_BUILD=1",
			"AWG_MANAGER_BIN_OVERRIDE="+hostBin,
			"FIRCD_BIN_OVERRIDE="+hostBin,
			"TUNVLESS_BIN_OVERRIDE="+tunvlessBin,
		)
		if out, err := badCmd.CombinedOutput(); err == nil {
			t.Fatalf("scripts/build-ipk.sh(%s) succeeded with host x86_64 fircd binary! Output:\n%s", spec.pkgArch, string(out))
		}

		// Positive test: valid target binaries -> scripts/build-ipk.sh succeeds and produces valid IPK
		okCmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "build-ipk.sh"), "0.9.9-contract", spec.pkgArch)
		okCmd.Dir = repoRoot
		okCmd.Env = append(os.Environ(),
			"SKIP_FRONTEND_BUILD=1",
			"AWG_MANAGER_BIN_OVERRIDE="+awgMgrBin,
			"XHTTPD_BIN_OVERRIDE="+fircdBin,
			"FPC_BIN_OVERRIDE="+tunvlessBin,
			"FIRCD_BIN_OVERRIDE="+fircdBin,
			"TUNVLESS_BIN_OVERRIDE="+tunvlessBin,
		)
		out, err := okCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("scripts/build-ipk.sh(%s) failed: %v\nOutput:\n%s", spec.pkgArch, err, string(out))
		}

		ipkPath := filepath.Join(repoRoot, "dist", "awg-manager_0.9.9-contract_"+spec.pkgArch+"-kn.ipk")
		defer os.Remove(ipkPath)
		verifyManagerOnlyIPK(t, ipkPath, spec.pkgArch)
	}
}

// TestRealCrossBuiltProductionIPKArtifactsAllArches is the strict production artifact acceptance gate (P2.1).
// It requires real cross-compiled ELF binaries (awg-manager, fircd, tunvless) for all three target architectures
// (aarch64-3.10, mipsel-3.4, mips-3.4) with zero synthetic fallback, verifies ELF headers, PT_INTERP, DT_NEEDED,
// readelf -h -l -d, builds the production .ipk via scripts/build-ipk.sh, and unpacks/verifies every .ipk.
func TestRealCrossBuiltProductionIPKArtifactsAllArches(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real cross-built ELF/IPK acceptance gate requires Linux/WSL environment")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if os.Getenv("FIRC_REQUIRE_CROSS_ARTIFACTS") != "1" {
		probe := filepath.Join(repoRoot, "c-core", "firc", "src", "backend-c", "build", "arm64", "fircd")
		if _, statErr := os.Stat(probe); os.IsNotExist(statErr) {
			t.Skip("skipping 3-arch cross-built ELF/IPK gate on clean checkout; set FIRC_REQUIRE_CROSS_ARTIFACTS=1 with staged cross-builds to enforce")
		}
	}
	ensureTestFircSourceRoot(t, repoRoot)

	archSpecs := []struct {
		pkgArch     string
		awgArch     string
		wantClass   string
		wantData    string
		wantMachine string
		wantInterp  string
		wantFlavour string
		class       elf.Class
		data        elf.Data
		machine     elf.Machine
	}{
		{
			pkgArch:     "aarch64-3.10",
			awgArch:     "arm64",
			wantClass:   "ELFCLASS64",
			wantData:    "ELFDATA2LSB",
			wantMachine: "EM_AARCH64",
			wantInterp:  "/opt/lib/ld-linux-aarch64.so.1",
			wantFlavour: "firc-flavour=entware_kn target=aarch64-3.10_kn",
			class:       elf.ELFCLASS64,
			data:        elf.ELFDATA2LSB,
			machine:     elf.EM_AARCH64,
		},
		{
			pkgArch:     "mipsel-3.4",
			awgArch:     "mipsle",
			wantClass:   "ELFCLASS32",
			wantData:    "ELFDATA2LSB",
			wantMachine: "EM_MIPS",
			wantInterp:  "/opt/lib/ld.so.1",
			wantFlavour: "firc-flavour=entware_kn target=mipsel-3.4_kn",
			class:       elf.ELFCLASS32,
			data:        elf.ELFDATA2LSB,
			machine:     elf.EM_MIPS,
		},
		{
			pkgArch:     "mips-3.4",
			awgArch:     "mips",
			wantClass:   "ELFCLASS32",
			wantData:    "ELFDATA2MSB",
			wantMachine: "EM_MIPS",
			wantInterp:  "/opt/lib/ld.so.1",
			wantFlavour: "firc-flavour=entware_kn target=mips-3.4_kn",
			class:       elf.ELFCLASS32,
			data:        elf.ELFDATA2MSB,
			machine:     elf.EM_MIPS,
		},
	}

	requiredFircdLibs := []string{
		"libyaml-0.so.2",
		"libpcre2-8.so.0",
		"libmnl.so.0",
		"libcjson.so.1",
		"libcurl.so.4",
		"libz.so.1",
	}

	for _, spec := range archSpecs {
		spec := spec
		t.Run(spec.pkgArch, func(t *testing.T) {
			awgMgrBin := filepath.Join(repoRoot, "build", "cross", "awg-manager-"+spec.awgArch)
			fircdBin := filepath.Join(repoRoot, "c-core", "firc", "src", "backend-c", "build", spec.awgArch, "fircd")
			tunvlessBin := filepath.Join(repoRoot, "c-core", "firc", "src", "tunvless", "out-"+spec.awgArch, "tunvless")

			for _, item := range []struct {
				name string
				path string
			}{
				{"awg-manager", awgMgrBin},
				{"fircd", fircdBin},
				{"tunvless", tunvlessBin},
			} {
				info, statErr := os.Stat(item.path)
				if statErr != nil {
					t.Fatalf("missing real cross-compiled %s binary for %s at %s: %v (synthetic fallback is forbidden)",
						item.name, spec.pkgArch, item.path, statErr)
				}
				if info.Size() < 100*1024 {
					t.Fatalf("binary %s for %s at %s is too small (%d bytes < 100 KiB); appears to be a synthetic stub",
						item.name, spec.pkgArch, item.path, info.Size())
				}
				if err := VerifyELFArch(item.path, spec.pkgArch); err != nil {
					t.Fatalf("VerifyELFArch(%s, %s) failed: %v", item.name, spec.pkgArch, err)
				}
				if err := VerifyELFArch(item.path, spec.awgArch); err != nil {
					t.Fatalf("VerifyELFArch(%s, %s) failed: %v", item.name, spec.awgArch, err)
				}
			}

			// Verify PT_INTERP and DT_NEEDED on real fircd and tunvless
			for _, cbin := range []struct {
				name string
				path string
			}{
				{"fircd", fircdBin},
				{"tunvless", tunvlessBin},
			} {
				rep, err := VerifyELFTargetLinkage(cbin.path, spec.pkgArch)
				if err != nil {
					t.Fatalf("VerifyELFTargetLinkage(%s, %s) failed: %v", cbin.name, spec.pkgArch, err)
				}
				if rep.Class != spec.wantClass || rep.Data != spec.wantData || rep.Machine != spec.wantMachine {
					t.Fatalf("unexpected ELF header for %s (%s): got %+v", cbin.name, spec.pkgArch, rep)
				}
				if rep.Interpreter != spec.wantInterp {
					t.Fatalf("unexpected PT_INTERP for %s (%s): got %q, want %q", cbin.name, spec.pkgArch, rep.Interpreter, spec.wantInterp)
				}
				if len(rep.NeededLibs) == 0 {
					t.Fatalf("expected non-empty DT_NEEDED for %s (%s)", cbin.name, spec.pkgArch)
				}

				// Run readelf -h -l -d to verify binutils ELF structure
				readelfCmd := exec.Command("readelf", "-h", "-l", "-d", cbin.path)
				if out, err := readelfCmd.CombinedOutput(); err != nil {
					t.Fatalf("readelf -h -l -d %s failed: %v\n%s", cbin.path, err, string(out))
				}
			}

			// Verify fircd DT_NEEDED libraries and Keenetic Entware target flavour marker
			fircdRep, err := VerifyELFTargetLinkage(fircdBin, spec.pkgArch)
			if err != nil {
				t.Fatalf("VerifyELFTargetLinkage(fircd, %s): %v", spec.pkgArch, err)
			}
			for _, reqLib := range requiredFircdLibs {
				found := false
				for _, gotLib := range fircdRep.NeededLibs {
					if gotLib == reqLib {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("fircd (%s) missing required DT_NEEDED library %q in %v", spec.pkgArch, reqLib, fircdRep.NeededLibs)
				}
			}
			fircdBytes, err := os.ReadFile(fircdBin)
			if err != nil {
				t.Fatalf("read fircd (%s): %v", spec.pkgArch, err)
			}
			if !strings.Contains(string(fircdBytes), spec.wantFlavour) {
				t.Fatalf("fircd (%s) missing expected Keenetic Entware flavour string %q", spec.pkgArch, spec.wantFlavour)
			}

			// Ensure prebuilt/bin/awg-<awgArch> and frontend/build/index.html exist for build-ipk.sh
			prebuiltAwg := filepath.Join(repoRoot, "prebuilt", "bin", "awg-"+spec.awgArch)
			createdPrebuiltAwg := false
			if _, err := os.Stat(prebuiltAwg); os.IsNotExist(err) {
				_ = os.MkdirAll(filepath.Dir(prebuiltAwg), 0o755)
				_ = os.WriteFile(prebuiltAwg, makeMinimalELF(spec.class, spec.data, spec.machine, "awg-cli-"+spec.awgArch), 0o755)
				createdPrebuiltAwg = true
			}
			if createdPrebuiltAwg {
				defer os.Remove(prebuiltAwg)
			}

			frontendIdx := filepath.Join(repoRoot, "frontend", "build", "index.html")
			createdFrontendIdx := false
			if _, err := os.Stat(frontendIdx); os.IsNotExist(err) {
				if _, gzErr := os.Stat(frontendIdx + ".gz"); os.IsNotExist(gzErr) {
					_ = os.MkdirAll(filepath.Dir(frontendIdx), 0o755)
					_ = os.WriteFile(frontendIdx, []byte("<!doctype html><html></html>"), 0o644)
					createdFrontendIdx = true
				}
			}
			if createdFrontendIdx {
				defer os.Remove(frontendIdx)
			}

			// Build production IPK using scripts/build-ipk.sh with the real cross-compiled binaries
			okCmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "build-ipk.sh"), "0.7.0-prod", spec.pkgArch)
			okCmd.Dir = repoRoot
			okCmd.Env = append(os.Environ(),
				"SKIP_FRONTEND_BUILD=1",
				"AWG_MANAGER_BIN_OVERRIDE="+awgMgrBin,
				"XHTTPD_BIN_OVERRIDE="+filepath.Join(repoRoot, "build", "cross", "awgm-xhttpd-"+spec.awgArch),
				"FPC_BIN_OVERRIDE="+filepath.Join(repoRoot, "build", "cross", "awgm-fpc-"+spec.awgArch),
				"FIRCD_BIN_OVERRIDE="+fircdBin,
				"TUNVLESS_BIN_OVERRIDE="+tunvlessBin,
			)
			out, err := okCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("scripts/build-ipk.sh(%s) failed: %v\nOutput:\n%s", spec.pkgArch, err, string(out))
			}

			ipkPath := filepath.Join(repoRoot, "dist", "awg-manager_0.7.0-prod_"+spec.pkgArch+"-kn.ipk")
			defer os.Remove(ipkPath)
			verifyManagerOnlyIPK(t, ipkPath, spec.pkgArch)
			runtimeIPK := filepath.Join(t.TempDir(), "runtime.ipk")
			if err := BuildRuntimeIPK(repoRoot, fircdBin, tunvlessBin, spec.pkgArch, "0.7.0-prod", runtimeIPK); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func verifyManagerOnlyIPK(t *testing.T, pkg, arch string) {
	t.Helper()
	raw, err := os.ReadFile(pkg)
	if err != nil {
		t.Fatal(err)
	}
	outer, err := extractTarGzToMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	control, err := extractTarGzToMap(outer["control.tar.gz"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(control["control"]), "Architecture: "+arch) {
		t.Fatal("wrong manager architecture")
	}
	payload, err := extractTarGzToMap(outer["data.tar.gz"])
	if err != nil {
		t.Fatal(err)
	}
	for p := range runtimePayload {
		if _, ok := payload[p]; ok {
			t.Fatalf("manager package owns FIRC runtime: %s", p)
		}
	}
	for _, p := range []string{"opt/bin/awg-manager", "opt/bin/awgm-xhttpd", "opt/bin/awgm-fpc"} {
		data, ok := payload[p]
		if !ok {
			t.Fatalf("manager package missing %s", p)
		}
		bin := filepath.Join(t.TempDir(), filepath.Base(p))
		if err := os.WriteFile(bin, data, 0755); err != nil {
			t.Fatal(err)
		}
		if err := VerifyELFArch(bin, arch); err != nil {
			t.Fatal(err)
		}
	}
}
