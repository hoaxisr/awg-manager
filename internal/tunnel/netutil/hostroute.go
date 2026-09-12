package netutil

import "net"

// SkipHostRoute — «до этого адреса host-route через WAN не ставят».
//
// Один предикат на весь проект: до него один и тот же набор был переписан
// трижды (ops.skipEndpointHostRoute, nwg.obfRouteIP, nwg.trackEndpointIP), и
// комментарий в ops просил «чиня третий такой случай, загляните в оба» —
// вместо этого счётчик рос. F230.
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
