package firc

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Only manager-specific runtime assets can be installed by this package. The
// complete archive hash is pinned; these checks additionally prevent accidental
// packaging of unrelated root-owned files and non-target executables.
var runtimePayload = map[string]bool{
	"opt/sbin/fircd": true, "opt/sbin/tunvless": true,
	"opt/bin/fircd": true, "opt/bin/tunvless": true,
	"opt/etc/init.d/S99firc": true, "opt/etc/ndm/netfilter.d/100-firc": true,
	"opt/etc/ndm/iflayerchanged.d/100-firc":         true,
	"opt/share/awg-firc-runtime/defaults/firc.conf": true, "opt/share/awg-firc-runtime/defaults/groups.yaml": true, "opt/etc/firc/SHA256SUMS": true,
	"opt/share/licenses/awg-firc-runtime/LICENSE": true,
}

var runtimeExpectedSymlinks = map[string]string{
	"opt/bin/fircd":                         "../sbin/fircd",
	"opt/bin/tunvless":                      "../sbin/tunvless",
	"opt/etc/ndm/iflayerchanged.d/100-firc": "../netfilter.d/100-firc",
}

type runtimeTarEntry struct {
	header tar.Header
	data   []byte
}

func readRuntimeTar(raw []byte, maxBytes int64) (map[string]runtimeTarEntry, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	entries := map[string]runtimeTarEntry{}
	var total int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(header.Name, "./")
		name = strings.TrimSuffix(name, "/")
		if name == "" || name == "." {
			if header.Typeflag == tar.TypeDir {
				continue
			}
			return nil, errors.New("empty archive path")
		}
		if path.IsAbs(name) || strings.Contains(name, "\\") || strings.Contains(name, ":") || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("unsafe runtime archive path %q", name)
		}
		if _, ok := entries[name]; ok {
			return nil, fmt.Errorf("duplicate runtime archive path %q", name)
		}
		if header.Mode&06000 != 0 {
			return nil, fmt.Errorf("privileged mode in runtime archive: %s", name)
		}
		if header.Size < 0 || header.Size > maxBytes-total {
			return nil, errors.New("runtime archive size limit exceeded")
		}
		total += header.Size
		if len(entries) >= 128 {
			return nil, errors.New("too many runtime archive entries")
		}
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeSymlink {
			return nil, fmt.Errorf("unsupported runtime archive type: %s", name)
		}
		data, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != header.Size {
			return nil, errors.New("truncated runtime archive")
		}
		entries[name] = runtimeTarEntry{*header, data}
	}
	return entries, nil
}

func runtimePackageContents(ipkPath, arch, version string) (map[string]runtimeTarEntry, error) {
	info, err := os.Stat(ipkPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxRuntimeIPKBytes {
		return nil, errors.New("runtime IPK too large")
	}
	raw, err := os.ReadFile(ipkPath)
	if err != nil {
		return nil, err
	}
	outer, err := readRuntimeTar(raw, 2*maxRuntimeIPKBytes)
	if err != nil {
		return nil, err
	}
	for name, entry := range outer {
		if name != "debian-binary" && name != "control.tar.gz" && name != "data.tar.gz" {
			return nil, fmt.Errorf("unexpected IPK member %s", name)
		}
		if entry.header.Typeflag != tar.TypeReg && entry.header.Typeflag != tar.TypeRegA {
			return nil, errors.New("IPK members must be regular files")
		}
	}
	if string(outer["debian-binary"].data) != "2.0\n" {
		return nil, errors.New("invalid debian-binary")
	}
	control, err := readRuntimeTar(outer["control.tar.gz"].data, 1<<20)
	if err != nil {
		return nil, err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(control["control"].data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			if _, duplicate := fields[key]; duplicate {
				return nil, fmt.Errorf("duplicate control field %s", key)
			}
			fields[key] = strings.TrimSpace(value)
		}
	}
	if fields["Package"] != RuntimePackageName || fields["Architecture"] != arch || fields["Version"] != version {
		return nil, errors.New("runtime package identity/version/architecture mismatch")
	}
	// Package relationships can remove or take ownership from unrelated packages.
	// A regular runtime artifact must never implicitly perform a legacy migration.
	for _, name := range []string{"Replaces", "Conflicts", "Provides"} {
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("unsupported runtime package relationship %s", name)
		}
	}
	if len(bytes.TrimSpace(control["conffiles"].data)) != 0 {
		return nil, errors.New("runtime package must not own user configuration")
	}
	for name, entry := range control {
		if name != "control" && name != "conffiles" && name != "postinst" && name != "prerm" {
			return nil, fmt.Errorf("unexpected package control member %s", name)
		}
		if entry.header.Typeflag != tar.TypeReg && entry.header.Typeflag != tar.TypeRegA {
			return nil, errors.New("package control members must be regular files")
		}
	}
	if !bytes.Equal(control["postinst"].data, []byte(runtimePostinst)) || !bytes.Equal(control["prerm"].data, []byte(runtimePrerm)) {
		return nil, errors.New("runtime lifecycle scripts do not match supported package contract")
	}
	payload, err := readRuntimeTar(outer["data.tar.gz"].data, 64<<20)
	if err != nil {
		return nil, err
	}
	for name, entry := range payload {
		if entry.header.Typeflag == tar.TypeDir {
			allowed := false
			for p := range runtimePayload {
				if strings.HasPrefix(p, name+"/") {
					allowed = true
					break
				}
			}
			if !allowed {
				return nil, fmt.Errorf("unexpected runtime directory %s", name)
			}
			continue
		}
		if !runtimePayload[name] {
			return nil, fmt.Errorf("unexpected runtime payload %s", name)
		}
		if expectedTarget, mustBeSymlink := runtimeExpectedSymlinks[name]; mustBeSymlink {
			if entry.header.Typeflag != tar.TypeSymlink || entry.header.Linkname != expectedTarget {
				return nil, fmt.Errorf("expected runtime symlink %s -> %s", name, expectedTarget)
			}
		} else if entry.header.Typeflag == tar.TypeSymlink {
			return nil, fmt.Errorf("unexpected runtime symlink %s", name)
		}
	}
	for name := range runtimePayload {
		if _, ok := payload[name]; !ok {
			return nil, fmt.Errorf("missing runtime payload %s", name)
		}
	}
	if !strings.Contains(string(payload["opt/etc/init.d/S99firc"].data), ".awgm-disabled") {
		return nil, errors.New("runtime init does not support persistent disable")
	}
	return payload, nil
}

// Extraction is into a fresh private directory only; links are created last so
// an archive cannot redirect a later write outside the staging directory.
func stageRuntimePayload(entries map[string]runtimeTarEntry, root string) error {
	for name, entry := range entries {
		if entry.header.Typeflag == tar.TypeDir || entry.header.Typeflag == tar.TypeSymlink {
			continue
		}
		dest := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(dest, entry.data, os.FileMode(entry.header.Mode)&0777); err != nil {
			return err
		}
	}
	for name, entry := range entries {
		if entry.header.Typeflag == tar.TypeSymlink {
			dest := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return err
			}
			_ = os.Remove(dest)
			if err := os.Symlink(entry.header.Linkname, dest); err != nil {
				return err
			}
		}
	}
	// Mirror postinst only inside private verification/test roots. User configs
	// are not package payload, so opkg remove cannot remove them.
	for _, name := range []string{"firc.conf", "groups.yaml"} {
		dest := filepath.Join(root, "opt/etc/firc", name)
		if _, err := os.Lstat(dest); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(dest, entries["opt/share/awg-firc-runtime/defaults/"+name].data, 0600); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func VerifyRuntimeIPK(ipkPath, arch, version string) error {
	payload, err := runtimePackageContents(ipkPath, arch, version)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp(filepath.Dir(ipkPath), ".firc-runtime-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := stageRuntimePayload(payload, root); err != nil {
		return err
	}
	layout := DefaultPackageLayout(root)
	if err := VerifyTargetPackageLayout(layout, arch); err != nil {
		return err
	}
	if !presentRegular(layout.ChecksumManifest) {
		return errors.New("runtime checksum manifest missing")
	}
	return VerifyChecksumManifest(layout.ChecksumManifest, filepath.Dir(layout.FircdBin))
}

// opkg may report success even after an extraction warning. Verify every
// installed package-owned file against the already authenticated artifact,
// rather than trusting the installed checksum manifest to verify itself.
func verifyInstalledRuntime(ipkPath, arch, version string, layout PackageLayout) error {
	entries, err := runtimePackageContents(ipkPath, arch, version)
	if err != nil {
		return err
	}
	for name := range runtimePayload {
		entry := entries[name]
		dest := filepath.Join(layout.RootDir, filepath.FromSlash(name))
		if layout.RootDir == "" {
			dest = "/" + filepath.ToSlash(dest)
		}
		st, err := os.Lstat(dest)
		if err != nil {
			return fmt.Errorf("installed runtime %s: %w", name, err)
		}
		if entry.header.Typeflag == tar.TypeSymlink {
			target, err := os.Readlink(dest)
			if err != nil || filepath.ToSlash(target) != entry.header.Linkname {
				return fmt.Errorf("installed runtime symlink mismatch: %s", name)
			}
			continue
		}
		if !st.Mode().IsRegular() || st.Size() != int64(len(entry.data)) {
			return fmt.Errorf("installed runtime type/size mismatch: %s", name)
		}
		if runtime.GOOS != "windows" && st.Mode().Perm() != os.FileMode(entry.header.Mode)&0777 {
			return fmt.Errorf("installed runtime permissions mismatch: %s", name)
		}
		data, err := os.ReadFile(dest)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, entry.data) {
			return fmt.Errorf("installed runtime content mismatch: %s", name)
		}
	}
	return VerifyTargetPackageLayout(layout, arch)
}

func presentRegular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

const runtimePostinst = `#!/bin/sh
set -e
# Installation never selects/starts FIRC or changes the user's routing engine.
mkdir -p /opt/var/lib/firc/tunnels
mkdir -p /opt/etc/firc
chmod 700 /opt/etc/firc
for name in firc.conf groups.yaml; do
  if [ ! -e "/opt/etc/firc/$name" ] && [ ! -L "/opt/etc/firc/$name" ]; then
    cp "/opt/share/awg-firc-runtime/defaults/$name" "/opt/etc/firc/$name"
    chmod 600 "/opt/etc/firc/$name"
  fi
done
exit 0
`
const runtimePrerm = `#!/bin/sh
set -e
# Manager checks dependencies first; refuse external removal of a live runtime.
if pidof fircd >/dev/null 2>&1 || pidof tunvless >/dev/null 2>&1; then
  echo "Stop FIRC and its tunnels before removing/upgrading runtime" >&2
  exit 1
fi
if [ "$1" = "remove" ]; then
  if [ -x /opt/sbin/fircd ]; then
    /opt/sbin/fircd --purge >/dev/null 2>&1 || true
  fi
  rm -rf /tmp/firc-init
fi
exit 0
`

// BuildRuntimeIPK emits a separate package: awg-manager never owns these files.
func BuildRuntimeIPK(sourceRoot, fircd, tunvless, arch, version, out string) error {
	if !validRuntimeVersion(version) {
		return errors.New("invalid runtime version")
	}
	root, err := os.MkdirTemp("", "firc-runtime-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if _, err := StageTargetPackageAssets(sourceRoot, fircd, tunvless, root, arch, nil); err != nil {
		return err
	}
	defaults := filepath.Join(root, "opt/share/awg-firc-runtime/defaults")
	if err := os.MkdirAll(defaults, 0755); err != nil {
		return err
	}
	for _, name := range []string{"firc.conf", "groups.yaml"} {
		if err := os.Rename(filepath.Join(root, "opt/etc/firc", name), filepath.Join(defaults, name)); err != nil {
			return err
		}
	}
	licenseSource := filepath.Join(sourceRoot, "c-core/firc/LICENSE")
	if src := os.Getenv("FIRC_SOURCE_ROOT"); src != "" {
		licenseSource = filepath.Join(src, "LICENSE")
	}
	license, err := os.ReadFile(licenseSource)
	if err != nil {
		return fmt.Errorf("runtime source license required: %w", err)
	}
	licenseTarget := filepath.Join(root, "opt/share/licenses/awg-firc-runtime/LICENSE")
	if err := os.MkdirAll(filepath.Dir(licenseTarget), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(licenseTarget, license, 0644); err != nil {
		return err
	}
	// User configuration is deliberately not purged on package removal.
	deps, err := runtimeDependencies(fircd, tunvless, arch)
	if err != nil {
		return err
	}
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nDepends: %s\nSection: net\nDescription: FIRC runtime managed by awg-manager\n", RuntimePackageName, version, arch, strings.Join(deps, ", "))
	ctrl, err := createTarGzFromEntries([]tarMemoryEntry{
		{name: "control", mode: 0644, data: []byte(control)},
		{name: "conffiles", mode: 0644, data: []byte{}},
		{name: "postinst", mode: 0755, data: []byte(runtimePostinst)},
		{name: "prerm", mode: 0755, data: []byte(runtimePrerm)},
	})
	if err != nil {
		return err
	}
	data, err := createTarGzFromDir(root)
	if err != nil {
		return err
	}
	pkg, err := createTarGzFromEntries([]tarMemoryEntry{{name: "debian-binary", mode: 0644, data: []byte("2.0\n")}, {name: "control.tar.gz", mode: 0644, data: ctrl}, {name: "data.tar.gz", mode: 0644, data: data}})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(out, pkg, 0644); err != nil {
		return err
	}
	return VerifyRuntimeIPK(out, arch, version)
}

func runtimeDependencies(binaries ...string) ([]string, error) {
	// Last argument is the target architecture, all preceding arguments are ELFs.
	arch := binaries[len(binaries)-1]
	deps := map[string]bool{"iptables": true, "ipset": true, "ip-full": true, "conntrack": true, "socat": true, "ca-bundle": true}
	for _, binary := range binaries[:len(binaries)-1] {
		report, err := VerifyELFTargetLinkage(binary, arch)
		if err != nil {
			return nil, err
		}
		loader := "/opt/lib/ld.so.1"
		if strings.HasPrefix(arch, "aarch64") {
			loader = "/opt/lib/ld-linux-aarch64.so.1"
		}
		if report.Interpreter != "" && report.Interpreter != loader {
			return nil, fmt.Errorf("non-Entware dynamic loader %q for %s", report.Interpreter, arch)
		}
		for _, lib := range report.NeededLibs {
			name := ""
			switch {
			case strings.HasPrefix(lib, "libyaml-"):
				name = "libyaml"
			case strings.HasPrefix(lib, "libpcre2-"):
				name = "libpcre2"
			case strings.HasPrefix(lib, "libmnl."):
				name = "libmnl"
			case strings.HasPrefix(lib, "libcjson."):
				name = "cJSON"
			case strings.HasPrefix(lib, "libcurl."):
				name = "libcurl"
			case strings.HasPrefix(lib, "libz."):
				name = "zlib"
			case strings.HasPrefix(lib, "libatomic."):
				name = "libatomic"
			case strings.HasPrefix(lib, "libssl."), strings.HasPrefix(lib, "libcrypto."):
				name = "libopenssl"
			case strings.HasPrefix(lib, "libgcc_s."):
				name = "libgcc"
			case strings.HasPrefix(lib, "libc."), strings.HasPrefix(lib, "libm."), strings.HasPrefix(lib, "libpthread."), strings.HasPrefix(lib, "libdl."), strings.HasPrefix(lib, "librt."):
			default:
				return nil, fmt.Errorf("no Entware dependency mapping for %s", lib)
			}
			if name != "" {
				deps[name] = true
			}
		}
	}
	result := make([]string, 0, len(deps))
	for name := range deps {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}
