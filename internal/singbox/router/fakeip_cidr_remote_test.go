package router

import (
	"context"
	"testing"
)

func TestRemoteTunCIDRs(t *testing.T) {
	origDL, origDC := ruleSetDownload, ruleSetDecompileExec
	t.Cleanup(func() { ruleSetDownload, ruleSetDecompileExec = origDL, origDC })

	ruleSetDownload = func(_ context.Context, url, _format string) (string, error) {
		return "/tmp/fake-" + url + ".srs", nil
	}
	ruleSetDecompileExec = func(_binary, _srsPath string) ([]byte, error) {
		return []byte(`{"version":3,"rules":[{"ip_cidr":["149.154.160.0/20","2001:b28::/32"]}]}`), nil
	}

	cfg := &RouterConfig{Route: Route{
		Rules:   []Rule{{Action: "route", Outbound: "proxy", RuleSet: []string{"r"}}},
		RuleSet: []RuleSet{{Tag: "r", Type: "remote", URL: "tg", Format: "binary"}},
	}}
	s := newTestService(t, Deps{})
	v4, v6 := s.remoteTunCIDRs(context.Background(), cfg)

	if len(v4) != 1 || v4[0] != "149.154.160.0/20" {
		t.Errorf("v4 = %v, want [149.154.160.0/20]", v4)
	}
	if len(v6) != 1 || v6[0] != "2001:b28::/32" {
		t.Errorf("v6 = %v, want [2001:b28::/32]", v6)
	}
}

// A remote rule-set referenced ONLY by a non-loop-safe proxy rule (here narrowed
// by port) must contribute NO CIDRs — routing its IPs to the tun without the rule
// guaranteeing a proxy match would risk a fakeip→tun loop.
func TestRemoteTunCIDRs_NonLoopSafeRuleYieldsNothing(t *testing.T) {
	origDL, origDC := ruleSetDownload, ruleSetDecompileExec
	t.Cleanup(func() { ruleSetDownload, ruleSetDecompileExec = origDL, origDC })

	ruleSetDownload = func(_ context.Context, url, _format string) (string, error) {
		return "/tmp/fake-" + url + ".srs", nil
	}
	ruleSetDecompileExec = func(_binary, _srsPath string) ([]byte, error) {
		return []byte(`{"version":3,"rules":[{"ip_cidr":["149.154.160.0/20"]}]}`), nil
	}

	cfg := &RouterConfig{Route: Route{
		Rules:   []Rule{{Action: "route", Outbound: "proxy", RuleSet: []string{"r"}, Port: []int{443}}},
		RuleSet: []RuleSet{{Tag: "r", Type: "remote", URL: "tg", Format: "binary"}},
	}}
	s := newTestService(t, Deps{})
	v4, v6 := s.remoteTunCIDRs(context.Background(), cfg)

	if len(v4) != 0 || len(v6) != 0 {
		t.Errorf("non-loop-safe rule contributed cidrs: v4=%v v6=%v, want none", v4, v6)
	}
}
