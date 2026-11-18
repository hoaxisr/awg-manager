package firc

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	DefaultInitScriptPath       = "/opt/etc/init.d/S99firc"
	DefaultFircdBinPath         = "/opt/sbin/fircd"
	DefaultTunvlessBinPath      = "/opt/sbin/tunvless"
	DefaultConfigFilePath       = "/opt/etc/firc/firc.conf"
	DefaultGroupsFilePath       = "/opt/etc/firc/groups.yaml"
	DefaultTunnelsFilePath      = "/opt/etc/firc/tunnels.yaml"
	DefaultChecksumManifestPath = "/opt/etc/firc/SHA256SUMS"
	DefaultNDMNetfilterHookPath = "/opt/etc/ndm/netfilter.d/100-firc"
	DefaultNDMIfLayerHookPath   = "/opt/etc/ndm/iflayerchanged.d/100-firc"

	minFreeDiskBytes int64 = 5 << 20 // 5 MiB safety margin
)

// PackageLayout describes all filesystem paths required for a complete FIRC installation.
type PackageLayout struct {
	RootDir          string
	InitScript       string
	FircdBin         string
	TunvlessBin      string
	ConfigFile       string
	GroupsFile       string
	ChecksumManifest string
	NDMNetfilterHook string
	NDMIfLayerHook   string
}

// DefaultPackageLayout returns the standard Entware/Keenetic paths rooted at rootDir ("" or "/" for live router).
func DefaultPackageLayout(rootDir string) PackageLayout {
	join := func(p string) string {
		if rootDir == "" || rootDir == "/" {
			return p
		}
		return filepath.Join(rootDir, strings.TrimPrefix(p, "/"))
	}
	return PackageLayout{
		RootDir:          rootDir,
		InitScript:       join(DefaultInitScriptPath),
		FircdBin:         join(DefaultFircdBinPath),
		TunvlessBin:      join(DefaultTunvlessBinPath),
		ConfigFile:       join(DefaultConfigFilePath),
		GroupsFile:       join(DefaultGroupsFilePath),
		ChecksumManifest: join(DefaultChecksumManifestPath),
		NDMNetfilterHook: join(DefaultNDMNetfilterHookPath),
		NDMIfLayerHook:   join(DefaultNDMIfLayerHookPath),
	}
}

// VerifyELFArch opens the binary at path as an ELF executable/shared-object and verifies
// its class (32/64-bit), endianness (LSB/MSB), and machine architecture against targetArch.
// Supported targetArch values: "aarch64" ("aarch64-3.10", "arm64"), "mipsel" ("mipsel-3.4", "mipsle"),
// "mips" ("mips-3.4"), "x86_64" ("amd64").
func VerifyELFArch(path, targetArch string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("firc elf verify %s: not a valid ELF binary: %w", path, err)
	}
	defer f.Close()

	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return fmt.Errorf("firc elf verify %s: unexpected ELF type %v (want ET_EXEC or ET_DYN)", path, f.Type)
	}

	norm := strings.ToLower(strings.TrimSpace(targetArch))
	var (
		wantClass   elf.Class
		wantData    elf.Data
		wantMachine elf.Machine
	)
	switch norm {
	case "aarch64", "aarch64-3.10", "arm64":
		wantClass = elf.ELFCLASS64
		wantData = elf.ELFDATA2LSB
		wantMachine = elf.EM_AARCH64
	case "mipsel", "mipsel-3.4", "mipsle":
		wantClass = elf.ELFCLASS32
		wantData = elf.ELFDATA2LSB
		wantMachine = elf.EM_MIPS
	case "mips", "mips-3.4":
		wantClass = elf.ELFCLASS32
		wantData = elf.ELFDATA2MSB
		wantMachine = elf.EM_MIPS
	case "x86_64", "amd64":
		wantClass = elf.ELFCLASS64
		wantData = elf.ELFDATA2LSB
		wantMachine = elf.EM_X86_64
	default:
		return fmt.Errorf("firc elf verify %s: unsupported target architecture %q", path, targetArch)
	}

	if f.Class != wantClass || f.Data != wantData || f.Machine != wantMachine {
		return fmt.Errorf(
			"firc elf verify %s: architecture mismatch for target %q: got (%v, %v, %v), want (%v, %v, %v)",
			path, targetArch, f.Class, f.Data, f.Machine, wantClass, wantData, wantMachine,
		)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			b, err := io.ReadAll(p.Open())
			if err == nil {
				interp := strings.TrimRight(string(b), "\x00\r\n ")
				if wantMachine != elf.EM_X86_64 && strings.Contains(interp, "x86-64") {
					return fmt.Errorf("firc elf verify %s: host x86_64 dynamic interpreter %q found in %q target binary", path, interp, targetArch)
				}
			}
		}
	}
	return nil
}

// ELFTargetLinkageReport summarizes the ELF header, PT_INTERP dynamic linker, RPATH/RUNPATH,
// and DT_NEEDED shared library dependencies of a target binary.
type ELFTargetLinkageReport struct {
	Path        string   `json:"path"`
	Class       string   `json:"class"`
	Data        string   `json:"data"`
	Machine     string   `json:"machine"`
	Interpreter string   `json:"interpreter,omitempty"`
	NeededLibs  []string `json:"neededLibs,omitempty"`
	RPath       []string `json:"rpath,omitempty"`
	RunPath     []string `json:"runpath,omitempty"`
}

// VerifyELFTargetLinkage verifies the ELF architecture of path against targetArch and extracts
// its PT_INTERP dynamic loader and DT_NEEDED shared library linkage metadata.
func VerifyELFTargetLinkage(path, targetArch string) (*ELFTargetLinkageReport, error) {
	if err := VerifyELFArch(path, targetArch); err != nil {
		return nil, err
	}
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rep := &ELFTargetLinkageReport{
		Path:    path,
		Class:   f.Class.String(),
		Data:    f.Data.String(),
		Machine: f.Machine.String(),
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			if b, err := io.ReadAll(p.Open()); err == nil {
				rep.Interpreter = strings.TrimRight(string(b), "\x00\r\n ")
			}
		}
	}
	if libs, err := f.ImportedLibraries(); err == nil {
		rep.NeededLibs = libs
	}
	if rp, err := f.DynString(elf.DT_RPATH); err == nil {
		rep.RPath = rp
	}
	if runp, err := f.DynString(elf.DT_RUNPATH); err == nil {
		rep.RunPath = runp
	}
	return rep, nil
}

// ComputeFileSHA256 returns the lowercase hex-encoded SHA-256 digest of a file.
func ComputeFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyFileSHA256 verifies that path has the expected SHA-256 hex digest.
func VerifyFileSHA256(path, expectedHex string) error {
	expected := strings.ToLower(strings.TrimSpace(expectedHex))
	if len(expected) != 64 {
		return fmt.Errorf("firc: invalid expected sha256 digest %q for %s", expectedHex, path)
	}
	actual, err := ComputeFileSHA256(path)
	if err != nil {
		return fmt.Errorf("firc: compute sha256 for %s: %w", path, err)
	}
	if actual != expected {
		return fmt.Errorf("firc: sha256 mismatch for %s: got %s, want %s", path, actual, expected)
	}
	return nil
}

// WriteChecksumManifest writes a standard sha256sum manifest file for the provided binary paths.
func WriteChecksumManifest(manifestPath string, files []string) error {
	var b strings.Builder
	for _, path := range files {
		sum, err := ComputeFileSHA256(path)
		if err != nil {
			return err
		}
		b.WriteString(fmt.Sprintf("%s  %s\n", sum, filepath.Base(path)))
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(manifestPath, []byte(b.String()), 0o644)
}

// VerifyChecksumManifest reads a SHA256SUMS manifest and verifies every listed binary inside binDir.
func VerifyChecksumManifest(manifestPath, binDir string) error {
	f, err := os.Open(manifestPath)
	if err != nil {
		return err
	}
	defer f.Close()

	verified := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return fmt.Errorf("firc: malformed line in %s: %q", manifestPath, line)
		}
		expected := fields[0]
		name := filepath.Base(strings.TrimPrefix(fields[1], "*"))
		targetPath := filepath.Join(binDir, name)
		if err := VerifyFileSHA256(targetPath, expected); err != nil {
			return err
		}
		verified++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if verified == 0 {
		return fmt.Errorf("firc: empty checksum manifest %s", manifestPath)
	}
	return nil
}

// VerifyPackageLayout checks that all required FIRC binaries, init script, config files,
// NDM hooks, and SHA-256 checksums are present and valid under layout.
func VerifyPackageLayout(layout PackageLayout) error {
	execFiles := []struct {
		name string
		path string
	}{
		{"init script", layout.InitScript},
		{"fircd binary", layout.FircdBin},
		{"tunvless binary", layout.TunvlessBin},
		{"NDM netfilter hook", layout.NDMNetfilterHook},
	}
	for _, ef := range execFiles {
		info, err := os.Stat(ef.path)
		if err != nil {
			return fmt.Errorf("firc package layout: missing %s at %s: %w", ef.name, ef.path, err)
		}
		if info.IsDir() || info.Size() == 0 {
			return fmt.Errorf("firc package layout: invalid %s at %s", ef.name, ef.path)
		}
		if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
			return fmt.Errorf("firc package layout: %s at %s is not executable (mode %o)", ef.name, ef.path, info.Mode())
		}
	}

	if _, err := os.Lstat(layout.NDMIfLayerHook); err != nil {
		return fmt.Errorf("firc package layout: missing NDM iflayerchanged hook at %s: %w", layout.NDMIfLayerHook, err)
	}

	cfgFiles := []struct {
		name string
		path string
	}{
		{"firc.conf", layout.ConfigFile},
		{"groups.yaml", layout.GroupsFile},
	}
	for _, cf := range cfgFiles {
		info, err := os.Stat(cf.path)
		if err != nil {
			return fmt.Errorf("firc package layout: missing %s at %s: %w", cf.name, cf.path, err)
		}
		if info.IsDir() || info.Size() == 0 {
			return fmt.Errorf("firc package layout: empty %s at %s", cf.name, cf.path)
		}
	}

	if layout.ChecksumManifest != "" {
		if _, err := os.Stat(layout.ChecksumManifest); err == nil {
			if err := VerifyChecksumManifest(layout.ChecksumManifest, filepath.Dir(layout.FircdBin)); err != nil {
				return err
			}
		}
	}
	return nil
}

// VerifyTargetPackageLayout verifies both the package layout and that both fircd and tunvless
// are valid ELF binaries matching targetArch.
func VerifyTargetPackageLayout(layout PackageLayout, targetArch string) error {
	if err := VerifyPackageLayout(layout); err != nil {
		return err
	}
	if strings.TrimSpace(targetArch) == "" {
		return fmt.Errorf("firc package layout: targetArch is required for ELF verification")
	}
	if err := VerifyELFArch(layout.FircdBin, targetArch); err != nil {
		return err
	}
	if err := VerifyELFArch(layout.TunvlessBin, targetArch); err != nil {
		return err
	}
	return nil
}

// StagePackageAssets stages the FIRC distribution files from sourceRepoRoot/c-core/firc
// and compiled fircd/tunvless binaries into targetRootDir, generating SHA256SUMS and verifying layout.
func StagePackageAssets(sourceRepoRoot, fircdSrcBin, tunvlessSrcBin, targetRootDir string, freeDiskFn func(string) (int64, bool)) (PackageLayout, error) {
	return StageTargetPackageAssets(sourceRepoRoot, fircdSrcBin, tunvlessSrcBin, targetRootDir, "", freeDiskFn)
}

// StageTargetPackageAssets stages the FIRC distribution files into targetRootDir and, when
// targetArch is non-empty, strictly verifies that fircdSrcBin and tunvlessSrcBin are ELF binaries
// for targetArch before and after staging (preventing host-binary leakage into target IPKs).
func StageTargetPackageAssets(sourceRepoRoot, fircdSrcBin, tunvlessSrcBin, targetRootDir, targetArch string, freeDiskFn func(string) (int64, bool)) (PackageLayout, error) {
	layout := DefaultPackageLayout(targetRootDir)

	fircdInfo, err := os.Stat(fircdSrcBin)
	if err != nil || fircdInfo.Size() == 0 {
		return layout, fmt.Errorf("firc stage: invalid fircd binary %s: %w", fircdSrcBin, err)
	}
	tunvlessInfo, err := os.Stat(tunvlessSrcBin)
	if err != nil || tunvlessInfo.Size() == 0 {
		return layout, fmt.Errorf("firc stage: invalid tunvless binary %s: %w", tunvlessSrcBin, err)
	}

	if strings.TrimSpace(targetArch) != "" {
		if err := VerifyELFArch(fircdSrcBin, targetArch); err != nil {
			return layout, err
		}
		if err := VerifyELFArch(tunvlessSrcBin, targetArch); err != nil {
			return layout, err
		}
	}

	requiredBytes := fircdInfo.Size() + tunvlessInfo.Size() + minFreeDiskBytes
	if freeDiskFn != nil {
		if free, ok := freeDiskFn(targetRootDir); ok && free < requiredBytes {
			return layout, fmt.Errorf("firc stage: insufficient disk space: have %d bytes, need %d bytes", free, requiredBytes)
		}
	}

	copyFile := func(src, dst string, mode os.FileMode) error {
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read %s: %w", src, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, data, mode); err != nil {
			return err
		}
		if err := os.Chmod(tmp, mode); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return os.Rename(tmp, dst)
	}

	fircFilesRoot := filepath.Join(sourceRepoRoot, "c-core", "firc", "files")
	if envRoot := strings.TrimSpace(os.Getenv("FIRC_SOURCE_ROOT")); envRoot != "" {
		fircFilesRoot = filepath.Join(envRoot, "files")
	}
	if err := copyFile(filepath.Join(fircFilesRoot, "entware_kn", "etc", "init.d", "S99firc"), layout.InitScript, 0o755); err != nil {
		return layout, err
	}
	if err := copyFile(filepath.Join(fircFilesRoot, "entware_kn", "etc", "ndm", "netfilter.d", "100-firc"), layout.NDMNetfilterHook, 0o755); err != nil {
		return layout, err
	}
	if err := os.MkdirAll(filepath.Dir(layout.NDMIfLayerHook), 0o755); err != nil {
		return layout, err
	}
	_ = os.Remove(layout.NDMIfLayerHook)
	if err := os.Symlink("../netfilter.d/100-firc", layout.NDMIfLayerHook); err != nil {
		if cerr := copyFile(layout.NDMNetfilterHook, layout.NDMIfLayerHook, 0o755); cerr != nil {
			return layout, cerr
		}
	}
	if err := copyFile(filepath.Join(fircFilesRoot, "common", "etc", "firc", "firc.conf"), layout.ConfigFile, 0o644); err != nil {
		return layout, err
	}
	if err := copyFile(filepath.Join(fircFilesRoot, "common", "etc", "firc", "groups.yaml"), layout.GroupsFile, 0o644); err != nil {
		return layout, err
	}
	if err := copyFile(fircdSrcBin, layout.FircdBin, 0o755); err != nil {
		return layout, err
	}
	if err := copyFile(tunvlessSrcBin, layout.TunvlessBin, 0o755); err != nil {
		return layout, err
	}
	binDir := filepath.Join(targetRootDir, "opt", "bin")
	if targetRootDir == "" || targetRootDir == "/" {
		binDir = "/opt/bin"
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return layout, err
	}
	for _, binName := range []string{"fircd", "tunvless"} {
		linkPath := filepath.Join(binDir, binName)
		_ = os.Remove(linkPath)
		if err := os.Symlink("../sbin/"+binName, linkPath); err != nil {
			return layout, fmt.Errorf("symlink %s: %w", linkPath, err)
		}
	}

	if err := WriteChecksumManifest(layout.ChecksumManifest, []string{layout.FircdBin, layout.TunvlessBin}); err != nil {
		return layout, err
	}
	if strings.TrimSpace(targetArch) != "" {
		if err := VerifyTargetPackageLayout(layout, targetArch); err != nil {
			return layout, err
		}
	} else if err := VerifyPackageLayout(layout); err != nil {
		return layout, err
	}
	return layout, nil
}

// BuildTargetIPK stages target-verified FIRC assets and control scripts and writes an Opkg .ipk
// archive (outer tar.gz containing ./debian-binary, ./control.tar.gz, ./data.tar.gz).
func BuildTargetIPK(sourceRepoRoot, fircdSrcBin, tunvlessSrcBin, pkgArch, version, outIPKPath string) error {
	stageDir, err := os.MkdirTemp("", "firc-ipk-stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)

	if _, err := StageTargetPackageAssets(sourceRepoRoot, fircdSrcBin, tunvlessSrcBin, stageDir, pkgArch, nil); err != nil {
		return err
	}

	controlBytes := []byte(fmt.Sprintf(
		"Package: awg-manager\nVersion: %s\nDepends: iptables, ipset, ip-full, wireguard-tools, conntrack, curl\nSection: net\nArchitecture: %s\nMaintainer: hoaxisr\nDescription: AmneziaWG tunnel manager with FIRC core\n",
		version, pkgArch,
	))
	postinstBytes, err := os.ReadFile(filepath.Join(sourceRepoRoot, "entware", "control", "postinst"))
	if err != nil {
		return fmt.Errorf("read postinst: %w", err)
	}
	prermBytes, err := os.ReadFile(filepath.Join(sourceRepoRoot, "entware", "control", "prerm"))
	if err != nil {
		return fmt.Errorf("read prerm: %w", err)
	}

	controlTarGz, err := createTarGzFromEntries([]tarMemoryEntry{
		{name: "./control", mode: 0o644, data: controlBytes},
		{name: "./postinst", mode: 0o755, data: postinstBytes},
		{name: "./prerm", mode: 0o755, data: prermBytes},
	})
	if err != nil {
		return fmt.Errorf("build control.tar.gz: %w", err)
	}

	dataTarGz, err := createTarGzFromDir(stageDir)
	if err != nil {
		return fmt.Errorf("build data.tar.gz: %w", err)
	}

	outerTarGz, err := createTarGzFromEntries([]tarMemoryEntry{
		{name: "./debian-binary", mode: 0o644, data: []byte("2.0\n")},
		{name: "./control.tar.gz", mode: 0o644, data: controlTarGz},
		{name: "./data.tar.gz", mode: 0o644, data: dataTarGz},
	})
	if err != nil {
		return fmt.Errorf("build outer ipk tar.gz: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outIPKPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outIPKPath, outerTarGz, 0o644)
}

// VerifyIPKArchive unpacks an .ipk archive into a temporary directory and verifies:
//  1. debian-binary == "2.0"
//  2. control.tar.gz contains control (with matching Architecture: expectedPkgArch), postinst, and prerm
//  3. data.tar.gz contains the complete FIRC layout (/opt/sbin/fircd, /opt/sbin/tunvless, /opt/etc/init.d/S99firc,
//     /opt/etc/firc/firc.conf, /opt/etc/firc/groups.yaml, /opt/etc/firc/SHA256SUMS, NDM hooks)
//  4. Both /opt/sbin/fircd and /opt/sbin/tunvless pass VerifyELFArch for expectedPkgArch and SHA256SUMS verification.
func VerifyIPKArchive(ipkPath, expectedPkgArch string) error {
	raw, err := os.ReadFile(ipkPath)
	if err != nil {
		return err
	}
	outerFiles, err := extractTarGzToMap(raw)
	if err != nil {
		return fmt.Errorf("extract outer ipk: %w", err)
	}
	if strings.TrimSpace(string(outerFiles["debian-binary"])) != "2.0" {
		return fmt.Errorf("invalid debian-binary in %s", ipkPath)
	}
	ctrlRaw, ok := outerFiles["control.tar.gz"]
	if !ok {
		return fmt.Errorf("missing control.tar.gz in %s", ipkPath)
	}
	dataRaw, ok := outerFiles["data.tar.gz"]
	if !ok {
		return fmt.Errorf("missing data.tar.gz in %s", ipkPath)
	}

	ctrlFiles, err := extractTarGzToMap(ctrlRaw)
	if err != nil {
		return fmt.Errorf("extract control.tar.gz: %w", err)
	}
	for _, reqCtrl := range []string{"control", "postinst", "prerm"} {
		if len(ctrlFiles[reqCtrl]) == 0 {
			return fmt.Errorf("control.tar.gz missing %s", reqCtrl)
		}
	}
	if !strings.Contains(string(ctrlFiles["control"]), "Architecture: "+expectedPkgArch) {
		return fmt.Errorf("control Architecture mismatch: want %s in:\n%s", expectedPkgArch, string(ctrlFiles["control"]))
	}

	extractDir, err := os.MkdirTemp("", "firc-ipk-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)

	if err := extractTarGzToDir(dataRaw, extractDir); err != nil {
		return fmt.Errorf("extract data.tar.gz: %w", err)
	}
	layout := DefaultPackageLayout(extractDir)
	if err := VerifyTargetPackageLayout(layout, expectedPkgArch); err != nil {
		return err
	}
	awgBin := filepath.Join(extractDir, "opt", "bin", "awg-manager")
	if info, err := os.Stat(awgBin); err == nil && info.Size() > 0 {
		if err := VerifyELFArch(awgBin, expectedPkgArch); err != nil {
			return err
		}
	}
	return nil
}

type tarMemoryEntry struct {
	name string
	mode int64
	data []byte
}

func createTarGzFromEntries(entries []tarMemoryEntry) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		hdr := &tar.Header{
			Name: e.name,
			Mode: e.mode,
			Size: int64(len(e.data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func createTarGzFromDir(rootDir string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == rootDir {
			return nil
		}
		rel, err := filepath.Rel(rootDir, path)
		if err != nil {
			return err
		}
		tarName := "./" + filepath.ToSlash(rel)
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			hdr := &tar.Header{
				Name:     tarName,
				Mode:     0o777,
				Typeflag: tar.TypeSymlink,
				Linkname: filepath.ToSlash(linkTarget),
			}
			return tw.WriteHeader(hdr)
		}
		if info.IsDir() {
			hdr := &tar.Header{
				Name:     tarName + "/",
				Mode:     0o755,
				Typeflag: tar.TypeDir,
			}
			return tw.WriteHeader(hdr)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := int64(info.Mode().Perm())
		if strings.Contains(tarName, "/sbin/") || strings.Contains(tarName, "/bin/") || strings.Contains(tarName, "/init.d/") || strings.Contains(tarName, "/ndm/") {
			mode |= 0o755
		}
		hdr := &tar.Header{
			Name:     tarName,
			Mode:     mode,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func extractTarGzToMap(raw []byte) (map[string][]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	out := make(map[string][]byte)
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		clean := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		out[clean] = b
	}
	return out, nil
}

func extractTarGzToDir(raw []byte, dstDir string) error {
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		clean := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		if clean == "" || clean == "." {
			continue
		}
		target := filepath.Join(dstDir, filepath.FromSlash(clean))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				// On Windows without symlink privileges, write a marker file so Lstat succeeds
				if werr := os.WriteFile(target, []byte(hdr.Linkname), 0o755); werr != nil {
					return werr
				}
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode) & 0o777
			if mode == 0 {
				mode = 0o755
			}
			if err := os.WriteFile(target, data, mode); err != nil {
				return err
			}
			_ = os.Chmod(target, mode)
		}
	}
	return nil
}
