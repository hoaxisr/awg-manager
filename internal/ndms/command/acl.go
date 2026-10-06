package command

import (
	"context"
	"errors"
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

// permitAllFamily — семейство снятия permit-all. Тип неэкспортирован, значений
// два: опечатка семейства не компилируется.
type permitAllFamily uint8

const (
	permitV4 permitAllFamily = iota + 1
	permitV6
)

// header — заголовок блока списка: `access-list <acl>` / `ipv6 access-list <acl>`.
func (f permitAllFamily) header(acl string) string {
	if f == permitV6 {
		return "ipv6 access-list " + acl
	}
	return "access-list " + acl
}

// group — первое слово привязки: `ip access-group` / `ipv6 access-group`.
func (f permitAllFamily) group() string {
	if f == permitV6 {
		return "ipv6"
	}
	return "ip"
}

func (f permitAllFamily) rule() string {
	if f == permitV6 {
		return permitAllRuleV6
	}
	return permitAllRuleV4
}

func (f permitAllFamily) label() string {
	if f == permitV6 {
		return "acl6"
	}
	return "acl"
}

// aclState — тройка П17 по тому же дереву, по которому решён hasForeignACLRules:
// bound — строка `<ip|ipv6> access-group <acl> in` в блоке `interface <name>`;
// listed — блок списка есть; autoDelete — строка `auto-delete` в его теле.
func aclState(lines []string, name string, f permitAllFamily) (bound, listed, autoDelete bool) {
	qf := query.ACLv4
	if f == permitV6 {
		qf = query.ACLv6
	}
	header := f.header("_WEBADMIN_" + name)
	bound = slices.Contains(query.InterfaceAccessGroupsOf(lines, name, qf), "_WEBADMIN_"+name)
	return bound, query.HasBlock(lines, header), query.HasBlockLine(lines, header, "auto-delete")
}

// removeOurPermitRule снимает ТОЛЬКО нашу строку, оставляя список и привязку
// пользователю (`no rule found to delete.` — цель достигнута, стенд 5.01).
func (c *InterfaceCommands) removeOurPermitRule(ctx context.Context, cmd, label string) error {
	return postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": cmd},
		label,
		isACLRuleAbsent,
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
	lines, err := c.freshACLTree(ctx, iface.Name())
	if err != nil {
		return err
	}
	return c.removePermitAll(ctx, iface.Name(), lines, permitV4)
}

// RemovePermitAllACLs снимает наш permit-all обоих семейств (v4 и v6) по ОДНОМУ
// чтению running-config: выключение, удаление и откат слота снимают оба, а
// полное дерево стоит ≈89 тиков ndm. v4-POST'ы v6-блоков не меняют, поэтому
// дерево годится и для второго семейства. Провал v4 не мешает снять v6:
// ошибки собираются вместе.
func (c *InterfaceCommands) RemovePermitAllACLs(ctx context.Context, iface query.Confirmed) error {
	lines, err := c.freshACLTree(ctx, iface.Name())
	if err != nil {
		return err
	}
	return errors.Join(
		c.removePermitAll(ctx, iface.Name(), lines, permitV4),
		c.removePermitAll(ctx, iface.Name(), lines, permitV6),
	)
}

// freshACLTree — свежий running-config для решения о снятии: мимо кэша и мимо
// чужого чтения в полёте (Fetch). Список и привязку меняют и веб-морда, и
// каскад auto-delete, а хук ndm об этом не приходит. Дерево не прочитано —
// не снимаем НИЧЕГО: гадать, чей это список, дороже, чем оставить своё
// разрешение до следующего прохода.
func (c *InterfaceCommands) freshACLTree(ctx context.Context, name string) ([]string, error) {
	lines, err := c.queries.RunningConfig.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("acl rules _WEBADMIN_%s: %w", name, err)
	}
	return lines, nil
}

// removePermitAll — снятие permit-all одного семейства по готовому дереву, F606.
// Таблица П17:
//
//	чужие правила            → снять только нашу строку (F314)
//	bound ∧ autoDelete       → только unbind: список NDMS снимает сам
//	bound ∧ ¬autoDelete      → unbind, затем remove
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
// remove уходит и после провала unbind: снятие best-effort, и висящий список
// хуже лишнего POST; наружу — ошибка unbind (первая по порядку).
//
// isACLNotBound на unbind/remove — для гонки «дерево показало, к POST уже
// снято». «Нет такой команды» (#828, прошивка без v6-ACL) здесь не прощается
// ни у одного семейства: POST уходит только по блоку из дерева, а такая
// прошивка v6-блоков не печатает.
func (c *InterfaceCommands) removePermitAll(ctx context.Context, name string, lines []string, f permitAllFamily) error {
	acl := "_WEBADMIN_" + name
	header, label := f.header(acl), f.label()
	if hasForeignACLRules(lines, header, f.rule()) {
		return c.removeOurPermitRule(ctx, fmt.Sprintf("no %s %s", header, f.rule()), label+" permit remove "+acl)
	}
	bound, listed, autoDelete := aclState(lines, name, f)
	var unbindErr, removeErr error
	if bound {
		unbindErr = postMutationCheckedTolerant(ctx, c.poster, c.save,
			map[string]any{"parse": fmt.Sprintf("no interface %s %s access-group %s in", name, f.group(), acl)},
			label+" unbind "+acl,
			isACLNotBound,
			func() { c.queries.Interfaces.Invalidate(name) },
			c.queries.RunningConfig.InvalidateAll,
		)
	}
	if listed && !(bound && autoDelete) {
		removeErr = postMutationCheckedTolerant(ctx, c.poster, c.save,
			map[string]any{"parse": "no " + header},
			label+" remove "+acl,
			isACLNotBound,
			c.queries.RunningConfig.InvalidateAll,
		)
	}
	if unbindErr != nil {
		return unbindErr
	}
	return removeErr
}
