// Package opkgtun — единый пул номеров интерфейсов OpkgTun.
//
// Пул делят четыре подсистемы: режимы роутера (fakeip-tun, policy-tun),
// kernel-туннели, инстансы прокси и записи NDMS. До #891 номер выбирала
// каждая сама, по своему окну и своей копии карты пула, — окна разошлись с
// прошивкой, а между чтением занятости и записью на диск зияло окно гонки.
//
// Здесь выбор атомарен: Reserve читает занятость и выбирает номер, не выпуская
// внутренний семафор, и держит выданное до Close вызывающего — то есть до
// конца его записи. Занятость пул собирает сам, из Source'ов, полученных при
// сборке: иначе очередной владелец опять посчитал бы её по-своему.
//
// Каноническая карта пула — map.go этого же пакета (MinIndex, Ceiling): копий
// у неё быть не должно, #891 родился ровно из их расхождения.
package opkgtun

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Digits — число из строки, состоящей ТОЛЬКО из ASCII-цифр.
//
// Не голый strconv.Atoi по хвосту имени: тот принимает "+5" и "-5", и запись
// с идентификатором "awg-5" уехала бы в занятость номером −5 (ручка создания
// такой идентификатор пропускает). Не unicode.IsDigit: он шире, чем принимает
// Atoi, — "٥" прошёл бы предикат и упал на конверсии. Ведущие нули не
// отсеиваются: "awg007" — тот же 7, потому что имя интерфейса строится по
// тому же числу. Atoi остаётся ради переполнения.
func Digits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// IndexOf — номер из имени интерфейса, в любом из двух написаний: NDMS зовёт
// его OpkgTun12, ядро (sing-box, /sys, ip) — opkgtun12. Для СКАНЕРОВ, которые
// читают имя оттуда, где оно уже есть.
func IndexOf(iface string) (int, bool) {
	for _, p := range [...]string{"OpkgTun", "opkgtun"} {
		if d, ok := strings.CutPrefix(iface, p); ok {
			return Digits(d)
		}
	}
	return 0, false
}

// NDMSIndexOf — то же, но ТОЛЬКО для написания NDMS.
//
// Строже IndexOf намеренно, и разница не косметическая: этим разбором читается
// ПОЛЕ КОНФИГА, объявленное как NDMS-имя. Строчное написание в нём — мусор
// (правка руками, чужая версия), и принять его значит собрать имя интерфейса
// из мусора и закрепить за инстансом номер, которого он не занимал. Там, где
// имя уже прочитано с роутера, такой строгости не нужно — см. IndexOf.
func NDMSIndexOf(name string) (int, bool) {
	d, ok := strings.CutPrefix(name, "OpkgTun")
	if !ok {
		return 0, false
	}
	return Digits(d)
}

// kind — класс владельца. Нужен отказу: свои номера он считает счётчиком, а
// чужие называет поимённо, и «свой» здесь — из того же класса.
type kind uint8

const (
	kindAnon kind = iota
	kindTunnel
	kindSystemTunnel
	kindProxy
	kindRouterMode
)

func (k kind) String() string {
	switch k {
	case kindTunnel:
		return "туннель"
	case kindSystemTunnel:
		return "системный туннель"
	case kindProxy:
		return "прокси"
	case kindRouterMode:
		return "режим роутера"
	default:
		return "прочее"
	}
}

// Holder — держатель номера: чем он занят и как назвать это человеку.
//
// Поля закрыты, строится конструкторами — ключ владельца обязан вычисляться
// ОДНОЙ функцией и у поставщика занятости, и у просителя, иначе владелец не
// узнает собственный пин и уедет с него, оборвав permit'ы пользователя.
//
// Пустой ключ — аноним: номер занимает, но не совпадает ни с кем, включая
// себя. Так выглядят живое устройство в ядре и запись NDMS: это след
// владельца, а не владелец.
type Holder struct {
	k    string
	name string
	kind kind
}

// String — готовая строка для человека. Формат задан конструктором: имя
// держателя видит пользователь в совете «освободите номер», и собирать его
// на каждом вызывающем значит разойтись формулировками.
func (h Holder) String() string { return h.name }

// sameOwner — «это тот же владелец». Пустой ключ не равен ничему, в том числе
// другому пустому: два анонима на одном номере — не один владелец.
func (h Holder) sameOwner(o Holder) bool { return h.k != "" && h.k == o.k }

func (h Holder) sameKind(o Holder) bool { return h.kind == o.kind }

// anonymous — «ключа нет». НЕ синоним «можно безопасно отобрать»: отобрать
// у анонима вправе только тот, кто доказал владение сам (режим роутера — по
// описанию интерфейса, посев — по своему старому конфигу).
func (h Holder) anonymous() bool { return h.k == "" }

// Конструкторы не паникуют: их зовут в цикле по записям стора, а запись
// приходит с диска и может быть любой.

// TunnelHolder — kernel-туннель. Пустой id — проситель, которому номер ещё не
// выдан («новичок»): ключа у него нет, пин ему невыразим.
func TunnelHolder(id, name string) Holder {
	return Holder{k: id, name: describe(kindTunnel, id, name), kind: kindTunnel}
}

// SystemTunnelHolder — NativeWG. Класс назван отдельно потому, что системные
// туннели лежат на ДРУГОЙ странице UI: без этого пользователь пойдёт искать
// держателя не туда.
func SystemTunnelHolder(id, name string) Holder {
	return Holder{k: id, name: describe(kindSystemTunnel, id, name), kind: kindSystemTunnel}
}

// ProxyHolder — инстанс прокси. У сервера две половины на одной записи, и
// каждая держит свой номер, поэтому ключ — запись плюс поле: "wg", "raw" или
// пустое (единственная половина клиента). Пустое поле даёт ГОЛЫЙ ключ записи,
// а не "ключ/" — под голым ключом инстанс освобождает пины.
func ProxyHolder(recKey, field, name string) Holder {
	k := recKey
	if field != "" {
		k = recKey + "/" + field
	}
	if name == "" {
		name = recKey
	}
	return Holder{k: k, name: "прокси " + name, kind: kindProxy}
}

// RouterModeHolder — fakeip-tun или policy-tun. Ключ ОДИН на оба режима:
// одновременно работает только один, а на смене режима новый должен узнавать
// номер старого своим — иначе handover уехал бы на другой номер и оборвал
// permit'ы в политиках.
func RouterModeHolder(mode string) Holder {
	name := kindRouterMode.String()
	if mode != "" {
		name += " " + mode
	}
	return Holder{k: "router-mode", name: name, kind: kindRouterMode}
}

// AnonHolder — держатель без ключа: живое устройство в ядре, запись NDMS.
// Занятость знает только номер, назвать точнее нечем; строку собирает
// вызывающий, потому что знает, что именно он увидел, — кроме живого
// устройства, у которого видеть нечего кроме номера (см. LiveHolder).
func AnonHolder(name string) Holder {
	return Holder{name: name, kind: kindAnon}
}

// LiveHolder — держатель, за которым стоит ТОЛЬКО живое устройство в ядре и
// ничья запись.
//
// Отдельный конструктор, а не AnonHolder со строкой на месте вызова: строку
// видит пользователь в совете «освободите номер», собрать её точнее номера
// нечем, и три поставщика, собирающие её каждый у себя, разойдутся
// формулировками. Прежде она жила в internal/storage — там, где к ней уже
// ничего не относилось.
func LiveHolder(idx int) Holder {
	return AnonHolder(fmt.Sprintf("живой интерфейс opkgtun%d", idx))
}

func describe(k kind, id, name string) string {
	switch {
	case name != "":
		return k.String() + " «" + name + "»"
	case id != "":
		return k.String() + " " + id
	default:
		return k.String()
	}
}

// Taken — занятость: номер → держатель. Занято = ключ в карте ЕСТЬ; держателей
// с пустым именем карта не содержит. Пустая карта означает «занятых нет», а не
// «не смотрели»: отличать обязан тот, кто её строит.
type Taken map[int]Holder

// Conflict — один номер заявлен двумя владельцами. Стороны равноправны: кто
// прав, пул не знает, поэтому номер он не отдаёт никому, а конфликт отдаёт
// вызывающему в журнал.
type Conflict struct {
	Index int
	A, B  Holder
}

type Conflicts []Conflict

func (cs Conflicts) String() string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("OpkgTun%d: %s и %s", c.Index, c.A, c.B))
	}
	return strings.Join(parts, "; ")
}

// Source — поставщик занятости. Имя обязательно: оно попадает в ошибку, и
// «занятость не собрана» без имени источника нечинимо.
//
// Ошибка НЕ равна «свободно»: недосчёт занятых — единственное направление
// ошибки, приводящее к коллизии, поэтому отказ любого источника отказывает
// всему выбору.
//
// Read обязан читать ИСТОЧНИК, а не кэш поверх него. Два исключения названы
// там, где они проводятся: занятость NDMS читается через кэш интерфейсов
// (остаточный риск), а настройки — через снимок, который сам ничего не пишет.
//
// Тип-функция, а не интерфейс: ровно один метод, и заводить интерфейс ради
// него значит потребовать от каждого поставщика именованный тип.
type Source struct {
	Name string
	Read func(ctx context.Context) (Taken, error)
}

// occupancy — ВСЕ держатели каждого номера, а не один.
//
// Схлопывание в одного держателя теряет ровно то, на чём стоит строгий пин.
// Заявленный бьёт анонима при ПОКАЗЕ (см. holder), поэтому чужая запись NDMS, лежащая под
// собственной записью просителя, из занятости ИСЧЕЗАЕТ, и строгий пин честится
// там, где обязан отказать. Голый пул этого не показывает — там номер держит
// один аноним, — а боевой состав содержит обоих всегда: собственная запись
// просителя в нём есть по построению.
//
// Поэтому решение о пине принимается по полному списку, а схлопывание остаётся
// тем, чем оно и было, — способом НАЗВАТЬ номер человеку.
type occupancy map[int][]Holder

// mergeAll складывает занятость источников, сохраняя всех держателей номера.
//
// Конфликт — два РАЗНЫХ заявленных владельца на одном номере: кто прав, пул не
// знает, поэтому номер он не отдаёт никому, а конфликт отдаёт вызывающему в
// журнал. Порядок источников значим только для показа: номер называет первый
// заявленный.
func mergeAll(parts []Taken) (occupancy, Conflicts) {
	occ := occupancy{}
	var conflicts Conflicts
	for _, src := range parts {
		// По возрастанию номера, а не по обходу карты: иначе при двух
		// конфликтах в одном источнике их порядок в журнале менялся бы от
		// запуска к запуску.
		for _, n := range sortedKeys(src) {
			h := src[n]
			if cur, busy := occ.holder(n); busy &&
				!cur.anonymous() && !h.anonymous() && !cur.sameOwner(h) {
				conflicts = append(conflicts, Conflict{Index: n, A: cur, B: h})
			}
			occ[n] = append(occ[n], h)
		}
	}
	return occ, conflicts
}

// busy — номер занят хоть кем-то. Ключ без держателей невыразим: класть в
// карту умеет только mergeAll, и он всегда добавляет.
func (o occupancy) busy(n int) bool { return len(o[n]) > 0 }

// holder — кого назвать человеку: заявленного, если он есть, иначе анонима.
// Живое устройство и запись NDMS — след владельца, и назвать номер именем
// записи полезнее, чем «живой интерфейс opkgtun12».
func (o occupancy) holder(n int) (Holder, bool) {
	hs := o[n]
	if len(hs) == 0 {
		return Holder{}, false
	}
	for _, h := range hs {
		if !h.anonymous() {
			return h, true
		}
	}
	return hs[0], true
}

// collapse — занятость «номер → один держатель» для показа в отказе. Решения
// по ней не принимаются: см. occupancy.
func (o occupancy) collapse() Taken {
	out := make(Taken, len(o))
	for n := range o {
		if h, busy := o.holder(n); busy {
			out[n] = h
		}
	}
	return out
}

func sortedKeys(t Taken) []int {
	out := make([]int, 0, len(t))
	for n := range t {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}
