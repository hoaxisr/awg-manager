package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// sourceRuleSet mirrors the JSON produced by `sing-box rule-set decompile`.
type sourceRuleSet struct {
	Version int `json:"version"`
	Rules   []struct {
		Domain        []string `json:"domain"`
		DomainSuffix  []string `json:"domain_suffix"`
		DomainKeyword []string `json:"domain_keyword"`
		DomainRegex   []string `json:"domain_regex"`
		IPCIDR        []string `json:"ip_cidr"`
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
