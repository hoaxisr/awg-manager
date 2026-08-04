package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

const selectiveIPRuleManaged = "selective-ip"

// selectiveRoutesSlot is the JSON shape for 19-selective-routes.json.
// Only route.rules — never inbounds/outbounds; null slices in a merged
// slot corrupt sing-box's outbounds array (unknown outbound type at index N).
type selectiveRoutesSlot struct {
	Route struct {
		Rules []Rule `json:"rules"`
	} `json:"route"`
}

func marshalSelectiveRoutesSlot(rules []Rule) ([]byte, error) {
	slot := selectiveRoutesSlot{}
	slot.Route.Rules = rules
	return json.MarshalIndent(slot, "", "  ")
}

// buildSelectiveIPRules builds ip_cidr route rules from outbound-grouped /32 lists.
// Output is canonical (sorted outbounds, sorted CIDRs): syncSelectiveRoutesSlot
// byte-compares the marshalled slot to skip the sing-box reload, so identical
// inputs must always marshal identically regardless of map iteration order.
func buildSelectiveIPRules(byOutbound map[string][]string) []Rule {
	if len(byOutbound) == 0 {
		return nil
	}
	outbounds := make([]string, 0, len(byOutbound))
	for ob := range byOutbound {
		outbounds = append(outbounds, ob)
	}
	sort.Strings(outbounds)
	out := make([]Rule, 0, len(outbounds))
	for _, ob := range outbounds {
		if len(byOutbound[ob]) == 0 {
			// A rule with an empty ip_cidr list is condition-less: sing-box
			// either rejects the config or match-alls the outbound. Upstream
			// accumulators never emit empty lists; keep the guard here as the
			// last line of defence before marshalling.
			continue
		}
		cidrs := append([]string(nil), byOutbound[ob]...)
		sort.Strings(cidrs)
		out = append(out, Rule{
			Action:   "route",
			Outbound: ob,
			IPCIDR:   cidrs,
			// AwgmManaged intentionally omitted: 19-selective-routes.json is
			// merged into sing-box config and must not contain unknown fields.
		})
	}
	return out
}

// stripSelectiveManagedRules removes auto-generated selective-ip rules that were
// mistakenly persisted into the routing slot by an earlier build.
func stripSelectiveManagedRules(rules []Rule) ([]Rule, bool) {
	if len(rules) == 0 {
		return rules, false
	}
	filtered := rules[:0]
	changed := false
	for _, r := range rules {
		if r.AwgmManaged == selectiveIPRuleManaged {
			changed = true
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered, changed
}

// stripLegacySelectiveRulesFromRouter removes managed selective-ip rules from
// the user-visible routing slot (21-routing.json). Returns whether anything changed.
func (s *ServiceImpl) stripLegacySelectiveRulesFromRouter(ctx context.Context) (bool, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return false, err
	}
	cfg = s.ruleSetMaterializer().restoreConfig(cfg)
	filtered, changed := stripSelectiveManagedRules(cfg.Route.Rules)
	if !changed {
		return false, nil
	}
	cfg.Route.Rules = filtered
	if err := s.persistConfigDirect(ctx, cfg); err != nil {
		return false, err
	}
	s.emitCfgEvent("rules", cfg)
	return true, nil
}

// syncSelectiveRoutesSlot writes ip_cidr overlay rules into 19-selective-routes.json
// (merged by sing-box, invisible in the rules UI). Uses SaveSilent so no staging
// draft / «Применить» banner appears after ipset rebuild.
//
// Returns changed=false when the merged config is already in the target shape
// (byte-equal active slot, same enabled state — mirrors the routing-slot
// diff in persistConfigDirect) so the caller can skip the sing-box reload: an
// unconditional SIGHUP after every manual rebuild drops all proxied
// connections even when zero routes changed.
func (s *ServiceImpl) syncSelectiveRoutesSlot(ctx context.Context, byOutbound map[string][]string) (bool, error) {
	if s.deps.Orch == nil {
		return false, nil
	}
	rules := buildSelectiveIPRules(byOutbound)
	enable := len(rules) > 0 && s.selectiveActive()
	data, err := marshalSelectiveRoutesSlot(rules)
	if err != nil {
		return false, err
	}
	// The merged config only sees the ACTIVE file: enabled ⇒ byte-equal active
	// file is a no-op; disabled ⇒ absent active file is a no-op regardless of
	// any parked disabled/ copy.
	activePath := filepath.Join(s.deps.Orch.ConfigDir(), "19-selective-routes.json")
	existing, readErr := os.ReadFile(activePath)
	var unchanged bool
	if enable {
		unchanged = readErr == nil && bytes.Equal(existing, data)
	} else {
		unchanged = os.IsNotExist(readErr)
	}
	// Always rewrite the slot file so a legacy build's `"outbounds": null`
	// cannot keep breaking sing-box check after rules are cleared.
	if err := s.deps.Orch.SaveSilent(orchestrator.SlotSelectiveRoutes, data); err != nil {
		return false, err
	}
	if err := s.deps.Orch.SetEnabledSilent(orchestrator.SlotSelectiveRoutes, enable); err != nil {
		return false, err
	}
	return !unchanged, nil
}

// healLegacySelectiveRoutesSlotIfNeeded rewrites 19-selective-routes.json when an
// older build left unsupported keys (outbounds/inbounds null, awgm_managed).
func (s *ServiceImpl) healLegacySelectiveRoutesSlotIfNeeded(ctx context.Context) error {
	if s.deps.Orch == nil {
		return nil
	}
	path := filepath.Join(s.deps.Orch.ConfigDir(), "19-selective-routes.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if !selectiveRoutesSlotNeedsHeal(data) {
		return nil
	}
	var slot selectiveRoutesSlot
	if err := json.Unmarshal(data, &slot); err != nil {
		var legacy RouterConfig
		if err2 := json.Unmarshal(data, &legacy); err2 != nil {
			_, err3 := s.syncSelectiveRoutesSlot(ctx, nil)
			return err3
		}
		slot.Route.Rules = legacy.Route.Rules
	}
	for i := range slot.Route.Rules {
		slot.Route.Rules[i].AwgmManaged = ""
	}
	fixed, err := marshalSelectiveRoutesSlot(slot.Route.Rules)
	if err != nil {
		return err
	}
	enable := len(slot.Route.Rules) > 0 && s.selectiveActive()
	if err := s.deps.Orch.SaveSilent(orchestrator.SlotSelectiveRoutes, fixed); err != nil {
		return err
	}
	return s.deps.Orch.SetEnabledSilent(orchestrator.SlotSelectiveRoutes, enable)
}

func selectiveRoutesSlotNeedsHeal(data []byte) bool {
	s := string(data)
	return bytesContainsLegacyNullSlotKeys(data) ||
		strings.Contains(s, `"awgm_managed"`)
}

func bytesContainsLegacyNullSlotKeys(data []byte) bool {
	s := string(data)
	return strings.Contains(s, `"outbounds"`) || strings.Contains(s, `"inbounds"`)
}

// selectiveActive reports whether селективный перехват реально действует
// (storage.SingboxRouterSettings.SelectiveActive по текущим настройкам).
// Без Settings-депа (юнит-тесты пайплайна) — активен; ошибка загрузки —
// fail-closed: слот в таком состоянии не включаем.
func (s *ServiceImpl) selectiveActive() bool {
	if s.deps.Settings == nil {
		return true
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return false
	}
	return settings.SingboxRouter.SelectiveActive()
}

// disableSelectiveRoutesSlot turns off the overlay slot when selective bypass
// is disabled.
func (s *ServiceImpl) disableSelectiveRoutesSlot() error {
	if s.deps.Orch == nil {
		return nil
	}
	return s.deps.Orch.SetEnabledSilent(orchestrator.SlotSelectiveRoutes, false)
}

func normalizeSelectiveIPCIDR(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if _, ipnet, err := net.ParseCIDR(raw); err == nil {
		if ipnet.IP.To4() == nil {
			return ""
		}
		return ipnet.String()
	}
	if ip := net.ParseIP(raw); ip != nil && ip.To4() != nil {
		return ip.To4().String() + "/32"
	}
	return ""
}
