// Package coverage connects SB-only DNS and the AWGM applied active-slot
// snapshot to the in-memory coverage/exclusion evaluator.
package coverage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/awgm"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/capture"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/engine"
)

type Client interface {
	Rules(context.Context) ([]awgm.Rule, error)
	RuleSets(context.Context) ([]awgm.RuleSet, error)
	MatchRuleSets(context.Context, string, []string) ([]awgm.RuleSetMatchResult, error)
	awgm.DNSCatalogClient
}

type indexedClient interface {
	CoverageIndexSnapshot(context.Context, []string) (awgm.CoverageSnapshot, error)
	MatchRuleSetsIndexed(context.Context, string, []string) (awgm.IndexedRuleSetMatch, error)
}

type indexedMatcher struct {
	client   indexedClient
	expected uint64
}

func (m indexedMatcher) MatchRuleSets(ctx context.Context, input string, tags []string) ([]awgm.RuleSetMatchResult, error) {
	out, err := m.client.MatchRuleSetsIndexed(ctx, input, tags)
	if err != nil {
		return nil, err
	}
	if out.Generation == 0 {
		return nil, fmt.Errorf("indexed match returned zero generation")
	}
	if m.expected != 0 && out.Generation != m.expected {
		return nil, fmt.Errorf("%w: snapshot=%d match=%d", awgm.ErrCoverageGenerationChanged, m.expected, out.Generation)
	}
	return out.Results, nil
}

type Service struct {
	client  Client
	routes  *awgm.RouteResolver
	indexed indexedClient
}

type snapshot struct {
	rules      []awgm.Rule
	sets       []awgm.RuleSet
	revision   string
	generation uint64
}

func New(client Client, routes *awgm.RouteResolver) *Service {
	s := &Service{client: client, routes: routes}
	if indexed, ok := client.(indexedClient); ok {
		s.indexed = indexed
	}
	return s
}

func (s *Service) CurrentRevision(ctx context.Context) (string, error) {
	snapshot, err := s.loadSnapshot(ctx, nil)
	if err != nil {
		return "", err
	}
	return snapshot.revision, nil
}

type evaluation struct {
	coverage awgm.CoverageResult
	excluded bool
}

func (s *Service) Check(ctx context.Context, suspect capture.Suspect, cfg config.Config) (engine.CoverageOutcome, error) {
	started := time.Now()
	outcome := engine.CoverageOutcome{}
	finish := func(err error) (engine.CoverageOutcome, error) {
		outcome.Evidence.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			outcome.Evidence.Detail = err.Error()
		}
		return outcome, err
	}
	finishEvaluation := func(result evaluation, err error) (engine.CoverageOutcome, error) {
		if err != nil {
			return finish(err)
		}
		outcome.Evidence.Covered = result.coverage.Covered
		outcome.Evidence.Excluded = result.excluded
		switch {
		case result.excluded:
			outcome.Evidence.Detail = "selected rule-set/geosite exclusion"
		case result.coverage.Covered:
			outcome.Evidence.Detail = fmt.Sprintf("existing rule %d: action=%s outbound=%s", result.coverage.MatchedRule, result.coverage.Action, result.coverage.Outbound)
		default:
			outcome.Evidence.Detail = "no existing coverage or exclusion"
		}
		return finish(nil)
	}

	if manuallyExcluded(suspect.Target.Names(), cfg.ExcludeDomains) {
		outcome.Evidence.Excluded = true
		outcome.Evidence.Detail = "manual domain exclusion"
		return finish(nil)
	}
	// Rules and every per-tag result must belong to one generation. An apply
	// landing mid-check causes a bounded whole-snapshot retry, never mixed
	// gen-N rules with gen-N+1 content under an old revision.
	for attempt := 0; attempt < 3; attempt++ {
		result, revision, err := s.checkCurrent(ctx, suspect, cfg)
		outcome.Revision = revision
		if errors.Is(err, awgm.ErrCoverageGenerationChanged) {
			continue
		}
		return finishEvaluation(result, err)
	}
	return finish(fmt.Errorf("coverage generation changed repeatedly"))
}

func (s *Service) checkCurrent(ctx context.Context, suspect capture.Suspect, cfg config.Config) (evaluation, string, error) {
	snapshotResult, err := s.loadSnapshot(ctx, cfg.ExcludeRuleSets)
	if err != nil {
		return evaluation{}, "", err
	}
	var matcher awgm.RuleSetMatcher = s.client
	if s.indexed != nil {
		matcher = indexedMatcher{client: s.indexed, expected: snapshotResult.generation}
	}
	evaluator := awgm.NewCoverageEvaluator(matcher)
	clientIP, _ := netip.ParseAddr(suspect.ClientIP)
	input := awgm.CoverageInput{
		Target: suspect.Target, ClientIP: clientIP,
		ClientMAC: suspect.ClientMAC, Protocol: suspect.Protocol,
	}

	// IP-only UDP has no name stage and never invokes DNS.
	if suspect.Target.Domain == "" {
		result, err := s.evaluate(ctx, evaluator, snapshotResult.rules, cfg.ExcludeRuleSets, input)
		return result, snapshotResult.revision, err
	}

	// Cheap name checks run before DNS. Target.IP is deliberately cleared so
	// this stage proves only name/static conditions; observed and resolved
	// addresses are evaluated below if no name verdict exists.
	nameInput := input
	nameInput.Target.IP = ""
	nameResult, err := s.evaluate(ctx, evaluator, snapshotResult.rules, cfg.ExcludeRuleSets, nameInput)
	if err != nil || nameResult.excluded || nameResult.coverage.Covered {
		return nameResult, snapshotResult.revision, err
	}

	// DNS and the already-observed AF_PACKET address proceed in parallel.
	var dnsAddresses []netip.Addr
	var dnsErr error
	var observedResult evaluation
	var observedErr error
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		resolver, err := awgm.NewDNSResolver(ctx, s.client, s.routes, cfg.ResolveVia)
		if err != nil {
			dnsErr = err
			return
		}
		queryName := suspect.Target.Host
		if queryName == "" {
			queryName = suspect.Target.Domain
		}
		dnsAddresses, dnsErr = resolver.Resolve(ctx, queryName)
	}()
	if suspect.Target.IP != "" {
		done.Add(1)
		go func() {
			defer done.Done()
			observedInput := input
			observedInput.Target.Domain = ""
			observedInput.Target.Host = ""
			observedResult, observedErr = s.evaluate(ctx, evaluator, snapshotResult.rules, cfg.ExcludeRuleSets, observedInput)
		}()
	}
	done.Wait()
	if observedErr != nil {
		if dnsErr != nil {
			return evaluation{}, snapshotResult.revision, errors.Join(observedErr, dnsErr)
		}
		return evaluation{}, snapshotResult.revision, observedErr
	}
	// Only a DNS failure permits an observed-only final verdict. With DNS
	// success, resolved addresses still need their own exclusion-first pass.
	if (observedResult.excluded || observedResult.coverage.Covered) && dnsErr != nil {
		return observedResult, snapshotResult.revision, nil
	}
	if dnsErr != nil {
		return evaluation{}, snapshotResult.revision, dnsErr
	}

	// Preserve full rule semantics for conditions that combine a name and an
	// address. Exclusions run before routing on every newly resolved IP.
	input.Addresses = dnsAddresses
	result, err := s.evaluate(ctx, evaluator, snapshotResult.rules, cfg.ExcludeRuleSets, input)
	return result, snapshotResult.revision, err
}

// evaluate deliberately checks exclusions before routing. Any unsupported,
// missing, duplicate or erroneous rule-set result fails closed in the existing
// CoverageEvaluator; this function never translates it into a negative match.
func (s *Service) evaluate(ctx context.Context, evaluator *awgm.CoverageEvaluator, rules []awgm.Rule, exclusions []string, input awgm.CoverageInput) (evaluation, error) {
	excluded, err := evaluator.Excluded(ctx, exclusions, input)
	if err != nil || excluded {
		return evaluation{excluded: excluded}, err
	}
	covered, err := evaluator.Check(ctx, rules, input)
	return evaluation{coverage: covered}, err
}

func (s *Service) loadSnapshot(ctx context.Context, exclusionTags []string) (snapshot, error) {
	if s.indexed != nil {
		indexed, err := s.indexed.CoverageIndexSnapshot(ctx, exclusionTags)
		if err != nil {
			return snapshot{}, err
		}
		if indexed.Generation == 0 {
			return snapshot{}, fmt.Errorf("indexed snapshot returned zero generation")
		}
		if indexed.Slot != "router" && indexed.Slot != "fakeip" {
			return snapshot{}, fmt.Errorf("indexed snapshot returned unsupported slot %q", indexed.Slot)
		}
		if indexed.Rules == nil {
			indexed.Rules = []awgm.Rule{}
		}
		if indexed.RuleSets == nil {
			indexed.RuleSets = []awgm.RuleSet{}
		}
		return snapshot{
			rules: indexed.Rules, sets: indexed.RuleSets,
			revision:   fmt.Sprintf("indexed:%s:%d", indexed.Slot, indexed.Generation),
			generation: indexed.Generation,
		}, nil
	}
	rules, err := s.client.Rules(ctx)
	if err != nil {
		return snapshot{}, err
	}
	sets, err := s.client.RuleSets(ctx)
	if err != nil {
		return snapshot{}, err
	}
	encoded, err := json.Marshal(struct {
		Rules []awgm.Rule    `json:"rules"`
		Sets  []awgm.RuleSet `json:"sets"`
	}{Rules: rules, Sets: sets})
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{rules: rules, sets: sets, revision: fmt.Sprintf("%x", sha256.Sum256(encoded))}, nil
}

func manuallyExcluded(names []string, exclusions []string) bool {
	for _, domain := range names {
		for _, excluded := range exclusions {
			excluded = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(excluded), "."))
			if domain == excluded || strings.HasSuffix(domain, "."+excluded) {
				return true
			}
		}
	}
	return false
}
