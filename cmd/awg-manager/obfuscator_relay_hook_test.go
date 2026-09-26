package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/obfuscator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Хук выключателя ядро/процесс (спека §4.8/§4.9): перезапуск получают только
// включённые Phobos-туннели, отметка сторожа снимается только при возврате к
// ядру, отказ одного туннеля не останавливает остальные. Туннель без живого
// релея не трогается: бэкенд ему выберет следующий Start.
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
		{ID: "awg6", Enabled: true, Obfuscator: phobos}, // релей не поднят — бэкенд выберет Start
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
		var tripped atomic.Bool
		tripped.Store(true)
		hook := obfuscatorRelayChanged(settings, tunnels, func(_ context.Context, id string) error {
			restarted = append(restarted, id)
			if id == "awg1" {
				return errors.New("отказ")
			}
			return nil
		}, func(id string) bool { return id != "awg6" }, &tripped, logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps))
		hook(tc.process)

		slices.Sort(restarted)
		if !slices.Equal(restarted, []string{"awg1", "awg2"}) {
			t.Errorf("process=%v: перезапущены %v, ждали [awg1 awg2]", tc.process, restarted)
		}
		cur, err := settings.Get()
		if err != nil {
			t.Fatal(err)
		}
		if tripped.Load() != tc.process {
			t.Errorf("process=%v: флаг сторожа в памяти %v, ждали %v", tc.process, tripped.Load(), tc.process)
		}
		if cur.ObfuscatorKmodTripped != tc.wantTripped {
			t.Errorf("process=%v: отметка сторожа %q, ждали %q", tc.process, cur.ObfuscatorKmodTripped, tc.wantTripped)
		}
	}
}

// Сторож (§4.9) обязан выключить ядро в памяти сразу, даже если запись в
// настройки не удалась; hash oops сохраняется только после записанного trip —
// иначе при сбое записи следующий старт не увидел бы oops снова.
func TestApplyObfWatchdog(t *testing.T) {
	log := logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps)
	phobos := &storage.Obfuscator{Flavor: storage.ObfuscatorFlavorPhobos}
	for _, tc := range []struct {
		name         string
		reason, hash string
		tripErr      error
		wantTrip     bool
		wantHash     string
		wantKernel   bool
	}{
		{name: "trip записан", reason: "oops", hash: "h2", wantTrip: true, wantHash: "h2"},
		{name: "trip не записан", reason: "oops", hash: "h2", tripErr: errors.New("диск"), wantTrip: true},
		{name: "чужой oops", hash: "h2", wantHash: "h2", wantKernel: true},
		{name: "тот же hash", hash: "h1", wantKernel: true},
		{name: "метка без oops, запись не удалась", reason: "reboot", tripErr: errors.New("диск"), wantTrip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tripped atomic.Bool
			var gotTrip bool
			var gotHash string
			applyObfWatchdog(tc.reason, tc.hash, "h1",
				func(string) error { gotTrip = true; return tc.tripErr },
				func(h string) error { gotHash = h; return nil },
				&tripped, log)
			if gotTrip != tc.wantTrip || gotHash != tc.wantHash {
				t.Errorf("trip=%v hash=%q, ждали trip=%v hash=%q", gotTrip, gotHash, tc.wantTrip, tc.wantHash)
			}
			useKernel := obfUseKernel(func() bool { return false }, func() bool { return true }, &tripped)
			if got := useKernel(phobos, "192.0.2.1"); got != tc.wantKernel {
				t.Errorf("useKernel=%v, ждали %v", got, tc.wantKernel)
			}
		})
	}
}

type obfLogSpy struct{ lines []string }

func (s *obfLogSpy) AppLog(_ logging.Level, _, _, _, _, msg string) { s.lines = append(s.lines, msg) }

// Сбой записи trip: для oops сигнал не теряется (hash не сохранён — сторож
// сработает на следующем старте), для метки boot_id — теряется. Журнал
// обязан говорить правду для обоих случаев.
func TestApplyObfWatchdog_TripSaveFailMessage(t *testing.T) {
	for _, tc := range []struct {
		name, reason, want, notWant string
	}{
		{"oops", obfuscator.OopsReasonPrefix + "epc relay_stop", "сработает снова", "не повторится"},
		{"метка загрузки", "роутер перезагрузился в первые 5 минут после загрузки awgm_relay", "не повторится", "сработает снова"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &obfLogSpy{}
			var tripped atomic.Bool
			applyObfWatchdog(tc.reason, "h2", "h1",
				func(string) error { return errors.New("диск") },
				func(string) error { return nil },
				&tripped, logging.NewScopedLogger(spy, logging.GroupTunnel, logging.SubOps))
			all := strings.Join(spy.lines, "\n")
			if !strings.Contains(all, tc.want) || strings.Contains(all, tc.notWant) {
				t.Fatalf("журнал: %q", spy.lines)
			}
		})
	}
}
