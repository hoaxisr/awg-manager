package mcp

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ruleIn is a routing rule as an agent writes it. ip_is_private is not
// offered: the router counts any rule carrying it as a system rule, so a
// rule made with it could not be edited again.
type ruleIn struct {
	DomainSuffix []string `json:"domainSuffix,omitempty" jsonschema:"domain suffixes (example.com matches it and its subdomains)"`
	Domain       []string `json:"domain,omitempty" jsonschema:"exact domains"`
	IPCIDR       []string `json:"ipCidr,omitempty" jsonschema:"destination addresses or subnets"`
	SourceIPCIDR []string `json:"sourceIpCidr,omitempty" jsonschema:"source addresses or subnets — LAN devices"`
	SourceMAC    []string `json:"sourceMac,omitempty" jsonschema:"source MAC addresses — LAN devices"`
	Port         []int    `json:"port,omitempty" jsonschema:"destination ports"`
	RuleSet      []string `json:"ruleSet,omitempty" jsonschema:"rule set tags from list_singbox_rulesets"`
	Network      string   `json:"network,omitempty" jsonschema:"tcp or udp; empty matches both"`
	Protocol     string   `json:"protocol,omitempty" jsonschema:"sniffed protocol (tls, http, quic, bittorrent, …); needs sniffing on"`
	Action       string   `json:"action" jsonschema:"route (send matching traffic through outbound) or reject (block it)"`
	Outbound     string   `json:"outbound,omitempty" jsonschema:"for route: outbound tag from list_singbox_outbounds or list_singbox_tunnels, or direct"`
}

type addRuleIn struct {
	ruleIn
	Position *int `json:"position,omitempty" jsonschema:"index in list_singbox_rules the new rule takes — the rule there and below move down; omit to append. sing-box uses the FIRST rule that matches, so a rule placed after a broader one never fires. Nothing goes above a system rule (list_singbox_rules marks them system)"`
}

type updateRuleIn struct {
	ruleIn
	Index int `json:"index" jsonschema:"rule position from list_singbox_rules; the rule is replaced WHOLE — matchers left out are removed"`
}

type ruleIndexIn struct {
	Index int `json:"index" jsonschema:"rule position from list_singbox_rules"`
}

type moveRuleIn struct {
	From int `json:"from" jsonschema:"position of the rule to move, from list_singbox_rules"`
	To   int `json:"to" jsonschema:"position it takes; the rules between shift by one"`
}

type routeFinalIn struct {
	Outbound string `json:"outbound" jsonschema:"outbound tag for traffic no rule matched, from list_singbox_outbounds or list_singbox_tunnels, or direct"`
}

type addRuleSetIn struct {
	Tag            string   `json:"tag" jsonschema:"tag rules will refer to; for inline, Latin letters, digits, dot, underscore and dash"`
	Type           string   `json:"type" jsonschema:"remote (sing-box downloads url) or inline (the domains and addresses given here)"`
	URL            string   `json:"url,omitempty" jsonschema:"remote: http or https URL of the .srs or .json rule set"`
	Format         string   `json:"format,omitempty" jsonschema:"remote: binary (.srs, default) or source (.json)"`
	UpdateInterval string   `json:"updateInterval,omitempty" jsonschema:"remote: how often it is downloaded again, as sing-box writes it (24h default)"`
	DomainSuffix   []string `json:"domainSuffix,omitempty" jsonschema:"inline: domain suffixes"`
	Domain         []string `json:"domain,omitempty" jsonschema:"inline: exact domains"`
	IPCIDR         []string `json:"ipCidr,omitempty" jsonschema:"inline: addresses or subnets"`
}

type deleteRuleSetIn struct {
	Tag   string `json:"tag" jsonschema:"rule set tag from list_singbox_rulesets"`
	Force bool   `json:"force,omitempty" jsonschema:"also delete it while rules use it: the references are removed, and a rule left with no condition at all is deleted too — otherwise it would match ALL traffic"`
}

type deletedRuleOut struct {
	Staged  bool        `json:"staged" jsonschema:"always true: the deletion is in the draft and does NOT affect traffic until apply_singbox_staging"`
	Deleted SingboxRule `json:"deleted" jsonschema:"the rule as it was — after apply_singbox_staging nothing in MCP restores it"`
}

type deletedRuleSetOut struct {
	Staged  bool           `json:"staged" jsonschema:"always true: the deletion is in the draft and does NOT affect traffic until apply_singbox_staging"`
	Deleted SingboxRuleSet `json:"deleted" jsonschema:"the rule set as it was"`
	// Affected and AffectedDNS are the rules force took the set out of.
	Affected    []SingboxRule    `json:"affectedRules,omitempty" jsonschema:"with force: the routing rules that used the set, as they were. Each lost this condition; one left with no condition was deleted, and the rules below it moved up"`
	AffectedDNS []SingboxDNSRule `json:"affectedDnsRules,omitempty" jsonschema:"with force: the DNS rules that used the set, as they were — changed the same way"`
}

type stagedRuleOut struct {
	Staged bool `json:"staged" jsonschema:"always true: the change is in the draft and does NOT affect traffic until apply_singbox_staging"`
}

// maxRuleValues bounds the values of one rule. A rule with more belongs in
// a rule set.
const maxRuleValues = 1024

// ruleInput checks a rule's shape before Deps. The router validates the
// matchers' syntax and the outbound; here only what keeps the call sane:
// an action it can take, no control characters, a bounded size.
func ruleInput(in ruleIn) (SingboxRuleInput, error) {
	action := strings.ToLower(strings.TrimSpace(in.Action))
	outbound := strings.TrimSpace(in.Outbound)
	switch action {
	case "route":
		if outbound == "" {
			return SingboxRuleInput{}, fmt.Errorf("outbound is required for route (use list_singbox_outbounds)")
		}
	case "reject":
		if outbound != "" {
			return SingboxRuleInput{}, fmt.Errorf("reject takes no outbound")
		}
	default:
		return SingboxRuleInput{}, fmt.Errorf("action must be route or reject")
	}
	out := SingboxRuleInput{Action: action, Outbound: outbound, Port: in.Port,
		Network: strings.ToLower(strings.TrimSpace(in.Network)), Protocol: strings.ToLower(strings.TrimSpace(in.Protocol))}
	n := len(in.Port)
	for _, f := range []struct {
		name string
		in   []string
		out  *[]string
	}{
		{"domainSuffix", in.DomainSuffix, &out.DomainSuffix}, {"domain", in.Domain, &out.Domain},
		{"ipCidr", in.IPCIDR, &out.IPCIDR}, {"sourceIpCidr", in.SourceIPCIDR, &out.SourceIPCIDR},
		{"sourceMac", in.SourceMAC, &out.SourceMAC}, {"ruleSet", in.RuleSet, &out.RuleSet},
	} {
		values, err := cleanValues(f.name, f.in)
		if err != nil {
			return SingboxRuleInput{}, err
		}
		*f.out = values
		n += len(values)
	}
	if n == 0 && out.Network == "" && out.Protocol == "" {
		return SingboxRuleInput{}, fmt.Errorf("a rule needs at least one condition: a rule with none matches ALL traffic")
	}
	if n > maxRuleValues {
		return SingboxRuleInput{}, fmt.Errorf("a rule holds more than %d values; put them in a rule set (add_singbox_ruleset) instead", maxRuleValues)
	}
	if out.Network != "" && out.Network != "tcp" && out.Network != "udp" {
		return SingboxRuleInput{}, fmt.Errorf("network must be tcp or udp")
	}
	if out.Protocol != "" && !isWord(out.Protocol) {
		return SingboxRuleInput{}, fmt.Errorf("protocol must be one word such as tls or quic")
	}
	return out, nil
}

// cleanValues trims a list, drops empty entries and refuses control
// characters, which no domain, address or tag carries.
func cleanValues(name string, in []string) ([]string, error) {
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("%s: values must not contain control characters", name)
		}
		out = append(out, v)
	}
	return out, nil
}

func isWord(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return false
		}
	}
	return s != ""
}

func stagedResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: stagedNotice}}}
}

func registerRuleTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "add_singbox_rule",
		Description: "Add a routing rule to the sing-box router: traffic matching its conditions is routed through an outbound or rejected. " +
			"Values within one condition are alternatives; the conditions combine as list_singbox_rules spells them out. Position matters — sing-box uses the FIRST rule that matches. " +
			"The rule goes into the router's DRAFT and changes nothing until apply_singbox_staging runs. Refused while the router runs in fakeip-tun mode.",
		Annotations: safeWrite("Add sing-box rule", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addRuleIn) (*mcp.CallToolResult, stagedRuleOut, error) {
		rule, err := ruleInput(in.ruleIn)
		if err != nil {
			return nil, stagedRuleOut{}, err
		}
		position := -1
		if in.Position != nil {
			if *in.Position < 0 {
				return nil, stagedRuleOut{}, fmt.Errorf("position must not be negative (omit it to append)")
			}
			position = *in.Position
		}
		if err := d.AddSingboxRule(ctx, position, rule); err != nil {
			return nil, stagedRuleOut{}, err
		}
		return stagedResult(), stagedRuleOut{Staged: true}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_singbox_rule",
		Description: "Replace one routing rule of the sing-box router WHOLE: conditions, action and outbound — anything left out is removed. To change only the outbound, use set_singbox_rule_outbound. " +
			"System rules and rules generated by awg-manager cannot be edited. The edit goes into the router's DRAFT and changes nothing until apply_singbox_staging runs. Refused while the router runs in fakeip-tun mode.",
		Annotations: safeWrite("Update sing-box rule", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateRuleIn) (*mcp.CallToolResult, stagedRuleOut, error) {
		if in.Index < 0 {
			return nil, stagedRuleOut{}, fmt.Errorf("index must not be negative (use list_singbox_rules)")
		}
		rule, err := ruleInput(in.ruleIn)
		if err != nil {
			return nil, stagedRuleOut{}, err
		}
		if err := d.UpdateSingboxRule(ctx, in.Index, rule); err != nil {
			return nil, stagedRuleOut{}, err
		}
		return stagedResult(), stagedRuleOut{Staged: true}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "move_singbox_rule",
		Description: "Move one routing rule of the sing-box router to another position; the rules between shift by one. Position decides the outcome: sing-box uses the FIRST rule that matches. " +
			"System rules and rules generated by awg-manager are not moved, and nothing is moved onto a system rule's place. The edit goes into the router's DRAFT. Refused while the router runs in fakeip-tun mode.",
		Annotations: safeWrite("Move sing-box rule", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in moveRuleIn) (*mcp.CallToolResult, stagedRuleOut, error) {
		if in.From < 0 || in.To < 0 {
			return nil, stagedRuleOut{}, fmt.Errorf("positions must not be negative (use list_singbox_rules)")
		}
		if err := d.MoveSingboxRule(ctx, in.From, in.To); err != nil {
			return nil, stagedRuleOut{}, err
		}
		return stagedResult(), stagedRuleOut{Staged: true}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_singbox_rule",
		Description: "Delete one routing rule of the sing-box router. System rules and rules generated by awg-manager cannot be deleted. " +
			"The deletion goes into the router's DRAFT and can be discarded until apply_singbox_staging runs; after that nothing in MCP restores the rule, so the result carries it as it was. Refused while the router runs in fakeip-tun mode.",
		Annotations: destructiveWrite("Delete sing-box rule", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ruleIndexIn) (*mcp.CallToolResult, deletedRuleOut, error) {
		if in.Index < 0 {
			return nil, deletedRuleOut{}, fmt.Errorf("index must not be negative (use list_singbox_rules)")
		}
		deleted, err := d.DeleteSingboxRule(ctx, in.Index)
		if err != nil {
			return nil, deletedRuleOut{}, err
		}
		return stagedResult(), deletedRuleOut{Staged: true, Deleted: deleted}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_singbox_route_final",
		Description: "Set the outbound for traffic no routing rule of the sing-box router matched (route.final; get_singbox_router_status shows the current one). " +
			"The edit goes into the router's DRAFT and changes nothing until apply_singbox_staging runs. Refused while the router runs in fakeip-tun mode.",
		Annotations: safeWrite("Set sing-box final outbound", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in routeFinalIn) (*mcp.CallToolResult, stagedTagOut, error) {
		tag, err := requireSingboxTag(in.Outbound, "list_singbox_outbounds or list_singbox_tunnels")
		if err != nil {
			return nil, stagedTagOut{}, err
		}
		if err := d.SetSingboxRouteFinal(ctx, tag); err != nil {
			return nil, stagedTagOut{}, err
		}
		return stagedResult(), stagedTagOut{Staged: true, Tag: tag}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_singbox_ruleset",
		Description: "Add a rule set to the sing-box router — a remote list sing-box downloads, or an inline one made of the domains and addresses given here — for rules to refer to (ruleSet in add_singbox_rule). " +
			"A remote set is downloaded directly; set_singbox_ruleset_detour sends the download through an outbound. " +
			"The set goes into the router's DRAFT and is used for nothing until apply_singbox_staging runs. Refused while the router runs in fakeip-tun mode.",
		Annotations: safeWrite("Add sing-box rule set", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addRuleSetIn) (*mcp.CallToolResult, stagedTagOut, error) {
		tag, err := requireSingboxTag(in.Tag, "a new name of your choice")
		if err != nil {
			return nil, stagedTagOut{}, err
		}
		set := SingboxRuleSetInput{Tag: tag, Type: strings.ToLower(strings.TrimSpace(in.Type))}
		switch set.Type {
		case "remote":
			set.URL = strings.TrimSpace(in.URL)
			set.Format = strings.ToLower(strings.TrimSpace(in.Format))
			set.UpdateInterval = strings.TrimSpace(in.UpdateInterval)
			if set.URL == "" {
				return nil, stagedTagOut{}, fmt.Errorf("url is required for a remote rule set")
			}
			if set.Format != "" && set.Format != "binary" && set.Format != "source" {
				return nil, stagedTagOut{}, fmt.Errorf("format must be binary or source")
			}
			if len(in.DomainSuffix)+len(in.Domain)+len(in.IPCIDR) > 0 {
				return nil, stagedTagOut{}, fmt.Errorf("a remote rule set takes no domains or addresses: its content comes from url")
			}
		case "inline":
			if in.URL != "" {
				return nil, stagedTagOut{}, fmt.Errorf("an inline rule set takes no url")
			}
			for _, f := range []struct {
				name string
				in   []string
				out  *[]string
			}{{"domainSuffix", in.DomainSuffix, &set.DomainSuffix}, {"domain", in.Domain, &set.Domain}, {"ipCidr", in.IPCIDR, &set.IPCIDR}} {
				values, err := cleanValues(f.name, f.in)
				if err != nil {
					return nil, stagedTagOut{}, err
				}
				*f.out = values
			}
			if len(set.DomainSuffix)+len(set.Domain)+len(set.IPCIDR) == 0 {
				return nil, stagedTagOut{}, fmt.Errorf("an inline rule set needs at least one domain or address")
			}
		default:
			return nil, stagedTagOut{}, fmt.Errorf("type must be remote or inline")
		}
		if strings.IndexFunc(set.URL+set.UpdateInterval, unicode.IsControl) >= 0 {
			return nil, stagedTagOut{}, fmt.Errorf("url and updateInterval must not contain control characters")
		}
		if err := d.AddSingboxRuleSet(ctx, set); err != nil {
			return nil, stagedTagOut{}, err
		}
		return stagedResult(), stagedTagOut{Staged: true, Tag: tag}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_singbox_ruleset",
		Description: "Delete a rule set of the sing-box router. Refused while routing or DNS rules use it, unless force: then the references are removed and a rule left with no condition is deleted too. " +
			"The deletion goes into the router's DRAFT and can be discarded until apply_singbox_staging runs; after that nothing in MCP restores it. Refused while the router runs in fakeip-tun mode.",
		Annotations: destructiveWrite("Delete sing-box rule set", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteRuleSetIn) (*mcp.CallToolResult, deletedRuleSetOut, error) {
		tag, err := requireSingboxTag(in.Tag, "list_singbox_rulesets")
		if err != nil {
			return nil, deletedRuleSetOut{}, err
		}
		del, err := d.DeleteSingboxRuleSet(ctx, tag, in.Force)
		if err != nil {
			return nil, deletedRuleSetOut{}, err
		}
		return stagedResult(), deletedRuleSetOut{Staged: true, Deleted: del.Deleted, Affected: del.Rules, AffectedDNS: del.DNSRules}, nil
	})
}
