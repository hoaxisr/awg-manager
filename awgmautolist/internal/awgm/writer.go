package awgm

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// ruleSetTag is the single inline rule-set that carries every autolist
// destination. One set, one unconditional owner rule.
const ruleSetTag = "awgm-autolist"

// targetOutboundTag is the writer's own direct outbound for a bindable
// kernel interface. Managed AWG/WG/NWG tunnels, standalone sing-box tunnels
// and enabled subscription (group) selectors use their existing catalog
// outbound instead; no other group is ever substituted.
const targetOutboundTag = "awgm-autolist-target"

// Writer implements engine.Writer against the AWGM singbox-router API.
// Every Add/Remove is a serialized read-modify-write guarded by mu, so
// concurrent calls cannot lose a rule-set update.
type Writer struct {
	client *Client
	mu     sync.Mutex
}

// NewWriter returns a Writer on top of the shared AWGM client.
func NewWriter(client *Client) *Writer { return &Writer{client: client} }

// WriteResult reports what a call actually changed. Outbound is the
// destination selected by the interface mapping. Persisted is true only
// when this call wrote at least one route object (rule-set entry, owner
// rule or target outbound) confirmed by a readback; duplicates and
// healthy repeats are no-ops with Persisted=false.
type WriteResult struct {
	Outbound string
	// Persisted reports any confirmed route-object write, including owner or
	// outbound repair. NewEntry is true only for a newly added destination;
	// the rolling 50/hour budget is spent from NewEntry, never from repairs.
	Persisted bool
	NewEntry  bool
}

// Add routes target through the outbound mapped from targetInterface.
func (w *Writer) Add(ctx context.Context, target model.Target, targetInterface string) (WriteResult, error) {
	return w.write(ctx, target, targetInterface, false)
}

// Remove undoes exactly what Add wrote for target: the destination entry in
// the owned unconditional rule-set object. Foreign, conditional and narrowed
// objects are preserved, and the owner rule is touched only when the
// rule-set itself must be deleted (an inline set cannot be empty).
func (w *Writer) Remove(ctx context.Context, target model.Target, targetInterface string) (WriteResult, error) {
	return w.write(ctx, target, targetInterface, true)
}

func (w *Writer) write(ctx context.Context, target model.Target, targetInterface string, remove bool) (WriteResult, error) {
	if w == nil || w.client == nil {
		return WriteResult{}, errors.New("awgm writer has no client")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}

	destination, err := routeDestination(target)
	if err != nil {
		return WriteResult{}, err
	}
	if err := validateInterface(targetInterface); err != nil {
		return WriteResult{}, err
	}

	// Preflight before ANY mutation: the owner rule must currently be
	// absent or exactly one unconditional rule_set=[awgm-autolist] rule.
	ownerOutbound, err := w.preflightOwnerRule(ctx)
	if err != nil {
		return WriteResult{}, err
	}

	result := WriteResult{}
	outbound := ownerOutbound
	if remove {
		// Removal never depends on a tunnel still being up or even present in
		// the catalog: the current owner already carries the effective outbound.
		result.Outbound = ownerOutbound
	} else {
		var outboundMutated bool
		outbound, outboundMutated, err = w.ensureTargetOutbound(ctx, targetInterface)
		if err != nil {
			return result, fmt.Errorf("target outbound: %w", err)
		}
		result.Outbound = outbound
		result.Persisted = outboundMutated
	}

	rsMutated, err := w.ensureRuleSetEntry(ctx, destination, remove)
	if err != nil {
		return result, err
	}
	result.Persisted = result.Persisted || rsMutated
	result.NewEntry = rsMutated && !remove

	if !remove {
		ownerMutated, err := w.ensureOwnerRule(ctx, outbound)
		if err != nil {
			return result, err
		}
		result.Persisted = result.Persisted || ownerMutated
	}

	if result.Persisted {
		// One staging apply after all required mutations. NO_DRAFT is not
		// accepted on its own; the readback below proves the state.
		if err := w.applyStaging(ctx); err != nil {
			return result, fmt.Errorf("route objects written (as draft), but staging apply failed: %w", err)
		}
	}
	// Readback after success and after NO_DRAFT: only the observed server
	// state proves the outcome (AWGM edits are last-write-wins).
	if err := w.verifyDestination(ctx, destination, outbound, remove); err != nil {
		return result, err
	}
	return result, nil
}

// routeDestination returns the single rule-set entry for the target: the
// registered zone as domain_suffix, or the whole address as ip_cidr with
// /32 or /128 and no port or network. The port and protocol of the
// observation are deliberately ignored: an IP-only decision covers every
// port, and a domain rule must not be narrower than the zone.
func routeDestination(target model.Target) (string, error) {
	zone := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(target.Domain), "."))
	if zone != "" {
		if _, err := netip.ParseAddr(zone); err == nil {
			return "", fmt.Errorf("target domain %q is an IP literal", zone)
		}
		return domainSuffixDestination(zone)
	}
	ip := strings.TrimSpace(target.IP)
	if ip == "" {
		return "", errors.New("target has neither domain nor IP")
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", fmt.Errorf("invalid observed IP %q: %w", ip, err)
	}
	return addr.String() + "/" + strconv.Itoa(addr.BitLen()), nil
}

// domainSuffixDestination validates one lower-case DNS name.
func domainSuffixDestination(zone string) (string, error) {
	if zone == "" {
		return "", errors.New("empty domain")
	}
	for _, label := range strings.Split(zone, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("invalid domain %q", zone)
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", fmt.Errorf("invalid domain %q", zone)
			}
		}
	}
	return zone, nil
}

func validateInterface(iface string) error {
	if iface == "" || strings.ContainsAny(iface, " \t\n") {
		return fmt.Errorf("target interface %q is empty or invalid", iface)
	}
	return nil
}

// ownerKeys are the only fields of an unconditional owner rule. Any other
// key narrows the rule (source, port, managed marker, ...) and disqualifies
// it: such rules are never claimed, updated or counted.
var ownerKeys = map[string]bool{"rule_set": true, "action": true, "outbound": true}

// isOwnedRuleRaw reports whether a raw rule from rules/list is exactly the
// writer's unconditional owner: rule_set=[awgm-autolist], a route action, a
// non-empty outbound and no other fields. Checked on the raw object, so a
// narrowing field the DTO does not know about still disqualifies the rule.
func isOwnedRuleRaw(rule map[string]any) bool {
	if len(rule) > len(ownerKeys) {
		return false
	}
	for key := range rule {
		if !ownerKeys[key] {
			return false
		}
	}
	tags, ok := rule["rule_set"].([]any)
	if !ok || len(tags) != 1 || tags[0] != ruleSetTag {
		return false
	}
	switch action := rule["action"].(type) {
	case nil, string:
		if action != nil && action != "" && action != "route" {
			return false
		}
	default:
		return false
	}
	outbound, _ := rule["outbound"].(string)
	return outbound != ""
}

// listRawRules reads rules/list preserving every field of every rule.
func (w *Writer) listRawRules(ctx context.Context) ([]map[string]any, error) {
	var rules []map[string]any
	if err := w.client.get(ctx, "/api/singbox/router/rules/list", &rules); err != nil {
		return nil, fmt.Errorf("rules list: %w", err)
	}
	return rules, nil
}

// scanOwners returns the indexes of the writer's unconditional owner rules.
func scanOwners(rules []map[string]any) []int {
	var owned []int
	for i, rule := range rules {
		if isOwnedRuleRaw(rule) {
			owned = append(owned, i)
		}
	}
	return owned
}

// preflightOwnerRule rejects owner ambiguity before any mutation happens.
func (w *Writer) preflightOwnerRule(ctx context.Context) (string, error) {
	rules, err := w.listRawRules(ctx)
	if err != nil {
		return "", err
	}
	owners := scanOwners(rules)
	if len(owners) > 1 {
		return "", fmt.Errorf("preflight: %d unconditional %s owner rules; refusing to pick one", len(owners), ruleSetTag)
	}
	if len(owners) == 1 {
		outbound, _ := rules[owners[0]]["outbound"].(string)
		return outbound, nil
	}
	return "", nil
}

// rawRuleSet is the current rule-set awgm-autolist as stored, including
// fields the writer does not model.
type rawRuleSet struct {
	set   map[string]any // nil when absent
	rules []any          // raw["rules"]; nil when absent or empty
}

// existingRuleSet reads the inline rule-set awgm-autolist. exists=false
// when it is absent; any other type is an error.
func (w *Writer) existingRuleSet(ctx context.Context) (rawRuleSet, bool, error) {
	var sets []map[string]any
	if err := w.client.get(ctx, "/api/singbox/router/rulesets/list", &sets); err != nil {
		return rawRuleSet{}, false, fmt.Errorf("rulesets list: %w", err)
	}
	for _, set := range sets {
		if set["tag"] != ruleSetTag {
			continue
		}
		if set["type"] != "inline" {
			return rawRuleSet{}, true, fmt.Errorf("rule-set %q has type %v, want inline", ruleSetTag, set["type"])
		}
		raw := rawRuleSet{set: set}
		if rules, ok := set["rules"].([]any); ok {
			raw.rules = rules
		}
		return raw, true, nil
	}
	return rawRuleSet{}, false, nil
}

// ownedObject is the writer's own unconditional single-key destination
// object inside the rule-set: exactly one domain_suffix or ip_cidr list.
type ownedObject struct {
	index        int
	destinations []string
}

// keyForDestination returns the inline rule key holding the destination.
func keyForDestination(destination string) string {
	if strings.Contains(destination, "/") {
		return "ip_cidr"
	}
	return "domain_suffix"
}

// findOwnedObject returns the first unconditional object of key holding
// raw destination lists. Later objects of the same key are left untouched:
// the writer does not guess which of them is its own.
func findOwnedObject(rules []any, key string) ownedObject {
	object := ownedObject{index: -1}
	for i, rawRule := range rules {
		rule, ok := rawRule.(map[string]any)
		if !ok || len(rule) != 1 {
			continue
		}
		list, ok := rule[key].([]any)
		if !ok || len(list) == 0 {
			continue
		}
		entries := make([]string, 0, len(list))
		pure := true
		for _, entry := range list {
			s, isString := entry.(string)
			if !isString {
				pure = false
				break
			}
			entries = append(entries, s)
		}
		if !pure {
			continue
		}
		if object.index >= 0 {
			break
		}
		object.index = i
		object.destinations = entries
	}
	return object
}

// containsFold reports whether list holds destination.
func containsFold(list []string, destination string) bool {
	for _, entry := range list {
		if strings.EqualFold(entry, destination) {
			return true
		}
	}
	return false
}

// anyUnconditionalContains reports whether any unconditional object of key
// holds destination. Used by the readback, which must fail closed when the
// destination is still routed unconditionally by an unclaimed object.
func anyUnconditionalContains(rules []any, key, destination string) bool {
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]any)
		if !ok || len(rule) != 1 {
			continue
		}
		list, ok := rule[key].([]any)
		if !ok {
			continue
		}
		for _, entry := range list {
			if s, isString := entry.(string); isString && strings.EqualFold(s, destination) {
				return true
			}
		}
	}
	return false
}

// ensureRuleSetEntry adds destination to (or removes it from) the single
// inline rule-set awgm-autolist, preserving every other object and unknown
// field. It reports whether a write happened.
func (w *Writer) ensureRuleSetEntry(ctx context.Context, destination string, remove bool) (bool, error) {
	raw, exists, err := w.existingRuleSet(ctx)
	if err != nil {
		return false, err
	}
	key := keyForDestination(destination)
	if !exists {
		if remove {
			return false, nil
		}
		payload := map[string]any{
			"tag":   ruleSetTag,
			"type":  "inline",
			"rules": []any{map[string]any{key: []any{destination}}},
		}
		if err := w.client.post(ctx, "/api/singbox/router/rulesets/add", payload, nil); err != nil {
			return false, fmt.Errorf("ruleset create: %w", err)
		}
		return true, nil
	}

	object := findOwnedObject(raw.rules, key)
	if remove {
		if object.index < 0 || !containsFold(object.destinations, destination) {
			return false, nil
		}
		return w.removeRuleSetEntry(ctx, raw, object, key, destination)
	}

	if object.index >= 0 && containsFold(object.destinations, destination) {
		return false, nil
	}
	var rules []any
	if object.index >= 0 {
		list := append(append([]any{}, raw.rules[object.index].(map[string]any)[key].([]any)...), destination)
		sort.Slice(list, func(i, j int) bool { return fmt.Sprint(list[i]) < fmt.Sprint(list[j]) })
		// Replace the owned object in place: it holds exactly one key, and
		// every other object is carried over untouched.
		rules = append(append([]any{}, raw.rules...), nil)
		rules[object.index] = map[string]any{key: list}
		rules = rules[:len(raw.rules)]
	} else {
		rules = append(append([]any{}, raw.rules...), map[string]any{key: []any{destination}})
	}
	if err := w.updateRuleSet(ctx, raw.set, rules); err != nil {
		return false, err
	}
	return true, nil
}

// removeRuleSetEntry drops destination from the owned object. An object
// with several destinations keeps the rest; an object that would become
// empty is removed, and the rule-set is deleted when nothing is left —
// an inline set cannot be empty. Deleting the set also removes the
// writer's own owner rule first (never a foreign referencing rule), so
// no referencing rule is orphaned.
func (w *Writer) removeRuleSetEntry(ctx context.Context, raw rawRuleSet, object ownedObject, key, destination string) (bool, error) {
	var kept []any
	if len(object.destinations) > 1 {
		kept = make([]any, 0, len(object.destinations)-1)
		for _, entry := range object.destinations {
			if !strings.EqualFold(entry, destination) {
				kept = append(kept, entry)
			}
		}
	}
	rules := make([]any, 0, len(raw.rules))
	for i, rule := range raw.rules {
		if i == object.index {
			if len(kept) > 0 {
				rules = append(rules, map[string]any{key: kept})
			}
			continue
		}
		rules = append(rules, rule)
	}
	if len(rules) > 0 {
		if err := w.updateRuleSet(ctx, raw.set, rules); err != nil {
			return false, err
		}
		return true, nil
	}
	// The writer's owned object was the only one left. Remove the owner
	// rule first so nothing references the set, then delete the set.
	if err := w.removeOwnerRule(ctx); err != nil {
		return false, err
	}
	if err := w.deleteRuleSet(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// removeOwnerRule deletes the writer's own unconditional owner rule. It
// re-reads the rules and refuses ambiguity; foreign, narrowed or shared
// references to the rule-set are left alone.
func (w *Writer) removeOwnerRule(ctx context.Context) error {
	rules, err := w.listRawRules(ctx)
	if err != nil {
		return err
	}
	owners := scanOwners(rules)
	switch len(owners) {
	case 0:
		return nil
	case 1:
		payload := map[string]any{"index": owners[0]}
		if err := w.client.post(ctx, "/api/singbox/router/rules/delete", payload, nil); err != nil {
			return fmt.Errorf("owner rule delete: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("%d unconditional %s owner rules; refusing to pick one", len(owners), ruleSetTag)
	}
}

func (w *Writer) deleteRuleSet(ctx context.Context) error {
	payload := map[string]any{"tag": ruleSetTag}
	if err := w.client.post(ctx, "/api/singbox/router/rulesets/delete", payload, nil); err != nil {
		return fmt.Errorf("ruleset delete: %w", err)
	}
	return nil
}

func (w *Writer) updateRuleSet(ctx context.Context, set map[string]any, rules []any) error {
	next := make(map[string]any, len(set)+1)
	for k, v := range set {
		next[k] = v
	}
	next["tag"] = ruleSetTag
	next["type"] = "inline"
	next["rules"] = rules
	payload := map[string]any{"tag": ruleSetTag, "ruleSet": next}
	if err := w.client.post(ctx, "/api/singbox/router/rulesets/update", payload, nil); err != nil {
		return fmt.Errorf("ruleset update: %w", err)
	}
	return nil
}

// ensureTargetOutbound maps targetInterface to the exact outbound the owner
// rule must use on Add. A bindable kernel interface gets the writer's own direct
// outbound (created or updated). All other interfaces need exactly one enabled
// mapping in TunnelCatalog;
// zero, ambiguous or disabled mappings are errors, and no arbitrary group
// is ever substituted.
func (w *Writer) ensureTargetOutbound(ctx context.Context, iface string) (string, bool, error) {
	bindable, err := w.client.BindableInterfaces(ctx)
	if err != nil {
		return "", false, fmt.Errorf("bindable interfaces: %w", err)
	}
	for _, name := range bindable {
		if name == iface {
			return w.ensureOwnDirectOutbound(ctx, iface)
		}
	}

	catalog, err := w.client.TunnelCatalog(ctx)
	if err != nil {
		return "", false, err
	}
	var matches, enabled []Tunnel
	for _, tunnel := range catalog {
		if tunnel.Interface != iface {
			continue
		}
		matches = append(matches, tunnel)
		if tunnel.Enabled && tunnel.Outbound != "" && tunnel.Interface != "" {
			enabled = append(enabled, tunnel)
		}
	}
	switch {
	case len(enabled) == 1:
		return enabled[0].Outbound, false, nil
	case len(enabled) > 1:
		tags := make([]string, 0, len(enabled))
		for _, tunnel := range enabled {
			tags = append(tags, tunnel.Outbound)
		}
		return "", false, fmt.Errorf("multiple exact outbounds for interface %q: %s", iface, strings.Join(tags, ", "))
	case len(matches) > 0:
		return "", false, fmt.Errorf("tunnel for interface %q is disabled or unmapped", iface)
	default:
		return "", false, fmt.Errorf("no exact managed outbound for interface %q", iface)
	}
}

// ensureOwnDirectOutbound creates or updates the writer's own direct
// outbound bound to iface and reports whether a write happened.
func (w *Writer) ensureOwnDirectOutbound(ctx context.Context, iface string) (string, bool, error) {
	outbounds, err := w.client.Outbounds(ctx)
	if err != nil {
		return "", false, fmt.Errorf("outbounds list: %w", err)
	}
	want := Outbound{Type: "direct", Tag: targetOutboundTag, BindInterface: iface}
	for _, outbound := range outbounds {
		if outbound.Tag != targetOutboundTag {
			continue
		}
		if !strings.EqualFold(outbound.Type, "direct") {
			return "", false, fmt.Errorf("outbound %q is occupied by type %q", targetOutboundTag, outbound.Type)
		}
		if outbound.BindInterface == iface {
			return targetOutboundTag, false, nil
		}
		payload := map[string]any{"tag": targetOutboundTag, "outbound": want}
		if err := w.client.post(ctx, "/api/singbox/router/outbounds/update", payload, nil); err != nil {
			return "", false, fmt.Errorf("outbound update: %w", err)
		}
		return targetOutboundTag, true, nil
	}
	if err := w.client.post(ctx, "/api/singbox/router/outbounds/add", want, nil); err != nil {
		return "", false, fmt.Errorf("outbound add: %w", err)
	}
	return targetOutboundTag, true, nil
}

// ensureOwnerRule creates or updates the single unconditional owner rule
// rule_set=[awgm-autolist] → outbound. It re-reads the rules list and
// refuses ambiguity again after the read-modify-write gap. Foreign,
// narrowed or shared references to the rule-set are never claimed.
func (w *Writer) ensureOwnerRule(ctx context.Context, outbound string) (bool, error) {
	rules, err := w.listRawRules(ctx)
	if err != nil {
		return false, err
	}
	owners := scanOwners(rules)
	switch {
	case len(owners) > 1:
		return false, fmt.Errorf("%d unconditional %s owner rules; refusing to pick one", len(owners), ruleSetTag)
	case len(owners) == 1:
		index := owners[0]
		if rules[index]["outbound"] == outbound {
			return false, nil
		}
		rule := map[string]any{"rule_set": []any{ruleSetTag}, "action": "route", "outbound": outbound}
		payload := map[string]any{"index": index, "rule": rule}
		if err := w.client.post(ctx, "/api/singbox/router/rules/update", payload, nil); err != nil {
			return false, fmt.Errorf("owner rule update: %w", err)
		}
		return true, nil
	default:
		payload := Rule{RuleSet: []string{ruleSetTag}, Action: "route", Outbound: outbound}
		if err := w.client.post(ctx, "/api/singbox/router/rules/add", payload, nil); err != nil {
			return false, fmt.Errorf("owner rule add: %w", err)
		}
		return true, nil
	}
}

// applyStaging applies the router draft.
func (w *Writer) applyStaging(ctx context.Context) error {
	if err := w.client.post(ctx, "/api/singbox/router/staging/apply", map[string]any{}, nil); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == "NO_DRAFT" {
			return nil
		}
		return fmt.Errorf("staging apply: %w", err)
	}
	return nil
}

// verifyDestination reads the rules and rule-set back and proves the
// expected end state: for Add, exactly one unconditional owner rule with
// the mapped outbound and the destination present; for Remove, the
// destination absent from every unconditional object of the same key.
func (w *Writer) verifyDestination(ctx context.Context, destination, outbound string, remove bool) error {
	rules, err := w.listRawRules(ctx)
	if err != nil {
		return fmt.Errorf("readback rules: %w", err)
	}
	owners := scanOwners(rules)
	if remove {
		raw, exists, err := w.existingRuleSet(ctx)
		if err != nil {
			return fmt.Errorf("readback rulesets: %w", err)
		}
		if !exists {
			if len(owners) > 0 {
				return fmt.Errorf("readback: %s owner rule remains without its rule-set", ruleSetTag)
			}
			return nil
		}
		if anyUnconditionalContains(raw.rules, keyForDestination(destination), destination) {
			return fmt.Errorf("readback: destination %q still present in %s", destination, ruleSetTag)
		}
		return nil
	}

	if len(owners) != 1 {
		return fmt.Errorf("readback: %d unconditional %s owner rules, want exactly 1", len(owners), ruleSetTag)
	}
	if owners[0] < 0 || owners[0] >= len(rules) || rules[owners[0]]["outbound"] != outbound {
		return fmt.Errorf("readback: owner outbound %v, want %q", rules[owners[0]]["outbound"], outbound)
	}
	raw, exists, err := w.existingRuleSet(ctx)
	if err != nil {
		return fmt.Errorf("readback rulesets: %w", err)
	}
	if !exists {
		return fmt.Errorf("readback: rule-set %s is missing", ruleSetTag)
	}
	if !anyUnconditionalContains(raw.rules, keyForDestination(destination), destination) {
		return fmt.Errorf("readback: destination %q is missing from %s", destination, ruleSetTag)
	}
	return nil
}
