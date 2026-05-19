package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const inlineRuleSetSourceVersion = 5

var safeRuleSetTagRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type inlineRuleSetSource struct {
	Version int              `json:"version"`
	Rules   []map[string]any `json:"rules"`
}

type ruleSetMaterializer struct {
	configDir string
	binary    string
}

var inlineRuleSetCompileExec = func(binary string, args []string) (stdout, stderr string, err error) {
	cmd := exec.Command(binary, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

func (m ruleSetMaterializer) materializeConfig(cfg *RouterConfig) (*RouterConfig, error) {
	if cfg == nil {
		return nil, nil
	}
	out := *cfg
	out.Route = cfg.Route
	out.Route.RuleSet = make([]RuleSet, 0, len(cfg.Route.RuleSet))
	for _, rs := range cfg.Route.RuleSet {
		if rs.Type != "inline" {
			out.Route.RuleSet = append(out.Route.RuleSet, rs)
			continue
		}
		local, err := m.materializeRuleSet(rs)
		if err != nil {
			return nil, err
		}
		out.Route.RuleSet = append(out.Route.RuleSet, local)
	}
	return &out, nil
}

func (m ruleSetMaterializer) restoreConfig(cfg *RouterConfig) *RouterConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.Route = cfg.Route
	out.Route.RuleSet = make([]RuleSet, 0, len(cfg.Route.RuleSet))
	for _, rs := range cfg.Route.RuleSet {
		out.Route.RuleSet = append(out.Route.RuleSet, m.restoreRuleSet(rs))
	}
	return &out
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
	hash := sha256.Sum256(sourceJSON)
	hashText := hex.EncodeToString(hash[:])[:16]
	base := safeRuleSetFilename(rs.Tag) + "-" + hashText
	dir := filepath.Join(m.configDir, "rule-sets", "inline")
	srsPath := filepath.Join(dir, base+".srs")
	jsonPath := filepath.Join(dir, base+".json")

	if regularFileExists(srsPath) && regularFileExists(jsonPath) {
		return managedLocalRuleSet(rs.Tag, srsPath), nil
	}
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
	_ = os.Remove(tmpOutPath)

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

	return managedLocalRuleSet(rs.Tag, srsPath), nil
}

func (m ruleSetMaterializer) restoreRuleSet(rs RuleSet) RuleSet {
	if !m.isManagedLocalRuleSet(rs) {
		return rs
	}
	raw, err := os.ReadFile(strings.TrimSuffix(rs.Path, ".srs") + ".json")
	if err != nil {
		return rs
	}
	var source inlineRuleSetSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return rs
	}
	if len(source.Rules) == 0 {
		return rs
	}
	return RuleSet{Tag: rs.Tag, Type: "inline", Rules: source.Rules}
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
