package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// ValidationError describes one cross-slot consistency problem.
// Slot is the slot whose JSON contained the offending construct;
// References (when set) names what was referenced.
type ValidationError struct {
	Slot    Slot
	Kind    string // "duplicate-outbound" / "duplicate-inbound" / "duplicate-dns" / "unknown-outbound"
	Tag     string // the offending tag value
	InRule  string // optional: human-readable location (e.g. "rules[3]" or "selector default")
	Message string
}

func (e ValidationError) Error() string {
	if e.InRule != "" {
		return fmt.Sprintf("[%s] %s: %s (%s) in %s", e.Slot, e.Kind, e.Tag, e.Message, e.InRule)
	}
	return fmt.Sprintf("[%s] %s: %s (%s)", e.Slot, e.Kind, e.Tag, e.Message)
}

// ValidationResult aggregates all problems found in a single pass.
type ValidationResult struct {
	Errors []ValidationError
}

func (r ValidationResult) Ok() bool { return len(r.Errors) == 0 }

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

// validateWith runs the cross-slot consistency algorithm. bytesFor is
// the source of slot JSON — callers pass readActiveBytes for normal
// validation and a swapping variant for draft validation. Caller MUST
// hold o.mu.
//
// We deliberately tolerate JSON parse errors: a single broken slot file
// is reported as one error, scan continues. This makes the result more
// useful when developing.
func (o *Orchestrator) validateWith(bytesFor func(Slot) ([]byte, error)) ValidationResult {
	type tagOrigin struct {
		slot Slot
	}
	outbounds := map[string]tagOrigin{}
	inbounds := map[string]tagOrigin{}
	dnsServers := map[string]tagOrigin{}
	var errs []ValidationError

	type sectionRefs struct {
		slot   Slot
		rules  []ruleSection
		finals []finalSection
		sels   []selSection
	}
	var pending []sectionRefs

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
		if !o.enabled[os.slot] {
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
		for _, ib := range c.Inbounds {
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

		// Collect refs to check after we have the full outbound set.
		rs := sectionRefs{slot: os.slot}
		for i, r := range c.Route.Rules {
			if r.Outbound != "" {
				rs.rules = append(rs.rules, ruleSection{idx: i, outbound: r.Outbound})
			}
		}
		if c.Route.Final != "" {
			rs.finals = append(rs.finals, finalSection{outbound: c.Route.Final})
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
	for _, rs := range pending {
		for _, r := range rs.rules {
			if !knownOutbound(r.outbound) {
				errs = append(errs, ValidationError{
					Slot:    rs.slot,
					Kind:    "unknown-outbound",
					Tag:     r.outbound,
					InRule:  fmt.Sprintf("route.rules[%d]", r.idx),
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
	return ValidationResult{Errors: errs}
}

// validateDraftLocked validates the merged config with one slot's bytes
// swapped for the supplied draft bytes. Other slots use their active
// content. Caller MUST hold o.mu.
//
// Use case: ApplyDraft pre-flights cross-slot consistency before
// renaming pending → active.
func (o *Orchestrator) validateDraftLocked(target Slot, draftBytes []byte) ValidationResult {
	return o.validateWith(func(slot Slot) ([]byte, error) {
		if slot == target {
			return draftBytes, nil
		}
		return o.readActiveBytes(slot)
	})
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
	Route     routeJSON      `json:"route"`
	DNS       dnsJSON        `json:"dns"`
}

type inboundJSON struct {
	Tag string `json:"tag"`
}

type outboundJSON struct {
	Tag       string   `json:"tag"`
	Outbounds []string `json:"outbounds,omitempty"`
	Default   string   `json:"default,omitempty"`
}

type routeJSON struct {
	Final string     `json:"final,omitempty"`
	Rules []ruleJSON `json:"rules,omitempty"`
}

type ruleJSON struct {
	Outbound string `json:"outbound"`
}

type dnsJSON struct {
	Servers []dnsServerJSON `json:"servers,omitempty"`
}

type dnsServerJSON struct {
	Tag string `json:"tag"`
}

type ruleSection struct {
	idx      int
	outbound string
}

type finalSection struct {
	outbound string
}

type selSection struct {
	parentTag string
	kind      string // "members" / "default"
	idx       int
	memberIdx int
	refTag    string
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
