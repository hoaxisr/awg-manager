package roles

// Метки владения NDMS-интерфейсами. Значения — ПОБАЙТОВО из старого кода
// (internal/wdtt/ndms_iface.go:34,38, client_ndms.go:9): sweeper обязан
// усыновить интерфейсы, созданные до апгрейда.
//
// Меток три, а не две (уточнение спеки §4.2 по факту кода): raw-половина
// сервера несёт собственное описание.
const (
	LabelServerWG  = "AWGM WDTT"
	LabelServerRaw = "AWGM WDTT Raw"
	// LabelClientPrefix — ПРЕФИКС описания клиентского OpkgTun; хвост —
	// человекочитаемое имя инстанса. Старый код писал в описание только имя
	// (TunnelNameFromClient), а реап искал точное совпадение константы
	// "AWGM WDTT Raw Client" — то есть клиентские сироты не находились НИКОГДА
	// (ndms_iface.go:452 против client_ndms.go:123-126). Префикс чинит скан,
	// сохраняя имя для человека.
	LabelClientPrefix = "AWGM WDTT Raw Client"
)

// ClientDescription собирает описание клиентского OpkgTun из метки и имени.
func ClientDescription(name string) string {
	if name == "" {
		return LabelClientPrefix
	}
	return LabelClientPrefix + ": " + name
}

// Диапазон индексов OpkgTun (вне fakeip 0..9, awg 10..16) и пул
// локальных listen-портов клиентов (127.0.0.1; действующий диапазон обеих
// подсистем — wdtt/service.go:583, freeturn/service.go:614). Выделение пинов —
// писатель конфига (план 5) через proxyrt.Allocator; роли пин только требуют.
const (
	// Диапазон arm/arm64: 17..49 — вне fakeip (0..9) и AWG-туннелей (10..16,
	// индекс выводится из имени туннеля, tunnel/types.go:209). Проверен
	// владельцем на железе.
	OpkgIndexMin = 17
	OpkgIndexMax = 49

	// mips/mipsel: прошивка отвергает индексы больше 15 («index 17 is too
	// large for "OpkgTun" interface», стенд 5.1.3, 2026-08-18). Статического
	// места под прокси там нет — пул 0..15 целиком расписан между fakeip и
	// AWG, поэтому индекс берётся ПЕРВЫЙ СВОБОДНЫЙ по факту занятости
	// (решение владельца 2026-08-18, вариант «а»): живые интерфейсы + пины
	// конфигов; при исчерпании — честный отказ.
	OpkgIndexMinMIPS = 0
	OpkgIndexMaxMIPS = 15

	ListenPortMin = 9000
	ListenPortMax = 9200
)

// OpkgIndexRange — допустимые индексы OpkgTun для архитектуры роутера.
// Границу задаёт прошивка, а не мы: на mips/mipsel запрос OpkgTun17 отвергается
// («index 17 is too large»), на arm/arm64 диапазон 17..49 рабочий. Второй
// возврат — признак «пул делится с чужими подсистемами»: на mips выделять
// можно только по факту занятости, статического поддиапазона под прокси нет.
func OpkgIndexRange(goarch string) (min, max int, shared bool) {
	switch goarch {
	case "mips", "mipsle", "mips64", "mips64le":
		return OpkgIndexMinMIPS, OpkgIndexMaxMIPS, true
	default:
		return OpkgIndexMin, OpkgIndexMax, false
	}
}
