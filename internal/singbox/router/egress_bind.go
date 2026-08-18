package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// SingboxTunnelEditor patches sing-box tunnel outbounds (10-tunnels.json).
// Optional dep — when nil, composite egress bind is stored but not propagated.
type SingboxTunnelEditor interface {
	GetTunnelOutbound(ctx context.Context, tag string) (json.RawMessage, error)
	// UpdateTunnelOutbounds applies the patch immediately (write +
	// sing-box check + SIGHUP). Use for user-acknowledged "Apply" flows
	// that are not part of a multi-slot draft.
	UpdateTunnelOutbounds(ctx context.Context, updates map[string]json.RawMessage) error
	// StageTunnelOutboundUpdates writes the merged 10-tunnels.json to
	// the orchestrator's pending/ directory so bind changes ride the
	// same draft as the composite group edit (#709, PR #732 review
	// blocker #2). When the orchestrator is not wired, implementations
	// must fall back to UpdateTunnelOutbounds.
	StageTunnelOutboundUpdates(ctx context.Context, updates map[string]json.RawMessage) error
	// IsSingboxTunnelTag reports whether tag names a sing-box tunnel
	// outbound (i.e. something the editor can patch). The error
	// return matters: silently swallowing it (returning false on
	// any error) would drop the bind change without telling the
	// caller, so callers must propagate the error all the way to
	// the API response — #709, PR #732 review non-blocker #10.
	IsSingboxTunnelTag(ctx context.Context, tag string) (bool, error)
}

func (s *ServiceImpl) compositeEgressBinds() map[string]string {
	if s.deps.Settings == nil {
		return nil
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return nil
	}
	return settings.SingboxRouter.CompositeEgressBinds
}

func (s *ServiceImpl) setCompositeEgressBind(compositeTag, bind string) error {
	if s.deps.Settings == nil {
		return fmt.Errorf("settings store not configured")
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	tag := strings.TrimSpace(compositeTag)
	bind = strings.TrimSpace(bind)
	if tag == "" {
		return fmt.Errorf("composite tag is required")
	}
	m := settings.SingboxRouter.CompositeEgressBinds
	if m == nil {
		m = make(map[string]string)
	}
	if bind == "" {
		delete(m, tag)
	} else {
		m[tag] = bind
	}
	if len(m) == 0 {
		settings.SingboxRouter.CompositeEgressBinds = nil
	} else {
		settings.SingboxRouter.CompositeEgressBinds = m
	}
	return s.deps.Settings.Save(settings)
}

func (s *ServiceImpl) deleteCompositeEgressBind(compositeTag string) error {
	return s.setCompositeEgressBind(compositeTag, "")
}

func (s *ServiceImpl) otherGroupBindForMember(currentGroupTag, member string) string {
	binds := s.compositeEgressBinds()
	if len(binds) == 0 {
		return ""
	}
	cfg, err := s.loadRouterConfig()
	if err != nil || cfg == nil {
		return ""
	}
	for _, o := range cfg.CompositeOutbounds() {
		if o.Tag == currentGroupTag {
			continue
		}
		bind := strings.TrimSpace(binds[o.Tag])
		if bind == "" {
			continue
		}
		for _, m := range o.Outbounds {
			if strings.TrimSpace(m) == member {
				return bind
			}
		}
	}
	return ""
}

func (s *ServiceImpl) applyCompositeEgressBind(ctx context.Context, oldMembers, newMembers []string, tag string, bind string) error {
	bind = strings.TrimSpace(bind)
	if err := validateBindInterfaceOptional(ctx, s, bind); err != nil {
		return err
	}

	oldBind := ""
	if binds := s.compositeEgressBinds(); binds != nil {
		oldBind = strings.TrimSpace(binds[tag])
	}

	// Self-heal: if the requested bind points at a kernel interface
	// that has disappeared (USB modem unplugged, NDMS proxy down,
	// etc.), the downstream sing-box will FATAL-loop. We must never
	// patch a tunnel with a bind_interface that /sys/class/net cannot
	// confirm. We also defensively strip such a bind from any member
	// that still carries it, so the next reload lands on a clean
	// tunnels file. The settings store is left untouched here — the
	// group-level egress_bind survives as user intent and will be
	// re-applied automatically once the interface reappears.
	bindValid := bind == "" || kernelInterfaceExists(bind)

	if s.deps.SingboxTunnelsEditor != nil {
		updates := make(map[string]json.RawMessage)

		// 1. Members being removed from the composite group
		for _, member := range oldMembers {
			member = strings.TrimSpace(member)
			if member == "" {
				continue
			}
			isTunnel, err := s.deps.SingboxTunnelsEditor.IsSingboxTunnelTag(ctx, member)
			if err != nil {
				return fmt.Errorf("inspect tunnel tag %q: %w", member, err)
			}
			if !isTunnel {
				continue
			}
			stillPresent := false
			for _, newMem := range newMembers {
				if strings.TrimSpace(newMem) == member {
					stillPresent = true
					break
				}
			}
			if stillPresent {
				continue
			}
			raw, err := s.deps.SingboxTunnelsEditor.GetTunnelOutbound(ctx, member)
			if err != nil {
				return fmt.Errorf("member %q: %w", member, err)
			}
			currBind := getOutboundBindInterface(raw)
			// Only clear if the current bind was placed by this group (currBind == oldBind)
			if oldBind != "" && currBind == oldBind {
				otherBind := s.otherGroupBindForMember(tag, member)
				patched, err := patchOutboundBindInterface(raw, otherBind)
				if err != nil {
					return fmt.Errorf("member %q: %w", member, err)
				}
				updates[member] = patched
			}
		}

		// 2. Members in the current (new) list
		if bind != "" && bindValid {
			for _, member := range newMembers {
				member = strings.TrimSpace(member)
				if member == "" {
					continue
				}
				isTunnel, err := s.deps.SingboxTunnelsEditor.IsSingboxTunnelTag(ctx, member)
				if err != nil {
					return fmt.Errorf("inspect tunnel tag %q: %w", member, err)
				}
				if !isTunnel {
					continue
				}
				raw, err := s.deps.SingboxTunnelsEditor.GetTunnelOutbound(ctx, member)
				if err != nil {
					return fmt.Errorf("member %q: %w", member, err)
				}
				currBind := getOutboundBindInterface(raw)
				// Foreign bind (set by another group OR set manually
				// on the tunnel page): never touch it. We only own
				// bind_interface values that came from this group
				// (currBind == oldBind); anything else is someone
				// else's responsibility.
				if currBind != "" && currBind != bind && currBind != oldBind {
					continue
				}
				if currBind != bind {
					patched, err := patchOutboundBindInterface(raw, bind)
					if err != nil {
						return fmt.Errorf("member %q: %w", member, err)
					}
					updates[member] = patched
				}
			}
		} else if oldBind != "" {
			// Clearing bind on the composite group: only revert members whose bind matches oldBind
			for _, member := range newMembers {
				member = strings.TrimSpace(member)
				if member == "" {
					continue
				}
				isTunnel, err := s.deps.SingboxTunnelsEditor.IsSingboxTunnelTag(ctx, member)
				if err != nil {
					return fmt.Errorf("inspect tunnel tag %q: %w", member, err)
				}
				if !isTunnel {
					continue
				}
				raw, err := s.deps.SingboxTunnelsEditor.GetTunnelOutbound(ctx, member)
				if err != nil {
					return fmt.Errorf("member %q: %w", member, err)
				}
				currBind := getOutboundBindInterface(raw)
				if currBind == oldBind {
					otherBind := s.otherGroupBindForMember(tag, member)
					patched, err := patchOutboundBindInterface(raw, otherBind)
					if err != nil {
						return fmt.Errorf("member %q: %w", member, err)
					}
					updates[member] = patched
				}
			}
		} else if bind != "" && !bindValid {
			// Self-heal: requested bind targets a missing kernel
			// interface — strip it from any current member that
			// already carries it. This is the boot-recovery case:
			// tunnels persisted with bind_interface=<missing-iface>
			// before the modem went away, and we need a clean
			// tunnels file before sing-box can start.
			for _, member := range newMembers {
				member = strings.TrimSpace(member)
				if member == "" {
					continue
				}
				isTunnel, err := s.deps.SingboxTunnelsEditor.IsSingboxTunnelTag(ctx, member)
				if err != nil {
					return fmt.Errorf("inspect tunnel tag %q: %w", member, err)
				}
				if !isTunnel {
					continue
				}
				raw, err := s.deps.SingboxTunnelsEditor.GetTunnelOutbound(ctx, member)
				if err != nil {
					return fmt.Errorf("member %q: %w", member, err)
				}
				currBind := getOutboundBindInterface(raw)
				if currBind == bind {
					patched, err := patchOutboundBindInterface(raw, "")
					if err != nil {
						return fmt.Errorf("member %q: %w", member, err)
					}
					updates[member] = patched
				}
			}
		}

		if len(updates) > 0 {
			// Ride the orchestrator's staging pipeline so the bind
			// changes land in the same draft as the composite group
			// edit; without this, a "Cancel draft" on the router
			// page would discard the group but keep the patched
			// bind_interface values on tunnels forever (#709,
			// PR #732 review blocker #2).
			if err := s.deps.SingboxTunnelsEditor.StageTunnelOutboundUpdates(ctx, updates); err != nil {
				return fmt.Errorf("batch update members: %w", err)
			}
		}
	}

	// Self-heal: do not persist a bind the kernel cannot satisfy —
	// next reload would FATAL-loop. Leave the group's stored bind
	// at the empty value so the user can re-apply when the interface
	// comes back.
	if bind != "" && !bindValid {
		return s.setCompositeEgressBind(tag, "")
	}

	// Update settings store ONLY after tunnels are patched successfully
	return s.setCompositeEgressBind(tag, bind)
}

func getOutboundBindInterface(raw json.RawMessage) string {
	var ob struct {
		BindInterface string `json:"bind_interface"`
	}
	_ = json.Unmarshal(raw, &ob)
	return strings.TrimSpace(ob.BindInterface)
}

func patchOutboundBindInterface(raw json.RawMessage, bind string) (json.RawMessage, error) {
	var ob map[string]any
	if err := json.Unmarshal(raw, &ob); err != nil {
		return nil, err
	}
	if bind != "" {
		ob["bind_interface"] = bind
	} else {
		delete(ob, "bind_interface")
	}
	return json.Marshal(ob)
}

func validateBindInterfaceOptional(ctx context.Context, s *ServiceImpl, name string) error {
	if name == "" {
		return nil
	}
	return s.validateBindInterface(ctx, name)
}

// ValidateBindInterface checks name against the bindable-interface catalog.
func (s *ServiceImpl) ValidateBindInterface(ctx context.Context, name string) error {
	return s.validateBindInterface(ctx, name)
}

// kernelInterfaceExists reports whether the kernel currently exposes a
// network interface named name. Mirrors the same helper in the
// subscription package and singbox.Operator so a stale bind_interface
// in 10-tunnels.json is detectable from any layer (#709, PR #732
// review blocker #5). Empty name returns false — we cannot assert
// running state without a concrete interface to check.
//
// Implemented as a package-level var so tests can substitute a
// stub; production code never reassigns it.
var kernelInterfaceExists = func(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	_, err := os.Stat("/sys/class/net/" + name)
	return err == nil
}

func (s *ServiceImpl) validateEgressBindConflicts(groupTag string, members []string, newBind string) error {
	newBind = strings.TrimSpace(newBind)
	if newBind == "" {
		return nil
	}

	binds := s.compositeEgressBinds()
	if binds == nil {
		return nil
	}

	cfg, err := s.loadRouterConfig()
	if err != nil {
		return fmt.Errorf("load router config: %w", err)
	}
	if cfg == nil {
		return nil
	}

	for _, member := range members {
		for _, o := range cfg.CompositeOutbounds() {
			if o.Tag == groupTag {
				continue
			}

			otherBind := strings.TrimSpace(binds[o.Tag])
			if otherBind == "" || otherBind == newBind {
				continue
			}

			for _, otherMember := range o.Outbounds {
				if otherMember == member {
					return fmt.Errorf("member %q is already in group %q with a different bind interface (%q)", member, o.Tag, otherBind)
				}
			}
		}
	}
	return nil
}
