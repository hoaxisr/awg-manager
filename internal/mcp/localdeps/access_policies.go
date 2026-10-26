package localdeps

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/events"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

// accessPolicy maps one policy, for the list and the edit tools alike.
// Interfaces keeps the permitted ones only: a Denied entry is one the
// router holds switched off — in the list, not used (query/policies.go,
// !Enabled) — and an agent that saw one there could route traffic over a
// path the policy blocks. Removing a permit (DenyInterface) is something
// else: the entry is gone from the list. Order is the whole list, as the
// edit tools see it.
func accessPolicy(p accesspolicy.Policy) mcpsrv.AccessPolicy {
	out := mcpsrv.AccessPolicy{Name: p.Name, Description: sanitizeLabel(p.Description), DeviceCount: p.DeviceCount, IsStandard: p.IsStandard, Standalone: p.Standalone, Interfaces: []string{}}
	for _, iface := range p.Interfaces {
		if iface.Name == "" {
			continue
		}
		if !iface.Denied {
			out.Interfaces = append(out.Interfaces, iface.Name)
		}
		out.Order = append(out.Order, mcpsrv.AccessPolicyInterface{Name: iface.Name, Label: sanitizeLabel(iface.Label), Order: iface.Order, Denied: iface.Denied})
	}
	return out
}

// findPolicy reads one policy back after an edit; the service returns
// none.
func (l *Local) findPolicy(ctx context.Context, name string) (mcpsrv.AccessPolicy, error) {
	list, err := l.c.Policies.List(ctx)
	if err != nil {
		return mcpsrv.AccessPolicy{}, err
	}
	i := slices.IndexFunc(list, func(p accesspolicy.Policy) bool { return p.Name == name })
	if i < 0 {
		return mcpsrv.AccessPolicy{}, fmt.Errorf("access policy %q not found (use list_access_policies)", name)
	}
	return accessPolicy(list[i]), nil
}

func (l *Local) ListPolicyInterfaces(ctx context.Context) ([]mcpsrv.PolicyInterface, error) {
	if l.c.Policies == nil {
		return nil, errUnavailable("access policies")
	}
	list, err := l.c.Policies.ListGlobalInterfaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.PolicyInterface, 0, len(list))
	for _, g := range list {
		out = append(out, mcpsrv.PolicyInterface{Name: g.Name, Label: sanitizeLabel(g.Label), Up: g.Up})
	}
	return out, nil
}

func (l *Local) CreateAccessPolicy(ctx context.Context, description string) (mcpsrv.AccessPolicy, error) {
	if l.c.Policies == nil {
		return mcpsrv.AccessPolicy{}, errUnavailable("access policies")
	}
	p, err := l.c.Policies.Create(ctx, description)
	if err != nil {
		l.policyLog.Warn("create", description, "Failed to create access policy (MCP): "+err.Error())
		return mcpsrv.AccessPolicy{}, err
	}
	if p == nil {
		return mcpsrv.AccessPolicy{}, fmt.Errorf("policy create returned no record")
	}
	l.policyLog.Info("create", p.Name, "Access policy created (MCP)")
	l.publish(events.ResourceRoutingAccessPolicies, "mcp-create")
	return accessPolicy(*p), nil
}

// DeleteAccessPolicy refuses the policy the sing-box router routes by: its
// devices would be cut off from sing-box, and the router would report the
// policy missing. The service itself refuses HydraRoute's policies.
func (l *Local) DeleteAccessPolicy(ctx context.Context, name string) (mcpsrv.AccessPolicy, error) {
	if l.c.Policies == nil {
		return mcpsrv.AccessPolicy{}, errUnavailable("access policies")
	}
	current, err := l.findPolicy(ctx, name)
	if err != nil {
		return mcpsrv.AccessPolicy{}, err
	}
	if l.c.Router != nil {
		// Fails closed: a guard that could not look is no guard.
		settings, err := l.c.Router.GetSettings(ctx)
		if err != nil {
			return mcpsrv.AccessPolicy{}, fmt.Errorf("could not check whether the sing-box router routes by access policy %q, so it was not deleted: %w", name, err)
		}
		if settings.PolicyName == name {
			return mcpsrv.AccessPolicy{}, fmt.Errorf("access policy %q is the one the sing-box router routes by; choose another policy for it in the web interface first", name)
		}
	}
	if err := l.c.Policies.Delete(ctx, name); err != nil {
		l.policyLog.Warn("delete", name, "Failed to delete access policy (MCP): "+err.Error())
		return mcpsrv.AccessPolicy{}, err
	}
	l.policyLog.Info("delete", name, "Access policy deleted (MCP)")
	l.publish(events.ResourceRoutingAccessPolicies, "mcp-delete")
	l.publish(events.ResourceRoutingPolicyDevices, "mcp-delete")
	return current, nil
}

// readBack reads a policy after a write that went through. A failed read
// does not undo the write, so it is no error: the agent gets the policy as
// it was, marked unconfirmed, instead of "failed" for a change in effect.
func (l *Local) readBack(ctx context.Context, name string, before mcpsrv.AccessPolicy) mcpsrv.AccessPolicy {
	after, err := l.findPolicy(ctx, name)
	if err == nil {
		return after
	}
	l.policyLog.Warn("read-back", name, "Policy changed, but could not be read back (MCP): "+err.Error())
	before.Unconfirmed = "the change is applied, but the policy could not be read back (" + sanitizeText(err.Error(), maxMessageRunes) + "); the fields here are from before it"
	return before
}

func (l *Local) UpdateAccessPolicy(ctx context.Context, name string, description *string, standalone *bool) (mcpsrv.AccessPolicy, error) {
	if l.c.Policies == nil {
		return mcpsrv.AccessPolicy{}, errUnavailable("access policies")
	}
	before, err := l.findPolicy(ctx, name)
	if err != nil {
		return mcpsrv.AccessPolicy{}, err
	}
	if description != nil {
		if err := l.c.Policies.SetDescription(ctx, name, *description); err != nil {
			return mcpsrv.AccessPolicy{}, err
		}
	}
	if standalone != nil {
		if err := l.c.Policies.SetStandalone(ctx, name, *standalone); err != nil {
			if description != nil {
				// The description is written already: open tabs must see it,
				// and the agent must not think nothing changed.
				l.publish(events.ResourceRoutingAccessPolicies, "mcp-update")
				return mcpsrv.AccessPolicy{}, fmt.Errorf("the description was changed, but the standalone mode was not: %w", err)
			}
			return mcpsrv.AccessPolicy{}, err
		}
	}
	l.policyLog.Info("update", name, "Access policy updated (MCP)")
	l.publish(events.ResourceRoutingAccessPolicies, "mcp-update")
	return l.readBack(ctx, name, before), nil
}

// SetAccessPolicyInterface permits iface at a position, or removes it.
//
// A position is relative to the list the agent read: expectOrder, the
// policy's interface names in order as list_access_policies showed them, is
// compared with the list read here, just before the write, and a list that
// changed meanwhile (a web edit, HydraRoute, an earlier call) is refused —
// the position would land somewhere else. NDMS offers no compare-and-set,
// so the read and the write are still two RCI calls; the check closes the
// gap of the agent's turn, not that one.
//
// order -1 puts a permitted interface last — after every other entry, so
// it takes traffic only when they are down — counted in the list read
// here.
//
// Only a permit needs an interface the router offers. Removing one goes by
// the policy's own list: an interface whose tunnel is gone is no longer
// offered, and it must still be removable (DenyInterface handles a name
// NDMS no longer has).
func (l *Local) SetAccessPolicyInterface(ctx context.Context, name, iface string, permit bool, order int, expectOrder []string) (mcpsrv.AccessPolicy, error) {
	if l.c.Policies == nil {
		return mcpsrv.AccessPolicy{}, errUnavailable("access policies")
	}
	current, err := l.findPolicy(ctx, name)
	if err != nil {
		return mcpsrv.AccessPolicy{}, err
	}
	names := make([]string, 0, len(current.Order))
	for _, o := range current.Order {
		names = append(names, o.Name)
	}
	if expectOrder != nil && !slices.Equal(names, expectOrder) {
		return mcpsrv.AccessPolicy{}, fmt.Errorf("the interfaces of %q are now [%s], not [%s] as expected — the list changed since it was read; nothing was changed: read list_access_policies again",
			name, strings.Join(names, ", "), strings.Join(expectOrder, ", "))
	}
	if permit {
		known, err := l.c.Policies.ListGlobalInterfaces(ctx)
		if err != nil {
			return mcpsrv.AccessPolicy{}, err
		}
		if !slices.ContainsFunc(known, func(g accesspolicy.GlobalInterface) bool { return g.Name == iface }) {
			return mcpsrv.AccessPolicy{}, fmt.Errorf("%q is not an interface a policy can use (use list_policy_interfaces)", iface)
		}
		if order < 0 {
			order = len(slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == iface }))
		}
		err = l.c.Policies.PermitInterface(ctx, name, iface, order)
	} else {
		if !slices.Contains(names, iface) {
			return mcpsrv.AccessPolicy{}, fmt.Errorf("%q is not in access policy %q, so there is nothing to remove (its interfaces: %s)", iface, name, strings.Join(names, ", "))
		}
		err = l.c.Policies.DenyInterface(ctx, name, iface)
	}
	if err != nil {
		l.policyLog.Warn("interface", name, "Failed to change policy interface (MCP): "+err.Error())
		return mcpsrv.AccessPolicy{}, err
	}
	verb := "removed"
	if permit {
		verb = fmt.Sprintf("permitted at %d", order)
	}
	l.policyLog.Info("interface", name, iface+" "+verb+" (MCP)")
	l.publish(events.ResourceRoutingAccessPolicies, "mcp-interface")
	return l.readBack(ctx, name, current), nil
}

func (l *Local) AssignDevicePolicy(ctx context.Context, mac, policy string) (mcpsrv.Device, error) {
	if l.c.Policies == nil {
		return mcpsrv.Device{}, errUnavailable("access policies")
	}
	devices, err := l.c.Policies.ListDevices(ctx)
	if err != nil {
		return mcpsrv.Device{}, err
	}
	i := slices.IndexFunc(devices, func(d accesspolicy.Device) bool { return strings.EqualFold(d.MAC, mac) })
	if i < 0 {
		return mcpsrv.Device{}, fmt.Errorf("device %s not found (use list_devices)", mac)
	}
	if policy == "" {
		err = l.c.Policies.UnassignDevice(ctx, devices[i].MAC)
	} else {
		if _, perr := l.findPolicy(ctx, policy); perr != nil {
			return mcpsrv.Device{}, perr
		}
		err = l.c.Policies.AssignDevice(ctx, devices[i].MAC, policy)
	}
	if err != nil {
		l.policyLog.Warn("assign", mac, "Failed to assign device (MCP): "+err.Error())
		return mcpsrv.Device{}, err
	}
	l.policyLog.Info("assign", mac, "Device moved to "+cmpOr(policy, "the default")+" (MCP)")
	// As REST: the policy list carries device counts.
	l.publish(events.ResourceRoutingAccessPolicies, "mcp-assign")
	l.publish(events.ResourceRoutingPolicyDevices, "mcp-assign")
	d := devices[i]
	return mcpsrv.Device{MAC: d.MAC, IP: d.IP, Name: d.Name, Hostname: d.Hostname, Active: d.Active, Policy: policy}, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
