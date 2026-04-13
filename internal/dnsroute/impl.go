package dnsroute

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// InterfaceResolver resolves tunnel IDs to NDMS interface names for RCI commands.
type InterfaceResolver interface {
	ResolveInterface(ctx context.Context, tunnelID string) (string, error)
}

// ServiceImpl implements the Service interface.
// All operations are serialized via opMu to prevent race conditions between
// concurrent HTTP handlers, background scheduler, and tunnel lifecycle hooks.
type ServiceImpl struct {
	opMu     sync.Mutex
	store    *Store
	ndms     ndms.Client
	resolver InterfaceResolver
	log      *logger.Logger
	appLog   *logging.ScopedLogger
	failover *FailoverManager
	hydra    *hydraroute.Service
}

// NewService creates a new DNS route service.
func NewService(store *Store, ndmsClient ndms.Client, resolver InterfaceResolver, log *logger.Logger, appLogger logging.AppLogger) *ServiceImpl {
	return &ServiceImpl{
		store:    store,
		ndms:     ndmsClient,
		resolver: resolver,
		log:      log,
		appLog:   logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubDnsRoute),
	}
}

// SetFailoverManager sets the failover manager for DNS route failover.
func (s *ServiceImpl) SetFailoverManager(fm *FailoverManager) {
	s.failover = fm
}

// SetHydraRoute sets the HydraRoute Neo service for hydraroute-backend lists.
func (s *ServiceImpl) SetHydraRoute(h *hydraroute.Service) {
	s.hydra = h
}

// LookupAffectedLists returns DNS lists that reference the given tunnelID,
// with FromTunnel/ToTunnel resolved based on the action ("switched" or "restored").
// For "switched": FromTunnel is the failed tunnel's interface name; ToTunnel is the
// next active route in the chain (or empty if none).
// For "restored": ToTunnel is the recovered tunnel's interface name; FromTunnel is
// what was active during the failure (the next route in chain).
func (s *ServiceImpl) LookupAffectedLists(tunnelID string, action string) []AffectedList {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return nil
	}

	var failedSet map[string]struct{}
	if s.failover != nil {
		failed := s.failover.FailedTunnels()
		if len(failed) > 0 {
			failedSet = make(map[string]struct{}, len(failed))
			for _, id := range failed {
				failedSet[id] = struct{}{}
			}
		}
	}

	var result []AffectedList
	for _, list := range data.Lists {
		if !list.Enabled {
			continue
		}

		// Find this tunnel's index in the route chain
		idx := -1
		for i, rt := range list.Routes {
			if rt.TunnelID == tunnelID {
				idx = i
				break
			}
		}
		if idx == -1 {
			continue // tunnel not in this list
		}

		// Determine the next active route in the chain (skipping failed ones).
		// Always exclude the current tunnel itself:
		// - For "switched": it just failed, traffic moves to a replacement.
		// - For "restored": we want "what WAS active during failure" (the replacement),
		//   not the recovered tunnel itself.
		var nextActive string
		for i, rt := range list.Routes {
			if i == idx {
				continue
			}
			if failedSet != nil {
				if _, isFailed := failedSet[rt.TunnelID]; isFailed {
					continue
				}
			}
			nextActive = rt.Interface
			break
		}

		failedIface := list.Routes[idx].Interface
		var from, to string
		if action == "switched" {
			from = failedIface
			to = nextActive
		} else { // "restored"
			from = nextActive
			to = failedIface
		}

		result = append(result, AffectedList{
			ListID:     list.ID,
			ListName:   list.Name,
			FromTunnel: from,
			ToTunnel:   to,
		})
	}

	return result
}

// Create adds a new domain list, persists it, and reconciles router state.
func (s *ServiceImpl) Create(ctx context.Context, list DomainList) (*DomainList, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return nil, fmt.Errorf("store not loaded")
	}

	if strings.TrimSpace(list.Name) == "" {
		return nil, fmt.Errorf("name must not be empty")
	}
	if len(list.ManualDomains) == 0 && len(list.Subscriptions) == 0 {
		return nil, fmt.Errorf("at least one domain or subscription is required")
	}

	now := time.Now().UTC().Format(time.RFC3339)

	list.ID = nextListID(data.Lists)
	list.Enabled = true
	list.CreatedAt = now
	list.UpdatedAt = now
	list.Domains = deduplicateDomains(list.ManualDomains)

	// Resolve tunnel IDs to NDMS interface names for RCI commands.
	if err := s.resolveRouteInterfaces(ctx, list.Routes); err != nil {
		return nil, fmt.Errorf("resolve routes: %w", err)
	}

	s.dedup(&list)

	data.Lists = append(data.Lists, list)

	if err := s.store.Save(data); err != nil {
		return nil, fmt.Errorf("save after create: %w", err)
	}

	s.log.Infof("created dns route list %q (%s)", list.Name, list.ID)

	// Validate subscriptions by fetching them. If any URL fails (wrong
	// Content-Type, unreachable, etc.), reject the entire Create.
	if len(list.Subscriptions) > 0 {
		if err := s.validateSubscriptions(ctx, list.Subscriptions); err != nil {
			// Remove the just-appended list from data.
			data.Lists = data.Lists[:len(data.Lists)-1]
			_ = s.store.Save(data)
			return nil, err
		}
		if err := s.refreshSubscriptions(ctx, list.ID); err != nil {
			s.logError("create", list.ID, "Refresh subscriptions failed", err.Error())
		}
	} else {
		if err := s.reconcileAll(ctx); err != nil {
			s.logError("create", list.ID, "Reconcile failed", err.Error())
		}
	}

	// Re-read the list after refresh (Domains may have been updated).
	for i := range data.Lists {
		if data.Lists[i].ID == list.ID {
			return &data.Lists[i], nil
		}
	}
	return &list, nil
}

// Get returns a domain list by ID.
func (s *ServiceImpl) Get(ctx context.Context, id string) (*DomainList, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return nil, fmt.Errorf("store not loaded")
	}

	for i := range data.Lists {
		if data.Lists[i].ID == id {
			return &data.Lists[i], nil
		}
	}
	return nil, fmt.Errorf("dns route list %q not found", id)
}

// List returns all domain lists.
func (s *ServiceImpl) List(ctx context.Context) ([]DomainList, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return nil, fmt.Errorf("store not loaded")
	}

	if data.Lists == nil {
		return []DomainList{}, nil
	}
	return data.Lists, nil
}

// Update modifies an existing domain list, persists changes, and reconciles.
func (s *ServiceImpl) Update(ctx context.Context, list DomainList) (*DomainList, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return nil, fmt.Errorf("store not loaded")
	}

	idx := -1
	for i := range data.Lists {
		if data.Lists[i].ID == list.ID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, fmt.Errorf("dns route list %q not found", list.ID)
	}

	existing := &data.Lists[idx]

	// Preserve fields not sent by the frontend update payload.
	list.CreatedAt = existing.CreatedAt
	list.ID = existing.ID
	list.Enabled = existing.Enabled
	list.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	// Defense-in-depth for partial updates: Go's json decoder cannot
	// distinguish "field absent" from "field present with zero value", so
	// a payload like {"routes": [...]} decodes into a DomainList where every
	// other field is zero (empty string / nil slice). Without preserve logic
	// Update() would silently wipe Name, ManualDomains, Subscriptions, etc.
	// A bulk "change tunnel" operation used to do exactly that.
	//
	// Policy: treat zero values as "not sent" and restore from existing.
	// This means the caller cannot set Name to "" or clear ManualDomains
	// via this endpoint — but those edge cases are not real user intents
	// (empty list name is invalid, empty ManualDomains makes the list
	// useless). Full-list PUT semantics still work because the caller
	// sends the existing values back unchanged.
	if list.Name == "" {
		list.Name = existing.Name
	}
	if list.ManualDomains == nil {
		list.ManualDomains = existing.ManualDomains
	}
	if list.Subscriptions == nil {
		list.Subscriptions = existing.Subscriptions
	}
	if list.Excludes == nil {
		list.Excludes = existing.Excludes
	}
	if list.Subnets == nil {
		list.Subnets = existing.Subnets
	}
	if list.Routes == nil {
		list.Routes = existing.Routes
	}

	// Validate any new subscription URLs before saving.
	newSubs := findNewSubscriptions(existing.Subscriptions, list.Subscriptions)
	if len(newSubs) > 0 {
		if err := s.validateSubscriptions(ctx, newSubs); err != nil {
			return nil, err
		}
	}

	// Merge domains: manual domains + existing subscription domains.
	manual := deduplicateDomains(list.ManualDomains)
	subDomains := subscriptionDomains(existing.Domains, existing.ManualDomains)
	list.Domains = deduplicateDomains(append(manual, subDomains...))

	// Resolve tunnel IDs to NDMS interface names for RCI commands.
	if err := s.resolveRouteInterfaces(ctx, list.Routes); err != nil {
		return nil, fmt.Errorf("resolve routes: %w", err)
	}

	s.dedup(&list)

	data.Lists[idx] = list

	if err := s.store.Save(data); err != nil {
		return nil, fmt.Errorf("save after update: %w", err)
	}

	s.log.Infof("updated dns route list %q (%s)", list.Name, list.ID)

	if err := s.reconcileAll(ctx); err != nil {
		s.logError("update", list.ID, "Reconcile failed", err.Error())
	}

	return &list, nil
}

// Delete removes a domain list by ID, persists changes, and reconciles.
func (s *ServiceImpl) Delete(ctx context.Context, id string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return fmt.Errorf("store not loaded")
	}

	idx := -1
	for i := range data.Lists {
		if data.Lists[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("dns route list %q not found", id)
	}

	name := data.Lists[idx].Name
	data.Lists = append(data.Lists[:idx], data.Lists[idx+1:]...)

	if err := s.store.Save(data); err != nil {
		return fmt.Errorf("save after delete: %w", err)
	}

	s.log.Infof("deleted dns route list %q (%s)", name, id)

	if err := s.reconcileAll(ctx); err != nil {
		s.logError("delete", id, "Reconcile failed", err.Error())
	}

	return nil
}

// DeleteBatch removes multiple domain lists by IDs, persists once, and reconciles once.
// Returns the count of actually deleted lists.
func (s *ServiceImpl) DeleteBatch(ctx context.Context, ids []string) (int, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return 0, fmt.Errorf("store not loaded")
	}

	toDelete := make(map[string]bool, len(ids))
	for _, id := range ids {
		toDelete[id] = true
	}

	kept := make([]DomainList, 0, len(data.Lists))
	deleted := 0
	for _, list := range data.Lists {
		if toDelete[list.ID] {
			s.log.Infof("deleted dns route list %q (%s)", list.Name, list.ID)
			deleted++
		} else {
			kept = append(kept, list)
		}
	}

	if deleted == 0 {
		return 0, nil
	}

	data.Lists = kept

	if err := s.store.Save(data); err != nil {
		return 0, fmt.Errorf("save after delete batch: %w", err)
	}

	if err := s.reconcileAll(ctx); err != nil {
		s.logError("delete-batch", "", "Reconcile failed", err.Error())
	}

	return deleted, nil
}

// CreateBatch adds multiple domain lists, persists once, and reconciles once.
// Lists with empty name or no domains/subscriptions are silently skipped.
func (s *ServiceImpl) CreateBatch(ctx context.Context, lists []DomainList) ([]*DomainList, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return nil, fmt.Errorf("store not loaded")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var createdIDs []string
	hasSubs := false

	for _, list := range lists {
		if strings.TrimSpace(list.Name) == "" {
			continue
		}
		if len(list.ManualDomains) == 0 && len(list.Subscriptions) == 0 {
			continue
		}

		list.ID = nextListID(data.Lists)
		list.Enabled = true
		list.CreatedAt = now
		list.UpdatedAt = now
		list.Domains = deduplicateDomains(list.ManualDomains)

		if err := s.resolveRouteInterfaces(ctx, list.Routes); err != nil {
			return nil, fmt.Errorf("resolve routes for %q: %w", list.Name, err)
		}

		s.dedup(&list)
		data.Lists = append(data.Lists, list)
		createdIDs = append(createdIDs, list.ID)
		s.log.Infof("created dns route list %q (%s)", list.Name, list.ID)

		if len(list.Subscriptions) > 0 {
			hasSubs = true
		}
	}

	if len(createdIDs) == 0 {
		return []*DomainList{}, nil
	}

	if err := s.store.Save(data); err != nil {
		return nil, fmt.Errorf("save after create batch: %w", err)
	}

	if hasSubs {
		for _, id := range createdIDs {
			// Find the list to check if it has subscriptions.
			for i := range data.Lists {
				if data.Lists[i].ID == id && len(data.Lists[i].Subscriptions) > 0 {
					if err := s.refreshSubscriptions(ctx, id); err != nil {
						s.logError("create-batch", id, "Refresh subscriptions failed", err.Error())
					}
					break
				}
			}
		}
	} else {
		if err := s.reconcileAll(ctx); err != nil {
			s.logError("create-batch", "", "Reconcile failed", err.Error())
		}
	}

	// Collect created lists from data (may have been updated by refreshSubscriptions).
	result := make([]*DomainList, 0, len(createdIDs))
	idSet := make(map[string]bool, len(createdIDs))
	for _, id := range createdIDs {
		idSet[id] = true
	}
	for i := range data.Lists {
		if idSet[data.Lists[i].ID] {
			result = append(result, &data.Lists[i])
		}
	}

	return result, nil
}

// SetEnabled toggles the enabled state of a domain list.
func (s *ServiceImpl) SetEnabled(ctx context.Context, id string, enabled bool) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return fmt.Errorf("store not loaded")
	}

	idx := -1
	for i := range data.Lists {
		if data.Lists[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("dns route list %q not found", id)
	}

	data.Lists[idx].Enabled = enabled
	data.Lists[idx].UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := s.store.Save(data); err != nil {
		return fmt.Errorf("save after set-enabled: %w", err)
	}

	s.log.Infof("set enabled=%v for dns route list %q", enabled, id)

	if err := s.reconcileAll(ctx); err != nil {
		s.logError("set-enabled", id, "Reconcile failed", err.Error())
	}

	return nil
}

// validateSubscriptions fetches each subscription URL and verifies it returns
// text/plain with at least one parseable domain. Returns the first error encountered.
func (s *ServiceImpl) validateSubscriptions(ctx context.Context, subs []Subscription) error {
	for _, sub := range subs {
		if err := validateSubscriptionURL(ctx, sub.URL); err != nil {
			return fmt.Errorf("подписка %q: %w", sub.URL, err)
		}
	}
	return nil
}

// RefreshSubscriptions fetches all subscriptions for a single list and merges domains.
func (s *ServiceImpl) RefreshSubscriptions(ctx context.Context, id string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.refreshSubscriptions(ctx, id)
}

func (s *ServiceImpl) refreshSubscriptions(ctx context.Context, id string) error {
	data := s.store.GetCached()
	if data == nil {
		return fmt.Errorf("store not loaded")
	}

	idx := -1
	for i := range data.Lists {
		if data.Lists[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("dns route list %q not found", id)
	}

	list := &data.Lists[idx]
	now := time.Now().UTC().Format(time.RFC3339)

	// Fetch each subscription
	var allSubDomains [][]string
	for i := range list.Subscriptions {
		sub := &list.Subscriptions[i]
		domains, err := fetchSubscription(ctx, sub.URL)
		sub.LastFetched = now
		if err != nil {
			sub.LastError = err.Error()
			sub.LastCount = 0
			s.log.Warn("subscription fetch failed", map[string]interface{}{
				"list": id, "url": sub.URL, "error": err.Error(),
			})
			// Keep going — one failed subscription shouldn't block others
			continue
		}
		sub.LastError = ""
		sub.LastCount = len(domains)
		allSubDomains = append(allSubDomains, domains)
	}

	// Merge manual + subscription domains
	list.Domains = mergeDomains(list.ManualDomains, allSubDomains)
	s.dedup(list)
	list.UpdatedAt = now

	if err := s.store.Save(data); err != nil {
		return fmt.Errorf("save after refresh: %w", err)
	}

	s.log.Info("subscriptions refreshed", map[string]interface{}{
		"list": id, "totalDomains": len(list.Domains),
	})

	return s.reconcileAll(ctx)
}

// RefreshAllSubscriptions fetches subscriptions for all lists.
func (s *ServiceImpl) RefreshAllSubscriptions(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return fmt.Errorf("store not loaded")
	}

	var lastErr error
	for _, list := range data.Lists {
		if err := s.refreshSubscriptions(ctx, list.ID); err != nil {
			s.logError("refresh", list.ID, "Refresh subscriptions failed", err.Error())
			lastErr = err
		}
	}
	return lastErr
}

// OnTunnelStart reconciles DNS routes after a tunnel becomes available.
func (s *ServiceImpl) OnTunnelStart(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.reconcileAll(ctx)
}

// OnTunnelDelete removes route targets referencing the deleted tunnel and reconciles.
// Also clears any failover state for the deleted tunnel.
func (s *ServiceImpl) OnTunnelDelete(ctx context.Context, tunnelID string) error {
	// Clean up failover state first (uses its own mutex, before opMu lock).
	// Skip if not in failedSet to avoid redundant reconcile.
	if s.failover != nil && s.failover.IsFailed(tunnelID) {
		_ = s.failover.MarkRecovered(tunnelID)
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return nil
	}

	changed := false
	for i := range data.Lists {
		var kept []RouteTarget
		for _, rt := range data.Lists[i].Routes {
			if rt.TunnelID == tunnelID {
				changed = true
				continue
			}
			kept = append(kept, rt)
		}
		if kept == nil {
			kept = []RouteTarget{}
		}
		data.Lists[i].Routes = kept
	}

	if changed {
		if err := s.store.Save(data); err != nil {
			return fmt.Errorf("save after tunnel delete cleanup: %w", err)
		}
		s.log.Infof("removed deleted tunnel %s from dns route targets", tunnelID)
	}

	return s.reconcileAll(ctx)
}

// CleanupAll removes all DNS route objects (AWG_*) from NDMS.
func (s *ServiceImpl) CleanupAll(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.store.Save(EmptyStoreData())
	return s.reconcileAll(ctx)
}

// Reconcile synchronises router state (object-groups, dns-proxy routes) with stored lists.
func (s *ServiceImpl) Reconcile(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.reconcileAll(ctx)
}

// resolveRouteInterfaces fills RouteTarget.Interface from TunnelID via the resolver.
// Frontend sends tunnelId; backend resolves it to the NDMS interface name needed by RCI.
func (s *ServiceImpl) resolveRouteInterfaces(ctx context.Context, routes []RouteTarget) error {
	if s.resolver == nil {
		return nil
	}
	for i := range routes {
		if routes[i].TunnelID == "" {
			continue
		}
		iface, err := s.resolver.ResolveInterface(ctx, routes[i].TunnelID)
		if err != nil {
			return fmt.Errorf("resolve tunnel %s: %w", routes[i].TunnelID, err)
		}
		routes[i].Interface = iface
	}
	return nil
}

// dedup runs domain and subnet deduplication for the given list against all other lists.
// It modifies list.Domains and list.Subnets in place and sets list.LastDedupeReport.
func (s *ServiceImpl) dedup(list *DomainList) {
	data := s.store.GetCached()
	if data == nil {
		return
	}

	// Build index from all lists except the current one.
	idx := BuildIndex(data.Lists, list.ID)
	names := listNameMap(data.Lists)
	names[list.ID] = list.Name

	// Deduplicate domains.
	keptDomains, domainReport := idx.CheckBatch(list.Domains, list.ID, names)

	// Deduplicate subnets.
	keptSubnets, subnetReport := dedupSubnets(list.Subnets, list.ID, data.Lists)

	// Merge reports.
	report := DedupeReport{
		TotalInput:    domainReport.TotalInput + subnetReport.TotalInput,
		TotalKept:     domainReport.TotalKept + subnetReport.TotalKept,
		TotalRemoved:  domainReport.TotalRemoved + subnetReport.TotalRemoved,
		ExactDupes:    domainReport.ExactDupes + subnetReport.ExactDupes,
		WildcardDupes: domainReport.WildcardDupes + subnetReport.WildcardDupes,
		Items:         append(domainReport.Items, subnetReport.Items...),
	}

	list.Domains = keptDomains
	if list.Domains == nil {
		list.Domains = []string{}
	}
	list.Subnets = keptSubnets

	if report.TotalRemoved > 0 {
		list.LastDedupeReport = &report
	} else {
		list.LastDedupeReport = nil
	}
}

// nextListID generates the next sequential list ID (list_1, list_2, ...).
func nextListID(lists []DomainList) string {
	max := 0
	for _, l := range lists {
		if strings.HasPrefix(l.ID, "list_") {
			var n int
			if _, err := fmt.Sscanf(l.ID, "list_%d", &n); err == nil && n > max {
				max = n
			}
		}
	}
	return fmt.Sprintf("list_%d", max+1)
}

// deduplicateDomains returns a lowercased, trimmed, deduplicated domain list.
func deduplicateDomains(domains []string) []string {
	seen := make(map[string]bool, len(domains))
	result := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && !seen[d] {
			seen[d] = true
			result = append(result, d)
		}
	}
	return result
}

// subscriptionDomains returns the domains that came from subscriptions (present in
// allDomains but not in manualDomains). Used to preserve subscription-fetched domains
// when the manual domain list is updated.
func subscriptionDomains(allDomains, manualDomains []string) []string {
	manual := make(map[string]bool, len(manualDomains))
	for _, d := range manualDomains {
		manual[strings.ToLower(strings.TrimSpace(d))] = true
	}

	var sub []string
	for _, d := range allDomains {
		norm := strings.ToLower(strings.TrimSpace(d))
		if norm != "" && !manual[norm] {
			sub = append(sub, norm)
		}
	}
	return sub
}

// ImportNativeRules converts native HydraRoute domain/IP rules into DomainList entries
// and saves them to the DNS route store. Implements hydraroute.NativeImporter.
// Idempotent: rules whose name already exists in the store are skipped.
// Returns the count of newly created lists.
func (s *ServiceImpl) ImportNativeRules(ctx context.Context, rules []hydraroute.NativeRule, ipBlocks []hydraroute.NativeIPBlock) (int, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	data := s.store.GetCached()
	if data == nil {
		return 0, fmt.Errorf("store not loaded")
	}

	// Index ip blocks by lowercase name.
	ipByName := make(map[string]*hydraroute.NativeIPBlock, len(ipBlocks))
	for i := range ipBlocks {
		ipByName[strings.ToLower(ipBlocks[i].Name)] = &ipBlocks[i]
	}

	// Idempotency: skip rules whose name already exists.
	existingNames := make(map[string]bool, len(data.Lists))
	for _, list := range data.Lists {
		existingNames[strings.ToLower(list.Name)] = true
	}

	imported := 0
	now := time.Now().UTC().Format(time.RFC3339)

	for _, rule := range rules {
		if existingNames[strings.ToLower(rule.Name)] {
			continue
		}

		list := DomainList{
			ID:            nextListID(data.Lists),
			Name:          rule.Name,
			ManualDomains: rule.Domains,
			Domains:       rule.Domains,
			Backend:       "hydraroute",
			Enabled:       true,
			CreatedAt:     now,
			UpdatedAt:     now,
			HRRouteMode:   "interface",
			Routes: []RouteTarget{
				{Interface: rule.Target, TunnelID: rule.Target},
			},
		}

		if ipBlock, ok := ipByName[strings.ToLower(rule.Name)]; ok {
			list.Subnets = ipBlock.Subnets
		}

		data.Lists = append(data.Lists, list)
		existingNames[strings.ToLower(rule.Name)] = true
		imported++
	}

	if imported == 0 {
		return 0, nil
	}

	if err := s.store.Save(data); err != nil {
		return 0, fmt.Errorf("save after native import: %w", err)
	}

	s.log.Infof("imported %d native hydraroute rules into dns route store", imported)

	if err := s.reconcileAll(ctx); err != nil {
		s.logError("import-native", "", "Reconcile failed", err.Error())
	}

	return imported, nil
}

// reconcileHydraRoute collects all hydraroute-backend lists and applies them.
func (s *ServiceImpl) reconcileHydraRoute(ctx context.Context) error {
	if s.hydra == nil {
		return nil
	}
	if !s.hydra.GetStatus().Installed {
		return nil
	}

	data := s.store.GetCached()
	if data == nil {
		return nil
	}

	// Ensure Keenetic ip policies exist for policy-mode rules
	for _, list := range data.Lists {
		if !isHydraRoute(list.Backend) || !list.Enabled {
			continue
		}
		if list.HRRouteMode == "policy" && list.HRPolicyName != "" {
			if err := s.hydra.EnsurePolicy(ctx, list.HRPolicyName); err != nil {
				if s.log != nil {
					s.log.Warnf("hydraroute: ensure policy %s: %v", list.HRPolicyName, err)
				}
			}
		}
	}

	var inputs []hydraroute.ListInput
	for _, list := range data.Lists {
		if !isHydraRoute(list.Backend) || !list.Enabled {
			continue
		}
		if len(list.Routes) == 0 {
			continue
		}
		inputs = append(inputs, hydraroute.ListInput{
			ListID:       list.ID,
			ListName:     list.Name,
			TunnelID:     list.Routes[0].TunnelID,
			Domains:      append(list.Domains, list.ManualDomains...),
			Subnets:      list.Subnets,
			HRRouteMode:  list.HRRouteMode,
			HRPolicyName: list.HRPolicyName,
		})
	}

	entries, err := s.hydra.BuildEntries(ctx, inputs)
	if err != nil {
		return fmt.Errorf("build hydraroute entries: %w", err)
	}

	return s.hydra.Apply(ctx, entries)
}

// reconcileAll runs both NDMS and HydraRoute reconciliation.
func (s *ServiceImpl) reconcileAll(ctx context.Context) error {
	err := s.reconcile(ctx)
	if hrErr := s.reconcileHydraRoute(ctx); hrErr != nil {
		if s.log != nil {
			s.log.Warnf("hydraroute reconcile failed: %v", hrErr)
		}
		if err == nil {
			err = hrErr
		}
	}
	return err
}

func (s *ServiceImpl) logInfo(action, target, msg string) {
	s.appLog.Info(action, target, msg)
}

func (s *ServiceImpl) logWarn(action, target, msg string) {
	s.appLog.Warn(action, target, msg)
}

func (s *ServiceImpl) logError(action, target, msg, errMsg string) {
	s.appLog.Warn(action, target, msg+": "+errMsg)
}
