package main

import (
	"context"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
)

// opkgTunOwners — пять поставщиков занятости пула OpkgTun и ЕДИНСТВЕННОЕ место,
// где собирается их состав.
//
// Состав — контракт для всех, кто выдаёт номера, а не деталь одного
// вызывающего: на mips/mipsel пул общий, и поставщик, выпавший у кого-то
// одного, отдаёт ему чужой занятый номер как свободный. Выпадение при этом не
// ломает ни сборку, ни один прогон — коллизия всплывает интерфейсом, который
// увели у соседней подсистемы. Поэтому состав собирается здесь и раздаётся
// готовым, а не составляется заново на каждом месте вызова (так было до #891:
// три места, два из них строили одно и то же дважды).
//
// Записи NDMS — ОТДЕЛЬНЫЙ поставщик, а не половина живого: после
// `ip link del opkgtunN` запись живёт дальше со state error, устройства нет.
// Номер занят, интерфейс мёртв, и одна карта на оба вопроса врёт.
type opkgTunOwners struct {
	tunnels  opkgtun.Source
	settings opkgtun.Source
	ndms     opkgtun.Source
	proxy    opkgtun.Source
	live     opkgtun.Source
}

// newOpkgTunOwners. Две половины, зависящие от роутера, приходят готовыми
// источниками: тестам нужно подставить их, оставив остальные три настоящими, —
// а состав обязан оставаться ОДИН, иначе проверенным окажется не он.
func newOpkgTunOwners(ndms, live opkgtun.Source, awg *storage.AWGTunnelStore,
	settings *storage.SettingsStore, store *instancestore.Store,
) opkgTunOwners {
	return opkgTunOwners{
		tunnels:  opkgtun.Source{Name: "записи туннелей", Read: tunnelHolders(awg)},
		settings: opkgtun.Source{Name: "запись режима роутера", Read: routerModeHolders(settings)},
		proxy:    opkgtun.Source{Name: "записи прокси", Read: proxyHolders(store)},
		ndms:     ndms,
		live:     live,
	}
}

// ndmsSource и liveSource — прод-половины, зависящие от роутера.
func ndmsSource(a *routerOpkgTunIndexAdapter) opkgtun.Source {
	return opkgtun.Source{Name: "записи NDMS", Read: ndmsHolders(a)}
}

func liveSource(a *routerOpkgTunIndexAdapter) opkgtun.Source {
	return opkgtun.Source{Name: "живые интерфейсы", Read: liveHolders(a)}
}

// all — состав для тех, кто выдаёт номера: все пятеро, и он ОДИН на всех.
//
// Вычитать из него собственную запись просителя не нужно: владелец узнаёт свой
// номер по ключу держателя, и пул отдаёт его пину по совпадению ключа. Прежде
// у режимов роутера был отдельный состав «без своей записи» — приём, который
// ломался на handover: там номер принадлежит ДРУГОМУ режиму, и вычитать было
// нечего.
//
// Порядок значим: заявленные идут раньше анонимов (живое устройство, запись
// NDMS), потому что при слиянии заявленный бьёт анонима, а два заявленных на
// одном номере дают конфликт, и в занятости остаётся первый.
func (o opkgTunOwners) all() []opkgtun.Source {
	return []opkgtun.Source{o.tunnels, o.settings, o.proxy, o.ndms, o.live}
}

// tunnelHolders — записи AWG-туннелей. Перечисление СТРОГОЕ: прощающее унесло
// бы временно нечитаемую запись в карантин и молча освободило её номер.
//
// Системных туннелей (NativeWG) здесь нет и быть не может: номер OpkgTun они
// не занимают вовсе (AWGTunnel.OpkgTunIndex), их awg<N> занимает пространство
// ИДЕНТИФИКАТОРОВ. Это другое множество, и просителю оно приходит отдельным
// вето, а не занятостью.
func tunnelHolders(awg *storage.AWGTunnelStore) func(context.Context) (opkgtun.Taken, error) {
	return func(context.Context) (opkgtun.Taken, error) {
		tunnels, err := awg.ListStrict()
		if err != nil {
			return nil, err
		}
		out := make(opkgtun.Taken, len(tunnels))
		for _, t := range tunnels {
			idx, ok := t.OpkgTunIndex()
			if !ok {
				continue
			}
			out[idx] = opkgtun.TunnelHolder(t.ID, t.Name)
		}
		return out, nil
	}
}

// routerModeHolders — удерживающая запись режима роутера. Гейт по НАЛИЧИЮ
// записи, а не по Provisioned: удержание номера ради permit'ов пользователя —
// это как раз Provisioned=false при непустой записи. Нулевой номер валиден:
// это начало диапазона режимов роутера.
//
// Узкий геттер, а не Snapshot(): тот маршалит ВСЕ настройки ради двух полей, а
// зовут этот источник на каждой выдаче номера, под общим семафором.
//
// Читается КЭШ настроек, а не файл: прежний поставщик маршалил ВСЕ настройки
// снимком ради двух полей. Разойтись кэш и файл могут только при внешнем писателе
// settings.json мимо стора — это восстановление из бэкапа, после которого
// демон перезапускается, так что окно закрыто.
func routerModeHolders(settings *storage.SettingsStore) func(context.Context) (opkgtun.Taken, error) {
	return func(context.Context) (opkgtun.Taken, error) {
		st, err := settings.OpkgTunStateSnapshot()
		if err != nil {
			return nil, err
		}
		if st == nil {
			return opkgtun.Taken{}, nil
		}
		return opkgtun.Taken{st.Index: opkgtun.RouterModeHolder(st.Mode)}, nil
	}
}

// proxyHolders — записи прокси-инстансов. У сервера две половины на одной
// записи, и каждая держит свой номер, поэтому ключ владельца — запись плюс
// поле; у клиента поле пустое и ключ голый.
func proxyHolders(store *instancestore.Store) func(context.Context) (opkgtun.Taken, error) {
	return func(context.Context) (opkgtun.Taken, error) {
		st, err := store.Load()
		if err != nil {
			return nil, err
		}
		out := opkgtun.Taken{}
		for _, rec := range st.Records {
			for _, half := range proxyRecordIfaces(rec) {
				idx, ok := opkgtun.IndexOf(half.iface)
				if !ok {
					continue
				}
				out[idx] = opkgtun.ProxyHolder(rec.Key(), half.field, rec.Name)
			}
		}
		return out, nil
	}
}

// ndmsHolders — записи интерфейсов NDMS. Держатель безключевой: запись — след
// владельца, а не владелец, и назвать его точнее нечем. Описание в имени
// единственное, по чему пользователь опознает чужой интерфейс: имена NDMS у
// всех одинаковой формы.
//
// НАЗВАННОЕ ИСКЛЮЧЕНИЕ из контракта Source «читай источник, а не кэш»: читается
// кэш интерфейсов. Первый фетч у него fail-closed, дальше отдаётся карта в
// памяти — при пропущенной инвалидации список вернётся неполным БЕЗ ошибки, то
// есть недосчёт занятых молча. Единственная альтернатива — RCI на каждую
// выдачу номера под общим семафором; остаточный риск принят.
func ndmsHolders(a *routerOpkgTunIndexAdapter) func(context.Context) (opkgtun.Taken, error) {
	return func(ctx context.Context) (opkgtun.Taken, error) {
		all, err := a.store.List(ctx)
		if err != nil {
			return nil, err
		}
		out := opkgtun.Taken{}
		for _, i := range all {
			// Разбор шире пула (ExtractInterfaceNumber принимает ещё awgN и
			// awgmN) и оставлен таким намеренно: сузить значит пометить МЕНЬШЕ
			// занятых, а это единственное направление ошибки, приводящее к
			// коллизии. Достижимость лишнего класса не проверялась: ID у NDMS
			// это класс плюс индекс, и классов «Awg»/«Awgm» у прошивки, судя по
			// всему, нет (F315).
			num, ok := sysinfo.ExtractInterfaceNumber(strings.ToLower(i.ID))
			if !ok {
				continue
			}
			name := "запись NDMS " + i.ID
			if i.Description != "" {
				name += " («" + i.Description + "»)"
			}
			out[num] = opkgtun.AnonHolder(name)
		}
		return out, nil
	}
}

// liveHolders — устройства, существующие в ядре прямо сейчас. Тоже безключевой
// держатель: живая половина знает только номер.
func liveHolders(a *routerOpkgTunIndexAdapter) func(context.Context) (opkgtun.Taken, error) {
	return func(ctx context.Context) (opkgtun.Taken, error) {
		live, err := a.LiveOpkgTunIndices(ctx)
		if err != nil {
			return nil, err
		}
		out := make(opkgtun.Taken, len(live))
		for idx := range live {
			out[idx] = opkgtun.LiveHolder(idx)
		}
		return out, nil
	}
}
