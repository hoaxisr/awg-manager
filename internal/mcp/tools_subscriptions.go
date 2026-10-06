package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type subscriptionsIn struct {
	Offset int `json:"offset,omitempty" jsonschema:"index of the first subscription to return; default 0"`
}

type subscriptionsOut struct {
	Subscriptions []SingboxSubscription `json:"subscriptions"`
	Total         int                   `json:"total" jsonschema:"subscriptions on the router, ignoring paging"`
	Offset        int                   `json:"offset" jsonschema:"index of the first subscription returned"`
	Truncated     bool                  `json:"truncated" jsonschema:"true when subscriptions beyond this page remain — call again with a larger offset before concluding one is absent"`
}

// pageSubscriptions cuts one page. An offset past the end yields an empty
// page rather than an error: an agent walking the pages stops on it.
func pageSubscriptions(all []SingboxSubscription, offset int) subscriptionsOut {
	total := len(all)
	start := min(offset, total)
	end := min(start+MaxSubscriptionsInOutput, total)
	page := all[start:end:end]
	if page == nil {
		page = []SingboxSubscription{}
	}
	return subscriptionsOut{Subscriptions: page, Total: total, Offset: start, Truncated: end < total}
}

type setSubscriptionEnabledIn struct {
	SubscriptionID string `json:"subscriptionId" jsonschema:"subscription id from list_singbox_subscriptions"`
	Enabled        bool   `json:"enabled" jsonschema:"true enables the subscription, false disables it"`
}

type setSubscriptionEnabledOut struct {
	SingboxSubscription
	Warnings []string `json:"warnings,omitempty" jsonschema:"aggregate groups whose servers may have changed because of this call — show these to the user"`
}

// subscriptionIDGrammar is hex and dashes. Ids are 24 hex characters
// today; the grammar is wider than that on purpose, and still leaves no
// room for a path separator or a control character to reach the store.
var subscriptionIDGrammar = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

func requireSubscriptionID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("subscriptionId is required (use list_singbox_subscriptions)")
	}
	if !subscriptionIDGrammar.MatchString(id) {
		return "", fmt.Errorf("subscriptionId is not a subscription id (use list_singbox_subscriptions)")
	}
	return id, nil
}

// subscriptionNotice says in words what state the subscription is in after
// the call, which is true whether or not this call changed anything.
// Structured output is not enough: a model reads any successful result
// as "the traffic stopped".
func subscriptionNotice(sub SingboxSubscription) string {
	if sub.Enabled {
		return fmt.Sprintf("The subscription is enabled: its servers can be part of the aggregate groups that list it, and it is refreshed on schedule if it has one. "+
			"No server list was fetched by this call. Its own group is %q.", sub.GroupTag)
	}
	return fmt.Sprintf("The subscription is disabled: it is not refreshed on schedule and its servers are not part of any aggregate group. "+
		"This does NOT stop traffic: its own group %q is still in the sing-box configuration, and routing rules that point at it keep using it. "+
		"To stop that traffic, retarget those rules with set_singbox_rule_outbound.", sub.GroupTag)
}

type setSubscriptionModeIn struct {
	SubscriptionID string `json:"subscriptionId" jsonschema:"subscription id from list_singbox_subscriptions"`
	Mode           string `json:"mode" jsonschema:"selector (one server chosen by the user) or urltest (sing-box picks the fastest)"`
}

type setSubscriptionActiveMemberIn struct {
	SubscriptionID string `json:"subscriptionId" jsonschema:"subscription id from list_singbox_subscriptions"`
	MemberTag      string `json:"memberTag" jsonschema:"tag of a server in the subscription's group, as get_singbox_outbound lists them for its groupTag"`
}

type setSubscriptionActiveMemberOut struct {
	SubscriptionID string `json:"subscriptionId"`
	GroupTag       string `json:"groupTag" jsonschema:"the subscription's group; get_singbox_outbound shows the server it uses now"`
	MemberTag      string `json:"memberTag" jsonschema:"the server the group was switched to"`
}

// subscriptionModeNotice says in words what the mode means for the traffic,
// and is just as true on a call that changed nothing.
func subscriptionModeNotice(sub SingboxSubscription) string {
	if sub.Mode == "urltest" {
		return fmt.Sprintf("The subscription's group %q is in urltest mode: sing-box tests its servers periodically and routes through the fastest. "+
			"A server cannot be chosen by hand in this mode; get_singbox_outbound with this groupTag shows the one in use.", sub.GroupTag)
	}
	return fmt.Sprintf("The subscription's group %q is in selector mode: it routes through one server chosen by the user, the one stored as active. "+
		"get_singbox_outbound with this groupTag shows which server that is; set_singbox_subscription_active_member changes it.", sub.GroupTag)
}

func registerSubscriptionTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_singbox_subscriptions",
		Description: "sing-box subscriptions: remote or pasted server lists, each feeding one group of servers. Shows whether each is enabled, when it was last fetched and whether that fetch failed. " +
			"A subscription is not a tunnel: it never appears in list_tunnels or list_singbox_tunnels. " +
			"To see which server a subscription is using now, pass its groupTag to get_singbox_outbound. Subscription URLs are never returned — only the host.",
		Annotations: readOnly("List sing-box subscriptions"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in subscriptionsIn) (*mcp.CallToolResult, subscriptionsOut, error) {
		if in.Offset < 0 {
			return nil, subscriptionsOut{}, fmt.Errorf("offset must not be negative")
		}
		all, err := d.ListSingboxSubscriptions(ctx)
		if err != nil {
			return nil, subscriptionsOut{}, err
		}
		return nil, pageSubscriptions(all, in.Offset), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_subscription_enabled",
		Description: "Enable or disable one sing-box subscription. Disabling stops its scheduled refresh, if it has one, and takes its servers out of aggregate groups. " +
			"It does NOT stop traffic: the subscription's own group stays in the configuration, and routing rules that point at it keep using it — retarget them with set_singbox_rule_outbound if the traffic must stop. " +
			"A change reloads sing-box, which can interrupt open connections for a moment; a call that changes nothing reloads nothing. Reversible: call it again with the other value.",
		Annotations: safeWrite("Enable/disable sing-box subscription", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setSubscriptionEnabledIn) (*mcp.CallToolResult, setSubscriptionEnabledOut, error) {
		id, err := requireSubscriptionID(in.SubscriptionID)
		if err != nil {
			return nil, setSubscriptionEnabledOut{}, err
		}
		updated, warnings, err := d.SetSingboxSubscriptionEnabled(ctx, id, in.Enabled)
		if err != nil {
			return nil, setSubscriptionEnabledOut{}, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: subscriptionNotice(updated)}}},
			setSubscriptionEnabledOut{SingboxSubscription: updated, Warnings: warnings}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_subscription_mode",
		Description: "Switch one sing-box subscription's group between selector (it routes through one server chosen by the user) and urltest (sing-box tests the servers and routes through the fastest). " +
			"Routing rules that point at the group keep pointing at it. A change rebuilds the group and reloads sing-box, which can interrupt open connections for a moment; a call that changes nothing reloads nothing. " +
			"Reversible: call it again with the other mode.",
		Annotations: safeWrite("Set sing-box subscription mode", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setSubscriptionModeIn) (*mcp.CallToolResult, SingboxSubscription, error) {
		id, err := requireSubscriptionID(in.SubscriptionID)
		if err != nil {
			return nil, SingboxSubscription{}, err
		}
		// Deps validates the mode (one check, where it becomes a
		// subscription.SubscriptionMode); the tool only normalises it.
		updated, err := d.SetSingboxSubscriptionMode(ctx, id, strings.ToLower(strings.TrimSpace(in.Mode)))
		if err != nil {
			return nil, SingboxSubscription{}, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: subscriptionModeNotice(updated)}}}, updated, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_subscription_active_member",
		Description: "Choose the server a selector-mode sing-box subscription routes through. memberTag is a server tag that get_singbox_outbound lists for the subscription's groupTag. " +
			"Refused in urltest mode, where sing-box picks the server itself: switch the mode with set_singbox_subscription_mode first. " +
			"The running group switches at once, without reloading sing-box. Reversible: choose another server.",
		Annotations: safeWrite("Choose sing-box subscription server", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setSubscriptionActiveMemberIn) (*mcp.CallToolResult, setSubscriptionActiveMemberOut, error) {
		id, err := requireSubscriptionID(in.SubscriptionID)
		if err != nil {
			return nil, setSubscriptionActiveMemberOut{}, err
		}
		tag, err := requireSingboxTag(in.MemberTag, "get_singbox_outbound with the subscription's groupTag")
		if err != nil {
			return nil, setSubscriptionActiveMemberOut{}, err
		}
		sub, err := d.SetSingboxSubscriptionActiveMember(ctx, id, tag)
		if err != nil {
			return nil, setSubscriptionActiveMemberOut{}, err
		}
		text := fmt.Sprintf("The subscription's group %q now routes through server %q. get_singbox_outbound with this groupTag shows the server in use.", sub.GroupTag, tag)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}},
			setSubscriptionActiveMemberOut{SubscriptionID: sub.ID, GroupTag: sub.GroupTag, MemberTag: tag}, nil
	})
}
