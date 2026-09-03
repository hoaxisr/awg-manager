package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type logsOut struct {
	Total   int        `json:"total" jsonschema:"entries matching the filter before the line cap"`
	Entries []LogEntry `json:"entries"`
}

type pingCheckOut struct {
	Tunnels []PingCheckStatus `json:"tunnels"`
}

func registerSystemTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_system_status",
		Description: "Router and daemon overview: version, boot phase, WAN interfaces, sing-box state, auth mode, and the raw system info (model, firmware, memory, kernel module).",
		Annotations: readOnly("System status"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, SystemStatus, error) {
		out, err := d.SystemStatus(ctx)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_logs",
		Description: "Read recent awg-manager (bucket=app) or sing-box (bucket=singbox) log entries, newest last. Filter by group, minimum level and message substring. At most 500 lines.",
		Annotations: readOnly("Logs"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, q LogsQuery) (*mcp.CallToolResult, logsOut, error) {
		if q.Lines <= 0 {
			q.Lines = 100
		}
		if q.Lines > 500 {
			q.Lines = 500
		}
		if q.Bucket == "" {
			q.Bucket = "app"
		}
		if q.Bucket != "app" && q.Bucket != "singbox" {
			return nil, logsOut{}, fmt.Errorf("bucket must be app or singbox")
		}
		entries, total, err := d.GetLogs(ctx, q)
		if entries == nil {
			entries = []LogEntry{}
		}
		return nil, logsOut{Total: total, Entries: entries}, err
	})

	type connIn struct {
		TunnelID string `json:"tunnelId" jsonschema:"tunnel id from list_tunnels"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "test_connectivity",
		Description: "Probe internet reachability through a tunnel (HTTP 204 check). Takes a few seconds.",
		Annotations: readOnly("Connectivity test"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in connIn) (*mcp.CallToolResult, ConnectivityResult, error) {
		if in.TunnelID == "" {
			return nil, ConnectivityResult{}, fmt.Errorf("tunnelId is required")
		}
		out, err := d.TestConnectivity(ctx, in.TunnelID)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_monitoring_matrix",
		Description: "Latest latency matrix: every monitored target × every tunnel, with ok/latency per cell.",
		Annotations: readOnly("Monitoring matrix"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, MonitoringMatrix, error) {
		out, err := d.MonitoringMatrix(ctx)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "run_pingcheck",
		Description: "Trigger an immediate ping-check of all monitored tunnels and return their current ping-check status.",
		Annotations: safeWrite("Run ping check now", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, pingCheckOut, error) {
		st, err := d.RunPingCheck(ctx)
		if st == nil {
			st = []PingCheckStatus{}
		}
		return nil, pingCheckOut{Tunnels: st}, err
	})
}
