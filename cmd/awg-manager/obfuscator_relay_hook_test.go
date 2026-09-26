package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Хук выключателя ядро/процесс (спека §4.8/§4.9): перезапуск получают только
// включённые Phobos-туннели, отметка сторожа снимается только при возврате к
// ядру, отказ одного туннеля не останавливает остальные.
func TestObfuscatorRelayChanged(t *testing.T) {
	dir := t.TempDir()
	settings := storage.NewSettingsStore(dir)
	if _, err := settings.Get(); err != nil {
		t.Fatal(err)
	}
	tunnels := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	phobos := &storage.Obfuscator{Flavor: storage.ObfuscatorFlavorPhobos}
	for _, tun := range []*storage.AWGTunnel{
		{ID: "awg1", Enabled: true, Obfuscator: phobos},
		{ID: "awg2", Enabled: true, Obfuscator: phobos},
		{ID: "awg3", Enabled: false, Obfuscator: phobos},
		{ID: "awg4", Enabled: true, Obfuscator: &storage.Obfuscator{Flavor: storage.ObfuscatorFlavorClusterM}},
		{ID: "awg5", Enabled: true},
	} {
		if err := tunnels.Create(tun); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		process     bool
		wantTripped string
	}{
		{process: true, wantTripped: "oops"},
		{process: false, wantTripped: ""},
	} {
		if err := settings.TripObfuscatorKmod("oops"); err != nil {
			t.Fatal(err)
		}
		var restarted []string
		hook := obfuscatorRelayChanged(settings, tunnels, func(_ context.Context, id string) error {
			restarted = append(restarted, id)
			if id == "awg1" {
				return errors.New("отказ")
			}
			return nil
		}, logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps))
		hook(tc.process)

		slices.Sort(restarted)
		if !slices.Equal(restarted, []string{"awg1", "awg2"}) {
			t.Errorf("process=%v: перезапущены %v, ждали [awg1 awg2]", tc.process, restarted)
		}
		cur, err := settings.Get()
		if err != nil {
			t.Fatal(err)
		}
		if cur.ObfuscatorKmodTripped != tc.wantTripped {
			t.Errorf("process=%v: отметка сторожа %q, ждали %q", tc.process, cur.ObfuscatorKmodTripped, tc.wantTripped)
		}
	}
}
