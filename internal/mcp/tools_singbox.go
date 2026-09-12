package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type singboxTunnelsOut struct {
	Tunnels []SingboxTunnel `json:"tunnels"`
}

type singboxTagIn struct {
	Tag string `json:"tag" jsonschema:"proxy tag from list_singbox_tunnels"`
}

type singboxIn struct {
	Action string `json:"action" jsonschema:"start|stop|restart"`
}

func registerSingboxTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_singbox_tunnels",
		Description: "Proxies configured inside sing-box (vless, hysteria2, naive) with their endpoint, local listen port and whether each one is actually up. " +
			"These are separate from the WireGuard/AmneziaWG tunnels in list_tunnels. Passwords and uuids are not returned.",
		Annotations: readOnly("List sing-box proxies"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, singboxTunnelsOut, error) {
		list, err := d.ListSingboxTunnels(ctx)
		if list == nil {
			list = []SingboxTunnel{}
		}
		return nil, singboxTunnelsOut{Tunnels: list}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "singbox_delay_check",
		Description: "Measure one sing-box proxy's latency through the running engine. reachable=false means it did not answer in time — " +
			"a single silent check can be transient, so repeat it before telling the user the proxy is down. busy=true means nothing was measured because a probe was already running: retry in a few seconds. sing-box must be running.",
		Annotations: readOnly("Sing-box delay check"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in singboxTagIn) (*mcp.CallToolResult, SingboxDelay, error) {
		if strings.TrimSpace(in.Tag) == "" {
			return nil, SingboxDelay{}, fmt.Errorf("tag is required (use list_singbox_tunnels)")
		}
		out, err := d.CheckSingboxDelay(ctx, strings.TrimSpace(in.Tag))
		if err != nil {
			return nil, SingboxDelay{}, err
		}
		if out.Busy {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
				Text: "A probe for this proxy was already in progress, so nothing was measured; this says nothing about whether the proxy works. Retry in a few seconds.",
			}}}, out, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "control_singbox",
		Description: "Start, stop or restart the sing-box proxy engine and return its status. Install/uninstall are not available via MCP.",
		Annotations: safeWrite("Control sing-box", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in singboxIn) (*mcp.CallToolResult, SingboxStatus, error) {
		switch in.Action {
		case "start", "stop", "restart":
		default:
			return nil, SingboxStatus{}, fmt.Errorf("action must be start, stop or restart")
		}
		out, err := d.ControlSingbox(ctx, in.Action)
		return nil, out, err
	})
}
