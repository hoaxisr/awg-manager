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

// missingInterfaceName вытаскивает имя из `no such interface: OpkgTun42.`.
// Нужно смешанному батчу ReplaceRoutes: там та же фраза безобидна для сноса
// строки и означает настоящий отказ для её постановки, а различить половины
// можно только по имени интерфейса. Сравнивать подстрокой нельзя — «OpkgTun1»
// входит в «OpkgTun10».
func missingInterfaceName(msg string) string {
	const marker = "no such interface:"
	i := strings.Index(strings.ToLower(msg), marker)
	if i < 0 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(msg[i+len(marker):]), ".")
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

// isACLNotBound — ответ NDMS на unbind/remove списка, которого уже нет:
// `argument parse error` (стенд 5.01, 2026-09-05: непривязанный, несуществующий
// список, несуществующий интерфейс, повторный unbind). Применять ТОЛЬКО к
// снятию permit-all: наша форма команды фиксирована, других «argument»-ошибок
// у неё нет; на прочих командах эта фраза означала бы настоящую опечатку.
func isACLNotBound(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "argument parse error")
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

// isACLUnsupported: `no such command: access-list.` / `no such command:
// access-group.` — v6-ACL (`ipv6 access-list`/`ipv6 access-group`) появился
// только в KeeneticOS 5.01; на 5.00 парсер этих команд не знает. Разрешать там
// нечего — механизма фильтрации v6 на OpkgTun нет вовсе, — поэтому отказ
// безобиден. Фатальным он валил ВСЁ включение fakeip/policy-tun на 5.00
// (issue #828). Предикат применяется ТОЛЬКО к v6-композициям: у v4 те же
// команды существуют на всех прошивках, и «нет такой команды» там означало бы
// настоящую поломку.
func isACLUnsupported(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "no such command: access-list") ||
		strings.Contains(strings.ToLower(msg), "no such command: access-group")
}
