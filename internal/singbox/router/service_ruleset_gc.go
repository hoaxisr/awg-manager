package router

import (
	"errors"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// GCRuleSetArtifacts removes rule-set artifact files (rule-sets/inline/*.{json,srs},
// rule-sets/dat/*.{json,srs,meta.json}) that no config references anymore
// (issue #448: deletes and renames of rule-sets never cleaned their files).
//
// The referenced set is the UNION of the APPLIED configs (active/, then
// disabled/ — what sing-box may be running) and the PENDING drafts (what the
// user is editing), for BOTH the router and fakeip slots — both slots
// materialize into the same rule-sets/ dirs. A rule-set deleted only in the
// pending draft therefore keeps its files until the draft is applied
// (TestDeleteRuleSet_StagedInlineKeepsSRSCompanionFiles), and a discarded
// draft never loses the active artifacts.
//
// Called after a successful ApplyStaging and once at boot (cmd/awg-manager)
// to reap historical leftovers. Best-effort: any config read error aborts the
// sweep (fail-safe — never GC against an incomplete referenced set).
func (s *ServiceImpl) GCRuleSetArtifacts() {
	referenced, err := s.referencedRuleSetArtifactBases()
	if err != nil {
		s.appLog.Warn("gc", "rule-sets", "skip rule-set artifact GC: "+err.Error())
		return
	}
	s.ruleSetMaterializer().gcArtifacts(referenced)
}

// referencedRuleSetArtifactBases builds the set of artifact base names still
// referenced by any router/fakeip config (applied or pending).
func (s *ServiceImpl) referencedRuleSetArtifactBases() (map[string]struct{}, error) {
	referenced := make(map[string]struct{})
	// Слот указывается ОДИН раз на слот, а не на каждый его конфиг: базис
	// артефакта несёт префикс слота (F435), и перепутанный слот у одного из
	// двух конфигов сносил бы чужой файл молча. Тестом это не наблюдается —
	// записи реального конфига несут путь и пришпиливаются по нему, — так что
	// форма кода здесь и есть единственная защита.
	add := func(slot orchestrator.Slot, cfgs ...*RouterConfig) {
		for _, cfg := range cfgs {
			addRuleSetArtifactBases(referenced, slot, cfg)
		}
	}

	// Router slot: effective (pending draft if present, else active) + applied.
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	appliedRouter, err := s.loadAppliedRouterConfig()
	if err != nil {
		return nil, err
	}
	add(orchestrator.SlotRouter, cfg, appliedRouter)

	// FakeIP slot (orch-only): it materializes into the same rule-sets/ dirs.
	if s.deps.Orch != nil {
		fakeip, err := s.loadFakeIPConfig()
		if err != nil {
			return nil, err
		}
		var appliedFakeIP *RouterConfig
		data, err := s.deps.Orch.LoadApplied(orchestrator.SlotFakeIP)
		if err != nil {
			if !errors.Is(err, orchestrator.ErrUnknownSlot) {
				return nil, err
			}
		} else if data != nil {
			appliedFakeIP, err = parseRouterConfigBytes(data)
			if err != nil {
				return nil, err
			}
		}
		add(orchestrator.SlotFakeIP, fakeip, appliedFakeIP)
	}
	return referenced, nil
}

// addRuleSetArtifactBases records every artifact base name cfg can reference:
//   - inline rule-sets and their materialized SRS companions — the file base
//     is inlineArtifactBase(slot, <inline tag>), то есть префикс слота плюс имя
//     из тега (materializeRuleSet называет файлы так же, а запись-компаньон
//     несёт тег "<tag>-srs"); оба варианта тега добавляются на всякий случай;
//   - фактический путь записи (см. ниже) — страховка для имён прежних схем;
//   - remote rule-sets whose URL points at the local dat-srs endpoint — the
//     base is datRuleSetBaseName(kind, tags), matching DatRuleSetFile.
func addRuleSetArtifactBases(set map[string]struct{}, slot orchestrator.Slot, cfg *RouterConfig) {
	if cfg == nil {
		return
	}
	for _, rs := range cfg.Route.RuleSet {
		if rs.Tag != "" {
			for _, tag := range ruleSetTagsWithCompanion(rs.Tag) {
				set[inlineArtifactBase(slot, tag)] = struct{}{}
			}
			if base, ok := inlineTagFromSRSTag(rs.Tag); ok {
				set[inlineArtifactBase(slot, base)] = struct{}{}
			}
		}
		// Страховка по фактическому пути: имя файла в конфиге может НЕ
		// совпадать с тем, что считается из тега сегодня — так выглядят
		// артефакты, материализованные прежней схемой именования (без
		// префикса слота, F435). Правила inline-набора живут только в этом
		// файле, поэтому пока запись на него ссылается, sweep обязан его
		// щадить; после ближайшей материализации ссылка станет новой, и
		// осиротевший старый файл уберётся сам.
		if base, ok := ruleSetArtifactBase(filepath.Base(rs.Path)); ok && rs.Path != "" {
			set[base] = struct{}{}
		}
		if kind, tags, ok := parseDatRuleSetURL(rs.URL); ok {
			set[datRuleSetBaseName(kind, tags)] = struct{}{}
		}
	}
}
