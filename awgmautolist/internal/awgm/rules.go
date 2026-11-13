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

type RuleSet struct {
	Tag             string           `json:"tag"`
	Type            string           `json:"type"`
	Format          string           `json:"format,omitempty"`
	URL             string           `json:"url,omitempty"`
	UpdateInterval  string           `json:"update_interval,omitempty"`
	DownloadDetour  string           `json:"download_detour,omitempty"`
	Path            string           `json:"path,omitempty"`
	Rules           []map[string]any `json:"rules,omitempty"`
	MaterializedSRS bool             `json:"materialized_srs,omitempty"`
}

func (c *Client) Rules(ctx context.Context) ([]Rule, error) {
	var rules []Rule
	err := c.get(ctx, "/api/singbox/router/rules/list", &rules)
	return rules, err
}

func (c *Client) RuleSets(ctx context.Context) ([]RuleSet, error) {
	var sets []RuleSet
	err := c.get(ctx, "/api/singbox/router/rulesets/list", &sets)
	return sets, err
}

type RuleSetMatchResult struct {
	Tag       string `json:"tag"`
	Matched   bool   `json:"matched"`
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

func (c *Client) MatchRuleSets(ctx context.Context, input string, tags []string) ([]RuleSetMatchResult, error) {
	var results []RuleSetMatchResult
	err := c.post(ctx, "/api/singbox/router/rulesets/match", map[string]any{"input": input, "tags": tags}, &results)
	return results, err
}

// IndexedRuleSetMatch is the additive AWGM indexed-match response.
type IndexedRuleSetMatch struct {
	Generation uint64               `json:"generation"`
	Results    []RuleSetMatchResult `json:"results"`
}

// CoverageSnapshot is one applied active-slot generation. Rules and rule-sets
// are projected together, so fakeip and tproxy cannot be mixed.
type CoverageSnapshot struct {
	Generation uint64    `json:"generation"`
	Slot       string    `json:"slot"`
	Rules      []Rule    `json:"rules"`
	RuleSets   []RuleSet `json:"rule_sets"`
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
