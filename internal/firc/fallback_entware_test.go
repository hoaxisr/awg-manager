package firc

import (
	"context"
	"debug/elf"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type mockFallbackRuntimePkgMgr struct {
	mu       sync.Mutex
	root     string
	arch     string
	expected string
	version  string
	installs int
	removes  int
}

func (m *mockFallbackRuntimePkgMgr) Version(context.Context, string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version, nil
}

func (m *mockFallbackRuntimePkgMgr) Install(_ context.Context, ipkPath string) error {
	m.mu.Lock()
	m.installs++
	m.mu.Unlock()
	entries, err := runtimePackageContents(ipkPath, m.arch, m.expected)
	if err != nil {
		return err
	}
	preserved := map[string][]byte{}
	for _, rel := range []string{"opt/etc/firc/firc.conf", "opt/etc/firc/groups.yaml", "opt/etc/firc/tunnels.yaml"} {
		if b, err := os.ReadFile(filepath.Join(m.root, rel)); err == nil {
			preserved[rel] = b
		}
	}
	if err := stageRuntimePayload(entries, m.root); err != nil {
		return err
	}
	for rel, b := range preserved {
		if err := os.WriteFile(filepath.Join(m.root, rel), b, 0600); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.version = m.expected
	m.mu.Unlock()
	return nil
}

func (m *mockFallbackRuntimePkgMgr) Remove(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removes++
	if name != RuntimePackageName {
		return errors.New("unexpected package name")
	}
	for rel := range runtimePayload {
		if rel == "opt/etc/firc/firc.conf" || rel == "opt/etc/firc/groups.yaml" {
			continue
		}
		if err := os.Remove(filepath.Join(m.root, rel)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	m.version = ""
	return nil
}

func buildMockEntwareFircIPK(t *testing.T, arch, version string, class elf.Class, data elf.Data, machine elf.Machine) []byte {
	t.Helper()
	s99 := strings.Join([]string{
		"#!/bin/sh",
		"ENABLED=yes",
		"PROCS=fircd",
		"start() {",
		`  [ "$ENABLED" != "yes" ] && return 8`,
		"  echo starting",
		"}",
		`case "$1" in start) start ;; stop) exit 0 ;; esac`,
		"",
	}, "\n")
	upstreamPostinst := "#!/bin/sh\n/opt/etc/init.d/S99firc restart\nexit 0\n"
	dataTar, err := createTarGzFromEntries([]tarMemoryEntry{
		{name: "./opt/bin/fircd", mode: 0755, data: makeMinimalELF(class, data, machine, "fircd")},
		{name: "./opt/bin/tunvless", mode: 0755, data: makeMinimalELF(class, data, machine, "tunvless")},
		{name: "./opt/etc/firc/firc.conf", mode: 0600, data: []byte("configVersion: 0.7.1\n")},
		{name: "./opt/etc/firc/groups.yaml", mode: 0600, data: []byte("groups: []\n")},
		{name: "./opt/etc/init.d/S99firc", mode: 0755, data: []byte(s99)},
		{name: "./opt/etc/ndm/netfilter.d/100-firc", mode: 0755, data: []byte("#!/bin/sh\nexit 0\n")},
		{name: "./opt/etc/ndm/iflayerchanged.d/100-firc", mode: 0755, data: []byte("#!/bin/sh\nexit 0\n")},
		{name: "./opt/usr/share/firc/skins/default/index.html", mode: 0644, data: []byte("<html></html>")},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctrlTar, err := createTarGzFromEntries([]tarMemoryEntry{
		{name: "./control", mode: 0644, data: []byte("Package: firc\nVersion: " + version + "\nArchitecture: " + arch + "\n")},
		{name: "./postinst", mode: 0755, data: []byte(upstreamPostinst)},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := createTarGzFromEntries([]tarMemoryEntry{
		{name: "debian-binary", mode: 0644, data: []byte("2.0\n")},
		{name: "control.tar.gz", mode: 0644, data: ctrlTar},
		{name: "data.tar.gz", mode: 0644, data: dataTar},
	})
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestFallbackFircPackagesMetadata(t *testing.T) {
	for _, arch := range []string{"aarch64-3.10", "mipsel-3.4", "mips-3.4"} {
		spec, ok := FallbackFircPackages[arch]
		if !ok {
			t.Fatalf("FallbackFircPackages missing %s", arch)
		}
		if !spec.IsAvailable() {
			t.Fatalf("FallbackFircPackages[%s] invalid: %+v", arch, spec)
		}
		if !strings.HasPrefix(spec.PackageURL, "https://repo.hoaxisr.ru/") {
			t.Fatalf("FallbackFircPackages[%s] unexpected URL %q", arch, spec.PackageURL)
		}
		if !strings.HasPrefix(spec.MirrorURL, "https://github.com/hoaxisr/firc/releases/download/") {
			t.Fatalf("FallbackFircPackages[%s] unexpected MirrorURL %q", arch, spec.MirrorURL)
		}
	}
}

func TestOnDemandInstaller_FallbackEntwareConversionAndOfflineReinstall(t *testing.T) {
	ctx := context.Background()
	arch := "mipsel-3.4"
	version := "0.8.0-1"
	upstreamIPK := buildMockEntwareFircIPK(t, arch, version, elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS)
	spec := FircBinarySpec{
		Version:       version,
		PackageURL:    "https://repo.hoaxisr.ru/mipsel-k3.4/firc_0.8.0-1_entware_mipsel-3.4_kn.ipk",
		MirrorURL:     "https://github.com/hoaxisr/firc/releases/download/0.8.0/firc_0.8.0-1_entware_mipsel-3.4_kn.ipk",
		PackageSHA256: sha256HexBytes(upstreamIPK),
		PackageSize:   int64(len(upstreamIPK)),
	}

	// Default NewOnDemandInstaller without explicit fallback preserves unavailability guarantees.
	root := t.TempDir()
	pm := &mockFallbackRuntimePkgMgr{root: root, arch: arch, expected: version}
	unconfigured := NewOnDemandInstaller(arch, FircBinarySpec{})
	unconfigured.SetPackageLayout(DefaultPackageLayout(root))
	unconfigured.SetPackageManager(pm)
	unconfigured.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	if st := unconfigured.GetInstallStatus(ctx, nil, false); st.Available || st.State != InstallStateUnavailable {
		t.Fatalf("expected unavailable without fallback, got %+v", st)
	}
	if err := unconfigured.Install(ctx); !errors.Is(err, ErrReleaseUnavailable) {
		t.Fatalf("expected ErrReleaseUnavailable, got %v", err)
	}

	// Reject mismatched checksum before opkg mutation.
	badSpec := spec
	badSpec.PackageSHA256 = strings.Repeat("f", 64)
	badInst := NewOnDemandInstaller(arch, FircBinarySpec{})
	badInst.SetFallbackPackageSpec(badSpec)
	badInst.SetPackageLayout(DefaultPackageLayout(root))
	badInst.SetPackageManager(pm)
	badInst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	badInst.SetDownloader(&mockFircDownloader{blobs: map[string][]byte{spec.PackageURL: upstreamIPK}})
	if err := badInst.Install(ctx); !errors.Is(err, ErrSHA256Mismatch) {
		t.Fatalf("expected ErrSHA256Mismatch, got %v", err)
	}
	if pm.installs != 0 {
		t.Fatalf("opkg install ran despite upstream checksum mismatch (%d)", pm.installs)
	}

	// Valid fallback: installs awg-firc-runtime with patched S99firc, preserves user files, caches offline.
	userTunnels := []byte("tunnels:\n  - name: vless-1\n")
	userGroups := []byte("groups:\n  - name: custom\n")
	fircDir := filepath.Join(root, "opt/etc/firc")
	if err := os.MkdirAll(fircDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fircDir, "tunnels.yaml"), userTunnels, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fircDir, "groups.yaml"), userGroups, 0600); err != nil {
		t.Fatal(err)
	}

	inst := NewOnDemandInstaller(arch, FircBinarySpec{})
	inst.SetFallbackPackageSpec(spec)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	inst.SetPackageManager(pm)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	inst.SetDownloader(&mockFircDownloader{blobs: map[string][]byte{spec.PackageURL: upstreamIPK}})

	if st := inst.GetInstallStatus(ctx, nil, false); !st.Available || st.AvailableVersion != version || st.State != InstallStateNotInstalled {
		t.Fatalf("expected available not_installed status via fallback, got %+v", st)
	}
	if err := inst.Install(ctx); err != nil {
		t.Fatalf("fallback install failed: %v", err)
	}
	s99Bytes, err := os.ReadFile(filepath.Join(root, "opt/etc/init.d/S99firc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s99Bytes), ".awgm-disabled") {
		t.Fatalf("converted S99firc missing .awgm-disabled check:\n%s", string(s99Bytes))
	}
	if got, _ := os.ReadFile(filepath.Join(fircDir, "tunnels.yaml")); string(got) != string(userTunnels) {
		t.Fatalf("tunnels.yaml modified on install: %q", string(got))
	}
	if got, _ := os.ReadFile(filepath.Join(fircDir, "groups.yaml")); string(got) != string(userGroups) {
		t.Fatalf("groups.yaml modified on install: %q", string(got))
	}
	if _, err := os.Stat(filepath.Join(root, "opt/usr/share/firc/skins/default/index.html")); !os.IsNotExist(err) {
		t.Fatalf("expected stripped skin assets in awg-firc-runtime, stat err=%v", err)
	}

	// Uninstall then reinstall with downloader removed: must succeed offline from fallback-converted cache.
	if err := inst.Uninstall(ctx, nil, false, false); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}
	inst.SetDownloader(nil)
	if err := inst.Install(ctx); err != nil {
		t.Fatalf("offline fallback reinstall failed: %v", err)
	}
	if pm.installs != 2 || pm.removes != 1 {
		t.Fatalf("unexpected opkg counts: installs=%d removes=%d", pm.installs, pm.removes)
	}

	// Verify MirrorURL fallback works when primary PackageURL 404s.
	mirrorInst := NewOnDemandInstaller(arch, FircBinarySpec{})
	mirrorInst.SetFallbackPackageSpec(spec)
	mirrorRoot := t.TempDir()
	mirrorPM := &mockFallbackRuntimePkgMgr{root: mirrorRoot, arch: arch, expected: version}
	mirrorInst.SetPackageLayout(DefaultPackageLayout(mirrorRoot))
	mirrorInst.SetPackageManager(mirrorPM)
	mirrorInst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	mirrorInst.SetDownloader(&mockFircDownloader{blobs: map[string][]byte{spec.MirrorURL: upstreamIPK}})
	if err := mirrorInst.Install(ctx); err != nil {
		t.Fatalf("mirror fallback install failed: %v", err)
	}
	if mirrorPM.installs != 1 {
		t.Fatalf("expected 1 install via mirror fallback, got %d", mirrorPM.installs)
	}
}
