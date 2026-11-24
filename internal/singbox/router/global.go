package router

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

const globalRetryAfter = 10 * time.Minute

func (s *ServiceImpl) reconcileGlobal(sr storage.SingboxRouterSettings, stopped bool) error {
	orch := s.deps.Orch
	if orch == nil {
		return nil
	}
	slot := orchestrator.SlotGlobal
	active := s.slotEnabled(slot)
	if stopped || !sr.Enabled || sr.GlobalOutbound == "" {
		s.globalFailed = nil
		if active {
			return orch.SetEnabled(slot, false)
		}
		return nil
	}
	// Keep DNS and private destinations on the existing system routing path.
	rule := map[string]any{"type": "logical", "mode": "and", "rules": []any{
		map[string]any{"inbound": []string{"tun-in", "redirect-in", "tproxy-in"}},
		map[string]any{"port": 53, "invert": true},
		map[string]any{"ip_is_private": true, "invert": true},
		map[string]any{"ip_cidr": []string{"100.64.0.0/10"}, "invert": true},
	}, "action": "route", "outbound": sr.GlobalOutbound}
	data, err := json.Marshal(map[string]any{"route": map[string]any{"rules": []any{rule}}})
	if err != nil {
		return err
	}
	old, err := orch.LoadApplied(slot)
	if err != nil {
		return err
	}
	if active && bytes.Equal(old, data) {
		return nil
	}
	// Тот же слот уже не взлетел: повтор на каждом тике давал бы запись на
	// флеш и полный reload раз в 30 с. Повтор — раз в globalRetryAfter: ссылку
	// мог починить другой слот (появился outbound), настройки при этом те же.
	if bytes.Equal(data, s.globalFailed) && time.Since(s.globalFailedAt) < globalRetryAfter {
		return nil
	}
	if err := orch.SaveSilent(slot, data); err != nil {
		return err
	}
	if err := orch.SetEnabledSilent(slot, true); err != nil {
		return err
	}
	if err := orch.ReloadNow(); err != nil {
		s.globalFailed, s.globalFailedAt = data, time.Now()
		if active {
			if restore := orch.Save(slot, old); restore != nil {
				return restore
			}
		} else if restore := orch.SetEnabled(slot, false); restore != nil {
			return restore
		}
		return err
	}
	s.globalFailed = nil
	return nil
}
