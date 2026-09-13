package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type serversOut struct {
	Servers []ManagedServer `json:"servers"`
}

func registerServerTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_managed_servers",
		Description: "WireGuard servers hosted on this router with status and peer counts. Servers with managed=true were created by awg-manager and accept the peer tools; " +
			"the others are NDMS built-in or marked servers that MCP can only observe.",
		Annotations: readOnly("List managed servers"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, serversOut, error) {
		list, err := d.ListManagedServers(ctx)
		if list == nil {
			list = []ManagedServer{}
		}
		return nil, serversOut{Servers: list}, err
	})
}
