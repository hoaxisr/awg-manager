package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxUserConfigLen is the REST editor's cap on the expert slot.
const maxUserConfigLen = 1 << 20

type routerModeIn struct {
	Mode string `json:"mode" jsonschema:"off, tproxy, fakeip-tun or policy-tun"`
}

type routerModeOut struct {
	Started bool   `json:"started"`
	Mode    string `json:"mode"`
}

type routerSettingsIn struct {
	Sniffer       *bool   `json:"sniffer,omitempty" jsonschema:"protocol sniffing (TLS SNI, HTTP Host) on or off"`
	UDPTimeout    *string `json:"udpTimeout,omitempty" jsonschema:"how long an idle UDP session is kept, as sing-box writes it (5m)"`
	BypassPorts   *string `json:"bypassPorts,omitempty" jsonschema:"extra ports that never go through sing-box, comma-separated; empty clears them"`
	BypassSubnets *string `json:"bypassSubnets,omitempty" jsonschema:"extra subnets that never go through sing-box, comma-separated; empty clears them"`
	PolicyName    *string `json:"policyName,omitempty" jsonschema:"access policy whose devices go through sing-box, from list_access_policies"`
	DeviceMode    *string `json:"deviceMode,omitempty" jsonschema:"which LAN devices are routed through sing-box, as get_singbox_router_status reports it"`
}

type userConfigIn struct {
	Content string `json:"content" jsonschema:"the whole 90-user.json: a JSON object in sing-box's configuration format, merged over the generated slots"`
}

type discardOutUser struct {
	Discarded bool `json:"discarded"`
}

func registerRouterEngineTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_router_mode",
		Description: "Switch the sing-box router's routing mode: off, tproxy, fakeip-tun or policy-tun. The switch tears the old mode down and brings the new one up — traffic through sing-box stops meanwhile — and rolls back if the new mode fails. " +
			"It starts and returns at once; it takes tens of seconds, and get_singbox_router_status shows the outcome. Ask the user before calling it.",
		Annotations: safeWrite("Switch sing-box router mode", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in routerModeIn) (*mcp.CallToolResult, routerModeOut, error) {
		mode := strings.ToLower(strings.TrimSpace(in.Mode))
		switch mode {
		case "off", "tproxy", "fakeip-tun", "policy-tun":
		default:
			return nil, routerModeOut{}, fmt.Errorf("mode must be off, tproxy, fakeip-tun or policy-tun")
		}
		if err := d.SwitchSingboxRouterMode(ctx, mode); err != nil {
			return nil, routerModeOut{}, err
		}
		text := fmt.Sprintf("The switch to %s has started. It is not done yet: check get_singbox_router_status in half a minute — routingMode and active say whether it took.", mode)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, routerModeOut{Started: true, Mode: mode}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_router_settings",
		Description: "Change the sing-box router's engine settings: sniffing, the idle UDP timeout, extra ports and subnets that bypass sing-box, the access policy whose devices it handles, the device mode. Omitted fields are kept. " +
			"Applied at once; interception rules are rebuilt.",
		Annotations: safeWrite("Set sing-box router settings", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in routerSettingsIn) (*mcp.CallToolResult, SingboxRouterStatus, error) {
		p := SingboxRouterSettingsPatch{Sniffer: in.Sniffer, UDPTimeout: in.UDPTimeout, BypassPorts: in.BypassPorts, BypassSubnets: in.BypassSubnets, PolicyName: in.PolicyName, DeviceMode: in.DeviceMode}
		if p == (SingboxRouterSettingsPatch{}) {
			return nil, SingboxRouterStatus{}, fmt.Errorf("nothing to change: pass at least one setting")
		}
		for name, v := range map[string]*string{"udpTimeout": in.UDPTimeout, "bypassPorts": in.BypassPorts, "bypassSubnets": in.BypassSubnets, "policyName": in.PolicyName, "deviceMode": in.DeviceMode} {
			if v != nil && (strings.ContainsAny(*v, "\r\n") || len(*v) > 8192) {
				return nil, SingboxRouterStatus{}, fmt.Errorf("%s must be one line", name)
			}
		}
		if in.PolicyName != nil && *in.PolicyName != "" {
			if _, err := requirePolicyName(*in.PolicyName); err != nil {
				return nil, SingboxRouterStatus{}, err
			}
		}
		out, err := d.UpdateSingboxRouterSettings(ctx, p)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_singbox_user_config",
		Description: "The sing-box router's expert configuration (90-user.json): hand-written sing-box JSON merged over the generated configuration, the draft when there is one. " +
			"It can hold credentials, so a read-only key is refused it.",
		Annotations: readOnly("Get sing-box expert config"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, SingboxUserConfig, error) {
		out, err := d.GetSingboxUserConfig(ctx)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "save_singbox_user_config",
		Description: "Save the expert configuration (90-user.json) as a DRAFT and check it against the rest of sing-box's configuration. Nothing changes until apply_singbox_user_config. " +
			"Pass the whole file; read it with get_singbox_user_config first. A mistake here can stop sing-box from starting, which is why apply refuses a draft that fails the check.",
		Annotations: safeWrite("Save sing-box expert config draft", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in userConfigIn) (*mcp.CallToolResult, SingboxConfigCheck, error) {
		raw := []byte(strings.TrimSpace(in.Content))
		if len(raw) == 0 || len(raw) > maxUserConfigLen {
			return nil, SingboxConfigCheck{}, fmt.Errorf("content must be a JSON object up to %d bytes", maxUserConfigLen)
		}
		var obj map[string]json.RawMessage
		// null decodes into a nil map without an error; it is not an object.
		if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
			return nil, SingboxConfigCheck{}, fmt.Errorf("content must be a JSON object")
		}
		out, err := d.SaveSingboxUserConfig(ctx, raw)
		if err != nil {
			return nil, SingboxConfigCheck{}, err
		}
		text := "The draft is saved, not applied. "
		if out.Ok {
			text += "It passed the check; apply_singbox_user_config applies it."
		} else {
			text += "It FAILED the check (see errors); apply would refuse it."
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "apply_singbox_user_config",
		Description: "Apply the expert configuration draft: sing-box checks the merged configuration and reloads. A draft that fails the check is refused and nothing changes. " +
			"The draft is shared with the web interface's editor.",
		Annotations: safeWrite("Apply sing-box expert config", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, SingboxConfigCheck, error) {
		out, err := d.ApplySingboxUserConfig(ctx)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "discard_singbox_user_config",
		Description: "Throw away the expert configuration draft; the applied 90-user.json stays. DESTRUCTIVE: the draft is shared with the web interface's editor, so unsaved edits made there are lost too. Confirm with the user first.",
		Annotations: destructiveWrite("Discard sing-box expert config draft", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, discardOutUser, error) {
		if err := d.DiscardSingboxUserConfig(ctx); err != nil {
			return nil, discardOutUser{}, err
		}
		return nil, discardOutUser{Discarded: true}, nil
	})
}
