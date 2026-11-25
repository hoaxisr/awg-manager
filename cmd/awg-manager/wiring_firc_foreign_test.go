package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// countingRuntimeManager ловит обращения к opkg на старте.
type countingRuntimeManager struct{ installs, removes int }

func (m *countingRuntimeManager) Install(context.Context, string) error { m.installs++; return nil }
func (m *countingRuntimeManager) Remove(context.Context, string) error  { m.removes++; return nil }
func (m *countingRuntimeManager) Version(context.Context, string) (string, error) {
	return "0.7.0-1", nil
}

// failingRouterLifecycle: поднимать роутер sing-box без бинаря — ошибка.
type failingRouterLifecycle struct{}

func (failingRouterLifecycle) ParkForExternalEngine(context.Context) error { return nil }
func (failingRouterLifecycle) ResumeFromExternalEngine(context.Context) error {
	return errors.New("sing-box не установлен")
}

func newForeignFircApp(t *testing.T, engine string) (*app, string, *atomic.Int32, *countingRuntimeManager) {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "opt/etc/awg-manager")
	for rel, content := range map[string]string{
		"opt/bin/fircd":                                     "fircd",
		"opt/etc/init.d/S99firc":                            "#!/bin/sh\necho \"$1\" >> \"$0.ran\"\n",
		"opt/etc/firc/awgm-ext.json":                        `{"serverRouting":{"enabledServers":["awg_server","keenetic_wg"]}}`,
		"opt/var/lib/firc/hwid":                             "hwid\n",
		"opt/lib/opkg/info/firc.list":                       "/opt/bin/fircd\n",
		"opt/etc/awg-manager/firc-runtime-operation.json":   `{"action":"uninstall","pending":true}`,
		"opt/etc/awg-manager/singbox/config.d/00-base.json": `{"log":{"level":"info"}}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store := storage.NewSettingsStore(dataDir)
	if err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = engine
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	log := logging.NewService(store)
	t.Cleanup(log.Stop)
	op := firc.NewOperator(firc.Options{
		ConfigDir:   filepath.Join(root, "opt/etc/firc"),
		FircdBinary: filepath.Join(root, "opt/bin/fircd"),
		TunvlessBin: filepath.Join(root, "opt/bin/tunvless"),
		InitScript:  filepath.Join(root, "opt/etc/init.d/S99firc"),
		Foreign:     firc.NewForeignDetector(root),
	})
	var lifecycle atomic.Int32
	op.SetLifecycleHooks(
		func(context.Context) error { lifecycle.Add(1); return nil },
		func(context.Context) error { lifecycle.Add(1); return nil },
	)
	inst := firc.NewOnDemandInstaller("aarch64-3.10", firc.FircBinarySpec{})
	inst.SetPackageLayout(firc.DefaultPackageLayout(root))
	pm := &countingRuntimeManager{}
	inst.SetPackageManager(pm)
	return &app{
		dataDir: dataDir, settingsStore: store, loggingService: log,
		bootLog: logging.NewScopedLogger(log, logging.GroupSystem, logging.SubCleanup),
		fircOp:  op, fircInstaller: inst,
	}, root, &lifecycle, pm
}

// foreignDirs — только каталоги стороннего firc и файлы пакетной операции.
func foreignDirs(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	for _, dir := range []string{"opt/bin", "opt/etc/firc", "opt/etc/init.d", "opt/var/lib/firc", "opt/lib"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			out.WriteString(p + info.ModTime().String())
			if info.Mode().IsRegular() {
				raw, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				out.Write(raw)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}

func TestCompleteFircStartup_ForeignFircLeftAlone(t *testing.T) {
	for _, engine := range []string{"sing-box", "firc"} {
		t.Run(engine, func(t *testing.T) {
			a, root, lifecycle, pm := newForeignFircApp(t, engine)
			before := foreignDirs(t, root)
			var ownChains atomic.Int32
			firc.SetExtIptablesRunner(func(_ context.Context, args ...string) ([]byte, error) {
				if cmd := strings.Join(args, " "); strings.HasSuffix(cmd, "-X FEXT_DNS") {
					ownChains.Add(1)
				} else if strings.Contains(cmd, " FIRC_") {
					t.Errorf("тронуты правила стороннего firc: %s", cmd)
				}
				return nil, errors.New("no such rule")
			})
			t.Cleanup(func() { firc.SetExtIptablesRunner(nil) })
			a.migrateLegacyFircServerRouting(context.Background())
			a.completeFircStartup()
			if got, want := ownChains.Load() > 0, engine == "firc"; got != want {
				t.Errorf("свои цепочки сняты: %v, want %v", got, want)
			}
			prev, next := storage.SingboxRouterSettings{}, storage.SingboxRouterSettings{SSTPIngress: true}
			a.syncFircFromRouterSettings(context.Background(), prev, next)
			if foreignDirs(t, root) != before {
				t.Error("файлы стороннего firc изменены на старте")
			}
			if lifecycle.Load() != 0 || pm.installs != 0 || pm.removes != 0 {
				t.Errorf("старт/стоп демона: %d, opkg install %d, remove %d", lifecycle.Load(), pm.installs, pm.removes)
			}
		})
	}
}

// Движок FIRC был выбран без sing-box: при стороннем firc уйти всё равно можно,
// а без стороннего отказ прежний — маршрутизация не должна пропасть молча.
func TestEngineHooks_LeavingFircWithoutSingbox(t *testing.T) {
	a, root, _, _ := newForeignFircApp(t, "firc")
	a.singboxOp = singbox.NewOperator(singbox.OperatorDeps{
		Dir: filepath.Join(a.dataDir, "singbox"), Binary: filepath.Join(root, "absent"), InitialStoppedByEngine: true,
	})
	a.routerCoord = &failingRouterLifecycle{}
	if err := a.fircOp.SwitchEngine(context.Background(), "sing-box", a.buildProductionEngineSwitchHooks()); err != nil {
		t.Fatalf("уход с FIRC: %v", err)
	}
	if a.isFircEngineActive() || a.singboxOp.IsStoppedByEngine() {
		t.Fatalf("движок FIRC всё ещё выбран (%v) или sing-box помечен остановленным им (%v)", a.isFircEngineActive(), a.singboxOp.IsStoppedByEngine())
	}
	if err := a.fircOp.SwitchEngine(context.Background(), "firc", a.buildProductionEngineSwitchHooks()); !errors.Is(err, firc.ErrForeignFirc) {
		t.Fatalf("выбор FIRC = %v, want ErrForeignFirc", err)
	}
	if err := os.Remove(filepath.Join(root, "opt/lib/opkg/info/firc.list")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "opt/bin/fircd")); err != nil {
		t.Fatal(err)
	}
	if err := a.buildProductionEngineSwitchHooks().StartTarget(context.Background(), "sing-box"); err == nil {
		t.Fatal("без стороннего firc переход на неустановленный sing-box должен отказать")
	}
}

func TestUplinkCatalog_ForeignFircTargetsHidden(t *testing.T) {
	a, _, _, _ := newForeignFircApp(t, "firc")
	list, err := a.uplinkCatalog(context.Background())
	if err != nil {
		t.Fatalf("каталог не должен сообщать о недоступности FIRC: %v", err)
	}
	for _, it := range list {
		if it.Target.Engine == "firc" {
			t.Errorf("цель стороннего firc в каталоге: %+v", it)
		}
	}
}

// Журнал прерванного переезда сборок develop не блокирует автостарт, в журнале —
// предупреждение с каталогом копии.
func TestCompleteFircStartup_AbandonedMigrationWarnsAndStarts(t *testing.T) {
	a, root, lifecycle, _ := newForeignFircApp(t, "firc")
	for _, rel := range []string{"opt/lib/opkg/info/firc.list", "opt/bin/fircd"} {
		if err := os.Remove(filepath.Join(root, rel)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(a.dataDir, "firc-runtime-operation.json"), []byte(`{"action":"migrate_external","pending":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.completeFircStartup()
	if lifecycle.Load() == 0 {
		t.Error("автостарт заблокирован прерванным переездом")
	}
	found := false
	entries, _ := a.loggingService.GetLogs(logging.BucketApp, "", "", "", time.Time{}, 100, 0)
	for _, e := range entries {
		found = found || strings.Contains(e.Message, a.fircInstaller.ExternalMigrationBackupDir())
	}
	if !found {
		t.Error("в журнале нет предупреждения с каталогом копии")
	}
}
