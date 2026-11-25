package firc

import (
	"context"
	"debug/elf"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type upgradeMockPackages struct {
	mu              sync.Mutex
	root            string
	arch            string
	versions        map[string]string
	installs        int
	failInstall     bool
	failNextInstall bool
	onInstall       func(ver string)
}

func (m *upgradeMockPackages) Version(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.versions == nil {
		return "", nil
	}
	return m.versions[name], nil
}

func (m *upgradeMockPackages) Install(_ context.Context, p string) error {
	ver, err := readRuntimeIPKControlVersion(p)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.installs++
	fail := m.failInstall
	if m.failNextInstall {
		fail = true
		m.failNextInstall = false
	}
	onInstall := m.onInstall
	m.mu.Unlock()

	if onInstall != nil {
		onInstall(ver)
	}
	if fail {
		return errors.New("opkg install failed")
	}

	entries, err := runtimePackageContents(p, m.arch, ver)
	if err != nil {
		return err
	}
	preserved := map[string][]byte{}
	for _, rel := range []string{"opt/etc/firc/firc.conf", "opt/etc/firc/groups.yaml"} {
		if b, rErr := os.ReadFile(filepath.Join(m.root, rel)); rErr == nil {
			preserved[rel] = b
		}
	}
	_ = os.Remove(filepath.Join(m.root, "opt/etc/ndm/iflayerchanged.d/100-firc"))
	if err := stageRuntimePayload(entries, m.root); err != nil {
		return err
	}
	for rel, b := range preserved {
		if err := os.WriteFile(filepath.Join(m.root, rel), b, 0600); err != nil {
			return err
		}
	}
	if err := writeOwnRuntimeList(m.root); err != nil {
		return err
	}
	m.mu.Lock()
	if m.versions == nil {
		m.versions = map[string]string{}
	}
	m.versions[RuntimePackageName] = ver
	m.mu.Unlock()
	return nil
}

func (m *upgradeMockPackages) Remove(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	if err := removeOwnRuntimeList(m.root); err != nil {
		return err
	}
	if m.versions != nil {
		m.versions[RuntimePackageName] = ""
	}
	return nil
}

type orderedDownloader struct {
	blobs      map[string][]byte
	err        error
	onDownload func(url string)
}

func (d *orderedDownloader) DownloadFile(_ context.Context, r DownloadFileRequest) (DownloadFileResult, error) {
	if d.onDownload != nil {
		d.onDownload(r.URL)
	}
	if d.err != nil {
		return DownloadFileResult{}, d.err
	}
	b, ok := d.blobs[r.URL]
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

func buildTestRuntimeIPKVersion(t *testing.T, source, arch, version, url string) (FircBinarySpec, []byte) {
	t.Helper()
	t.Setenv("FIRC_SOURCE_ROOT", source)
	for rel, s := range map[string]string{
		"LICENSE":                             "GPL-3.0-or-later test fixture\n",
		"files/entware_kn/etc/init.d/S99firc": "#!/bin/sh\n[ -e /opt/etc/firc/.awgm-disabled ] && exit 8\nexit 0\n",
		"files/entware_kn/etc/ndm/netfilter.d/100-firc": "#!/bin/sh\nexit 0\n",
		"files/common/etc/firc/firc.conf":               "configVersion: " + version + "\n",
		"files/common/etc/firc/groups.yaml":             "groups: []\n",
	} {
		p := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0755); err != nil {
			t.Fatal(err)
		}
	}
	fircd := filepath.Join(source, "fircd-"+version)
	tun := filepath.Join(source, "tunvless-"+version)
	if err := os.WriteFile(fircd, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64, "fircd-"+version), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tun, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64, "tunvless-"+version), 0755); err != nil {
		t.Fatal(err)
	}
	ipk := filepath.Join(source, "runtime-"+version+".ipk")
	if err := BuildRuntimeIPK(source, fircd, tun, arch, version, ipk); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ipk)
	if err != nil {
		t.Fatal(err)
	}
	return FircBinarySpec{
		Version:       version,
		PackageURL:    url,
		PackageSHA256: sha256HexBytes(b),
		PackageSize:   int64(len(b)),
	}, b
}

func setupUpgradeFixture(t *testing.T) (
	root string,
	pm *upgradeMockPackages,
	dl *orderedDownloader,
	op *Operator,
	running *atomic.Bool,
	specV1 FircBinarySpec,
	specV2 FircBinarySpec,
	ipkV1 []byte,
	ipkV2 []byte,
) {
	t.Helper()
	source := t.TempDir()
	root = t.TempDir()
	const arch = "aarch64-3.10"

	specV1, ipkV1 = buildTestRuntimeIPKVersion(t, source, arch, "0.7.0-1", "https://example.invalid/runtime-0.7.0-1.ipk")
	specV2, ipkV2 = buildTestRuntimeIPKVersion(t, source, arch, "0.8.0-1", "https://example.invalid/runtime-0.8.0-1.ipk")

	pm = &upgradeMockPackages{root: root, arch: arch, versions: map[string]string{}}
	dl = &orderedDownloader{
		blobs: map[string][]byte{
			specV1.PackageURL: ipkV1,
			specV2.PackageURL: ipkV2,
		},
	}

	instV1 := NewOnDemandInstaller(arch, specV1)
	instV1.SetPackageLayout(DefaultPackageLayout(root))
	instV1.SetPackageManager(pm)
	instV1.SetDownloader(dl)
	instV1.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	if err := instV1.Install(context.Background()); err != nil {
		t.Fatalf("seed install v1: %v", err)
	}

	running = &atomic.Bool{}
	op = NewOperator(Options{
		ConfigDir:   filepath.Join(root, "opt", "etc", "firc"),
		FircdBinary: filepath.Join(root, "opt", "sbin", "fircd"),
		TunvlessBin: filepath.Join(root, "opt", "sbin", "tunvless"),
		InitScript:  filepath.Join(root, "opt", "etc", "init.d", "S99firc"),
	})
	return root, pm, dl, op, running, specV1, specV2, ipkV1, ipkV2
}

func TestOnDemandInstaller_UpgradeActiveRunningSuccess(t *testing.T) {
	ctx := context.Background()
	root, pm, dl, op, running, _, specV2, _, _ := setupUpgradeFixture(t)
	running.Store(true)

	userCfg := []byte("custom-config: true\n")
	if err := os.WriteFile(filepath.Join(root, "opt", "etc", "firc", "firc.conf"), userCfg, 0600); err != nil {
		t.Fatal(err)
	}

	var events []string
	var mu sync.Mutex
	record := func(e string) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}

	dl.onDownload = func(u string) { record("download:" + u) }
	pm.onInstall = func(ver string) { record("install:" + ver) }
	op.SetLifecycleHooks(
		func(context.Context) error {
			record("start")
			running.Store(true)
			return nil
		},
		func(context.Context) error {
			record("stop")
			running.Store(false)
			return nil
		},
	)

	inst := NewOnDemandInstaller("aarch64-3.10", specV2)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	inst.SetPackageManager(pm)
	inst.SetDownloader(dl)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	inst.SetEngineActiveChecker(func() bool { return true })
	inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })
	inst.SetTunvlessRunningChecker(func(context.Context) bool { return false })

	pre := inst.GetInstallStatus(ctx, op, true)
	if !pre.UpdateAvailable || pre.InstalledVersion != "0.7.0-1" || pre.AvailableVersion != "0.8.0-1" || pre.State != InstallStateRunning {
		t.Fatalf("unexpected pre-upgrade status: %+v", pre)
	}

	if err := inst.Upgrade(ctx, op); err != nil {
		t.Fatalf("Upgrade failed: %v", err)
	}

	wantOrder := []string{"download:" + specV2.PackageURL, "stop", "install:0.8.0-1", "start"}
	if !slices.Equal(events, wantOrder) {
		t.Fatalf("unexpected operation sequence: got %v, want %v", events, wantOrder)
	}
	if !running.Load() {
		t.Fatal("expected daemon running after upgrade")
	}
	if gotCfg, err := os.ReadFile(filepath.Join(root, "opt", "etc", "firc", "firc.conf")); err != nil || string(gotCfg) != string(userCfg) {
		t.Fatalf("expected config preserved, got %q (err=%v)", string(gotCfg), err)
	}
	post := inst.GetInstallStatus(ctx, op, true)
	if post.UpdateAvailable || post.InstalledVersion != "0.8.0-1" || post.AvailableVersion != "0.8.0-1" || post.State != InstallStateRunning {
		t.Fatalf("unexpected post-upgrade status: %+v", post)
	}
	journal, err := inst.readOperation()
	if err != nil || journal.Pending || journal.Action != "upgrade" {
		t.Fatalf("expected clean upgrade journal, got %+v (err=%v)", journal, err)
	}
}

func TestOnDemandInstaller_UpgradeRollbackOnStartFailure(t *testing.T) {
	ctx := context.Background()
	root, pm, dl, op, running, _, specV2, _, _ := setupUpgradeFixture(t)
	running.Store(true)

	startAttempts := 0
	var receiptDuringFirstBoot string
	op.SetLifecycleHooks(
		func(context.Context) error {
			startAttempts++
			if startAttempts == 1 {
				if raw, err := os.ReadFile(filepath.Join(root, "opt", "var", "cache", "awg-manager", "firc-runtime", "installed.json")); err == nil {
					receiptDuringFirstBoot = string(raw)
				}
				return errors.New("new fircd crashed on boot")
			}
			running.Store(true)
			return nil
		},
		func(context.Context) error {
			running.Store(false)
			return nil
		},
	)

	inst := NewOnDemandInstaller("aarch64-3.10", specV2)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	inst.SetPackageManager(pm)
	inst.SetDownloader(dl)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	inst.SetEngineActiveChecker(func() bool { return true })
	inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })
	inst.SetTunvlessRunningChecker(func(context.Context) bool { return false })

	err := inst.Upgrade(ctx, op)
	if err == nil || !strings.Contains(err.Error(), "new fircd crashed on boot") {
		t.Fatalf("expected startup failure error, got %v", err)
	}
	if !strings.Contains(receiptDuringFirstBoot, `"0.7.0-1"`) || strings.Contains(receiptDuringFirstBoot, `"0.8.0-1"`) {
		t.Fatalf("expected installed.json receipt to stay at 0.7.0-1 until post-start health check passes, got %q", receiptDuringFirstBoot)
	}

	st := inst.GetInstallStatus(ctx, op, true)
	if st.State != InstallStateRunning || st.InstalledVersion != "0.7.0-1" || !st.UpdateAvailable {
		t.Fatalf("expected rolled back running 0.7.0-1 with updateAvailable=true, got %+v", st)
	}
	journal, jErr := inst.readOperation()
	if jErr != nil || journal.Pending || !strings.Contains(journal.Error, "new fircd crashed on boot") {
		t.Fatalf("expected non-pending journal recording error, got %+v (err=%v)", journal, jErr)
	}
}

func TestOnDemandInstaller_UpgradeDownloadFailureDoesNotStopRunningDaemon(t *testing.T) {
	ctx := context.Background()
	root, pm, dl, op, running, _, specV2, _, _ := setupUpgradeFixture(t)
	running.Store(true)

	var stops atomic.Int32
	op.SetLifecycleHooks(
		func(context.Context) error { running.Store(true); return nil },
		func(context.Context) error { stops.Add(1); running.Store(false); return nil },
	)
	dl.err = errors.New("download timeout")

	inst := NewOnDemandInstaller("aarch64-3.10", specV2)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	inst.SetPackageManager(pm)
	inst.SetDownloader(dl)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	inst.SetEngineActiveChecker(func() bool { return true })
	inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })
	inst.SetTunvlessRunningChecker(func(context.Context) bool { return false })

	err := inst.Upgrade(ctx, op)
	if err == nil || !strings.Contains(err.Error(), "download timeout") {
		t.Fatalf("expected download timeout error, got %v", err)
	}
	if stops.Load() != 0 {
		t.Fatalf("expected 0 stop calls when download fails, got %d", stops.Load())
	}
	if !running.Load() {
		t.Fatal("expected daemon to remain running")
	}
}

func TestOnDemandInstaller_UpgradeMissingBackupFailsClosedWithoutStopping(t *testing.T) {
	ctx := context.Background()
	root, pm, dl, op, running, _, specV2, _, _ := setupUpgradeFixture(t)
	running.Store(true)

	var stops atomic.Int32
	op.SetLifecycleHooks(
		func(context.Context) error { running.Store(true); return nil },
		func(context.Context) error { stops.Add(1); running.Store(false); return nil },
	)
	if err := os.Remove(filepath.Join(root, "opt/var/cache/awg-manager/firc-runtime/installed.json")); err != nil {
		t.Fatal(err)
	}

	inst := NewOnDemandInstaller("aarch64-3.10", specV2)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	inst.SetPackageManager(pm)
	inst.SetDownloader(dl)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	inst.SetEngineActiveChecker(func() bool { return true })
	inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })
	inst.SetTunvlessRunningChecker(func(context.Context) bool { return false })

	if err := inst.Upgrade(ctx, op); !errors.Is(err, ErrUpgradeBackupMissing) {
		t.Fatalf("expected ErrUpgradeBackupMissing, got %v", err)
	}
	if stops.Load() != 0 {
		t.Fatalf("expected 0 stop calls when backup is missing, got %d", stops.Load())
	}
}

func TestOnDemandInstaller_UpgradeInterruptedStartupRecovery(t *testing.T) {
	ctx := context.Background()
	root, pm, dl, op, _, _, specV2, _, _ := setupUpgradeFixture(t)

	// Simulate crash mid-upgrade after opkg started mutating files/version, before installed.json updated to 0.8.0-1.
	pm.mu.Lock()
	pm.version(RuntimePackageName, "0.8.0-1")
	pm.mu.Unlock()
	if err := os.Remove(filepath.Join(root, "opt", "sbin", "fircd")); err != nil {
		t.Fatal(err)
	}
	inst := NewOnDemandInstaller("aarch64-3.10", specV2)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	inst.SetPackageManager(pm)
	inst.SetDownloader(dl)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })

	if err := inst.saveOperation(runtimeOperation{
		Action:          "upgrade",
		Pending:         true,
		PreviousVersion: "0.7.0-1",
		TargetVersion:   "0.8.0-1",
	}); err != nil {
		t.Fatal(err)
	}

	fresh := NewOnDemandInstaller("aarch64-3.10", specV2)
	fresh.SetPackageLayout(DefaultPackageLayout(root))
	fresh.SetPackageManager(pm)
	if err := fresh.RecoverPendingOperationOnStartup(ctx, op); err != nil {
		t.Fatalf("RecoverPendingOperationOnStartup failed: %v", err)
	}

	st := fresh.GetInstallStatus(ctx, op, true)
	if st.State != InstallStateInstalled || st.InstalledVersion != "0.7.0-1" || !st.UpdateAvailable {
		t.Fatalf("expected recovered 0.7.0-1 runtime with updateAvailable=true, got %+v", st)
	}
	journal, err := fresh.readOperation()
	if err != nil || journal.Pending || journal.Action != "upgrade" {
		t.Fatalf("expected cleared upgrade recovery journal, got %+v (err=%v)", journal, err)
	}
}

func (m *upgradeMockPackages) version(pkg, ver string) {
	if m.versions == nil {
		m.versions = map[string]string{}
	}
	m.versions[pkg] = ver
}

func TestOnDemandInstaller_UpgradePreconditions(t *testing.T) {
	ctx := context.Background()

	// 1. Not installed -> ErrRuntimeNotInstalled
	rootEmpty := t.TempDir()
	specV2 := FircBinarySpec{Version: "0.8.0-1", PackageURL: "https://example.invalid/v2.ipk", PackageSHA256: strings.Repeat("a", 64), PackageSize: 1024}
	instEmpty := NewOnDemandInstaller("aarch64-3.10", specV2)
	instEmpty.SetPackageLayout(DefaultPackageLayout(rootEmpty))
	instEmpty.SetPackageManager(&upgradeMockPackages{root: rootEmpty, arch: "aarch64-3.10"})
	if err := instEmpty.Upgrade(ctx, nil); !errors.Is(err, ErrRuntimeNotInstalled) {
		t.Fatalf("expected ErrRuntimeNotInstalled, got %v", err)
	}

	// 2. Already up-to-date -> ErrAlreadyUpToDate
	root, pm, dl, op, _, specV1, specV2Real, _, _ := setupUpgradeFixture(t)
	instSame := NewOnDemandInstaller("aarch64-3.10", specV1)
	instSame.SetPackageLayout(DefaultPackageLayout(root))
	instSame.SetPackageManager(pm)
	instSame.SetDownloader(dl)
	if err := instSame.Upgrade(ctx, op); !errors.Is(err, ErrAlreadyUpToDate) {
		t.Fatalf("expected ErrAlreadyUpToDate, got %v", err)
	}

	// 3. Recovery required (missing fircd) -> ErrUpgradeRequiresRepair
	if err := os.Remove(filepath.Join(root, "opt", "sbin", "fircd")); err != nil {
		t.Fatal(err)
	}
	instBroken := NewOnDemandInstaller("aarch64-3.10", specV2Real)
	instBroken.SetPackageLayout(DefaultPackageLayout(root))
	instBroken.SetPackageManager(pm)
	instBroken.SetDownloader(dl)
	if err := instBroken.Upgrade(ctx, op); !errors.Is(err, ErrUpgradeRequiresRepair) {
		t.Fatalf("expected ErrUpgradeRequiresRepair, got %v", err)
	}

	// 4. Dependency probe error -> fails closed
	rootDep, pmDep, dlDep, opDep, _, _, specDepV2, _, _ := setupUpgradeFixture(t)
	instDep := NewOnDemandInstaller("aarch64-3.10", specDepV2)
	instDep.SetPackageLayout(DefaultPackageLayout(rootDep))
	instDep.SetPackageManager(pmDep)
	instDep.SetDownloader(dlDep)
	depErr := errors.New("settings unreadable")
	instDep.SetDependencyChecker(func(context.Context) ([]string, error) { return nil, depErr })
	if err := instDep.Upgrade(ctx, opDep); !errors.Is(err, depErr) {
		t.Fatalf("expected dependency probe error, got %v", err)
	}
}
