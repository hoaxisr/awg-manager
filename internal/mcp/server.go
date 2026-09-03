package mcp

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ResourceOpenAPI is the URI of the embedded swagger spec resource.
const ResourceOpenAPI = "awgm://openapi"

// NewServer builds the MCP server with every v1 tool registered against
// deps. It is transport-agnostic; NewHTTPHandler mounts it.
func NewServer(deps Deps, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "awg-manager",
		Title:   "AWG Manager",
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: "Tools manage AmneziaWG/WireGuard tunnels and routing on a Keenetic router via awg-manager. " +
			"Tunnel ids come from list_tunnels. Writes are reversible; destructive operations (delete tunnel, backup restore, system update) are not exposed.",
	})
	registerSystemTools(s, deps)
	registerTunnelTools(s, deps)
	registerRoutingTools(s, deps)
	registerSingboxTools(s, deps)
	registerServerTools(s, deps)
	s.AddResource(&mcp.Resource{
		URI:         ResourceOpenAPI,
		Name:        "openapi",
		Title:       "AWG Manager REST API (OpenAPI 2.0)",
		Description: "Full swagger spec of the daemon's HTTP API, for capabilities not covered by tools.",
		MIMEType:    "application/yaml",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: ResourceOpenAPI, MIMEType: "application/yaml", Text: string(deps.OpenAPISpec()),
		}}}, nil
	})
	return s
}

// NewHTTPHandler mounts server as a stateless Streamable HTTP endpoint.
// Localhost protection is disabled on purpose: the NDMS/KeenDNS reverse
// proxy reaches the daemon on 127.0.0.1 with the public Host header,
// which the SDK would otherwise reject as DNS rebinding. Auth is enforced
// by KeyMiddleware (auth.go) in front of this handler.
func NewHTTPHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		DisableLocalhostProtection: true,
	})
}

func boolPtr(b bool) *bool { return &b }

// readOnly annotates a tool that never changes router state.
func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)}
}

// safeWrite annotates a reversible mutation.
func safeWrite(title string, idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: false, IdempotentHint: idempotent, DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)}
}

// empty is the input type for tools without arguments.
type empty struct{}
