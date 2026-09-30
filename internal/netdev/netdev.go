// Package netdev — факты о kernel-устройствах (/sys/class/net), без NDMS.
package netdev

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SysClassNet — корень, где ядро показывает сетевые устройства; тесты
// подменяют его временным каталогом.
var SysClassNet = "/sys/class/net"

// ErrPresent — устройство с этим именем есть.
var ErrPresent = errors.New("netdev: device present")

// Free — «устройства name сейчас нет в /sys/class/net». Собрать вне пакета
// нельзя (поле неэкспортируемое, литерал — сканер R18); получить — только
// Absent. Срока нет: единственные создатели opkgtunN — мы (amneziawg) и
// NDMS (tun по записи), а NDMS создаёт его лишь вместе с записью (F569).
type Free struct{ name string }

// Name — имя устройства, отсутствие которого доказано.
func (f Free) Name() string { return f.name }

// Absent доказывает, что устройства name нет. Есть — ErrPresent с именем;
// stat не ответил иначе, чем «нет такого», — ошибка: «не знаем» ≠ «нет».
func Absent(name string) (Free, error) {
	_, err := os.Stat(filepath.Join(SysClassNet, name))
	switch {
	case err == nil:
		return Free{}, fmt.Errorf("%s: %w", name, ErrPresent)
	case errors.Is(err, os.ErrNotExist):
		return Free{name: name}, nil
	default:
		return Free{}, fmt.Errorf("netdev %s: %w", name, err)
	}
}
