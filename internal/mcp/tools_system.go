package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// LogLevelRank orders LogsQuery.Level for the STRICT minimum-level filter
// every Deps implementation must apply: an entry is kept only when its own
// level ranks at or above the requested one, and an entry whose level is
// not in this table is dropped. It deliberately does not reuse
// logging.IsVisible, which treats warn/error as always visible.
var LogLevelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

type logsOut struct {
	Total   int        `json:"total" jsonschema:"entries matching the filter before the line cap (within the newest 5000 buffer entries)"`
	Entries []LogEntry `json:"entries"`
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
		Name: "get_logs",
		Description: "Read recent awg-manager (bucket=app) or sing-box (bucket=singbox) log entries, newest last. Filter by group, minimum level and message substring. At most 500 lines. " +
			"IPs and domains in messages are partially masked unless raw=true, which needs a full-access key; repeats>1 marks a line the buffer collapsed.",
		Annotations: readOnly("Logs"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, q LogsQuery) (*mcp.CallToolResult, logsOut, error) {
		// Unmasked lines carry the router's peers, servers and subscription
		// hosts. A read-only key is for an agent the user does not fully
		// trust, so it reads the masked journal only — the same line the
		// scope draws around credentialTools. No key at all is the dev
		// server (see RequireWriteScope) and is left alone.
		if key, ok := KeyFromContext(ctx); ok && key.ReadOnly && q.Raw {
			return nil, logsOut{}, fmt.Errorf("raw=true returns unmasked addresses, and this MCP key is read-only. Nothing was read. Call get_logs without raw, or ask the user for a full-access key")
		}
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
		// Normalised here so every Deps implementation gets the same
		// lower-case, validated value: level is a STRICT minimum, and an
		// unrecognised one must be an error rather than a silently
		// different filter.
		if q.Level != "" {
			q.Level = strings.ToLower(strings.TrimSpace(q.Level))
			if _, ok := LogLevelRank[q.Level]; !ok {
				return nil, logsOut{}, fmt.Errorf("level must be one of debug|info|warn|error")
			}
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
		if err := requireTunnelID(in.TunnelID); err != nil {
			return nil, ConnectivityResult{}, err
		}
		out, err := d.TestConnectivity(ctx, in.TunnelID)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "check_ip",
		Description: "Compare the external IP seen through a tunnel with the one seen over the bare WAN. This is the check that answers whether traffic really goes through the tunnel: " +
			"ipChanged=false means it does NOT, even when test_connectivity succeeds. The tunnel must be running. Takes several seconds and makes outbound requests.",
		Annotations: readOnly("Check external IP"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in connIn) (*mcp.CallToolResult, IPCheckResult, error) {
		if err := requireTunnelID(in.TunnelID); err != nil {
			return nil, IPCheckResult{}, err
		}
		out, err := d.CheckIP(ctx, in.TunnelID)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_monitoring_matrix",
		Description: "Latest latency matrix: monitored targets × tunnels, with ok/latency per cell. " +
			"A row with probed=false has no cell — the matrix does not measure it, which says nothing about its health. " +
			"Every sing-box row (source=singbox) is like that: measure those with singbox_delay_check, passing singboxTag.",
		Annotations: readOnly("Monitoring matrix"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, MonitoringMatrix, error) {
		out, err := d.MonitoringMatrix(ctx)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "run_pingcheck",
		Description: "Trigger a ping-check of all monitored tunnels in the background and return the status as of the LAST completed check. " +
			"The check takes up to ~10 s: call get_monitoring_matrix or run_pingcheck again afterwards for fresh results.",
		Annotations: safeWrite("Run ping check now", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, PingCheckRun, error) {
		run, err := d.RunPingCheck(ctx)
		if run.Tunnels == nil {
			run.Tunnels = []PingCheckStatus{}
		}
		return nil, run, err
	})
}
