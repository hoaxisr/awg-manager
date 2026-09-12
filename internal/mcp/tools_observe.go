package mcp

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxConnectionsInOutput caps one page of the connection table. A busy
// router holds thousands of flows; the point of the tool is to see what a
// device is doing, not to ship conntrack into a model's context.
const MaxConnectionsInOutput = 100

// MaxPingCheckLogEntries caps the ping-check journal page.
const MaxPingCheckLogEntries = 200

type connectionsIn struct {
	TunnelID string `json:"tunnelId,omitempty" jsonschema:"only flows going through this tunnel; omit for all"`
	ClientIP string `json:"clientIp,omitempty" jsonschema:"only flows from this LAN device"`
	Limit    int    `json:"limit,omitempty" jsonschema:"1..100, default 50"`
}

type connectionsOut struct {
	Connections []Connection `json:"connections"`
	// Total is the number of flows matching the filter before paging, so a
	// short page is not mistaken for a quiet network.
	Total int `json:"total" jsonschema:"matching flows before the limit was applied"`
}

type pingLogsIn struct {
	TunnelID string `json:"tunnelId,omitempty" jsonschema:"only this tunnel's checks; omit for all"`
	Limit    int    `json:"limit,omitempty" jsonschema:"1..200, default 100"`
}

type pingLogsOut struct {
	Entries []PingCheckLogEntry `json:"entries" jsonschema:"newest first"`
}

func registerObservabilityTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_connections",
		Description: "Open network flows through the router: source device, destination, protocol and which tunnel each one takes. " +
			"Use it to answer what a device is actually doing and where its traffic goes. Snapshot of the moment it is called.",
		Annotations: readOnly("List connections"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in connectionsIn) (*mcp.CallToolResult, connectionsOut, error) {
		if in.TunnelID != "" {
			if err := requireTunnelID(in.TunnelID); err != nil {
				return nil, connectionsOut{}, err
			}
		}
		clientIP := ""
		if strings.TrimSpace(in.ClientIP) != "" {
			// An address, not free text: the adapter matches it exactly
			// against the flow source, so only the canonical spelling
			// may cross the boundary.
			ip := net.ParseIP(strings.TrimSpace(in.ClientIP))
			if ip == nil || ip.To4() == nil {
				return nil, connectionsOut{}, fmt.Errorf("clientIp %q is not a valid IPv4 address", in.ClientIP)
			}
			clientIP = ip.To4().String()
		}
		if in.Limit <= 0 {
			in.Limit = 50
		}
		if in.Limit > MaxConnectionsInOutput {
			in.Limit = MaxConnectionsInOutput
		}
		list, total, err := d.ListConnections(ctx, ConnectionsQuery{TunnelID: in.TunnelID, ClientIP: clientIP, Limit: in.Limit})
		if list == nil {
			list = []Connection{}
		}
		return nil, connectionsOut{Connections: list, Total: total}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_pingcheck_logs",
		Description: "History of the automatic tunnel health checks: when a tunnel started failing, when it recovered, and what the latency was. " +
			"get_monitoring_matrix shows only the present; this answers \"since when\".",
		Annotations: readOnly("Ping check log"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pingLogsIn) (*mcp.CallToolResult, pingLogsOut, error) {
		if in.TunnelID != "" {
			if err := requireTunnelID(in.TunnelID); err != nil {
				return nil, pingLogsOut{}, err
			}
		}
		if in.Limit <= 0 {
			in.Limit = 100
		}
		if in.Limit > MaxPingCheckLogEntries {
			in.Limit = MaxPingCheckLogEntries
		}
		entries, err := d.PingCheckLogs(ctx, in.TunnelID, in.Limit)
		if entries == nil {
			entries = []PingCheckLogEntry{}
		}
		return nil, pingLogsOut{Entries: entries}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "run_diagnostics",
		Description: "Start a full diagnostic sweep of the router in the background: interfaces, routes, firewall, kernel module, per-tunnel checks. " +
			"It takes tens of seconds; call get_diagnostics afterwards for the outcome. Changes nothing.",
		Annotations: readOnly("Run diagnostics"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, DiagnosticsRun, error) {
		out, err := d.RunDiagnostics(ctx)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_diagnostics",
		Description: "Outcome of the last diagnostic sweep: how many checks passed, and every check that did NOT — failures first, each with the detail explaining it. " +
			"The full report is far larger than a tool result and stays in the web interface; this is the part that says what is wrong.",
		Annotations: readOnly("Diagnostics result"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, DiagnosticsResult, error) {
		out, err := d.DiagnosticsResult(ctx)
		if err != nil {
			return nil, DiagnosticsResult{}, err
		}
		if out.Problems == nil {
			out.Problems = []DiagnosticsProblem{}
		}
		if out.Status == "running" {
			// Said in text as well: a zero-count result with status
			// "running" is easy to read as a clean sweep.
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
				Text: "The diagnostic sweep is still running; the counts above are not a result. Call get_diagnostics again in about half a minute.",
			}}}, out, nil
		}
		return nil, out, nil
	})
}

// ErrNoDiagnostics is what DiagnosticsResult reports when nothing has run
// yet. The message names the tool that starts a run, because "no report"
// on its own reads as "nothing is wrong".
var ErrNoDiagnostics = fmt.Errorf("no diagnostics report yet — call run_diagnostics first, then try again in half a minute")
