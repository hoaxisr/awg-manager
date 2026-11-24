package netutil

import "net"

// SkipHostRoute — «до этого адреса host-route через WAN не ставят».
//
// Один предикат на операторов обоих бэкендов: kernel (ops) и nativewg (nwg,
// трекер endpoint'ов и выбор адреса для снятия host-route). F230.
//
// Тот же набор проверок стоит в SSRF-гарде `internal/proxyapp/wdttlink` —
// совпадение случайное: там режут ВНУТРЕННИЕ адреса, здесь — бессмысленные
// для маршрута. Объединять нельзя, разъезжаться им можно.
//
// Что отсеивается и почему:
//   - петля: связанные туннели wdtt/freeturn и обфусцированный nativewg несут
//     endpoint 127.0.0.1:<порт> — релей слушает на самом роутере;
//   - link-local (uni и multi): маршрутизируются on-link, через WAN им нечего
//     делать;
//   - «неуказанный» 0.0.0.0/::: фильтрующий DNS отдаёт его на заблокированный
//     домен, и такой адрес не должен становиться маршрутом.
//
// Имя про решение, а не про свойство адреса: 127.0.0.1 как раз маршрутизируется
// — через lo, — просто не через WAN. Неразбираемый вход НЕ отсеивается: пусть
// отказывает команда ip и причина видна в журнале.
func SkipHostRoute(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.IsLoopback() || parsed.IsLinkLocalUnicast() ||
		parsed.IsLinkLocalMulticast() || parsed.IsUnspecified()
}

// RouteHolders — кто, кроме спрашивающего туннеля, смотрит на тот же IP сервера.
// Host-route ставят двое, и строки у них разные: kernel-туннель — в ядро,
// обфусцированный nativewg — запись NDMS. Обычный nativewg не ставит ничего:
// в Any он есть, а снятию чужих маршрутов не мешает (F889).
type RouteHolders struct {
	Kernel     bool
	Obfuscated bool
	Any        bool
}
