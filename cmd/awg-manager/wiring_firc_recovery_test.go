package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/logging"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeStartupRuntimeManager struct {
	ver string
}

func (f *fakeStartupRuntimeManager) Install(context.Context, string) error {
	f.ver = "0.7.0-1"
	return nil
}

func (f *fakeStartupRuntimeManager) Remove(context.Context, string) error {
	f.ver = ""
	return nil
}

func (f *fakeStartupRuntimeManager) Version(context.Context, string) (string, error) {
	return f.ver, nil
}

func TestFircStartupRecoveryGate_BlocksOnCorruptPendingMigrationAndRespectsManualDisable(t *testing.T) {
	t.Run("unrecoverable_pending_package_operation_blocks_startup", func(t *testing.T) {
		root := t.TempDir()
		dataDir := filepath.Join(root, "opt/etc/awg-manager")
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			t.Fatal(err)
		}
		settingsStore := storage.NewSettingsStore(dataDir)
		if err := settingsStore.Update(func(s *storage.Settings) error {
			s.SingboxRouter.RoutingEngine = "firc"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		loggingService := logging.NewService(settingsStore)
		t.Cleanup(loggingService.Stop)

		var started atomic.Int32
		fircOp := firc.NewOperator(firc.Options{
			ConfigDir:   filepath.Join(root, "opt/etc/firc"),
			FircdBinary: filepath.Join(root, "opt/sbin/fircd"),
			TunvlessBin: filepath.Join(root, "opt/sbin/tunvless"),
			InitScript:  filepath.Join(root, "opt/etc/init.d/S99firc"),
		})
		fircOp.SetLifecycleHooks(
			func(context.Context) error { started.Add(1); return nil },
			func(context.Context) error { return nil },
		)

		inst := firc.NewOnDemandInstaller("aarch64-3.10", firc.FircBinarySpec{
			Version:       "0.7.0-1",
			PackageURL:    "https://example.invalid/runtime.ipk",
			PackageSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			PackageSize:   1024,
		})
		inst.SetPackageLayout(firc.DefaultPackageLayout(root))
		inst.SetPackageManager(&fakeStartupRuntimeManager{})
		inst.SetRuntimeLocker(fircOp.LockRuntimeMutation)

		// Write a pending migrate_legacy operation: the action is no longer recognised and must fail closed.
		opJournalPath := filepath.Join(root, "opt/etc/awg-manager/firc-runtime-operation.json")
		if err := os.WriteFile(opJournalPath, []byte(`{"action":"migrate_legacy","pending":true}`), 0600); err != nil {
			t.Fatal(err)
		}

		a := &app{
			dataDir:        dataDir,
			settingsStore:  settingsStore,
			loggingService: loggingService,
			bootLog:        logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubCleanup),
			fircOp:         fircOp,
			fircInstaller:  inst,
		}
		a.completeFircStartup()

		if started.Load() != 0 {
			t.Fatalf("expected FIRC auto-start to be blocked by pending unrecoverable package migration, got starts=%d", started.Load())
		}
	})

	t.Run("clean_startup_respects_manual_disable_marker", func(t *testing.T) {
		root := t.TempDir()
		dataDir := filepath.Join(root, "opt/etc/awg-manager")
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			t.Fatal(err)
		}
		settingsStore := storage.NewSettingsStore(dataDir)
		if err := settingsStore.Update(func(s *storage.Settings) error {
			s.SingboxRouter.RoutingEngine = "firc"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		loggingService := logging.NewService(settingsStore)
		t.Cleanup(loggingService.Stop)

		var started atomic.Int32
		fircOp := firc.NewOperator(firc.Options{
			ConfigDir:   filepath.Join(root, "opt/etc/firc"),
			FircdBinary: filepath.Join(root, "opt/sbin/fircd"),
			TunvlessBin: filepath.Join(root, "opt/sbin/tunvless"),
			InitScript:  filepath.Join(root, "opt/etc/init.d/S99firc"),
		})
		fircOp.SetLifecycleHooks(
			func(context.Context) error { started.Add(1); return nil },
			func(context.Context) error { return nil },
		)
		if err := fircOp.StopByUser(context.Background()); err != nil {
			t.Fatal(err)
		}

		inst := firc.NewOnDemandInstaller("aarch64-3.10", firc.FircBinarySpec{})
		inst.SetPackageLayout(firc.DefaultPackageLayout(root))
		inst.SetPackageManager(&fakeStartupRuntimeManager{})
		inst.SetRuntimeLocker(fircOp.LockRuntimeMutation)

		a := &app{
			dataDir:        dataDir,
			settingsStore:  settingsStore,
			loggingService: loggingService,
			bootLog:        logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubCleanup),
			fircOp:         fircOp,
			fircInstaller:  inst,
		}
		a.completeFircStartup()

		if started.Load() != 0 {
			t.Fatalf("expected manually disabled FIRC not to auto-start on boot, got starts=%d", started.Load())
		}
	})
}

type stubNDMSGetter struct {
	payload []byte
	err     error
}

func (s stubNDMSGetter) Get(ctx context.Context, path string, dst any) error {
	raw, err := s.GetRaw(ctx, path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func (s stubNDMSGetter) GetRaw(_ context.Context, path string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	if path == "/show/ndns" {
		return s.payload, nil
	}
	return nil, errors.New("unexpected path: " + path)
}

func (s stubNDMSGetter) Post(context.Context, any) (json.RawMessage, error) {
	return nil, errors.New("not implemented")
}

func TestWiringFircKeenDNSCloudProvider_FromNDMSQueries(t *testing.T) {
	payload := []byte(`{
		"booked": "myrouter",
		"domain": "keenetic.pro",
		"access": "cloud",
		"ttp": {
			"tunnel": [
				{"target": "relay1.keenetic.net:443", "target-remote": "91.92.241.80:443"},
				{"target": "relay2.keenetic.cloud", "target-remote": "185.162.93.42"}
			]
		}
	}`)
	a := &app{
		ndmsQueries: ndmsquery.NewQueries(ndmsquery.Deps{Getter: stubNDMSGetter{payload: payload}}),
		fircOp: firc.NewOperator(firc.Options{
			ConfigDir: t.TempDir(),
		}),
	}
	a.wireFircKeenDNSCloudProvider()

	prov := a.fircOp.KeenCloudProvider()
	if prov == nil {
		t.Fatal("expected wireFircKeenDNSCloudProvider to register KeenCloudProvider on fircOp")
	}
	snap, err := prov(context.Background())
	if err != nil {
		t.Fatalf("unexpected provider error: %v", err)
	}
	if !snap.Enabled {
		t.Fatal("expected snap.Enabled=true")
	}
	wantIPs := []string{"91.92.241.80", "185.162.93.42"}
	if !reflect.DeepEqual(snap.RelayIPs, wantIPs) {
		t.Fatalf("RelayIPs mismatch: got %v want %v", snap.RelayIPs, wantIPs)
	}
	wantDomains := []string{"relay1.keenetic.net", "relay2.keenetic.cloud"}
	if !reflect.DeepEqual(snap.RelayDomains, wantDomains) {
		t.Fatalf("RelayDomains mismatch: got %v want %v", snap.RelayDomains, wantDomains)
	}

	// Verify RCI errors propagate rather than returning a false-empty snapshot.
	wantErr := errors.New("ndms timeout")
	aErr := &app{
		ndmsQueries: ndmsquery.NewQueries(ndmsquery.Deps{Getter: stubNDMSGetter{err: wantErr}}),
		fircOp:      firc.NewOperator(firc.Options{ConfigDir: t.TempDir()}),
	}
	aErr.wireFircKeenDNSCloudProvider()
	if _, err := aErr.fircOp.KeenCloudProvider()(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v to propagate from NDMS KeenDNS store, got %v", wantErr, err)
	}
}
