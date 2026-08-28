package subscription

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// DropReason records one outbound that was filtered out of a subscription,
// for surfacing back to the user (UI / app log) so they understand which
// servers were skipped and why.
type DropReason struct {
	Tag    string `json:"tag"`
	Reason string `json:"reason"`
}

// preFilterOutbounds runs the in-Go Pass 1 of the subscription validation
// pipeline (issue #221). Drops outbounds that violate structural rules
// we are confident will not move in upstream sing-box (UUID format,
// reality-requires-uTLS, required field presence). Group outbounds
// (selector / urltest) are NOT filtered here — that happens during
// reference cleanup after a leaf outbound is removed.
//
// singboxFeatures is the list of build tags from the installed sing-box
// binary (Features field of InstallStatus). When non-empty, outbounds
// that require an optional tag (e.g. naive needs
// with_naive_outbound) are dropped early with a user-facing reason
// instead of waiting for `sing-box check` to report "unknown outbound
// type".
//
// Returns the kept outbounds (same shape) and the list of dropped ones
// with a human-readable reason. Outbounds whose shape we cannot parse
// (non-object) are dropped with reason "non-object outbound" — sing-box
// would reject the whole config on those anyway.
func preFilterOutbounds(outbounds []any, singboxFeatures []string) ([]any, []DropReason) {
	kept := make([]any, 0, len(outbounds))
	dropped := []DropReason{}
	for _, raw := range outbounds {
		ob, ok := raw.(map[string]any)
		if !ok {
			dropped = append(dropped, DropReason{Tag: "", Reason: "non-object outbound"})
			continue
		}
		if reason := classifyOutbound(ob); reason != "" {
			dropped = append(dropped, DropReason{Tag: outboundTag(ob), Reason: reason})
			continue
		}
		if len(singboxFeatures) > 0 {
			if reason := classifyOutboundFeatureSupport(ob, singboxFeatures); reason != "" {
				dropped = append(dropped, DropReason{Tag: outboundTag(ob), Reason: reason})
				continue
			}
		}
		kept = append(kept, ob)
	}
	return kept, dropped
}

// classifyOutbound returns "" when the outbound passes Pass 1 rules,
// or a short human-readable reason string when it should be dropped.
func classifyOutbound(ob map[string]any) string {
	typ, _ := ob["type"].(string)
	if typ == "" {
		return "missing type"
	}
	// Group outbounds: validated after leaf cleanup; skip here.
	if typ == "selector" || typ == "urltest" {
		return ""
	}
	// Universal required fields for connect-style outbounds.
	srv, ok := ob["server"].(string)
	if !ok || srv == "" {
		// Direct/block/dns don't take a server — exempt those.
		switch typ {
		case "direct", "block", "dns":
			// ok
		default:
			return "missing server"
		}
	}
	port := portValue(ob["server_port"])
	if port < 1 || port > 65535 {
		switch typ {
		case "direct", "block", "dns":
			// ok
		default:
			return "missing or invalid server_port"
		}
	}
	// Type-specific rules.
	switch typ {
	case "vless", "vmess":
		if !isValidUUID(stringOf(ob["uuid"])) {
			return "invalid uuid format"
		}
		if reason := checkRealityUTLS(ob); reason != "" {
			return reason
		}
	case "trojan":
		// trojan uses password, not uuid; reality+utls coupling still applies.
		if reason := checkRealityUTLS(ob); reason != "" {
			return reason
		}
	case "hysteria2":
		if stringOf(ob["password"]) == "" {
			return "missing hysteria2 password"
		}
	case "shadowsocks":
		if stringOf(ob["password"]) == "" {
			return "missing shadowsocks password"
		}
		if stringOf(ob["method"]) == "" {
			return "missing shadowsocks method"
		}
	}

	if srv != "" {
		lowerSrv := strings.ToLower(strings.TrimSpace(srv))
		if lowerSrv == "0.0.0.0" || (port == 1 && (lowerSrv == "127.0.0.1" || lowerSrv == "localhost" || lowerSrv == "::1")) {
			switch typ {
			case "direct", "block", "dns":
				// ok
			default:
				return "non-routable/dummy host"
			}
		}
	}
	return ""
}

// classifyOutboundFeatureSupport returns "" when the outbound's required
// build tag (if any) is present in singboxFeatures. Otherwise returns a
// short human-readable reason string like
// "naive: missing sing-box build tag with_naive_outbound,
// update sing-box to a version supporting naive".
func classifyOutboundFeatureSupport(ob map[string]any, singboxFeatures []string) string {
	typ, _ := ob["type"].(string)
	if typ == "" || typ == "selector" || typ == "urltest" {
		return ""
	}
	required := singbox.OutboundTypeRequiresFeature(typ)
	if required == "" {
		return ""
	}
	if singbox.OutboundSupportedByFeatures(singboxFeatures, typ) {
		return ""
	}
	return fmt.Sprintf(
		"%s: missing sing-box build tag %q, update sing-box to a version supporting %s",
		typ, required, typ,
	)
}

// checkRealityUTLS reports the "reality requires uTLS" structural rule
// that caused issue #221. Reality (TLS escape protocol) on the client
// side ONLY works when uTLS fingerprinting is also enabled — sing-box
// rejects the outbound with FATAL "uTLS is required by reality client"
// at process init, killing the whole config.
func checkRealityUTLS(ob map[string]any) string {
	tls, _ := ob["tls"].(map[string]any)
	if tls == nil {
		return ""
	}
	reality, _ := tls["reality"].(map[string]any)
	if reality == nil {
		return ""
	}
	enabled, _ := reality["enabled"].(bool)
	if !enabled {
		return ""
	}
	utls, _ := tls["utls"].(map[string]any)
	if utls == nil {
		return "reality requires utls block"
	}
	utlsEnabled, _ := utls["enabled"].(bool)
	if !utlsEnabled {
		return "reality requires utls.enabled=true"
	}
	return ""
}

// outboundTag extracts the "tag" field from an outbound map. Empty
// string when missing — caller decides how to surface that.
func outboundTag(ob map[string]any) string {
	return stringOf(ob["tag"])
}

// stringOf returns v as string when it is one, otherwise "".
func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// portValue coerces a JSON-decoded port value (float64 by default) to int.
// Returns 0 when not numeric.
func portValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// uuidRe matches the canonical RFC-4122 UUID textual form: 8-4-4-4-12
// hex digits. Sing-box accepts only this form for vless/vmess uuid.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isValidUUID(s string) bool {
	return uuidRe.MatchString(s)
}

// subscriptionsOutboundIndex returns the local index of the outbound a
// sing-box check failure was attributed to, when it belongs to our slot.
// Attribution happens in orchestrator.CheckMerged — only it knows the
// snapshot composition (sing-box reports initialize errors with an index
// into the MERGED outbounds array). An error attributed to another slot's
// outbound — or not attributed at all — returns false: we cannot
// self-heal by dropping one of ours.
func subscriptionsOutboundIndex(res orchestrator.ValidationResult) (int, bool) {
	for _, e := range res.Errors {
		if e.OutboundIndex != nil && e.OutboundSlot == orchestrator.SlotSubscriptions {
			return *e.OutboundIndex, true
		}
	}
	return -1, false
}

// dropOutboundAndCleanRefs removes the outbound at index `idx` from cfg
// and cascades the cleanup: any selector / urltest / route rule that
// referenced its tag is updated (tag removed from outbound lists,
// rules dropped, selector.default re-pointed if it became dangling,
// empty groups removed). Mutates cfg in place; returns the dropped tag
// for caller's bookkeeping.
func dropOutboundAndCleanRefs(cfg *slotConfig, idx int) (string, error) {
	if idx < 0 || idx >= len(cfg.Outbounds) {
		return "", fmt.Errorf("dropOutboundAndCleanRefs: idx %d out of range [0,%d)", idx, len(cfg.Outbounds))
	}
	ob, _ := cfg.Outbounds[idx].(map[string]any)
	tag := outboundTag(ob)
	cfg.Outbounds = append(cfg.Outbounds[:idx], cfg.Outbounds[idx+1:]...)
	if tag == "" {
		// No tag to cascade; nothing else references it by name.
		return "", nil
	}
	cleanReferencesToTag(cfg, tag)
	return tag, nil
}

// cleanReferencesToTag walks outbounds (selectors / urltests) and route
// rules, removing every reference to the dropped tag. Group outbounds
// that lose all members are themselves dropped (recursively cascading).
func cleanReferencesToTag(cfg *slotConfig, tag string) {
	// Iterate by index because we may mutate the slice (drop empty groups).
	for i := 0; i < len(cfg.Outbounds); {
		ob, ok := cfg.Outbounds[i].(map[string]any)
		if !ok {
			i++
			continue
		}
		typ, _ := ob["type"].(string)
		if typ != "selector" && typ != "urltest" {
			i++
			continue
		}
		raw, _ := ob["outbounds"].([]any)
		filtered := raw[:0:0]
		for _, t := range raw {
			if stringOf(t) != tag {
				filtered = append(filtered, t)
			}
		}
		ob["outbounds"] = filtered
		// selector.default may point at the dropped tag; re-aim or drop selector.
		if def, _ := ob["default"].(string); def == tag {
			if len(filtered) > 0 {
				ob["default"] = stringOf(filtered[0])
			} else {
				delete(ob, "default")
			}
		}
		// Empty group → drop the group itself (also recursive).
		if len(filtered) == 0 {
			groupTag := outboundTag(ob)
			cfg.Outbounds = append(cfg.Outbounds[:i], cfg.Outbounds[i+1:]...)
			if groupTag != "" && groupTag != tag {
				cleanReferencesToTag(cfg, groupTag)
			}
			continue
		}
		i++
	}

	// Route rules referencing dropped tag.
	if cfg.Route != nil {
		if rawRules, ok := cfg.Route["rules"].([]any); ok {
			filtered := rawRules[:0:0]
			for _, raw := range rawRules {
				rule, ok := raw.(map[string]any)
				if !ok {
					filtered = append(filtered, raw)
					continue
				}
				if outb, _ := rule["outbound"].(string); outb == tag {
					continue
				}
				filtered = append(filtered, rule)
			}
			cfg.Route["rules"] = filtered
		}
		if final, _ := cfg.Route["final"].(string); final == tag {
			delete(cfg.Route, "final")
		}
	}
}

// formatDropList returns a compact "tag (reason); tag (reason); ..."
// string for app-log lines. Empty input returns "".
func formatDropList(drops []DropReason) string {
	if len(drops) == 0 {
		return ""
	}
	parts := make([]string, 0, len(drops))
	for _, d := range drops {
		tag := d.Tag
		if tag == "" {
			tag = "<no-tag>"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", tag, d.Reason))
	}
	return strings.Join(parts, "; ")
}
