package router

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

var coverageTokenErrorPattern = regexp.MustCompile(`([?&]token=)[^&\s]+`)

// coverageRuleSetDownload shares the on-disk remote cache, but unlike the old
// Inspector wrapper it also cancels an in-flight HTTP request with generation.
var coverageRuleSetDownload = func(ctx context.Context, url, format, via string) (string, error) {
	cache := sharedRemoteCIDRCache()
	if path, ok := cache.freshCachedFile(url, format); ok {
		return path, nil
	}
	return cache.getOrDownloadContext(ctx, url, format, nil, "", via)
}

func redactCoverageIndexError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", coverageTokenErrorPattern.ReplaceAllString(err.Error(), "${1}<redacted>"))
}

func (s *ServiceImpl) activeCoverageIndex() *coverageIndex {
	s.coverageIdxOnce.Do(func() {
		s.coverageIdx = newCoverageIndex(s.buildCoverageIndex, func(message string) {
			s.appLog.Info("coverage-index", "", message)
		})
	})
	return s.coverageIdx
}

// MatchRuleSetsIndexed is deliberately separate from MatchRuleSets: the old
// effective/draft-aware inspector path and sing-box CLI semantics are untouched.
func (s *ServiceImpl) MatchRuleSetsIndexed(ctx context.Context, input string, tags []string) (IndexedRuleSetMatch, error) {
	out, err := s.activeCoverageIndex().indexedMatch(ctx, input, tags)
	return out, redactCoverageIndexError(err)
}

// CoverageIndexSnapshot returns the same applied active-slot generation that
// serves indexed matching. It reads no files on a valid warm hit.
func (s *ServiceImpl) CoverageIndexSnapshot(ctx context.Context, exclusionTags []string) (CoverageSnapshot, error) {
	out, err := s.activeCoverageIndex().snapshotRead(ctx, exclusionTags)
	return out, redactCoverageIndexError(err)
}

// InvalidateCoverageIndex is a synchronous internal signal, not an SSE
// subscription. The events bus may drop slow-subscriber events and is not part
// of the correctness protocol.
func (s *ServiceImpl) InvalidateCoverageIndex() {
	s.activeCoverageIndex().invalidate()
}

// WireCoverageIndexRemoteCache connects publication in the process-wide cache
// shared with fakeip Tier-2. A builder's own fresh download is already part of
// that generation and must not cancel itself; external publications do.
func (s *ServiceImpl) WireCoverageIndexRemoteCache() {
	sharedRemoteCIDRCache().onPublish = func(ctx context.Context) {
		if building, _ := ctx.Value(coverageIndexBuildContextMarker{}).(bool); building {
			return
		}
		s.InvalidateCoverageIndex()
	}
}

func (s *ServiceImpl) appliedCoverageConfig() (*RouterConfig, orchestrator.Slot, error) {
	mode := ""
	if s.deps.Settings != nil {
		settings, err := s.deps.Settings.Load()
		if err != nil {
			return nil, orchestrator.SlotRouter, fmt.Errorf("coverage index: load settings: %w", err)
		}
		if settings != nil {
			mode = settings.SingboxRouter.RoutingMode
		}
	}
	slot := RouterSlotForMode(mode)
	if slot == orchestrator.SlotRouter {
		cfg, err := s.loadAppliedRouterConfig()
		return cfg, slot, err
	}
	if s.deps.Orch == nil {
		return nil, slot, fmt.Errorf("coverage index: fakeip slot requires orchestrator")
	}
	data, err := s.deps.Orch.LoadApplied(orchestrator.SlotFakeIP)
	if err != nil {
		return nil, slot, fmt.Errorf("load applied fakeip config: %w", err)
	}
	cfg, err := parseRouterConfigBytes(data)
	return cfg, slot, err
}

func (s *ServiceImpl) buildCoverageIndex(ctx context.Context, extraTags []string) (_ *coverageIndexSnapshot, retErr error) {
	defer func() { retErr = redactCoverageIndexError(retErr) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, slot, err := s.appliedCoverageConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = NewEmptyConfig()
	}
	materializer := s.ruleSetMaterializer()
	allSets := withRuleSetFetchVia(slot, materializer.inspectRuleSetsWithInlineAliases(cfg))
	byTag := make(map[string]RuleSet, len(allSets))
	for _, rs := range allSets {
		byTag[rs.Tag] = rs
	}

	required := map[string]struct{}{}
	for _, rule := range cfg.Route.Rules {
		for _, tag := range ruleSetTagsInRule(rule, nil) {
			required[tag] = struct{}{}
		}
	}
	requested := make(map[string]struct{}, len(required)+len(extraTags))
	wanted := make(map[string]struct{}, len(required)+len(extraTags))
	for tag := range required {
		requested[tag] = struct{}{}
		wanted[tag] = struct{}{}
		// Applied rules reference X-srs while the restored projection used by
		// Autolist references the inline alias X.
		if base, ok := inlineTagFromSRSTag(tag); ok {
			if _, exists := byTag[base]; exists {
				requested[base] = struct{}{}
				wanted[base] = struct{}{}
			}
		}
	}
	for _, tag := range extraTags {
		requested[tag] = struct{}{}
		wanted[tag] = struct{}{}
	}
	ordered := make([]string, 0, len(wanted))
	for tag := range wanted {
		ordered = append(ordered, tag)
	}
	slices.Sort(ordered)

	snapshot := &coverageIndexSnapshot{
		slot:      string(slot),
		entries:   make(map[string]*coverageIndexEntry, len(ordered)),
		requested: requested,
	}
	for _, tag := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rs, ok := byTag[tag]
		if !ok {
			if _, isRequired := required[tag]; isRequired {
				return nil, fmt.Errorf("coverage index: active rule references undefined rule-set %q", tag)
			}
			continue
		}
		if rs.Type == "remote" {
			refresh := 24 * time.Hour
			if rs.UpdateInterval != "" {
				refresh, err = parseSingboxDuration(rs.UpdateInterval)
				if err != nil || refresh < 0 {
					return nil, fmt.Errorf("coverage index rule-set %q: invalid update_interval", tag)
				}
			}
			if refresh > 0 && (snapshot.refreshAfter == 0 || refresh < snapshot.refreshAfter) {
				snapshot.refreshAfter = refresh
			}
		}
		source, err := s.coverageRuleSetSource(ctx, materializer, rs)
		if err != nil {
			return nil, fmt.Errorf("coverage index rule-set %q: %w", tag, err)
		}
		entry, err := compileCoverageIndexEntry(source.Rules)
		if err != nil {
			return nil, fmt.Errorf("coverage index rule-set %q: %w", tag, err)
		}
		snapshot.entries[tag] = entry
	}
	// Restore once per generation. Warm snapshot reads return the immutable
	// projection and do not read these inline JSON files again.
	restored := materializer.restoreConfig(cfg)
	snapshot.rules = restored.Route.Rules
	snapshot.ruleSets = restored.Route.RuleSet
	return snapshot, nil
}

func (s *ServiceImpl) coverageRuleSetSource(ctx context.Context, materializer ruleSetMaterializer, rs RuleSet) (inlineRuleSetSource, error) {
	if rs.Type == "inline" {
		return inlineRuleSetSource{Rules: rs.Rules}, nil
	}
	var path, format string
	switch rs.Type {
	case "remote":
		if rs.URL == "" {
			return inlineRuleSetSource{}, fmt.Errorf("remote URL is empty")
		}
		format = rs.Format
		if format == "" {
			format = inferFormat(rs.URL)
		}
		local, err := coverageRuleSetDownload(ctx, rs.URL, format, rs.fetchVia)
		if err != nil {
			return inlineRuleSetSource{}, fmt.Errorf("download: %w", err)
		}
		path = local
	case "local":
		if rs.Path == "" {
			return inlineRuleSetSource{}, fmt.Errorf("local path is empty")
		}
		path = rs.Path
		format = rs.Format
		if format == "" {
			format = inferFormat(path)
		}
	default:
		return inlineRuleSetSource{}, fmt.Errorf("unsupported type %q", rs.Type)
	}
	if err := ctx.Err(); err != nil {
		return inlineRuleSetSource{}, err
	}
	var raw []byte
	var err error
	switch {
	case format == "source":
		raw, err = os.ReadFile(path)
	case materializer.isManagedLocalRuleSet(rs):
		// Managed inline SRS has an authoritative JSON sibling published in
		// the same generation. No binary decompile is needed.
		raw, err = os.ReadFile(strings.TrimSuffix(path, ".srs") + ".json")
	default:
		raw, err = ruleSetDecompileExec(ctx, s.singboxBinary(), path)
	}
	if err != nil {
		return inlineRuleSetSource{}, fmt.Errorf("read/decompile: %w", err)
	}
	var source inlineRuleSetSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return inlineRuleSetSource{}, fmt.Errorf("parse source JSON: %w", err)
	}
	return source, nil
}

func compileCoverageIndexEntry(rules []map[string]any) (*coverageIndexEntry, error) {
	entry := &coverageIndexEntry{rules: make([]coverageIndexRule, 0, len(rules))}
	for index, raw := range rules {
		rule, err := compileCoverageIndexRule(raw)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", index, err)
		}
		entry.rules = append(entry.rules, rule)
	}
	return entry, nil
}

func compileCoverageIndexRule(raw map[string]any) (coverageIndexRule, error) {
	var out coverageIndexRule
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var nested any
	for _, key := range keys {
		value := raw[key]
		switch key {
		case "type", "mode":
			text, ok := value.(string)
			if !ok {
				return out, fmt.Errorf("%s must be a string", key)
			}
			if key == "type" {
				out.logical = text == "logical"
				if text != "" && text != "default" && text != "logical" {
					return out, fmt.Errorf("unsupported rule type %q", text)
				}
			} else {
				out.mode = strings.ToLower(text)
			}
		case "invert":
			invert, ok := value.(bool)
			if !ok {
				return out, fmt.Errorf("invert must be boolean")
			}
			out.invert = invert
		case "rules":
			nested = value
		case "domain", "domain_suffix", "domain_keyword", "domain_regex", "ip_cidr", "network", "port", "port_range":
			// Compiled below after the logical/default shape is known.
		default:
			return out, fmt.Errorf("unsupported headless field %q", key)
		}
	}
	if out.logical {
		if out.mode != "and" && out.mode != "or" {
			return out, fmt.Errorf("logical rule mode must be and/or")
		}
		for _, key := range keys {
			switch key {
			case "domain", "domain_suffix", "domain_keyword", "domain_regex", "ip_cidr", "network", "port", "port_range":
				return out, fmt.Errorf("logical rule cannot carry headless default fields")
			}
		}
		children, err := coverageNestedRuleMaps(nested)
		if err != nil {
			return out, err
		}
		if len(children) == 0 {
			return out, fmt.Errorf("logical rule has no children")
		}
		for _, child := range children {
			compiled, err := compileCoverageIndexRule(child)
			if err != nil {
				return out, err
			}
			out.children = append(out.children, compiled)
		}
		return out, nil
	}
	if nested != nil {
		return out, fmt.Errorf("default rule cannot carry rules")
	}
	if out.mode != "" {
		return out, fmt.Errorf("default rule cannot carry mode")
	}
	if err := compileCoverageDestination(&out.dest, raw); err != nil {
		return out, err
	}
	needsContext, err := validateCoverageContextFields(raw)
	if err != nil {
		return out, err
	}
	out.needsContext = needsContext
	if !out.dest.hasFields && !out.needsContext && !out.invert {
		return out, fmt.Errorf("default rule has no evaluable destination condition")
	}
	return out, nil
}

func coverageNestedRuleMaps(raw any) ([]map[string]any, error) {
	switch values := raw.(type) {
	case []map[string]any:
		return values, nil
	case []any:
		out := make([]map[string]any, 0, len(values))
		for index, value := range values {
			child, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("logical child %d must be an object", index)
			}
			out = append(out, child)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("logical rules must be an array")
	}
}

func compileCoverageDestination(dest *coverageDestination, raw map[string]any) error {
	for _, key := range []string{"domain", "domain_suffix", "domain_keyword", "domain_regex", "ip_cidr"} {
		value, exists := raw[key]
		if !exists {
			continue
		}
		values, err := coverageStringList(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if len(values) == 0 {
			continue
		}
		dest.hasFields = true
		switch key {
		case "domain":
			dest.exact = make(map[string]struct{}, len(values))
			for _, value := range values {
				value = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(value), "."), "."))
				if value == "" {
					return fmt.Errorf("domain contains an empty value")
				}
				dest.exact[value] = struct{}{}
			}
		case "domain_suffix":
			dest.suffix = make(map[string]struct{}, len(values))
			for _, value := range values {
				value = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(value), "."), "."))
				if value == "" {
					return fmt.Errorf("domain_suffix contains an empty value")
				}
				dest.suffix[value] = struct{}{}
			}
		case "domain_keyword":
			for _, value := range values {
				value = strings.ToLower(strings.TrimSpace(value))
				if value == "" {
					return fmt.Errorf("domain_keyword contains an empty value")
				}
				dest.keywords = append(dest.keywords, value)
			}
		case "domain_regex":
			for _, value := range values {
				expression, err := regexp.Compile(value)
				if err != nil {
					return fmt.Errorf("domain_regex %q: %w", value, err)
				}
				dest.regexes = append(dest.regexes, expression)
			}
		case "ip_cidr":
			dest.prefixes = &coveragePrefixTrie{}
			for _, value := range values {
				prefix, err := coveragePrefix(value)
				if err != nil {
					return fmt.Errorf("ip_cidr %q: %w", value, err)
				}
				if err := dest.prefixes.insert(prefix); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateCoverageContextFields(raw map[string]any) (bool, error) {
	needsContext := false
	if value, exists := raw["network"]; exists {
		network, ok := value.(string)
		if !ok || (network != "tcp" && network != "udp") {
			return false, fmt.Errorf("network must be tcp or udp")
		}
		needsContext = true
	}
	if value, exists := raw["port"]; exists {
		values, ok := value.([]any)
		if !ok {
			if ints, isInts := value.([]int); isInts {
				values = make([]any, len(ints))
				for index, port := range ints {
					values[index] = port
				}
			} else {
				values = []any{value}
			}
		}
		for _, value := range values {
			if err := validateCoveragePort(value); err != nil {
				return false, err
			}
		}
		needsContext = needsContext || len(values) > 0
	}
	if value, exists := raw["port_range"]; exists {
		values, err := coverageStringList(value)
		if err != nil {
			return false, fmt.Errorf("port_range: %w", err)
		}
		for _, value := range values {
			parts := strings.Split(value, ":")
			if len(parts) != 2 {
				return false, fmt.Errorf("invalid port_range %q", value)
			}
			start, err := strconv.Atoi(parts[0])
			if err != nil {
				return false, fmt.Errorf("invalid port_range %q", value)
			}
			end, err := strconv.Atoi(parts[1])
			if err != nil || start < 1 || end < start || end > 65535 {
				return false, fmt.Errorf("invalid port_range %q", value)
			}
		}
		needsContext = needsContext || len(values) > 0
	}
	return needsContext, nil
}

func validateCoveragePort(value any) error {
	var port int
	switch value := value.(type) {
	case int:
		port = value
	case float64:
		if value != math.Trunc(value) {
			return fmt.Errorf("port must be an integer")
		}
		port = int(value)
	case json.Number:
		parsed, err := strconv.Atoi(value.String())
		if err != nil {
			return fmt.Errorf("port must be an integer")
		}
		port = parsed
	default:
		return fmt.Errorf("port must be numeric")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("port out of range")
	}
	return nil
}

func coverageStringList(raw any) ([]string, error) {
	switch values := raw.(type) {
	case string:
		return []string{values}, nil
	case []string:
		return values, nil
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("expected a string array")
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a string or string array")
	}
}

func coveragePrefix(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "/") {
		addr, err := netip.ParseAddr(value)
		if err != nil || addr.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("invalid IP")
		}
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix.Addr().Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("invalid CIDR")
	}
	if prefix.Addr().Is4In6() {
		if prefix.Bits() < 96 {
			return netip.Prefix{}, fmt.Errorf("invalid mapped IPv4 prefix")
		}
		return netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96).Masked(), nil
	}
	return prefix.Masked(), nil
}
