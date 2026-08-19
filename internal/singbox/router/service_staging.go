package router

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// StagingStatus is what /api/singbox/router/staging returns.
type StagingStatus struct {
	HasDraft   bool
	DraftedAt  time.Time
	Validation *orchestrator.ValidationResult
}

// StagingStatus returns metadata about the current pending draft for the
// router slot. When a draft exists, Validation is populated with the
// current cross-slot diagnostic so the UI can render a preview of "what
// Apply would say".
func (s *ServiceImpl) StagingStatus(ctx context.Context) StagingStatus {
	info := s.deps.Orch.DraftInfo(orchestrator.SlotRouter)
	st := StagingStatus{HasDraft: info.HasDraft, DraftedAt: info.DraftedAt}
	if !info.HasDraft {
		return st
	}
	bytes, err := s.deps.Orch.LoadEffective(orchestrator.SlotRouter)
	if err != nil || bytes == nil {
		return st
	}
	res := s.deps.Orch.ValidateDraft(orchestrator.SlotRouter, bytes)
	st.Validation = &res
	return st
}

// ApplyStaging is the service-level wrapper around Orch.ApplyDraft. On
// success it emits "singbox.router.staging" + "singbox.router.rules" SSE
// invalidations.
func (s *ServiceImpl) ApplyStaging(ctx context.Context) (orchestrator.ValidationResult, error) {
	res, err := s.deps.Orch.ApplyDraft(orchestrator.SlotRouter)
	if err == nil && res.Ok() {
		// A staged rule-set delete/rename is final now — reap the orphaned
		// inline/dat artifacts (issue #448: files were never deleted).
		s.GCRuleSetArtifacts()
		s.reconcileBaseDNSStrategy()
		s.emitStagingEvent("applied")
		s.emitRulesEvent()
	}
	return res, err
}

// DiscardStaging removes the pending draft for the router slot.
func (s *ServiceImpl) DiscardStaging(ctx context.Context) error {
	if err := s.deps.Orch.DiscardDraft(orchestrator.SlotRouter); err != nil {
		return err
	}
	if err := s.restoreEffectiveRuleSetArtifacts(); err != nil {
		return err
	}
	s.emitStagingEvent("discarded")
	s.emitRulesEvent()
	return nil
}

func (s *ServiceImpl) restoreEffectiveRuleSetArtifacts() error {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return err
	}
	m := s.ruleSetMaterializer()
	_, err = m.materializeConfig(m.restoreConfig(cfg))
	return err
}

// reconcileBaseDNSStrategy зовёт опциональный dep после того, как содержимое
// routing-слота стало активным. Best-effort: изменение слота уже применено,
// ошибка примирения обязана попасть в app-лог, но не отменять успех.
func (s *ServiceImpl) reconcileBaseDNSStrategy() {
	if s.deps.ReconcileBaseDNSStrategy == nil {
		return
	}
	if err := s.deps.ReconcileBaseDNSStrategy(); err != nil {
		s.appLog.Warn("dns-globals", "", "reconcile base dns.strategy: "+err.Error())
	}
}
