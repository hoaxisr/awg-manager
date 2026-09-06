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

// inspectRuleSetsWithInlineAliases возвращает rule_set'ы конфига,
// дополненные алиасами: каждый материализованный inline-набор (managed
// local "X-srs") дублируется под базовым тегом "X". Инспектор ходит по
// восстановленным правилам (как их видит пользователь в UI), где ссылки
// указывают на "X" — без алиаса поиск по карте наборов давал «не
// определён в rule_set[]» (#506). Алиас указывает на скомпилированный
// .srs, поэтому матчинг полноценный, пока файл существует на диске
// (иначе инспектор честно деградирует к «не удалось проверить») —
// inline-содержимое сам инспектор проверять не умеет.
//
// Алиасы добавляются в конец: при построении карты «последний
// побеждает», и alias перекрывает возможную сырую inline-запись с тем
// же тегом. Настоящий НЕ-inline набор с тегом "X" (remote/local в
// legacy/ручных конфигах) алиас не затеняет — иначе инспектор молча
// матчил бы не тот файл.
func (m ruleSetMaterializer) inspectRuleSetsWithInlineAliases(cfg *RouterConfig) []RuleSet {
	if cfg == nil {
		return nil
	}
	rawType := make(map[string]string, len(cfg.Route.RuleSet))
	for _, rs := range cfg.Route.RuleSet {
		rawType[rs.Tag] = rs.Type
	}
	out := make([]RuleSet, 0, len(cfg.Route.RuleSet)+4)
	out = append(out, cfg.Route.RuleSet...)
	for _, rs := range cfg.Route.RuleSet {
		if !m.isManagedLocalRuleSet(rs) {
			continue
		}
		base, ok := inlineTagFromSRSTag(rs.Tag)
		if !ok {
			continue
		}
		if t, exists := rawType[base]; exists && t != "inline" {
			continue
		}
		alias := rs
		alias.Tag = base
		out = append(out, alias)
	}
	return out
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
	applyHTTPClients(&out)
	return &out, nil
}

// ruleSetHTTPClientTag — тег общего HTTP-клиента загрузки наборов.
const ruleSetHTTPClientTag = "rs-download"

// ruleSetDirectClientPrefix — префикс тега клиента без detour, которым
// заменяется detour на пустой direct-outbound (см. isEmptyDirectTag). Тег
// несёт исходное имя outbound'а (ruleSetDirectClientTag), чтобы обратная
// проекция вернула ровно его.
const ruleSetDirectClientPrefix = "rs-direct:"

func ruleSetDirectClientTag(outbound string) string {
	return ruleSetDirectClientPrefix + outbound
}

// isEmptyDirectTag сообщает, ссылается ли tag на direct-outbound без
// dial-настроек: базовый неявный "direct" или объявленный в cfg.Outbounds
// direct с пустыми BindInterface и DomainResolver. Detour на такой outbound
// sing-box 1.14 отвергает при старте отдельно от "check" (форк,
// common/dialer/detour.go): "detour to an empty direct outbound makes no
// sense". Любой другой тег (туннели, подписки, composite outbound'ы) —
// не пустой, даже если не найден в cfg.Outbounds.
func isEmptyDirectTag(cfg *RouterConfig, tag string) bool {
	if tag == "direct" {
		return true
	}
	for _, o := range cfg.Outbounds {
		if o.Tag == tag {
			return o.Type == "direct" && o.BindInterface == "" && o.DomainResolver == nil
		}
	}
	return false
}

// applyHTTPClients переводит хранимую форму в форму sing-box 1.14:
// download_detour → http_client{detour}, плюс общий клиент rs-download с
// detour на route.final — так раньше вёл себя неявный клиент «через дефолтный
// outbound» (deprecated, удаление в 1.16). Явно выразить «через дефолтный
// outbound» в 1.14 нельзя: поле DefaultOutbound у клиента помечено json:"-".
//
// Detour на пустой direct-outbound (isEmptyDirectTag) sing-box запрещает —
// "detour to an empty direct outbound makes no sense", а клиент вовсе без
// detour эквивалентен такому выходу (системный диалер = прямой выход,
// common/dialer/dialer.go). Поэтому выбор пользователя не подменяется:
// пустой direct выражается через ОТСУТСТВИЕ detour, а не через другой
// outbound (решение владельца 2026-09-06). Для rs-download это просто пустой
// Detour; для rule_set с DownloadDetour на пустой direct — отдельный клиент
// без detour, на который набор ссылается СТРОКОЙ (http_client:"rs-direct:X"),
// чтобы восстановление знало исходный X.
func applyHTTPClients(cfg *RouterConfig) {
	finalDetour := cfg.Route.Final
	if finalDetour == "" || isEmptyDirectTag(cfg, finalDetour) {
		finalDetour = ""
	}
	cfg.HTTPClients = []HTTPClient{{Tag: ruleSetHTTPClientTag, Detour: finalDetour}}
	cfg.Route.DefaultHTTPClient = ruleSetHTTPClientTag

	directClientSeen := make(map[string]struct{})
	for i := range cfg.Route.RuleSet {
		rs := &cfg.Route.RuleSet[i]
		if rs.DownloadDetour == "" {
			// Уже материализован (повторный materializeConfig без restore
			// между вызовами, F115) — cfg.HTTPClients выше пересобран с
			// нуля, а rs.HTTPClient.Ref на rule_set'е, не прошедшем через
			// expandManagedToInline (не inline/managed-local), уцелел от
			// прошлого прохода. Без этого восстановления ссылка повисает:
			// клиента, на который она указывает, в свежем http_clients нет.
			if rs.HTTPClient != nil && strings.HasPrefix(rs.HTTPClient.Ref, ruleSetDirectClientPrefix) {
				if _, ok := directClientSeen[rs.HTTPClient.Ref]; !ok {
					directClientSeen[rs.HTTPClient.Ref] = struct{}{}
					cfg.HTTPClients = append(cfg.HTTPClients, HTTPClient{Tag: rs.HTTPClient.Ref})
				}
			}
			continue
		}
		x := rs.DownloadDetour
		if isEmptyDirectTag(cfg, x) {
			ref := ruleSetDirectClientTag(x)
			rs.HTTPClient = &RuleSetHTTPClient{Ref: ref}
			if _, ok := directClientSeen[ref]; !ok {
				directClientSeen[ref] = struct{}{}
				cfg.HTTPClients = append(cfg.HTTPClients, HTTPClient{Tag: ref})
			}
		} else {
			rs.HTTPClient = &RuleSetHTTPClient{Detour: x}
		}
		rs.DownloadDetour = ""
	}
}

// restoreHTTPClients — обратная проекция для читателей слота.
func restoreHTTPClients(cfg *RouterConfig) {
	cfg.HTTPClients = nil
	cfg.Route.DefaultHTTPClient = ""
	for i := range cfg.Route.RuleSet {
		rs := &cfg.Route.RuleSet[i]
		if rs.HTTPClient == nil {
			continue
		}
		if rs.HTTPClient.Ref != "" {
			if x, ok := strings.CutPrefix(rs.HTTPClient.Ref, ruleSetDirectClientPrefix); ok && rs.DownloadDetour == "" {
				rs.DownloadDetour = x
			}
			rs.HTTPClient = nil
			continue
		}
		// Если в слоте есть оба поля (ручная правка), побеждает уже
		// выставленный DownloadDetour — намеренно.
		if rs.DownloadDetour == "" {
			rs.DownloadDetour = rs.HTTPClient.Detour
		}
		rs.HTTPClient = nil
	}
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
	restoreHTTPClients(&out)
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
		rewriteRuleRefsDeep(&cfg.Route.Rules[i], rewriteRuleSetTagsStripSRSSuffix)
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
		rewriteRuleRefsDeep(&cfg.Route.Rules[i], func(tags []string) []string {
			return rewriteRuleSetSlice(tags, from, to)
		})
	}
	for i := range cfg.DNS.Rules {
		cfg.DNS.Rules[i].RuleSet = rewriteRuleSetSlice(cfg.DNS.Rules[i].RuleSet, from, to)
	}
}

// rewriteRuleRefsDeep применяет fn к rule_set-ссылкам правила и всех его
// вложенных logical-подправил: ссылка на inline-набор внутри
// type:"logical" без рекурсии избегала переписывания при материализации
// и указывала на несуществующий после неё тег.
func rewriteRuleRefsDeep(rule *Rule, fn func([]string) []string) {
	rule.RuleSet = fn(rule.RuleSet)
	for i := range rule.Rules {
		rewriteRuleRefsDeep(&rule.Rules[i], fn)
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

	// F110: reconcile персистит конфиг на каждый тик, даже когда правила
	// набора не менялись — без этой проверки каждый тик форкал бы `sing-box
	// rule-set compile` и переименовывал свежие .json/.srs поверх уже
	// актуальных. sourceJSON уже несёт inlineRuleSetSourceVersion
	// (buildInlineRuleSetSource пишет его в поле version), поэтому байт-в-байт
	// сравнение с уже лежащим .json ловит и бамп версии формата — .json от
	// прежней версии не совпадёт с пересобранным.
	if existing, err := os.ReadFile(jsonPath); err == nil && bytes.Equal(existing, sourceJSON) && regularFileExists(srsPath) {
		return managedLocalRuleSet(inlineSRSTag(rs.Tag), srsPath), nil
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

// gcArtifacts deletes orphaned rule-set artifacts under rule-sets/inline/
// (*.json, *.srs) and rule-sets/dat/ (*.json, *.srs, *.meta.json) whose base
// name is not in referenced. The dat token file and *.tmp files (in-flight
// compiles) are never touched. Callers build referenced as the union of every
// config that may still point at the files (active + pending, router + fakeip)
// — see (*ServiceImpl).gcRuleSetArtifacts.
func (m ruleSetMaterializer) gcArtifacts(referenced map[string]struct{}) {
	if m.configDir == "" {
		return
	}
	removed := m.gcArtifactDir(filepath.Join(m.configDir, "rule-sets", "inline"), referenced)
	removed += m.gcArtifactDir(filepath.Join(m.configDir, "rule-sets", "dat"), referenced)
	// Пофайловые строки — debug; сводка уборки видна на info.
	if removed > 0 && m.log != nil {
		m.log.Info("gc", "", fmt.Sprintf("removed %d orphaned rule-set artifact(s)", removed))
	}
}

func (m ruleSetMaterializer) gcArtifactDir(dir string, referenced map[string]struct{}) (removed int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0 // dir absent — nothing to sweep
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		base, ok := ruleSetArtifactBase(name)
		if !ok {
			continue
		}
		if _, ok := referenced[base]; ok {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil {
			if !os.IsNotExist(err) && m.log != nil {
				m.log.Warn("gc", base, fmt.Sprintf("remove orphaned rule-set artifact %s: %v", path, err))
			}
			continue
		}
		removed++
		if m.log != nil {
			m.log.Debug("gc", base, "removed orphaned rule-set artifact "+path)
		}
	}
	return removed
}

// ruleSetArtifactBase maps an artifact filename to the base name compared
// against the referenced set. Returns ok=false for files GC must never touch
// (the dat token, in-flight *.tmp compiles, unknown extensions).
func ruleSetArtifactBase(name string) (string, bool) {
	if name == datRuleSetTokenFile || strings.HasSuffix(name, ".tmp") {
		return "", false
	}
	if strings.HasSuffix(name, datRuleSetMetaExt) {
		return strings.TrimSuffix(name, datRuleSetMetaExt), true
	}
	for _, ext := range []string{".json", ".srs"} {
		if strings.HasSuffix(name, ext) {
			return strings.TrimSuffix(name, ext), true
		}
	}
	return "", false
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
