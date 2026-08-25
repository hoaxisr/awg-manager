package tunnel

import "strconv"

// OpkgTunIndexOf — номер OpkgTun, который получит туннель с этим идентификатором.
//
// Номер выводится из NewNames — той же функции, что СТРОИТ имя интерфейса, —
// поэтому предикат не может разойтись с реальностью: если NewNames построит
// OpkgTun4, здесь вернётся 4. Собственный разбор идентификатора был бы четвёртой
// копией правила и слепым к клиентским ID: ручка создания принимает любой
// идентификатор вида [a-zA-Z][a-zA-Z0-9_-]{0,31}, а extractTunnelNum берёт хвост
// с первой цифры и подставляет "0", если цифр нет.
//
// false означает, что номер не занят: NDMS-имени нет (OS 4.x, awgm<N>).
// ВНИМАНИЕ: по идентификатору нельзя отсеять nativewg и raw-клиентов wdtt —
// у них номер либо не в идентификаторе, либо не в пространстве OpkgTun. Это
// делает вызывающий по backend записи (storage.AWGTunnel.OpkgTunIndex).
func OpkgTunIndexOf(tunnelID string) (int, bool) {
	names := NewNames(tunnelID)
	if names.NDMSName == "" {
		return 0, false
	}
	num, err := strconv.Atoi(names.TunnelNum)
	if err != nil {
		return 0, false
	}
	return num, true
}
