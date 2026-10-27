package router

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRouterListenPortsCoversEveryInbound(t *testing.T) {
	ports := routerListenPorts()
	want := map[int]bool{TPROXYPort: false, RedirectPort: false, ruleSetFetchPortRouter: false, ruleSetFetchPortFakeIP: false}
	for slot := 0; slot < MaxQoSClasses; slot++ {
		tp, rp := QoSClassPorts(slot)
		want[tp], want[rp] = false, false
	}
	for _, p := range ports {
		if _, ok := want[p]; !ok {
			t.Errorf("лишний порт %d", p)
		}
		want[p] = true
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("порт %d не попал в список резервирования", p)
		}
	}
}

// Порты v6-слоя резервируются отдельно и только при включённом перехвате
// IPv6: в безусловный список они попадать не должны.
func TestV6ListenPortsCoverEveryInboundAndStayOutOfBootList(t *testing.T) {
	want := map[int]bool{Redirect6Port: false, TProxy6Port: false}
	for n := 0; n < maxV6Bridges; n++ {
		want[TProxy6PortBase+n] = false
	}
	for slot := 0; slot < MaxQoSClasses; slot++ {
		tp, rp := QoSClassPorts6(slot)
		want[tp], want[rp] = false, false
	}
	for _, p := range v6ListenPorts() {
		if _, ok := want[p]; !ok {
			t.Errorf("лишний порт %d", p)
		}
		want[p] = true
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("порт %d не попал в список резервирования v6", p)
		}
	}
	boot := map[int]bool{}
	for _, p := range routerListenPorts() {
		boot[p] = true
	}
	for p := range want {
		if boot[p] {
			t.Errorf("порт v6-слоя %d резервируется безусловно", p)
		}
	}
}

func TestReservePortsAtAppendsV6Ports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ip_local_reserved_ports")
	if err := os.WriteFile(path, []byte("51271-51272\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reservePortsAt(path, v6ListenPorts()); err != nil {
		t.Fatalf("резервирование: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reservedSpecCovers(string(got), v6ListenPorts()) {
		t.Errorf("порты v6 не покрыты: %q", got)
	}
	if !reservedSpecCovers(string(got), []int{51271, 51272}) {
		t.Errorf("прежний резерв потерян: %q", got)
	}
}

func TestReservedSpecCovers(t *testing.T) {
	cases := []struct {
		name  string
		spec  string
		ports []int
		want  bool
	}{
		{"пусто", "", []int{51271}, false},
		{"точное совпадение диапазоном", "51271-51272", []int{51271, 51272}, true},
		{"одиночный порт", "51271", []int{51271}, true},
		{"часть портов вне", "51271-51272", []int{51272, 51281}, false},
		{"чужой резерв плюс наш", "1000-1010,51271-51272,51281-51288", []int{51271, 51288}, true},
		{"мусор не считается покрытием", "abc,,-", []int{51271}, false},
		{"граница диапазона", "51281-51288", []int{51289}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reservedSpecCovers(c.spec, c.ports); got != c.want {
				t.Errorf("reservedSpecCovers(%q, %v) = %v, ожидалось %v", c.spec, c.ports, got, c.want)
			}
		})
	}
}

func TestReserveListenPortsWritesMergedSpec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ip_local_reserved_ports")
	if err := os.WriteFile(path, []byte("1000-1010\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reserveListenPortsAt(path); err != nil {
		t.Fatalf("резервирование: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reservedSpecCovers(string(got), routerListenPorts()) {
		t.Errorf("после записи порты не покрыты: %q", got)
	}
	if !reservedSpecCovers(string(got), []int{1000, 1010}) {
		t.Errorf("чужой резерв потерян: %q", got)
	}
}

func TestReserveListenPortsSkipsWriteWhenCovered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ip_local_reserved_ports")
	covered := "51271-51272,51275-51276,51281-51288,51301-51308\n"
	if err := os.WriteFile(path, []byte(covered), 0o644); err != nil {
		t.Fatal(err)
	}
	// Файл только для чтения: повторный старт не должен в него писать.
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := reserveListenPortsAt(path); err != nil {
		t.Fatalf("повторный вызов не должен ни писать, ни падать: %v", err)
	}
	// Под root запрет записи не действует, и проверка выше ничего бы не
	// поймала — поэтому сверяем и содержимое: файл обязан остаться байт в байт.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != covered {
		t.Errorf("покрытый резерв переписан: %q, было %q", got, covered)
	}
}

// Отсутствующий sysctl (ядро без опции, не-Linux) — не ошибка запуска.
func TestReserveListenPortsMissingSysctl(t *testing.T) {
	if err := reserveListenPortsAt(filepath.Join(t.TempDir(), "нет-такого")); err != nil {
		t.Errorf("отсутствие файла должно быть тихим, получено: %v", err)
	}
}
