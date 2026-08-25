package router

import (
	"context"
	"errors"
)

// maxFakeIPIndex — верхняя граница диапазона индексов OpkgTun, который
// fakeip-tun сканирует под свой интерфейс (0..9 включительно).
const maxFakeIPIndex = 9

// ErrFakeIPIndexExhausted возвращается, когда в диапазоне 0..maxFakeIPIndex
// не осталось свободного индекса OpkgTun.
var ErrFakeIPIndexExhausted = errors.New("нет свободного OpkgTun-индекса в 0..9")

// allocateFakeIPIndex возвращает низший свободный индекс OpkgTun в диапазоне
// 0..maxFakeIPIndex, отсутствующий в live, иначе ErrFakeIPIndexExhausted.
//
// «Наш» интерфейс определяется НЕ по номеру: эвристика sysinfo (interfaces.go:49)
// читает 0..9 как external, а awg-manager — 100+. Поэтому caller персистит
// собственный выбранный индекс отдельно (по персисту, не по номеру). При этом
// ещё живущий «свой» iface корректно попадает в live и читается как занятый —
// повторная аллокация его не выдаст.
func allocateFakeIPIndex(live map[int]bool) (int, error) {
	for i := 0; i <= maxFakeIPIndex; i++ {
		if !live[i] {
			return i, nil
		}
	}
	return 0, ErrFakeIPIndexExhausted
}

// OpkgTunIndexLister перечисляет занятые индексы OpkgTun по kernel-именам
// из /sys/class/net.
//
// Узкий интерфейс намеренно объявлен в router, а не тянет конкретные типы
// internal/ndms: router декаплится от ndms через consumer-owned контракты (DIP),
// как и WANInterfaceLister/IngressResolver; реализация живёт в cmd/awg-manager.
type OpkgTunIndexLister interface {
	LiveOpkgTunIndices(ctx context.Context) (map[int]bool, error)
}
