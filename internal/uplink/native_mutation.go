package uplink

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// targetOwner — адаптер, который знает, держит ли он привязку цели.
type targetOwner interface{ OwnsTarget(string) bool }

// managesRef — туннель под политикой (любой, в том числе выключенной или
// цели незавершённого переименования) либо во владении адаптера.
func (c *Controller) managesRef(ref string) bool {
	for _, p := range c.store.MaterializationPolicies() {
		if p.Target.Ref == ref {
			return true
		}
	}
	for _, a := range c.adapters {
		if o, ok := a.(targetOwner); ok && o.OwnsTarget(ref) {
			return true
		}
	}
	return false
}

// GuardNativeMutation takes the controller before service/operator locks. A
// lifecycle change may stop/restart a tunnel; protected transport edits and
// deletion require relinquishing its policy first. Туннель вне политик и
// без привязки не ждёт ни проб чужих ref, ни gate, ни паузы бэкапа, но
// держит семафор своего ref всю операцию: Put политики на этот же ref
// (Activate) не вклинится между решением «не управляется» и разрушающим
// шагом. Решение принимается уже под семафором. Туннель политики ждёт
// gate и замок своего ref.
func (c *Controller) GuardNativeMutation(ctx context.Context, ref, operation string) (func(), error) {
	own := c.refLock(ref)
	select {
	case own <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if !c.managesRef(ref) {
		var once sync.Once
		return func() { once.Do(func() { <-own }) }, nil
	}
	<-own
	release, err := c.lockRefs(ctx, ref)
	if err != nil {
		return nil, err
	}
	if c.paused.Load() {
		release()
		return nil, fmt.Errorf("%w: uplink paused for backup", ErrConflict)
	}
	for _, p := range c.store.List() {
		if p.Enabled && (p.Target.Engine == "wireguard" || p.Target.Engine == "system-wireguard" || p.Target.Engine == "kernel-amneziawg") && p.Target.Ref == ref && operation != "lifecycle" && operation != "metadata" {
			release()
			return nil, fmt.Errorf("%w: disable uplink policy %q before changing or deleting its native tunnel", ErrConflict, p.ID)
		}
	}
	// Disabled/orphaned resources must stop overriding ordinary edits.
	if operation != "lifecycle" && operation != "metadata" {
		for _, engine := range []string{"wireguard", "system-wireguard", "kernel-amneziawg"} {
			if a, ok := c.adapters[engine].(Releaser); ok {
				if err := a.ReleaseUplink(ctx, Target{Engine: engine, Ref: ref}); err != nil {
					release()
					return nil, err
				}
			}
		}
	}
	return release, nil
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
