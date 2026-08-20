package router

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// reservedPortsSysctl — список портов, которые ядро НЕ выдаёт эфемерным
// (исходящим) сокетам.
const reservedPortsSysctl = "/proc/sys/net/ipv4/ip_local_reserved_ports"

// routerListenPorts перечисляет фиксированные порты инбаундов sb-router.
// Источник — те же константы, что рендерят правила iptables и конфиг
// sing-box, поэтому список не может разъехаться с реальными слушателями.
func routerListenPorts() []int {
	ports := []int{TPROXYPort, RedirectPort}
	for slot := 0; slot < MaxQoSClasses; slot++ {
		tp, rp := QoSClassPorts(slot)
		ports = append(ports, tp, rp)
	}
	return ports
}

// ReserveListenPorts убирает порты инбаундов sb-router из эфемерного пула
// ядра.
//
// На Keenetic ip_local_port_range = 49000-61001 (замер на роутере), то есть
// 51271/51272 и QoS-диапазоны лежат ВНУТРИ него. Пока sing-box слушает, порт
// защищён; но переключение режима с tun-инбаундом требует полного рестарта
// процесса, и в это окно ядро вправе отдать порт локальным концом любого
// исходящего соединения. Тогда старт падает с «bind: address already in use»,
// а переключение режима откатывается (issue #762).
//
// Best-effort: отсутствующий sysctl (ядро без опции) — не ошибка.
func ReserveListenPorts() error { return reserveListenPortsAt(reservedPortsSysctl) }

func reserveListenPortsAt(path string) error {
	ports := routerListenPorts()
	cur, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if reservedSpecCovers(string(cur), ports) {
		return nil
	}
	// Дописываем к текущему значению, а не заменяем: резерв могли ставить
	// не мы. Ядро само схлопывает пересечения и дубли в диапазоны
	// (проверено на роутере).
	parts := make([]string, 0, len(ports)+1)
	if s := strings.TrimSpace(string(cur)); s != "" {
		parts = append(parts, s)
	}
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(p))
	}
	if err := os.WriteFile(path, []byte(strings.Join(parts, ",")), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// reservedSpecCovers сообщает, покрыты ли все ports значением sysctl —
// списком через запятую из одиночных портов и диапазонов «от-до».
func reservedSpecCovers(spec string, ports []int) bool {
	reserved := make(map[int]bool)
	for _, item := range strings.Split(spec, ",") {
		lo, hi, ok := parseReservedItem(item)
		if !ok {
			continue
		}
		for p := lo; p <= hi; p++ {
			reserved[p] = true
		}
	}
	for _, p := range ports {
		if !reserved[p] {
			return false
		}
	}
	return true
}

func parseReservedItem(item string) (lo, hi int, ok bool) {
	item = strings.TrimSpace(item)
	if item == "" {
		return 0, 0, false
	}
	from, to, isRange := strings.Cut(item, "-")
	lo, err := strconv.Atoi(strings.TrimSpace(from))
	if err != nil {
		return 0, 0, false
	}
	if !isRange {
		return lo, lo, true
	}
	hi, err = strconv.Atoi(strings.TrimSpace(to))
	if err != nil || hi < lo {
		return 0, 0, false
	}
	return lo, hi, true
}
