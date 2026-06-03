package main

import "testing"

const sampleSource = `{
  "version": 3,
  "rules": [
    {
      "domain": ["youtube.com", "youtu.be"],
      "domain_suffix": [".ytimg.com", "googlevideo.com"],
      "domain_keyword": ["ggpht"],
      "domain_regex": ["^.*\\.example\\.com$"],
      "ip_cidr": ["1.2.3.0/24", "2001:db8::/32"]
    }
  ]
}`

func TestExtractDomainsAndSubnets(t *testing.T) {
	dom, sub, skipped, err := extractRuleSet([]byte(sampleSource))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	wantDom := []string{"youtube.com", "youtu.be", "ytimg.com", "googlevideo.com"} // leading dot stripped
	if !equalStrings(dom, wantDom) {
		t.Fatalf("domains=%v want %v", dom, wantDom)
	}
	wantSub := []string{"1.2.3.0/24", "2001:db8::/32"}
	if !equalStrings(sub, wantSub) {
		t.Fatalf("subnets=%v want %v", sub, wantSub)
	}
	if skipped["domain_keyword"] != 1 || skipped["domain_regex"] != 1 {
		t.Fatalf("skipped=%v want keyword=1 regex=1", skipped)
	}
}

// Real geosite-facebook decompiles with scalar (non-array) fields.
const sampleScalarSource = `{"version":2,"rules":[{"domain":"facebook.com","ip_cidr":"31.13.0.0/16"}]}`

func TestExtractHandlesScalarFields(t *testing.T) {
	dom, sub, _, err := extractRuleSet([]byte(sampleScalarSource))
	if err != nil {
		t.Fatalf("extract scalar: %v", err)
	}
	if !equalStrings(dom, []string{"facebook.com"}) {
		t.Fatalf("domains=%v want [facebook.com]", dom)
	}
	if !equalStrings(sub, []string{"31.13.0.0/16"}) {
		t.Fatalf("subnets=%v want [31.13.0.0/16]", sub)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
