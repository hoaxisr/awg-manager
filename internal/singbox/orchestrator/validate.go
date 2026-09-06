package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ValidationError describes one cross-slot consistency problem.
// Slot is the slot whose JSON contained the offending construct;
// References (when set) names what was referenced.
type ValidationError struct {
	Slot Slot
	Kind string // "duplicate-outbound" / "duplicate-inbound" / "duplicate-dns" / "unknown-outbound" / "unknown-rule-set" / "unknown-dns-server" / "unknown-http-client" / "dns-final-conflict" / "route-final-conflict"
	// Severity is "" (error, blocks reload) or SeverityWarning (advisory,
	// does not block). Defaults to error so existing entries keep blocking.
	Severity string
	Tag      string // the offending tag value
	InRule   string // optional: human-readable location (e.g. "rules[3]" or "selector default")
	Message  string

	// OutboundSlot / OutboundIndex attribute a "sing-box check" failure to
	// a specific outbound: the slot whose file declares it and the index
	// within THAT file's outbounds array. sing-box reports initialize
	// errors with an index into the merged outbounds array (config.d files
	// concatenated in lexical filename order) and decode errors with a
	// per-file index — checkMergedLocked translates both, because only the
	// orchestrator knows the snapshot composition. OutboundIndex nil =
	// error not attributable to a specific outbound.
	OutboundSlot  Slot
	OutboundIndex *int
}

// Severity values for ValidationError. Empty string is an error (blocks
// reload); SeverityWarning is advisory and does not affect ValidationResult.Ok.
const (
	SeverityError   = ""
	SeverityWarning = "warning"
)

func (e ValidationError) Error() string {
	if e.InRule != "" {
		return fmt.Sprintf("[%s] %s: %s (%s) in %s", e.Slot, e.Kind, e.Tag, e.Message, e.InRule)
	}
	return fmt.Sprintf("[%s] %s: %s (%s)", e.Slot, e.Kind, e.Tag, e.Message)
}

// ValidationResult aggregates all problems found in a single pass.
type ValidationResult struct {
	Errors []ValidationError

	// HasTun reports whether ANY enabled slot declares an inbound of
	// type "tun" in the merged config. The orchestrator uses this to
	// decide reload strategy: sing-box cannot add or remove a tun
	// inbound via SIGHUP (the tun device never gets carrier), so a
	// change in tun presence forces a full restart instead.
	HasTun bool
}

// Ok reports whether the config is safe to apply: true when there are no
// blocking errors. Advisory warnings (SeverityWarning) are ignored so a
// defense-in-depth warning (e.g. dns-final-conflict) never blocks a reload.
func (r ValidationResult) Ok() bool {
	for _, e := range r.Errors {
		if e.Severity != SeverityWarning {
			return false
		}
	}
	return true
}

func (r ValidationResult) Error() string {
	if r.Ok() {
		return ""
	}
	s := fmt.Sprintf("%d cross-slot validation error(s):", len(r.Errors))
	for _, e := range r.Errors {
		s += "\n  - " + e.Error()
	}
	return s
}

// validateLocked is the public-facing entry kept for backward
// compatibility: it validates the current active state of every
// enabled slot. Caller MUST hold o.mu.
func (o *Orchestrator) validateLocked() ValidationResult {
	return o.validateWith(o.readActiveBytes)
}

// readActiveBytes is the default bytes source: read the active path
// for a slot, return nil if it doesn't exist (slot is enabled but the
// producer hasn't written anything yet — not a validation error).
func (o *Orchestrator) readActiveBytes(slot Slot) ([]byte, error) {
	meta, ok := o.slots[slot]
	if !ok {
		return nil, nil
	}
	return readIfExists(o.activePath(meta))
}

// validateWith runs the cross-slot consistency algorithm over the
// currently-enabled slots. bytesFor is the source of slot JSON — callers
// pass readActiveBytes for normal validation and a swapping variant for
// draft validation. Caller MUST hold o.mu.
func (o *Orchestrator) validateWith(bytesFor func(Slot) ([]byte, error)) ValidationResult {
	return o.validateWithEnabled(bytesFor, func(s Slot) bool { return o.enabled[s] })
}

// validateWithEnabled is validateWith with an explicit enabled-predicate.
// Draft validation passes a predicate that treats the TARGET slot as
// enabled regardless of its current state — "validate as if applied"
// (the CheckMerged contract): без этого черновик отключённого слота
// (типично 90-user.json до первого включения) тихо проходил бы
// логическую проверку. Caller MUST hold o.mu.
//
// We deliberately tolerate JSON parse errors: a single broken slot file
// is reported as one error, scan continues. This makes the result more
// useful when developing.
func (o *Orchestrator) validateWithEnabled(bytesFor func(Slot) ([]byte, error), enabledFor func(Slot) bool) ValidationResult {
	type tagOrigin struct {
		slot Slot
	}
	outbounds := map[string]tagOrigin{}
	inbounds := map[string]tagOrigin{}
	dnsServers := map[string]tagOrigin{}
	ruleSetsBySlot := map[Slot]map[string]bool{}
	var errs []ValidationError
	var hasTun bool

	// Slots (in scan order) that set route.final / dns.final. More than one
	// setter is a shadowing hazard: sing-box keeps the lexically-first slot's
	// value and silently ignores the rest (bug #445). Surfaced as a warning
	// after the scan.
	var routeFinalSlots []Slot
	var dnsFinalSlots []Slot

	var pending []validationSectionRefs

	type orderedSlot struct {
		slot Slot
		meta SlotMeta
	}
	// Preserve declared order for deterministic output.
	var ordered []orderedSlot
	for _, m := range KnownSlots() {
		if _, ok := o.slots[m.Slot]; ok {
			ordered = append(ordered, orderedSlot{slot: m.Slot, meta: m})
		}
	}

	for _, os := range ordered {
		if !enabledFor(os.slot) {
			continue
		}
		data, err := bytesFor(os.slot)
		if err != nil {
			errs = append(errs, ValidationError{
				Slot:    os.slot,
				Kind:    "read-error",
				Message: err.Error(),
			})
			continue
		}
		if len(data) == 0 {
			continue
		}
		var c slotConfig
		if err := json.Unmarshal(data, &c); err != nil {
			errs = append(errs, ValidationError{
				Slot:    os.slot,
				Kind:    "parse-error",
				Message: err.Error(),
			})
			continue
		}
		for _, ob := range c.Outbounds {
			if ob.Tag == "" {
				continue
			}
			if existing, dup := outbounds[ob.Tag]; dup {
				errs = append(errs, ValidationError{
					Slot:    os.slot,
					Kind:    "duplicate-outbound",
					Tag:     ob.Tag,
					Message: fmt.Sprintf("also declared in [%s]", existing.slot),
				})
			} else {
				outbounds[ob.Tag] = tagOrigin{slot: os.slot}
			}
		}
		// Endpoints live in the same tag namespace as outbounds: collect them
		// into the same map so refs resolve and a tag shared with an outbound
		// (in any slot) surfaces as duplicate-outbound.
		for _, ep := range c.Endpoints {
			if ep.Tag == "" {
				continue
			}
			if existing, dup := outbounds[ep.Tag]; dup {
				errs = append(errs, ValidationError{
					Slot:    os.slot,
					Kind:    "duplicate-outbound",
					Tag:     ep.Tag,
					Message: fmt.Sprintf("also declared in [%s]", existing.slot),
				})
			} else {
				outbounds[ep.Tag] = tagOrigin{slot: os.slot}
			}
		}
		for _, ib := range c.Inbounds {
			if ib.Type == "tun" {
				hasTun = true
			}
			if ib.Tag == "" {
				continue
			}
			if existing, dup := inbounds[ib.Tag]; dup {
				errs = append(errs, ValidationError{
					Slot:    os.slot,
					Kind:    "duplicate-inbound",
					Tag:     ib.Tag,
					Message: fmt.Sprintf("also declared in [%s]", existing.slot),
				})
			} else {
				inbounds[ib.Tag] = tagOrigin{slot: os.slot}
			}
		}
		for _, ds := range c.DNS.Servers {
			if ds.Tag == "" {
				continue
			}
			if existing, dup := dnsServers[ds.Tag]; dup {
				errs = append(errs, ValidationError{
					Slot:    os.slot,
					Kind:    "duplicate-dns",
					Tag:     ds.Tag,
					Message: fmt.Sprintf("also declared in [%s]", existing.slot),
				})
			} else {
				dnsServers[ds.Tag] = tagOrigin{slot: os.slot}
			}
		}
		ruleSetTags := make(map[string]bool, len(c.Route.RuleSet))
		for _, ruleSet := range c.Route.RuleSet {
			if ruleSet.Tag != "" {
				ruleSetTags[ruleSet.Tag] = true
			}
		}
		ruleSetsBySlot[os.slot] = ruleSetTags

		// Collect refs to check after we have the full outbound set.
		rs := validationSectionRefs{slot: os.slot}
		for i, r := range c.Route.Rules {
			collectRuleRefs(&rs, r, fmt.Sprintf("route.rules[%d]", i), i)
		}
		if c.Route.Final != "" {
			rs.finals = append(rs.finals, finalSection{outbound: c.Route.Final})
			routeFinalSlots = append(routeFinalSlots, os.slot)
		}
		if c.DNS.Final != "" {
			rs.dnsTagRefs = append(rs.dnsTagRefs, dnsTagRefSection{refTag: c.DNS.Final, where: "dns.final"})
			dnsFinalSlots = append(dnsFinalSlots, os.slot)
		}
		if c.Route.DefaultDomainResolver != nil && c.Route.DefaultDomainResolver.Server != "" {
			rs.dnsTagRefs = append(rs.dnsTagRefs, dnsTagRefSection{refTag: c.Route.DefaultDomainResolver.Server, where: "route.default_domain_resolver.server"})
		}
		httpClientTags := make(map[string]bool, len(c.HTTPClients))
		for _, hc := range c.HTTPClients {
			if hc.Tag != "" {
				httpClientTags[hc.Tag] = true
			}
		}
		for i, ruleSet := range c.Route.RuleSet {
			if ruleSet.DownloadDetour != "" {
				rs.sels = append(rs.sels, selSection{
					parentTag: ruleSet.Tag, kind: "download_detour", idx: i, refTag: ruleSet.DownloadDetour,
				})
			}
			if len(ruleSet.HTTPClient) > 0 {
				var ref string
				if err := json.Unmarshal(ruleSet.HTTPClient, &ref); err == nil {
					// Строковая ссылка на тег из http_clients — она же
					// materialize'ится для detour на пустой direct.
					if ref != "" && !httpClientTags[ref] {
						errs = append(errs, ValidationError{
							Slot:    os.slot,
							Kind:    "unknown-http-client",
							Tag:     ref,
							InRule:  fmt.Sprintf("route.rule_set[%d=%q].http_client", i, ruleSet.Tag),
							Message: "no http_clients entry declares this tag",
						})
					}
				} else {
					var obj struct {
						Detour string `json:"detour,omitempty"`
					}
					if err := json.Unmarshal(ruleSet.HTTPClient, &obj); err == nil && obj.Detour != "" {
						rs.sels = append(rs.sels, selSection{
							parentTag: ruleSet.Tag, kind: "http_client", idx: i, refTag: obj.Detour,
						})
					}
				}
			}
		}
		for i, hc := range c.HTTPClients {
			if hc.Detour != "" {
				rs.sels = append(rs.sels, selSection{
					parentTag: hc.Tag, kind: "http_clients", idx: i, refTag: hc.Detour,
				})
			}
		}
		for i, ob := range c.Outbounds {
			for j, member := range ob.Outbounds {
				rs.sels = append(rs.sels, selSection{
					parentTag: ob.Tag, kind: "members", idx: i, memberIdx: j, refTag: member,
				})
			}
			if ob.Default != "" {
				rs.sels = append(rs.sels, selSection{
					parentTag: ob.Tag, kind: "default", idx: i, refTag: ob.Default,
				})
			}
			if ob.DomainResolver != nil && ob.DomainResolver.Server != "" {
				rs.dnsTagRefs = append(rs.dnsTagRefs, dnsTagRefSection{
					refTag: ob.DomainResolver.Server,
					where:  fmt.Sprintf("outbounds[%d].domain_resolver.server", i),
				})
			}
		}
		// Endpoints делят с outbounds и структуру, и пространство тегов (:520),
		// значит их domain_resolver обязан резолвиться так же — иначе опечатка
		// в теге DNS-сервера прошла бы валидацию молча.
		for i, ep := range c.Endpoints {
			if ep.DomainResolver != nil && ep.DomainResolver.Server != "" {
				rs.dnsTagRefs = append(rs.dnsTagRefs, dnsTagRefSection{
					refTag: ep.DomainResolver.Server,
					where:  fmt.Sprintf("endpoints[%d].domain_resolver.server", i),
				})
			}
		}
		for i, ds := range c.DNS.Servers {
			if ds.Detour != "" {
				rs.sels = append(rs.sels, selSection{
					parentTag: ds.Tag, kind: "dns_detour", idx: i, refTag: ds.Detour,
				})
			}
		}
		for i, r := range c.DNS.Rules {
			for _, tag := range r.RuleSet {
				rs.ruleSets = append(rs.ruleSets, ruleSetSection{idx: i, refTag: tag, inRule: fmt.Sprintf("dns.rules[%d].rule_set", i)})
			}
		}
		pending = append(pending, rs)
	}

	// Built-in tags that sing-box defines implicitly (no JSON declaration).
	builtins := map[string]bool{
		"direct": true,
		"block":  true,
		"dns":    true,
	}

	// Resolve refs.
	knownOutbound := func(tag string) bool {
		if builtins[tag] {
			return true
		}
		_, ok := outbounds[tag]
		return ok
	}
	knownDNSServer := func(tag string) bool {
		_, ok := dnsServers[tag]
		return ok
	}
	for _, rs := range pending {
		for _, r := range rs.rules {
			if !knownOutbound(r.outbound) {
				errs = append(errs, ValidationError{
					Slot:    rs.slot,
					Kind:    "unknown-outbound",
					Tag:     r.outbound,
					InRule:  r.inRule,
					Message: "no slot declares this outbound tag",
				})
			}
		}
		for _, f := range rs.finals {
			if !knownOutbound(f.outbound) {
				errs = append(errs, ValidationError{
					Slot:    rs.slot,
					Kind:    "unknown-outbound",
					Tag:     f.outbound,
					InRule:  "route.final",
					Message: "no slot declares this outbound tag",
				})
			}
		}
		for _, s := range rs.sels {
			if !knownOutbound(s.refTag) {
				where := fmt.Sprintf("outbounds[%d=%q].%s", s.idx, s.parentTag, s.kind)
				if s.kind == "members" {
					where = fmt.Sprintf("outbounds[%d=%q].outbounds[%d]", s.idx, s.parentTag, s.memberIdx)
				} else if s.kind == "download_detour" {
					where = fmt.Sprintf("route.rule_set[%d=%q].download_detour", s.idx, s.parentTag)
				} else if s.kind == "http_client" {
					where = fmt.Sprintf("route.rule_set[%d=%q].http_client.detour", s.idx, s.parentTag)
				} else if s.kind == "http_clients" {
					where = fmt.Sprintf("http_clients[%d=%q].detour", s.idx, s.parentTag)
				} else if s.kind == "dns_detour" {
					where = fmt.Sprintf("dns.servers[%d=%q].detour", s.idx, s.parentTag)
				}
				errs = append(errs, ValidationError{
					Slot:    rs.slot,
					Kind:    "unknown-outbound",
					Tag:     s.refTag,
					InRule:  where,
					Message: "no slot declares this outbound tag",
				})
			}
		}
		ruleSets := ruleSetsBySlot[rs.slot]
		for _, r := range rs.ruleSets {
			if !ruleSets[r.refTag] {
				errs = append(errs, ValidationError{
					Slot:    rs.slot,
					Kind:    "unknown-rule-set",
					Tag:     r.refTag,
					InRule:  r.inRule,
					Message: "slot does not declare this rule_set tag",
				})
			}
		}
		for _, d := range rs.dnsTagRefs {
			if !knownDNSServer(d.refTag) {
				errs = append(errs, ValidationError{
					Slot:    rs.slot,
					Kind:    "unknown-dns-server",
					Tag:     d.refTag,
					InRule:  d.where,
					Message: "no slot declares this dns server tag",
				})
			}
		}
	}

	// Defense in depth: flag when more than one enabled slot sets a scalar
	// `final`. sing-box merges these first-file-wins, so a second setter is
	// silently shadowed. After #445 phase 1 the base slot no longer sets
	// dns.final/route.final, so this stays quiet in the normal path.
	if len(routeFinalSlots) > 1 {
		errs = append(errs, ValidationError{
			Slot:     routeFinalSlots[0],
			Kind:     "route-final-conflict",
			Severity: SeverityWarning,
			InRule:   "route.final",
			Message: fmt.Sprintf(
				"route.final set by multiple slots (%s); sing-box keeps the first and ignores the rest",
				slotsList(routeFinalSlots)),
		})
	}
	if len(dnsFinalSlots) > 1 {
		errs = append(errs, ValidationError{
			Slot:     dnsFinalSlots[0],
			Kind:     "dns-final-conflict",
			Severity: SeverityWarning,
			InRule:   "dns.final",
			Message: fmt.Sprintf(
				"dns.final set by multiple slots (%s); sing-box keeps the first and ignores the rest",
				slotsList(dnsFinalSlots)),
		})
	}

	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Slot != errs[j].Slot {
			return errs[i].Slot < errs[j].Slot
		}
		if errs[i].Kind != errs[j].Kind {
			return errs[i].Kind < errs[j].Kind
		}
		return errs[i].Tag < errs[j].Tag
	})
	return ValidationResult{Errors: errs, HasTun: hasTun}
}

// validateDraftLocked validates the merged config with one slot's bytes
// swapped for the supplied draft bytes. Other slots use their active
// content. Caller MUST hold o.mu.
//
// Use case: ApplyDraft pre-flights cross-slot consistency before
// renaming pending → active.
func (o *Orchestrator) validateDraftLocked(target Slot, draftBytes []byte) ValidationResult {
	// Виртуальный prune сиблингов (issue #633): реальный Reload перед
	// валидацией самолечит висячие члены/default селекторов в не-user
	// слотах (pruneDanglingSelectorRefsLocked), поэтому draft-валидация
	// обязана судить сиблингов так, как их увидит Reload. Иначе удаление
	// outbound'а из целевого слота отклоняется из-за дисковой ссылки в
	// чужом слоте (device-proxy селектор → удаляемая подписка), хотя
	// Reload прошёл бы: подписка становилась неудаляемой (500 could not
	// isolate outbound). Диск не трогаем — прунится только копия для
	// валидации; сам кандидат НЕ прунится (flush подписок лечит свои
	// висячие ссылки по ошибкам своего слота).
	known := o.enabledOutboundTagsLocked(map[Slot]bool{target: true})
	var dc slotConfig
	if json.Unmarshal(draftBytes, &dc) == nil {
		for _, ob := range dc.Outbounds {
			if ob.Tag != "" {
				known[ob.Tag] = true
			}
		}
		for _, ep := range dc.Endpoints {
			if ep.Tag != "" {
				known[ep.Tag] = true
			}
		}
	}
	return o.validateWithEnabled(
		func(slot Slot) ([]byte, error) {
			if slot == target {
				return draftBytes, nil
			}
			data, err := o.readActiveBytes(slot)
			if err != nil || slot == SlotUser {
				// 90-user.json Reload не прунит — валидируем как есть.
				return data, err
			}
			return pruneDanglingSelectorRefsBytes(data, known), nil
		},
		// Цель считается включённой — «валидируем как будто применили»,
		// в согласии со снапшотом sing-box check в checkMergedLocked.
		func(slot Slot) bool { return slot == target || o.enabled[slot] },
	)
}

// pruneDanglingSelectorRefsBytes returns data with the virtual selector
// prune applied, or data unchanged when nothing was pruned / it isn't a
// JSON object. Never mutates the input's on-disk source.
func pruneDanglingSelectorRefsBytes(data []byte, known map[string]bool) []byte {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return data
	}
	changed, _ := pruneDanglingSelectorRefsInDoc(root, known, "")
	if !changed {
		return data
	}
	out, err := json.Marshal(root)
	if err != nil {
		return data
	}
	return out
}

// slotsList renders slots as a comma-separated string for warning messages.
func slotsList(slots []Slot) string {
	parts := make([]string, len(slots))
	for i, s := range slots {
		parts[i] = string(s)
	}
	return strings.Join(parts, ", ")
}

// Validate is the public, lock-acquiring entry point.
func (o *Orchestrator) Validate() ValidationResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.validateLocked()
}

type slotConfig struct {
	Inbounds  []inboundJSON  `json:"inbounds,omitempty"`
	Outbounds []outboundJSON `json:"outbounds,omitempty"`
	// Endpoints share the outbound tag namespace in sing-box (a router rule
	// may route to an endpoint tag). 16-awg3.json is the first slot to carry
	// them; without collecting their tags a rule → awg3 endpoint would fail
	// with unknown-outbound and a tag colliding with an outbound would slip past.
	Endpoints   []outboundJSON   `json:"endpoints,omitempty"`
	Route       routeJSON        `json:"route"`
	DNS         dnsJSON          `json:"dns"`
	HTTPClients []httpClientJSON `json:"http_clients,omitempty"`
}

type inboundJSON struct {
	Tag  string `json:"tag"`
	Type string `json:"type"`
}

type outboundJSON struct {
	Tag            string              `json:"tag"`
	Outbounds      []string            `json:"outbounds,omitempty"`
	Default        string              `json:"default,omitempty"`
	DomainResolver *domainResolverJSON `json:"domain_resolver,omitempty"`
}

type routeJSON struct {
	Final                 string              `json:"final,omitempty"`
	Rules                 []ruleJSON          `json:"rules,omitempty"`
	RuleSet               []ruleSetJSON       `json:"rule_set,omitempty"`
	DefaultDomainResolver *domainResolverJSON `json:"default_domain_resolver,omitempty"`
}

type domainResolverJSON struct {
	Server string `json:"server,omitempty"`
}

// UnmarshalJSON accepts both forms sing-box allows for default_domain_resolver:
// a bare string (the server tag, e.g. "dns-bootstrap" in 00-base.json) or an
// object ({"server":"real",...}). Without this, a string value fails to
// unmarshal into the struct, which fails parsing of the WHOLE slot config and
// silently skips every orchestrator reload (stand-caught 2026-06-18).
func (d *domainResolverJSON) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		d.Server = s
		return nil
	}
	type alias domainResolverJSON
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*d = domainResolverJSON(a)
	return nil
}

type ruleJSON struct {
	Outbound string     `json:"outbound"`
	RuleSet  []string   `json:"rule_set,omitempty"`
	Rules    []ruleJSON `json:"rules,omitempty"`
}

// ruleSetJSON.HTTPClient держит http_client как json.RawMessage: sing-box
// 1.14 допускает и объект {"detour":"X"}, и строковую ссылку на тег из
// http_clients (materialize'ится для detour на пустой direct-outbound —
// applyHTTPClients). Строгая форма *struct{Detour} падала бы на строке и
// проваливала бы весь json.Unmarshal слота.
type ruleSetJSON struct {
	Tag            string          `json:"tag"`
	DownloadDetour string          `json:"download_detour,omitempty"`
	HTTPClient     json.RawMessage `json:"http_client,omitempty"`
}

type httpClientJSON struct {
	Tag    string `json:"tag"`
	Detour string `json:"detour,omitempty"`
}

type dnsJSON struct {
	Servers []dnsServerJSON `json:"servers,omitempty"`
	Rules   []dnsRuleJSON   `json:"rules,omitempty"`
	Final   string          `json:"final,omitempty"`
}

type dnsServerJSON struct {
	Tag    string `json:"tag"`
	Detour string `json:"detour,omitempty"`
}

type dnsRuleJSON struct {
	RuleSet []string `json:"rule_set,omitempty"`
}

type validationSectionRefs struct {
	slot       Slot
	rules      []ruleSection
	finals     []finalSection
	sels       []selSection
	ruleSets   []ruleSetSection
	dnsTagRefs []dnsTagRefSection
}

type dnsTagRefSection struct {
	refTag string
	where  string
}

type ruleSection struct {
	idx      int
	outbound string
	inRule   string
}

type finalSection struct {
	outbound string
}

type selSection struct {
	parentTag string
	kind      string // "members" / "default" / "download_detour" / "http_client" / "http_clients" / "dns_detour"
	idx       int
	memberIdx int
	refTag    string
}

type ruleSetSection struct {
	idx    int
	refTag string
	inRule string
}

func collectRuleRefs(refs *validationSectionRefs, rule ruleJSON, path string, topIndex int) {
	if rule.Outbound != "" {
		refs.rules = append(refs.rules, ruleSection{idx: topIndex, outbound: rule.Outbound, inRule: path})
	}
	for _, tag := range rule.RuleSet {
		refs.ruleSets = append(refs.ruleSets, ruleSetSection{idx: topIndex, refTag: tag, inRule: path + ".rule_set"})
	}
	for i, nested := range rule.Rules {
		collectRuleRefs(refs, nested, fmt.Sprintf("%s.rules[%d]", path, i), topIndex)
	}
}

func readIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}
