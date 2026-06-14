package router

import (
	"context"
	"errors"

	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
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

// OpkgTunIndexLister перечисляет занятые индексы OpkgTun из источника NDMS.
//
// Узкий интерфейс намеренно объявлен в router, а не тянет конкретные типы
// internal/ndms: router декаплится от ndms через consumer-owned контракты (DIP),
// как и WANInterfaceLister/IngressResolver. Реальный union (kernel /sys +
// NDMS-имена) строит адаптер в cmd/awg-manager поверх UnionOpkgTunIndices
// (Task 1C.2); здесь — только контракт.
type OpkgTunIndexLister interface {
	LiveOpkgTunIndices(ctx context.Context) (map[int]bool, error)
}

// UnionOpkgTunIndices — pure-ядро union занятых индексов OpkgTun: объединяет
// kernel-числа из /sys/class/net (sysinfo.ListSystemInterfaces) с индексами,
// извлечёнными из NDMS system-имён интерфейсов. Вынесено отдельно от адаптера,
// чтобы покрыть тестом без /sys и без NDMS.
//
// Имена из NDMS прогоняются через sysinfo.ExtractInterfaceNumber, которая
// заякорена (^opkgtun\d+$, ^awgm\d+$, ^awg\d+$): nwg2/Wireguard0/br0 не матчатся
// и в union не попадут. opkgtun — это именно наш диапазон; awg/awgm семантически
// не OpkgTun, но их попадание лишь over-count'ит (займём слот зря, не баг) —
// ложного освобождения занятого индекса не происходит.
func UnionOpkgTunIndices(sysNums []int, ndmsNames []string) map[int]bool {
	live := make(map[int]bool)
	for _, n := range sysNums {
		live[n] = true
	}
	for _, name := range ndmsNames {
		if num, ok := sysinfo.ExtractInterfaceNumber(name); ok {
			live[num] = true
		}
	}
	return live
}
