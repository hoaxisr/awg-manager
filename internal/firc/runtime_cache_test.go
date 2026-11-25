package firc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// F831: перед операцией из кэша уходят IPK прежних версий и брошенные stage;
// IPK установленной версии (для отката), verified.ipk
// остаются. При незавершённой операции кэш не трогается.
func TestPruneRuntimeCache(t *testing.T) {
	root := t.TempDir()
	inst := NewOnDemandInstaller("aarch64-3.10", FircBinarySpec{})
	inst.SetPackageLayout(DefaultPackageLayout(root))
	cache := inst.rootPath("/opt/var/cache/awg-manager/firc-runtime")
	for _, d := range []string{"stage-1", "upgrade-stage-2"} {
		if err := os.MkdirAll(filepath.Join(cache, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"installed.json":  `{"version":"1","packageUrl":"https://x/a.ipk","packageSha256":"aaa","packageSize":1}`,
		"runtime-aaa.ipk": "cur",
		"runtime-bbb.ipk": "old",
		"verified.ipk":    "v",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(cache, n), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := inst.saveOperation(runtimeOperation{Action: "upgrade", Pending: true}); err != nil {
		t.Fatal(err)
	}
	inst.pruneRuntimeCache(cache)
	if _, err := os.Stat(filepath.Join(cache, "runtime-bbb.ipk")); err != nil {
		t.Fatal("pending operation: cache must stay untouched")
	}
	if err := inst.saveOperation(runtimeOperation{Action: "upgrade", Pending: false}); err != nil {
		t.Fatal(err)
	}
	inst.pruneRuntimeCache(cache)
	for _, gone := range []string{"runtime-bbb.ipk", "stage-1", "upgrade-stage-2"} {
		if _, err := os.Stat(filepath.Join(cache, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived prune", gone)
		}
	}
	for _, kept := range []string{"runtime-aaa.ipk", "verified.ipk", "installed.json"} {
		if _, err := os.Stat(filepath.Join(cache, kept)); err != nil {
			t.Errorf("%s removed: %v", kept, err)
		}
	}
}

// F834: IPK скачан в verified.ipk до захвата runtime-лока — загрузка не
// держит лок.
func TestInstallDownloadsBeforeRuntimeLock(t *testing.T) {
	root := t.TempDir()
	blob := []byte("runtime-ipk-bytes")
	spec := FircBinarySpec{Version: "0.7.0-1", PackageURL: "https://example.invalid/runtime.ipk", PackageSHA256: sha256HexBytes(blob), PackageSize: int64(len(blob))}
	inst := NewOnDemandInstaller("aarch64-3.10", spec)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	inst.SetDownloader(&mockFircDownloader{blobs: map[string][]byte{spec.PackageURL: blob}})
	verified := inst.rootPath("/opt/var/cache/awg-manager/firc-runtime/verified.ipk")
	var atLock error = os.ErrNotExist
	inst.SetRuntimeLocker(func() func() {
		atLock = VerifyFileSHA256(verified, spec.PackageSHA256)
		return func() {}
	})
	_ = inst.Install(context.Background())
	if atLock != nil {
		t.Fatalf("verified.ipk not ready when runtime lock taken: %v", atLock)
	}
}

// Ревью F831: уборка на установке действительно срабатывает (раньше стояла
// после отметки Pending и всегда выходила).
func TestInstallPrunesStaleRuntimeIPK(t *testing.T) {
	root := t.TempDir()
	blob := []byte("runtime-ipk-bytes")
	inst := NewOnDemandInstaller("aarch64-3.10", FircBinarySpec{Version: "1", PackageURL: "https://example.invalid/r.ipk", PackageSHA256: sha256HexBytes(blob), PackageSize: int64(len(blob))})
	inst.SetDownloader(&mockFircDownloader{blobs: map[string][]byte{"https://example.invalid/r.ipk": blob}})
	inst.SetPackageLayout(DefaultPackageLayout(root))
	inst.SetPackageManager(&mockRuntimePackages{root: root, versions: map[string]string{}})
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	cache := inst.rootPath("/opt/var/cache/awg-manager/firc-runtime")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(cache, "runtime-old.ipk")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ierr := inst.Install(context.Background())
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale runtime IPK survived install (install err: %v)", ierr)
	}
}
