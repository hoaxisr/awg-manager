package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Старт при установленном рантайме без tunnels/groups.yaml ничего не переносит из
// sing-box сам: тихая автомиграция заполняла AWGM Route без ведома пользователя.
func TestCompleteFircStartup_NoSilentMigrationFromSingbox(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "opt/etc/awg-manager")
	for rel, content := range map[string]string{
		"opt/sbin/fircd":                                       "fircd",
		"opt/sbin/tunvless":                                    "tunvless",
		"opt/lib/opkg/info/awgm-route.list":                    "/opt/sbin/fircd\n/opt/sbin/tunvless\n",
		"opt/etc/awg-manager/singbox/config.d/10-tunnels.json": `{"outbounds":[{"type":"vless","tag":"vless-a","server":"1.2.3.4","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`,
		"opt/etc/awg-manager/singbox/config.d/20-router.json":  `{"route":{"rules":[{"domain_suffix":["example.com"],"outbound":"vless-a"}],"final":"direct"}}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	settingsStore := storage.NewSettingsStore(dataDir)
	if err := settingsStore.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "sing-box"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loggingService := logging.NewService(settingsStore)
	t.Cleanup(loggingService.Stop)

	fircOp := firc.NewOperator(firc.Options{
		ConfigDir:   filepath.Join(root, "opt/etc/firc"),
		FircdBinary: filepath.Join(root, "opt/sbin/fircd"),
		TunvlessBin: filepath.Join(root, "opt/sbin/tunvless"),
		InitScript:  filepath.Join(root, "opt/etc/init.d/S99firc"),
	})
	fircOp.SetLifecycleHooks(func(context.Context) error { return nil }, func(context.Context) error { return nil })
	if err := fircOp.ValidateBinaries(); err != nil {
		t.Fatalf("рантайм должен считаться установленным: %v", err)
	}
	inst := firc.NewOnDemandInstaller("aarch64-3.10", firc.FircBinarySpec{})
	inst.SetPackageLayout(firc.DefaultPackageLayout(root))
	inst.SetPackageManager(&fakeStartupRuntimeManager{ver: "0.7.0-1"})
	inst.SetRuntimeLocker(fircOp.LockRuntimeMutation)

	a := &app{
		dataDir:        dataDir,
		settingsStore:  settingsStore,
		loggingService: loggingService,
		bootLog:        logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubCleanup),
		fircOp:         fircOp,
		fircInstaller:  inst,
		singboxOp: singbox.NewOperator(singbox.OperatorDeps{
			Dir: filepath.Join(dataDir, "singbox"), Binary: filepath.Join(root, "absent"),
		}),
	}
	a.completeFircStartup()

	for _, p := range []string{fircOp.TunnelsPath(), fircOp.GroupsPath()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("старт записал %s из sing-box без ведома пользователя (err=%v)", p, err)
		}
	}
}
