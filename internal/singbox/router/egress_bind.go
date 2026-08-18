package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SingboxTunnelEditor patches sing-box tunnel outbounds (10-tunnels.json).
// Optional dep — when nil, composite egress bind is stored but not propagated.
type SingboxTunnelEditor interface {
	GetTunnelOutbound(ctx context.Context, tag string) (json.RawMessage, error)
	UpdateTunnelOutbounds(ctx context.Context, updates map[string]json.RawMessage) error
	IsSingboxTunnelTag(ctx context.Context, tag string) bool
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

	if s.deps.SingboxTunnelsEditor != nil {
		updates := make(map[string]json.RawMessage)

		// 1. Members being removed from the composite group
		for _, member := range oldMembers {
			member = strings.TrimSpace(member)
			if member == "" || !s.deps.SingboxTunnelsEditor.IsSingboxTunnelTag(ctx, member) {
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
		if bind != "" {
			for _, member := range newMembers {
				member = strings.TrimSpace(member)
				if member == "" || !s.deps.SingboxTunnelsEditor.IsSingboxTunnelTag(ctx, member) {
					continue
				}
				raw, err := s.deps.SingboxTunnelsEditor.GetTunnelOutbound(ctx, member)
				if err != nil {
					return fmt.Errorf("member %q: %w", member, err)
				}
				currBind := getOutboundBindInterface(raw)
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
				if member == "" || !s.deps.SingboxTunnelsEditor.IsSingboxTunnelTag(ctx, member) {
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
		}

		if len(updates) > 0 {
			if err := s.deps.SingboxTunnelsEditor.UpdateTunnelOutbounds(ctx, updates); err != nil {
				return fmt.Errorf("batch update members: %w", err)
			}
		}
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
