package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type singboxIn struct {
	Action string `json:"action" jsonschema:"start|stop|restart"`
}

func registerSingboxTools(s *mcp.Server, d Deps) {
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
