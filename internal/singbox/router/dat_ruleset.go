package router

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const (
	datRuleSetTokenFile = "token"
	datRuleSetMetaExt   = ".meta.json"
	// datRuleSetFormatVersion invalidates cached .srs artifacts when the
	// dat→rule mapping changes. Version 2: typed geosite expansion (issue
	// #448) — Plain→domain_keyword, Full→domain, RootDomain→suffix+apex.
	// Old meta.json files lack the field (0 ≠ 2), so datRuleSetCacheValid's
	// DeepEqual fails and the rule-set is recompiled with the fixed mapping.
	datRuleSetFormatVersion = 2
)

type datRuleSetMeta struct {
	Kind          string                 `json:"kind"`
	Tags          []string               `json:"tags"`
	Sources       []datRuleSetSourceMeta `json:"sources"`
	FormatVersion int                    `json:"formatVersion"`
}

type datRuleSetSourceMeta struct {
	Tag         string `json:"tag"`
	SourcePath  string `json:"sourcePath"`
	SourceSize  int64  `json:"sourceSize"`
	SourceMtime int64  `json:"sourceMtime"`
}

func (s *ServiceImpl) DatRuleSetURL(_ context.Context, kind string, tags []string) (string, error) {
	kind, tags, err := normalizeDatRuleSetInput(kind, tags)
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
	for _, tag := range tags {
		q.Add("tag", tag)
	}
	q.Set("token", token)
	return fmt.Sprintf("http://127.0.0.1:%d/api/singbox/router/rulesets/dat-srs?%s", port, q.Encode()), nil
}

func (s *ServiceImpl) DatRuleSetFile(ctx context.Context, kind string, tags []string, token string) (string, error) {
	kind, tags, err := normalizeDatRuleSetInput(kind, tags)
	if err != nil {
		return "", err
	}
	wantToken, err := s.ensureDatRuleSetToken()
	if err != nil {
		return "", err
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(wantToken)) != 1 {
		return "", ErrDatRuleSetForbidden
	}
	if s.deps.GeoData == nil {
		return "", fmt.Errorf("geo data store not initialized")
	}

	s.datRuleSetMu.Lock()
	defer s.datRuleSetMu.Unlock()

	// Материализация dat→srs состоит из дорогих фаз (разворачивание тегов из
	// .dat, компиляция srs, публикация файлов), каждая из которых сама по
	// себе не подчинена контексту — между фазами проверяем ctx, чтобы
	// отменённая пересборка (stall guard, отмена пользователем) не продолжала
	// молотить CPU на 580МГц MIPS впустую. Вывод функции при живом ctx
	// не меняется.
	allLines := make([]string, 0)
	sources := make([]datRuleSetSourceMeta, 0, len(tags))
	for _, tag := range tags {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("dat rule-set %s: %w", kind, err)
		}
		lines, sourcePath, err := s.deps.GeoData.ExpandGeoTagTyped(kind, tag)
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
		allLines = append(allLines, lines...)
		sources = append(sources, datRuleSetSourceMeta{
			Tag:         tag,
			SourcePath:  sourcePath,
			SourceSize:  st.Size(),
			SourceMtime: st.ModTime().UnixNano(),
		})
	}
	allLines = dedupeStrings(allLines)
	if len(allLines) == 0 {
		return "", fmt.Errorf("%s tags %q are empty", kind, strings.Join(tags, ", "))
	}

	base := safeRuleSetFilename(kind + "-" + strings.Join(tags, "-"))
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
		Kind:          kind,
		Tags:          tags,
		Sources:       sources,
		FormatVersion: datRuleSetFormatVersion,
	}
	if datRuleSetCacheValid(srsPath, metaPath, meta) {
		return srsPath, nil
	}

	// Фаза разворачивания завершена — перед компиляцией srs ещё раз сверяемся
	// с контекстом (см. комментарий выше).
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("dat rule-set %s: %w", kind, err)
	}
	rules, err := datLinesToRuleSetRules(kind, allLines)
	if err != nil {
		return "", err
	}
	_, sourceJSON, err := buildInlineRuleSetSource(rules)
	if err != nil {
		return "", err
	}
	// Перед записью/публикацией json+srs (последняя дорогая фаза).
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("dat rule-set %s: %w", kind, err)
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

func normalizeDatRuleSetInput(kind string, tags []string) (string, []string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "geosite" && kind != "geoip" {
		return "", nil, fmt.Errorf("unknown dat rule-set kind %q", kind)
	}
	clean := dedupeStrings(tags)
	if len(clean) == 0 {
		return "", nil, fmt.Errorf("dat rule-set tag is required")
	}
	return kind, clean, nil
}

// parseDatRuleSetURL recognizes a rule-set URL that points back at our own
// dat-srs endpoint and extracts its kind/tags.
func parseDatRuleSetURL(rawURL string) (kind string, tags []string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil {
		return "", nil, false
	}
	if !strings.HasSuffix(u.Path, "/dat-srs") && !strings.HasSuffix(u.Path, "dat-srs") {
		return "", nil, false
	}
	kind = strings.ToLower(strings.TrimSpace(u.Query().Get("kind")))
	if kind != "geosite" && kind != "geoip" {
		return "", nil, false
	}
	tags = dedupeStrings(u.Query()["tag"])
	if len(tags) == 0 {
		return "", nil, false
	}
	return kind, tags, true
}

func datRuleSetBaseName(kind string, tags []string) string {
	return safeRuleSetFilename(kind + "-" + strings.Join(tags, "-"))
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
	return reflect.DeepEqual(got, want)
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
			case strings.HasPrefix(line, "keyword:"):
				// typed v2ray Plain — substring semantics
				keywords = append(keywords, strings.TrimSpace(strings.TrimPrefix(line, "keyword:")))
			case strings.HasPrefix(line, "domain_keyword:"):
				keywords = append(keywords, strings.TrimSpace(strings.TrimPrefix(line, "domain_keyword:")))
			case strings.HasPrefix(line, "full:"):
				// typed v2ray Full — exact match
				domains = append(domains, strings.TrimSpace(strings.TrimPrefix(line, "full:")))
			case strings.HasPrefix(line, "domain:"):
				domains = append(domains, strings.TrimSpace(strings.TrimPrefix(line, "domain:")))
			case strings.HasPrefix(line, "suffix:"):
				suffixes = append(suffixes, strings.TrimSpace(strings.TrimPrefix(line, "suffix:")))
			case strings.HasPrefix(line, "."):
				// v2ray RootDomain: sing-box domain_suffix ".x.com" matches
				// subdomains only, NOT the apex — emit the apex as an exact
				// domain too, replicating the official geosite→srs converters
				// (issue #448: "chatgpt.com" itself did not match).
				suffixes = append(suffixes, line)
				if apex := strings.TrimSpace(strings.TrimPrefix(line, ".")); apex != "" {
					domains = append(domains, apex)
				}
			default:
				// bare value (legacy/unknown) — defensive suffix fallback
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
