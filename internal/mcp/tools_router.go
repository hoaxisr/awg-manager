package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type setRuleOutboundIn struct {
	Index    int    `json:"index" jsonschema:"rule position from list_singbox_rules"`
	Outbound string `json:"outbound" jsonschema:"target outbound tag from list_singbox_outbounds or list_singbox_tunnels"`
}

type rulesOut struct {
	Rules []SingboxRule `json:"rules" jsonschema:"in evaluation order — sing-box takes the FIRST rule that matches"`
	// HasDraft repeats the staging flag here because these rules come from
	// the draft when one exists: an agent reading them as live would
	// report a change that nobody has applied.
	HasDraft bool `json:"hasDraft" jsonschema:"true means these rules include staged, NOT YET APPLIED edits"`
}

type outboundsOut struct {
	Outbounds []SingboxOutbound `json:"outbounds"`
	HasDraft  bool              `json:"hasDraft" jsonschema:"true means the router holds changes that are not applied; the groups and members listed include them, and sing-box is not running them yet"`
}

type outboundDetailIn struct {
	Tag           string `json:"tag" jsonschema:"group tag from list_singbox_outbounds, or a subscription's groupTag"`
	MembersOffset int    `json:"membersOffset,omitempty" jsonschema:"index of the first member to return; default 0"`
}

// outboundDetailOut is the group with Members replaced by one page.
// MemberCount is always the real size, so a short page is never mistaken
// for a short group.
type outboundDetailOut struct {
	SingboxOutbound
	Members          []SingboxGroupMember `json:"members"`
	MembersOffset    int                  `json:"membersOffset" jsonschema:"index of the first member returned"`
	MembersTruncated bool                 `json:"membersTruncated" jsonschema:"true when members beyond this page remain — call again with a larger membersOffset before concluding a server is absent"`
	HasDraft         bool                 `json:"hasDraft" jsonschema:"true means the router holds changes that are not applied; the groups and members listed include them, and sing-box is not running them yet"`
}

// pageOutboundDetail cuts one page of members. An offset past the end
// yields an empty page rather than an error.
func pageOutboundDetail(detail SingboxOutboundDetail, offset int) outboundDetailOut {
	page, start, truncated := pageOf(detail.Members, offset, MaxGroupMembersInOutput)
	out := outboundDetailOut{SingboxOutbound: detail.SingboxOutbound, Members: page, MembersOffset: start, MembersTruncated: truncated, HasDraft: detail.HasDraft}
	out.MemberCount = len(detail.Members)
	return out
}

type stagedOut struct {
	Staged bool `json:"staged" jsonschema:"always true: the change is in the draft and does NOT affect traffic until apply_singbox_staging"`
	Index  int  `json:"index"`
}

type applyOut struct {
	Applied bool `json:"applied"`
}

type discardOut struct {
	Discarded bool `json:"discarded"`
}

type dnsServersOut struct {
	Servers  []SingboxDNSServer `json:"servers" jsonschema:"in configuration order"`
	HasDraft bool               `json:"hasDraft" jsonschema:"true means these servers include staged, NOT YET APPLIED edits"`
}

type ruleSetsIn struct {
	Offset int `json:"offset,omitempty" jsonschema:"index of the first rule set to return; default 0"`
}

type ruleSetsOut struct {
	RuleSets  []SingboxRuleSet `json:"ruleSets" jsonschema:"in configuration order"`
	Total     int              `json:"total" jsonschema:"rule sets on the router, ignoring paging"`
	Offset    int              `json:"offset" jsonschema:"index of the first rule set returned"`
	Truncated bool             `json:"truncated" jsonschema:"true when rule sets beyond this page remain — call again with a larger offset before concluding one is absent"`
	HasDraft  bool             `json:"hasDraft" jsonschema:"true means these rule sets include staged, NOT YET APPLIED edits"`
}

type createGroupIn struct {
	Tag     string   `json:"tag" jsonschema:"tag of the new group — what routing rules will point at; must not be taken by another outbound"`
	Type    string   `json:"type" jsonschema:"selector (one member chosen by the user, the first one to begin with), urltest (sing-box tests the members and uses the fastest) or fallback (sing-box uses the first live member in list order)"`
	Members []string `json:"members" jsonschema:"member tags in order: groups from list_singbox_outbounds, proxies from list_singbox_tunnels, or direct (not in urltest)"`
}

type setGroupMembersIn struct {
	Tag     string   `json:"tag" jsonschema:"tag of a group from list_singbox_outbounds with source router"`
	Members []string `json:"members" jsonschema:"the group's COMPLETE new member list, in order — members left out are removed. get_singbox_outbound shows the current list"`
}

type setDNSServerDetourIn struct {
	Tag    string `json:"tag" jsonschema:"DNS server tag from list_singbox_dns_servers"`
	Detour string `json:"detour" jsonschema:"outbound tag to reach the server through, from list_singbox_outbounds or list_singbox_tunnels; empty or direct dials it directly"`
}

type setRuleSetDetourIn struct {
	Tags   []string `json:"tags" jsonschema:"tags of remote rule sets from list_singbox_rulesets; all are changed or none"`
	Detour string   `json:"detour" jsonschema:"outbound tag to download them through, from list_singbox_outbounds or list_singbox_tunnels; empty downloads them directly"`
}

type stagedTagOut struct {
	Staged bool   `json:"staged" jsonschema:"always true: the change is in the draft and does NOT affect traffic until apply_singbox_staging"`
	Tag    string `json:"tag"`
}

type stagedTagsOut struct {
	Staged bool     `json:"staged" jsonschema:"always true: the change is in the draft and does NOT affect traffic until apply_singbox_staging"`
	Tags   []string `json:"tags"`
}

// maxTagsPerCall bounds a tag list in one call. A router group or a batch
// of rule sets is a handful; the cap is a bound, not an expectation.
const maxTagsPerCall = 256

// requireSingboxTags checks a list of tags as requireSingboxTag checks
// one, and refuses an empty list and a repeated tag: a group listing a
// member twice, or a batch naming a set twice, is a mistake in the call
// rather than something to merge silently.
func requireSingboxTags(tags []string, field, listTool string) ([]string, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%s must name at least one tag (use %s)", field, listTool)
	}
	if len(tags) > maxTagsPerCall {
		return nil, fmt.Errorf("%s holds more than %d tags", field, maxTagsPerCall)
	}
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag, err := requireSingboxTag(raw, listTool)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		if slices.Contains(out, tag) {
			return nil, fmt.Errorf("%s names %q twice", field, tag)
		}
		out = append(out, tag)
	}
	return out, nil
}

// optionalSingboxTag is requireSingboxTag for a field where empty is a
// value of its own (a detour cleared).
func optionalSingboxTag(tag, listTool string) (string, error) {
	if strings.TrimSpace(tag) == "" {
		return "", nil
	}
	return requireSingboxTag(tag, listTool)
}

// stagedNotice is attached to every staging write so the model cannot
// report the change as live. Structured output alone is not enough: a
// model that skims tends to read "staged: true" as "done".
const stagedNotice = "The change is staged, not applied: traffic still follows the previous configuration. " +
	"Call apply_singbox_staging to publish it, or tell the user it is waiting in the web interface."

func registerRouterTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_singbox_rules",
		Description: "Routing rules of the sing-box router, in evaluation order — sing-box uses the FIRST rule that matches, so position decides the outcome. " +
			"Rules marked managed are generated by awg-manager and rewritten on the next reconcile; editing one has no lasting effect. " +
			"These are separate from the NDMS domain lists in list_dns_routes.",
		Annotations: readOnly("List sing-box rules"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, rulesOut, error) {
		rules, hasDraft, err := d.ListSingboxRules(ctx)
		rules = orEmpty(rules)
		return nil, rulesOut{Rules: rules, HasDraft: hasDraft}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_singbox_outbounds",
		Description: "Groups of servers in sing-box — selectors and urltest groups — that a rule can point at, with the member each one is routing through now. " +
			"Subscriptions appear here as groups (subscriptionId set); so do groups that gather several subscriptions (aggregateOf set). " +
			"runtimeKnown=false means sing-box gave no answer for that group: it exists, and nothing is known about what it is doing. " +
			"outOfSync=true means sing-box answered and is not running the group as listed — because of a draft not applied yet (hasDraft says whether there is one), the sing-box router being switched off, or a reload in progress. " +
			"For the members of one group, call get_singbox_outbound. A rule may also target a single proxy from list_singbox_tunnels, or the built-in direct and block.",
		Annotations: readOnly("List sing-box outbounds"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, outboundsOut, error) {
		list, hasDraft, err := d.ListSingboxOutbounds(ctx)
		list = orEmpty(list)
		return nil, outboundsOut{Outbounds: list, HasDraft: hasDraft}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_singbox_outbound",
		Description: "One sing-box group in full: its members, which one carries traffic now, and the last delay the engine recorded for each. Use it to answer which server a subscription or group is using. " +
			"Members are paged: when membersTruncated is true, call again with membersOffset. " +
			"Read lastDelayMs here before probing — singbox_delay_check makes a real request through the server, and a subscription can hold hundreds. " +
			"delayKnown=false means no test is on record, not that the server is down; runtimeKnown=false means sing-box gave no answer for this group and nothing here describes the present. " +
			"outOfSync=true means sing-box is not running the group as listed — a draft not applied yet (see hasDraft), the sing-box router switched off, or a reload in progress; activeMember then describes what is running and may not be among the members.",
		Annotations: readOnly("Get sing-box group"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in outboundDetailIn) (*mcp.CallToolResult, outboundDetailOut, error) {
		tag, err := requireSingboxTag(in.Tag, "list_singbox_outbounds")
		if err != nil {
			return nil, outboundDetailOut{}, err
		}
		if in.MembersOffset < 0 {
			return nil, outboundDetailOut{}, fmt.Errorf("membersOffset must not be negative")
		}
		detail, err := d.GetSingboxOutbound(ctx, tag)
		if err != nil {
			return nil, outboundDetailOut{}, err
		}
		return nil, pageOutboundDetail(detail, in.MembersOffset), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_singbox_staging",
		Description: "Whether the sing-box router has unapplied changes waiting, and when they were drafted. " +
			"A draft may hold edits made in the web interface as well as through MCP — they share one draft.",
		Annotations: readOnly("Sing-box staging status"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, SingboxStaging, error) {
		out, err := d.SingboxStaging(ctx)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_rule_outbound",
		Description: "Send one routing rule's traffic through a different outbound. The edit goes into the router's DRAFT and changes nothing until apply_singbox_staging runs. " +
			"Rules generated by awg-manager cannot be edited this way.",
		Annotations: safeWrite("Set rule outbound", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setRuleOutboundIn) (*mcp.CallToolResult, stagedOut, error) {
		if in.Index < 0 {
			return nil, stagedOut{}, fmt.Errorf("index must not be negative (use list_singbox_rules)")
		}
		if strings.TrimSpace(in.Outbound) == "" {
			return nil, stagedOut{}, fmt.Errorf("outbound is required (use list_singbox_outbounds)")
		}
		if err := d.SetSingboxRuleOutbound(ctx, in.Index, strings.TrimSpace(in.Outbound)); err != nil {
			return nil, stagedOut{}, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: stagedNotice}}},
			stagedOut{Staged: true, Index: in.Index}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_singbox_dns_servers",
		Description: "DNS servers of the sing-box router and the outbound each one is reached through (detour; empty means directly). " +
			"These are separate from the NDMS domain lists in list_dns_routes.",
		Annotations: readOnly("List sing-box DNS servers"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, dnsServersOut, error) {
		servers, hasDraft, err := d.ListSingboxDNSServers(ctx)
		return nil, dnsServersOut{Servers: orEmpty(servers), HasDraft: hasDraft}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_singbox_rulesets",
		Description: "Rule sets of the sing-box router — the domain and address lists its rules match against — and, for the remote ones, the outbound each is downloaded through (downloadDetour; empty means directly). " +
			"Paged: when truncated is true, call again with offset.",
		Annotations: readOnly("List sing-box rule sets"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ruleSetsIn) (*mcp.CallToolResult, ruleSetsOut, error) {
		if in.Offset < 0 {
			return nil, ruleSetsOut{}, fmt.Errorf("offset must not be negative")
		}
		all, hasDraft, err := d.ListSingboxRuleSets(ctx)
		if err != nil {
			return nil, ruleSetsOut{}, err
		}
		page, start, truncated := pageOf(all, in.Offset, MaxRuleSetsInOutput)
		return nil, ruleSetsOut{RuleSets: page, Total: len(all), Offset: start, Truncated: truncated, HasDraft: hasDraft}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_singbox_outbound_group",
		Description: "Create a group of outbounds in the sing-box router — selector, urltest or fallback — that routing rules can then point at. " +
			"It gets the probe settings the web form starts with (generate_204 test URL, 3 minute interval); the user can tune them in the web interface. " +
			"The group goes into the router's DRAFT and exists for nothing until apply_singbox_staging runs. Refused while the router runs in fakeip-tun mode.",
		Annotations: safeWrite("Create sing-box group", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createGroupIn) (*mcp.CallToolResult, stagedTagOut, error) {
		tag, err := requireSingboxTag(in.Tag, "a new name of your choice")
		if err != nil {
			return nil, stagedTagOut{}, err
		}
		members, err := requireSingboxTags(in.Members, "members", "list_singbox_outbounds or list_singbox_tunnels")
		if err != nil {
			return nil, stagedTagOut{}, err
		}
		// Deps validates the type, where it becomes a router outbound type;
		// the tool only normalises it.
		group := SingboxGroupInput{Tag: tag, Type: strings.ToLower(strings.TrimSpace(in.Type)), Members: members}
		if err := d.CreateSingboxOutboundGroup(ctx, group); err != nil {
			return nil, stagedTagOut{}, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: stagedNotice}}},
			stagedTagOut{Staged: true, Tag: tag}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_outbound_group_members",
		Description: "Replace the members of a group made in the sing-box router (source router in list_singbox_outbounds). Pass the COMPLETE new list in order: members left out are removed, and a fallback group tries them in this order. " +
			"The group's other settings stay. A subscription's group is refused: its servers come from the subscription (add_singbox_subscription_server). " +
			"The edit goes into the router's DRAFT and changes nothing until apply_singbox_staging runs. Refused while the router runs in fakeip-tun mode.",
		Annotations: safeWrite("Set sing-box group members", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setGroupMembersIn) (*mcp.CallToolResult, stagedTagOut, error) {
		tag, err := requireSingboxTag(in.Tag, "list_singbox_outbounds")
		if err != nil {
			return nil, stagedTagOut{}, err
		}
		members, err := requireSingboxTags(in.Members, "members", "list_singbox_outbounds or list_singbox_tunnels")
		if err != nil {
			return nil, stagedTagOut{}, err
		}
		if err := d.SetSingboxOutboundGroupMembers(ctx, tag, members); err != nil {
			return nil, stagedTagOut{}, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: stagedNotice}}},
			stagedTagOut{Staged: true, Tag: tag}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_dns_server_detour",
		Description: "Send one DNS server of the sing-box router through an outbound (\"Detour (outbound)\" in the web interface), or back to a direct connection with an empty detour. " +
			"As in the web form, a detour drops the server's sender address and bootstrap resolver: sing-box ignores them behind a detour. " +
			"The edit goes into the router's DRAFT and changes nothing until apply_singbox_staging runs. Refused while the router runs in fakeip-tun mode.",
		Annotations: safeWrite("Set sing-box DNS server detour", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setDNSServerDetourIn) (*mcp.CallToolResult, stagedTagOut, error) {
		tag, err := requireSingboxTag(in.Tag, "list_singbox_dns_servers")
		if err != nil {
			return nil, stagedTagOut{}, err
		}
		detour, err := optionalSingboxTag(in.Detour, "list_singbox_outbounds or list_singbox_tunnels")
		if err != nil {
			return nil, stagedTagOut{}, err
		}
		if err := d.SetSingboxDNSServerDetour(ctx, tag, detour); err != nil {
			return nil, stagedTagOut{}, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: stagedNotice}}},
			stagedTagOut{Staged: true, Tag: tag}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_ruleset_detour",
		Description: "Set the outbound remote rule sets of the sing-box router are downloaded through (\"Скачивать через\" in the web interface), or back to a direct download with an empty detour. " +
			"Every tag must name a remote rule set; if one does not, nothing is changed. " +
			"The edit goes into the router's DRAFT and changes nothing until apply_singbox_staging runs. Refused while the router runs in fakeip-tun mode.",
		Annotations: safeWrite("Set sing-box rule set detour", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setRuleSetDetourIn) (*mcp.CallToolResult, stagedTagsOut, error) {
		tags, err := requireSingboxTags(in.Tags, "tags", "list_singbox_rulesets")
		if err != nil {
			return nil, stagedTagsOut{}, err
		}
		detour, err := optionalSingboxTag(in.Detour, "list_singbox_outbounds or list_singbox_tunnels")
		if err != nil {
			return nil, stagedTagsOut{}, err
		}
		if err := d.SetSingboxRuleSetDetour(ctx, tags, detour); err != nil {
			return nil, stagedTagsOut{}, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: stagedNotice}}},
			stagedTagsOut{Staged: true, Tags: tags}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "apply_singbox_staging",
		Description: "Publish the sing-box router's draft: validate it and reload the engine. " +
			"WARNING: this applies the WHOLE draft, including any edits the user started in the web interface and has not finished. " +
			"Check get_singbox_staging first and confirm with the user when the draft is not only yours.",
		Annotations: safeWrite("Apply sing-box draft", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, applyOut, error) {
		if err := d.ApplySingboxStaging(ctx); err != nil {
			return nil, applyOut{}, err
		}
		return nil, applyOut{Applied: true}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "discard_singbox_staging",
		Description: "Throw away the sing-box router's draft and return to the applied configuration. " +
			"DESTRUCTIVE: the draft is shared, so this also destroys unfinished edits the user made in the web interface, and they cannot be recovered. " +
			"Confirm with the user before calling it.",
		Annotations: destructiveWrite("Discard sing-box draft", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, discardOut, error) {
		if err := d.DiscardSingboxStaging(ctx); err != nil {
			return nil, discardOut{}, err
		}
		return nil, discardOut{Discarded: true}, nil
	})
}
