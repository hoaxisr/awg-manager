package mcp

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var serverIDShape = regexp.MustCompile(`^Wireguard[0-9]{1,3}$`)

func requireManagedServerID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if !serverIDShape.MatchString(id) {
		return "", fmt.Errorf("serverId must be a server id from list_managed_servers (Wireguard3)")
	}
	return id, nil
}

type createServerIn struct {
	Description string `json:"description,omitempty" jsonschema:"the server's name"`
	Address     string `json:"address,omitempty" jsonschema:"the server's address inside the tunnel (10.8.0.1); omit for a free private subnet"`
	Mask        string `json:"mask,omitempty" jsonschema:"subnet mask, /16 to /30 (24 or 255.255.255.0); omit with address"`
	ListenPort  int    `json:"listenPort" jsonschema:"UDP port clients connect to, 1-65535; must be free"`
	Endpoint    string `json:"endpoint,omitempty" jsonschema:"address clients connect to (a domain or IP); omit for the router's WAN address"`
	DNS         string `json:"dns,omitempty" jsonschema:"DNS servers written into client configs, comma-separated"`
	MTU         int    `json:"mtu,omitempty" jsonschema:"interface MTU; omit for the default"`
	AWG3        bool   `json:"awg3,omitempty" jsonschema:"create an AmneziaWG 3 server (needs firmware support); default AmneziaWG 2"`
}

type updateServerIn struct {
	ServerID    string  `json:"serverId" jsonschema:"server id from list_managed_servers"`
	Description *string `json:"description,omitempty"`
	Address     *string `json:"address,omitempty" jsonschema:"new address inside the tunnel; existing clients keep their old addresses"`
	Mask        *string `json:"mask,omitempty"`
	ListenPort  *int    `json:"listenPort,omitempty" jsonschema:"new port; clients must be given the new one"`
	Endpoint    *string `json:"endpoint,omitempty" jsonschema:"address clients connect to; empty for the WAN address"`
	DNS         *string `json:"dns,omitempty"`
	MTU         *int    `json:"mtu,omitempty"`
}

type serverAccessIn struct {
	ServerID    string    `json:"serverId" jsonschema:"server id from list_managed_servers"`
	NATMode     *string   `json:"natMode,omitempty" jsonschema:"full (clients reach everything through the router), internet-only (the internet, not the LAN) or none"`
	LANSegments *[]string `json:"lanSegments,omitempty" jsonschema:"LAN segments clients may reach, from list_wireguard_server_options; [] lets them reach none"`
	Policy      *string   `json:"policy,omitempty" jsonschema:"access policy client traffic follows, from list_wireguard_server_options; none clears it"`
}

type serverEnabledIn struct {
	ServerID string `json:"serverId" jsonschema:"server id from list_managed_servers"`
	Enabled  bool   `json:"enabled"`
}

type serverObfuscationIn struct {
	ServerID string         `json:"serverId" jsonschema:"server id from list_managed_servers"`
	Params   map[string]any `json:"params" jsonschema:"AmneziaWG parameters to change, by name (jc, jmin, jmax, s1, s2, s3, s4, h1, h2, h3, h4); the rest stay. Clients must be given the same values"`
}

type createdServerOut struct {
	WireguardServer
	Warnings []string `json:"warnings,omitempty" jsonschema:"the server exists, but something needs a look — show these to the user"`
}

// obfuscationKeys are the AmneziaWG parameters the tool accepts; I1–I5
// belong to the peers (managed.SetASCParams strips them).
var obfuscationKeys = map[string]bool{"jc": true, "jmin": true, "jmax": true, "s1": true, "s2": true, "s3": true, "s4": true, "h1": true, "h2": true, "h3": true, "h4": true}

func checkPort(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("listenPort must be between 1 and 65535")
	}
	return nil
}

func checkLine(name, v string) error {
	if strings.ContainsAny(v, "\r\n") || len(v) > 256 {
		return fmt.Errorf("%s must be one line", name)
	}
	return nil
}

func registerWireguardServerTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_wireguard_server_options",
		Description: "What the WireGuard server tools accept: the LAN segments clients may be let into, the access policies a server can follow, and a free private subnet for a new server.",
		Annotations: readOnly("WireGuard server options"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, WireguardServerOptions, error) {
		out, err := d.ListWireguardServerOptions(ctx)
		out.LANSegments, out.Policies = orEmpty(out.LANSegments), orEmpty(out.Policies)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_wireguard_server",
		Description: "Create a WireGuard (AmneziaWG) server on the router and start it, with NAT on so clients reach the internet; add clients with add_server_peer. " +
			"Applied at once in NDMS. A port or subnet already in use is refused. MCP cannot delete a server; that is done in the web interface.",
		Annotations: safeWrite("Create WireGuard server", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createServerIn) (*mcp.CallToolResult, createdServerOut, error) {
		if err := checkPort(in.ListenPort); err != nil {
			return nil, createdServerOut{}, err
		}
		if (in.Address == "") != (in.Mask == "") {
			return nil, createdServerOut{}, fmt.Errorf("give address and mask together, or neither")
		}
		if in.Address != "" && net.ParseIP(strings.TrimSpace(in.Address)).To4() == nil {
			return nil, createdServerOut{}, fmt.Errorf("address must be an IPv4 address")
		}
		for name, v := range map[string]string{"description": in.Description, "endpoint": in.Endpoint, "dns": in.DNS, "mask": in.Mask} {
			if err := checkLine(name, v); err != nil {
				return nil, createdServerOut{}, err
			}
		}
		if in.MTU != 0 && (in.MTU < 576 || in.MTU > 9000) {
			return nil, createdServerOut{}, fmt.Errorf("mtu must be between 576 and 9000")
		}
		created, warnings, err := d.CreateWireguardServer(ctx, WireguardServerInput{
			Description: strings.TrimSpace(in.Description), Address: strings.TrimSpace(in.Address), Mask: strings.TrimSpace(in.Mask), ListenPort: in.ListenPort,
			Endpoint: strings.TrimSpace(in.Endpoint), DNS: strings.TrimSpace(in.DNS), MTU: in.MTU, AWG3: in.AWG3,
		})
		if err != nil {
			return nil, createdServerOut{}, err
		}
		return nil, createdServerOut{WireguardServer: created, Warnings: warnings}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_wireguard_server",
		Description: "Change a WireGuard server's name, address, port, client endpoint, client DNS or MTU. Omitted fields are kept. Applied at once; a new port or address means existing client configs need updating.",
		Annotations: safeWrite("Update WireGuard server", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateServerIn) (*mcp.CallToolResult, WireguardServer, error) {
		id, err := requireManagedServerID(in.ServerID)
		if err != nil {
			return nil, WireguardServer{}, err
		}
		p := WireguardServerPatch{ID: id, Description: in.Description, Address: in.Address, Mask: in.Mask, ListenPort: in.ListenPort, Endpoint: in.Endpoint, DNS: in.DNS, MTU: in.MTU}
		if p == (WireguardServerPatch{ID: id}) {
			return nil, WireguardServer{}, fmt.Errorf("nothing to change: pass at least one field")
		}
		if in.ListenPort != nil {
			if err := checkPort(*in.ListenPort); err != nil {
				return nil, WireguardServer{}, err
			}
		}
		for name, v := range map[string]*string{"description": in.Description, "address": in.Address, "mask": in.Mask, "endpoint": in.Endpoint, "dns": in.DNS} {
			if v != nil {
				if err := checkLine(name, *v); err != nil {
					return nil, WireguardServer{}, err
				}
			}
		}
		out, err := d.UpdateWireguardServer(ctx, p)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_wireguard_server_access",
		Description: "Set what a WireGuard server's clients reach: NAT mode (full, internet-only, none), the LAN segments they may enter, the access policy their traffic follows. Omitted fields are kept. Applied at once.",
		Annotations: safeWrite("Set WireGuard server access", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in serverAccessIn) (*mcp.CallToolResult, WireguardServer, error) {
		id, err := requireManagedServerID(in.ServerID)
		if err != nil {
			return nil, WireguardServer{}, err
		}
		if in.NATMode == nil && in.LANSegments == nil && in.Policy == nil {
			return nil, WireguardServer{}, fmt.Errorf("nothing to change: pass natMode, lanSegments or policy")
		}
		if in.NATMode != nil {
			switch *in.NATMode {
			case "full", "internet-only", "none":
			default:
				return nil, WireguardServer{}, fmt.Errorf("natMode must be full, internet-only or none")
			}
		}
		if in.Policy != nil {
			if err := checkLine("policy", *in.Policy); err != nil || strings.TrimSpace(*in.Policy) == "" {
				return nil, WireguardServer{}, fmt.Errorf("policy must be a policy id from list_wireguard_server_options, or none")
			}
		}
		out, err := d.SetWireguardServerAccess(ctx, WireguardServerAccess{ID: id, NATMode: in.NATMode, LANSegments: in.LANSegments, Policy: in.Policy})
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_wireguard_server_enabled",
		Description: "Bring a WireGuard server up or down. Down disconnects every client until it is brought up again; the server and its clients are kept. Applied at once.",
		Annotations: safeWrite("Enable/disable WireGuard server", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in serverEnabledIn) (*mcp.CallToolResult, WireguardServer, error) {
		id, err := requireManagedServerID(in.ServerID)
		if err != nil {
			return nil, WireguardServer{}, err
		}
		out, err := d.SetWireguardServerEnabled(ctx, id, in.Enabled)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_wireguard_server_obfuscation",
		Description: "Change a WireGuard server's AmneziaWG obfuscation parameters (jc, jmin, jmax, s1–s4, h1–h4); the ones not given stay. " +
			"Clients connect only with the same values, so every client config must be renewed (get_server_peer_config). Applied at once.",
		Annotations: safeWrite("Set WireGuard server obfuscation", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in serverObfuscationIn) (*mcp.CallToolResult, WireguardServer, error) {
		id, err := requireManagedServerID(in.ServerID)
		if err != nil {
			return nil, WireguardServer{}, err
		}
		if len(in.Params) == 0 {
			return nil, WireguardServer{}, fmt.Errorf("params must name at least one parameter")
		}
		params := map[string]any{}
		for k, v := range in.Params {
			key := strings.ToLower(strings.TrimSpace(k))
			if !obfuscationKeys[key] {
				return nil, WireguardServer{}, fmt.Errorf("%q is not a parameter this tool sets (jc, jmin, jmax, s1–s4, h1–h4)", k)
			}
			switch val := v.(type) {
			case float64:
				if val < 0 || val != float64(int64(val)) {
					return nil, WireguardServer{}, fmt.Errorf("%s must be a whole number, not negative", key)
				}
			case string:
				// Only the headers (h1–h4) take a min-max range.
				if !strings.HasPrefix(key, "h") {
					if _, err := strconv.ParseUint(strings.TrimSpace(val), 10, 32); err != nil {
						return nil, WireguardServer{}, fmt.Errorf("%s must be a whole number, not negative", key)
					}
				} else if !headerShape.MatchString(strings.TrimSpace(val)) {
					return nil, WireguardServer{}, fmt.Errorf("%s must be a number or a min-max range", key)
				}
			default:
				return nil, WireguardServer{}, fmt.Errorf("%s must be a number or a min-max range", key)
			}
			params[key] = v
		}
		out, err := d.SetWireguardServerObfuscation(ctx, id, params)
		return nil, out, err
	})
}
