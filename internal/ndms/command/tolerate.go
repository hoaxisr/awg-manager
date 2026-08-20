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

// isNetlinkFileExists: `file exists` от Io::Netlink — ответ NDMS на СНЯТИЕ
// кандидатуры дефолтного маршрута. Ошибка ложная: запись при этом реально
// снимается (проверено на роутере 2026-08-18 — после ответа
// show/rc/ip/route пуст). Считать её провалом нельзя: teardown policy-tun
// рапортовал бы отказ там, где роутер всё сделал, и не сбрасывал бы кэши.
func isNetlinkFileExists(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "file exists")
}

// IsMissingInterfaceError сообщает, что роутер отказал из-за отсутствия
// интерфейса или сегмента. Экспортирован для вызывающих, которым такой отказ
// означает «нечего делать»: восстановление NAT на сегменте, удалённом
// пользователем, — не провал восстановления.
func IsMissingInterfaceError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return isMissingInterface(msg) || isNoSuchInterface(msg) || isUnknownInterface(msg) ||
		strings.Contains(strings.ToLower(msg), "ip interface found")
}

// isNoInput: `no input [http/rci 127.0.0.1].` — форма отказа Command::Root,
// которой NDMS отвечает на снос отсутствующей политики и отсутствующей
// object-group. Общая фраза, поэтому применяется ТОЛЬКО к этим двум сносам.
func isNoInput(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "no input [http/rci")
}

// toleratesMissingDNSRoute — безобидные отказы сноса dns-маршрута: правила уже
// нет либо исчез интерфейс, на который оно ссылалось. Список на снос строится
// из живой выдачи роутера, поэтому вторая форма — штатный дрейф, а не поломка;
// фатальной она останавливала весь синк, и застрявшая запись не вычищалась.
func toleratesMissingDNSRoute(msg string) bool {
	return isMissingDNSRouteRule(msg) || isNoSuchInterface(msg)
}
