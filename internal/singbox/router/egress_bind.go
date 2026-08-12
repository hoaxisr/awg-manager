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
	UpdateTunnelOutbound(ctx context.Context, tag string, outbound json.RawMessage) error
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

func (s *ServiceImpl) applyCompositeEgressBind(ctx context.Context, o Outbound, bind string) error {
	bind = strings.TrimSpace(bind)
	if err := validateBindInterfaceOptional(ctx, s, bind); err != nil {
		return err
	}
	if err := s.setCompositeEgressBind(o.Tag, bind); err != nil {
		return err
	}
	if s.deps.SingboxTunnelsEditor == nil || len(o.Outbounds) == 0 {
		return nil
	}
	for _, member := range o.Outbounds {
		member = strings.TrimSpace(member)
		if member == "" || !s.deps.SingboxTunnelsEditor.IsSingboxTunnelTag(ctx, member) {
			continue
		}
		raw, err := s.deps.SingboxTunnelsEditor.GetTunnelOutbound(ctx, member)
		if err != nil {
			return fmt.Errorf("member %q: %w", member, err)
		}
		patched, err := patchOutboundBindInterface(raw, bind)
		if err != nil {
			return fmt.Errorf("member %q: %w", member, err)
		}
		if err := s.deps.SingboxTunnelsEditor.UpdateTunnelOutbound(ctx, member, patched); err != nil {
			return fmt.Errorf("member %q: %w", member, err)
		}
	}
	return nil
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
