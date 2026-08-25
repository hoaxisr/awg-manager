package storage

import "github.com/hoaxisr/awg-manager/internal/tunnel"

// OpkgTunIndex — номер OpkgTun, который занимает эта запись, и признак того,
// занимает ли она его вообще.
//
// Единственный источник номера — tunnel.OpkgTunIndexOf поверх NewNames, то есть
// та же функция, что строит имя интерфейса. Своего разбора идентификатора здесь
// нет намеренно: он разошёлся бы с реальностью на клиентских ID, которые ручка
// создания принимает как есть.
//
// Номер не занимают:
//   - nativewg — живёт как Wireguard<N>, OpkgTun не создаёт; его идентификатор
//     awg<N> занимает пространство ИДЕНТИФИКАТОРОВ, но не номеров интерфейсов
//     (легаси-записи такого рода лежат и в kernel-диапазоне awg10..16);
//   - wdtt-raw — номер приходит от своей подсистемы и хранится в RawNdmsIface,
//     а из идентификатора вида "wdttraw-<клиент>" вывелся бы ложный ноль;
//   - записи без NDMS-имени (OS 4.x, awgm<N>).
//
// Пустой Backend считается kernel — так же его трактуют nextAvailableID и
// рантайм-диспатч по бэкенду.
func (t AWGTunnel) OpkgTunIndex() (int, bool) {
	switch t.Backend {
	case "nativewg", "wdtt-raw":
		return 0, false
	}
	return tunnel.OpkgTunIndexOf(t.ID)
}

// OpkgTunIndicesOf — номера OpkgTun, занятые набором записей.
//
// Пустая карта означает «занятых нет». Отличать это от «не смогли посмотреть»
// обязан вызывающий: перечисление записей может отказать, и тогда занятость
// неизвестна — выдавать по ней номер нельзя.
func OpkgTunIndicesOf(tunnels []AWGTunnel) map[int]bool {
	indices := make(map[int]bool, len(tunnels))
	for _, t := range tunnels {
		if idx, ok := t.OpkgTunIndex(); ok {
			indices[idx] = true
		}
	}
	return indices
}
