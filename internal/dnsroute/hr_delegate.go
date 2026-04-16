package dnsroute

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/hydraroute"
)

// policyOrchestrator handles the two-step "wait for HR Neo to create the
// policy, then permit the interfaces" sequence used by the new-policy flow.
// Split out for testability.
type policyOrchestrator interface {
	WaitForPolicy(ctx context.Context, policyName string, timeout time.Duration) error
	EnsurePolicyInterfaces(ctx context.Context, policyName string, ndmsIfaces []string) error
}

// hrReady reports whether the HR backend is available for read/write ops.
func (s *ServiceImpl) hrReady() bool {
	return s.hydra != nil && s.hydra.GetStatus().Installed
}

// listHydraRoute returns HR-backed rules read straight from the HR Neo config
// files, converted into the DomainList shape the API expects.
func (s *ServiceImpl) listHydraRoute(ctx context.Context) ([]DomainList, error) {
	if !s.hrReady() {
		return nil, nil
	}
	rules, _, err := s.hydra.ListRules()
	if err != nil {
		return nil, fmt.Errorf("list HR rules: %w", err)
	}

	// Classify Target: is it a policy name or a kernel iface? Best-effort —
	// a transient NDMS error just means everything looks like an interface.
	policyNames, _ := s.hydra.ListPolicyNames(ctx)
	policySet := make(map[string]bool, len(policyNames))
	for _, n := range policyNames {
		policySet[n] = true
	}

	result := make([]DomainList, 0, len(rules))
	for _, r := range rules {
		result = append(result, hrRuleToDomainList(r, policySet))
	}
	return result, nil
}

// hrIDPrefix tags HR rule identifiers in the API layer so dispatch by ID
// keeps working even though HR rules use their name as the real identity.
const hrIDPrefix = "hr:"

// hrIDFromName builds the API-side ID for an HR rule.
func hrIDFromName(name string) string { return hrIDPrefix + name }

// nameFromHRID returns the rule name from an "hr:Name" ID; empty if not HR.
func nameFromHRID(id string) string {
	if !strings.HasPrefix(id, hrIDPrefix) {
		return ""
	}
	return strings.TrimPrefix(id, hrIDPrefix)
}

// isHRID reports whether the ID refers to an HR rule.
func isHRID(id string) bool { return strings.HasPrefix(id, hrIDPrefix) }

// hrRuleToDomainList converts the HR-file-native shape into the DomainList
// contract shared with NDMS. Subscriptions/enabled/dedup don't exist at this
// layer — HR rules are always "active" by virtue of being in the file.
func hrRuleToDomainList(r hydraroute.HRRule, policySet map[string]bool) DomainList {
	domains := append([]string(nil), r.Domains...)
	domains = append(domains, r.Subnets...)

	dl := DomainList{
		ID:            hrIDFromName(r.Name),
		Name:          r.Name,
		Domains:       r.Domains,
		Subnets:       r.Subnets,
		ManualDomains: domains,
		Backend:       "hydraroute",
		Enabled:       true,
	}
	if policySet[r.Target] {
		dl.HRRouteMode = "policy"
		dl.HRPolicyName = r.Target
		return dl
	}
	dl.HRRouteMode = "interface"
	dl.Routes = []RouteTarget{{Interface: r.Target, TunnelID: r.Target}}
	return dl
}

// createHydraRoute validates the input, resolves the target tunnel, and
// persists a new rule straight into the HR Neo config files. Returns the
// stored rule re-read through the same pipeline so callers see exactly
// what the next List() will show.
func (s *ServiceImpl) createHydraRoute(ctx context.Context, list DomainList) (*DomainList, error) {
	if !s.hrReady() {
		return nil, fmt.Errorf("HydraRoute Neo is not installed")
	}
	if strings.TrimSpace(list.Name) == "" {
		return nil, fmt.Errorf("name must not be empty")
	}
	if len(list.ManualDomains) == 0 {
		return nil, fmt.Errorf("at least one domain or subnet is required")
	}

	target, err := s.resolveHRTarget(ctx, list)
	if err != nil {
		return nil, err
	}

	domains, subnets := splitDomainsAndSubnets(deduplicateDomains(list.ManualDomains))

	created, err := s.hydra.CreateRule(hydraroute.HRRule{
		Name:    list.Name,
		Domains: domains,
		Subnets: subnets,
		Target:  target,
	})
	if err != nil {
		return nil, err
	}

	// Orchestrate policy flow: wait for HR Neo to create the policy on the
	// router (new-policy only), then permit the user's interfaces in order.
	if err := s.applyPolicyInterfaces(ctx, list); err != nil {
		return nil, err
	}

	s.log.Infof("hydraroute: created dns-route %q", created.Name)

	dl := hrRuleToDomainList(*created, s.currentPolicySet(ctx))
	dl.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	dl.UpdatedAt = dl.CreatedAt
	return &dl, nil
}

// updateHydraRoute replaces an existing HR rule identified by id ("hr:Name").
// list.Name in the payload may differ — that's a rename, handled by HR layer.
func (s *ServiceImpl) updateHydraRoute(ctx context.Context, id string, list DomainList) (*DomainList, error) {
	if !s.hrReady() {
		return nil, fmt.Errorf("HydraRoute Neo is not installed")
	}
	originalName := nameFromHRID(id)
	if originalName == "" {
		// Caller passed a bare name (legacy or non-prefixed) — accept it.
		originalName = id
	}
	target, err := s.resolveHRTarget(ctx, list)
	if err != nil {
		return nil, err
	}
	domains, subnets := splitDomainsAndSubnets(deduplicateDomains(list.ManualDomains))

	updated, err := s.hydra.UpdateRule(originalName, hydraroute.HRRule{
		Name:    list.Name,
		Domains: domains,
		Subnets: subnets,
		Target:  target,
	})
	if err != nil {
		return nil, err
	}
	if err := s.applyPolicyInterfaces(ctx, list); err != nil {
		return nil, err
	}
	s.log.Infof("hydraroute: updated dns-route %q (was %q)", updated.Name, originalName)

	dl := hrRuleToDomainList(*updated, s.currentPolicySet(ctx))
	dl.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return &dl, nil
}

// resolveHRTarget turns the user-facing DomainList fields into the opaque
// Target string that lives in the HR config file. Policy mode passes the
// policy name through; interface mode resolves the tunnel ID to a kernel
// interface name and refuses to write if the tunnel doesn't exist.
func (s *ServiceImpl) resolveHRTarget(ctx context.Context, list DomainList) (string, error) {
	if list.HRRouteMode == "policy" {
		if strings.TrimSpace(list.HRPolicyName) == "" {
			return "", fmt.Errorf("policy mode requires hrPolicyName")
		}
		return list.HRPolicyName, nil
	}
	if len(list.Routes) == 0 {
		return "", fmt.Errorf("interface mode requires a tunnel")
	}
	tunnelID := list.Routes[0].TunnelID
	iface, err := s.resolver.GetKernelIfaceName(ctx, tunnelID)
	if err != nil {
		return "", fmt.Errorf("tunnel %q is not available: %w", tunnelID, err)
	}
	return iface, nil
}

// currentPolicySet is a best-effort snapshot of policy names used only to
// classify Target during DomainList conversion.
func (s *ServiceImpl) currentPolicySet(ctx context.Context) map[string]bool {
	names, _ := s.hydra.ListPolicyNames(ctx)
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// policyWaitTimeout is how long we wait for HR Neo to actually create a new
// policy on the router after writing the rule and restarting the daemon.
// Covers the 2s scheduleRestart debounce + restart + daemon's RCI round-trip
// with a comfortable margin.
const policyWaitTimeout = 15 * time.Second

// applyPolicyInterfaces permits the requested interfaces in the target policy,
// in the order given. Used by the HR policy-mode create/update paths.
//
//   - Existing-policy flow (HRPolicyInterfaces empty): legacy fallback
//     resolves Routes[0].TunnelID to an NDMS name and permits that single
//     interface.
//   - New-policy flow (HRPolicyInterfaces non-empty): the slice already
//     contains NDMS interface names. Wait for HR Neo to create the policy
//     on the router, then permit the names in the given order.
//
// A failure here propagates up to fail the Create/Update so the frontend can
// surface it. The rule itself has already been written to HR files — HR Neo
// keeps them as SoT, so a failed permit leaves the user with a rule until
// they retry via Access Policies.
func (s *ServiceImpl) applyPolicyInterfaces(ctx context.Context, list DomainList) error {
	if list.HRRouteMode != "policy" || list.HRPolicyName == "" {
		return nil
	}
	if s.policyOrchestrator == nil {
		return nil
	}

	var ndmsNames []string
	newPolicyFlow := len(list.HRPolicyInterfaces) > 0
	if newPolicyFlow {
		// Frontend sends NDMS names directly in this flow.
		ndmsNames = append(ndmsNames, list.HRPolicyInterfaces...)
	} else if len(list.Routes) > 0 {
		// Legacy single-interface path: still resolve from a TunnelID.
		name, err := s.resolver.ResolveInterface(ctx, list.Routes[0].TunnelID)
		if err == nil && name != "" {
			ndmsNames = []string{name}
		}
	}
	if len(ndmsNames) == 0 {
		return nil
	}

	if newPolicyFlow {
		if err := s.policyOrchestrator.WaitForPolicy(ctx, list.HRPolicyName, policyWaitTimeout); err != nil {
			return fmt.Errorf("policy %q not created on router: %w", list.HRPolicyName, err)
		}
	}

	return s.policyOrchestrator.EnsurePolicyInterfaces(ctx, list.HRPolicyName, ndmsNames)
}

