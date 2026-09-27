package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
)

func newForeignEnv(t *testing.T, take opkgtun.Taken, ndms map[string]bool) *foreignIfaces {
	t.Helper()
	settings := storage.NewSettingsStore(t.TempDir())
	if _, err := settings.Load(); err != nil {
		t.Fatal(err)
	}
	src := opkgtun.Source{Name: "тест", Read: func(context.Context) (opkgtun.Taken, error) { return take, nil }}
	return &foreignIfaces{
		settings:  settings,
		pool:      opkgtun.NewPool(16, src),
		ndmsNames: func(context.Context) (map[string]bool, error) { return ndms, nil },
		orphans:   func(context.Context) ([]external.OrphanIface, error) { return nil, nil },
		sysNet:    t.TempDir(),
	}
}

func TestForeignMark_Rejections(t *testing.T) {
	f := newForeignEnv(t, opkgtun.Taken{12: opkgtun.TunnelHolder("awg12", "дом")}, map[string]bool{"ppp0": true})
	for _, name := range []string{"", "a-very-long-name-16", "bad name", "a/b", "opkgtun12", "opkgtun17", "awgm0", "t2s0", "nwg1", "ppp0"} {
		err := f.Mark(context.Background(), name)
		if !errors.Is(err, api.ErrForeignIfaceRejected) {
			t.Errorf("Mark(%q) = %v, ждали отказ", name, err)
		}
	}
	if got := f.settings.GetForeignInterfaces(); len(got) != 0 {
		t.Fatalf("отказы записали %v", got)
	}
}

func TestForeignMark_AcceptsFreeAndAbsent(t *testing.T) {
	f := newForeignEnv(t, opkgtun.Taken{7: opkgtun.AnonHolder("запись NDMS OpkgTun7")}, nil)
	for _, name := range []string{"opkgtun7", "csqtt0", "csqtt0"} {
		if err := f.Mark(context.Background(), name); err != nil {
			t.Fatalf("Mark(%q): %v", name, err)
		}
	}
	if got := f.settings.GetForeignInterfaces(); !slices.Equal(got, []string{"opkgtun7", "csqtt0"}) {
		t.Fatalf("отметки = %v", got)
	}
}

func TestForeignMark_CanonicalName(t *testing.T) {
	f := newForeignEnv(t, nil, nil)
	for _, name := range []string{"OpkgTun7", "opkgtun07"} {
		if err := f.Mark(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.settings.GetForeignInterfaces(); !slices.Equal(got, []string{"opkgtun7"}) {
		t.Fatalf("отметки = %v, ждали одну opkgtun7", got)
	}
	if err := f.Unmark(context.Background(), "OpkgTun7"); err != nil {
		t.Fatal(err)
	}
	if got := f.settings.GetForeignInterfaces(); len(got) != 0 {
		t.Fatalf("после снятия = %v", got)
	}
}

func TestForeignMark_NDMSErrorFailsClosed(t *testing.T) {
	f := newForeignEnv(t, nil, nil)
	f.ndmsNames = func(context.Context) (map[string]bool, error) { return nil, errors.New("rci down") }
	err := f.Mark(context.Background(), "csqtt0")
	if err == nil || errors.Is(err, api.ErrForeignIfaceRejected) {
		t.Fatalf("err = %v, ждали внутреннюю ошибку", err)
	}
	if len(f.settings.GetForeignInterfaces()) != 0 {
		t.Fatal("отметка записана при недоступном NDMS")
	}
}

func TestForeignCandidates(t *testing.T) {
	f := newForeignEnv(t, nil, map[string]bool{"eth3": true})
	f.orphans = func(context.Context) ([]external.OrphanIface, error) {
		return []external.OrphanIface{{Iface: "opkgtun7", Description: "csqtt", KernelDevice: true}}, nil
	}
	mk := func(name string, tun bool, carrier string) {
		dir := filepath.Join(f.sysNet, name)
		_ = os.MkdirAll(dir, 0o755)
		if tun {
			_ = os.WriteFile(filepath.Join(dir, "tun_flags"), []byte("0x1001\n"), 0o644)
		}
		_ = os.WriteFile(filepath.Join(dir, "carrier"), []byte(carrier+"\n"), 0o644)
	}
	mk("csqtt0", true, "1")
	mk("zt0", true, "0")
	mk("eth3", true, "1")                      // известен NDMS
	mk("t2s0", true, "1")                      // наше имя
	mk("gre0", false, "1")                     // не TUN
	_ = f.settings.MarkForeignInterface("zt0") // уже отмечен — не кандидат

	got, err := f.Candidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []api.ForeignIfaceCandidate{
		{Name: "opkgtun7", Label: "csqtt", Kind: "opkgtun", Up: true},
		{Name: "csqtt0", Label: "csqtt0", Kind: "kernel", Up: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("кандидаты = %+v, want %+v", got, want)
	}
}
