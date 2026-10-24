package mcp

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type createSubscriptionIn struct {
	Label         string `json:"label" jsonschema:"the subscription's name, shown in the web interface"`
	URL           string `json:"url,omitempty" jsonschema:"http or https address of the provider's server list; give this or links. It usually carries the provider's token: take it only from the user"`
	Links         string `json:"links,omitempty" jsonschema:"share links (vless://, hysteria2://, …), one per line, for a pasted subscription; give this or url"`
	RefreshHours  *int   `json:"refreshHours,omitempty" jsonschema:"how often a URL subscription is fetched again, in hours; omitted means 24, as in the web form; 0 means only by hand"`
	Mode          string `json:"mode,omitempty" jsonschema:"selector (default: one server chosen by the user), urltest (the fastest) or fallback (the first live one)"`
	FilterInclude string `json:"filterInclude,omitempty" jsonschema:"regular expression: keep only servers whose name matches"`
	FilterExclude string `json:"filterExclude,omitempty" jsonschema:"regular expression: drop servers whose name matches"`
}

type subscriptionIDIn struct {
	SubscriptionID string `json:"subscriptionId" jsonschema:"subscription id from list_singbox_subscriptions"`
}

type subscriptionSettingsIn struct {
	SubscriptionID string  `json:"subscriptionId" jsonschema:"subscription id from list_singbox_subscriptions"`
	Label          *string `json:"label,omitempty" jsonschema:"new name; omit to keep it"`
	RefreshHours   *int    `json:"refreshHours,omitempty" jsonschema:"hours between automatic fetches, 0 for only by hand; omit to keep it"`
	FilterInclude  *string `json:"filterInclude,omitempty" jsonschema:"regular expression for servers to keep, empty to drop the filter; omit to keep it. A change refetches the subscription"`
	FilterExclude  *string `json:"filterExclude,omitempty" jsonschema:"regular expression for servers to drop, empty to drop the filter; omit to keep it. A change refetches the subscription"`
}

type subscriptionServersIn struct {
	SubscriptionID string   `json:"subscriptionId" jsonschema:"subscription id from list_singbox_subscriptions"`
	Tags           []string `json:"tags" jsonschema:"server tags, as get_singbox_outbound lists them for the subscription's groupTag"`
}

type addTunnelIn struct {
	ShareLink string `json:"shareLink" jsonschema:"one proxy share link (vless://, hysteria2://, trojan://, ss://, …) the user gave you; it carries the proxy's credentials"`
}

type tunnelTagIn struct {
	Tag string `json:"tag" jsonschema:"proxy tag from list_singbox_tunnels"`
}

type deletedSubscriptionOut struct {
	Deleted SingboxSubscription `json:"deleted" jsonschema:"the subscription as it was; MCP cannot recreate it without its address"`
}

type deletedTunnelOut struct {
	Deleted SingboxTunnel `json:"deleted" jsonschema:"the proxy as it was; MCP cannot recreate it without its share link"`
}

// maxLinksLen bounds a pasted list of share links.
const maxLinksLen = 256 << 10

// shareLinkShape is a single share link: a scheme, then no whitespace.
var shareLinkShape = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://\S+$`)

// checkRegex bounds a filter before Deps; the service compiles it.
func checkRegex(name string, s string) error {
	if len(s) > 1024 {
		return fmt.Errorf("%s is longer than 1024 bytes", name)
	}
	if strings.ContainsAny(s, "\r\n") {
		return fmt.Errorf("%s must be one line", name)
	}
	return nil
}

// addedTunnelOut is the added proxy, as list_singbox_tunnels shows it, and
// what went wrong around it.
type addedTunnelOut struct {
	SingboxTunnel
	Warnings []string `json:"warnings,omitempty" jsonschema:"the proxy is added, but something around it needs a look"`
}

func registerSubscriptionManageTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_singbox_subscription",
		Description: "Create a sing-box subscription — a provider's server list fetched from url, or a pasted one made of share links — and fetch it now; it becomes a group routing rules can point at. " +
			"The URL and the links carry the provider's token or the servers' credentials: use only ones the user gave you for this, and do not repeat them. " +
			"If the fetch fails or yields no server, nothing is created. When NDMS proxy mode is on, a Keenetic ProxyN interface is created for it.",
		Annotations: safeWrite("Create sing-box subscription", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createSubscriptionIn) (*mcp.CallToolResult, SingboxSubscription, error) {
		label := strings.TrimSpace(in.Label)
		if label == "" || len([]rune(label)) > MaxSingboxLabelRunes || strings.ContainsAny(label, "\r\n") {
			return nil, SingboxSubscription{}, fmt.Errorf("label is required: one line, up to %d characters", MaxSingboxLabelRunes)
		}
		hours := 24
		if in.RefreshHours != nil {
			hours = *in.RefreshHours
		}
		sub := SingboxSubscriptionInput{Label: label, URL: strings.TrimSpace(in.URL), Links: strings.TrimSpace(in.Links), RefreshHours: hours,
			Mode: strings.ToLower(strings.TrimSpace(in.Mode)), FilterInclude: in.FilterInclude, FilterExclude: in.FilterExclude}
		switch {
		case (sub.URL == "") == (sub.Links == ""):
			return nil, SingboxSubscription{}, fmt.Errorf("give url or links, one of the two")
		case sub.URL != "":
			u, err := url.Parse(sub.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(sub.URL, " \t\r\n") {
				return nil, SingboxSubscription{}, fmt.Errorf("url must be an http or https address")
			}
		case len(sub.Links) > maxLinksLen:
			return nil, SingboxSubscription{}, fmt.Errorf("links is longer than %d bytes", maxLinksLen)
		}
		if hours < 0 || hours > 24*30 {
			return nil, SingboxSubscription{}, fmt.Errorf("refreshHours must be between 0 and %d", 24*30)
		}
		for name, f := range map[string]string{"filterInclude": in.FilterInclude, "filterExclude": in.FilterExclude} {
			if err := checkRegex(name, f); err != nil {
				return nil, SingboxSubscription{}, err
			}
		}
		created, err := d.CreateSingboxSubscription(ctx, sub)
		if err != nil {
			return nil, SingboxSubscription{}, err
		}
		text := fmt.Sprintf("The subscription is created and fetched: its group %q has %d servers and sing-box runs it now. Point routing rules at the group with set_singbox_rule_outbound.", created.GroupTag, created.MemberCount)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, created, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_singbox_subscription",
		Description: "Delete a sing-box subscription with its group and servers. Refused while routing rules, groups or the device proxy still use its group or servers — retarget them first. " +
			"DESTRUCTIVE: applied at once, and MCP cannot recreate the subscription without its address. Confirm with the user first.",
		Annotations: destructiveWrite("Delete sing-box subscription", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in subscriptionIDIn) (*mcp.CallToolResult, deletedSubscriptionOut, error) {
		id, err := requireSubscriptionID(in.SubscriptionID)
		if err != nil {
			return nil, deletedSubscriptionOut{}, err
		}
		deleted, err := d.DeleteSingboxSubscription(ctx, id)
		if err != nil {
			return nil, deletedSubscriptionOut{}, err
		}
		return nil, deletedSubscriptionOut{Deleted: deleted}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "refresh_singbox_subscription",
		Description: "Fetch a sing-box subscription now (\"Обновить сейчас\") and wait for the result: servers added, changed and gone. " +
			"Changes reload sing-box, which can interrupt open connections for a moment. A pasted subscription has nothing to fetch.",
		Annotations: safeWrite("Refresh sing-box subscription", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in subscriptionIDIn) (*mcp.CallToolResult, SingboxRefreshResult, error) {
		id, err := requireSubscriptionID(in.SubscriptionID)
		if err != nil {
			return nil, SingboxRefreshResult{}, err
		}
		out, err := d.RefreshSingboxSubscription(ctx, id)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_subscription_settings",
		Description: "Change a sing-box subscription's name, refresh interval or server filters. Omitted fields are kept. " +
			"A filter change refetches the subscription and rebuilds its group (sing-box reloads); for a pasted subscription it re-reads the pasted links, undoing servers added or removed by hand.",
		Annotations: safeWrite("Set sing-box subscription settings", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in subscriptionSettingsIn) (*mcp.CallToolResult, SingboxSubscription, error) {
		id, err := requireSubscriptionID(in.SubscriptionID)
		if err != nil {
			return nil, SingboxSubscription{}, err
		}
		if in.Label == nil && in.RefreshHours == nil && in.FilterInclude == nil && in.FilterExclude == nil {
			return nil, SingboxSubscription{}, fmt.Errorf("nothing to change: pass at least one field")
		}
		p := SingboxSubscriptionPatch{ID: id, RefreshHours: in.RefreshHours, FilterInclude: in.FilterInclude, FilterExclude: in.FilterExclude}
		if in.Label != nil {
			label := strings.TrimSpace(*in.Label)
			if label == "" || len([]rune(label)) > MaxSingboxLabelRunes || strings.ContainsAny(label, "\r\n") {
				return nil, SingboxSubscription{}, fmt.Errorf("label must be one line, 1 to %d characters", MaxSingboxLabelRunes)
			}
			p.Label = &label
		}
		if in.RefreshHours != nil && (*in.RefreshHours < 0 || *in.RefreshHours > 24*30) {
			return nil, SingboxSubscription{}, fmt.Errorf("refreshHours must be between 0 and %d", 24*30)
		}
		for name, f := range map[string]*string{"filterInclude": in.FilterInclude, "filterExclude": in.FilterExclude} {
			if f != nil {
				if err := checkRegex(name, *f); err != nil {
					return nil, SingboxSubscription{}, err
				}
			}
		}
		out, err := d.UpdateSingboxSubscriptionSettings(ctx, p)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "exclude_singbox_subscription_servers",
		Description: "Take servers out of a fetched sing-box subscription's group; they stay out across refreshes until restored. The group is rebuilt and sing-box reloads. " +
			"Not for a pasted subscription. A group cannot be left with no server.",
		Annotations: safeWrite("Exclude sing-box subscription servers", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in subscriptionServersIn) (*mcp.CallToolResult, SingboxSubscription, error) {
		id, err := requireSubscriptionID(in.SubscriptionID)
		if err != nil {
			return nil, SingboxSubscription{}, err
		}
		tags, err := requireSingboxTags(in.Tags, "tags", "get_singbox_outbound with the subscription's groupTag")
		if err != nil {
			return nil, SingboxSubscription{}, err
		}
		out, err := d.ExcludeSingboxSubscriptionServers(ctx, id, tags)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "restore_singbox_subscription_servers",
		Description: "Bring excluded servers back into a sing-box subscription's group. The subscription is refetched (a network request) and sing-box reloads. " +
			"Excluded servers are counted in list_singbox_subscriptions (excludedCount); the web interface lists their tags.",
		Annotations: safeWrite("Restore sing-box subscription servers", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in subscriptionServersIn) (*mcp.CallToolResult, SingboxSubscription, error) {
		id, err := requireSubscriptionID(in.SubscriptionID)
		if err != nil {
			return nil, SingboxSubscription{}, err
		}
		tags, err := requireSingboxTags(in.Tags, "tags", "the web interface's list of excluded servers")
		if err != nil {
			return nil, SingboxSubscription{}, err
		}
		out, err := d.RestoreSingboxSubscriptionServers(ctx, id, tags)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_singbox_tunnel",
		Description: "Add one hand-configured sing-box proxy from a share link (vless://, hysteria2://, trojan://, ss://, …); it appears in list_singbox_tunnels and can be a member of groups or a rule's outbound. " +
			"The link carries working credentials: use only a link the user gave you for this, and do not repeat it. Applied at once; when NDMS proxy mode is on, a Keenetic ProxyN interface is created for it. " +
			"Configuration files (JSON, TOML) are imported in the web interface.",
		Annotations: safeWrite("Add sing-box proxy", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addTunnelIn) (*mcp.CallToolResult, addedTunnelOut, error) {
		link := strings.TrimSpace(in.ShareLink)
		switch {
		case link == "":
			return nil, addedTunnelOut{}, fmt.Errorf("shareLink is required")
		case len(link) > maxShareLinkLen:
			return nil, addedTunnelOut{}, fmt.Errorf("shareLink is longer than %d bytes", maxShareLinkLen)
		case !shareLinkShape.MatchString(link):
			return nil, addedTunnelOut{}, fmt.Errorf("shareLink must be one share link (scheme://…); add proxies one call at a time")
		}
		out, warnings, err := d.AddSingboxTunnel(ctx, link)
		if err != nil {
			return nil, addedTunnelOut{}, err
		}
		return nil, addedTunnelOut{SingboxTunnel: out, Warnings: warnings}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_singbox_tunnel",
		Description: "Delete a hand-configured sing-box proxy. Refused while routing rules, groups or the device proxy still use it — retarget them first. " +
			"DESTRUCTIVE: applied at once, and MCP cannot recreate the proxy without its share link. Confirm with the user first.",
		Annotations: destructiveWrite("Delete sing-box proxy", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tunnelTagIn) (*mcp.CallToolResult, deletedTunnelOut, error) {
		tag, err := requireSingboxTag(in.Tag, "list_singbox_tunnels")
		if err != nil {
			return nil, deletedTunnelOut{}, err
		}
		deleted, err := d.DeleteSingboxTunnel(ctx, tag)
		if err != nil {
			return nil, deletedTunnelOut{}, err
		}
		return nil, deletedTunnelOut{Deleted: deleted}, nil
	})
}
