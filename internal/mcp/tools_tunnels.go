package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type tunnelIDIn struct {
	TunnelID string `json:"tunnelId" jsonschema:"tunnel id from list_tunnels"`
}

type tunnelsOut struct {
	Tunnels []TunnelSummary `json:"tunnels"`
}

type controlIn struct {
	TunnelID string `json:"tunnelId" jsonschema:"tunnel id from list_tunnels"`
	Action   string `json:"action" jsonschema:"start|stop|restart|enable|disable|set_default_route|unset_default_route"`
}

// controlOut reports the action's outcome. The action itself either
// succeeded or the tool returned an error; State/Enabled describe the
// tunnel AFTERWARDS and come from a separate read that can fail on its
// own. StateKnown says whether that read succeeded — when it is false,
// State is "unknown" and Enabled carries no information.
type controlOut struct {
	TunnelID   string `json:"tunnelId"`
	Action     string `json:"action"`
	State      string `json:"state" jsonschema:"tunnel state after the action; \"unknown\" when stateKnown is false"`
	Enabled    bool   `json:"enabled" jsonschema:"autostart flag after the action; meaningless when stateKnown is false"`
	StateKnown bool   `json:"stateKnown" jsonschema:"false means the action succeeded but reading the tunnel back failed — do not report state or enabled to the user"`
}

// stateUnknown is the State value used when the post-action read failed.
const stateUnknown = "unknown"

type createIn struct {
	Name   string `json:"name" jsonschema:"display name for the new tunnel"`
	Config string `json:"config" jsonschema:"full WireGuard/AmneziaWG .conf text with [Interface] and [Peer] sections"`
}

type replaceIn struct {
	TunnelID string `json:"tunnelId"`
	Config   string `json:"config" jsonschema:"new .conf text; replaces keys, addresses and peer"`
	Name     string `json:"name,omitempty" jsonschema:"optional new display name"`
}

type replaceOut struct {
	TunnelID string   `json:"tunnelId"`
	Replaced bool     `json:"replaced"`
	Warnings []string `json:"warnings,omitempty" jsonschema:"non-fatal problems: the tunnel failed to start again after the replace, or its new address conflicts with another interface — show these to the user"`
}

type exportOut struct {
	TunnelID string `json:"tunnelId"`
	Config   string `json:"config" jsonschema:".conf text including the private key"`
}

var validActions = map[string]bool{
	ActionStart: true, ActionStop: true, ActionRestart: true, ActionEnable: true, ActionDisable: true,
	ActionSetDefaultRoute: true, ActionUnsetDefaultRoute: true,
}

func requireConf(config string) error {
	if !strings.Contains(config, "[Interface]") || !strings.Contains(config, "[Peer]") {
		return fmt.Errorf("config must contain [Interface] and [Peer] sections")
	}
	return nil
}

func registerTunnelTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tunnels",
		Description: "List AWG/WireGuard tunnels with runtime state (running/stopped), enabled flag, backend, endpoint and whether the default route goes through them.",
		Annotations: readOnly("List tunnels"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, tunnelsOut, error) {
		list, err := d.ListTunnels(ctx)
		if list == nil {
			list = []TunnelSummary{}
		}
		return nil, tunnelsOut{Tunnels: list}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_tunnel",
		Description: "Details of one tunnel including addresses, allowed IPs, process state and traffic over the last hour.",
		Annotations: readOnly("Get tunnel"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tunnelIDIn) (*mcp.CallToolResult, TunnelDetail, error) {
		if in.TunnelID == "" {
			return nil, TunnelDetail{}, fmt.Errorf("tunnelId is required")
		}
		out, err := d.GetTunnel(ctx, in.TunnelID)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "control_tunnel",
		Description: "Start, stop or restart a tunnel; enable/disable it (autostart); or make it the default route (set_default_route/unset_default_route). Reversible.",
		Annotations: safeWrite("Control tunnel", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in controlIn) (*mcp.CallToolResult, controlOut, error) {
		if in.TunnelID == "" {
			return nil, controlOut{}, fmt.Errorf("tunnelId is required")
		}
		if !validActions[in.Action] {
			return nil, controlOut{}, fmt.Errorf("action %q is not one of start|stop|restart|enable|disable|set_default_route|unset_default_route", in.Action)
		}
		if err := d.ControlTunnel(ctx, in.TunnelID, in.Action); err != nil {
			return nil, controlOut{}, err
		}
		det, err := d.GetTunnel(ctx, in.TunnelID)
		if err != nil {
			// The action succeeded — reporting IsError would be a lie in the
			// other direction. But the zero value of State/Enabled reads as
			// "stopped and disabled", so say "unknown" out loud, in the
			// structured output AND in a text block the model cannot miss.
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
				Text: fmt.Sprintf("Action %q on tunnel %q succeeded, but reading the tunnel back failed: %v. "+
					"The resulting state is UNKNOWN — do not tell the user the tunnel is stopped or disabled; call get_tunnel again.", in.Action, in.TunnelID, err),
			}}}, controlOut{TunnelID: in.TunnelID, Action: in.Action, State: stateUnknown}, nil
		}
		return nil, controlOut{TunnelID: in.TunnelID, Action: in.Action, State: det.State, Enabled: det.Enabled, StateKnown: true}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_tunnel",
		Description: "Create a tunnel from a .conf text (WireGuard or AmneziaWG). The tunnel is created DISABLED and stopped: use control_tunnel with \"enable\" (autostart) and then \"start\" to bring it up.",
		Annotations: safeWrite("Create tunnel", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, TunnelSummary, error) {
		if strings.TrimSpace(in.Name) == "" {
			return nil, TunnelSummary{}, fmt.Errorf("name is required")
		}
		if err := requireConf(in.Config); err != nil {
			return nil, TunnelSummary{}, err
		}
		out, err := d.ImportTunnel(ctx, in.Name, in.Config)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "replace_tunnel_config",
		Description: "Replace a tunnel's configuration with new .conf text (e.g. new keys or endpoint) and optionally rename it. " +
			"A running tunnel is stopped and started again automatically, so no manual restart is needed; check the returned warnings.",
		Annotations: safeWrite("Replace tunnel config", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in replaceIn) (*mcp.CallToolResult, replaceOut, error) {
		if in.TunnelID == "" {
			return nil, replaceOut{}, fmt.Errorf("tunnelId is required")
		}
		if err := requireConf(in.Config); err != nil {
			return nil, replaceOut{}, err
		}
		warnings, err := d.ReplaceTunnelConfig(ctx, in.TunnelID, in.Config, in.Name)
		if err != nil {
			return nil, replaceOut{}, err
		}
		return nil, replaceOut{TunnelID: in.TunnelID, Replaced: true, Warnings: warnings}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "export_tunnel_config",
		Description: "Export a tunnel as .conf text. WARNING: includes the private key — only use when the user asks for the config.",
		Annotations: readOnly("Export tunnel config"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tunnelIDIn) (*mcp.CallToolResult, exportOut, error) {
		if in.TunnelID == "" {
			return nil, exportOut{}, fmt.Errorf("tunnelId is required")
		}
		cfg, err := d.ExportTunnelConfig(ctx, in.TunnelID)
		return nil, exportOut{TunnelID: in.TunnelID, Config: cfg}, err
	})
}
