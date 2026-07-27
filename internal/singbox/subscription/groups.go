package subscription

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// groupTagRe — допустимый пользовательский outbound-тег группы (#572):
// латиница/цифры/._-, до 32 символов, первый символ — буква или цифра.
var groupTagRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// reservedGroupTags — теги, занятые системными outbound'ами sing-box.
var reservedGroupTags = map[string]struct{}{"direct": {}, "block": {}, "dns-out": {}}

// validateGroupTag проверяет формат пользовательского тега; коллизии со
// слотами вне подписок ловит sing-box check при Reload (rollback → 422).
func validateGroupTag(tag string) error {
	if !groupTagRe.MatchString(tag) {
		return fmt.Errorf("%w: недопустимый тег %q — латиница, цифры и ._-, до 32 символов, первый символ — буква или цифра", ErrValidation, tag)
	}
	if _, ok := reservedGroupTags[tag]; ok {
		return fmt.Errorf("%w: тег %q зарезервирован", ErrValidation, tag)
	}
	if strings.HasPrefix(tag, "sub-") {
		return fmt.Errorf("%w: префикс \"sub-\" зарезервирован за подписками", ErrValidation)
	}
	return nil
}

// groupTagConflict проверяет коллизии тега (и производного "<tag>-in") с
// selector/inbound/member-тегами существующих подписок.
func (s *Service) groupTagConflict(tag string) error {
	in := tag + "-in"
	for _, sub := range s.store.List() {
		if sub.SelectorTag == tag || sub.InboundTag == tag ||
			sub.SelectorTag == in || sub.InboundTag == in {
			return fmt.Errorf("%w: тег %q уже занят подпиской %q", ErrValidation, tag, sub.Label)
		}
		for _, m := range sub.Members {
			if m.Tag == tag || m.Tag == in {
				return fmt.Errorf("%w: тег %q уже занят сервером подписки %q", ErrValidation, tag, sub.Label)
			}
		}
	}
	return nil
}

// ErrGroupsDisabled возвращается Group-CRUD, когда GroupStore не подключён
// (SetGroupStore не вызывался — тесты / legacy bootstrap).
var ErrGroupsDisabled = errors.New("subscription: aggregate groups are not configured")

// ErrGroupSubscriptionNotFound возвращается Create/UpdateGroup, когда
// useSubscriptionIds ссылается на несуществующую подписку.
var ErrGroupSubscriptionNotFound = errors.New("subscription: группа ссылается на несуществующую подписку")

// SetGroupStore подключает хранилище сводных групп. Вызывается один раз
// при bootstrap; nil-store оставляет функциональность выключенной.
func (s *Service) SetGroupStore(gs *GroupStore) { s.groups = gs }

// ListGroups возвращает все сводные группы.
func (s *Service) ListGroups() []AggregateGroup {
	if s.groups == nil {
		return nil
	}
	return s.groups.List()
}

// GetGroup возвращает одну сводную группу.
func (s *Service) GetGroup(id string) (*AggregateGroup, error) {
	if s.groups == nil {
		return nil, ErrGroupsDisabled
	}
	return s.groups.Get(id)
}

// ResolveGroupMembers собирает членов группы в детерминированном порядке:
// useSubscriptionIds в сохранённом порядке → для каждой существующей и
// включённой подписки её Members в сохранённом порядке → тег попадает в
// группу, когда фильтр группы пропускает имя сервера. Повторные ID подписок
// схлопываются (member-теги в группе не дублируются).
func (s *Service) ResolveGroupMembers(g AggregateGroup) ([]MemberInfo, error) {
	flt, err := CompileMemberFilter(g.FilterInclude, g.FilterExclude)
	if err != nil {
		return nil, fmt.Errorf("subscription group: %w", err)
	}
	out := []MemberInfo{}
	seenSub := make(map[string]bool, len(g.UseSubscriptionIDs))
	for _, subID := range g.UseSubscriptionIDs {
		if seenSub[subID] {
			continue
		}
		seenSub[subID] = true
		sub, err := s.store.Get(subID)
		if err != nil || !sub.Enabled {
			continue // удалённая или выключенная подписка не участвует
		}
		for _, m := range sub.Members {
			if flt.Allows(m.Label) {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// stageGroups пересобирает outbound/inbound/route каждой сводной группы в
// текущем НЕзакоммиченном батче мутатора. Reload внутри НЕ вызывается —
// вызывающая сторона коммитит всё одним flush (один SIGHUP на операцию,
// группы всегда консистентны с подписками). overrides позволяет refresh-пути
// подставить свежую (ещё не записанную в store) членскую базу подписки.
func (s *Service) stageGroups(overrides map[string][]MemberInfo) {
	s.stageGroupsExcept(overrides, "")
}

// stageGroupsExcept — тело stageGroups с исключением одной группы по ID.
// Нужен DeleteGroup: во время teardown-reload строка группы ещё лежит в
// store (удаляется только после успешного коммита — retry-friendly), и без
// исключения пересборка вернула бы только что снятые сущности в тот же батч.
func (s *Service) stageGroupsExcept(overrides map[string][]MemberInfo, exceptID string) {
	if s.groups == nil {
		return
	}
	for _, g := range s.groups.List() {
		if exceptID != "" && g.ID == exceptID {
			continue
		}
		// Старый group outbound снимаем всегда (идемпотентно): при пустом
		// разрешённом наборе он не должен остаться висеть в конфиге.
		s.mutator.RemoveOutbound(g.Tag)
		if !g.Enabled {
			// Выключенная группа: outbound и route-правило снимаем, но
			// inbound оставляем — он держит занятым listen_port (иначе
			// AllocListenPort мог бы выдать его другой подписке/группе,
			// и повторное включение упёрлось бы в коллизию портов) и
			// структурно валиден без правила. Полный teardown inbound —
			// только в DeleteGroup.
			s.mutator.RemoveRouteRule(g.InboundTag, g.Tag)
			if g.ListenPort != 0 {
				if err := s.mutator.AddInbound(g.InboundTag, BuildMixedInbound(g.InboundTag, g.ListenPort)); err != nil {
					s.logWarn("subscription-group", g.ID, "stage inbound (disabled group) failed: "+err.Error())
				}
			}
			continue
		}
		tags, err := s.resolveGroupTags(g, overrides)
		if err != nil {
			// Битый фильтр из руками отредактированного файла: не паникуем,
			// снимаем сущности группы и пишем warning.
			s.logWarn("subscription-group", g.ID, "skip group (bad filter): "+err.Error())
			s.mutator.RemoveRouteRule(g.InboundTag, g.Tag)
			s.mutator.RemoveInbound(g.InboundTag)
			continue
		}
		if len(tags) == 0 {
			// Пустой selector/urltest sing-box отвергает — группу оставляем
			// в store (UI покажет «0 серверов»), но outbound не эмитим и
			// route-правило снимаем. Inbound сохраняем: он держит занятым
			// listen_port (иначе AllocListenPort мог бы выдать его другому)
			// и структурно валиден без правила.
			s.mutator.RemoveRouteRule(g.InboundTag, g.Tag)
			if g.ListenPort != 0 {
				if err := s.mutator.AddInbound(g.InboundTag, BuildMixedInbound(g.InboundTag, g.ListenPort)); err != nil {
					s.logWarn("subscription-group", g.ID, "stage inbound (empty group) failed: "+err.Error())
				}
			}
			continue
		}
		if err := s.mutator.AddOutbound(g.Tag, BuildAggregateGroupOutbound(g, tags)); err != nil {
			s.logWarn("subscription-group", g.ID, "stage outbound failed: "+err.Error())
			continue
		}
		if g.ListenPort != 0 {
			if err := s.mutator.AddInbound(g.InboundTag, BuildMixedInbound(g.InboundTag, g.ListenPort)); err != nil {
				// Inbound не встал (например, коллизия listen_port) —
				// route-правило без него ссылалось бы на несуществующий
				// inbound и валило бы весь flush. Outbound группы остаётся
				// (валиден сам по себе), точка входа появится после
				// устранения коллизии на следующей пересборке.
				s.logWarn("subscription-group", g.ID, "stage inbound failed: "+err.Error())
				continue
			}
			if err := s.mutator.AddRouteRule(BuildRouteRule(g.InboundTag, g.Tag)); err != nil {
				s.logWarn("subscription-group", g.ID, "stage route rule failed: "+err.Error())
			}
		}
	}
}

// resolveGroupTags — теговая проекция ResolveGroupMembers с поддержкой
// overrides (subID → свежие члены, ещё не записанные в store).
func (s *Service) resolveGroupTags(g AggregateGroup, overrides map[string][]MemberInfo) ([]string, error) {
	flt, err := CompileMemberFilter(g.FilterInclude, g.FilterExclude)
	if err != nil {
		return nil, err
	}
	var tags []string
	seenSub := make(map[string]bool, len(g.UseSubscriptionIDs))
	for _, subID := range g.UseSubscriptionIDs {
		if seenSub[subID] {
			continue
		}
		seenSub[subID] = true
		sub, err := s.store.Get(subID)
		if err != nil || !sub.Enabled {
			continue // удалённая или выключенная подписка не участвует
		}
		members := sub.Members
		if ov, ok := overrides[subID]; ok {
			members = ov // свежий состав из идущего refresh (ещё не в store)
		}
		for _, m := range members {
			if flt.Allows(m.Label) {
				tags = append(tags, m.Tag)
			}
		}
	}
	return tags, nil
}

// reloadWithGroups — единая точка коммита для всех мутаций подписок:
// пересобирает сводные группы в том же батче и делает один Reload.
// ВНИМАНИЕ: вызывающий обязан держать txMu (withTx) — коммит работает с
// общим батчем адаптера и не должен пересекаться с другими операциями.
func (s *Service) reloadWithGroups(ctx context.Context) error {
	s.stageGroups(nil)
	return s.mutator.Reload(ctx)
}

// validateGroupInput — общая валидация Create/UpdateGroup.
func (s *Service) validateGroupSubs(ids []string) error {
	for _, id := range ids {
		if _, err := s.store.Get(id); err != nil {
			return fmt.Errorf("%w: %s", ErrGroupSubscriptionNotFound, id)
		}
	}
	return nil
}

// CreateGroup создаёт сводную группу: валидация, alloc listen-port, alloc
// ProxyN (когда глобальный тумблер включён), материализация + один Reload.
// Зеркалит Service.Create по rollback-семантике: при неудаче частично
// созданные сущности снимаются и строка удаляется из store.
func (s *Service) CreateGroup(ctx context.Context, in GroupCreateInput) (*AggregateGroup, error) {
	if s.groups == nil {
		return nil, ErrGroupsDisabled
	}
	if strings.TrimSpace(in.Label) == "" {
		return nil, errors.New("subscription: название группы не может быть пустым")
	}
	if in.Tag != "" {
		if err := validateGroupTag(in.Tag); err != nil {
			return nil, err
		}
		if err := s.groupTagConflict(in.Tag); err != nil {
			return nil, err
		}
	}
	if _, err := CompileMemberFilter(in.FilterInclude, in.FilterExclude); err != nil {
		return nil, fmt.Errorf("subscription: %w", err)
	}
	if err := s.validateGroupSubs(in.UseSubscriptionIDs); err != nil {
		return nil, err
	}
	// Сериализуем с Create подписок: allocation сканирует без резервирования
	// (та же семантика, что и у подписок — issue #287).
	s.createMu.Lock()
	defer s.createMu.Unlock()
	s.groupMu.Lock()
	defer s.groupMu.Unlock()

	g, err := s.groups.Create(in)
	if err != nil {
		return nil, err
	}
	port, err := s.mutator.AllocListenPort()
	if err != nil {
		s.groups.Delete(g.ID)
		return nil, fmt.Errorf("subscription group: alloc listen port: %w", err)
	}
	if err := s.groups.SetListenPort(g.ID, port); err != nil {
		s.groups.Delete(g.ID)
		return nil, err
	}
	proxyIdx := -1
	if s.proxyEnabled() {
		idx, err := s.mutator.AllocProxyIndex(ctx)
		if err != nil {
			s.groups.Delete(g.ID)
			return nil, fmt.Errorf("subscription group: alloc proxy index: %w", err)
		}
		if err := s.groups.SetProxyIndex(g.ID, idx); err != nil {
			s.groups.Delete(g.ID)
			return nil, err
		}
		proxyIdx = idx
		if err := s.mutator.EnsureProxy(ctx, idx, int(port), g.Label); err != nil {
			_ = s.mutator.RemoveProxy(ctx, idx)
			s.groups.Delete(g.ID)
			return nil, fmt.Errorf("subscription group: register NDMS proxy: %w", err)
		}
	}

	// Stage (пересборка групп) + Reload и Rollback-компенсация — одна
	// txMu-секция: параллельная операция не должна закоммитить наш
	// полу-staged батч, а наш Rollback — сбросить её staged-мутации.
	if err := s.withTx(func() error {
		if err := s.reloadWithGroups(ctx); err != nil {
			s.mutator.Rollback()
			return err
		}
		return nil
	}); err != nil {
		if proxyIdx >= 0 {
			_ = s.mutator.RemoveProxy(ctx, proxyIdx)
		}
		s.groups.Delete(g.ID)
		return nil, fmt.Errorf("subscription group: materialize: %w", err)
	}

	final, err := s.groups.Get(g.ID)
	if err != nil {
		return nil, err
	}
	s.logInfo("subscription-group-create", g.ID, fmt.Sprintf("created mode=%s subs=%d listen_port=%d proxy_index=%d", final.EffectiveMode(), len(final.UseSubscriptionIDs), final.ListenPort, final.ProxyIndex))
	return final, nil
}

// UpdateGroup применяет частичный патч и пере-материализует группу
// (stage + один Reload). Смена label пробрасывается в описание NDMS Proxy.
func (s *Service) UpdateGroup(ctx context.Context, id string, patch GroupUpdatePatch) (*AggregateGroup, error) {
	if s.groups == nil {
		return nil, ErrGroupsDisabled
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()

	current, err := s.groups.Get(id)
	if err != nil {
		return nil, err
	}
	if patch.Label != nil && strings.TrimSpace(*patch.Label) == "" {
		return nil, errors.New("subscription: название группы не может быть пустым")
	}
	if patch.FilterInclude != nil || patch.FilterExclude != nil {
		newInclude, newExclude := current.FilterInclude, current.FilterExclude
		if patch.FilterInclude != nil {
			newInclude = *patch.FilterInclude
		}
		if patch.FilterExclude != nil {
			newExclude = *patch.FilterExclude
		}
		if _, err := CompileMemberFilter(newInclude, newExclude); err != nil {
			return nil, fmt.Errorf("subscription: %w", err)
		}
	}
	if patch.UseSubscriptionIDs != nil {
		if err := s.validateGroupSubs(*patch.UseSubscriptionIDs); err != nil {
			return nil, err
		}
	}
	g, err := s.groups.Update(id, patch)
	if err != nil {
		return nil, err
	}
	// Stage (пересборка групп) + Reload — одна txMu-секция (общий батч).
	if err := s.withTx(func() error { return s.reloadWithGroups(ctx) }); err != nil {
		return g, fmt.Errorf("subscription group: reload: %w", err)
	}
	if patch.Label != nil && s.proxyEnabled() && g.ProxyIndex >= 0 {
		// EnsureProxy идемпотентен — обновляет описание ProxyN «на месте».
		if err := s.mutator.EnsureProxy(ctx, g.ProxyIndex, int(g.ListenPort), g.Label); err != nil {
			return g, fmt.Errorf("subscription group: sync proxy description: %w", err)
		}
	}
	s.logInfo("subscription-group-update", id, "updated")
	return g, nil
}

// DeleteGroup сносит группу целиком: outbound, route-правило, inbound,
// NDMS ProxyN и строку в store. Teardown в слоте коммитится (Reload) ДО
// снятия прокси и удаления строки — при упавшем reload строка остаётся и
// retry возможен (зеркалит deleteLocked подписок); иначе agg-* сущности
// зависли бы в конфиге без владельца, а повторный вызов получал бы 404.
func (s *Service) DeleteGroup(ctx context.Context, id string) error {
	if s.groups == nil {
		return ErrGroupsDisabled
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()

	g, err := s.groups.Get(id)
	if err != nil {
		return err
	}
	// Teardown-мутации + Reload — одна txMu-секция (общий батч адаптера).
	if err := s.withTx(func() error {
		s.mutator.RemoveRouteRule(g.InboundTag, g.Tag)
		s.mutator.RemoveInbound(g.InboundTag)
		s.mutator.RemoveOutbound(g.Tag)
		// Строка группы ещё в store — исключаем её из пересборки, чтобы
		// stageGroups не вернул снятые сущности в этот же батч.
		s.stageGroupsExcept(nil, id)
		return s.mutator.Reload(ctx)
	}); err != nil {
		return fmt.Errorf("subscription group: delete reload: %w", err)
	}
	// Ошибка снятия прокси не блокирует удаление строки (симметрично
	// Service.Delete для подписок): осиротевший ProxyN подберёт cleanup-свип.
	if g.ProxyIndex >= 0 {
		if err := s.mutator.RemoveProxy(ctx, g.ProxyIndex); err != nil {
			s.logWarn("subscription-group-delete", id, "remove proxy failed: "+err.Error())
		}
	}
	if err := s.groups.Delete(id); err != nil {
		return err
	}
	s.logInfo("subscription-group-delete", id, "deleted")
	return nil
}
