package dnsroute

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
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
}

// --- RCI diff types ---

type rciDiff struct {
	routeDeletes []rciRouteDelete
	groupDeletes []string
	groupUpdates []rciGroupUpdate
	routeUpserts []rciRouteOp
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
		len(d.groupUpdates) == 0 && len(d.routeUpserts) == 0
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

	allGroups, err := s.ndms.ShowObjectGroupFQDN(ctx)
	if err != nil {
		s.logError("reconcile", "", "Failed to read object-groups", err.Error())
		return fmt.Errorf("show object-group fqdn: %w", err)
	}

	allRoutes, err := s.ndms.ShowDnsProxyRoute(ctx)
	if err != nil {
		s.logError("reconcile", "", "Failed to read dns-proxy routes", err.Error())
		return fmt.Errorf("show dns-proxy route: %w", err)
	}

	current := filterAWGState(allGroups, allRoutes)

	diff := computeDiff(current, target)
	if diff.isEmpty() {
		return nil
	}

	s.logInfo("reconcile", "", fmt.Sprintf("Reconciling: %d group deletes, %d group updates, %d route deletes, %d route upserts",
		len(diff.groupDeletes), len(diff.groupUpdates), len(diff.routeDeletes), len(diff.routeUpserts)))

	applyErr := s.applyDiff(ctx, diff)
	if applyErr != nil {
		s.logError("reconcile", "", "Partial apply failure", applyErr.Error())
	}

	// Always save — even on partial failure some operations succeeded
	// and must be persisted to running-config.
	if err := s.ndms.Save(ctx); err != nil {
		s.logError("reconcile", "", "Failed to save config", err.Error())
		return fmt.Errorf("save config: %w", err)
	}

	if applyErr != nil {
		return fmt.Errorf("apply diff: %w", applyErr)
	}

	s.logInfo("reconcile", "", "Reconcile complete")
	return nil
}

// buildTargetState converts stored domain lists into the desired router state.
func buildTargetState(data *StoreData, failedTunnels map[string]struct{}) targetState {
	var ts targetState

	for _, list := range data.Lists {
		if !list.Enabled {
			continue
		}
		if len(list.Domains) == 0 && len(list.Subnets) == 0 {
			continue
		}

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
func filterAWGState(groups []ndms.ObjectGroupFQDN, routes []ndms.DnsProxyRoute) currentState {
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

	// For each target group: compare routes, delete removed, upsert if changed
	for group, tgts := range targetByGroup {
		curs := currentByGroup[group]

		if routesEqual(curs, tgts) {
			continue
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

		// Upsert all target routes (NDMS creates or updates)
		for _, tr := range tgts {
			diff.routeUpserts = append(diff.routeUpserts, rciRouteOp{
				Group:  tr.group,
				Iface:  tr.iface,
				Auto:   true,
				Reject: tr.fallback == "reject",
			})
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

// applyDiff sends JSON RCI payloads to apply the computed diff.
func (s *ServiceImpl) applyDiff(ctx context.Context, diff rciDiff) error {
	// Phase 1: Delete routes (before deleting groups they reference)
	if len(diff.routeDeletes) > 0 {
		payload := map[string]interface{}{
			"dns-proxy": map[string]interface{}{
				"route": diff.routeDeletes,
			},
		}
		if _, err := s.ndms.RCIPost(ctx, payload); err != nil {
			return fmt.Errorf("delete routes: %w", err)
		}
	}

	// Phase 2: Delete groups
	if len(diff.groupDeletes) > 0 {
		fqdnPayload := make(map[string]interface{})
		for _, name := range diff.groupDeletes {
			fqdnPayload[name] = map[string]interface{}{"no": true}
		}
		payload := map[string]interface{}{
			"object-group": map[string]interface{}{
				"fqdn": fqdnPayload,
			},
		}
		if _, err := s.ndms.RCIPost(ctx, payload); err != nil {
			return fmt.Errorf("delete groups: %w", err)
		}
	}

	// Phase 3: Create/update groups (incremental domain add/remove)
	// Continue on per-group errors so other groups still get applied.
	var groupErrors []string
	for _, g := range diff.groupUpdates {
		groupBody := make(map[string]interface{})

		var includeOps []map[string]interface{}
		for _, d := range g.removeIncludes {
			includeOps = append(includeOps, map[string]interface{}{"address": d, "no": true})
		}
		for _, d := range g.addIncludes {
			includeOps = append(includeOps, map[string]interface{}{"address": d})
		}
		if len(includeOps) > 0 {
			groupBody["include"] = includeOps
		}

		var excludeOps []map[string]interface{}
		for _, d := range g.removeExcludes {
			excludeOps = append(excludeOps, map[string]interface{}{"address": d, "no": true})
		}
		for _, d := range g.addExcludes {
			excludeOps = append(excludeOps, map[string]interface{}{"address": d})
		}
		if len(excludeOps) > 0 {
			groupBody["exclude"] = excludeOps
		}

		if len(groupBody) == 0 {
			continue
		}

		payload := map[string]interface{}{
			"object-group": map[string]interface{}{
				"fqdn": map[string]interface{}{
					g.name: groupBody,
				},
			},
		}
		if _, err := s.ndms.RCIPost(ctx, payload); err != nil {
			groupErrors = append(groupErrors, fmt.Sprintf("%s: %v", g.name, err))
			s.log.Warnf("reconcile: group %s update failed: %v", g.name, err)
		}
	}

	// Phase 4: Create/update routes (all in one call, order = priority)
	if len(diff.routeUpserts) > 0 {
		payload := map[string]interface{}{
			"dns-proxy": map[string]interface{}{
				"route": diff.routeUpserts,
			},
		}
		if _, err := s.ndms.RCIPost(ctx, payload); err != nil {
			return fmt.Errorf("upsert routes: %w", err)
		}
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
			current[i].fallback != target[i].fallback {
			return false
		}
	}
	return true
}

// groupDataEqual checks if current router state matches the target group.
func groupDataEqual(current currentGroupData, target targetGroup) bool {
	// On the router, subnets appear as regular entries alongside domains.
	allIncludes := target.includes
	if len(target.subnets) > 0 {
		allIncludes = make([]string, 0, len(target.includes)+len(target.subnets))
		allIncludes = append(allIncludes, target.includes...)
		allIncludes = append(allIncludes, target.subnets...)
	}
	return domainsEqual(current.includes, allIncludes) &&
		domainsEqual(current.excludes, target.excludes)
}

// domainsEqual checks if two domain slices contain the same elements (order-insensitive, case-insensitive).
func domainsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, d := range a {
		set[strings.ToLower(d)]++
	}
	for _, d := range b {
		set[strings.ToLower(d)]--
		if set[strings.ToLower(d)] < 0 {
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
