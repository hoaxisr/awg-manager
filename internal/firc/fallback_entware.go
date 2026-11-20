package firc

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FallbackFircPackages pins verified HTTPS Entware `firc` IPK packages from
// repo.hoaxisr.ru used ONLY when EmbeddedFircBinaries lacks an awg-firc-runtime
// GitHub release for the router architecture.
//
// Each downloaded IPK is verified by size and SHA-256, checked for target ELF
// architecture and dynamic linkage, patched for `.awgm-disabled` support, and
// repackaged locally into an `awg-firc-runtime` IPK with `runtimePostinst` so
// upstream `firc` postinst never auto-starts `fircd` over an active engine.
var FallbackFircPackages = map[string]FircBinarySpec{
	"aarch64-3.10": {
		Version:       "0.8.0-1",
		PackageURL:    "https://repo.hoaxisr.ru/aarch64-k3.10/firc_0.8.0-1_entware_aarch64-3.10_kn.ipk",
		MirrorURL:     "https://github.com/hoaxisr/firc/releases/download/0.8.0/firc_0.8.0-1_entware_aarch64-3.10_kn.ipk",
		PackageSHA256: "d16f6f3bee76a1cf3370c95f013b702d2a5598bdfab1271625c6be618b83d86d",
		PackageSize:   1982174,
	},
	"mipsel-3.4": {
		Version:       "0.8.0-1",
		PackageURL:    "https://repo.hoaxisr.ru/mipsel-k3.4/firc_0.8.0-1_entware_mipsel-3.4_kn.ipk",
		MirrorURL:     "https://github.com/hoaxisr/firc/releases/download/0.8.0/firc_0.8.0-1_entware_mipsel-3.4_kn.ipk",
		PackageSHA256: "b727da46e817b7dfe225016219195bd88fe6257f77169e58d4bf3f2e2398af68",
		PackageSize:   1980688,
	},
	"mips-3.4": {
		Version:       "0.8.0-1",
		PackageURL:    "https://repo.hoaxisr.ru/mips-k3.4/firc_0.8.0-1_entware_mips-3.4_kn.ipk",
		MirrorURL:     "https://github.com/hoaxisr/firc/releases/download/0.8.0/firc_0.8.0-1_entware_mips-3.4_kn.ipk",
		PackageSHA256: "7319027b6be1f19d7599cc09c131265e67f841e6973acdb9bdd649cda773c40c",
		PackageSize:   1983965,
	},
}

const fallbackFircLicenseNotice = `FIRC — DNS-based routing application for Entware
Copyright (C) hoaxisr (https://github.com/hoaxisr/firc)

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

See <https://www.gnu.org/licenses/gpl-3.0.txt> for the full license text.
`

func patchEntwareInitScriptForDisabledMarker(raw []byte) ([]byte, error) {
	if bytes.Contains(raw, []byte(".awgm-disabled")) {
		return raw, nil
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	guard := "  if [ -e /opt/etc/firc/.awgm-disabled ]; then\n" +
		"    echo -e \"$ansi_yellow $DESC is disabled by awg-manager (.awgm-disabled). $ansi_std\"\n" +
		"    logger \"$DESC: disabled by awg-manager (.awgm-disabled); refusing start.\"\n" +
		"    return 8\n" +
		"  fi\n"
	for _, anchor := range []string{
		"  [ \"$ENABLED\" != \"yes\" ] && return 8\n",
		"[ \"$ENABLED\" != \"yes\" ] && return 8\n",
		"start() {\n",
	} {
		if idx := strings.Index(text, anchor); idx >= 0 {
			pos := idx + len(anchor)
			return []byte(text[:pos] + guard + text[pos:]), nil
		}
	}
	return nil, errors.New("upstream S99firc lacks start() entry point for .awgm-disabled guard")
}

func requireRegularTarEntry(entries map[string]runtimeTarEntry, name string) ([]byte, error) {
	entry, ok := entries[name]
	if !ok {
		return nil, fmt.Errorf("upstream FIRC package missing %s", name)
	}
	if entry.header.Typeflag != tar.TypeReg && entry.header.Typeflag != tar.TypeRegA {
		return nil, fmt.Errorf("upstream FIRC entry %s is not a regular file", name)
	}
	if len(entry.data) == 0 {
		return nil, fmt.Errorf("upstream FIRC entry %s is empty", name)
	}
	return entry.data, nil
}

func writeExactModeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// BuildRuntimeIPKFromEntwareIPK converts a verified upstream Entware `firc` IPK
// into an `awg-firc-runtime` IPK matching `VerifyRuntimeIPK` and
// `runtimePackageContents` so FIRC installation never invokes upstream postinst
// auto-start and preserves user configs across upgrades/reinstalls.
func BuildRuntimeIPKFromEntwareIPK(upstreamIPKPath, arch, version, outIPKPath string) error {
	if !validRuntimeVersion(version) {
		return errors.New("invalid runtime version")
	}
	info, err := os.Stat(upstreamIPKPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("upstream FIRC IPK must be a non-empty regular file")
	}
	if info.Size() > maxRuntimeIPKBytes {
		return errors.New("upstream FIRC IPK too large")
	}
	raw, err := os.ReadFile(upstreamIPKPath)
	if err != nil {
		return err
	}
	outer, err := readRuntimeTar(raw, 2*maxRuntimeIPKBytes)
	if err != nil {
		return fmt.Errorf("parse upstream FIRC IPK container: %w", err)
	}
	dataTar, ok := outer["data.tar.gz"]
	if !ok {
		return errors.New("upstream FIRC IPK missing data.tar.gz")
	}
	payload, err := readRuntimeTar(dataTar.data, 64<<20)
	if err != nil {
		return fmt.Errorf("parse upstream FIRC payload: %w", err)
	}

	fircdBytes, err := requireRegularTarEntry(payload, "opt/bin/fircd")
	if err != nil {
		return err
	}
	tunvlessBytes, err := requireRegularTarEntry(payload, "opt/bin/tunvless")
	if err != nil {
		return err
	}
	initBytes, err := requireRegularTarEntry(payload, "opt/etc/init.d/S99firc")
	if err != nil {
		return err
	}
	netfilterBytes, err := requireRegularTarEntry(payload, "opt/etc/ndm/netfilter.d/100-firc")
	if err != nil {
		return err
	}
	fircConfBytes, err := requireRegularTarEntry(payload, "opt/etc/firc/firc.conf")
	if err != nil {
		return err
	}
	groupsYAMLBytes, err := requireRegularTarEntry(payload, "opt/etc/firc/groups.yaml")
	if err != nil {
		return err
	}
	patchedInit, err := patchEntwareInitScriptForDisabledMarker(initBytes)
	if err != nil {
		return err
	}

	root, err := os.MkdirTemp("", "firc-entware-repack-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	dirs := []struct {
		rel  string
		mode os.FileMode
	}{
		{"opt/sbin", 0755},
		{"opt/bin", 0755},
		{"opt/etc/init.d", 0755},
		{"opt/etc/ndm/netfilter.d", 0755},
		{"opt/etc/ndm/iflayerchanged.d", 0755},
		{"opt/etc/firc", 0700},
		{"opt/share/awg-firc-runtime/defaults", 0755},
		{"opt/share/licenses/awg-firc-runtime", 0755},
	}
	for _, d := range dirs {
		p := filepath.Join(root, filepath.FromSlash(d.rel))
		if err := os.MkdirAll(p, d.mode); err != nil {
			return err
		}
		if err := os.Chmod(p, d.mode); err != nil {
			return err
		}
	}

	fircdDest := filepath.Join(root, "opt/sbin/fircd")
	tunvlessDest := filepath.Join(root, "opt/sbin/tunvless")
	if err := writeExactModeFile(fircdDest, fircdBytes, 0755); err != nil {
		return err
	}
	if err := writeExactModeFile(tunvlessDest, tunvlessBytes, 0755); err != nil {
		return err
	}
	if err := VerifyELFArch(fircdDest, arch); err != nil {
		return fmt.Errorf("upstream fircd ELF verification: %w", err)
	}
	if err := VerifyELFArch(tunvlessDest, arch); err != nil {
		return fmt.Errorf("upstream tunvless ELF verification: %w", err)
	}
	deps, err := runtimeDependencies(fircdDest, tunvlessDest, arch)
	if err != nil {
		return err
	}

	for linkRel, target := range runtimeExpectedSymlinks {
		linkDest := filepath.Join(root, filepath.FromSlash(linkRel))
		if err := os.Symlink(target, linkDest); err != nil {
			return err
		}
	}

	files := []struct {
		rel  string
		data []byte
		mode os.FileMode
	}{
		{"opt/etc/init.d/S99firc", patchedInit, 0755},
		{"opt/etc/ndm/netfilter.d/100-firc", netfilterBytes, 0755},
		{"opt/share/awg-firc-runtime/defaults/firc.conf", fircConfBytes, 0644},
		{"opt/share/awg-firc-runtime/defaults/groups.yaml", groupsYAMLBytes, 0644},
		{"opt/share/licenses/awg-firc-runtime/LICENSE", []byte(fallbackFircLicenseNotice), 0644},
	}
	for _, f := range files {
		if err := writeExactModeFile(filepath.Join(root, filepath.FromSlash(f.rel)), f.data, f.mode); err != nil {
			return err
		}
	}
	shaPath := filepath.Join(root, "opt/etc/firc/SHA256SUMS")
	if err := WriteChecksumManifest(shaPath, []string{fircdDest, tunvlessDest}); err != nil {
		return err
	}
	if err := os.Chmod(shaPath, 0644); err != nil {
		return err
	}

	control := fmt.Sprintf(
		"Package: %s\nVersion: %s\nArchitecture: %s\nDepends: %s\nSection: net\nDescription: FIRC runtime managed by awg-manager\n",
		RuntimePackageName, version, arch, strings.Join(deps, ", "),
	)
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
	pkg, err := createTarGzFromEntries([]tarMemoryEntry{
		{name: "debian-binary", mode: 0644, data: []byte("2.0\n")},
		{name: "control.tar.gz", mode: 0644, data: ctrl},
		{name: "data.tar.gz", mode: 0644, data: data},
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outIPKPath), 0755); err != nil {
		return err
	}
	if err := writeExactModeFile(outIPKPath, pkg, 0644); err != nil {
		return err
	}
	return VerifyRuntimeIPK(outIPKPath, arch, version)
}
