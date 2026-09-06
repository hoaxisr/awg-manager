package router

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withFakeRuleSetCompiler(t *testing.T, fn func(binary string, args []string) (string, string, error)) {
	t.Helper()
	old := inlineRuleSetCompileExec
	inlineRuleSetCompileExec = fn
	t.Cleanup(func() { inlineRuleSetCompileExec = old })
}

func writeCompiledOutput(t *testing.T, args []string, body string) {
	t.Helper()
	out := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--output" {
			out = args[i+1]
			break
		}
	}
	if out == "" {
		t.Fatalf("compile args missing --output: %v", args)
	}
	if err := os.WriteFile(out, []byte(body), 0644); err != nil {
		t.Fatalf("write compiled output: %v", err)
	}
}

func TestInlineRuleSetMaterializer_CompilesLocalBinary(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		calls++
		if binary != "/opt/bin/sing-box" {
			t.Fatalf("binary = %q", binary)
		}
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})

	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	rs := RuleSet{
		Tag:  "geosite/example",
		Type: "inline",
		Rules: []map[string]any{
			{"domain_suffix": []any{"example.com"}},
			{"domain_suffix": []any{"example.com"}},
			{"domain_suffix": []any{"example.org"}},
		},
	}
	got, err := m.materializeRuleSet(rs)
	if err != nil {
		t.Fatalf("materializeRuleSet: %v", err)
	}
	wantTag := inlineSRSTag(rs.Tag)
	if got.Tag != wantTag || got.Type != "local" || got.Format != "binary" {
		t.Fatalf("unexpected materialized ruleset: %+v", got)
	}
	wantPath := filepath.Join(dir, "rule-sets", "inline", "geosite-example.srs")
	if got.Path != wantPath {
		t.Fatalf("path = %q, want %q", got.Path, wantPath)
	}
	if _, err := os.Stat(got.Path); err != nil {
		t.Fatalf("compiled .srs missing: %v", err)
	}

	raw, err := os.ReadFile(strings.TrimSuffix(got.Path, ".srs") + ".json")
	if err != nil {
		t.Fatalf("source json missing: %v", err)
	}
	var source inlineRuleSetSource
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatalf("source json invalid: %v", err)
	}
	if source.Version != inlineRuleSetSourceVersion {
		t.Fatalf("version = %d", source.Version)
	}
	if len(source.Rules) != 2 {
		t.Fatalf("deduped rules len = %d, rules=%v", len(source.Rules), source.Rules)
	}

	again, err := m.materializeRuleSet(rs)
	if err != nil {
		t.Fatalf("second materializeRuleSet: %v", err)
	}
	if again.Path != got.Path {
		t.Fatalf("stable path changed: %q -> %q", got.Path, again.Path)
	}
	if calls != 2 {
		t.Fatalf("expected compile on each save (overwrite), got %d", calls)
	}
}

func TestInlineRuleSetMaterializer_CompileErrorDoesNotPublishBinary(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		return "", "bad rule", errors.New("exit status 1")
	})

	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	_, err := m.materializeRuleSet(RuleSet{
		Tag:   "bad",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"example.com"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "bad rule") {
		t.Fatalf("expected compile error with stderr, got %v", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, "rule-sets", "inline", "*.srs"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("unexpected published .srs after failed compile: %v", matches)
	}
}

func TestInlineRuleSetMaterializer_RestoresManagedLocalAsInline(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	inline := RuleSet{
		Tag:   "inline-a",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"example.com"}}},
	}
	local, err := m.materializeRuleSet(inline)
	if err != nil {
		t.Fatal(err)
	}
	restored := m.restoreRuleSet(local)
	if restored.Type != "inline" || restored.Tag != inline.Tag || len(restored.Rules) != 1 {
		t.Fatalf("unexpected restored ruleset: %+v", restored)
	}
	if !restored.MaterializedSRS {
		t.Fatal("expected materialized_srs on restore")
	}
}

func TestInlineRuleSetMaterializer_RestoreConfigHidesSRSCompanion(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	inline := RuleSet{
		Tag:   "pair",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"example.com"}}},
	}
	local, err := m.materializeRuleSet(inline)
	if err != nil {
		t.Fatal(err)
	}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{inline, local}
	out := m.restoreConfig(cfg)
	if len(out.Route.RuleSet) != 1 {
		t.Fatalf("want one visible ruleset, got %d: %+v", len(out.Route.RuleSet), out.Route.RuleSet)
	}
	if out.Route.RuleSet[0].Tag != "pair" || out.Route.RuleSet[0].Type != "inline" || !out.Route.RuleSet[0].MaterializedSRS {
		t.Fatalf("unexpected list projection: %+v", out.Route.RuleSet[0])
	}
}

func TestInlineRuleSetMaterializer_MaterializeConfigWritesSRSCompanion(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{
		Tag:   "foo",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".example.com"}}},
	}}
	out, err := m.materializeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Route.RuleSet) != 1 {
		t.Fatalf("rule_set len = %d", len(out.Route.RuleSet))
	}
	if out.Route.RuleSet[0].Tag != "foo-srs" || out.Route.RuleSet[0].Type != "local" {
		t.Fatalf("unexpected persisted ruleset: %+v", out.Route.RuleSet[0])
	}
}

func TestInlineRuleSetMaterializer_RemoveInlineArtifacts(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	_, err := m.materializeRuleSet(RuleSet{
		Tag:   "gone",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.removeInlineArtifacts("gone")
	for _, ext := range []string{".json", ".srs"} {
		if _, err := os.Stat(filepath.Join(dir, "rule-sets", "inline", "gone"+ext)); !os.IsNotExist(err) {
			t.Fatalf("expected gone%s removed, stat err=%v", ext, err)
		}
	}
}

func TestInlineRuleSetMaterializer_RewritesRuleRefsOnMaterialize(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{
		Tag:   "foo",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".example.com"}}},
	}}
	cfg.Route.Rules = []Rule{{RuleSet: []string{"foo"}, Action: "route", Outbound: "direct"}}
	out, err := m.materializeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Route.Rules) != 1 || len(out.Route.Rules[0].RuleSet) != 1 || out.Route.Rules[0].RuleSet[0] != "foo-srs" {
		t.Fatalf("route rule_set refs = %+v", out.Route.Rules[0].RuleSet)
	}
}

func TestInlineRuleSetMaterializer_RestoreRewritesSRSRefsToInline(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	local, err := m.materializeRuleSet(RuleSet{
		Tag:   "foo",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".example.com"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{local}
	cfg.Route.Rules = []Rule{{RuleSet: []string{"foo-srs"}, Action: "route", Outbound: "direct"}}
	out := m.restoreConfig(cfg)
	if len(out.Route.Rules) != 1 || out.Route.Rules[0].RuleSet[0] != "foo" {
		t.Fatalf("restored route rule_set refs = %+v", out.Route.Rules[0].RuleSet)
	}
}

func TestInlineRuleSetMaterializer_RestoreRewritesSRSRefsWithoutManagedEntryInOutput(t *testing.T) {
	// Simulates persisted config: only foo-srs in rule_set[], refs still foo-srs.
	m := ruleSetMaterializer{configDir: t.TempDir(), binary: "/opt/bin/sing-box"}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{
		Tag:    "geosite-samsung-srs",
		Type:   "local",
		Format: "binary",
		Path:   filepath.Join(m.configDir, "rule-sets", "inline", "geosite-samsung.srs"),
	}}
	cfg.Route.Rules = []Rule{{RuleSet: []string{"geosite-samsung-srs"}, Action: "route", Outbound: "direct"}}
	out := m.restoreConfig(cfg)
	if len(out.Route.RuleSet) != 1 || out.Route.RuleSet[0].Tag != "geosite-samsung" {
		t.Fatalf("rule_set projection = %+v", out.Route.RuleSet)
	}
	if len(out.Route.Rules) != 1 || out.Route.Rules[0].RuleSet[0] != "geosite-samsung" {
		t.Fatalf("rule refs = %+v", out.Route.Rules[0].RuleSet)
	}
}

func TestDeleteRuleSet_RemovesCompanionTag(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{
		{Tag: "foo", Type: "inline", Rules: []map[string]any{{"domain_suffix": []any{".x"}}}},
		{Tag: "foo-srs", Type: "local", Format: "binary", Path: "/tmp/foo.srs"},
	}
	if err := cfg.DeleteRuleSet("foo", false); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Route.RuleSet) != 0 {
		t.Fatalf("expected empty rule sets, got %+v", cfg.Route.RuleSet)
	}
}

// TestMapTagSlice locks the shared rewriter skeleton: unchanged input returns
// the SAME backing slice (no churn), changed input returns a new slice.
func TestMapTagSlice(t *testing.T) {
	in := []string{"a", "b", "c"}
	same := mapTagSlice(in, func(s string) (string, bool) { return "", false })
	if &same[0] != &in[0] {
		t.Error("unchanged input must return the original slice (same backing array)")
	}
	got := mapTagSlice(in, func(s string) (string, bool) {
		if s == "b" {
			return "B", true
		}
		return "", false
	})
	if got[1] != "B" || got[0] != "a" || got[2] != "c" {
		t.Errorf("got %v, want [a B c]", got)
	}
	if &got[0] == &in[0] {
		t.Error("changed input must return a new slice")
	}
	// rewriteTagSlice keeps its from==to / empty guards.
	if r := rewriteTagSlice(in, "a", "a"); &r[0] != &in[0] {
		t.Error("rewriteTagSlice from==to must be a no-op returning original")
	}
}

// #506: DNS-инспектор ходит по восстановленным правилам (ссылки на
// inline-теги без -srs) — карта наборов должна содержать алиасы
// материализованных inline'ов под базовым тегом, указывающие на
// скомпилированный .srs.
func TestInspectRuleSetsWithInlineAliases(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}

	local, err := m.materializeRuleSet(RuleSet{
		Tag:   "geo-telegram",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"t.me"}}},
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	remote := RuleSet{Tag: "geosite-google", Type: "remote", URL: "https://example.com/g.srs"}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{remote, local}

	got := m.inspectRuleSetsWithInlineAliases(cfg)
	if len(got) != 3 {
		t.Fatalf("expected original 2 + 1 alias, got %d: %+v", len(got), got)
	}
	alias := got[2]
	if alias.Tag != "geo-telegram" {
		t.Errorf("alias tag = %q, want geo-telegram", alias.Tag)
	}
	if alias.Type != "local" || alias.Path != local.Path {
		t.Errorf("alias must point at the compiled srs, got %+v", alias)
	}

	// Карта «последний побеждает»: алиас перекрывает сырую inline-запись
	// с тем же тегом и остаётся матчабельным local-набором.
	byTag := map[string]RuleSet{}
	for _, rs := range got {
		byTag[rs.Tag] = rs
	}
	if byTag["geo-telegram"].Type != "local" {
		t.Errorf("map entry for base tag must be the local alias, got %+v", byTag["geo-telegram"])
	}
	if byTag["geosite-google"].Type != "remote" {
		t.Errorf("remote entry must be untouched, got %+v", byTag["geosite-google"])
	}

	if m.inspectRuleSetsWithInlineAliases(nil) != nil {
		t.Error("nil config must yield nil")
	}
}

// Guard: алиас не затеняет настоящий НЕ-inline набор с тем же базовым
// тегом (legacy/ручной конфиг с remote "geo-x" рядом с managed
// "geo-x-srs") — иначе инспектор молча матчил бы не тот файл.
func TestInspectRuleSetsWithInlineAliases_DoesNotShadowGenuineSet(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	local, err := m.materializeRuleSet(RuleSet{
		Tag:   "geo-x",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"x.example"}}},
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	genuine := RuleSet{Tag: "geo-x", Type: "remote", URL: "https://example.com/x.srs"}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{genuine, local}

	got := m.inspectRuleSetsWithInlineAliases(cfg)
	if len(got) != 2 {
		t.Fatalf("alias must be skipped for a shadowed genuine set, got %d entries: %+v", len(got), got)
	}
	byTag := map[string]RuleSet{}
	for _, rs := range got {
		byTag[rs.Tag] = rs
	}
	if byTag["geo-x"].Type != "remote" {
		t.Errorf("genuine remote set must stay resolvable, got %+v", byTag["geo-x"])
	}
}

// Вложенные logical-подправила: ссылки на inline-наборы переписываются
// при материализации (иначе персистится ссылка на несуществующий тег)
// и восстанавливаются обратно.
func TestMaterializeConfig_RewritesNestedRuleRefs(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}

	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{
		Tag:   "geo-telegram",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"t.me"}}},
	}}
	cfg.Route.Rules = []Rule{{
		Type: "logical",
		Mode: "or",
		Rules: []Rule{
			{RuleSet: []string{"geo-telegram"}},
			{Protocol: "dns"},
		},
		Action:   "route",
		Outbound: "vpn",
	}}

	persisted, err := m.materializeConfig(cfg)
	if err != nil {
		t.Fatalf("materializeConfig: %v", err)
	}
	nested := persisted.Route.Rules[0].Rules[0].RuleSet
	if len(nested) != 1 || nested[0] != "geo-telegram-srs" {
		t.Fatalf("nested ref must be rewritten to the materialized tag, got %v", nested)
	}

	restored := m.restoreConfig(persisted)
	nested = restored.Route.Rules[0].Rules[0].RuleSet
	if len(nested) != 1 || nested[0] != "geo-telegram" {
		t.Fatalf("restored nested ref must be the inline tag, got %v", nested)
	}
}

// sing-box 1.14: download_detour и неявный HTTP-клиент deprecated (удаление в
// 1.16). Хранимая форма — download_detour; в слот уходит http_client{detour}
// плюс общий клиент rs-download с detour на route.final. Без final — прямой
// выход (решение владельца 2026-09-06).
func findRuleSetByTag(rs []RuleSet, tag string) RuleSet {
	for _, r := range rs {
		if r.Tag == tag {
			return r
		}
	}
	return RuleSet{}
}

func TestMaterializeConfig_HTTPClients(t *testing.T) {
	dir := t.TempDir()
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	cfg := &RouterConfig{
		Route: Route{
			Final: "direct",
			RuleSet: []RuleSet{
				{Tag: "geo-direct", Type: "remote", URL: "https://x/geo.srs", DownloadDetour: "direct"},
				{Tag: "geo-vpn", Type: "remote", URL: "https://x/vpn.srs", DownloadDetour: "vpn"},
				{Tag: "plain", Type: "remote", URL: "https://x/plain.srs"},
			},
		},
	}
	out, err := m.materializeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// (a) final:"direct" (пустой) — rs-download без detour.
	if len(out.HTTPClients) == 0 || out.HTTPClients[0].Tag != ruleSetHTTPClientTag || out.HTTPClients[0].Detour != "" {
		t.Fatalf("rs-download = %+v, want no detour (final is empty direct)", out.HTTPClients)
	}
	if out.Route.DefaultHTTPClient != ruleSetHTTPClientTag {
		t.Errorf("default_http_client = %q", out.Route.DefaultHTTPClient)
	}

	// (c) download_detour на пустой direct → строковая ссылка + отдельный клиент без detour.
	geoDirect := findRuleSetByTag(out.Route.RuleSet, "geo-direct")
	if geoDirect.DownloadDetour != "" || geoDirect.HTTPClient == nil ||
		geoDirect.HTTPClient.Ref != "rs-direct:direct" || geoDirect.HTTPClient.Detour != "" {
		t.Fatalf("geo-direct: download_detour=%q http_client=%+v, want ref rs-direct:direct",
			geoDirect.DownloadDetour, geoDirect.HTTPClient)
	}
	var directClient *HTTPClient
	for i := range out.HTTPClients {
		if out.HTTPClients[i].Tag == "rs-direct:direct" {
			directClient = &out.HTTPClients[i]
		}
	}
	if directClient == nil {
		t.Fatalf("http_clients missing rs-direct:direct: %+v", out.HTTPClients)
	}
	if directClient.Detour != "" {
		t.Errorf("rs-direct:direct has detour %q, want none", directClient.Detour)
	}

	// (d) download_detour на непустой outbound — как раньше, объект {detour}.
	geoVPN := findRuleSetByTag(out.Route.RuleSet, "geo-vpn")
	if geoVPN.DownloadDetour != "" || geoVPN.HTTPClient == nil ||
		geoVPN.HTTPClient.Detour != "vpn" || geoVPN.HTTPClient.Ref != "" {
		t.Fatalf("geo-vpn: download_detour=%q http_client=%+v, want http_client{detour:vpn}",
			geoVPN.DownloadDetour, geoVPN.HTTPClient)
	}

	if findRuleSetByTag(out.Route.RuleSet, "plain").HTTPClient != nil {
		t.Errorf("plain must have no http_client")
	}

	// (f) сериализованная строковая ссылка — буквально строка в JSON, не объект.
	raw, err := json.Marshal(out.Route.RuleSet)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"http_client":"rs-direct:direct"`) {
		t.Errorf("materialized JSON missing string http_client ref: %s", raw)
	}
	if strings.Contains(string(raw), `"detour":"direct"`) {
		t.Errorf("must not detour to empty direct outbound: %s", raw)
	}

	// Исходный конфиг не тронут: материализация — проекция, не мутация.
	if cfg.HTTPClients != nil || cfg.Route.RuleSet[0].DownloadDetour != "direct" {
		t.Errorf("source config mutated: %+v", cfg)
	}

	// (b) непустой final — общий клиент получает detour.
	cfg.Route.Final = "vpn"
	out, err = m.materializeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if out.HTTPClients[0].Detour != "vpn" {
		t.Errorf("final=vpn: detour = %q, want vpn", out.HTTPClients[0].Detour)
	}

	// (e) обратная проекция восстанавливает download_detour для обеих форм
	// и убирает материализованных клиентов.
	back := m.restoreConfig(out)
	if back.HTTPClients != nil || back.Route.DefaultHTTPClient != "" {
		t.Errorf("restore left http_clients: %+v / %q", back.HTTPClients, back.Route.DefaultHTTPClient)
	}
	if rs := findRuleSetByTag(back.Route.RuleSet, "geo-direct"); rs.HTTPClient != nil || rs.DownloadDetour != "direct" {
		t.Errorf("restore geo-direct: %+v", rs)
	}
	if rs := findRuleSetByTag(back.Route.RuleSet, "geo-vpn"); rs.HTTPClient != nil || rs.DownloadDetour != "vpn" {
		t.Errorf("restore geo-vpn: %+v", rs)
	}
}
