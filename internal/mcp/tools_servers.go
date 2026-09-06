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
		Name:        "list_managed_servers",
		Description: "WireGuard servers hosted on this router (NDMS WireguardN interfaces) with status and peer counts.",
		Annotations: readOnly("List managed servers"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, serversOut, error) {
		list, err := d.ListManagedServers(ctx)
		if list == nil {
			list = []ManagedServer{}
		}
		return nil, serversOut{Servers: list}, err
	})
}
