package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
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
		boundBy:   func(context.Context) (map[string]bool, error) { return nil, nil },
		orphans:   func(context.Context) ([]external.OrphanIface, error) { return nil, nil },
		sysNet:    t.TempDir(),
	}
}

func TestForeignMark_Rejections(t *testing.T) {
	f := newForeignEnv(t, opkgtun.Taken{12: opkgtun.TunnelHolder("awg12", "дом")}, map[string]bool{"ppp0": true})
	for _, name := range []string{"", "a-very-long-name-16", "bad name", "a/b", "opkgtun12", "opkgtun17", "awgm0", "t2s0", "nwg1", "ppp0",
		"a\nb", "a:b", ".", "..", "opkgtun", "opkgtun7x", "OPKGTUN7", "opkgtun99999999", "abcdefghijklmnop",
		"proxy3", "opkgtunx", "AWGM0", "ezcfg0", "EZCFG1"} {
		_, err := f.Mark(context.Background(), name)
		if !errors.Is(err, api.ErrForeignIfaceRejected) {
			t.Errorf("Mark(%q) = %v, ждали отказ", name, err)
		}
	}
	if got := f.settings.GetForeignInterfaces(); len(got) != 0 {
		t.Fatalf("отказы записали %v", got)
	}
}

// R15: wg*/awg* — имена userland-программ (wireguard-go, amneziawg-go), а не
// панели; отметка их спасает от strip. Отсутствующий и существующий TUN.
func TestForeignMark_UserlandWGNamesAccepted(t *testing.T) {
	f := newForeignEnv(t, nil, nil)
	if err := os.MkdirAll(filepath.Join(f.sysNet, "awg0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sysNet, "awg0", "tun_flags"), []byte("0x1001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wg0", "awg0"} {
		if _, err := f.Mark(context.Background(), name); err != nil {
			t.Fatalf("Mark(%q): %v", name, err)
		}
	}
	if got := f.settings.GetForeignInterfaces(); !slices.Equal(got, []string{"wg0", "awg0"}) {
		t.Fatalf("отметки = %v, ждали wg0 и awg0", got)
	}
}

func TestForeignMark_NameLength15Accepted(t *testing.T) {
	f := newForeignEnv(t, nil, nil)
	if _, err := f.Mark(context.Background(), "abcdefghijklmno"); err != nil {
		t.Fatalf("Mark(15 символов): %v", err)
	}
}

func TestForeignUnmark_BoundRefused(t *testing.T) {
	f := newForeignEnv(t, nil, nil)
	if _, err := f.Mark(context.Background(), "opkgtun7"); err != nil {
		t.Fatal(err)
	}
	f.boundBy = func(context.Context) (map[string]bool, error) { return map[string]bool{"opkgtun7": true}, nil }
	if err := f.Unmark(context.Background(), "OpkgTun7"); !errors.Is(err, api.ErrForeignIfaceRejected) {
		t.Fatalf("Unmark = %v, ждали отказ", err)
	}
	if got := f.settings.GetForeignInterfaces(); !slices.Equal(got, []string{"opkgtun7"}) {
		t.Fatalf("отметки = %v, ждали opkgtun7", got)
	}
}

func TestForeignUnmark_BoundErrorFailsClosed(t *testing.T) {
	f := newForeignEnv(t, nil, nil)
	if _, err := f.Mark(context.Background(), "csqtt0"); err != nil {
		t.Fatal(err)
	}
	f.boundBy = func(context.Context) (map[string]bool, error) { return nil, errors.New("router down") }
	err := f.Unmark(context.Background(), "csqtt0")
	if err == nil || errors.Is(err, api.ErrForeignIfaceRejected) {
		t.Fatalf("Unmark = %v, ждали внутреннюю ошибку", err)
	}
	if got := f.settings.GetForeignInterfaces(); !slices.Equal(got, []string{"csqtt0"}) {
		t.Fatalf("отметки = %v, ждали csqtt0", got)
	}
}

func TestForeignUnmark_Unbound(t *testing.T) {
	f := newForeignEnv(t, nil, nil)
	if _, err := f.Mark(context.Background(), "csqtt0"); err != nil {
		t.Fatal(err)
	}
	f.boundBy = func(context.Context) (map[string]bool, error) { return map[string]bool{"zt0": true}, nil }
	if err := f.Unmark(context.Background(), "csqtt0"); err != nil {
		t.Fatal(err)
	}
	if got := f.settings.GetForeignInterfaces(); len(got) != 0 {
		t.Fatalf("после снятия = %v", got)
	}
}

func TestForeignMark_AcceptsFreeAndAbsent(t *testing.T) {
	f := newForeignEnv(t, opkgtun.Taken{7: opkgtun.AnonHolder("запись NDMS OpkgTun7")}, nil)
	for _, name := range []string{"opkgtun7", "csqtt0", "csqtt0"} {
		if _, err := f.Mark(context.Background(), name); err != nil {
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
		got, err := f.Mark(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if got != "opkgtun7" { // R19: фронт выбирает ровно записанное имя
			t.Fatalf("Mark(%q) вернул %q, ждали opkgtun7", name, got)
		}
	}
	if got, err := f.Mark(context.Background(), " csqtt0 "); err != nil || got != "csqtt0" {
		t.Fatalf("Mark(csqtt0) = %q, %v", got, err)
	}
	if got := f.settings.GetForeignInterfaces(); !slices.Equal(got, []string{"opkgtun7", "csqtt0"}) {
		t.Fatalf("отметки = %v, ждали opkgtun7 и csqtt0", got)
	}
	for _, name := range []string{"OpkgTun7", "csqtt0"} {
		if err := f.Unmark(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.settings.GetForeignInterfaces(); len(got) != 0 {
		t.Fatalf("после снятия = %v", got)
	}
}

func TestForeignMark_NDMSErrorFailsClosed(t *testing.T) {
	f := newForeignEnv(t, nil, nil)
	f.ndmsNames = func(context.Context) (map[string]bool, error) { return nil, errors.New("rci down") }
	_, err := f.Mark(context.Background(), "csqtt0")
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
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if tun {
			if err := os.WriteFile(filepath.Join(dir, "tun_flags"), []byte("0x1001\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "carrier"), []byte(carrier+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("csqtt0", true, "1")
	mk("zt0", true, "0")
	mk("eth3", true, "1")                                          // известен NDMS
	mk("t2s0", true, "1")                                          // наше имя
	mk("gre0", false, "1")                                         // не TUN
	mk("ezcfg0", true, "1")                                        // служебный TUN прошивки
	mk("wg0", true, "0")                                           // userland-программа
	if err := f.settings.MarkForeignInterface("zt0"); err != nil { // уже отмечен — не кандидат
		t.Fatal(err)
	}

	got, err := f.Candidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []api.ForeignIfaceCandidate{
		{Name: "opkgtun7", Label: "csqtt", Kind: "opkgtun", Up: true},
		{Name: "csqtt0", Label: "csqtt0", Kind: "kernel", Up: true},
		{Name: "wg0", Label: "wg0", Kind: "kernel", Up: false},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("кандидаты = %+v, want %+v", got, want)
	}
}

// Стенд 5.02.A.11: `interface-name` в списке — эхо id или метка (Bridge0 →
// "Home"), настоящее имя ядра даёт только резолвер. Без него br0 проходил
// отметку.
func TestNDMSSystemNames_ResolvesLabels(t *testing.T) {
	fg := ndmsquery.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{
  "Bridge0": {"id":"Bridge0","interface-name":"Home","type":"Bridge","state":"up","link":"up"},
  "GigabitEthernet0": {"id":"GigabitEthernet0","interface-name":"GigabitEthernet0","type":"GigabitEthernet","state":"up","link":"up"}
}`)
	fg.SetPostSystemName("Bridge0", `"br0"`)
	fg.SetPostSystemName("GigabitEthernet0", `"eth2"`)
	f := newForeignEnv(t, nil, nil)
	f.ndmsNames = ndmsSystemNames(ndmsquery.NewInterfaceStore(fg, ndmsquery.NopLogger()))
	for _, name := range []string{"br0", "eth2"} {
		if _, err := f.Mark(context.Background(), name); !errors.Is(err, api.ErrForeignIfaceRejected) {
			t.Errorf("Mark(%q) = %v, ждали отказ «интерфейс роутера»", name, err)
		}
	}
	if got := f.settings.GetForeignInterfaces(); len(got) != 0 {
		t.Fatalf("отказы записали %v", got)
	}
}

// R21: существующий интерфейс ядра без tun_flags — не программа (lo, dummy0,
// tunl0…); существующий TUN, неизвестный NDMS, отмечается.
func TestForeignMark_ExistingNonTUNRejected(t *testing.T) {
	f := newForeignEnv(t, nil, nil)
	for _, n := range []string{"lo", "zt0"} {
		if err := os.MkdirAll(filepath.Join(f.sysNet, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.sysNet, "zt0", "tun_flags"), []byte("0x1001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Mark(context.Background(), "lo"); !errors.Is(err, api.ErrForeignIfaceRejected) {
		t.Fatalf("Mark(lo) = %v, ждали отказ «не TUN»", err)
	}
	if _, err := f.Mark(context.Background(), "zt0"); err != nil {
		t.Fatalf("Mark(zt0): %v", err)
	}
	if got := f.settings.GetForeignInterfaces(); !slices.Equal(got, []string{"zt0"}) {
		t.Fatalf("отметки = %v, ждали zt0", got)
	}
}
