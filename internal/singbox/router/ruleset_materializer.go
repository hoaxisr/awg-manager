package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

const (
	inlineRuleSetSourceVersion = 5
	inlineSRSSuffix            = "-srs"
)

var safeRuleSetTagRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type inlineRuleSetSource struct {
	Version int              `json:"version"`
	Rules   []map[string]any `json:"rules"`
}

type ruleSetMaterializer struct {
	configDir string
	binary    string
	log       *logging.ScopedLogger
}

var inlineRuleSetCompileExec = func(binary string, args []string) (stdout, stderr string, err error) {
	cmd := exec.Command(binary, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

func inlineSRSTag(inlineTag string) string {
	return inlineTag + inlineSRSSuffix
}

func inlineTagFromSRSTag(tag string) (string, bool) {
	if !strings.HasSuffix(tag, inlineSRSSuffix) {
		return "", false
	}
	base := strings.TrimSuffix(tag, inlineSRSSuffix)
	if base == "" {
		return "", false
	}
	return base, true
}

func ruleSetTagsWithCompanion(tag string) []string {
	if _, ok := inlineTagFromSRSTag(tag); ok {
		return []string{tag}
	}
	return []string{tag, inlineSRSTag(tag)}
}

func (m ruleSetMaterializer) materializeConfig(cfg *RouterConfig) (*RouterConfig, error) {
	if cfg == nil {
		return nil, nil
	}
	working := m.expandManagedToInline(cfg)
	out := *working
	out.Route = working.Route
	out.Route.RuleSet = make([]RuleSet, 0, len(working.Route.RuleSet))

	var inlineSets []RuleSet
	for _, rs := range working.Route.RuleSet {
		if rs.Type == "inline" {
			inlineSets = append(inlineSets, rs)
			continue
		}
		if m.isManagedLocalRuleSet(rs) {
			continue
		}
		out.Route.RuleSet = append(out.Route.RuleSet, rs)
	}
	for _, rs := range inlineSets {
		local, err := m.materializeRuleSet(rs)
		if err != nil {
			return nil, err
		}
		m.rewriteRuleSetRefs(&out, rs.Tag, local.Tag)
		out.Route.RuleSet = append(out.Route.RuleSet, local)
	}
	return &out, nil
}

func (m ruleSetMaterializer) expandManagedToInline(cfg *RouterConfig) *RouterConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.Route = cfg.Route
	out.Route.RuleSet = make([]RuleSet, len(cfg.Route.RuleSet))
	copy(out.Route.RuleSet, cfg.Route.RuleSet)
	for i, rs := range out.Route.RuleSet {
		if m.isManagedLocalRuleSet(rs) {
			out.Route.RuleSet[i] = m.restoreRuleSet(rs)
		}
	}
	return &out
}

func (m ruleSetMaterializer) restoreConfig(cfg *RouterConfig) *RouterConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.Route = cfg.Route
	out.Route.RuleSet = make([]RuleSet, 0, len(cfg.Route.RuleSet))
	inlineSeen := make(map[string]struct{}, len(cfg.Route.RuleSet))

	for _, rs := range cfg.Route.RuleSet {
		if m.isManagedLocalRuleSet(rs) {
			inline := m.restoreRuleSet(rs)
			inline.MaterializedSRS = true
			if _, ok := inlineSeen[inline.Tag]; ok {
				continue
			}
			inlineSeen[inline.Tag] = struct{}{}
			out.Route.RuleSet = append(out.Route.RuleSet, inline)
			continue
		}
		if base, ok := inlineTagFromSRSTag(rs.Tag); ok {
			if _, seen := inlineSeen[base]; seen {
				continue
			}
			if rs.Type == "inline" {
				rs.MaterializedSRS = m.hasManagedSRSCompanion(cfg, base)
				inlineSeen[base] = struct{}{}
				out.Route.RuleSet = append(out.Route.RuleSet, rs)
				continue
			}
			continue
		}
		if rs.Type == "inline" {
			rs.MaterializedSRS = m.hasManagedSRSCompanion(cfg, rs.Tag)
			inlineSeen[rs.Tag] = struct{}{}
		}
		out.Route.RuleSet = append(out.Route.RuleSet, rs)
	}
	// Rewrite rule_set refs using the on-disk rule_set slice: out.Route.RuleSet
	// no longer contains managed local entries (they were projected to inline).
	m.rewritePersistedSRSRefsToInline(cfg, &out)
	m.rewriteSRSSuffixRuleSetRefs(&out)
	return &out
}

func (m ruleSetMaterializer) rewritePersistedSRSRefsToInline(src, dst *RouterConfig) {
	for _, rs := range src.Route.RuleSet {
		if !m.isManagedLocalRuleSet(rs) {
			continue
		}
		inlineTag, ok := inlineTagFromSRSTag(rs.Tag)
		if !ok {
			inlineTag = rs.Tag
		}
		m.rewriteRuleSetRefs(dst, rs.Tag, inlineTag)
	}
}

// rewriteSRSSuffixRuleSetRefs strips reserved -srs suffixes from rule_set tags
// in route/DNS rules so the UI always shows the inline tag (defense in depth).
func (m ruleSetMaterializer) rewriteSRSSuffixRuleSetRefs(cfg *RouterConfig) {
	for i := range cfg.Route.Rules {
		cfg.Route.Rules[i].RuleSet = rewriteRuleSetTagsStripSRSSuffix(cfg.Route.Rules[i].RuleSet)
	}
	for i := range cfg.DNS.Rules {
		cfg.DNS.Rules[i].RuleSet = rewriteRuleSetTagsStripSRSSuffix(cfg.DNS.Rules[i].RuleSet)
	}
}

// mapTagSlice returns tags with each element transformed by fn (which reports
// the replacement and whether it changed). When nothing matched it returns the
// ORIGINAL slice (same backing array), so callers avoid needless allocations
// and reconcile-churn. Single skeleton behind the tag/rule-set rewriters.
func mapTagSlice(tags []string, fn func(string) (string, bool)) []string {
	if len(tags) == 0 {
		return tags
	}
	out := make([]string, len(tags))
	changed := false
	for i, tag := range tags {
		if v, ok := fn(tag); ok {
			out[i] = v
			changed = true
		} else {
			out[i] = tag
		}
	}
	if !changed {
		return tags
	}
	return out
}

func rewriteRuleSetTagsStripSRSSuffix(tags []string) []string {
	return mapTagSlice(tags, inlineTagFromSRSTag)
}

func (m ruleSetMaterializer) rewriteRuleSetRefs(cfg *RouterConfig, from, to string) {
	if cfg == nil || from == "" || to == "" || from == to {
		return
	}
	for i := range cfg.Route.Rules {
		cfg.Route.Rules[i].RuleSet = rewriteRuleSetSlice(cfg.Route.Rules[i].RuleSet, from, to)
	}
	for i := range cfg.DNS.Rules {
		cfg.DNS.Rules[i].RuleSet = rewriteRuleSetSlice(cfg.DNS.Rules[i].RuleSet, from, to)
	}
}

func rewriteRuleSetSlice(tags []string, from, to string) []string {
	return mapTagSlice(tags, func(tag string) (string, bool) {
		return to, tag == from
	})
}

func (m ruleSetMaterializer) hasManagedSRSCompanion(cfg *RouterConfig, inlineTag string) bool {
	want := inlineSRSTag(inlineTag)
	for _, rs := range cfg.Route.RuleSet {
		if rs.Tag == want && m.isManagedLocalRuleSet(rs) {
			return true
		}
	}
	return false
}

func (m ruleSetMaterializer) materializeRuleSet(rs RuleSet) (RuleSet, error) {
	if m.configDir == "" {
		return RuleSet{}, fmt.Errorf("rule_set %q: config dir is required to compile inline rules", rs.Tag)
	}
	if strings.TrimSpace(m.binary) == "" {
		return RuleSet{}, fmt.Errorf("rule_set %q: sing-box binary is required to compile inline rules", rs.Tag)
	}
	_, sourceJSON, err := buildInlineRuleSetSource(rs.Rules)
	if err != nil {
		return RuleSet{}, fmt.Errorf("rule_set %q: %w", rs.Tag, err)
	}
	base := safeRuleSetFilename(rs.Tag)
	dir := filepath.Join(m.configDir, "rule-sets", "inline")
	jsonPath := filepath.Join(dir, base+".json")
	srsPath := filepath.Join(dir, base+".srs")

	if err := os.MkdirAll(dir, 0755); err != nil {
		return RuleSet{}, fmt.Errorf("mkdir inline rule-set dir: %w", err)
	}

	tmpSource, err := os.CreateTemp(dir, base+"-*.json.tmp")
	if err != nil {
		return RuleSet{}, fmt.Errorf("create source temp: %w", err)
	}
	tmpSourcePath := tmpSource.Name()
	if _, err := tmpSource.Write(sourceJSON); err != nil {
		_ = tmpSource.Close()
		_ = os.Remove(tmpSourcePath)
		return RuleSet{}, fmt.Errorf("write source temp: %w", err)
	}
	if err := tmpSource.Close(); err != nil {
		_ = os.Remove(tmpSourcePath)
		return RuleSet{}, fmt.Errorf("close source temp: %w", err)
	}

	tmpOut, err := os.CreateTemp(dir, base+"-*.srs.tmp")
	if err != nil {
		_ = os.Remove(tmpSourcePath)
		return RuleSet{}, fmt.Errorf("create output temp: %w", err)
	}
	tmpOutPath := tmpOut.Name()
	_ = tmpOut.Close()

	args := []string{"rule-set", "compile", "--output", tmpOutPath, tmpSourcePath}
	_, stderr, err := inlineRuleSetCompileExec(m.binary, args)
	if err != nil {
		_ = os.Remove(tmpSourcePath)
		_ = os.Remove(tmpOutPath)
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return RuleSet{}, fmt.Errorf("compile inline rule-set: %s", msg)
	}
	if !regularFileExists(tmpOutPath) {
		_ = os.Remove(tmpSourcePath)
		return RuleSet{}, fmt.Errorf("compile inline rule-set: output file was not created")
	}
	if err := os.Rename(tmpSourcePath, jsonPath); err != nil {
		_ = os.Remove(tmpSourcePath)
		_ = os.Remove(tmpOutPath)
		return RuleSet{}, fmt.Errorf("publish source: %w", err)
	}
	if err := os.Rename(tmpOutPath, srsPath); err != nil {
		_ = os.Remove(tmpOutPath)
		return RuleSet{}, fmt.Errorf("publish binary: %w", err)
	}

	if m.log != nil {
		m.log.Info(
			"materialize",
			rs.Tag,
			fmt.Sprintf("compiled inline rule-set %q to %s (%s)", rs.Tag, srsPath, inlineSRSTag(rs.Tag)),
		)
	}

	return managedLocalRuleSet(inlineSRSTag(rs.Tag), srsPath), nil
}

func (m ruleSetMaterializer) removeInlineArtifacts(tag string) {
	if m.configDir == "" || tag == "" {
		return
	}
	base := safeRuleSetFilename(tag)
	dir := filepath.Join(m.configDir, "rule-sets", "inline")
	for _, name := range []string{base + ".json", base + ".srs"} {
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) && m.log != nil {
			m.log.Warn("cleanup", tag, fmt.Sprintf("remove %s: %v", path, err))
		}
	}
}

func (m ruleSetMaterializer) restoreRuleSet(rs RuleSet) RuleSet {
	if !m.isManagedLocalRuleSet(rs) {
		return rs
	}
	inlineTag := rs.Tag
	if base, ok := inlineTagFromSRSTag(rs.Tag); ok {
		inlineTag = base
	}
	raw, err := os.ReadFile(strings.TrimSuffix(rs.Path, ".srs") + ".json")
	if err != nil {
		return RuleSet{Tag: inlineTag, Type: "inline", MaterializedSRS: true}
	}
	var source inlineRuleSetSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return RuleSet{Tag: inlineTag, Type: "inline", MaterializedSRS: true}
	}
	if len(source.Rules) == 0 {
		return RuleSet{Tag: inlineTag, Type: "inline", MaterializedSRS: true}
	}
	return RuleSet{
		Tag:             inlineTag,
		Type:            "inline",
		Rules:           source.Rules,
		MaterializedSRS: true,
	}
}

func (m ruleSetMaterializer) isManagedLocalRuleSet(rs RuleSet) bool {
	if rs.Type != "local" || rs.Format != "binary" || rs.Path == "" {
		return false
	}
	inlineDir := filepath.Join(m.configDir, "rule-sets", "inline")
	rel, err := filepath.Rel(inlineDir, rs.Path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return false
	}
	return strings.HasSuffix(rs.Path, ".srs")
}

func buildInlineRuleSetSource(rules []map[string]any) (inlineRuleSetSource, []byte, error) {
	deduped := make([]map[string]any, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		canonical, err := json.Marshal(rule)
		if err != nil {
			return inlineRuleSetSource{}, nil, fmt.Errorf("canonicalize rule: %w", err)
		}
		key := string(canonical)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, rule)
	}
	source := inlineRuleSetSource{Version: inlineRuleSetSourceVersion, Rules: deduped}
	raw, err := json.MarshalIndent(source, "", "  ")
	if err != nil {
		return inlineRuleSetSource{}, nil, err
	}
	return source, append(raw, '\n'), nil
}

func safeRuleSetFilename(tag string) string {
	safe := strings.Trim(safeRuleSetTagRe.ReplaceAllString(tag, "-"), "-")
	if safe == "" {
		return "ruleset"
	}
	return safe
}

func managedLocalRuleSet(tag, path string) RuleSet {
	return RuleSet{
		Tag:    tag,
		Type:   "local",
		Format: "binary",
		Path:   path,
	}
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
