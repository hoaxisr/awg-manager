package mihomo

import (
	"fmt"
	"sort"
	"strings"
)

// RuleSpec defines syntax and validation constraints for a Mihomo rule type.
type RuleSpec struct {
	Type             string   // Uppercase rule type name (e.g. "DOMAIN-SUFFIX", "IP-CIDR", "MATCH")
	MinFields        int      // Minimum number of comma-separated tokens (2 for MATCH, 3 for standard)
	MaxFields        int      // Maximum number of comma-separated tokens (2 for MATCH, 3 standard, 4 for IP with no-resolve)
	TargetIndex      int      // Token index where outbound proxy/group target is located (1 for MATCH, 2 standard)
	RequiresPayload  bool     // Whether payload token must be non-empty (true for all except MATCH)
	AllowedModifiers []string // Allowed trailing modifiers after target (e.g. ["no-resolve"])
	Category         string   // Category: "Domains", "IP & Networks", "Ports & Ingress", "Process", "Composite & Match"
	SamplePayload    string   // Deterministic sample payload for tests and validation
}

var supportedRuleSpecs = []RuleSpec{
	// Domains (6)
	{
		Type:             "DOMAIN",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Domains",
		SamplePayload:    "example.com",
	},
	{
		Type:             "DOMAIN-SUFFIX",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Domains",
		SamplePayload:    "example.com",
	},
	{
		Type:             "DOMAIN-KEYWORD",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Domains",
		SamplePayload:    "google",
	},
	{
		Type:             "DOMAIN-WILDCARD",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Domains",
		SamplePayload:    "*.example.com",
	},
	{
		Type:             "DOMAIN-REGEX",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Domains",
		SamplePayload:    `^abc\..*`,
	},
	{
		Type:             "GEOSITE",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Domains",
		SamplePayload:    "youtube",
	},

	// IP & Networks (9)
	{
		Type:             "IP-CIDR",
		MinFields:        3,
		MaxFields:        4,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: []string{"no-resolve"},
		Category:         "IP & Networks",
		SamplePayload:    "192.168.1.0/24",
	},
	{
		Type:             "IP-CIDR6",
		MinFields:        3,
		MaxFields:        4,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: []string{"no-resolve"},
		Category:         "IP & Networks",
		SamplePayload:    "2001:db8::/32",
	},
	{
		Type:             "IP-SUFFIX",
		MinFields:        3,
		MaxFields:        4,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: []string{"no-resolve"},
		Category:         "IP & Networks",
		SamplePayload:    "8.8.8.8/24",
	},
	{
		Type:             "IP-ASN",
		MinFields:        3,
		MaxFields:        4,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: []string{"no-resolve"},
		Category:         "IP & Networks",
		SamplePayload:    "13335",
	},
	{
		Type:             "GEOIP",
		MinFields:        3,
		MaxFields:        4,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: []string{"no-resolve"},
		Category:         "IP & Networks",
		SamplePayload:    "telegram",
	},
	{
		Type:             "SRC-GEOIP",
		MinFields:        3,
		MaxFields:        4,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: []string{"no-resolve"},
		Category:         "IP & Networks",
		SamplePayload:    "cn",
	},
	{
		Type:             "SRC-IP-ASN",
		MinFields:        3,
		MaxFields:        4,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: []string{"no-resolve"},
		Category:         "IP & Networks",
		SamplePayload:    "13335",
	},
	{
		Type:             "SRC-IP-CIDR",
		MinFields:        3,
		MaxFields:        4,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: []string{"no-resolve"},
		Category:         "IP & Networks",
		SamplePayload:    "192.168.1.201/32",
	},
	{
		Type:             "SRC-IP-SUFFIX",
		MinFields:        3,
		MaxFields:        4,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: []string{"no-resolve"},
		Category:         "IP & Networks",
		SamplePayload:    "192.168.1.201/24",
	},

	// Ports & Ingress (9)
	{
		Type:             "DST-PORT",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Ports & Ingress",
		SamplePayload:    "80",
	},
	{
		Type:             "SRC-PORT",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Ports & Ingress",
		SamplePayload:    "7777",
	},
	{
		Type:             "IN-PORT",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Ports & Ingress",
		SamplePayload:    "7890",
	},
	{
		Type:             "IN-TYPE",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Ports & Ingress",
		SamplePayload:    "SOCKS/HTTP",
	},
	{
		Type:             "IN-USER",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Ports & Ingress",
		SamplePayload:    "admin",
	},
	{
		Type:             "IN-NAME",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Ports & Ingress",
		SamplePayload:    "mixed-in",
	},
	{
		Type:             "REMATCH-NAME",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Ports & Ingress",
		SamplePayload:    "mixed-in",
	},
	{
		Type:             "NETWORK",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Ports & Ingress",
		SamplePayload:    "tcp",
	},
	{
		Type:             "DSCP",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Ports & Ingress",
		SamplePayload:    "4",
	},

	// Process (7)
	{
		Type:             "PROCESS-PATH",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Process",
		SamplePayload:    "/usr/bin/curl",
	},
	{
		Type:             "PROCESS-PATH-WILDCARD",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Process",
		SamplePayload:    "*/curl",
	},
	{
		Type:             "PROCESS-PATH-REGEX",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Process",
		SamplePayload:    ".*curl.*",
	},
	{
		Type:             "PROCESS-NAME",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Process",
		SamplePayload:    "curl",
	},
	{
		Type:             "PROCESS-NAME-WILDCARD",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Process",
		SamplePayload:    "*curl*",
	},
	{
		Type:             "PROCESS-NAME-REGEX",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Process",
		SamplePayload:    ".*curl.*",
	},
	{
		Type:             "UID",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Process",
		SamplePayload:    "1001",
	},

	// Composite & Match (5)
	{
		Type:             "RULE-SET",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Composite & Match",
		SamplePayload:    "provider-name",
	},
	{
		Type:             "AND",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Composite & Match",
		SamplePayload:    "((DOMAIN,example.com),(NETWORK,tcp))",
	},
	{
		Type:             "OR",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Composite & Match",
		SamplePayload:    "((DOMAIN,example.com),(NETWORK,tcp))",
	},
	{
		Type:             "NOT",
		MinFields:        3,
		MaxFields:        3,
		TargetIndex:      2,
		RequiresPayload:  true,
		AllowedModifiers: nil,
		Category:         "Composite & Match",
		SamplePayload:    "((DOMAIN,example.com))",
	},
	{
		Type:             "MATCH",
		MinFields:        2,
		MaxFields:        2,
		TargetIndex:      1,
		RequiresPayload:  false,
		AllowedModifiers: nil,
		Category:         "Composite & Match",
		SamplePayload:    "",
	},
}

var ruleSpecMap = func() map[string]RuleSpec {
	m := make(map[string]RuleSpec, len(supportedRuleSpecs))
	for _, spec := range supportedRuleSpecs {
		m[spec.Type] = spec
	}
	return m
}()

// LookupRuleSpec looks up a RuleSpec by uppercase rule type name.
func LookupRuleSpec(ruleType string) (RuleSpec, bool) {
	spec, ok := ruleSpecMap[strings.ToUpper(strings.TrimSpace(ruleType))]
	return spec, ok
}

// IsSupportedRuleType reports whether the specified rule type is supported in AWG Manager.
func IsSupportedRuleType(ruleType string) bool {
	_, ok := LookupRuleSpec(ruleType)
	return ok
}

// AllRuleSpecs returns a copy of all supported RuleSpecs in deterministic order.
func AllRuleSpecs() []RuleSpec {
	res := make([]RuleSpec, len(supportedRuleSpecs))
	copy(res, supportedRuleSpecs)
	return res
}

// CategoryGroup groups rule types for the UI.
type CategoryGroup struct {
	Label string   `json:"label"`
	Items []string `json:"items"`
}

// CategorizedRuleTypes returns rule types organized by UI category groups.
func CategorizedRuleTypes() []CategoryGroup {
	categories := []struct {
		GoCategory string
		UILabel    string
	}{
		{"Domains", "Домены"},
		{"IP & Networks", "IP и сети"},
		{"Ports & Ingress", "Порты и входы"},
		{"Process", "Процесс"},
		{"Composite & Match", "Составные"},
	}

	res := make([]CategoryGroup, 0, len(categories))
	for _, c := range categories {
		var items []string
		for _, spec := range supportedRuleSpecs {
			if spec.Category == c.GoCategory {
				items = append(items, spec.Type)
			}
		}
		res = append(res, CategoryGroup{
			Label: c.UILabel,
			Items: items,
		})
	}
	return res
}

// RenderRuleTypesTypeScript generates the deterministic content of mihomoRuleTypes.generated.ts.
func RenderRuleTypesTypeScript() []byte {
	var b strings.Builder
	b.WriteString("// Code generated by internal/mihomo/cmd/genrules. DO NOT EDIT.\n\n")
	b.WriteString("export interface MihomoRuleTypeGroup {\n")
	b.WriteString("\tlabel: string;\n")
	b.WriteString("\titems: string[];\n")
	b.WriteString("}\n\n")

	b.WriteString("export const MIHOMO_SUPPORTED_RULE_TYPES: MihomoRuleTypeGroup[] = [\n")
	for _, cat := range CategorizedRuleTypes() {
		b.WriteString(fmt.Sprintf("\t{\n\t\tlabel: %q,\n\t\titems: [\n", cat.Label))
		for _, item := range cat.Items {
			b.WriteString(fmt.Sprintf("\t\t\t%q,\n", item))
		}
		b.WriteString("\t\t]\n\t},\n")
	}
	b.WriteString("];\n\n")

	allTypes := make([]string, len(supportedRuleSpecs))
	for i, spec := range supportedRuleSpecs {
		allTypes[i] = spec.Type
	}
	sort.Strings(allTypes)
	b.WriteString("export const ALL_MIHOMO_RULE_TYPES: readonly string[] = [\n")
	for _, t := range allTypes {
		b.WriteString(fmt.Sprintf("\t%q,\n", t))
	}
	b.WriteString("] as const;\n\n")

	b.WriteString("export type MihomoSupportedRuleType = (typeof ALL_MIHOMO_RULE_TYPES)[number];\n")
	return []byte(b.String())
}
