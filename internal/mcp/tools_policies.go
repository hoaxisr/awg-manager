package mcp

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// policyNameShape is the shape of a policy name: PolicyN for the router's
// own, a word for one another subsystem made.
var policyNameShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)

// ifaceNameShape is the shape of an NDMS interface name.
var ifaceNameShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9/_.-]{0,63}$`)

func requirePolicyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !policyNameShape.MatchString(name) {
		return "", fmt.Errorf("policy must be a policy name from list_access_policies")
	}
	return name, nil
}

// policyDescriptionShape is what the router takes for a policy's
// description (accesspolicy.validDescription).
var policyDescriptionShape = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func requireDescription(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > MaxSingboxLabelRunes || !policyDescriptionShape.MatchString(s) {
		return "", fmt.Errorf("description must be 1 to %d Latin letters, digits, hyphens or underscores — the router takes nothing else (Kids_room, not \"Kids room\")", MaxSingboxLabelRunes)
	}
	return s, nil
}

type policyDescriptionIn struct {
	Description string `json:"description" jsonschema:"the policy's name as the web interface and the router show it: Latin letters, digits, hyphens and underscores only (Kids_room)"`
}

type policyNameIn struct {
	Policy string `json:"policy" jsonschema:"policy name from list_access_policies (Policy0, Policy1, …)"`
}

type updatePolicyIn struct {
	Policy      string  `json:"policy" jsonschema:"policy name from list_access_policies"`
	Description *string `json:"description,omitempty" jsonschema:"new description — Latin letters, digits, hyphens and underscores only; omit to keep it"`
	Standalone  *bool   `json:"standalone,omitempty" jsonschema:"true: the policy works on its own, not bound to the router's global rules: static routes from the router settings are not copied into it (routes from the \"IP addresses\" tab do not work for its devices). It does NOT block the router's default connection — that is decided by the interfaces the policy permits; omit to keep it"`
}

type policyInterfaceIn struct {
	Policy    string `json:"policy" jsonschema:"policy name from list_access_policies"`
	Interface string `json:"interface" jsonschema:"interface name from list_policy_interfaces"`
	Permit    bool   `json:"permit" jsonschema:"true permits it; false removes it from the policy, so devices of the policy no longer go out through it"`
	// Order is a pointer: an interface permitted without one goes to the
	// end, where it takes no traffic while the ones above are up. A zero
	// default would put it first and switch the whole policy to it.
	Order *int `json:"order,omitempty" jsonschema:"position for a permitted interface, from 0, in the policy's order as list_access_policies shows it — traffic uses the first permitted interface that is up, so 0 moves the whole policy onto it. Omit to put it last. Requires expectOrder"`
	// ExpectOrder ties Order to the list it was counted in.
	ExpectOrder []string `json:"expectOrder,omitempty" jsonschema:"the names in the policy's order exactly as list_access_policies showed them; required with order. A position counted in a list that has changed since lands elsewhere, so a list that differs is refused and nothing changes"`
}

type assignDeviceIn struct {
	MAC    string `json:"mac" jsonschema:"device MAC address from list_devices"`
	Policy string `json:"policy" jsonschema:"policy name from list_access_policies; empty returns the device to the router's default"`
}

type policyInterfacesOut struct {
	Interfaces []PolicyInterface `json:"interfaces"`
}

type deletedPolicyOut struct {
	Deleted AccessPolicy `json:"deleted" jsonschema:"the policy as it was; its devices go back to the router's default"`
}

func registerPolicyTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_policy_interfaces",
		Description: "Router interfaces an access policy can permit: WAN connections, tunnels and VPN interfaces, with whether each is up.",
		Annotations: readOnly("List policy interfaces"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, policyInterfacesOut, error) {
		list, err := d.ListPolicyInterfaces(ctx)
		return nil, policyInterfacesOut{Interfaces: orEmpty(list)}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_access_policy",
		Description: "Create a Keenetic access policy (the first free PolicyN) — a set of interfaces a group of devices is allowed to go out through. It starts with no interface; permit some with set_access_policy_interface and move devices in with assign_device_policy. " +
			"Applied at once in the router.",
		Annotations: safeWrite("Create access policy", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in policyDescriptionIn) (*mcp.CallToolResult, AccessPolicy, error) {
		desc, err := requireDescription(in.Description)
		if err != nil {
			return nil, AccessPolicy{}, err
		}
		out, err := d.CreateAccessPolicy(ctx, desc)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_access_policy",
		Description: "Delete a Keenetic access policy; its devices go back to the router's default connection. The policy the sing-box router routes by is refused, as are HydraRoute's own. " +
			"DESTRUCTIVE: applied at once, and the policy's interface list is gone. Confirm with the user first.",
		Annotations: destructiveWrite("Delete access policy", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in policyNameIn) (*mcp.CallToolResult, deletedPolicyOut, error) {
		name, err := requirePolicyName(in.Policy)
		if err != nil {
			return nil, deletedPolicyOut{}, err
		}
		deleted, err := d.DeleteAccessPolicy(ctx, name)
		if err != nil {
			return nil, deletedPolicyOut{}, err
		}
		return nil, deletedPolicyOut{Deleted: deleted}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_access_policy",
		Description: "Change an access policy's description or standalone mode. Applied at once in the router.",
		Annotations: safeWrite("Update access policy", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updatePolicyIn) (*mcp.CallToolResult, AccessPolicy, error) {
		name, err := requirePolicyName(in.Policy)
		if err != nil {
			return nil, AccessPolicy{}, err
		}
		if in.Description == nil && in.Standalone == nil {
			return nil, AccessPolicy{}, fmt.Errorf("nothing to change: pass description or standalone")
		}
		var desc *string
		if in.Description != nil {
			v, err := requireDescription(*in.Description)
			if err != nil {
				return nil, AccessPolicy{}, err
			}
			desc = &v
		}
		out, err := d.UpdateAccessPolicy(ctx, name, desc, in.Standalone)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_access_policy_interface",
		Description: "Permit an interface in an access policy, or remove it from the policy. Traffic uses the first permitted interface that is up: without order the interface goes last, as a fallback; order 0 makes it the one the whole policy uses. " +
			"A position is counted in the policy's order from list_access_policies, which changes with every permit and removal: order requires that order as expectOrder, and read the list again before the next call. " +
			"Applied at once in the router; devices of the policy switch path immediately.",
		Annotations: safeWrite("Set access policy interface", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in policyInterfaceIn) (*mcp.CallToolResult, AccessPolicy, error) {
		name, err := requirePolicyName(in.Policy)
		if err != nil {
			return nil, AccessPolicy{}, err
		}
		iface := strings.TrimSpace(in.Interface)
		if !ifaceNameShape.MatchString(iface) {
			return nil, AccessPolicy{}, fmt.Errorf("interface must be a name from list_policy_interfaces")
		}
		order := -1
		if in.Order != nil {
			if *in.Order < 0 || *in.Order > 64 {
				return nil, AccessPolicy{}, fmt.Errorf("order must be between 0 and 64")
			}
			if !in.Permit {
				return nil, AccessPolicy{}, fmt.Errorf("order is a position for a permitted interface; a removal takes none")
			}
			if in.ExpectOrder == nil {
				return nil, AccessPolicy{}, fmt.Errorf("order is counted in the policy's list, which changes with every edit: pass that list as expectOrder (the order names from list_access_policies), or omit order to put the interface last")
			}
			order = *in.Order
		}
		var expect []string
		if in.ExpectOrder != nil {
			expect = make([]string, 0, len(in.ExpectOrder))
			for _, n := range in.ExpectOrder {
				expect = append(expect, strings.TrimSpace(n))
			}
		}
		out, err := d.SetAccessPolicyInterface(ctx, name, iface, in.Permit, order, expect)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "assign_device_policy",
		Description: "Move a LAN device into an access policy, or back to the router's default with an empty policy. Applied at once; the device's traffic switches path immediately.",
		Annotations: safeWrite("Assign device policy", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in assignDeviceIn) (*mcp.CallToolResult, Device, error) {
		mac, err := net.ParseMAC(strings.TrimSpace(in.MAC))
		if err != nil || len(mac) != 6 {
			return nil, Device{}, fmt.Errorf("mac must be a device MAC address from list_devices")
		}
		policy := strings.TrimSpace(in.Policy)
		if policy != "" {
			if policy, err = requirePolicyName(policy); err != nil {
				return nil, Device{}, err
			}
		}
		out, err := d.AssignDevicePolicy(ctx, mac.String(), policy)
		return nil, out, err
	})
}
