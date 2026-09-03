package mcp

import "context"

// Deps is everything the tools need from the host. Production wires
// localdeps (internal services); tests and cmd/mcp-dev wire mcptest.Fake.
// Implementations return plain errors with user-facing messages: tools
// forward err.Error() to the model verbatim.
type Deps interface {
	SystemStatus(ctx context.Context) (SystemStatus, error)

	ListTunnels(ctx context.Context) ([]TunnelSummary, error)
	GetTunnel(ctx context.Context, id string) (TunnelDetail, error)
	ControlTunnel(ctx context.Context, id, action string) error
	ImportTunnel(ctx context.Context, name, config string) (TunnelSummary, error)
	ReplaceTunnelConfig(ctx context.Context, id, config, newName string) error
	ExportTunnelConfig(ctx context.Context, id string) (string, error)

	ListDNSRoutes(ctx context.Context) ([]DNSRoute, error)
	AddDNSRoute(ctx context.Context, in DNSRouteInput) (DNSRoute, error)
	RemoveDNSRoute(ctx context.Context, id string) error

	ListStaticRoutes(ctx context.Context) ([]StaticRoute, error)
	AddStaticRoute(ctx context.Context, in StaticRouteInput) (StaticRoute, error)
	RemoveStaticRoute(ctx context.Context, id string) error

	ListClientRoutes(ctx context.Context) ([]ClientRoute, error)
	SetClientRoute(ctx context.Context, in ClientRouteInput) (*ClientRoute, error) // nil when removed

	ListAccessPolicies(ctx context.Context) ([]AccessPolicy, error)
	ListDevices(ctx context.Context) ([]Device, error)

	GetLogs(ctx context.Context, q LogsQuery) ([]LogEntry, int, error) // entries, total matched
	TestConnectivity(ctx context.Context, tunnelID string) (ConnectivityResult, error)
	MonitoringMatrix(ctx context.Context) (MonitoringMatrix, error)
	RunPingCheck(ctx context.Context) ([]PingCheckStatus, error)

	ListManagedServers(ctx context.Context) ([]ManagedServer, error)
	ControlSingbox(ctx context.Context, action string) (SingboxStatus, error)

	OpenAPISpec() []byte
}
