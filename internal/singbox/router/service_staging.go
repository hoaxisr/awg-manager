package router

import (
	"context"
	"encoding/json"
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
	if err := s.syncDraftTunExternal(); err != nil {
		s.appLog.Warn("staging", "tun-in", "выровнять external_configuration черновика: "+err.Error())
	}
	res, err := s.deps.Orch.ApplyDraft(orchestrator.SlotRouter)
	if err == nil && res.Ok() {
		// A staged rule-set delete/rename is final now — reap the orphaned
		// inline/dat artifacts (issue #448: files were never deleted).
		s.GCRuleSetArtifacts()
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
	_, err = m.materializeConfig(orchestrator.SlotRouter, m.restoreConfig(cfg))
	return err
}

// syncDraftTunExternal переносит флаг external_configuration tun-in из
// применённого слота в черновик. Черновик хранит снимок момента создания, а
// флаг — не пользовательское поле: переключает его только тик
// (healTunSettings → completeExternalFlip). Устаревший снимок при применении
// перекинул бы флаг мимо тика — лишние SIGHUP, а для чужого бинаря отказ check.
// Правка по сырому JSON: черновик уже материализован, round-trip через
// RouterConfig его бы пересобрал.
func (s *ServiceImpl) syncDraftTunExternal() error {
	if !s.deps.Orch.HasDraft(orchestrator.SlotRouter) {
		return nil
	}
	applied, err := s.loadAppliedRouterConfig()
	if err != nil {
		return err
	}
	want, found := false, false
	for _, in := range applied.Inbounds {
		if in.Tag == "tun-in" {
			want, found = in.ExternalConfiguration, true
		}
	}
	if !found {
		return nil
	}
	raw, err := s.deps.Orch.LoadEffective(orchestrator.SlotRouter)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	inbounds, _ := doc["inbounds"].([]any)
	changed := false
	for _, it := range inbounds {
		in, ok := it.(map[string]any)
		if !ok || in["tag"] != "tun-in" {
			continue
		}
		if have, _ := in["external_configuration"].(bool); have != want {
			if want {
				in["external_configuration"] = true
			} else {
				delete(in, "external_configuration")
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return s.deps.Orch.SaveDraft(orchestrator.SlotRouter, out)
}
