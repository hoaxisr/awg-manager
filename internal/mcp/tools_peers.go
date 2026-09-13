package mcp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type serverIDIn struct {
	ServerID string `json:"serverId" jsonschema:"id of a server with managed=true in list_managed_servers"`
}

type peerRefIn struct {
	ServerID  string `json:"serverId" jsonschema:"id of a server with managed=true in list_managed_servers"`
	PublicKey string `json:"publicKey" jsonschema:"peer public key from list_server_peers"`
}

type setPeerEnabledIn struct {
	ServerID  string `json:"serverId" jsonschema:"id of a server with managed=true in list_managed_servers"`
	PublicKey string `json:"publicKey" jsonschema:"peer public key from list_server_peers"`
	Enabled   bool   `json:"enabled" jsonschema:"false suspends the client without deleting it"`
}

type peersOut struct {
	ServerID string       `json:"serverId"`
	Peers    []ServerPeer `json:"peers"`
}

type peerConfigOut struct {
	ServerID  string `json:"serverId"`
	PublicKey string `json:"publicKey"`
	Config    string `json:"config" jsonschema:".conf text including the client's PRIVATE key"`
}

// requireServerID rejects an empty id before it reaches Deps. Unlike
// tunnel ids these are NDMS interface names (Wireguard0), so there is no
// shared validator; the emptiness check is what stops a blank id from
// being read as "the first server".
func requireServerID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("serverId is required (use list_managed_servers)")
	}
	return nil
}

func requirePublicKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("publicKey is required (use list_server_peers)")
	}
	return nil
}

// MaxPeerDescriptionRunes caps a peer description. It travels to the NDMS
// peer comment, settings.json and the journal; a multi-megabyte value from
// a valid key would bloat all three on a router with tens of MB of RAM.
const MaxPeerDescriptionRunes = 64

func validatePeerDescription(d string) (string, error) {
	d = strings.TrimSpace(d)
	if d == "" {
		return "", fmt.Errorf("description is required — it is how the user recognises this client later")
	}
	if utf8.RuneCountInString(d) > MaxPeerDescriptionRunes {
		return "", fmt.Errorf("description is longer than %d characters", MaxPeerDescriptionRunes)
	}
	for _, r := range d {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("description must not contain control characters")
		}
	}
	return d, nil
}

// validatePeerDNS mirrors managed.ValidatePeerDNS at the tool boundary, so
// the model gets the refusal with the field named rather than a service
// error. The value ends up verbatim in the client .conf: anything but IP
// addresses is a line the user's wg-quick would execute.
func validatePeerDNS(dns string) (string, error) {
	if strings.TrimSpace(dns) == "" {
		return "", nil
	}
	parts := strings.Split(dns, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		ip := net.ParseIP(strings.TrimSpace(p))
		if ip == nil {
			return "", fmt.Errorf("dns must be a comma-separated list of IP addresses; %q is not one", strings.TrimSpace(p))
		}
		out = append(out, ip.String())
	}
	return strings.Join(out, ", "), nil
}

func registerPeerTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_server_peers",
		Description: "Clients of a WireGuard server hosted on this router: who they are, their address inside the tunnel and whether each is enabled. " +
			"Keys that could be used to connect are not returned — get_server_peer_config renders a client's configuration.",
		Annotations: readOnly("List server peers"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in serverIDIn) (*mcp.CallToolResult, peersOut, error) {
		if err := requireServerID(in.ServerID); err != nil {
			return nil, peersOut{}, err
		}
		list, err := d.ListServerPeers(ctx, in.ServerID)
		if list == nil {
			list = []ServerPeer{}
		}
		return nil, peersOut{ServerID: in.ServerID, Peers: list}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_server_peer",
		Description: "Add a client to a WireGuard server hosted on this router. Keys are generated on the router, and the address inside the tunnel is allocated automatically unless you pass one. " +
			"The peer is created enabled. This produces working VPN credentials: call it only when the user asked for a new client, then hand them get_server_peer_config.",
		Annotations: safeWrite("Add server peer", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AddPeerInput) (*mcp.CallToolResult, ServerPeer, error) {
		if err := requireServerID(in.ServerID); err != nil {
			return nil, ServerPeer{}, err
		}
		// A peer with no description cannot be told apart from the others
		// later, and the peer list is the only place the user sees it.
		desc, err := validatePeerDescription(in.Description)
		if err != nil {
			return nil, ServerPeer{}, err
		}
		in.Description = desc
		dns, err := validatePeerDNS(in.DNS)
		if err != nil {
			return nil, ServerPeer{}, err
		}
		in.DNS = dns
		if in.TunnelIP != "" {
			in.TunnelIP = strings.TrimSpace(in.TunnelIP)
			if _, _, err := net.ParseCIDR(in.TunnelIP); err != nil {
				return nil, ServerPeer{}, fmt.Errorf("tunnelIp %q is not a CIDR address like 10.0.0.5/32", in.TunnelIP)
			}
		}
		out, err := d.AddServerPeer(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_server_peer_enabled",
		Description: "Suspend or restore one client of a hosted WireGuard server. A disabled peer keeps its keys and address and can be switched back on; " +
			"deleting a peer is not available through MCP.",
		Annotations: safeWrite("Enable/disable server peer", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setPeerEnabledIn) (*mcp.CallToolResult, ServerPeer, error) {
		if err := requireServerID(in.ServerID); err != nil {
			return nil, ServerPeer{}, err
		}
		if err := requirePublicKey(in.PublicKey); err != nil {
			return nil, ServerPeer{}, err
		}
		out, err := d.SetServerPeerEnabled(ctx, in.ServerID, in.PublicKey, in.Enabled)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_server_peer_config",
		Description: "Render one client's WireGuard configuration for a hosted server. WARNING: it contains the client's private key — only call it when the user asked for the config, " +
			"and pass it to them directly rather than repeating it anywhere else.",
		Annotations: readOnly("Get server peer config"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in peerRefIn) (*mcp.CallToolResult, peerConfigOut, error) {
		if err := requireServerID(in.ServerID); err != nil {
			return nil, peerConfigOut{}, err
		}
		if err := requirePublicKey(in.PublicKey); err != nil {
			return nil, peerConfigOut{}, err
		}
		conf, err := d.ServerPeerConfig(ctx, in.ServerID, in.PublicKey)
		if err != nil {
			return nil, peerConfigOut{}, err
		}
		return nil, peerConfigOut{ServerID: in.ServerID, PublicKey: in.PublicKey, Config: conf}, nil
	})
}
