package command

import "strings"

// Предикаты «безобидного отказа» для идемпотентных сносов. Каждая фраза снята
// с живого роутера (KeeneticOS 5.01) — NDMS отвечает HTTP 200 и кладёт отказ во
// вложенный status, а повторный снос того, чего уже нет, для нас нормальная
// ситуация: реап, откат и disable зовут снос по нескольку раз.
//
// Предикаты намеренно узкие и разные: одна общая фраза-ловушка проглотила бы
// настоящие отказы (например, отказ в СОЗДАНИИ интерфейса тоже начинается с
// «unable to find», но означает совсем другое — см. isMissingInterface).

// isMissingInterface: `unable to find interface "OpkgTun9"` — снос интерфейса
// (OpkgTun, Proxy), которого уже нет.
func isMissingInterface(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "unable to find interface")
}

// isNoSuchInterface: `no such interface: OpkgTun42.` — снятие маршрута с
// интерфейса, удалённого раньше маршрута (так работает отложенный drain fakeip).
func isNoSuchInterface(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "no such interface")
}

// isUnknownInterface: `unknown interface "AwgmNoSeg".` — снятие Static NAT,
// когда сегмента или WAN-выхода уже нет.
func isUnknownInterface(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "unknown interface")
}

// isMissingDNSRouteRule: `unable to find the DNS route rule.` — повторное
// удаление dns-proxy маршрута.
func isMissingDNSRouteRule(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "unable to find the dns route rule")
}

// isNoInput: `no input [http/rci 127.0.0.1].` — форма отказа Command::Root,
// которой NDMS отвечает на снос отсутствующей политики и отсутствующей
// object-group. Общая фраза, поэтому применяется ТОЛЬКО к этим двум сносам.
func isNoInput(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "no input [http/rci")
}
