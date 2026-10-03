package router

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestBuildKeeneticCloudRules(t *testing.T) {
	rules := BuildKeeneticCloudRules("awg10")
	if len(rules) != 4 {
		t.Fatalf("expected 4 rules, got %d", len(rules))
	}

	// 1. Direct local domain exceptions
	if rules[0].Outbound != "direct" {
		t.Errorf("rule[0] outbound = %s, want direct", rules[0].Outbound)
	}
	if !slices.Contains(rules[0].Domain, "my.keenetic.net") || !slices.Contains(rules[0].Domain, "my.netcraze.net") {
		t.Errorf("rule[0] missing my.keenetic.net/my.netcraze.net: %+v", rules[0].Domain)
	}

	// 2. Cloud domains
	if rules[1].Outbound != "awg10" {
		t.Errorf("rule[1] outbound = %s, want awg10", rules[1].Outbound)
	}
	if !slices.Contains(rules[1].DomainSuffix, "keenetic.cloud") {
		t.Errorf("rule[1] missing keenetic.cloud: %+v", rules[1].DomainSuffix)
	}
	if !slices.Contains(rules[1].DomainSuffix, "netcraze.cloud") {
		t.Errorf("rule[1] missing netcraze.cloud: %+v", rules[1].DomainSuffix)
	}

	// 3. CIDRs
	if rules[2].Outbound != "awg10" {
		t.Errorf("rule[2] outbound = %s, want awg10", rules[2].Outbound)
	}
	if !slices.Contains(rules[2].IPCIDR, "95.213.212.0/24") {
		t.Errorf("rule[2] missing 95.213.212.0/24: %+v", rules[2].IPCIDR)
	}

	// 4. Ports
	if rules[3].Outbound != "awg10" {
		t.Errorf("rule[3] outbound = %s, want awg10", rules[3].Outbound)
	}
	if !slices.Contains(rules[3].Port, 4044) {
		t.Errorf("rule[3] missing port 4044: %+v", rules[3].Port)
	}
	if !slices.Contains(rules[3].Port, 5683) {
		t.Errorf("rule[3] missing port 5683: %+v", rules[3].Port)
	}
}

func TestInsertCloudRules_HijackDNSOrdering(t *testing.T) {
	trueVal := true
	existing := []Rule{
		{Action: "hijack-dns", Protocol: "dns"},
		{IPIsPrivate: &trueVal, Outbound: "direct"},
		{DomainSuffix: []string{"youtube.com"}, Outbound: "vpn"},
	}

	cloudRules := BuildKeeneticCloudRules("awg10")
	result := insertCloudRules(existing, cloudRules)

	if len(result) != len(existing)+len(cloudRules) {
		t.Fatalf("unexpected length: got %d, want %d", len(result), len(existing)+len(cloudRules))
	}

	if result[0].Action != "hijack-dns" {
		t.Errorf("result[0] must be hijack-dns, got %+v", result[0])
	}
	if result[1].IPIsPrivate == nil || !*result[1].IPIsPrivate {
		t.Errorf("result[1] must be ip_is_private, got %+v", result[1])
	}
	if result[2].Domain == nil || !slices.Contains(result[2].Domain, "my.keenetic.net") {
		t.Errorf("result[2] must be local cloud domain rule, got %+v", result[2])
	}
}

func TestKeeneticCloudIPTables(t *testing.T) {
	spec := RestoreInputSpec{
		KeeneticCloudTunnel: true,
		KeeneticCloudSet:    true,
	}

	mangle := buildMangleRestoreInput(spec)
	if !strings.Contains(mangle, "AWGM-OUTPUT-UDP") {
		t.Errorf("mangle output missing AWGM-OUTPUT-UDP:\n%s", mangle)
	}
	if !strings.Contains(mangle, "--dports 9,3478,3479,4044,5683,5684") {
		t.Errorf("mangle output missing dports 4044/5683/5684:\n%s", mangle)
	}

	if !strings.Contains(mangle, "--match-set AWGM-CLOUD dst") {
		t.Errorf("mangle output missing --match-set AWGM-CLOUD dst:\n%s", mangle)
	}

	nat := buildNatRestoreInput(spec)
	if !strings.Contains(nat, "AWGM-OUTPUT") {
		t.Errorf("nat output missing AWGM-OUTPUT:\n%s", nat)
	}
	if !strings.Contains(nat, "--match-set AWGM-CLOUD dst") {
		t.Errorf("nat output missing --match-set AWGM-CLOUD dst:\n%s", nat)
	}
	if !strings.Contains(nat, "95.213.212.0/24") {
		t.Errorf("nat output missing 95.213.212.0/24:\n%s", nat)
	}
}

func TestKeeneticCloudIPTables_NoCloudSet(t *testing.T) {
	spec := RestoreInputSpec{
		KeeneticCloudTunnel: true,
		KeeneticCloudSet:    false,
	}

	mangle := buildMangleRestoreInput(spec)
	if !strings.Contains(mangle, "AWGM-OUTPUT-UDP") {
		t.Errorf("mangle output missing AWGM-OUTPUT-UDP:\n%s", mangle)
	}
	if strings.Contains(mangle, "--match-set AWGM-CLOUD dst") {
		t.Errorf("mangle output must NOT contain --match-set AWGM-CLOUD dst when KeeneticCloudSet is false:\n%s", mangle)
	}

	nat := buildNatRestoreInput(spec)
	if !strings.Contains(nat, "AWGM-OUTPUT") {
		t.Errorf("nat output missing AWGM-OUTPUT:\n%s", nat)
	}
	if strings.Contains(nat, "--match-set AWGM-CLOUD dst") {
		t.Errorf("nat output must NOT contain --match-set AWGM-CLOUD dst when KeeneticCloudSet is false:\n%s", nat)
	}
	if !strings.Contains(nat, "95.213.212.0/24") {
		t.Errorf("nat output missing 95.213.212.0/24:\n%s", nat)
	}
}

func TestBuildKeeneticCloudRules_WithExtraCIDRs(t *testing.T) {
	extra := []string{"1.2.3.4/32", "5.6.7.8/32", "95.213.212.0/24"} // includes duplicate
	rules := BuildKeeneticCloudRules("awg10", extra...)

	if len(rules) != 4 {
		t.Fatalf("expected 4 rules, got %d", len(rules))
	}
	cidrs := rules[2].IPCIDR
	if !slices.Contains(cidrs, "1.2.3.4/32") {
		t.Errorf("missing dynamic CIDR 1.2.3.4/32: %+v", cidrs)
	}
	if !slices.Contains(cidrs, "5.6.7.8/32") {
		t.Errorf("missing dynamic CIDR 5.6.7.8/32: %+v", cidrs)
	}
	// Verify deduplication
	count := 0
	for _, c := range cidrs {
		if c == "95.213.212.0/24" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 95.213.212.0/24 to appear once, got %d", count)
	}
}

func TestIsCoveredByCIDRs(t *testing.T) {
	subnets := []string{"87.228.71.0/24", "185.162.93.0/24"}

	// Covered
	if !isCoveredByCIDRs("87.228.71.67", subnets) {
		t.Errorf("87.228.71.67 should be covered by 87.228.71.0/24")
	}
	// Uncovered (new IP)
	if isCoveredByCIDRs("1.2.3.4", subnets) {
		t.Errorf("1.2.3.4 should NOT be covered")
	}
	// Invalid
	if isCoveredByCIDRs("not-an-ip", subnets) {
		t.Errorf("invalid string should not be covered")
	}
}

type fakeCloudRelayProvider struct {
	relayIPs     []string
	relayDomains []string
	err          error
}

func (f *fakeCloudRelayProvider) KeenDNSInfo(context.Context) (string, []string, error) {
	return "myrouter.keenetic.pro", []string{"192.168.1.1"}, nil
}

func (f *fakeCloudRelayProvider) KeenCloudRelays(context.Context) ([]string, []string, error) {
	return f.relayIPs, f.relayDomains, f.err
}

func TestSyncKeeneticCloudRelays(t *testing.T) {
	svc := &ServiceImpl{
		appLog:    logging.NewScopedLogger(nil, logging.GroupRouting, logging.SubSingboxRouter),
		bypassLog: logging.NewScopedLogger(nil, logging.GroupRouting, logging.SubBypassSet),
	}
	prov := &fakeCloudRelayProvider{
		relayIPs:     []string{"198.51.100.1", "87.228.71.67"},
		relayDomains: []string{"ndns115.omni.ru"},
	}
	svc.SetKeenDNSPreset(prov, nil)

	// Disabled: no-op
	svc.syncKeeneticCloudRelays(context.Background(), storage.SingboxRouterSettings{KeeneticCloudTunnel: false})
	if len(svc.dynamicCloudCIDRs()) != 0 {
		t.Fatalf("expected 0 dynamic CIDRs when disabled, got %v", svc.dynamicCloudCIDRs())
	}

	// Enabled: discovers relays
	svc.syncKeeneticCloudRelays(context.Background(), storage.SingboxRouterSettings{KeeneticCloudTunnel: true})
	cidrs := svc.dynamicCloudCIDRs()
	if !slices.Contains(cidrs, "198.51.100.1/32") {
		t.Errorf("expected 198.51.100.1/32 in dynamic CIDRs, got %v", cidrs)
	}
	if !slices.Contains(cidrs, "87.228.71.67/32") {
		t.Errorf("expected 87.228.71.67/32 in dynamic CIDRs, got %v", cidrs)
	}

	// Verify BuildKeeneticCloudRules enriches with these CIDRs
	rules := BuildKeeneticCloudRules("vpn", svc.dynamicCloudCIDRs()...)
	ruleCIDRs := rules[2].IPCIDR
	if !slices.Contains(ruleCIDRs, "198.51.100.1/32") {
		t.Errorf("expected 198.51.100.1/32 in enriched rules, got %v", ruleCIDRs)
	}
}
