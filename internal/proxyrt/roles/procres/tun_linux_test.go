package procres

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/netdev"
)

// stubTunDev — /sys/class/net во временном каталоге (present — устройство
// есть) и счётчик открытий /dev/net/tun; само открытие отказывает, так что
// до TUNSETIFF не доходит ни одна ветка теста.
func stubTunDev(t *testing.T, present bool) *int {
	t.Helper()
	root := t.TempDir()
	if present {
		if err := os.Mkdir(filepath.Join(root, "opkgtun7"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldRoot, oldOpen := netdev.SysClassNet, openTunDev
	netdev.SysClassNet = root
	opens := 0
	openTunDev = func() (int, error) { opens++; return -1, errors.New("stub: /dev/net/tun") }
	t.Cleanup(func() { netdev.SysClassNet, openTunDev = oldRoot, oldOpen })
	return &opens
}

// R62: устройства нет — отказ с понятной ошибкой, /dev/net/tun не
// открывается и TUNSETIFF не зовётся (он создал бы tun под записью мимо NDMS).
// Мутация: убрать проверку netdev.Absent → open вызван, красный.
func TestOpenTunFD_AbsentDevice_RefusesWithoutOpen(t *testing.T) {
	opens := stubTunDev(t, false)
	f, err := OpenTunFD("opkgtun7")
	if err == nil || f != nil {
		t.Fatalf("OpenTunFD по отсутствующему устройству: f=%v err=%v, want отказ", f, err)
	}
	if *opens != 0 {
		t.Fatalf("/dev/net/tun открыт %d раз при отсутствующем устройстве", *opens)
	}
}

// Устройство есть — идём к /dev/net/tun (штатный путь прикрепления).
// Мутация: отказ на ErrPresent (перевёрнутая проверка) → open не вызван, красный.
func TestOpenTunFD_PresentDevice_Opens(t *testing.T) {
	opens := stubTunDev(t, true)
	if _, err := OpenTunFD("opkgtun7"); err == nil {
		t.Fatal("заглушка open отказывает — ожидалась её ошибка")
	}
	if *opens != 1 {
		t.Fatalf("/dev/net/tun открыт %d раз, want 1", *opens)
	}
}
