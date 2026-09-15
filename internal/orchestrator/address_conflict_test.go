package orchestrator

import (
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// Проверяется РАЗВЕДЕНИЕ severity по состоянию чужого интерфейса. Слить обе
// ветки в одну — регресс в любую сторону: вверх (всё отказ) роняет старт
// туннеля, который до обновления работал, вниз (всё пропустить) возвращает
// невидимый конфликт адресов, из-за которого сирота opkgtun10 с адресом
// живого opkgtun13 пролезал мимо проверки.
func TestCheckSystemAddressConflict_SeverityByInterfaceState(t *testing.T) {
	cases := []struct {
		name      string
		ifaces    []hostIface
		exclude   []string
		ipv4      string
		ipv6      string
		wantErr   bool
		wantWarn  bool
		wantNamed string
	}{
		{
			name:   "свободный адрес",
			ifaces: []hostIface{{Name: "opkgtun10", Up: false, Addrs: []string{"10.8.1.3"}}},
			ipv4:   "10.8.1.9",
		},
		{
			name:      "поднятый чужой интерфейс — отказ",
			ifaces:    []hostIface{{Name: "opkgtun13", Up: true, Addrs: []string{"10.8.1.3"}}},
			ipv4:      "10.8.1.3",
			wantErr:   true,
			wantNamed: "opkgtun13",
		},
		{
			name:      "погашенный чужой интерфейс — предупреждение, старт идёт",
			ifaces:    []hostIface{{Name: "opkgtun10", Up: false, Addrs: []string{"10.8.1.3"}}},
			ipv4:      "10.8.1.3",
			wantWarn:  true,
			wantNamed: "opkgtun10",
		},
		{
			name:    "свой интерфейс исключён даже поднятым",
			ifaces:  []hostIface{{Name: "opkgtun13", Up: true, Addrs: []string{"10.8.1.3"}}},
			exclude: []string{"opkgtun13"},
			ipv4:    "10.8.1.3",
		},
		{
			name:      "ipv6 на погашенном — тоже предупреждение",
			ifaces:    []hostIface{{Name: "opkgtun10", Up: false, Addrs: []string{"fd00::2"}}},
			ipv6:      "fd00::2",
			wantWarn:  true,
			wantNamed: "opkgtun10",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prev := listInterfaces
			listInterfaces = func() ([]hostIface, error) { return c.ifaces, nil }
			t.Cleanup(func() { listInterfaces = prev })

			warnings, err := checkSystemAddressConflict(c.ipv4, c.ipv6, c.exclude)

			if c.wantErr {
				if !errors.Is(err, tunnel.ErrAddressInUse) {
					t.Fatalf("err = %v, ожидался ErrAddressInUse", err)
				}
				if !strings.Contains(err.Error(), c.wantNamed) {
					t.Errorf("отказ не называет интерфейс %q: %v", c.wantNamed, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданный отказ: %v", err)
			}
			if c.wantWarn {
				if len(warnings) != 1 {
					t.Fatalf("warnings = %v, ожидалось ровно одно", warnings)
				}
				if !strings.Contains(warnings[0], c.wantNamed) {
					t.Errorf("предупреждение не называет интерфейс %q: %q", c.wantNamed, warnings[0])
				}
				return
			}
			if len(warnings) != 0 {
				t.Errorf("warnings = %v, ожидалось пусто", warnings)
			}
		})
	}
}
