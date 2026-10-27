package router

import (
	"context"
	"encoding/json"
	"reflect"
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
	// Под transitionMu: тот же 20-router.json пишут смена режима и
	// UpdateSettings (reapplyRouterSettings) — иначе перенос WAN/сниффера в
	// черновик и запись применённого слота гоняются (F703/F704).
	if !s.transitionMu.TryLock() {
		return orchestrator.ValidationResult{}, ErrTransitionInProgress
	}
	defer s.transitionMu.Unlock()
	// Черновик пишет и withConfig: правка, вставшая между переносом в
	// черновик и его применением, иначе не попала бы в применённое (F741).
	res, err := func() (orchestrator.ValidationResult, error) {
		s.routerCfgMu.Lock()
		defer s.routerCfgMu.Unlock()
		if err := s.syncDraftTunExternal(); err != nil {
			s.appLog.Warn("staging", "tun-in", "выровнять external_configuration черновика: "+err.Error())
		}
		if err := s.syncDraftRouterSettings(); err != nil {
			s.appLog.Warn("staging", "router-settings", "выровнять WAN/сниффер черновика: "+err.Error())
		}
		return s.deps.Orch.ApplyDraft(orchestrator.SlotRouter)
	}()
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
	// Под routerCfgMu: withConfig, стоящий в мутаторе, иначе записал бы после
	// сброса черновик со сброшенными правками (F741).
	if err := func() error {
		s.routerCfgMu.Lock()
		defer s.routerCfgMu.Unlock()
		if err := s.deps.Orch.DiscardDraft(orchestrator.SlotRouter); err != nil {
			return err
		}
		return s.restoreEffectiveRuleSetArtifacts()
	}(); err != nil {
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

// routeWANKeys — поля route, которыми владеют настройки (выбор WAN,
// EnsureRouteWAN), а не пользователь.
var routeWANKeys = []string{"auto_detect_interface", "default_interface"}

// syncDraftRouterSettings переносит в черновик то, чем в слоте владеют
// настройки: выбор WAN из применённого слота (F703) и наличие системного
// правила sniff по snifferEnabled (F704). Черновик — снимок момента первой
// правки, и его применение вернуло бы прежнее мимо настроек. Правка по сырому
// JSON — как у syncDraftTunExternal.
func (s *ServiceImpl) syncDraftRouterSettings() error {
	if !s.deps.Orch.HasDraft(orchestrator.SlotRouter) {
		return nil
	}
	rawApplied, err := s.deps.Orch.LoadApplied(orchestrator.SlotRouter)
	if err != nil || rawApplied == nil {
		return err
	}
	var applied struct {
		Route map[string]any `json:"route"`
	}
	if err := json.Unmarshal(rawApplied, &applied); err != nil {
		return err
	}
	raw, err := s.deps.Orch.LoadEffective(orchestrator.SlotRouter)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	route, _ := doc["route"].(map[string]any)
	if route == nil {
		route = map[string]any{}
		doc["route"] = route
	}
	changed := false
	if settings, err := s.deps.Settings.Get(); err == nil {
		changed = syncDraftSniffRule(route, settings.SingboxRouter.SnifferEnabled)
	}
	for _, k := range routeWANKeys {
		want, ok := applied.Route[k]
		have, had := route[k]
		switch {
		case ok && (!had || !reflect.DeepEqual(have, want)):
			route[k] = want
			changed = true
		case !ok && had:
			delete(route, k)
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

// syncDraftSniffRule приводит системное правило sniff (`{"action":"sniff"}`
// без условий, см. isSystemSniffRule) в route.rules черновика к want: снимает
// все такие правила или ставит одно первым, как EnsureSystemRules.
func syncDraftSniffRule(route map[string]any, want bool) bool {
	rules, _ := route["rules"].([]any)
	kept := make([]any, 0, len(rules))
	has := false
	for _, v := range rules {
		if m, ok := v.(map[string]any); ok && len(m) == 1 && m["action"] == "sniff" {
			has = true
			if !want {
				continue
			}
		}
		kept = append(kept, v)
	}
	switch {
	case want && !has:
		route["rules"] = append([]any{map[string]any{"action": "sniff"}}, kept...)
		return true
	case !want && has:
		route["rules"] = kept
		return true
	}
	return false
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
