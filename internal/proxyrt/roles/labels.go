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

// Пул локальных listen-портов клиентов (127.0.0.1; действующий диапазон обеих
// подсистем — wdtt/service.go:583, freeturn/service.go:614). Выделение пинов —
// писатель конфига (план 5) через proxyrt.Allocator; роли пин только требуют.
// Своего окна номеров OpkgTun у прокси больше нет: порядок обхода задаёт
// общий пул (internal/opkgtun), и он один на четыре подсистемы. Отдельное
// окно было гарантией ровно до #891, когда kernel-туннели пошли в 17..потолок.
const (
	ListenPortMin = 9000
	ListenPortMax = 9200
)
