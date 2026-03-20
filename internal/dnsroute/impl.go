package dnsroute

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// TunnelLister provides tunnel info for route target selection.
type TunnelLister interface {
	ListTunnelInfo(ctx context.Context) ([]TunnelInfo, error)
}

// ServiceImpl implements the Service interface.
// All operations are serialized via opMu to prevent race conditions between
// concurrent HTTP handlers, background scheduler, and tunnel lifecycle hooks.
type ServiceImpl struct {
	opMu     sync.Mutex
	store    *Store
	ndms     ndms.Client
	lister   TunnelLister
	log      *logger.Logger
	appLog   *logging.ScopedLogger
}

// NewService creates a new DNS route service.
func NewService(store *Store, ndmsClient ndms.Client, lister TunnelLister, log *logger.Logger, appLogger logging.AppLogger) *ServiceImpl {
	return &ServiceImpl{
		store:  store,
		ndms:   ndmsClient,
		lister: lister,
		log:    log,
		appLog: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubDnsRoute),
	}
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

	data.Lists = append(data.Lists, list)

	if err := s.store.Save(data); err != nil {
		return nil, fmt.Errorf("save after create: %w", err)
	}

	s.log.Infof("created dns route list %q (%s)", list.Name, list.ID)

	// If the list has subscriptions, fetch them now so Domains gets populated
	// before reconcile. RefreshSubscriptions calls Reconcile at the end.
	if len(list.Subscriptions) > 0 {
		if err := s.refreshSubscriptions(ctx, list.ID); err != nil {
			s.logError("create", list.ID, "Refresh subscriptions failed", err.Error())
		}
	} else {
		if err := s.reconcile(ctx); err != nil {
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

	// Preserve excludes and subnets if not provided (frontend doesn't send them on edit).
	if list.Excludes == nil {
		list.Excludes = existing.Excludes
	}
	if list.Subnets == nil {
		list.Subnets = existing.Subnets
	}

	// Merge domains: manual domains + existing subscription domains.
	manual := deduplicateDomains(list.ManualDomains)
	subDomains := subscriptionDomains(existing.Domains, existing.ManualDomains)
	list.Domains = deduplicateDomains(append(manual, subDomains...))

	data.Lists[idx] = list

	if err := s.store.Save(data); err != nil {
		return nil, fmt.Errorf("save after update: %w", err)
	}

	s.log.Infof("updated dns route list %q (%s)", list.Name, list.ID)

	if err := s.reconcile(ctx); err != nil {
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

	if err := s.reconcile(ctx); err != nil {
		s.logError("delete", id, "Reconcile failed", err.Error())
	}

	return nil
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

	if err := s.reconcile(ctx); err != nil {
		s.logError("set-enabled", id, "Reconcile failed", err.Error())
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
	list.UpdatedAt = now

	if err := s.store.Save(data); err != nil {
		return fmt.Errorf("save after refresh: %w", err)
	}

	s.log.Info("subscriptions refreshed", map[string]interface{}{
		"list": id, "totalDomains": len(list.Domains),
	})

	return s.reconcile(ctx)
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
	return s.reconcile(ctx)
}

// OnTunnelDelete removes route targets referencing the deleted tunnel and reconciles.
func (s *ServiceImpl) OnTunnelDelete(ctx context.Context, tunnelID string) error {
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

	return s.reconcile(ctx)
}

// Reconcile synchronises router state (object-groups, dns-proxy routes) with stored lists.
func (s *ServiceImpl) Reconcile(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.reconcile(ctx)
}

// GetAvailableTunnels returns tunnels that can be used as route targets.
func (s *ServiceImpl) GetAvailableTunnels(ctx context.Context) ([]TunnelInfo, error) {
	return s.lister.ListTunnelInfo(ctx)
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

func (s *ServiceImpl) logInfo(action, target, msg string) {
	s.appLog.Info(action, target, msg)
}

func (s *ServiceImpl) logWarn(action, target, msg string) {
	s.appLog.Warn(action, target, msg)
}

func (s *ServiceImpl) logError(action, target, msg, errMsg string) {
	s.appLog.Warn(action, target, msg+": "+errMsg)
}
