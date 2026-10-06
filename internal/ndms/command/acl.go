package command

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// NDMS access-list примитивы — единственная точка ACL-мутаций (SET только
// через parse: структурные формы NDMS отвергает). Потребители: fakeip-tun
// (permit-all на OpkgTun, композиции ниже) и managed-серверы (гранулярные
// permit'ы peer→сегмент). Все через postMutationChecked: parse-ответы NDMS
// кладут ошибки во вложенный status[] («a duplicate was found», «cannot
// enable auto-deletion for unreferenced lists», «argument parse error» —
// stand-verified 2026-07-16), который транспортный уровень не видит.

// ACLPermitIP добавляет permit-правило (первый permit неявно создаёт список).
// Повторный идентичный permit NDMS отклоняет «a duplicate was found» БЕЗ
// дублирования правила — вызывающие, которым нужен идемпотентный re-assert,
// матчат IsACLDuplicate.
func (c *InterfaceCommands) ACLPermitIP(ctx context.Context, acl, srcSub, srcMask, dstSub, dstMask string) error {
	return postMutationChecked(ctx, c.poster, c.save,
		map[string]any{"parse": aclPermitIPRule(acl, srcSub, srcMask, dstSub, dstMask)},
		"acl permit "+acl,
		c.queries.RunningConfig.InvalidateAll,
	)
}

// ACLRemovePermitIP снимает одно правило, поставленное ACLPermitIP, оставляя
// список и привязку (`no access-list <acl> permit ip …`, стенд 5.01, 12.09).
// Правила уже нет (`no rule found to delete.`) — цель достигнута.
func (c *InterfaceCommands) ACLRemovePermitIP(ctx context.Context, acl, srcSub, srcMask, dstSub, dstMask string) error {
	return postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": "no " + aclPermitIPRule(acl, srcSub, srcMask, dstSub, dstMask)},
		"acl permit remove "+acl,
		isACLRuleAbsent,
		c.queries.RunningConfig.InvalidateAll,
	)
}

func aclPermitIPRule(acl, srcSub, srcMask, dstSub, dstMask string) string {
	return fmt.Sprintf("access-list %s permit ip %s %s %s %s", acl, srcSub, srcMask, dstSub, dstMask)
}

// ACLRemove удаляет список целиком (`no access-list`). Идемпотентно на
// уровне вызывающих: несуществующий список — ошибка, teardown-пути её логируют.
func (c *InterfaceCommands) ACLRemove(ctx context.Context, acl string) error {
	return postMutationChecked(ctx, c.poster, c.save,
		map[string]any{"parse": "no access-list " + acl},
		"acl remove "+acl,
		c.queries.RunningConfig.InvalidateAll,
	)
}

// ACLBind привязывает список `in` к интерфейсу. Повторная привязка
// идемпотентна (status message, stand-verified).
func (c *InterfaceCommands) ACLBind(ctx context.Context, iface query.Confirmed, acl string) error {
	return postMutationChecked(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("interface %s ip access-group %s in", iface.Name(), acl)},
		"acl bind "+acl,
		func() { c.queries.Interfaces.Invalidate(iface.Name()) },
		c.queries.RunningConfig.InvalidateAll,
	)
}

// ACLUnbind снимает привязку списка с интерфейса.
func (c *InterfaceCommands) ACLUnbind(ctx context.Context, iface query.Confirmed, acl string) error {
	return postMutationChecked(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("no interface %s ip access-group %s in", iface.Name(), acl)},
		"acl unbind "+acl,
		func() { c.queries.Interfaces.Invalidate(iface.Name()) },
		c.queries.RunningConfig.InvalidateAll,
	)
}

// ACLAutoDelete включает каскадное удаление списка вместе с последним
// ссылающимся интерфейсом. Работает ТОЛЬКО на привязанном списке («cannot
// enable auto-deletion for unreferenced lists») — вызывать после ACLBind.
// Повторное включение идемпотентно (stand-verified).
func (c *InterfaceCommands) ACLAutoDelete(ctx context.Context, acl string) error {
	return postMutationChecked(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("access-list %s auto-delete", acl)},
		"acl auto-delete "+acl,
		c.queries.RunningConfig.InvalidateAll,
	)
}

// IsACLDuplicate распознаёт NDMS-отказ на повторный идентичный permit —
// безвредный случай для идемпотентного re-assert. Матчится ПОЛНАЯ фраза
// NDMS («a duplicate was found for the rule being set», stand-verified),
// а не слово «duplicate» — чтобы смешанный ответ с реальной ошибкой,
// случайно содержащей это слово, не был проглочен (ревью).
func IsACLDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "a duplicate was found")
}

// SetPermitAllACL создаёт permit-all access-list `_WEBADMIN_<name>` (конвенция
// веб-морды Keenetic — UI показывает его как разрешение доступа к
// интерфейсу), привязывает `in` и включает auto-delete. Идемпотентен: дубль
// permit толерируется, повторные bind/auto-delete идемпотентны в NDMS.
func (c *InterfaceCommands) SetPermitAllACL(ctx context.Context, iface query.Confirmed) error {
	name := iface.Name()
	acl := "_WEBADMIN_" + name
	if err := c.ACLPermitIP(ctx, acl, "0.0.0.0", "0.0.0.0", "0.0.0.0", "0.0.0.0"); err != nil && !IsACLDuplicate(err) {
		return err
	}
	if err := c.ACLBind(ctx, iface, acl); err != nil {
		return err
	}
	return c.ACLAutoDelete(ctx, acl)
}

// SetPermitAllACLv6 — v6-близнец SetPermitAllACL. У NDMS для IPv6 ОТДЕЛЬНОЕ
// пространство списков: `ip access-group` v6-трафик не покрывает (verified на
// роутере 2026-08-11), и интерфейс с v6-адресом без этой пары остаётся без
// разрешения. Имя списка то же — пространства не пересекаются.
//
// Гранулярных v6-примитивов сознательно нет: единственный потребитель — вот эта
// композиция, а managed-серверы работают только с v4.
//
// Порядок и толерантность те же, что у v4: permit → bind → auto-delete
// (auto-delete работает только на привязанном списке), повторный permit NDMS
// отклоняет как дубль без дублирования правила. Фраза отказа у v6 ТА ЖЕ, что у
// v4 («a duplicate was found for the rule being set», stand-verified
// 2026-08-11), хотя ident другой (Network::Ip6::Acl) — поэтому IsACLDuplicate
// годится на оба протокола и отдельного матчера не нужно.
//
// Дополнительная толерантность против v4: до KeeneticOS 5.01 команд
// `ipv6 access-list`/`ipv6 access-group` не существует вовсе (isACLUnsupported).
// Там разрешать нечего, и отказ не должен валить включение режима — issue #828.
func (c *InterfaceCommands) SetPermitAllACLv6(ctx context.Context, iface query.Confirmed) error {
	name := iface.Name()
	acl := "_WEBADMIN_" + name
	err := postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("ipv6 access-list %s permit ipv6 ::/0 ::/0", acl)},
		"acl6 permit "+acl,
		isACLUnsupported,
		c.queries.RunningConfig.InvalidateAll,
	)
	if err != nil && !IsACLDuplicate(err) {
		return err
	}
	if err := postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("interface %s ipv6 access-group %s in", name, acl)},
		"acl6 bind "+acl,
		isACLUnsupported,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll,
	); err != nil {
		return err
	}
	return postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("ipv6 access-list %s auto-delete", acl)},
		"acl6 auto-delete "+acl,
		isACLUnsupported,
		c.queries.RunningConfig.InvalidateAll,
	)
}

// RemovePermitAllACLv6 — v6-близнец RemovePermitAllACL (та же функция
// removePermitAll, семейство «ipv6»). Прошивки без v6-ACL (isACLUnsupported):
// там блоков нет, по дереву не шлётся ничего; терпимость к «нет такой
// команды» оставлена на обеих командах на случай, если дерево всё же их покажет.
func (c *InterfaceCommands) RemovePermitAllACLv6(ctx context.Context, iface query.Confirmed) error {
	return c.removePermitAll(ctx, iface, "ipv6")
}

// permitAllRuleV4/V6 — наше правило в списке `_WEBADMIN_<name>`, как его
// печатает running-config (стенд 5.01, 2026-09-12).
const (
	permitAllRuleV4 = "permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0"
	permitAllRuleV6 = "permit ipv6 ::/0 ::/0"
)

// hasForeignACLRules — есть ли в списке правила, кроме нашего permit-all.
func hasForeignACLRules(lines []string, header, ours string) bool {
	for _, r := range query.ACLRulesOf(lines, header) {
		if r != ours {
			return true
		}
	}
	return false
}

// aclState — тройка П17 по тому же дереву, по которому решён hasForeignACLRules:
// bound — строка `<family> access-group <acl> in` в блоке `interface <name>`;
// listed — блок header есть; autoDelete — строка `auto-delete` в его теле.
func aclState(lines []string, name, family, header string) (bound, listed, autoDelete bool) {
	bound = slices.Contains(query.InterfaceAccessGroupsOf(lines, name, family), "_WEBADMIN_"+name)
	return bound, query.HasBlock(lines, header), query.HasBlockLine(lines, header, "auto-delete")
}

// removeOurPermitRule снимает ТОЛЬКО нашу строку, оставляя список и привязку
// пользователю (`no rule found to delete.` — цель достигнута, стенд 5.01).
// tolerate — от вызывающего: isACLUnsupported законен только у v6 (на v4
// «нет такой команды» — настоящая поломка, см. предикат).
func (c *InterfaceCommands) removeOurPermitRule(ctx context.Context, cmd, label string, tolerate func(string) bool) error {
	return postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": cmd},
		label,
		tolerate,
		c.queries.RunningConfig.InvalidateAll,
	)
}

// RemovePermitAllACL снимает наш permit-all с интерфейса (v4; порядок —
// removePermitAll).
//
// Список `_WEBADMIN_<name>` — это ЕЩЁ И место, куда веб-морда Keenetic кладёт
// правила межсетевого экрана интерфейса, поэтому сносить его целиком можно
// только тогда, когда кроме нашей строки в нём ничего нет; иначе снимается
// одна наша строка, а список и его привязка остаются пользователю (F314,
// issue #879: `no access-list` уносил и правило, написанное руками).
func (c *InterfaceCommands) RemovePermitAllACL(ctx context.Context, iface query.Confirmed) error {
	return c.removePermitAll(ctx, iface, "ip")
}

// removePermitAll — снятие permit-all одного семейства («ip» или «ipv6»), F606.
//
// Решение — по одному свежему running-config, прочитанному мимо кэша и мимо
// чужого чтения в полёте (Fetch): список и привязку меняют и веб-морда, и
// каскад auto-delete, а хук ndm об этом не приходит. Таблица П17:
//
//	чужие правила            → снять только нашу строку (F314)
//	bound ∧ autoDelete       → только unbind: список NDMS снимает сам
//	bound ∧ ¬autoDelete      → unbind + remove
//	¬bound ∧ listed          → remove
//	¬bound ∧ ¬listed         → ничего
//
// Улики (стенд 5.01.C.6, Task 59 П5; X1 стенда F595): unbind БЕЗ привязки —
// `E Command::Base: argument parse error` в журнале NDMS (это и есть F606:
// выключение policy-tun без v6 слало v6-unbind вслепую); с привязкой все формы
// unbind приняты; после unbind списка с auto-delete блока в running-config нет;
// `no [ipv6 ]access-list` по отсутствующему списку — `access list removed.`
// без E. Поэтому unbind гейтится привязкой, а remove в ветке auto-delete не
// шлётся просто как лишний POST. Пустой список без auto-delete NDMS сам не
// убирает (стенд 5.01, 2026-09-12) — его снимаем.
//
// Running-config недоступен — не снимаем НИЧЕГО: гадать, чей это список,
// дороже, чем оставить своё разрешение до следующего прохода.
//
// isACLNotBound на unbind/remove — для гонки «дерево показало, к POST уже
// снято»; у v6 сверх того isACLUnsupported (#828). На v4 «нет такой команды» —
// настоящая поломка и всплывает.
func (c *InterfaceCommands) removePermitAll(ctx context.Context, iface query.Confirmed, family string) error {
	name := iface.Name()
	acl := "_WEBADMIN_" + name
	header, rule, label := "access-list "+acl, permitAllRuleV4, "acl"
	tolerate := isACLNotBound
	foreignTolerate := isACLRuleAbsent
	if family == "ipv6" {
		header, rule, label = "ipv6 access-list "+acl, permitAllRuleV6, "acl6"
		tolerate = func(msg string) bool { return isACLNotBound(msg) || isACLUnsupported(msg) }
		foreignTolerate = func(msg string) bool { return isACLRuleAbsent(msg) || isACLUnsupported(msg) }
	}
	lines, err := c.queries.RunningConfig.Fetch(ctx)
	if err != nil {
		return fmt.Errorf("%s rules %s: %w", label, acl, err)
	}
	if hasForeignACLRules(lines, header, rule) {
		return c.removeOurPermitRule(ctx, fmt.Sprintf("no %s %s", header, rule), label+" permit remove "+acl, foreignTolerate)
	}
	bound, listed, autoDelete := aclState(lines, name, family, header)
	var unbindErr, removeErr error
	if bound {
		unbindErr = postMutationCheckedTolerant(ctx, c.poster, c.save,
			map[string]any{"parse": fmt.Sprintf("no interface %s %s access-group %s in", name, family, acl)},
			label+" unbind "+acl,
			tolerate,
			func() { c.queries.Interfaces.Invalidate(name) },
			c.queries.RunningConfig.InvalidateAll,
		)
	}
	if listed && !(bound && autoDelete) {
		removeErr = postMutationCheckedTolerant(ctx, c.poster, c.save,
			map[string]any{"parse": "no " + header},
			label+" remove "+acl,
			tolerate,
			c.queries.RunningConfig.InvalidateAll,
		)
	}
	if unbindErr != nil {
		return unbindErr
	}
	return removeErr
}
