package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
)

// orphanIfaces — сироты пула в виде, готовом для списка внешних туннелей и
// ручки удаления.
//
// Кто осиротел, решает АЛЛОКАТОР (Pool.Orphans) — тем же составом
// поставщиков, каким он выдаёт номера. Своего мнения о занятости здесь нет и
// быть не должно: расхождение составов — это ровно #891, только вместо увода
// чужого номера оно предложит пользователю снести чужой интерфейс.
//
// Адреса дочитываются здесь: строка списка показывает их пользователю, и по
// ним же считается совпадение с адресом действующего туннеля.
func orphanIfaces(pool *opkgtun.Pool, ifaces *ndmsquery.InterfaceStore) func(context.Context) ([]external.OrphanIface, error) {
	return orphanIfacesWith(pool, ifaces, false)
}

// orphanIfacesExclusive — то же под семафором выбора, для перепроверки ПЕРЕД
// сносом. Показ терпит лишнюю строку до следующего опроса, снос — нет: там
// лишняя строка означает удаление интерфейса, который в эту секунду создают.
func orphanIfacesExclusive(pool *opkgtun.Pool, ifaces *ndmsquery.InterfaceStore) func(context.Context) ([]external.OrphanIface, error) {
	return orphanIfacesWith(pool, ifaces, true)
}

// orphanListTTL — сколько живёт кэш показа.
//
// Список сирот зовётся из /tunnels/all, а его фронт опрашивает каждые 5 секунд.
// Без кэша каждый опрос заново обходит каталог туннелей (второй раз за тот же
// запрос), читает файл записей прокси и дважды проходит по интерфейсам NDMS —
// на mipsel это постоянная добавка ради списка, который меняется почти никогда.
//
// Кэшируется ТОЛЬКО показ: устаревшая строка живёт до следующего опроса и
// ничего не ломает. Перепроверка перед сносом идёт мимо кэша и под семафором.
const orphanListTTL = 15 * time.Second

func orphanIfacesWith(pool *opkgtun.Pool, ifaces *ndmsquery.InterfaceStore, exclusive bool) func(context.Context) ([]external.OrphanIface, error) {
	if pool == nil {
		return nil // проводка без пула: список пуст, ручка выключена
	}
	var (
		mu     sync.Mutex
		cached []external.OrphanIface
		at     time.Time
	)
	return func(ctx context.Context) ([]external.OrphanIface, error) {
		if !exclusive {
			mu.Lock()
			fresh := time.Since(at) < orphanListTTL && cached != nil
			out := cached
			mu.Unlock()
			if fresh {
				return out, nil
			}
		}
		read := pool.Orphans
		if exclusive {
			read = pool.OrphansExclusive
		}
		orphans, err := read(ctx)
		if err != nil {
			return nil, err
		}
		ndms := ndmsOpkgTuns(ctx, ifaces)
		out := make([]external.OrphanIface, 0, len(orphans))
		for _, o := range orphans {
			iface := fmt.Sprintf("opkgtun%d", o.Index)
			rec, inNDMS := ndms[o.Index]
			inKernel := kernelIfacePresent(iface)
			// Показываем ТОЛЬКО интерфейсы OpkgTun — то есть номера, за
			// которыми стоит настоящая сущность: запись NDMS OpkgTun<N> или
			// устройство opkgtun<N> в ядре.
			//
			// Пул считает занятость ШИРЕ имени: живой источник собирает номера
			// ещё и из awgm<N>/awg<N> (sysinfo.ListSystemInterfaces прямо
			// предупреждает, что класс имени там теряется). Для ВЫДАЧИ номера
			// это верный перебор — лишний занятый безопасен. Здесь он
			// переворачивается: номер туннеля awgm10, за которым никакого
			// opkgtun10 нет, приехал бы пользователю как «удалить opkgtun10».
			// Поэтому номер без своей сущности отбрасывается, а не гейтится по
			// версии прошивки: правило про ИМЕНА, а не про версию.
			if !inNDMS && !inKernel {
				continue
			}
			out = append(out, external.OrphanIface{
				Iface:        iface,
				NDMSName:     rec.id,
				Description:  rec.description,
				Addrs:        ifaceAddrs(iface),
				NDMSRecord:   inNDMS,
				KernelDevice: inKernel,
			})
		}
		if !exclusive {
			mu.Lock()
			cached, at = out, time.Now()
			mu.Unlock()
		}
		return out, nil
	}
}

// ndmsDescriptionsFn — описания записей OpkgTun по номеру, для списка внешних
// туннелей.
func ndmsDescriptionsFn(ifaces *ndmsquery.InterfaceStore) func(context.Context) map[int]string {
	return func(ctx context.Context) map[int]string {
		out := map[int]string{}
		for n, rec := range ndmsOpkgTuns(ctx, ifaces) {
			out[n] = rec.description
		}
		return out
	}
}

// ndmsRecord — запись интерфейса в NDMS так, как её отдал роутер.
type ndmsRecord struct {
	id          string
	description string
}

// ndmsOpkgTuns — записи OpkgTun в NDMS: номер → описание. НАЛИЧИЕ ключа значит
// «запись есть», описание при этом может быть пустым.
//
// Отказ чтения не отказывает всей проверке: описание — подсказка человеку, а не
// основание решения. Но и сиротой по одному лишь отсутствию записи никто не
// становится — вторая половина проверки смотрит в ядро.
func ndmsOpkgTuns(ctx context.Context, ifaces *ndmsquery.InterfaceStore) map[int]ndmsRecord {
	out := map[int]ndmsRecord{}
	if ifaces == nil {
		return out
	}
	all, err := ifaces.List(ctx)
	if err != nil {
		return out
	}
	for _, i := range all {
		// ТОТ ЖЕ разбор, которым занятость считал пул (ndmsHolders). Свой,
		// более строгий, давал бы записи, которую пул видит, а показ нет: номер
		// приезжал бы как «только устройство», а снос уходил бы имени, которого
		// в NDMS нет.
		n, ok := sysinfo.ExtractInterfaceNumber(strings.ToLower(i.ID))
		if !ok {
			continue
		}
		out[n] = ndmsRecord{id: i.ID, description: i.Description}
	}
	return out
}

// kernelIfacePresent — есть ли устройство с таким именем в ядре. Шов ради
// тестов: настоящие интерфейсы машины, на которой запущен тест, к делу не
// относятся.
var kernelIfacePresent = func(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

// ifaceAddrs — адреса устройства, если оно вообще есть в ядре. Отсутствие
// устройства — не ошибка: у сироты может остаться одна лишь запись NDMS,
// и адресов у неё нет.
func ifaceAddrs(name string) []string {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			out = append(out, ipNet.IP.String())
		}
	}
	return out
}
