package mcp

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type singboxConnectionsIn struct {
	ClientIP string `json:"clientIp,omitempty" jsonschema:"only connections from this LAN device"`
	Host     string `json:"host,omitempty" jsonschema:"only connections whose destination name contains this"`
	Outbound string `json:"outbound,omitempty" jsonschema:"only connections passing through this outbound (a group, a server, a proxy)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"1..100, default 50"`
}

type singboxConnectionsOut struct {
	Connections []SingboxConnection `json:"connections"`
	Total       int                 `json:"total" jsonschema:"connections that matched, ignoring limit"`
	Truncated   bool                `json:"truncated" jsonschema:"true when more matched than are listed"`
}

func registerSingboxObserveTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_singbox_connections",
		Description: "Connections sing-box is carrying right now, read once: from which device to where, the outbound actually carrying each (a group's member, a proxy, direct) and the rule that chose it. " +
			"Use it to confirm traffic goes through the intended group — list_connections shows only the NDMS interface. A snapshot: a connection that closed a moment ago is not in it.",
		Annotations: readOnly("List sing-box connections"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in singboxConnectionsIn) (*mcp.CallToolResult, singboxConnectionsOut, error) {
		q := SingboxConnectionsQuery{Host: strings.TrimSpace(in.Host), Limit: in.Limit}
		if q.Limit <= 0 {
			q.Limit = 50
		}
		if q.Limit > MaxConnectionsInOutput {
			q.Limit = MaxConnectionsInOutput
		}
		if in.ClientIP != "" {
			ip := net.ParseIP(strings.TrimSpace(in.ClientIP))
			if ip == nil {
				return nil, singboxConnectionsOut{}, fmt.Errorf("clientIp %q is not an IP address", in.ClientIP)
			}
			q.ClientIP = ip.String()
		}
		if in.Outbound != "" {
			tag, err := requireSingboxTag(in.Outbound, "list_singbox_outbounds")
			if err != nil {
				return nil, singboxConnectionsOut{}, err
			}
			q.Outbound = tag
		}
		if len(q.Host) > maxTargetLen {
			return nil, singboxConnectionsOut{}, fmt.Errorf("host is longer than %d characters", maxTargetLen)
		}
		page, total, err := d.ListSingboxConnections(ctx, q)
		if err != nil {
			return nil, singboxConnectionsOut{}, err
		}
		return nil, singboxConnectionsOut{Connections: orEmpty(page), Total: total, Truncated: total > len(page)}, nil
	})
}
