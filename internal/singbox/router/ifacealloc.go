package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
)

// maxFakeIPIndex — верхняя граница диапазона индексов OpkgTun, который
// fakeip-tun сканирует под свой интерфейс (0..9 включительно).
const maxFakeIPIndex = 9

// ErrFakeIPIndexExhausted возвращается, когда в диапазоне 0..maxFakeIPIndex
// не осталось свободного индекса OpkgTun.
var ErrFakeIPIndexExhausted = errors.New("нет свободного OpkgTun-индекса в 0..9")

// allocOccupancy — занятость для ВЫДАЧИ номера: живая половина плюс пины
// чужих владельцев. Отдельна от live намеренно: та же карта отвечает ещё на
// два вопроса — «жив ли мой прежний интерфейс» и «переиспользовать ли свой
// удержанный номер», — и подмешивание туда чужих пинов сломало бы оба.
//
// Fail-closed: недосчёт занятых номеров — единственное направление ошибки,
// приводящее к коллизии.
func allocOccupancy(ctx context.Context, live map[int]bool, pins func(context.Context) (map[int]bool, error)) (map[int]bool, error) {
	if pins == nil {
		return live, nil
	}
	held, err := pins(ctx)
	if err != nil {
		return nil, fmt.Errorf("пины OpkgTun: %w", err)
	}
	out := make(map[int]bool, len(live)+len(held))
	for i := range live {
		out[i] = true
	}
	for i := range held {
		out[i] = true
	}
	return out, nil
}

// allocateFakeIPIndex возвращает низший свободный индекс OpkgTun в диапазоне
// 0..maxFakeIPIndex, отсутствующий в live, иначе ErrFakeIPIndexExhausted.
//
// Диапазоны OpkgTun разделены между подсистемами, у каждой свой аллокатор:
//
//	0..9   — режимы роутера (здесь), maxFakeIPIndex
//	10..16 — AWG-туннели, storage.os5MinIndex/os5MaxIndex
//	17..49 — прокси-инстансы на arm/arm64, roles.OpkgIndexMin/Max
//
// Раскладка статична ТОЛЬКО на arm/arm64. На mips/mipsel прошивка отвергает
// индексы от 17 («index 17 is too large»; рабочий максимум — OpkgTun16,
// стенд 25.08), поэтому статического поддиапазона под прокси там нет: они
// берут ПЕРВЫЙ СВОБОДНЫЙ из 0..15 (roles.OpkgIndexRange, shared=true) и пул
// пересекается с обоими диапазонами выше. Занятость поэтому считается по всем четырём
// владельцам сразу — cmd/awg-manager.opkgOccupancyAllOwners (F16).
//
// NativeWG в этой нумерации не участвует — OpkgTun он не создаёт (его туннели
// живут как Wireguard<N>), поэтому его собственная нумерация awg20+ сюда не
// вторгается.
//
// Край: legacy-отображение awg0 → OpkgTun0 (tunnel.NewNames) кладёт туннели
// старых установок ВНУТРЬ этого диапазона. Новые туннели туда не попадут
// (nextAvailableID на OS5 начинает с 10), но на мигрировавшей установке
// незапущенный awg3 выглядит свободным номером. Порчи чужого интерфейса это
// не даёт — владение проверяется по описанию (provenForeignOpkgTun), — но
// оба владельца захотят одно имя.
//
// «Наш» интерфейс определяется НЕ по номеру, а по персисту и NDMS-описанию:
// caller персистит собственный выбранный индекс отдельно. При этом ещё живущий
// «свой» iface корректно попадает в live и читается как занятый — повторная
// аллокация его не выдаст.
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

// UnionOpkgTunIndices — pure-ядро набора занятых индексов OpkgTun: собирает
// kernel-числа из /sys/class/net (sysinfo.ListSystemInterfaces) и индексы,
// извлечённые из имён интерфейсов NDMS. Вынесено отдельно от адаптера, чтобы
// покрыть тестом без /sys и без NDMS.
//
// Оба источника опциональны, и прод-адаптер зовёт её ДВУМЯ половинами:
// живые интерфейсы и записи NDMS отвечают на разные вопросы и складываются не
// здесь, а в занятости для выдачи номера (storage.OpkgTunOccupancy). Так что
// «union» в имени — про способность сложить два источника, а не про то, что
// каждый вызов их объединяет.
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
