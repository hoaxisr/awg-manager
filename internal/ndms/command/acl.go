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

// RemovePermitAllACLv6 снимает v6-привязку и удаляет v6-список. Best-effort по
// замыслу, как и v4-близнец: интерфейс мог быть уже удалён, а auto-delete —
// каскадировать список.
func (c *InterfaceCommands) RemovePermitAllACLv6(ctx context.Context, name string) error {
	acl := "_WEBADMIN_" + name
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

// RemovePermitAllACL снимает permit-all `_WEBADMIN_<name>` с интерфейса и
// удаляет список. Идемпотентно: «привязки/списка уже нет» (argument parse
// error — стенд 2026-09-05) не ошибка; прочие отказы всплывают. auto-delete
// мог снести список сразу после unbind — remove это тоже терпит (симметрия
// на практике мёртвая: стенд показал, что `no access-list` после
// auto-delete отвечает «access list removed», не ошибкой).
func (c *InterfaceCommands) RemovePermitAllACL(ctx context.Context, name string) error {
	acl := "_WEBADMIN_" + name
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
