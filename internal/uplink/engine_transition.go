package uplink

import (
	"context"
	"fmt"
	"sync"
)

// EngineTransition holds the controller before any engine operator lock. This
// excludes probes, activations and policy changes throughout a lifecycle change.
type EngineTransition struct {
	controller *Controller
	once       sync.Once
}

func (c *Controller) AcquireEngineTransition(ctx context.Context) (*EngineTransition, error) {
	if err := c.gate.lockExclusive(ctx); err != nil {
		return nil, err
	}
	return &EngineTransition{controller: c}, nil
}

// CheckTarget deliberately refuses implicit migration. Disabled policies remain
// durable and may be explicitly remapped before being enabled on another engine.
func (g *EngineTransition) CheckTarget(engine string) error {
	if g.controller.paused.Load() {
		return fmt.Errorf("%w: uplink controller paused for backup", ErrConflict)
	}
	if g.controller.store.renameIntent() != nil {
		return fmt.Errorf("%w: uplink target rename needs recovery", ErrConflict)
	}
	for _, p := range g.controller.store.List() {
		if g.controller.Capabilities(p.Target).IndependentEngine {
			continue
		}
		if p.Enabled && p.Target.Engine != engine {
			if engine == "" {
				return fmt.Errorf("%w: disable uplink policy %q before migrating engine configuration; policy target migration must be explicit", ErrConflict, p.ID)
			}
			return fmt.Errorf("%w: policy %q protects %s/%s; disable it before switching to %s (automatic policy migration is unavailable)", ErrConflict, p.ID, p.Target.Engine, p.Target.Ref, engine)
		}
	}
	return nil
}

func (g *EngineTransition) Release() { g.once.Do(g.controller.gate.unlockExclusive) }
