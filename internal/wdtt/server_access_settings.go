package wdtt

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
)

// SetServerNATMode меняет режим NAT и применяет доступ (как managed AWG SetNATMode).
func (s *Service) SetServerNATMode(ctx context.Context, id, mode string) (ServerConfig, error) {
	mode = normalizeNatMode(mode)
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return ServerConfig{}, err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		s.mu.Unlock()
		return ServerConfig{}, fmt.Errorf("сервер %q не найден", id)
	}
	prev := full.Servers[idx].Config
	if normalizeNatMode(prev.NatMode) == mode {
		s.mu.Unlock()
		return prev, nil
	}
	cfg := prev
	cfg.NatMode = mode
	s.mu.Unlock()
	return s.UpdateServerInstance(id, cfg)
}

// SetServerPolicy меняет политику маршрутизации и применяет доступ (как managed AWG SetPolicy).
func (s *Service) SetServerPolicy(ctx context.Context, id, policy string) (ServerConfig, error) {
	if policy == "" {
		return ServerConfig{}, fmt.Errorf("policy must not be empty")
	}
	policy = normalizePolicy(policy)
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return ServerConfig{}, err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		s.mu.Unlock()
		return ServerConfig{}, fmt.Errorf("сервер %q не найден", id)
	}
	prev := full.Servers[idx].Config
	if normalizePolicy(prev.Policy) == policy {
		s.mu.Unlock()
		return prev, nil
	}
	s.mu.Unlock()

	if err := s.validateServerPolicy(ctx, policy); err != nil {
		return ServerConfig{}, err
	}

	cfg := prev
	cfg.Policy = policy
	return s.UpdateServerInstance(id, cfg)
}

// SetServerLANSegments меняет доступ в LAN и применяет ACL (как managed AWG SetLANSegments).
func (s *Service) SetServerLANSegments(ctx context.Context, id string, segments []string) (ServerConfig, error) {
	if segments == nil {
		segments = []string{}
	}
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return ServerConfig{}, err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		s.mu.Unlock()
		return ServerConfig{}, fmt.Errorf("сервер %q не найден", id)
	}
	prev := full.Servers[idx].Config
	if stringSliceEqual(prev.LanSegments, segments) {
		s.mu.Unlock()
		return prev, nil
	}
	cfg := prev
	cfg.LanSegments = append([]string(nil), segments...)
	s.mu.Unlock()
	return s.UpdateServerInstance(id, cfg)
}

func (s *Service) validateServerPolicy(ctx context.Context, policy string) error {
	if policy == "none" {
		return nil
	}
	if s.policyList == nil {
		return nil
	}
	policies, err := s.policyList.List(ctx)
	if err != nil {
		return fmt.Errorf("list policies: %w", err)
	}
	for _, p := range policies {
		if !accesspolicy.IsStandardPolicyName(p.Name) {
			continue
		}
		if p.Name == policy {
			return nil
		}
	}
	return fmt.Errorf("unknown policy: %s", policy)
}
