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

func requireDescription(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > MaxSingboxLabelRunes || strings.ContainsAny(s, "\r\n") {
		return "", fmt.Errorf("description must be one line, 1 to %d characters", MaxSingboxLabelRunes)
	}
	return s, nil
}

type policyDescriptionIn struct {
	Description string `json:"description" jsonschema:"the policy's name as the web interface and the router show it"`
}

type policyNameIn struct {
	Policy string `json:"policy" jsonschema:"policy name from list_access_policies (Policy0, Policy1, …)"`
}

type updatePolicyIn struct {
	Policy      string  `json:"policy" jsonschema:"policy name from list_access_policies"`
	Description *string `json:"description,omitempty" jsonschema:"new description; omit to keep it"`
	Standalone  *bool   `json:"standalone,omitempty" jsonschema:"true: devices reach only the interfaces the policy permits, never the router's default connection; omit to keep it"`
}

type policyInterfaceIn struct {
	Policy    string `json:"policy" jsonschema:"policy name from list_access_policies"`
	Interface string `json:"interface" jsonschema:"interface name from list_policy_interfaces"`
	Permit    bool   `json:"permit" jsonschema:"true permits it at position order; false denies it (kept in the list, not used)"`
	Order     int    `json:"order,omitempty" jsonschema:"position for a permitted interface, from 0; traffic uses the first permitted interface that is up"`
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
		Description: "Permit an interface in an access policy at a given position — the order decides which permitted interface traffic uses first — or deny it. " +
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
		if in.Order < 0 || in.Order > 64 {
			return nil, AccessPolicy{}, fmt.Errorf("order must be between 0 and 64")
		}
		out, err := d.SetAccessPolicyInterface(ctx, name, iface, in.Permit, in.Order)
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
