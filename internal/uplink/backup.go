package uplink

import (
	"context"
	"fmt"
)

// Pause waits for all in-flight probes/activations, then rejects mutations
// until Resume or daemon restart. No lease is held across engine callbacks.
func (c *Controller) Pause(ctx context.Context) error {
	lease, err := c.AcquireEngineTransition(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	if c.paused {
		return fmt.Errorf("%w: backup already holds uplink controller", ErrConflict)
	}
	c.paused = true
	c.backupPaused.Store(true)
	for _, p := range c.store.List() {
		c.publish(p.ID, State{Phase: "paused"})
	}
	return nil
}
func (c *Controller) Resume() {
	c.mu.Lock()
	c.paused = false
	c.backupPaused.Store(false)
	for _, p := range c.store.List() {
		c.publish(p.ID, State{Phase: "pending"})
	}
	c.mu.Unlock()
	c.NotifyWANChange()
}

// BackupPaused can be inspected under a lifecycle lock without taking the
// controller in reverse lock order.
func (c *Controller) BackupPaused() bool { return c.backupPaused.Load() }

// BeginBackupResume permits the orchestrator to restore child services after
// export while policy mutations and controller probes remain paused. Import
// never enters this stage: it waits for daemon restart.
func (c *Controller) BeginBackupResume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused {
		c.backupPaused.Store(false)
	}
}
