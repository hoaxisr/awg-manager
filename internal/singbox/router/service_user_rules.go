package router

import (
	"context"
	"fmt"
	"slices"
)

// Правки пользовательских правил для MCP. В отличие от AddRule/UpdateRule
// (форма веб-интерфейса) они проверяют то, что форма гарантирует сама:
// outbound правила существует, системное правило (sniff, hijack-dns, обход
// приватных адресов) не сдвигается и не правится, managed-правило не
// трогается — его перепишет следующий reconcile. Каждая правка — один
// withConfig: чтение и запись черновика без зазора.

// checkRuleTarget — правило из MCP: route с известным outbound или reject.
// Наборы правил, на которые оно ссылается, должны существовать; остальные
// матчеры и их форму проверяет RouterConfig (AddRule/UpdateRule).
func (s *ServiceImpl) checkRuleTarget(ctx context.Context, r Rule, c *RouterConfig) error {
	switch r.Action {
	case "", "route":
		if r.Outbound == "" {
			return fmt.Errorf("%w: route-правилу нужен outbound", ErrRuleUnsupported)
		}
		if !s.isKnownOutboundTag(ctx, r.Outbound, c) {
			return fmt.Errorf("%w: неизвестный outbound %q", ErrRuleUnsupported, r.Outbound)
		}
	case "reject":
		if r.Outbound != "" {
			return fmt.Errorf("%w: у reject-правила нет outbound", ErrRuleUnsupported)
		}
	default:
		return fmt.Errorf("%w: действие %q", ErrRuleUnsupported, r.Action)
	}
	if r.Type == "logical" || isSystemRule(r) || r.AwgmManaged != "" {
		return fmt.Errorf("%w: логические, системные и managed-правила здесь не создаются", ErrRuleUnsupported)
	}
	for _, tag := range r.RuleSet {
		if !slices.ContainsFunc(c.Route.RuleSet, func(rs RuleSet) bool { return rs.Tag == tag }) {
			return fmt.Errorf("%w: %q", ErrRuleSetNotFound, tag)
		}
	}
	return nil
}

// RuleCheck сверяет правило на позиции, которую правка собирается тронуть,
// с тем, что видел вызывающий. Агент берёт позицию из прочитанного раньше
// списка; после удаления или правки в другом окне на ней стоит другое
// правило. Сверка идёт внутри той же записи, что и правка, — между ними
// нет зазора; её ошибка отменяет правку до записи. nil — без сверки.
type RuleCheck func(Rule) error

// userRuleAt — правило i существует, принадлежит пользователю и проходит
// check.
func userRuleAt(c *RouterConfig, i int, check RuleCheck) error {
	if i < 0 || i >= len(c.Route.Rules) {
		return ErrRuleIndexOutOfRange
	}
	switch r := c.Route.Rules[i]; {
	case isSystemRule(r):
		return fmt.Errorf("%w: правило %d системное", ErrRuleProtected, i)
	case r.AwgmManaged != "":
		return fmt.Errorf("%w: правило %d создано awg-manager и будет переписано", ErrRuleProtected, i)
	}
	if check != nil {
		return check(c.Route.Rules[i])
	}
	return nil
}

// InsertRule ставит r на место index, сдвигая правило с этого места ниже;
// index, равный числу правил, — в конец. Как перетаскивание в веб-интерфейсе,
// на место системного правила вставить нельзя: оно стоит выше правил
// пользователя намеренно. check сверяет правило, которое сейчас стоит на
// месте index и уйдёт ниже; в конце списка сверять не с чем.
func (s *ServiceImpl) InsertRule(ctx context.Context, index int, r Rule, check RuleCheck) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error {
		n := len(c.Route.Rules)
		if index == -1 {
			// В конец — по списку внутри этой же записи, а не по прочитанному
			// раньше: правка из другого окна между ними сдвинула бы конец.
			index = n
		}
		if index < 0 || index > n {
			return ErrRuleIndexOutOfRange
		}
		// AddRule ниже ставит r в конец, а MoveRule(n, index) переносит его
		// через весь прежний суффикс. Как и Move/MoveUserRule, запрещаем
		// пересекать любое системное правило до первой мутации конфига.
		for i := index; i < n; i++ {
			if isSystemRule(c.Route.Rules[i]) {
				return fmt.Errorf("%w: диапазон %d..%d содержит системное правило %d", ErrRuleProtected, n, index, i)
			}
		}
		if check != nil {
			if index == n {
				return fmt.Errorf("%w: на месте %d правила нет — сверять не с чем", ErrRuleIndexOutOfRange, index)
			}
			if err := check(c.Route.Rules[index]); err != nil {
				return err
			}
		}
		if err := s.checkRuleTarget(ctx, r, c); err != nil {
			return err
		}
		if err := c.AddRule(r); err != nil {
			return err
		}
		return c.MoveRule(n, index)
	})
}

// ReplaceUserRule заменяет правило пользователя index на r целиком.
func (s *ServiceImpl) ReplaceUserRule(ctx context.Context, index int, r Rule, check RuleCheck) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error {
		if err := userRuleAt(c, index, check); err != nil {
			return err
		}
		if err := s.checkRuleTarget(ctx, r, c); err != nil {
			return err
		}
		return c.UpdateRule(index, r)
	})
}

// DeleteUserRule удаляет правило пользователя index и возвращает его, каким
// оно было: после применения черновика правило ничем, кроме этой записи, не
// восстановить.
func (s *ServiceImpl) DeleteUserRule(ctx context.Context, index int, check RuleCheck) (Rule, error) {
	var deleted Rule
	err := s.withConfig(ctx, "rules", func(c *RouterConfig) error {
		if err := userRuleAt(c, index, check); err != nil {
			return err
		}
		deleted = c.Route.Rules[index]
		return c.DeleteRule(index)
	})
	return deleted, err
}

// MoveUserRule переносит правило пользователя from на место to. Как и
// перетаскивание в веб-интерфейсе, пересекаемый интервал не может содержать
// системное правило. check сверяет переносимое правило from.
func (s *ServiceImpl) MoveUserRule(ctx context.Context, from, to int, check RuleCheck) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error {
		if err := userRuleAt(c, from, check); err != nil {
			return err
		}
		if to < 0 || to >= len(c.Route.Rules) {
			return ErrRuleIndexOutOfRange
		}
		for i := min(from, to); i <= max(from, to); i++ {
			if isSystemRule(c.Route.Rules[i]) {
				return fmt.Errorf("%w: диапазон %d..%d содержит системное правило %d", ErrRuleProtected, from, to, i)
			}
		}
		return c.MoveRule(from, to)
	})
}

// SetUserRuleOutbound направляет правило пользователя index в outbound —
// как BulkSetRuleOutbound для одного правила (outbound известен, правило —
// route, не системное и не managed), но со сверкой check в той же записи.
func (s *ServiceImpl) SetUserRuleOutbound(ctx context.Context, index int, outbound string, check RuleCheck) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error {
		if err := userRuleAt(c, index, check); err != nil {
			return err
		}
		return bulkSetRuleOutbound(c, []int{index}, outbound, func(t string) bool { return s.isKnownOutboundTag(ctx, t, c) })
	})
}
