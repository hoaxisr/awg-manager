package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type awgParamsIn struct {
	Jc   *int    `json:"jc,omitempty" jsonschema:"junk packet count"`
	Jmin *int    `json:"jmin,omitempty" jsonschema:"junk packet minimum size"`
	Jmax *int    `json:"jmax,omitempty" jsonschema:"junk packet maximum size"`
	S1   *int    `json:"s1,omitempty" jsonschema:"init packet junk size"`
	S2   *int    `json:"s2,omitempty" jsonschema:"response packet junk size"`
	S3   *int    `json:"s3,omitempty" jsonschema:"cookie packet junk size (AmneziaWG 2)"`
	S4   *int    `json:"s4,omitempty" jsonschema:"transport packet junk size (AmneziaWG 2)"`
	H1   *string `json:"h1,omitempty" jsonschema:"init packet header, a number or a min-max range"`
	H2   *string `json:"h2,omitempty" jsonschema:"response packet header"`
	H3   *string `json:"h3,omitempty" jsonschema:"cookie packet header"`
	H4   *string `json:"h4,omitempty" jsonschema:"transport packet header"`
}

type tunnelSettingsIn struct {
	TunnelID            string       `json:"tunnelId" jsonschema:"tunnel id from list_tunnels"`
	MTU                 *int         `json:"mtu,omitempty" jsonschema:"interface MTU, 576–9000"`
	DNS                 *string      `json:"dns,omitempty" jsonschema:"DNS servers, comma-separated as in the .conf; empty clears them"`
	PersistentKeepalive *string      `json:"persistentKeepalive,omitempty" jsonschema:"seconds between keepalives (0 turns them off), or a min-max range on AmneziaWG 2/3"`
	ISPInterface        *string      `json:"ispInterface,omitempty" jsonschema:"connection the tunnel goes out through: auto (the router's default), a WAN id from list_routing_targets (wan:…), or tunnel:<id> to chain it through another awg-manager tunnel (type managed in list_routing_targets). A running tunnel restarts to apply it"`
	ConnectivityCheck   *string      `json:"connectivityCheck,omitempty" jsonschema:"how the tunnel's health is judged: http, ping, handshake or disabled"`
	PingTarget          string       `json:"pingTarget,omitempty" jsonschema:"IP address to ping, for connectivityCheck ping"`
	AWG                 *awgParamsIn `json:"awg,omitempty" jsonschema:"AmneziaWG obfuscation parameters to change; both ends must match, so change them only to match the server"`
}

type tunnelSettingsOut struct {
	TunnelDetail
	Warnings []string `json:"warnings,omitempty" jsonschema:"address conflicts the edit introduced — show them to the user"`
}

var headerShape = regexp.MustCompile(`^[0-9]{1,10}(-[0-9]{1,10})?$`)

func registerTunnelSettingsTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "update_tunnel_settings",
		Description: "Change individual settings of a WireGuard/AmneziaWG tunnel — MTU, DNS, keepalive, the connection it goes out through, its health check, AmneziaWG obfuscation — without replacing its config. " +
			"The same checks as the web interface's tunnel card apply; a running tunnel takes the change live, and restarts when the connection changes. Omitted fields are kept. " +
			"Refused for a locked tunnel; a proxy-backed (wdtt-raw) tunnel takes only connectivityCheck. The backend (kernel or NativeWG) cannot be changed: it is chosen on import.",
		Annotations: safeWrite("Update tunnel settings", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tunnelSettingsIn) (*mcp.CallToolResult, tunnelSettingsOut, error) {
		if err := requireTunnelID(in.TunnelID); err != nil {
			return nil, tunnelSettingsOut{}, err
		}
		out := TunnelSettingsInput{TunnelID: in.TunnelID, MTU: in.MTU, DNS: in.DNS, PersistentKeepalive: in.PersistentKeepalive, PingTarget: strings.TrimSpace(in.PingTarget)}
		if in.MTU == nil && in.DNS == nil && in.PersistentKeepalive == nil && in.ISPInterface == nil && in.ConnectivityCheck == nil && in.AWG == nil {
			return nil, tunnelSettingsOut{}, fmt.Errorf("nothing to change: pass at least one setting")
		}
		if in.MTU != nil && (*in.MTU < 576 || *in.MTU > 9000) {
			return nil, tunnelSettingsOut{}, fmt.Errorf("mtu must be between 576 and 9000")
		}
		if in.DNS != nil && strings.ContainsAny(*in.DNS, "\r\n=[]") {
			return nil, tunnelSettingsOut{}, fmt.Errorf("dns must be addresses separated by commas")
		}
		if in.PersistentKeepalive != nil && !headerShape.MatchString(strings.TrimSpace(*in.PersistentKeepalive)) {
			return nil, tunnelSettingsOut{}, fmt.Errorf("persistentKeepalive must be a number of seconds or a min-max range")
		}
		if in.ISPInterface != nil {
			v := strings.TrimSpace(*in.ISPInterface)
			if v != "auto" && !strings.HasPrefix(v, "wan:") && !strings.HasPrefix(v, "tunnel:") || !routeTargetShape.MatchString(v) {
				return nil, tunnelSettingsOut{}, fmt.Errorf("ispInterface must be auto, a wan: id from list_routing_targets, or tunnel:<id>")
			}
			out.ISPInterface = &v
		}
		if in.ConnectivityCheck != nil {
			m := strings.ToLower(strings.TrimSpace(*in.ConnectivityCheck))
			switch m {
			case "http", "handshake", "disabled":
			case "ping":
				if out.PingTarget == "" {
					return nil, tunnelSettingsOut{}, fmt.Errorf("pingTarget is required for connectivityCheck ping")
				}
			default:
				return nil, tunnelSettingsOut{}, fmt.Errorf("connectivityCheck must be http, ping, handshake or disabled")
			}
			out.ConnectivityCheck = &m
		}
		if in.AWG != nil {
			a := in.AWG
			for _, h := range []*string{a.H1, a.H2, a.H3, a.H4} {
				if h != nil && !headerShape.MatchString(strings.TrimSpace(*h)) {
					return nil, tunnelSettingsOut{}, fmt.Errorf("an AmneziaWG header (h1–h4) must be a number or a min-max range")
				}
			}
			for _, n := range []*int{a.Jc, a.Jmin, a.Jmax, a.S1, a.S2, a.S3, a.S4} {
				if n != nil && *n < 0 {
					return nil, tunnelSettingsOut{}, fmt.Errorf("AmneziaWG sizes and counts must not be negative")
				}
			}
			out.AWG = &AWGParamsInput{Jc: a.Jc, Jmin: a.Jmin, Jmax: a.Jmax, S1: a.S1, S2: a.S2, S3: a.S3, S4: a.S4, H1: a.H1, H2: a.H2, H3: a.H3, H4: a.H4}
		}
		detail, warnings, err := d.UpdateTunnelSettings(ctx, out)
		if err != nil {
			return nil, tunnelSettingsOut{}, err
		}
		return nil, tunnelSettingsOut{TunnelDetail: detail, Warnings: warnings}, nil
	})
}
