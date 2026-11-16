package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// AutolistTeardownResult — итог сноса объектов Autolist: сам снос прошёл
// (ошибки — вторым возвращаемым), а BackupSaved говорит, сохранился ли
// снимок удалённых объектов перед удалением. false — фронт предупреждает:
// восстановить объекты будет не из чего.
type AutolistTeardownResult struct {
	BackupSaved bool `json:"backupSaved"`
}

// Теги объектов Autolist: owner-правило ссылается на inline rule-set и
// направляет трафик в direct-outbound сателлита (литералы совпадают с
// awgmautolist/internal/awgm/writer.go: ruleSetTag, targetOutboundTag).
const (
	autolistRuleSetTag     = "awgm-autolist"
	autolistTargetOutbound = "awgm-autolist-target"
)

// teardownRuleRefsOutbound сообщает, ссылается ли правило (включая вложенные
// подправила logical) на outbound tag.
func teardownRuleRefsOutbound(r Rule, tag string) bool {
	if r.Outbound == tag {
		return true
	}
	for _, nested := range r.Rules {
		if teardownRuleRefsOutbound(nested, tag) {
			return true
		}
	}
	return false
}

// TeardownAutolistObjects полностью убирает объекты Autolist из слота router:
// owner-правило, inline rule-set awgm-autolist и outbound awgm-autolist-target.
// Идемпотентен: отсутствующий объект — пропуск. Отказ (ErrOutboundReferenced),
// пока outbound держит чужое правило или группу-композит: снос сломал бы
// чужую маршрутизацию. Все удаления едут одним draft→apply в конце; когда
// удалять нечего, apply не зовётся. Автоматики при выключении сателлита нет —
// это явная кнопка панели.
//
// Перед первым Delete снимок удалённых объектов пишется в каталог данных
// панели (CaptureAutolistSnapshot); восстановление — RestoreAutolistObjects.
// Снимок best-effort: неудача записи НЕ отменяет снос, но в ответе обработчика
// она видна как backupSaved=false (фронт предупреждает), а факт уходит в журнал.
func (s *ServiceImpl) TeardownAutolistObjects(ctx context.Context) (AutolistTeardownResult, error) {
	result := AutolistTeardownResult{BackupSaved: true}
	rules, err := s.ListRules(ctx)
	if err != nil {
		return result, err
	}
	owner := -1
	owners := 0
	for i, r := range rules {
		if isAutolistOwnerRule(r) {
			owners++
			owner = i
			continue
		}
		if teardownRuleRefsOutbound(r, autolistTargetOutbound) {
			return result, fmt.Errorf("%w: outbound %q используется правилом route.rules[%d]", ErrOutboundReferenced, autolistTargetOutbound, i)
		}
	}
	if owners > 1 {
		return result, fmt.Errorf("найдено %d безусловных owner-правил %s — неоднозначно, снос отклонён", owners, autolistRuleSetTag)
	}
	outbounds, err := s.ListCompositeOutbounds(ctx)
	if err != nil {
		return result, err
	}
	for _, ob := range outbounds {
		for _, member := range ob.Outbounds {
			if member == autolistTargetOutbound {
				return result, fmt.Errorf("%w: outbound %q входит в группу %q", ErrOutboundReferenced, autolistTargetOutbound, ob.Tag)
			}
		}
	}

	// Снимок ДО первого Delete и после всех проверок-отказов: удаляемое
	// состояние зафиксировано, отказов сноса уже не будет. Best-effort:
	// сорванный снимок не отменяет снос, но делает его видимым как
	// backupSaved=false и в журнале.
	if err := s.CaptureAutolistSnapshot(ctx); err != nil {
		result.BackupSaved = false
		s.appLog.Warn("autolist-teardown", "", "снимок удалённых объектов не сохранён: "+err.Error())
	}

	// Порядок writer'а сателлита (writer.go removeRuleSetEntry): сперва
	// owner-правило, затем rule-set, затем outbound. Индексы между шагами не
	// переиспользуются: каждое удаление перевычитывает актуальный список.
	deleted := false
	if owner >= 0 {
		if err := s.DeleteRule(ctx, owner); err != nil {
			return result, err
		}
		deleted = true
	}
	sets, err := s.ListRuleSets(ctx)
	if err != nil {
		return result, err
	}
	for _, rs := range sets {
		if rs.Tag != autolistRuleSetTag {
			continue
		}
		// force: ссылок на набор после сноса owner-правила быть не должно,
		// но force гарантирует, что и неожиданная ссылка не оставит набор
		// висеть (осиротевшие правила набор-референсы чистятся на месте).
		if err := s.DeleteRuleSet(ctx, autolistRuleSetTag, true); err != nil {
			return result, err
		}
		deleted = true
		break
	}
	for _, ob := range outbounds {
		if ob.Tag != autolistTargetOutbound {
			continue
		}
		if err := s.DeleteCompositeOutbound(ctx, autolistTargetOutbound, true); err != nil {
			return result, err
		}
		deleted = true
		break
	}
	if deleted {
		res, err := s.ApplyStaging(ctx)
		// Черновика могло не оказаться: persistConfig сбрасывает фантомный
		// черновик при байт-равенстве материализованного конфига активному.
		if err != nil && !errors.Is(err, orchestrator.ErrNoDraft) {
			return result, err
		}
		if err == nil && !res.Ok() {
			return result, fmt.Errorf("teardown отклонён валидацией применяемого конфига: %s", res.Error())
		}
	}
	if err := s.verifyAutolistGone(ctx); err != nil {
		return result, err
	}
	return result, nil
}

// verifyAutolistGone перечитывает конфиг и доказывает отсутствие всех трёх
// объектов — то же доказательство по прочитанному состоянию, что и у writer'а
// сателлита после apply (AWGM-правки last-write-wins, решает только readback).
func (s *ServiceImpl) verifyAutolistGone(ctx context.Context) error {
	rules, err := s.ListRules(ctx)
	if err != nil {
		return err
	}
	for i, r := range rules {
		if isAutolistOwnerRule(r) || teardownRuleRefsOutbound(r, autolistTargetOutbound) {
			return fmt.Errorf("readback: объекты Autolist ещё присутствуют (route.rules[%d])", i)
		}
	}
	sets, err := s.ListRuleSets(ctx)
	if err != nil {
		return err
	}
	for _, rs := range sets {
		if rs.Tag == autolistRuleSetTag {
			return fmt.Errorf("readback: rule-set %q ещё присутствует", autolistRuleSetTag)
		}
	}
	outbounds, err := s.ListCompositeOutbounds(ctx)
	if err != nil {
		return err
	}
	for _, ob := range outbounds {
		if ob.Tag == autolistTargetOutbound {
			return fmt.Errorf("readback: outbound %q ещё присутствует", autolistTargetOutbound)
		}
	}
	return nil
}
