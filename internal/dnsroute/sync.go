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

// reconcile is the main entry point: reads desired and actual state, computes diff, applies.
func (s *ServiceImpl) reconcile(ctx context.Context) error {
	data := s.store.GetCached()
	if data == nil {
		return nil
	}

	target := buildTargetState(data)

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

	phases := computeDiff(current, target)

	totalCmds := 0
	for _, p := range phases {
		totalCmds += len(p)
	}
	if totalCmds == 0 {
		return nil
	}

	s.logInfo("reconcile", "", fmt.Sprintf("Applying %d commands in %d phases", totalCmds, len(phases)))

	if err := s.applyCommands(ctx, phases); err != nil {
		s.logError("reconcile", "", "Failed to apply commands", err.Error())
		return fmt.Errorf("apply commands: %w", err)
	}

	if err := s.ndms.Save(ctx); err != nil {
		s.logError("reconcile", "", "Failed to save config", err.Error())
		return fmt.Errorf("save config: %w", err)
	}

	s.logInfo("reconcile", "", fmt.Sprintf("Applied %d commands in %d phases", totalCmds, len(phases)))
	return nil
}

// buildTargetState converts stored domain lists into the desired router state.
func buildTargetState(data *StoreData) targetState {
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

			// Every group gets the same route targets.
			// Fallback (reject/auto) only applies to the LAST route in the chain.
			for j, rt := range list.Routes {
				fallback := ""
				if j == len(list.Routes)-1 {
					fallback = rt.Fallback
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

// computeDiff returns phases of context-based CLI commands that transform current into target.
// Each phase is a self-contained RCI batch (maintains its own CLI context).
// Order: delete stale routes → delete stale groups → create groups → create routes.
func computeDiff(current currentState, target targetState) [][]string {
	var phases [][]string

	// Build lookup sets for target.
	targetGroupSet := make(map[string]*targetGroup, len(target.groups))
	for i := range target.groups {
		targetGroupSet[target.groups[i].name] = &target.groups[i]
	}

	type routeKey struct {
		group    string
		iface    string
		fallback string
	}
	targetRouteSet := make(map[routeKey]bool, len(target.routes))
	for _, r := range target.routes {
		targetRouteSet[routeKey{group: r.group, iface: r.iface, fallback: r.fallback}] = true
	}

	// Step 1: Delete routes that exist on router but not in target.
	// All route deletes go in one dns-proxy context.
	var routeDeletes []string
	for _, cr := range current.routes {
		key := routeKey{group: cr.group, iface: cr.iface, fallback: cr.fallback}
		if !targetRouteSet[key] {
			routeDeletes = append(routeDeletes, fmt.Sprintf("no route object-group %s %s", cr.group, cr.iface))
		}
	}
	if len(routeDeletes) > 0 {
		phase := []string{"dns-proxy"}
		phase = append(phase, routeDeletes...)
		phase = append(phase, "exit")
		phases = append(phases, phase)
	}

	// Step 2: Delete groups that should not exist or have different contents.
	// Track deleted groups so we know to recreate their routes in step 4.
	deletedGroups := make(map[string]bool)

	var currentGroupNames []string
	for name := range current.groups {
		currentGroupNames = append(currentGroupNames, name)
	}
	sort.Strings(currentGroupNames)

	var groupDeletes []string
	for _, name := range currentGroupNames {
		tg, wantExists := targetGroupSet[name]
		if !wantExists {
			groupDeletes = append(groupDeletes, fmt.Sprintf("no object-group fqdn %s", name))
			deletedGroups[name] = true
			continue
		}
		if !groupDataEqual(current.groups[name], *tg) {
			groupDeletes = append(groupDeletes, fmt.Sprintf("no object-group fqdn %s", name))
			deletedGroups[name] = true
		}
	}
	if len(groupDeletes) > 0 {
		phases = append(phases, groupDeletes)
	}

	// Step 3: Create/recreate groups.
	// Each group is its own context block (enter → includes → excludes → exit).
	for _, tg := range target.groups {
		if data, exists := current.groups[tg.name]; exists && groupDataEqual(data, tg) {
			continue // Already correct on router.
		}

		phase := []string{fmt.Sprintf("object-group fqdn %s", tg.name)}
		for _, domain := range tg.includes {
			phase = append(phase, fmt.Sprintf("include %s", domain))
		}
		for _, subnet := range tg.subnets {
			phase = append(phase, fmt.Sprintf("include %s", subnet))
		}
		for _, domain := range tg.excludes {
			phase = append(phase, fmt.Sprintf("exclude %s", domain))
		}
		phase = append(phase, "exit")
		phases = append(phases, phase)
	}

	// Step 4: Create routes that are new, whose group was recreated, or whose order changed.
	// NDMS route priority is determined by creation order, so we must detect order changes.
	// All route creates go in one dns-proxy context.
	currentRouteSet := make(map[routeKey]bool, len(current.routes))
	for _, cr := range current.routes {
		currentRouteSet[routeKey{group: cr.group, iface: cr.iface, fallback: cr.fallback}] = true
	}

	// Detect order changes: group routes by group name and compare sequence.
	// If the set of routes for a group is the same but order differs, delete all and recreate.
	type groupedRoute struct {
		iface    string
		fallback string
	}
	currentByGroup := make(map[string][]groupedRoute)
	for _, cr := range current.routes {
		currentByGroup[cr.group] = append(currentByGroup[cr.group], groupedRoute{cr.iface, cr.fallback})
	}
	targetByGroup := make(map[string][]groupedRoute)
	for _, tr := range target.routes {
		targetByGroup[tr.group] = append(targetByGroup[tr.group], groupedRoute{tr.iface, tr.fallback})
	}

	reorderGroups := make(map[string]bool)
	for group, targetSeq := range targetByGroup {
		currentSeq, exists := currentByGroup[group]
		if !exists || len(currentSeq) != len(targetSeq) {
			continue // New or changed routes — handled by normal create logic.
		}
		// Same length — check if sets match but order differs.
		sameOrder := true
		for i := range targetSeq {
			if targetSeq[i] != currentSeq[i] {
				sameOrder = false
				break
			}
		}
		if sameOrder {
			continue // Order is correct.
		}
		// Check if same set of routes (just reordered).
		currentSet := make(map[groupedRoute]int)
		for _, r := range currentSeq {
			currentSet[r]++
		}
		targetSet := make(map[groupedRoute]int)
		for _, r := range targetSeq {
			targetSet[r]++
		}
		sameSet := len(currentSet) == len(targetSet)
		if sameSet {
			for k, v := range currentSet {
				if targetSet[k] != v {
					sameSet = false
					break
				}
			}
		}
		if sameSet {
			reorderGroups[group] = true
		}
	}

	// Delete routes for reordered groups first.
	if len(reorderGroups) > 0 {
		var reorderDeletes []string
		for _, cr := range current.routes {
			if reorderGroups[cr.group] {
				reorderDeletes = append(reorderDeletes, fmt.Sprintf("no route object-group %s %s", cr.group, cr.iface))
			}
		}
		if len(reorderDeletes) > 0 {
			phase := []string{"dns-proxy"}
			phase = append(phase, reorderDeletes...)
			phase = append(phase, "exit")
			phases = append(phases, phase)
		}
	}

	var routeCreates []string
	for _, tr := range target.routes {
		key := routeKey{group: tr.group, iface: tr.iface, fallback: tr.fallback}
		if !currentRouteSet[key] || deletedGroups[tr.group] || reorderGroups[tr.group] {
			cmd := fmt.Sprintf("route object-group %s %s auto", tr.group, tr.iface)
			if tr.fallback == "reject" {
				cmd += " reject"
			}
			routeCreates = append(routeCreates, cmd)
		}
	}
	if len(routeCreates) > 0 {
		phase := []string{"dns-proxy"}
		phase = append(phase, routeCreates...)
		phase = append(phase, "exit")
		phases = append(phases, phase)
	}

	return phases
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

// domainsEqual checks if two domain slices contain the same elements (order-insensitive).
func domainsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, d := range a {
		set[d]++
	}
	for _, d := range b {
		set[d]--
		if set[d] < 0 {
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
		s = s[:maxGroupNamePart]
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

// applyCommands sends command phases to the router via RCI POST.
// Each phase is sent as a single RCI batch to preserve CLI context.
func (s *ServiceImpl) applyCommands(ctx context.Context, phases [][]string) error {
	for i, phase := range phases {
		payload := make([]map[string]string, len(phase))
		for j, cmd := range phase {
			payload[j] = map[string]string{"parse": cmd}
		}

		if _, err := s.ndms.RCIPost(ctx, payload); err != nil {
			return fmt.Errorf("rci phase %d: %w", i, err)
		}
	}
	return nil
}
