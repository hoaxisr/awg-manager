package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// listable matches sing-box's source-format fields, which serialize a single
// value as a bare string and multiple values as an array (e.g. geosite-facebook
// decompiles to `"domain": "facebook.com"`, geosite-youtube to `"domain": [...]`).
type listable []string

func (l *listable) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*l = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*l = []string{s}
	return nil
}

// sourceRuleSet mirrors the JSON produced by `sing-box rule-set decompile`.
type sourceRuleSet struct {
	Version int `json:"version"`
	Rules   []struct {
		Domain        listable `json:"domain"`
		DomainSuffix  listable `json:"domain_suffix"`
		DomainKeyword listable `json:"domain_keyword"`
		DomainRegex   listable `json:"domain_regex"`
		IPCIDR        listable `json:"ip_cidr"`
	} `json:"rules"`
}

// extractRuleSet pulls DNS-engine-compatible data from a decompiled rule-set:
// domain + domain_suffix (leading dot stripped) → domains; ip_cidr → subnets.
// Unsupported rule kinds (domain_keyword/domain_regex) are counted in `skipped`
// so a partial extraction is visible, never silent.
func extractRuleSet(decompiled []byte) (domains, subnets []string, skipped map[string]int, err error) {
	var rs sourceRuleSet
	if err = json.Unmarshal(decompiled, &rs); err != nil {
		return nil, nil, nil, fmt.Errorf("parse decompiled rule-set: %w", err)
	}
	skipped = map[string]int{}
	seenD, seenS := map[string]bool{}, map[string]bool{}
	for _, r := range rs.Rules {
		for _, d := range r.Domain {
			addUnique(&domains, seenD, d)
		}
		for _, d := range r.DomainSuffix {
			addUnique(&domains, seenD, strings.TrimPrefix(d, "."))
		}
		for _, c := range r.IPCIDR {
			addUnique(&subnets, seenS, c)
		}
		skipped["domain_keyword"] += len(r.DomainKeyword)
		skipped["domain_regex"] += len(r.DomainRegex)
	}
	return domains, subnets, skipped, nil
}

func addUnique(dst *[]string, seen map[string]bool, v string) {
	if v == "" || seen[v] {
		return
	}
	seen[v] = true
	*dst = append(*dst, v)
}
