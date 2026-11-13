package coverage

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/awgm"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/capture"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

type fakeClient struct {
	mu         sync.Mutex
	rules      []awgm.Rule
	sets       []awgm.RuleSet
	inputs     []string
	matches    map[string]bool
	rulesCalls int
	dns        awgm.DNSServer
	dnsPort    int
}

func (f *fakeClient) Rules(context.Context) ([]awgm.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rulesCalls++
	return append([]awgm.Rule(nil), f.rules...), nil
}
func (f *fakeClient) RuleSets(context.Context) ([]awgm.RuleSet, error) { return f.sets, nil }
func (f *fakeClient) MatchRuleSets(_ context.Context, input string, tags []string) ([]awgm.RuleSetMatchResult, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, input)
	f.mu.Unlock()
	out := make([]awgm.RuleSetMatchResult, 0, len(tags))
	for _, tag := range tags {
		out = append(out, awgm.RuleSetMatchResult{Tag: tag, Supported: true, Matched: f.matches[input+"|"+tag]})
	}
	return out, nil
}
func (f *fakeClient) DNSGlobals(context.Context) (awgm.DNSGlobals, error) {
	return awgm.DNSGlobals{Final: "dns"}, nil
}
func (f *fakeClient) DNSServers(context.Context) ([]awgm.DNSServer, error) {
	server := f.dns
	server.ServerPort = f.dnsPort
	return []awgm.DNSServer{server}, nil
}

func localDNS(t *testing.T) int {
	return localDNSAddress(t, net.ParseIP("185.59.211.40"))
}

func localDNSAddress(t *testing.T, address net.IP) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 512)
		for {
			n, from, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			query := append([]byte(nil), buffer[:n]...)
			response := []byte{query[0], query[1], 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0}
			qtype := binary.BigEndian.Uint16(query[n-4 : n-2])
			if qtype == 1 {
				binary.BigEndian.PutUint16(response[6:8], 1)
			}
			response = append(response, query[12:]...)
			if qtype == 1 {
				response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
				response = append(response, address.To4()...)
			}
			if _, err := conn.WriteToUDP(response, from); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { conn.Close(); <-done })
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func TestIPOnlyCoverageNeedsNoDNS(t *testing.T) {
	client := &fakeClient{rules: []awgm.Rule{{IPCIDR: []string{"185.59.211.40/32"}, Action: "route", Outbound: "vpn"}}}
	service := New(client, nil)
	target, _ := model.NewTarget("", "185.59.211.40", model.NetworkUDP, 3478)
	outcome, err := service.Check(context.Background(), capture.Suspect{Target: target, Protocol: "stun"}, config.Default())
	if err != nil || !outcome.Evidence.Covered || outcome.Revision == "" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestDomainCoverageUsesSBResolverAndObservedIP(t *testing.T) {
	client := &fakeClient{
		rules: []awgm.Rule{{RuleSet: []string{"geo"}, Action: "route", Outbound: "vpn"}},
		dns:   awgm.DNSServer{Tag: "dns", Type: "udp", Server: "127.0.0.1"},
	}
	client.dnsPort = localDNS(t)
	service := New(client, nil)
	target, _ := model.NewTarget("example.com", "185.59.211.40", model.NetworkTCP, 443)
	outcome, err := service.Check(context.Background(), capture.Suspect{Target: target, Protocol: "tls"}, config.Default())
	if err != nil || outcome.Evidence.Covered {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	seen := map[string]bool{}
	for _, input := range client.inputs {
		seen[input] = true
	}
	if !seen["example.com"] || !seen["185.59.211.40"] {
		t.Fatalf("matcher inputs=%v", client.inputs)
	}
}

func TestManualExclusion(t *testing.T) {
	client := &fakeClient{dns: awgm.DNSServer{Tag: "dns", Type: "udp", Server: "127.0.0.1"}}
	client.dnsPort = localDNS(t)
	service := New(client, nil)
	target, _ := model.NewTarget("www.example.com", "185.59.211.40", model.NetworkTCP, 443)
	cfg := config.Default()
	cfg.ExcludeDomains = []string{"example.com"}
	outcome, err := service.Check(context.Background(), capture.Suspect{Target: target}, cfg)
	if err != nil || !outcome.Evidence.Excluded {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestRevisionChangesWithSnapshot(t *testing.T) {
	client := &fakeClient{}
	service := New(client, nil)
	first, err := service.CurrentRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	client.rules = []awgm.Rule{{DomainSuffix: []string{"example.com"}, Action: "route", Outbound: "direct"}}
	second, err := service.CurrentRevision(context.Background())
	if err != nil || first == second {
		t.Fatalf("revision did not change: %q %q err=%v", first, second, err)
	}
}

func TestNameCoverageSkipsDNS(t *testing.T) {
	client := &fakeClient{rules: []awgm.Rule{{DomainSuffix: []string{"example.com"}, Action: "route", Outbound: "vpn"}}}
	// No DNS server is configured: any accidental DNS attempt fails the test.
	service := New(client, nil)
	target, _ := model.NewTarget("www.example.com", "185.59.211.40", model.NetworkTCP, 443)
	outcome, err := service.Check(context.Background(), capture.Suspect{Target: target}, config.Default())
	if err != nil || !outcome.Evidence.Covered {
		t.Fatalf("name coverage=%+v err=%v", outcome, err)
	}
}

func TestObservedCoverageSurvivesDNSFailure(t *testing.T) {
	client := &fakeClient{rules: []awgm.Rule{{IPCIDR: []string{"185.59.211.40/32"}, Action: "route", Outbound: "vpn"}}}
	service := New(client, nil)
	target, _ := model.NewTarget("example.com", "185.59.211.40", model.NetworkTCP, 443)
	outcome, err := service.Check(context.Background(), capture.Suspect{Target: target}, config.Default())
	if err != nil || !outcome.Evidence.Covered {
		t.Fatalf("observed coverage=%+v err=%v", outcome, err)
	}
}

func TestDNSFailureWithoutOtherVerdictFailsClosed(t *testing.T) {
	client := &fakeClient{}
	service := New(client, nil)
	target, _ := model.NewTarget("example.com", "185.59.211.40", model.NetworkTCP, 443)
	if _, err := service.Check(context.Background(), capture.Suspect{Target: target}, config.Default()); err == nil {
		t.Fatal("DNS error became a no-coverage verdict")
	}
}

type fakeIndexedClient struct {
	*fakeClient
	generation          uint64
	snapshotGenerations []uint64
	matchGeneration     uint64
	exclusions          []string
	snapshotErr         error
	matchErr            error
	indexedCalls        int
}

func (f *fakeIndexedClient) CoverageIndexSnapshot(_ context.Context, tags []string) (awgm.CoverageSnapshot, error) {
	f.exclusions = append([]string(nil), tags...)
	generation := f.generation
	if len(f.snapshotGenerations) > 0 {
		generation = f.snapshotGenerations[0]
		f.snapshotGenerations = f.snapshotGenerations[1:]
	}
	return awgm.CoverageSnapshot{Generation: generation, Slot: "fakeip", Rules: f.rules, RuleSets: f.sets}, f.snapshotErr
}

func (f *fakeIndexedClient) MatchRuleSetsIndexed(_ context.Context, input string, tags []string) (awgm.IndexedRuleSetMatch, error) {
	f.indexedCalls++
	if f.matchErr != nil {
		return awgm.IndexedRuleSetMatch{}, f.matchErr
	}
	results, err := f.fakeClient.MatchRuleSets(context.Background(), input, tags)
	generation := f.generation
	if f.matchGeneration != 0 {
		generation = f.matchGeneration
	}
	return awgm.IndexedRuleSetMatch{Generation: generation, Results: results}, err
}

func TestIndexedSnapshotAndMatcherAreExclusive(t *testing.T) {
	base := &fakeClient{
		rules: []awgm.Rule{{RuleSet: []string{"geo"}, Action: "route", Outbound: "vpn"}},
		dns:   awgm.DNSServer{Tag: "dns", Type: "udp", Server: "127.0.0.1"},
	}
	base.dnsPort = localDNS(t)
	client := &fakeIndexedClient{fakeClient: base, generation: 7}
	service := New(client, nil)
	target, _ := model.NewTarget("example.com", "185.59.211.40", model.NetworkTCP, 443)
	cfg := config.Default()
	cfg.ExcludeRuleSets = []string{"excluded"}
	outcome, err := service.Check(context.Background(), capture.Suspect{Target: target}, cfg)
	if err != nil || outcome.Revision != "indexed:fakeip:7" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if base.rulesCalls != 0 || client.indexedCalls == 0 || len(client.exclusions) != 1 {
		t.Fatalf("legacy rules calls=%d indexed calls=%d exclusions=%v", base.rulesCalls, client.indexedCalls, client.exclusions)
	}
}

func TestIndexedFailureDoesNotFallBackToLegacy(t *testing.T) {
	base := &fakeClient{}
	client := &fakeIndexedClient{fakeClient: base, generation: 1, snapshotErr: errors.New("generation failed")}
	service := New(client, nil)
	target, _ := model.NewTarget("example.com", "185.59.211.40", model.NetworkTCP, 443)
	if _, err := service.Check(context.Background(), capture.Suspect{Target: target}, config.Default()); err == nil {
		t.Fatal("indexed failure was hidden")
	}
	if base.rulesCalls != 0 || len(base.inputs) != 0 {
		t.Fatal("legacy coverage path was used after indexed failure")
	}
}

func TestIndexedGenerationMismatchReloadsWholeSnapshot(t *testing.T) {
	base := &fakeClient{
		rules: []awgm.Rule{{RuleSet: []string{"geo"}, Action: "route", Outbound: "vpn"}},
		dns:   awgm.DNSServer{Tag: "dns", Type: "udp", Server: "127.0.0.1"},
	}
	base.dnsPort = localDNS(t)
	client := &fakeIndexedClient{
		fakeClient: base, generation: 2, snapshotGenerations: []uint64{1, 2}, matchGeneration: 2,
	}
	service := New(client, nil)
	target, _ := model.NewTarget("example.com", "185.59.211.40", model.NetworkTCP, 443)
	outcome, err := service.Check(context.Background(), capture.Suspect{Target: target}, config.Default())
	if err != nil || outcome.Revision != "indexed:fakeip:2" || client.indexedCalls < 2 {
		t.Fatalf("outcome=%+v err=%v calls=%d", outcome, err, client.indexedCalls)
	}
}

func TestResolvedExclusionRunsDespiteObservedCoverage(t *testing.T) {
	client := &fakeClient{
		rules:   []awgm.Rule{{IPCIDR: []string{"203.0.113.5/32"}, Action: "route", Outbound: "vpn"}},
		matches: map[string]bool{"185.59.211.40|geo": true},
		dns:     awgm.DNSServer{Tag: "dns", Type: "udp", Server: "127.0.0.1"},
	}
	client.dnsPort = localDNSAddress(t, net.ParseIP("185.59.211.40"))
	service := New(client, nil)
	target, _ := model.NewTarget("example.com", "203.0.113.5", model.NetworkTCP, 443)
	cfg := config.Default()
	cfg.ExcludeRuleSets = []string{"geo"}
	outcome, err := service.Check(context.Background(), capture.Suspect{Target: target}, cfg)
	if err != nil || !outcome.Evidence.Excluded || outcome.Evidence.Covered {
		t.Fatalf("resolved exclusion=%+v err=%v", outcome, err)
	}
}
