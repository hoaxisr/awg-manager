package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// qosReloadWait bounds the reconcile-path wait for sing-box to rebind its
// inbounds after a QoS config heal, before Install moves the iptables
// dispatch onto the new per-class ports (see reconcileInstalled). Shorter
// than Enable's full boot window on purpose: reconcile is a periodic heal
// and must not block the scheduler for a minute behind a dead engine —
// the wait is soft (proceed on timeout).
const qosReloadWait = 15 * time.Second

// Managed QoS route rules live in their own orchestrator slot,
// 18-qos-routes.json. Why not 20-router.json:
//   - 20 is the user-visible rules file: managed rules leaked into the rules
//     UI as anonymous matcher-less rows, users deleted them, the reconcile
//     heal re-added them — a churn loop;
//   - the user slot goes through the staging pipeline: a heal that loads the
//     EFFECTIVE config (pending draft preferred) and direct-writes to active
//     silently applied staged drafts, bypassing the «Применить» gate;
//   - sing-box evaluates merged route rules in file order, so rules parked at
//     a fixed position inside 20 could end up AFTER user rules and never
//     match. Slot 18 merges before 20, so a DSCP mark (an explicit per-packet
//     policy signal) always wins over user rules.
//
// DNS never depends on these rules: buildRestoreInput intercepts UDP/53 to
// the main TPROXY port and (with QoS classes present) TCP/53 to the main
// REDIRECT port BEFORE the per-class DSCP dispatch, so DSCP-marked DNS lands
// on the main inbounds where the system hijack-dns rule (slot 20) applies.
// LAN/RFC1918 destinations likewise never reach the class inbounds — the
// builtin bypass RETURNs precede the DSCP dispatch in both chains.

// qosRoutesSlot is the JSON shape for 18-qos-routes.json. Only route.rules —
// never inbounds/outbounds; null slices in a merged slot corrupt sing-box's
// outbounds array.
type qosRoutesSlot struct {
	Route struct {
		Rules []Rule `json:"rules"`
	} `json:"route"`
}

func marshalQoSRoutesSlot(rules []Rule) ([]byte, error) {
	slot := qosRoutesSlot{}
	slot.Route.Rules = rules
	return json.MarshalIndent(slot, "", "  ")
}

// buildQoSRouteRules renders one managed route rule per class: the class's
// inbound tag pair routed to its outbound. Output is canonical/deterministic
// for a given class slice (activeQoSClasses preserves the persisted settings
// order), so syncQoSRoutesSlot can byte-compare the marshalled slot to skip
// the sing-box reload. No AwgmManaged marker — the slot is merged by
// sing-box, which rejects unknown rule fields.
//
// The leading route-options rule carries the same UDP timeout that
// EnsureUDPTimeoutRule keeps in 20-router.json (#554). That copy cannot be
// reached from here: sing-box merges config.d in filename order, so this slot's
// rules run first and a class's final `route` action ends evaluation. Without
// the rule QoS traffic gets no metadata.UDPTimeout at all — the inbound's own
// udp_timeout only sets the udpnat cache lifetime, which sing-box purges lazily
// (nothing calls PurgeExpired), so an idle session never closes. Putting
// udp_timeout on the class rule itself does NOT work: sing-box drops the field
// when building an `action: route` (route/rule/rule_action.go copies it only
// for route-options — the docs claim otherwise). Scoped to the class tproxy
// inbounds on purpose: a matcher-less copy would land ahead of the system
// hijack-dns rule in the merge and stretch 10s DNS sessions to the user's
// timeout. The 20-router.json copy stays — this slot is parked whenever QoS
// is off.
//
// The sniff rule is there for the same merge-order reason: the system one lives
// in 20-router.json, behind the class rules, so QoS connections would carry no
// protocol/domain in the connections view. It follows the user's sniffer toggle
// exactly like EnsureSystemRules does, and cannot shorten the timeout above —
// route/conn.go prefers metadata.UDPTimeout over the sniffed-protocol defaults.
func buildQoSRouteRules(classes []qosClass, sr storage.SingboxRouterSettings) []Rule {
	if len(classes) == 0 {
		return nil
	}
	udpInbounds := make([]string, 0, len(classes))
	allInbounds := make([]string, 0, 2*len(classes))
	for _, q := range classes {
		udpInbounds = append(udpInbounds, qosTProxyTag(q.DSCP))
		allInbounds = append(allInbounds, qosTProxyTag(q.DSCP), qosRedirectTag(q.DSCP))
	}
	out := make([]Rule, 0, len(classes)+2)
	if sr.SnifferEnabled {
		out = append(out, Rule{Inbound: allInbounds, Action: "sniff"})
	}
	out = append(out, Rule{
		Inbound:    udpInbounds,
		Action:     "route-options",
		Network:    "udp",
		UDPTimeout: resolveUDPTimeout(sr.UDPTimeout),
	})
	for _, q := range classes {
		out = append(out, Rule{
			Inbound:  []string{qosTProxyTag(q.DSCP), qosRedirectTag(q.DSCP)},
			Action:   "route",
			Outbound: q.Outbound,
		})
	}
	return out
}

// filterQoSClassesWithKnownOutbounds partitions classes into those whose
// outbound resolves against the known catalogs (cfg composites + subscription
// composites + AWG tags + sing-box tunnel tags + built-ins) and those that
// don't. Defense in depth for the emit path (FIX for the engine-down failure
// mode): a merged rule routing to a nonexistent outbound is rejected by
// sing-box at load time and takes the WHOLE engine down, so unknown-outbound
// classes are skipped at emit time and surfaced as a qos-outbound-missing
// status Issue instead.
func (s *ServiceImpl) filterQoSClassesWithKnownOutbounds(ctx context.Context, classes []qosClass, cfg *RouterConfig) (known, missing []qosClass) {
	if len(classes) == 0 {
		return nil, nil
	}
	if cfg == nil {
		cfg = NewEmptyConfig()
	}
	known = make([]qosClass, 0, len(classes))
	for _, c := range classes {
		if s.isKnownOutboundTag(ctx, c.Outbound, cfg) {
			known = append(known, c)
			continue
		}
		missing = append(missing, c)
	}
	return known, missing
}

// syncQoSRoutesSlot writes the managed QoS route rules into
// 18-qos-routes.json (merged by sing-box, invisible in the rules UI). Uses
// SaveSilent/SetEnabledSilent so no staging draft or debounced reload is
// triggered — callers decide when to apply (Enable's orchestratorApplyNow,
// the reconcile heal's applyQoSRoutesSlot).
//
// File semantics: at least one emitted rule ⇒
// slot enabled with the canonical content; no classes (feature off, all
// disabled, or every outbound unknown) ⇒ the slot is cleared (canonical empty
// content written, file parked under disabled/). Returns changed=false when
// the merged config is already in the target shape (byte-equal active file
// when enabling, absent active file when disabling) so the caller can skip
// the sing-box reload.
//
// Classes whose outbound is unknown at emit time are skipped (see
// filterQoSClassesWithKnownOutbounds); GetStatus independently reports them
// as qos-outbound-missing issues.
func (s *ServiceImpl) syncQoSRoutesSlot(ctx context.Context, classes []qosClass, sr storage.SingboxRouterSettings) (bool, error) {
	if s.deps.Orch == nil {
		return false, nil
	}
	emit := classes
	if len(classes) > 0 {
		// Resolve outbounds against the APPLIED router config — the emit
		// decision must reflect what sing-box actually merges, never a
		// pending draft.
		cfg, err := s.loadAppliedRouterConfig()
		if err != nil {
			return false, err
		}
		var skipped []qosClass
		emit, skipped = s.filterQoSClassesWithKnownOutbounds(ctx, classes, cfg)
		for _, c := range skipped {
			s.appLog.Warn("qos-routes", c.Outbound,
				fmt.Sprintf("класс QoS (DSCP %d) ссылается на несуществующий outbound %q — правило не эмитится", c.DSCP, c.Outbound))
		}
	}
	rules := buildQoSRouteRules(emit, sr)
	enable := len(rules) > 0
	data, err := marshalQoSRoutesSlot(rules)
	if err != nil {
		return false, err
	}
	// The merged config only sees the ACTIVE file: enabled ⇒ byte-equal
	// active file is a no-op; disabled ⇒ absent active file is a no-op
	// regardless of any parked disabled/ copy.
	activePath := filepath.Join(s.deps.Orch.ConfigDir(), "18-qos-routes.json")
	existing, readErr := os.ReadFile(activePath)
	if enable {
		if readErr == nil && bytes.Equal(existing, data) {
			return false, nil // already the exact active content — steady state
		}
	} else if os.IsNotExist(readErr) {
		return false, nil // already absent from the merge — steady state
	}
	if err := s.deps.Orch.SaveSilent(orchestrator.SlotQoSRoutes, data); err != nil {
		return false, err
	}
	if err := s.deps.Orch.SetEnabledSilent(orchestrator.SlotQoSRoutes, enable); err != nil {
		return false, err
	}
	return true, nil
}

// disableQoSRoutesSlot parks 18-qos-routes.json under disabled/ when the
// router itself is disabled: the managed rules reference qos-* inbound tags
// that only exist while 20-router.json is active, so the overlay must never
// outlive the router slot.
func (s *ServiceImpl) disableQoSRoutesSlot() error {
	if s.deps.Orch == nil {
		return nil
	}
	return s.deps.Orch.SetEnabledSilent(orchestrator.SlotQoSRoutes, false)
}

// applyQoSRoutesSlot reloads sing-box when the QoS config changed OR the
// previous apply failed — disk state ≠ running sing-box after a failed
// apply, so the next heal must re-apply even when everything on disk is
// byte-identical, or the retry heal becomes a no-op forever and loses its
// self-heal role.
func (s *ServiceImpl) applyQoSRoutesSlot(changed bool) {
	if !changed && !s.qosApplyFailed.Load() {
		return
	}
	if err := s.orchestratorApplyNow(); err != nil {
		s.qosApplyFailed.Store(true)
		s.appLog.Warn("qos-routes", "", err.Error())
		return
	}
	s.qosApplyFailed.Store(false)
}

// qosClassesReferencing returns human-readable locations of persisted QoS
// classes (enabled or not) that route to tag. Used by the outbound-delete
// guard so a non-force delete refuses instead of silently orphaning a class.
func (s *ServiceImpl) qosClassesReferencing(tag string) []string {
	if s.deps.Settings == nil || strings.TrimSpace(tag) == "" {
		return nil
	}
	settings, err := s.deps.Settings.Load()
	if err != nil || settings == nil {
		return nil
	}
	var refs []string
	for i, c := range settings.SingboxRouter.QoSClasses {
		if strings.TrimSpace(c.Outbound) == tag {
			refs = append(refs, fmt.Sprintf("qosClasses[%d] (DSCP %d)", i, c.DSCP))
		}
	}
	return refs
}

// renameQoSClassOutbound rewrites persisted QoS classes routing to oldTag so
// an outbound rename never leaves classes pointing at a tag that no longer
// exists. Companion of renameOutboundReferences for the settings side.
func (s *ServiceImpl) renameQoSClassOutbound(oldTag, newTag string) error {
	oldTag = strings.TrimSpace(oldTag)
	newTag = strings.TrimSpace(newTag)
	if s.deps.Settings == nil || oldTag == "" || newTag == "" || oldTag == newTag {
		return nil
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	// Mutate a private copy: Load/Get hand out the store's live cache, which
	// other goroutines read without a lock. QoSClasses elements are edited in
	// place below, so the slice needs its own backing array too.
	cp := *settings
	cp.SingboxRouter.QoSClasses = append([]storage.SingboxQoSClass(nil), cp.SingboxRouter.QoSClasses...)
	settings = &cp
	changed := false
	for i := range settings.SingboxRouter.QoSClasses {
		if strings.TrimSpace(settings.SingboxRouter.QoSClasses[i].Outbound) == oldTag {
			settings.SingboxRouter.QoSClasses[i].Outbound = newTag
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.deps.Settings.Save(settings)
}

// disableQoSClassesForOutbound flips Enabled off on persisted QoS classes
// routing to tag. Companion of removeOutboundReferences (force-delete) for
// the settings side: the class is deliberately DISABLED, not deleted, so the
// UI still shows it (marked off) and the user can re-point it — silently
// dropping user configuration on a force-delete would be data loss. The next
// reconcile tick converges the sing-box/iptables side.
func (s *ServiceImpl) disableQoSClassesForOutbound(tag string) error {
	tag = strings.TrimSpace(tag)
	if s.deps.Settings == nil || tag == "" {
		return nil
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	// Mutate a private copy: Load/Get hand out the store's live cache, which
	// other goroutines read without a lock. QoSClasses elements are edited in
	// place below, so the slice needs its own backing array too.
	cp := *settings
	cp.SingboxRouter.QoSClasses = append([]storage.SingboxQoSClass(nil), cp.SingboxRouter.QoSClasses...)
	settings = &cp
	changed := false
	for i := range settings.SingboxRouter.QoSClasses {
		c := &settings.SingboxRouter.QoSClasses[i]
		if strings.TrimSpace(c.Outbound) == tag && c.Enabled {
			c.Enabled = false
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.deps.Settings.Save(settings)
}

// healQoSConfig is the reconcile-path self-heal for the QoS sing-box side:
// per-class inbound pairs in 20-router.json plus the managed route rules in
// 18-qos-routes.json. It re-derives the canonical state from settings and
// writes only what actually drifted; applyQoSRoutesSlot then reloads
// sing-box only on change (sticky-failure aware). Returns whether anything
// was (re)written so reconcileInstalled can wait for the reload to land
// before installing new iptables dispatch ports.
//
// Load-source discipline: the inbound heal reads the APPLIED router config
// (active/, then disabled/), never LoadEffective — a heal that starts from
// the pending draft and direct-writes to active would silently apply staged
// edits, bypassing the «Применить» gate. persistConfigDirect's byte-equal
// short-circuit keeps the steady state write-free.
func (s *ServiceImpl) healQoSConfig(ctx context.Context, sr storage.SingboxRouterSettings) (bool, error) {
	classes := activeQoSClasses(sr.QoSClasses)
	cfg, err := s.loadAppliedRouterConfig()
	if err != nil {
		return false, err
	}
	inbounds, inChanged := ensureQoSInbounds(cfg.Inbounds, classes, sr.UDPTimeout)
	if inChanged {
		cfg.Inbounds = inbounds
		if err := s.persistConfigDirect(ctx, cfg); err != nil {
			return false, err
		}
	}
	slotChanged, err := s.syncQoSRoutesSlot(ctx, classes, sr)
	if err != nil {
		return inChanged, err
	}
	changed := inChanged || slotChanged
	s.applyQoSRoutesSlot(changed)
	return changed, nil
}
