package firc

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockFircDownloader struct {
	blobs map[string][]byte
	errOn string
}

func (m *mockFircDownloader) DownloadFile(_ context.Context, r DownloadFileRequest) (DownloadFileResult, error) {
	if m.errOn != "" {
		return DownloadFileResult{}, errors.New("network failure")
	}
	b, ok := m.blobs[r.URL]
	if !ok {
		return DownloadFileResult{}, errors.New("not found")
	}
	if int64(len(b)) > r.MaxFileBytes {
		return DownloadFileResult{}, errors.New("download limit")
	}
	if err := os.WriteFile(r.DestPath, b, r.Mode); err != nil {
		return DownloadFileResult{}, err
	}
	return DownloadFileResult{Path: r.DestPath, Size: int64(len(b))}, nil
}
func sha256HexBytes(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

type mockRuntimePackages struct {
	mu                      sync.Mutex
	root, version           string
	versions                map[string]string
	installs, removes       int
	extRemoves              int
	failInstall, failRemove bool
	failExtRemove           bool
	failNextInstall         bool
	entered, release        chan struct{}
}

func (m *mockRuntimePackages) Version(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.versions != nil {
		return m.versions[name], nil
	}
	if name == RuntimePackageName {
		return m.version, nil
	}
	return "", nil
}
func (m *mockRuntimePackages) Install(ctx context.Context, p string) error {
	m.mu.Lock()
	m.installs++
	fail := m.failInstall
	if m.failNextInstall {
		fail = true
		m.failNextInstall = false
	}
	m.mu.Unlock()
	if m.entered != nil {
		close(m.entered)
		select {
		case <-m.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		return errors.New("opkg install failed")
	}
	entries, err := runtimePackageContents(p, "aarch64-3.10", "0.7.0-1")
	if err != nil {
		return err
	}
	preserved := map[string][]byte{}
	for _, rel := range []string{"opt/etc/firc/firc.conf", "opt/etc/firc/groups.yaml"} {
		if b, err := os.ReadFile(filepath.Join(m.root, rel)); err == nil {
			preserved[rel] = b
		}
	}
	// Simulate opkg replacing existing package-owned files.
	_ = os.Remove(filepath.Join(m.root, "opt/etc/ndm/iflayerchanged.d/100-firc"))
	if err := stageRuntimePayload(entries, m.root); err != nil {
		return err
	}
	for rel, b := range preserved {
		if err := os.WriteFile(filepath.Join(m.root, rel), b, 0600); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.version = "0.7.0-1"
	if m.versions != nil {
		m.versions[RuntimePackageName] = "0.7.0-1"
	}
	m.mu.Unlock()
	return nil
}
func (m *mockRuntimePackages) Remove(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removes++
	if m.failRemove {
		return errors.New("opkg remove failed")
	}
	if name != RuntimePackageName {
		return errors.New("wrong package")
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
	if m.versions != nil {
		m.versions[RuntimePackageName] = ""
	}
	return nil
}

func (m *mockRuntimePackages) RemoveExternal(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.extRemoves++
	if m.failExtRemove {
		return errors.New("opkg remove external failed")
	}
	if !isAllowedExternalFircPackage(name) {
		return errors.New("unsupported external package")
	}
	listPath := filepath.Join(m.root, "opt", "lib", "opkg", "info", name+".list")
	if _, err := os.Stat(listPath); err != nil {
		return err
	}
	if err := os.Remove(listPath); err != nil {
		return err
	}
	// Remove runtime binaries/hooks and simulate upstream firc prerm wiping /opt/var/lib/firc state.
	for _, rel := range []string{
		"opt/bin/fircd",
		"opt/bin/tunvless",
		"opt/sbin/fircd",
		"opt/sbin/tunvless",
		"opt/etc/init.d/S99firc",
		"opt/etc/ndm/netfilter.d/100-firc",
		"opt/etc/ndm/iflayerchanged.d/100-firc",
		"opt/var/lib/firc/hwid",
		"opt/var/lib/firc/rci-token",
		"opt/var/lib/firc/opkgtun",
	} {
		_ = os.Remove(filepath.Join(m.root, filepath.FromSlash(rel)))
	}
	_ = os.RemoveAll(filepath.Join(m.root, "opt", "var", "lib", "firc", "tunnels"))
	if m.versions != nil {
		m.versions[name] = ""
	}
	return nil
}

func runtimeFixture(t *testing.T) (*OnDemandInstaller, *mockRuntimePackages, []byte) {
	t.Helper()
	source := t.TempDir()
	root := t.TempDir()
	t.Setenv("FIRC_SOURCE_ROOT", source)
	files := map[string]string{
		"LICENSE":                             "GPL-3.0-or-later test fixture\n",
		"files/entware_kn/etc/init.d/S99firc": "#!/bin/sh\n[ -e /opt/etc/firc/.awgm-disabled ] && exit 8\nexit 0\n",
		"files/entware_kn/etc/ndm/netfilter.d/100-firc": "#!/bin/sh\nexit 0\n",
		"files/common/etc/firc/firc.conf":               "configVersion: 0.7.0\n",
		"files/common/etc/firc/groups.yaml":             "groups: []\n",
	}
	for rel, s := range files {
		p := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0755); err != nil {
			t.Fatal(err)
		}
	}
	fircd := filepath.Join(source, "fircd")
	tun := filepath.Join(source, "tunvless")
	for p, tag := range map[string]string{fircd: "fircd", tun: "tunvless"} {
		if err := os.WriteFile(p, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64, tag), 0755); err != nil {
			t.Fatal(err)
		}
	}
	ipk := filepath.Join(source, "runtime.ipk")
	if err := BuildRuntimeIPK(source, fircd, tun, "aarch64-3.10", "0.7.0-1", ipk); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ipk)
	if err != nil {
		t.Fatal(err)
	}
	spec := FircBinarySpec{Version: "0.7.0-1", PackageURL: "https://example.invalid/runtime.ipk", PackageSHA256: sha256HexBytes(b), PackageSize: int64(len(b))}
	inst := NewOnDemandInstaller("aarch64-3.10", spec)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	pm := &mockRuntimePackages{root: root}
	inst.SetPackageManager(pm)
	inst.SetDownloader(&mockFircDownloader{blobs: map[string][]byte{spec.PackageURL: b}})
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	return inst, pm, b
}

func TestOnDemandInstaller_PackagedLifecycle(t *testing.T) {
	i, pm, _ := runtimeFixture(t)
	ctx := context.Background()
	if err := i.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if st := i.GetInstallStatus(ctx, nil, false); st.State != InstallStateInstalled || st.InstalledVersion != "0.7.0-1" {
		t.Fatalf("status: %+v", st)
	}
	config := []byte("user configuration\n")
	if err := os.WriteFile(i.layout.ConfigFile, config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := i.Uninstall(ctx, nil, false, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(i.layout.ConfigFile); string(b) != string(config) {
		t.Fatal("config lost")
	}
	if err := i.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(i.layout.ConfigFile); string(b) != string(config) {
		t.Fatal("reinstall overwrote config")
	}
	if pm.installs != 2 || pm.removes != 1 {
		t.Fatal("wrong package operations")
	}
}

func TestOnDemandInstaller_VerificationPrecedesOpkg(t *testing.T) {
	i, pm, _ := runtimeFixture(t)
	i.spec.PackageSHA256 = strings.Repeat("0", 64)
	if err := i.Install(context.Background()); !errors.Is(err, ErrSHA256Mismatch) {
		t.Fatalf("checksum: %v", err)
	}
	if pm.installs != 0 {
		t.Fatal("unverified artifact installed")
	}
	i.spec.PackageURL = "http://example.invalid/runtime.ipk"
	if err := i.Install(context.Background()); !errors.Is(err, ErrReleaseUnavailable) {
		t.Fatalf("URL: %v", err)
	}
}

func TestOnDemandInstaller_InterruptedOperationRequiresRecovery(t *testing.T) {
	i, pm, _ := runtimeFixture(t)
	pm.failInstall = true
	if err := i.Install(context.Background()); err == nil {
		t.Fatal("expected package failure")
	}
	fresh := NewOnDemandInstaller(i.arch, i.spec)
	fresh.SetPackageLayout(i.layout)
	fresh.SetPackageManager(pm)
	if st := fresh.GetInstallStatus(context.Background(), nil, false); st.State != InstallStateRecoveryNeeded {
		t.Fatalf("lost recovery journal: %+v", st)
	}
	pm.failInstall = false
	if err := i.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := i.GetInstallStatus(context.Background(), nil, false); st.State != InstallStateInstalled {
		t.Fatalf("repair failed: %+v", st)
	}
}

func TestOnDemandInstaller_AsyncReservation(t *testing.T) {
	i, pm, _ := runtimeFixture(t)
	pm.entered = make(chan struct{})
	pm.release = make(chan struct{})
	if err := i.StartInstall(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pm.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not start")
	}
	if err := i.StartInstall(); !errors.Is(err, ErrInstallBusy) {
		close(pm.release)
		t.Fatalf("second job accepted: %v", err)
	}
	close(pm.release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !i.GetInstallStatus(context.Background(), nil, false).Busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job remained busy")
}

func TestOnDemandInstaller_ActiveAndDependenciesRefuseMutation(t *testing.T) {
	i, pm, _ := runtimeFixture(t)
	ctx := context.Background()
	if err := i.Install(ctx); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	i.SetEngineActiveChecker(func() bool { return true })
	if err := i.Install(ctx); !errors.Is(err, ErrFircActive) {
		t.Fatalf("active upgrade/install while healthy: %v", err)
	}
	if err := i.Uninstall(ctx, nil, false, false); !errors.Is(err, ErrFircActive) {
		t.Fatalf("active uninstall: %v", err)
	}
	i.SetEngineActiveChecker(func() bool { return false })
	i.SetTunvlessRunningChecker(func(context.Context) bool { return true })
	if err := i.Uninstall(ctx, nil, false, false); !errors.Is(err, ErrTunvlessRunning) {
		t.Fatalf("running tunnel: %v", err)
	}
	if pm.installs != 1 || pm.removes != 0 {
		t.Fatalf("unexpected package mutation: installs=%d removes=%d", pm.installs, pm.removes)
	}
}

func TestOnDemandInstaller_ActiveEngineAllowsInstallAndRepairWhenStoppedAndBroken(t *testing.T) {
	ctx := context.Background()

	// 1. Active engine + stopped + not_installed -> Install succeeds.
	instMissing, pmMissing, _ := runtimeFixture(t)
	instMissing.SetEngineActiveChecker(func() bool { return true })
	instMissing.SetFircRunningChecker(func(context.Context) bool { return false })
	instMissing.SetTunvlessRunningChecker(func(context.Context) bool { return false })
	if err := instMissing.Install(ctx); err != nil {
		t.Fatalf("active+stopped+missing Install failed: %v", err)
	}
	if pmMissing.installs != 1 {
		t.Fatalf("expected 1 install for missing active engine, got %d", pmMissing.installs)
	}

	// 2. Active engine + stopped + recovery_required (missing binary + dependents) -> Repair succeeds; running or uninstall rejected.
	instBroken, pmBroken, _ := runtimeFixture(t)
	if err := instBroken.Install(ctx); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if err := os.Remove(instBroken.layout.FircdBin); err != nil {
		t.Fatalf("remove fircd to simulate broken runtime: %v", err)
	}
	var fircRunning, tunRunning bool
	instBroken.SetEngineActiveChecker(func() bool { return true })
	instBroken.SetFircRunningChecker(func(context.Context) bool { return fircRunning })
	instBroken.SetTunvlessRunningChecker(func(context.Context) bool { return tunRunning })
	instBroken.SetDependencyChecker(func(context.Context) ([]string, error) {
		return []string{"awg_server"}, nil
	})

	fircRunning = true
	if err := instBroken.Install(ctx); !errors.Is(err, ErrFircRunning) {
		t.Fatalf("expected ErrFircRunning while daemon running on broken install, got: %v", err)
	}
	fircRunning = false
	tunRunning = true
	if err := instBroken.Install(ctx); !errors.Is(err, ErrTunvlessRunning) {
		t.Fatalf("expected ErrTunvlessRunning while tunvless running on broken install, got: %v", err)
	}
	tunRunning = false

	if err := instBroken.Uninstall(ctx, nil, false, false); !errors.Is(err, ErrFircActive) {
		t.Fatalf("expected ErrFircActive on active+broken Uninstall, got: %v", err)
	}
	if err := instBroken.Install(ctx); err != nil {
		t.Fatalf("active+stopped+broken repair Install failed: %v", err)
	}
	if st := instBroken.GetInstallStatus(ctx, nil, true); st.State != InstallStateInstalled {
		t.Fatalf("expected installed status after repair, got %+v", st)
	}
	if pmBroken.installs != 2 {
		t.Fatalf("expected 2 installs (initial + repair), got %d", pmBroken.installs)
	}
}

func TestOnDemandInstaller_OrphanedLeftoversRepairAndUninstall(t *testing.T) {
	ctx := context.Background()

	// 1. Partial leftovers (S99firc + regular /opt/bin/fircd, no .list, existing firc.conf) -> Install repairs cleanly.
	instRepair, pmRepair, _ := runtimeFixture(t)
	rootRepair := instRepair.layout.RootDir
	for rel, content := range map[string]string{
		"opt/bin/fircd":          "stale-fircd-binary",
		"opt/etc/init.d/S99firc": "#!/bin/sh\nexit 0\n",
		"opt/etc/firc/firc.conf": "user-conf\n",
	} {
		p := filepath.Join(rootRepair, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := instRepair.Install(ctx); err != nil {
		t.Fatalf("repair over partial orphaned leftovers failed: %v", err)
	}
	if pmRepair.installs != 1 {
		t.Fatalf("expected 1 package install, got %d", pmRepair.installs)
	}
	if target, err := os.Readlink(filepath.Join(rootRepair, "opt/bin/fircd")); err != nil || filepath.ToSlash(target) != "../sbin/fircd" {
		t.Fatalf("expected /opt/bin/fircd symlink to ../sbin/fircd, got %q (err=%v)", target, err)
	}
	if cfg, err := os.ReadFile(instRepair.layout.ConfigFile); err != nil || string(cfg) != "user-conf\n" {
		t.Fatalf("expected firc.conf preserved after repair, got %q (err=%v)", string(cfg), err)
	}

	// 2. Partial leftovers with no awg-firc-runtime in opkg -> Uninstall cleans leftovers without opkg remove.
	instClean, pmClean, _ := runtimeFixture(t)
	rootClean := instClean.layout.RootDir
	for rel, content := range map[string]string{
		"opt/bin/fircd":                    "stale-fircd-binary",
		"opt/etc/init.d/S99firc":           "#!/bin/sh\nexit 0\n",
		"opt/etc/ndm/netfilter.d/100-firc": "#!/bin/sh\nexit 0\n",
		"opt/etc/firc/firc.conf":           "keep-me\n",
	} {
		p := filepath.Join(rootClean, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := instClean.Uninstall(ctx, nil, false, false); err != nil {
		t.Fatalf("uninstall of orphaned leftovers failed: %v", err)
	}
	if pmClean.removes != 0 {
		t.Fatalf("expected 0 opkg remove calls when awg-firc-runtime absent, got %d", pmClean.removes)
	}
	for _, rel := range []string{"opt/bin/fircd", "opt/etc/init.d/S99firc", "opt/etc/ndm/netfilter.d/100-firc"} {
		if _, err := os.Lstat(filepath.Join(rootClean, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", rel, err)
		}
	}
	if cfg, err := os.ReadFile(instClean.layout.ConfigFile); err != nil || string(cfg) != "keep-me\n" {
		t.Fatalf("expected firc.conf preserved on orphan uninstall, got %q (err=%v)", string(cfg), err)
	}
	if st := instClean.GetInstallStatus(ctx, nil, false); st.State != InstallStateNotInstalled {
		t.Fatalf("expected State=%q after orphan uninstall, got %+v", InstallStateNotInstalled, st)
	}

	// 3. Complete unmanaged installation (all 5 runtime files present, no .list) -> still refuses direct overwrite.
	instUnmanaged, pmUnmanaged, _ := runtimeFixture(t)
	rootUnmanaged := instUnmanaged.layout.RootDir
	for rel, content := range map[string]string{
		"opt/bin/fircd":                         "unmanaged-fircd",
		"opt/bin/tunvless":                      "unmanaged-tunvless",
		"opt/etc/init.d/S99firc":                "#!/bin/sh\nexit 0\n",
		"opt/etc/ndm/netfilter.d/100-firc":      "#!/bin/sh\nexit 0\n",
		"opt/etc/ndm/iflayerchanged.d/100-firc": "#!/bin/sh\nexit 0\n",
	} {
		p := filepath.Join(rootUnmanaged, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := instUnmanaged.Install(ctx); err == nil || !strings.Contains(err.Error(), "unmanaged FIRC runtime found") {
		t.Fatalf("expected refusal for complete unmanaged runtime, got: %v", err)
	}
	if pmUnmanaged.installs != 0 {
		t.Fatalf("expected 0 opkg installs for complete unmanaged runtime, got %d", pmUnmanaged.installs)
	}
}

func TestRuntimeUpgradeFailureRestoresPreviousPackage(t *testing.T) {
	i, pm, _ := runtimeFixture(t)
	if err := i.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	pm.failNextInstall = true
	if err := i.Install(context.Background()); err == nil {
		t.Fatal("expected upgrade failure")
	}
	if st := i.GetInstallStatus(context.Background(), nil, false); st.State != InstallStateInstalled || st.InstalledVersion != "0.7.0-1" {
		t.Fatalf("previous runtime not restored: %+v", st)
	}
	if pm.installs != 3 {
		t.Fatalf("expected initial install, failed upgrade and rollback; got %d", pm.installs)
	}
}

func seedExternalFircTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"opt/bin/fircd":                         "elf-fircd",
		"opt/bin/tunvless":                      "elf-tunvless",
		"opt/etc/init.d/S99firc":                "#!/bin/sh\nexit 0\n",
		"opt/etc/ndm/netfilter.d/100-firc":      "#!/bin/sh\nexit 0\n",
		"opt/etc/ndm/iflayerchanged.d/100-firc": "#!/bin/sh\nexit 0\n",
		"opt/etc/firc/firc.conf":                "configVersion: 0.7.0\n",
		"opt/etc/firc/groups.yaml":              "groups: []\n",
		"opt/lib/opkg/info/firc.list": "/opt/bin/fircd\n" +
			"/opt/bin/tunvless\n" +
			"/opt/etc/init.d/S99firc\n" +
			"/opt/etc/ndm/netfilter.d/100-firc\n" +
			"/opt/etc/ndm/iflayerchanged.d/100-firc\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0644)
		if strings.Contains(rel, "/bin/") || strings.Contains(rel, "/init.d/") || strings.Contains(rel, "/ndm/") {
			mode = 0755
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOnDemandInstaller_ExternalFircPackageDetectedAndProtected(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	seedExternalFircTree(t, root)

	spec := FircBinarySpec{
		Version:       "0.8.0-awgm.4",
		PackageURL:    "https://example.invalid/awg-firc-runtime.ipk",
		PackageSHA256: strings.Repeat("b", 64),
		PackageSize:   1024,
	}
	inst := NewOnDemandInstaller("mipsel-3.4", spec)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	pm := &mockRuntimePackages{
		root:     root,
		versions: map[string]string{"firc": "0.8.2-1"},
	}
	inst.SetPackageManager(pm)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 128 << 20, true })

	st := inst.GetInstallStatus(ctx, nil, false)
	if st.State != InstallStateInstalled {
		t.Fatalf("expected state=%q for external firc package, got %+v", InstallStateInstalled, st)
	}
	if st.ExternalPackage != "firc" || st.InstalledVersion != "0.8.2-1" {
		t.Fatalf("expected externalPackage=firc version=0.8.2-1, got %+v", st)
	}
	if !st.FircdPresent || !st.TunvlessPresent || !st.InitScriptPresent || !st.NDMHookPresent || !st.ConfigPresent {
		t.Fatalf("expected all runtime presence flags true, got %+v", st)
	}
	if st.FreeDiskBytes != 128<<20 {
		t.Fatalf("expected freeDisk fallback to probe /opt/bin, got %d", st.FreeDiskBytes)
	}

	inst.SetFircRunningChecker(func(context.Context) bool { return true })
	runningSt := inst.GetInstallStatus(ctx, nil, true)
	if runningSt.State != InstallStateRunning || runningSt.ExternalPackage != "firc" {
		t.Fatalf("expected running external package status, got %+v", runningSt)
	}

	inst.SetFircRunningChecker(func(context.Context) bool { return false })
	if err := inst.Install(ctx); err == nil || !strings.Contains(err.Error(), "external package firc.list") {
		t.Fatalf("expected Install refusal for external package firc, got %v", err)
	}
	if err := inst.Uninstall(ctx, nil, false, false); err == nil || !strings.Contains(err.Error(), "external package firc.list") {
		t.Fatalf("expected Uninstall refusal for external package firc, got %v", err)
	}
	if pm.installs != 0 || pm.removes != 0 {
		t.Fatalf("unexpected opkg mutations for external package: installs=%d removes=%d", pm.installs, pm.removes)
	}
}

func TestOpkgRuntimeManager_ReadsExternalFircVersion(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "status")
	statusBody := strings.Join([]string{
		"Package: busybox",
		"Version: 1.36.1-1",
		"Status: install ok installed",
		"",
		"Package: firc",
		"Version: 0.8.2-1",
		"Depends: libc, libssp",
		"Status: install user installed",
		"Architecture: mipsel-3.4",
		"",
	}, "\n")
	if err := os.WriteFile(statusPath, []byte(statusBody), 0644); err != nil {
		t.Fatal(err)
	}

	pm := OpkgRuntimeManager{StatusPath: statusPath}
	ver, err := pm.Version(context.Background(), "firc")
	if err != nil {
		t.Fatalf("Version(firc): %v", err)
	}
	if ver != "0.8.2-1" {
		t.Fatalf("Version(firc) = %q, want %q", ver, "0.8.2-1")
	}
	awgVer, err := pm.Version(context.Background(), RuntimePackageName)
	if err != nil {
		t.Fatalf("Version(%s): %v", RuntimePackageName, err)
	}
	if awgVer != "" {
		t.Fatalf("expected empty version for absent %s, got %q", RuntimePackageName, awgVer)
	}
}
