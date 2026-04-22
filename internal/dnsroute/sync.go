package dnsroute

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// --- Target state types (what we WANT on the router) ---

type targetGroup struct {
	name     string
	includes []string
	excludes []string
	subnets  []string
}

type targetRoute struct {
	group    string
	iface    string
	fallback string
	// disabled — when true, the route must exist on the router but be
	// paused via dns-proxy.route.disable. Disabled routes stay materialized
	// so re-enabling is a cheap one-call toggle (SetDisabled) rather than
	// a full recreate. Introduced to mirror Keenetic-native toggle semantics.
	disabled bool
}

type targetState struct {
	groups []targetGroup
	routes []targetRoute
}

// --- Current state types (what the router HAS) ---

type currentGroupData struct {
	includes []string
	excludes []string
}

type currentState struct {
	groups map[string]currentGroupData
	routes []currentRoute
}

type currentRoute struct {
	group    string
	iface    string
	fallback string
	// index is Keenetic's stable hash from /show/sc/dns-proxy/route, used
	// for the disable.index toggle command. Empty when routes were fetched
	// from an endpoint that doesn't expose indexes (shouldn't happen after
	// the sc migration, but kept tolerant for tests).
	index    string
	disabled bool
}

// --- RCI diff types ---

type rciDiff struct {
	routeDeletes  []rciRouteDelete
	groupDeletes  []string
	groupUpdates  []rciGroupUpdate
	routeUpserts  []rciRouteOp
	routeDisables []rciRouteDisable
}

// rciRouteDisable is a toggle of the `disable` flag on an existing route,
// keyed by Keenetic's stable index hash. Pending disables for routes that
// were just upserted (no index yet) are carried by routeUpserts.Disabled
// and applied after a post-upsert refetch.
type rciRouteDisable struct {
	Index    string
	Disabled bool
	// Group/Iface carried for logging; not used in the RCI payload.
	Group string
	Iface string
}

type rciRouteDelete struct {
	Group string `json:"group"`
	Iface string `json:"interface"`
	No    bool   `json:"no"`
}

type rciRouteOp struct {
	Group  string `json:"group"`
	Iface  string `json:"interface"`
	Auto   bool   `json:"auto,omitempty"`
	Reject bool   `json:"reject,omitempty"`
	// Disabled — if true, the route is created/updated as enabled first
	// (UpsertRoutes has no `disable` field), and the reconciler fetches
	// its freshly-assigned index post-upsert to issue a SetDisabled call.
	Disabled bool `json:"-"`
}

type rciGroupUpdate struct {
	name           string
	addIncludes    []string
	removeIncludes []string
	addExcludes    []string
	removeExcludes []string
	isNew          bool
}

func (d rciDiff) isEmpty() bool {
	return len(d.routeDeletes) == 0 && len(d.groupDeletes) == 0 &&
		len(d.groupUpdates) == 0 && len(d.routeUpserts) == 0 &&
		len(d.routeDisables) == 0
}

// reconcile is the main entry point: reads desired and actual state, computes diff, applies.
func (s *ServiceImpl) reconcile(ctx context.Context) error {
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
	target := buildTargetState(data, failedSet)

	// Force-refresh caches: router state may have been mutated since the
	// last fetch (60-minute TTL is too long for reconcile-time freshness).
	if s.queries != nil {
		s.queries.ObjectGroups.InvalidateAll()
		s.queries.DNSProxy.InvalidateAll()
	}

	allRoutes, err := s.queries.DNSProxy.List(ctx)
	if errors.Is(err, query.ErrNotSupportedOnOS4) {
		// OS4 has no NDMS dns-proxy; dnsroute is a no-op on this platform.
		// HR Neo handles DNS routing on OS4 via the hr_delegate path.
		return nil
	}
	if err != nil {
		s.logError("reconcile", "", "Failed to read dns-proxy routes", err.Error())
		return fmt.Errorf("show dns-proxy route: %w", err)
	}

	allGroups, err := s.queries.ObjectGroups.List(ctx)
	if err != nil {
		s.logError("reconcile", "", "Failed to read object-groups", err.Error())
		return fmt.Errorf("show object-group fqdn: %w", err)
	}

	current := filterAWGState(allGroups, allRoutes)

	diff := computeDiff(current, target)
	if diff.isEmpty() {
		return nil
	}

	s.logInfo("reconcile", "", fmt.Sprintf("Reconciling: %d group deletes, %d group updates, %d route deletes, %d route upserts, %d route disables",
		len(diff.groupDeletes), len(diff.groupUpdates), len(diff.routeDeletes), len(diff.routeUpserts), len(diff.routeDisables)))

	// Detailed names — the count-only log above loses diagnostic power when
	// reconcile runs but the router still shows an old entry; these lines
	// let triage see exactly which AWG_* groups/routes were targeted.
	if len(diff.groupDeletes) > 0 {
		s.logInfo("reconcile", "", fmt.Sprintf("Group deletes: %v", diff.groupDeletes))
	}
	if len(diff.routeDeletes) > 0 {
		pairs := make([]string, 0, len(diff.routeDeletes))
		for _, rd := range diff.routeDeletes {
			pairs = append(pairs, rd.Group+"->"+rd.Iface)
		}
		s.logInfo("reconcile", "", fmt.Sprintf("Route deletes: %v", pairs))
	}
	if len(diff.routeDisables) > 0 {
		pairs := make([]string, 0, len(diff.routeDisables))
		for _, rd := range diff.routeDisables {
			pairs = append(pairs, fmt.Sprintf("%s->%s=%v", rd.Group, rd.Iface, rd.Disabled))
		}
		s.logInfo("reconcile", "", fmt.Sprintf("Route disables: %v", pairs))
	}

	applyErr := s.applyDiff(ctx, diff)
	if applyErr != nil {
		s.logError("reconcile", "", "Partial apply failure", applyErr.Error())
		return fmt.Errorf("apply diff: %w", applyErr)
	}

	s.logInfo("reconcile", "", "Reconcile complete")
	return nil
}

// buildTargetState converts stored domain lists into the desired router state.
func buildTargetState(data *StoreData, failedTunnels map[string]struct{}) targetState {
	var ts targetState

	for _, list := range data.Lists {
		// Skip non-NDMS lists (handled by hydraroute reconcile)
		if !isNDMS(list.Backend) {
			continue
		}
		if len(list.Domains) == 0 && len(list.Subnets) == 0 {
			continue
		}
		// Disabled lists stay in target state but are marked disabled —
		// the route is kept on the router and toggled via
		// dns-proxy.route.disable rather than delete/recreate. This matches
		// Keenetic's own UI toggle semantics.
		routeDisabled := !list.Enabled

		chunks := chunkDomains(list.Domains, MaxDomainsPerGroup)
		// Ensure at least one group even if Domains is empty but Subnets is not.
		if len(chunks) == 0 {
			chunks = [][]string{{}}
		}

		for i, chunk := range chunks {
			groupName := buildGroupName(list.ID, list.Name, i+1)

			g := targetGroup{
				name:     groupName,
				includes: chunk,
			}

			// Excludes and subnets go into the first group only.
			if i == 0 {
				g.excludes = list.Excludes
				g.subnets = list.Subnets
			}

			ts.groups = append(ts.groups, g)

			// Filter out failed tunnels, reassign fallback to last active route.
			var activeRoutes []RouteTarget
			for _, rt := range list.Routes {
				if failedTunnels != nil {
					if _, failed := failedTunnels[rt.TunnelID]; failed {
						continue
					}
				}
				activeRoutes = append(activeRoutes, rt)
			}
			for j, rt := range activeRoutes {
				fallback := ""
				if j == len(activeRoutes)-1 && len(list.Routes) > 0 {
					// Last active route inherits the fallback from the original last route
					fallback = list.Routes[len(list.Routes)-1].Fallback
				}
				ts.routes = append(ts.routes, targetRoute{
					group:    groupName,
					iface:    rt.Interface,
					fallback: fallback,
					disabled: routeDisabled,
				})
			}
		}
	}

	return ts
}

// chunkDomains splits a domain slice into chunks of at most maxSize.
func chunkDomains(domains []string, maxSize int) [][]string {
	if len(domains) == 0 {
		return nil
	}
	var chunks [][]string
	for i := 0; i < len(domains); i += maxSize {
		end := i + maxSize
		if end > len(domains) {
			end = len(domains)
		}
		chunks = append(chunks, domains[i:end])
	}
	return chunks
}

// filterAWGState extracts only AWG_* groups and their routes from the full router state.
func filterAWGState(groups []ndms.FQDNGroup, routes []ndms.DNSRouteRule) currentState {
	cs := currentState{
		groups: make(map[string]currentGroupData),
	}

	for _, g := range groups {
		if !strings.HasPrefix(g.Name, GroupPrefix) {
			continue
		}
		cs.groups[g.Name] = currentGroupData{
			includes: g.Includes,
			excludes: g.Excludes,
		}
	}

	for _, r := range routes {
		if !strings.HasPrefix(r.Group, GroupPrefix) {
			continue
		}
		var fallback string
		if r.Reject {
			fallback = "reject"
		}
		cs.routes = append(cs.routes, currentRoute{
			group:    r.Group,
			iface:    r.Interface,
			fallback: fallback,
			index:    r.Index,
			disabled: r.Disabled,
		})
	}

	return cs
}

// computeDiff computes the minimal incremental diff between current and target state.
func computeDiff(current currentState, target targetState) rciDiff {
	var diff rciDiff

	targetGroupSet := make(map[string]*targetGroup)
	for i := range target.groups {
		targetGroupSet[target.groups[i].name] = &target.groups[i]
	}

	// --- Groups ---

	// Delete: exist on router but not in target
	for name := range current.groups {
		if _, want := targetGroupSet[name]; !want {
			diff.groupDeletes = append(diff.groupDeletes, name)
		}
	}
	sort.Strings(diff.groupDeletes)

	// Create or update
	for _, tg := range target.groups {
		cur, exists := current.groups[tg.name]
		allIncludes := tg.includes
		if len(tg.subnets) > 0 {
			allIncludes = append(append([]string{}, tg.includes...), tg.subnets...)
		}

		if !exists {
			diff.groupUpdates = append(diff.groupUpdates, rciGroupUpdate{
				name:        tg.name,
				addIncludes: allIncludes,
				addExcludes: tg.excludes,
				isNew:       true,
			})
			continue
		}

		addInc, removeInc := diffStringSlices(cur.includes, allIncludes)
		addExc, removeExc := diffStringSlices(cur.excludes, tg.excludes)

		if len(addInc) > 0 || len(removeInc) > 0 || len(addExc) > 0 || len(removeExc) > 0 {
			diff.groupUpdates = append(diff.groupUpdates, rciGroupUpdate{
				name:           tg.name,
				addIncludes:    addInc,
				removeIncludes: removeInc,
				addExcludes:    addExc,
				removeExcludes: removeExc,
			})
		}
	}

	// --- Routes ---

	currentByGroup := make(map[string][]currentRoute)
	for _, cr := range current.routes {
		currentByGroup[cr.group] = append(currentByGroup[cr.group], cr)
	}
	targetByGroup := make(map[string][]targetRoute)
	for _, tr := range target.routes {
		targetByGroup[tr.group] = append(targetByGroup[tr.group], tr)
	}

	// Delete routes for deleted groups
	for _, name := range diff.groupDeletes {
		for _, cr := range currentByGroup[name] {
			diff.routeDeletes = append(diff.routeDeletes, rciRouteDelete{
				Group: cr.group, Iface: cr.iface, No: true,
			})
		}
	}

	// For each target group: compare routes, delete removed, upsert if changed,
	// toggle disable flag if only that differs.
	for group, tgts := range targetByGroup {
		curs := currentByGroup[group]

		if routesEqual(curs, tgts) {
			continue
		}

		// Index current routes by iface for O(1) lookups while diffing.
		curByIface := make(map[string]currentRoute, len(curs))
		for _, cr := range curs {
			curByIface[cr.iface] = cr
		}

		// Delete current routes for interfaces no longer in target
		targetIfaceSet := make(map[string]bool)
		for _, tr := range tgts {
			targetIfaceSet[tr.iface] = true
		}
		for _, cr := range curs {
			if !targetIfaceSet[cr.iface] {
				diff.routeDeletes = append(diff.routeDeletes, rciRouteDelete{
					Group: cr.group, Iface: cr.iface, No: true,
				})
			}
		}

		// For each target route:
		//  - if no matching current route → upsert (new route).
		//  - if current exists and only `disabled` differs → issue a
		//    disable.index toggle (no recreate).
		//  - otherwise (fallback/reject diff) → upsert.
		//
		// Upserts that should end up disabled carry the Disabled flag so
		// applyDiff can re-fetch the freshly-assigned index and follow up
		// with SetDisabled. UpsertRoutes itself has no `disable` field.
		for _, tr := range tgts {
			cr, exists := curByIface[tr.iface]
			if !exists {
				diff.routeUpserts = append(diff.routeUpserts, rciRouteOp{
					Group:    tr.group,
					Iface:    tr.iface,
					Auto:     true,
					Reject:   tr.fallback == "reject",
					Disabled: tr.disabled,
				})
				continue
			}

			sameShape := cr.fallback == tr.fallback
			if !sameShape {
				diff.routeUpserts = append(diff.routeUpserts, rciRouteOp{
					Group:    tr.group,
					Iface:    tr.iface,
					Auto:     true,
					Reject:   tr.fallback == "reject",
					Disabled: tr.disabled,
				})
				continue
			}

			if cr.disabled != tr.disabled {
				diff.routeDisables = append(diff.routeDisables, rciRouteDisable{
					Index:    cr.index,
					Disabled: tr.disabled,
					Group:    tr.group,
					Iface:    tr.iface,
				})
			}
		}
	}

	// Delete routes for groups that exist on router but have no target routes
	// (skip groups already handled by groupDeletes)
	deletedGroupSet := make(map[string]bool, len(diff.groupDeletes))
	for _, name := range diff.groupDeletes {
		deletedGroupSet[name] = true
	}
	for group, curs := range currentByGroup {
		if _, inTarget := targetByGroup[group]; !inTarget && !deletedGroupSet[group] {
			for _, cr := range curs {
				diff.routeDeletes = append(diff.routeDeletes, rciRouteDelete{
					Group: cr.group, Iface: cr.iface, No: true,
				})
			}
		}
	}

	return diff
}

// applyDiff issues the computed diff to NDMS via the new CQRS command layer.
// Save is handled by the debounced SaveCoordinator inside each command group.
func (s *ServiceImpl) applyDiff(ctx context.Context, diff rciDiff) error {
	// Phase 1: Delete routes (before deleting groups they reference)
	if len(diff.routeDeletes) > 0 {
		specs := make([]command.DNSRouteSpec, 0, len(diff.routeDeletes))
		for _, rd := range diff.routeDeletes {
			specs = append(specs, command.DNSRouteSpec{
				Group:     rd.Group,
				Interface: rd.Iface,
			})
		}
		if err := s.commands.DNSRoutes.DeleteRoutes(ctx, specs); err != nil {
			s.logError("applyDiff", "", fmt.Sprintf("Phase1: DeleteRoutes(%d) failed", len(specs)), err.Error())
			return fmt.Errorf("delete routes: %w", err)
		}
		s.logInfo("applyDiff", "", fmt.Sprintf("Phase1: deleted %d routes OK", len(specs)))
	}

	// Phase 2: Delete groups
	if len(diff.groupDeletes) > 0 {
		if err := s.commands.ObjectGroups.DeleteGroups(ctx, diff.groupDeletes); err != nil {
			s.logError("applyDiff", "", fmt.Sprintf("Phase2: DeleteGroups(%d) failed", len(diff.groupDeletes)), err.Error())
			return fmt.Errorf("delete groups: %w", err)
		}
		s.logInfo("applyDiff", "", fmt.Sprintf("Phase2: deleted %d groups OK", len(diff.groupDeletes)))
	}

	// Phase 3: Create/update groups (incremental domain add/remove)
	// Continue on per-group errors so other groups still get applied.
	var groupErrors []string
	for _, g := range diff.groupUpdates {
		mut := command.FQDNGroupMutation{
			Name:           g.name,
			AddIncludes:    g.addIncludes,
			RemoveIncludes: g.removeIncludes,
			AddExcludes:    g.addExcludes,
			RemoveExcludes: g.removeExcludes,
		}
		if err := s.commands.ObjectGroups.UpsertGroup(ctx, mut); err != nil {
			groupErrors = append(groupErrors, fmt.Sprintf("%s: %v", g.name, err))
			s.log.Warnf("reconcile: group %s update failed: %v", g.name, err)
		}
	}

	// Phase 4: Create/update routes (all in one call, order = priority)
	if len(diff.routeUpserts) > 0 {
		specs := make([]command.DNSRouteSpec, 0, len(diff.routeUpserts))
		for _, ro := range diff.routeUpserts {
			specs = append(specs, command.DNSRouteSpec{
				Group:     ro.Group,
				Interface: ro.Iface,
				Reject:    ro.Reject,
			})
		}
		if err := s.commands.DNSRoutes.UpsertRoutes(ctx, specs); err != nil {
			s.logError("applyDiff", "", fmt.Sprintf("Phase4: UpsertRoutes(%d) failed", len(specs)), err.Error())
			return fmt.Errorf("upsert routes: %w", err)
		}
		s.logInfo("applyDiff", "", fmt.Sprintf("Phase4: upserted %d routes OK", len(specs)))
	}

	// Phase 5a: re-fetch router state so newly-created routes get their
	// freshly-assigned indexes, needed for disable toggles below.
	var needPostFetch bool
	for _, ro := range diff.routeUpserts {
		if ro.Disabled {
			needPostFetch = true
			break
		}
	}

	postDisables := append([]rciRouteDisable{}, diff.routeDisables...)

	if needPostFetch {
		s.queries.DNSProxy.InvalidateAll()
		fresh, err := s.queries.DNSProxy.List(ctx)
		if err != nil {
			s.logError("applyDiff", "", "Phase5a: refetch dns-proxy after upsert", err.Error())
			// Non-fatal: the next reconcile will catch up.
		} else {
			indexByKey := make(map[string]string, len(fresh))
			for _, r := range fresh {
				if r.Index == "" {
					continue
				}
				indexByKey[r.Group+"|"+r.Interface] = r.Index
			}
			for _, ro := range diff.routeUpserts {
				if !ro.Disabled {
					continue
				}
				key := ro.Group + "|" + ro.Iface
				idx, ok := indexByKey[key]
				if !ok || idx == "" {
					s.logError("applyDiff", "", fmt.Sprintf("Phase5a: no index for %s", key), "route not in post-upsert show")
					continue
				}
				postDisables = append(postDisables, rciRouteDisable{
					Index:    idx,
					Disabled: true,
					Group:    ro.Group,
					Iface:    ro.Iface,
				})
			}
		}
	}

	// Phase 5b: apply disable toggles (pre-existing routes + newly-upserted
	// ones whose index we just learned).
	for _, rd := range postDisables {
		if rd.Index == "" {
			continue
		}
		if err := s.commands.DNSRoutes.SetDisabled(ctx, rd.Index, rd.Disabled); err != nil {
			s.logError("applyDiff",
				fmt.Sprintf("%s->%s", rd.Group, rd.Iface),
				fmt.Sprintf("Phase5b: SetDisabled(%v) failed", rd.Disabled),
				err.Error())
			return fmt.Errorf("set disabled %s->%s: %w", rd.Group, rd.Iface, err)
		}
		s.logInfo("applyDiff",
			fmt.Sprintf("%s->%s", rd.Group, rd.Iface),
			fmt.Sprintf("Phase5b: SetDisabled=%v OK (index=%s)", rd.Disabled, rd.Index))
	}

	if len(groupErrors) > 0 {
		return fmt.Errorf("%d group update(s) failed: %s", len(groupErrors), strings.Join(groupErrors, "; "))
	}
	return nil
}

// --- Helper functions ---

// diffStringSlices returns elements to add and remove to go from current to target.
func diffStringSlices(current, target []string) (add, remove []string) {
	curSet := make(map[string]bool, len(current))
	for _, s := range current {
		curSet[strings.ToLower(s)] = true
	}
	tgtSet := make(map[string]bool, len(target))
	for _, s := range target {
		tgtSet[strings.ToLower(s)] = true
	}
	for _, s := range target {
		if !curSet[strings.ToLower(s)] {
			add = append(add, s)
		}
	}
	for _, s := range current {
		if !tgtSet[strings.ToLower(s)] {
			remove = append(remove, s)
		}
	}
	return
}

// routesEqual checks if current routes match target routes (same interfaces, order, fallback).
func routesEqual(current []currentRoute, target []targetRoute) bool {
	if len(current) != len(target) {
		return false
	}
	for i := range current {
		if current[i].group != target[i].group ||
			current[i].iface != target[i].iface ||
			current[i].fallback != target[i].fallback ||
			current[i].disabled != target[i].disabled {
			return false
		}
	}
	return true
}

// buildGroupName generates a human-readable NDMS object-group name.
// Format: AWG_{num}_{sanitized_name}_{chunk}
// Example: list_2 "hetzner" chunk 1 → "AWG_2_hetzner_1"
func buildGroupName(listID, listName string, chunk int) string {
	num := listID
	if strings.HasPrefix(listID, "list_") {
		num = strings.TrimPrefix(listID, "list_")
	}
	name := sanitizeGroupName(listName)
	if name == "" {
		name = num
	}
	return fmt.Sprintf("%s%s_%s_%d", GroupPrefix, num, name, chunk)
}

// maxGroupNamePart is the max length of the sanitized name portion.
const maxGroupNamePart = 20

// sanitizeGroupName transliterates Cyrillic and strips non-alphanumeric characters
// to produce a valid NDMS object-group name component.
func sanitizeGroupName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(name) {
		if tr, ok := cyrTranslit[r]; ok {
			b.WriteString(tr)
		} else if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	s := collapseUnderscores.ReplaceAllString(b.String(), "_")
	s = strings.Trim(s, "_")
	if utf8.RuneCountInString(s) > maxGroupNamePart {
		runes := []rune(s)
		s = string(runes[:maxGroupNamePart])
		s = strings.TrimRight(s, "_")
	}
	return s
}

var collapseUnderscores = regexp.MustCompile(`_+`)

var cyrTranslit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}
