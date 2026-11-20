package uplink

import (
	"context"
	"fmt"
)

type RenameIntent struct {
	From Target `json:"from"`
	To   Target `json:"to"`
}
type TargetLookup interface {
	TargetExists(context.Context, Target) (bool, error)
}

// Read-only snapshot for engine writers. It never takes the controller lock.
// Include the destination during a journalled rename so the engine's config
// write is protected before the policy mapping is durably committed.
func (s *Store) MaterializationPolicies() []Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []Policy
	for _, p := range s.policies {
		result = append(result, copyPolicy(p))
		if s.pendingRename != nil && p.Target == s.pendingRename.From {
			p.Target = s.pendingRename.To
			result = append(result, copyPolicy(p))
		}
	}
	return result
}

func (s *Store) renameIntent() *RenameIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingRename == nil {
		return nil
	}
	cp := *s.pendingRename
	return &cp
}
func (s *Store) beginRename(from, to Target) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingRename != nil {
		return false, ErrConflict
	}
	affected := false
	for _, p := range s.policies {
		if p.Target == to {
			return false, fmt.Errorf("%w: destination already has a policy", ErrConflict)
		}
		if p.Target == from {
			affected = true
			p.Target = to
			if err := p.Validate(); err != nil {
				return false, err
			}
		}
	}
	if !affected {
		return false, nil
	}
	s.pendingRename = &RenameIntent{From: from, To: to}
	if err := s.save(s.policies); err != nil {
		s.pendingRename = nil
		return false, err
	}
	return true, nil
}
func (s *Store) finishRename(commit bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent := s.pendingRename
	if intent == nil {
		return "", nil
	}
	next := make(map[string]Policy, len(s.policies))
	id := ""
	for key, p := range s.policies {
		if commit && p.Target == intent.From {
			p.Target = intent.To
			p.Revision++
			id = key
		}
		next[key] = p
	}
	s.pendingRename = nil
	if err := s.save(next); err != nil {
		s.pendingRename = intent
		return "", err
	}
	s.policies = next
	return id, nil
}

// RenameTarget journals intent before the engine mutation. A crash at either
// side of the two durable writes is reconciled by inspecting both actual IDs.
func (c *Controller) RenameTarget(ctx context.Context, from, to Target, apply func() error) error {
	if from.Engine != to.Engine || from.Kind != to.Kind {
		return ErrUnsupported
	}
	guard, err := c.AcquireEngineTransition(ctx)
	if err != nil {
		return err
	}
	defer guard.Release()
	if c.paused {
		return fmt.Errorf("%w: uplink controller paused for backup", ErrConflict)
	}
	if err := c.recoverRename(ctx); err != nil {
		return err
	}
	if from == to {
		return apply()
	}
	owned := false
	for _, p := range c.store.List() {
		if p.Target == from {
			owned = true
		}
	}
	if !owned {
		return apply()
	}
	lookup, ok := c.adapters[from.Engine].(TargetLookup)
	if !ok {
		return ErrUnsupported
	}
	oldExists, err := lookup.TargetExists(ctx, from)
	if err != nil {
		return err
	}
	if !oldExists {
		return ErrNotFound
	}
	exists, err := lookup.TargetExists(ctx, to)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: rename destination exists", ErrConflict)
	}
	if _, err := c.store.beginRename(from, to); err != nil {
		return err
	}
	applyErr := apply()
	// Use observations even on an apply error: an error after a durable config
	// write must not roll the policy back to an ID that no longer exists.
	if err := c.recoverRename(ctx); err != nil {
		return fmt.Errorf("rename recovery required: %w", err)
	}
	return applyErr
}
func (c *Controller) recoverRename(ctx context.Context) error {
	intent := c.store.renameIntent()
	if intent == nil {
		return nil
	}
	lookup, ok := c.adapters[intent.From.Engine].(TargetLookup)
	if !ok {
		return ErrUnsupported
	}
	old, err := lookup.TargetExists(ctx, intent.From)
	if err != nil {
		return err
	}
	next, err := lookup.TargetExists(ctx, intent.To)
	if err != nil {
		return err
	}
	if old == next {
		return fmt.Errorf("%w: ambiguous uplink rename; restore exactly one target before retrying", ErrConflict)
	}
	id, err := c.store.finishRename(next)
	if err != nil {
		return err
	}
	if id != "" {
		c.publish(id, State{Phase: "pending"})
	}
	return nil
}
