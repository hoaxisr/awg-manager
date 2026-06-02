package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	datRuleSetTokenFile = "token"
	datRuleSetMetaExt   = ".meta.json"
)

type datRuleSetMeta struct {
	Kind        string `json:"kind"`
	Tag         string `json:"tag"`
	SourcePath  string `json:"sourcePath"`
	SourceSize  int64  `json:"sourceSize"`
	SourceMtime int64  `json:"sourceMtime"`
}

func (s *ServiceImpl) DatRuleSetURL(_ context.Context, kind, tag string) (string, error) {
	kind, tag, err := normalizeDatRuleSetInput(kind, tag)
	if err != nil {
		return "", err
	}
	token, err := s.ensureDatRuleSetToken()
	if err != nil {
		return "", err
	}
	port := 0
	if s.deps.Settings != nil {
		settings, err := s.deps.Settings.Get()
		if err != nil {
			return "", fmt.Errorf("load settings: %w", err)
		}
		port = settings.Server.Port
	}
	if port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	q := url.Values{}
	q.Set("kind", kind)
	q.Set("tag", tag)
	q.Set("token", token)
	return fmt.Sprintf("http://127.0.0.1:%d/api/singbox/router/rulesets/dat-srs?%s", port, q.Encode()), nil
}

func (s *ServiceImpl) DatRuleSetFile(_ context.Context, kind, tag, token string) (string, error) {
	kind, tag, err := normalizeDatRuleSetInput(kind, tag)
	if err != nil {
		return "", err
	}
	wantToken, err := s.ensureDatRuleSetToken()
	if err != nil {
		return "", err
	}
	if token == "" || token != wantToken {
		return "", ErrDatRuleSetForbidden
	}
	if s.deps.GeoData == nil {
		return "", fmt.Errorf("geo data store not initialized")
	}

	s.datRuleSetMu.Lock()
	defer s.datRuleSetMu.Unlock()

	lines, sourcePath, err := s.deps.GeoData.ExpandGeoTag(kind, tag)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("%s tag %q is empty", kind, tag)
	}
	st, err := os.Stat(sourcePath)
	if err != nil {
		return "", fmt.Errorf("stat source dat file: %w", err)
	}

	base := safeRuleSetFilename(kind + "-" + tag)
	dir, err := s.datRuleSetDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir dat rule-set dir: %w", err)
	}
	jsonPath := filepath.Join(dir, base+".json")
	srsPath := filepath.Join(dir, base+".srs")
	metaPath := filepath.Join(dir, base+datRuleSetMetaExt)
	meta := datRuleSetMeta{
		Kind:        kind,
		Tag:         tag,
		SourcePath:  sourcePath,
		SourceSize:  st.Size(),
		SourceMtime: st.ModTime().UnixNano(),
	}
	if datRuleSetCacheValid(srsPath, metaPath, meta) {
		return srsPath, nil
	}

	rules, err := datLinesToRuleSetRules(kind, lines)
	if err != nil {
		return "", err
	}
	_, sourceJSON, err := buildInlineRuleSetSource(rules)
	if err != nil {
		return "", err
	}
	binary := ""
	if s.deps.Singbox != nil {
		binary = s.deps.Singbox.Binary()
	}
	if err := compileDatRuleSet(binary, dir, base, jsonPath, srsPath, metaPath, sourceJSON, meta); err != nil {
		return "", err
	}
	return srsPath, nil
}

func normalizeDatRuleSetInput(kind, tag string) (string, string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	tag = strings.TrimSpace(tag)
	if kind != "geosite" && kind != "geoip" {
		return "", "", fmt.Errorf("unknown dat rule-set kind %q", kind)
	}
	if tag == "" {
		return "", "", fmt.Errorf("dat rule-set tag is required")
	}
	return kind, tag, nil
}

func (s *ServiceImpl) datRuleSetDir() (string, error) {
	configDir := ""
	if s.deps.Orch != nil {
		configDir = s.deps.Orch.ConfigDir()
	} else if s.deps.Singbox != nil {
		configDir = s.deps.Singbox.ConfigDir()
	}
	if configDir == "" {
		return "", fmt.Errorf("sing-box config dir is not available")
	}
	return filepath.Join(configDir, "rule-sets", "dat"), nil
}

func (s *ServiceImpl) ensureDatRuleSetToken() (string, error) {
	dir, err := s.datRuleSetDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, datRuleSetTokenFile)
	if raw, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(raw))
		if token != "" {
			return token, nil
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir dat rule-set dir: %w", err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate dat rule-set token: %w", err)
	}
	token := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write dat rule-set token: %w", err)
	}
	return token, nil
}

func datRuleSetCacheValid(srsPath, metaPath string, want datRuleSetMeta) bool {
	if !regularFileExists(srsPath) {
		return false
	}
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}
	var got datRuleSetMeta
	if err := json.Unmarshal(raw, &got); err != nil {
		return false
	}
	return got == want
}

func datLinesToRuleSetRules(kind string, lines []string) ([]map[string]any, error) {
	switch kind {
	case "geoip":
		cidrs := dedupeStrings(lines)
		if len(cidrs) == 0 {
			return nil, fmt.Errorf("geoip tag has no CIDR entries")
		}
		return []map[string]any{{"ip_cidr": cidrs}}, nil
	case "geosite":
		domains := make([]string, 0)
		suffixes := make([]string, 0, len(lines))
		keywords := make([]string, 0)
		regexes := make([]string, 0)
		for _, raw := range lines {
			line := strings.TrimSpace(raw)
			if line == "" {
				continue
			}
			switch {
			case strings.HasPrefix(line, "domain_regex:"):
				regexes = append(regexes, strings.TrimSpace(strings.TrimPrefix(line, "domain_regex:")))
			case strings.HasPrefix(line, "domain_keyword:"):
				keywords = append(keywords, strings.TrimSpace(strings.TrimPrefix(line, "domain_keyword:")))
			case strings.HasPrefix(line, "domain:"):
				domains = append(domains, strings.TrimSpace(strings.TrimPrefix(line, "domain:")))
			case strings.HasPrefix(line, "suffix:"):
				suffixes = append(suffixes, strings.TrimSpace(strings.TrimPrefix(line, "suffix:")))
			default:
				suffixes = append(suffixes, line)
			}
		}
		rule := make(map[string]any)
		if v := dedupeStrings(keywords); len(v) > 0 {
			rule["domain_keyword"] = v
		}
		if v := dedupeStrings(suffixes); len(v) > 0 {
			rule["domain_suffix"] = v
		}
		if v := dedupeStrings(domains); len(v) > 0 {
			rule["domain"] = v
		}
		if v := dedupeStrings(regexes); len(v) > 0 {
			rule["domain_regex"] = v
		}
		if len(rule) == 0 {
			return nil, fmt.Errorf("geosite tag has no domain entries")
		}
		return []map[string]any{rule}, nil
	default:
		return nil, fmt.Errorf("unknown dat rule-set kind %q", kind)
	}
}

func dedupeStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func compileDatRuleSet(binary, dir, base, jsonPath, srsPath, metaPath string, sourceJSON []byte, meta datRuleSetMeta) error {
	if strings.TrimSpace(binary) == "" {
		return fmt.Errorf("sing-box binary is required to compile dat rule-set")
	}
	tmpSource, err := os.CreateTemp(dir, base+"-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create dat source temp: %w", err)
	}
	tmpSourcePath := tmpSource.Name()
	if _, err := tmpSource.Write(sourceJSON); err != nil {
		_ = tmpSource.Close()
		_ = os.Remove(tmpSourcePath)
		return fmt.Errorf("write dat source temp: %w", err)
	}
	if err := tmpSource.Close(); err != nil {
		_ = os.Remove(tmpSourcePath)
		return fmt.Errorf("close dat source temp: %w", err)
	}

	tmpOut, err := os.CreateTemp(dir, base+"-*.srs.tmp")
	if err != nil {
		_ = os.Remove(tmpSourcePath)
		return fmt.Errorf("create dat output temp: %w", err)
	}
	tmpOutPath := tmpOut.Name()
	_ = tmpOut.Close()

	args := []string{"rule-set", "compile", "--output", tmpOutPath, tmpSourcePath}
	_, stderr, err := inlineRuleSetCompileExec(binary, args)
	if err != nil {
		_ = os.Remove(tmpSourcePath)
		_ = os.Remove(tmpOutPath)
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("compile dat rule-set: %s", msg)
	}
	if !regularFileExists(tmpOutPath) {
		_ = os.Remove(tmpSourcePath)
		return fmt.Errorf("compile dat rule-set: output file was not created")
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		_ = os.Remove(tmpSourcePath)
		_ = os.Remove(tmpOutPath)
		return fmt.Errorf("marshal dat rule-set metadata: %w", err)
	}
	if err := os.Rename(tmpSourcePath, jsonPath); err != nil {
		_ = os.Remove(tmpSourcePath)
		_ = os.Remove(tmpOutPath)
		return fmt.Errorf("publish dat source: %w", err)
	}
	if err := os.Rename(tmpOutPath, srsPath); err != nil {
		_ = os.Remove(tmpOutPath)
		return fmt.Errorf("publish dat binary: %w", err)
	}
	if err := os.WriteFile(metaPath, append(metaJSON, '\n'), 0644); err != nil {
		return fmt.Errorf("publish dat metadata: %w", err)
	}
	return nil
}
