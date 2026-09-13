package command

import (
	"context"
	"fmt"
	"strings"
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
		map[string]any{"parse": fmt.Sprintf("access-list %s permit ip %s %s %s %s", acl, srcSub, srcMask, dstSub, dstMask)},
		"acl permit "+acl,
		c.queries.RunningConfig.InvalidateAll,
	)
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
func (c *InterfaceCommands) ACLBind(ctx context.Context, iface, acl string) error {
	return postMutationChecked(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("interface %s ip access-group %s in", iface, acl)},
		"acl bind "+acl,
		func() { c.queries.Interfaces.Invalidate(iface) },
		c.queries.RunningConfig.InvalidateAll,
	)
}

// ACLUnbind снимает привязку списка с интерфейса.
func (c *InterfaceCommands) ACLUnbind(ctx context.Context, iface, acl string) error {
	return postMutationChecked(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("no interface %s ip access-group %s in", iface, acl)},
		"acl unbind "+acl,
		func() { c.queries.Interfaces.Invalidate(iface) },
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
func (c *InterfaceCommands) SetPermitAllACL(ctx context.Context, name string) error {
	acl := "_WEBADMIN_" + name
	if err := c.ACLPermitIP(ctx, acl, "0.0.0.0", "0.0.0.0", "0.0.0.0", "0.0.0.0"); err != nil && !IsACLDuplicate(err) {
		return err
	}
	if err := c.ACLBind(ctx, name, acl); err != nil {
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
func (c *InterfaceCommands) SetPermitAllACLv6(ctx context.Context, name string) error {
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

// RemovePermitAllACLv6 — v6-близнец RemovePermitAllACL: та же развилка «в
// списке есть чужие правила → снять только нашу строку», та же терпимость к
// прошивкам без v6-ACL (isACLUnsupported: там блока нет, чужих правил нет,
// и обе команды чистой ветки отказ терпят).
func (c *InterfaceCommands) RemovePermitAllACLv6(ctx context.Context, name string) error {
	acl := "_WEBADMIN_" + name
	foreign, err := c.hasForeignACLRules(ctx, "ipv6 access-list "+acl, permitAllRuleV6)
	if err != nil {
		return fmt.Errorf("acl6 rules %s: %w", acl, err)
	}
	if foreign {
		return c.removeOurPermitRule(ctx,
			fmt.Sprintf("no ipv6 access-list %s %s", acl, permitAllRuleV6), "acl6 permit remove "+acl)
	}
	unbindErr := postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("no interface %s ipv6 access-group %s in", name, acl)},
		"acl6 unbind "+acl,
		isACLUnsupported,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll,
	)
	removeErr := postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": "no ipv6 access-list " + acl},
		"acl6 remove "+acl,
		isACLUnsupported,
		c.queries.RunningConfig.InvalidateAll,
	)
	if unbindErr != nil {
		return unbindErr
	}
	return removeErr
}

// permitAllRuleV4/V6 — наше правило в списке `_WEBADMIN_<name>`, как его
// печатает running-config (стенд 5.01, 2026-09-12).
const (
	permitAllRuleV4 = "permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0"
	permitAllRuleV6 = "permit ipv6 ::/0 ::/0"
)

// hasForeignACLRules — есть ли в списке правила, кроме нашего permit-all.
// Кэш сбрасывается перед чтением: правила в этот список пишет ещё и веб-морда
// роутера, а хук ndm на её правку к нам не приходит (стенд 2026-09-06) — по
// устаревшему снимку мы снесли бы чужие строки.
func (c *InterfaceCommands) hasForeignACLRules(ctx context.Context, header, ours string) (bool, error) {
	c.queries.RunningConfig.InvalidateAll()
	rules, err := c.queries.RunningConfig.ACLRules(ctx, header)
	if err != nil {
		return false, err
	}
	for _, r := range rules {
		if r != ours {
			return true, nil
		}
	}
	return false, nil
}

// removeOurPermitRule снимает ТОЛЬКО нашу строку, оставляя список и привязку
// пользователю (`no rule found to delete.` — цель достигнута, стенд 5.01).
func (c *InterfaceCommands) removeOurPermitRule(ctx context.Context, cmd, label string) error {
	return postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": cmd},
		label,
		func(msg string) bool { return isACLRuleAbsent(msg) || isACLUnsupported(msg) },
		c.queries.RunningConfig.InvalidateAll,
	)
}

// RemovePermitAllACL снимает наш permit-all с интерфейса.
//
// Список `_WEBADMIN_<name>` — это ЕЩЁ И место, куда веб-морда Keenetic кладёт
// правила межсетевого экрана интерфейса, поэтому сносить его целиком можно
// только тогда, когда кроме нашей строки в нём ничего нет; иначе снимается
// одна наша строка, а список и его привязка остаются пользователю (F314,
// issue #879: `no access-list` уносил и правило, написанное руками). Пустой
// список NDMS сам не убирает (стенд 5.01, 2026-09-12) — в «чистой» ветке
// по-прежнему unbind + `no access-list`, чтобы вернуть роутер ровно в то
// состояние, какое было до нас.
//
// Running-config недоступен — не снимаем НИЧЕГО: гадать, чей это список,
// дороже, чем оставить своё разрешение до следующего прохода.
//
// Идемпотентность чистой ветки прежняя: «привязки/списка уже нет» (argument
// parse error — стенд 2026-09-05) не ошибка; прочие отказы всплывают.
func (c *InterfaceCommands) RemovePermitAllACL(ctx context.Context, name string) error {
	acl := "_WEBADMIN_" + name
	foreign, err := c.hasForeignACLRules(ctx, "access-list "+acl, permitAllRuleV4)
	if err != nil {
		return fmt.Errorf("acl rules %s: %w", acl, err)
	}
	if foreign {
		return c.removeOurPermitRule(ctx,
			fmt.Sprintf("no access-list %s %s", acl, permitAllRuleV4), "acl permit remove "+acl)
	}
	unbindErr := postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("no interface %s ip access-group %s in", name, acl)},
		"acl unbind "+acl,
		isACLNotBound,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll,
	)
	removeErr := postMutationCheckedTolerant(ctx, c.poster, c.save,
		map[string]any{"parse": "no access-list " + acl},
		"acl remove "+acl,
		isACLNotBound,
		c.queries.RunningConfig.InvalidateAll,
	)
	if unbindErr != nil {
		return unbindErr
	}
	return removeErr
}
