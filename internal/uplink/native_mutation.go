package uplink

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// GuardNativeMutation takes the controller before service/operator locks. A
// lifecycle change may stop/restart a tunnel; protected transport edits and
// deletion require relinquishing its policy first.
func (c *Controller) GuardNativeMutation(ctx context.Context, ref, operation string) (func(), error) {
	lease, err := c.AcquireEngineTransition(ctx)
	if err != nil {
		return nil, err
	}
	if c.paused {
		lease.Release()
		return nil, fmt.Errorf("%w: uplink paused for backup", ErrConflict)
	}
	for _, p := range c.store.List() {
		if p.Enabled && (p.Target.Engine == "wireguard" || p.Target.Engine == "system-wireguard" || p.Target.Engine == "kernel-amneziawg") && p.Target.Ref == ref && operation != "lifecycle" && operation != "metadata" {
			lease.Release()
			return nil, fmt.Errorf("%w: disable uplink policy %q before changing or deleting its native tunnel", ErrConflict, p.ID)
		}
	}
	// Disabled/orphaned resources must stop overriding ordinary edits.
	if operation != "lifecycle" && operation != "metadata" {
		for _, engine := range []string{"wireguard", "system-wireguard", "kernel-amneziawg"} {
			if a, ok := c.adapters[engine].(Releaser); ok {
				if err := a.ReleaseUplink(ctx, Target{Engine: engine, Ref: ref}); err != nil {
					lease.Release()
					return nil, err
				}
			}
		}
	}
	return lease.Release, nil
}

func (c *Controller) Targets(ctx context.Context) ([]TargetInfo, error) {
	var targets []TargetInfo
	var failures []error
	for _, a := range c.adapters {
		if l, ok := a.(TargetLister); ok {
			v, err := l.UplinkTargets(ctx)
			targets = append(targets, v...)
			if err != nil {
				failures = append(failures, err)
				continue
			}
		}
	}
	if c.TargetCatalog != nil {
		v, err := c.TargetCatalog(ctx)
		targets = append(targets, v...)
		if err != nil {
			failures = append(failures, err)
		}
	}
	unique := map[Target]TargetInfo{}
	for _, target := range targets {
		if _, exists := unique[target.Target]; !exists {
			unique[target.Target] = target
		}
	}
	targets = targets[:0]
	for _, target := range unique {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Target.Engine != targets[j].Target.Engine {
			return targets[i].Target.Engine < targets[j].Target.Engine
		}
		if targets[i].Target.Kind != targets[j].Target.Kind {
			return targets[i].Target.Kind < targets[j].Target.Kind
		}
		return targets[i].Target.Ref < targets[j].Target.Ref
	})
	return targets, errors.Join(failures...)
}
