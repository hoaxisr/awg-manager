package awgm

import "context"

// Rule mirrors the full router.Rule JSON returned by rules/list, not the
// smaller historical Swagger DTO.
type Rule struct {
	Type             string   `json:"type,omitempty"`
	Mode             string   `json:"mode,omitempty"`
	Rules            []Rule   `json:"rules,omitempty"`
	DomainSuffix     []string `json:"domain_suffix,omitempty"`
	Domain           []string `json:"domain,omitempty"`
	IPCIDR           []string `json:"ip_cidr,omitempty"`
	SourceIPCIDR     []string `json:"source_ip_cidr,omitempty"`
	SourceMACAddress []string `json:"source_mac_address,omitempty"`
	Port             []int    `json:"port,omitempty"`
	RuleSet          []string `json:"rule_set,omitempty"`
	Protocol         string   `json:"protocol,omitempty"`
	Inbound          []string `json:"inbound,omitempty"`
	AuthUser         []string `json:"auth_user,omitempty"`
	IPIsPrivate      *bool    `json:"ip_is_private,omitempty"`
	Network          string   `json:"network,omitempty"`
	Action           string   `json:"action,omitempty"`
	Outbound         string   `json:"outbound,omitempty"`
	UDPTimeout       string   `json:"udp_timeout,omitempty"`
	AWGMManaged      string   `json:"awgm_managed,omitempty"`
}

type RuleSetMatchResult struct {
	Tag       string `json:"tag"`
	Matched   bool   `json:"matched"`
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

// IndexedRuleSetMatch is the additive AWGM indexed-match response.
type IndexedRuleSetMatch struct {
	Generation uint64               `json:"generation"`
	Results    []RuleSetMatchResult `json:"results"`
}

// CoverageSnapshot is one applied active-slot generation: the routing rules
// and the selected exclusion tags, without the full rule-set payloads.
type CoverageSnapshot struct {
	Generation uint64 `json:"generation"`
	Slot       string `json:"slot"`
	Rules      []Rule `json:"rules"`
}

func (c *Client) MatchRuleSetsIndexed(ctx context.Context, input string, tags []string) (IndexedRuleSetMatch, error) {
	var out IndexedRuleSetMatch
	err := c.post(ctx, "/api/singbox/router/rulesets/indexed-match", map[string]any{"input": input, "tags": tags}, &out)
	return out, err
}

func (c *Client) CoverageIndexSnapshot(ctx context.Context, exclusionTags []string) (CoverageSnapshot, error) {
	var out CoverageSnapshot
	err := c.post(ctx, "/api/singbox/router/rulesets/indexed-snapshot", map[string]any{"tags": exclusionTags}, &out)
	return out, err
}
