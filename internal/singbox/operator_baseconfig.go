package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// defaultCacheDBPath is the absolute path for sing-box's experimental.cache_file.
// Must live in a writable directory — sing-box resolves relative paths against
// CWD ("/" when the manager runs as a service on Entware), which is read-only.
// var (not const) because filepath.Join requires runtime evaluation; tests can
// override defaultDir to redirect this too.
var defaultCacheDBPath = filepath.Join(defaultDir, "cache.db")

// DefaultCacheDBPath exports the sing-box experimental.cache_file path so the
// fakeip-tun router wiring (cmd/awg-manager) can pin its store_fakeip cache to
// the same writable file without importing the unexported var. Keeps the router
// package decoupled from the operator's path layout (it just receives a string).
func DefaultCacheDBPath() string { return defaultCacheDBPath }

// ensureBaseConfigWithLogLevel writes a minimal 00-base.json if config.d is
// empty, so sing-box starts standalone (direct outbound + bootstrap DNS) before
// any tunnels are added. Also surgically self-heals an older base config
// that hard-coded the wrong Clash API port (9090 instead of
// clashAPIAddr's 9099), which silently broke our LogForwarder /
// DelayChecker on existing installs.
func ensureBaseConfig(configDir, desiredLogLevel, desiredBootstrapDNS string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	basePath := filepath.Join(configDir, "00-base.json")
	if _, err := os.Stat(basePath); err == nil {
		patchBaseClashPort(basePath, log)
		patchBaseLogLevel(basePath, desiredLogLevel, log)
		reconcileBaseDomainResolver(configDir, log)
		patchBaseDirectOutbound(basePath, log)
		patchBaseCacheFilePath(basePath, log)
		patchBaseDNSStrategy(basePath, log)
		patchBaseBootstrapDNS(basePath, desiredBootstrapDNS, log)
		return
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		logConfigPatchWarn(log, "singbox config reconcile: mkdir failed",
			"step", stepEnsureBaseConfig, "path", configDir, "err", err)
		return
	}
	writeSlotJSON(stepEnsureBaseConfig, basePath, freshBaseConfig(desiredLogLevel, desiredBootstrapDNS), log)
	// Свежая база несёт default_domain_resolver, но если routing-слот уже
	// задал свой, база обязана уступить: скаляры route мержатся
	// first-file-wins и 00-base лексически первый.
	reconcileBaseDomainResolver(configDir, log)
}

func logConfigPatchInfo(log *slog.Logger, msg string, args ...any) {
	if log == nil {
		return
	}
	log.Info(msg, args...)
}

func logConfigPatchWarn(log *slog.Logger, msg string, args ...any) {
	if log == nil {
		return
	}
	log.Warn(msg, args...)
}

// Имена шагов примирения config.d. Попадают в Warn-строки пролога, так что
// по логу видно, какой именно шаг потерялся.
const (
	stepEnsureBaseConfig       = "ensure-base-config"
	stepPatchBaseClashPort     = "patch-base-clash-port"
	stepPatchBaseLogLevel      = "patch-base-log-level"
	stepPatchBaseDomainResolve = "patch-base-domain-resolver"
	stepPatchBaseDirectOutbnd  = "patch-base-direct-outbound"
	stepPatchBaseCacheFile     = "patch-base-cache-file"
	stepPatchBaseDNSStrategy   = "patch-base-dns-strategy"
	stepPatchBaseBootstrapDNS  = "patch-base-bootstrap-dns"
	stepMigrateLegacyTunnels   = "migrate-legacy-tunnels"
	stepStripBaseOwnedBlocks   = "strip-base-owned-blocks"
	stepOutboundCompat         = "outbound-compat"
	stepStripStrayDirect       = "strip-stray-direct"
	stepRemoveRouteFinal       = "remove-route-final"
	stepRemoveDNSFinal         = "remove-dns-final"
	stepReconcileDNSStrategy   = "reconcile-dns-strategy"
)

// reconcileStep — один шаг примирения config.d, гоняемого каждый бут.
type reconcileStep struct {
	name string
	run  func()
}

// reconcileConfigSteps — плоский именованный набор шагов примирения.
// Гоняется каждый бут из NewOperator; каждый шаг идемпотентен и самогасится
// проверкой «уже так — no-op».
//
// ЕДИНСТВЕННОЕ ограничение порядка — снаружи набора: MigrateLegacyConfigDir
// обязан выполниться ПЕРВЫМ (создаёт config.d; иначе dns-блок легаси-конфига
// скопируется дважды и configmerge отвергнет дубликаты тегов — см.
// MigrateLegacyConfigDir в config.go). Внутри набора ограничений порядка нет:
// шаги попарно коммутируют по конечному состоянию (закреплено
// TestReconcileConfigSteps_CommuteReversed).
func reconcileConfigSteps(dir, configPath, desiredLogLevel, desiredBootstrapDNS string, log *slog.Logger) []reconcileStep {
	base := filepath.Join(configPath, "00-base.json")
	tunnels := filepath.Join(configPath, "10-tunnels.json")
	return []reconcileStep{
		{stepEnsureBaseConfig, func() { ensureBaseConfig(configPath, desiredLogLevel, desiredBootstrapDNS, log) }},
		{stepMigrateLegacyTunnels, func() { ensureLegacyConfigMigrated(dir, log) }},
		{stepStripBaseOwnedBlocks, func() { patchTunnelsSlotStripBaseOwnedBlocks(tunnels, log) }},
		// Компат-фиксы нужны каждому продюсеру outbound'ов: 10-tunnels пишет
		// UI, 40-subscriptions — подписочный адаптер. Только активный путь
		// config.d/ — слоты в disabled/ и pending/ не трогаются (та же
		// семантика, что у stripStrayDirectPlaceholder: их нет в merged).
		{stepOutboundCompat, func() {
			patchSlotOutboundCompat(tunnels, log)
			patchSlotOutboundCompat(filepath.Join(configPath, "40-subscriptions.json"), log)
		}},
		{stepStripStrayDirect, func() { stripStrayDirectPlaceholder(configPath, log) }},
		{stepRemoveRouteFinal, func() { removeFinalFromBase(base, log) }},
		{stepRemoveDNSFinal, func() { removeDNSFinalFromBase(base, log) }},
		{stepReconcileDNSStrategy, func() { reconcileBaseDNSStrategy(configPath, log) }},
	}
}

// readSlotJSON — общий пролог чтения шага примирения. «Файла нет» — норма
// (молчаливый no-op, ok=false); «файл есть, но прочитать/распарсить не
// смогли» — Warn с именем шага: молчаливый провал патчера оставляет демон
// работать с тихо потерянной функцией, и этот лог — единственный свидетель.
func readSlotJSON(step, path string, log *slog.Logger) (map[string]any, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logConfigPatchWarn(log, "singbox config reconcile: read failed",
				"step", step, "path", path, "err", err)
		}
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		logConfigPatchWarn(log, "singbox config reconcile: parse failed",
			"step", step, "path", path, "err", err)
		return nil, false
	}
	return m, true
}

// writeSlotJSON — общий пролог записи шага примирения: ошибка записи не
// молчит, а называет шаг и путь.
func writeSlotJSON(step, path string, m map[string]any, log *slog.Logger) bool {
	if err := writeJSONFile(path, m); err != nil {
		logConfigPatchWarn(log, "singbox config reconcile: write failed",
			"step", step, "path", path, "err", err)
		return false
	}
	return true
}

// firstLogger распаковывает variadic-логгер патчеров: существующие вызовы без
// логгера остаются валидными, новые передают его первым аргументом.
func firstLogger(loggers []*slog.Logger) *slog.Logger {
	if len(loggers) > 0 {
		return loggers[0]
	}
	return nil
}

// ensureLegacyConfigMigrated copies user-added sing-box tunnels from a
// pre-2.9.10 single-file config.json into the new slot layout
// (config.d/10-tunnels.json), then removes the legacy file.
//
// pre-2.9.10 layout: <dir>/config.json — sing-box read this single file.
// 2.9.10+ layout:    <dir>/config.d/<NN-name>.json — directory merged.
//
// Idempotent: returns silently when legacy is absent, when 10-tunnels.json
// already exists, when legacy is unparseable, or when legacy is a
// directory (degenerate). On parse failure we leave the legacy file in
// place so a manual fix or next-boot retry can recover.
//
// dir is the singbox parent dir (e.g. /opt/etc/awg-manager/singbox).
func ensureLegacyConfigMigrated(dir string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	legacy := filepath.Join(dir, "config.json")
	target := filepath.Join(dir, "config.d", "10-tunnels.json")

	st, err := os.Stat(legacy)
	if err != nil || st.IsDir() {
		return
	}
	if _, err := os.Stat(target); err == nil {
		return
	}

	cfg, err := LoadConfig(legacy)
	if err != nil {
		// Parse failure — leave legacy in place for retry.
		logConfigPatchWarn(log, "singbox config reconcile: parse failed",
			"step", stepMigrateLegacyTunnels, "path", legacy, "err", err)
		return
	}

	// Legacy may include device-proxy artefacts; modern code emits those
	// in their own 30-deviceproxy.json slot. Strip leftovers so the user
	// can re-enable device proxy without tag collisions on next start.
	inbounds := filterOutDeviceProxyTags(cfg.inbounds())
	outbounds := filterOutDeviceProxyTags(filterOutDirectPlaceholder(cfg.outbounds()))
	rules := filterOutDeviceProxyRouteRules(cfg.routeRules())

	raw := map[string]any{
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route":     map[string]any{"rules": rules},
	}

	// Custom DNS: copy user-defined servers (excluding our bootstrap/doh
	// which 00-base owns) plus dns.rules. configmerge will concatenate
	// across slots.
	dnsBlock, _ := cfg.raw["dns"].(map[string]any)
	if dnsBlock != nil {
		dnsSlot := map[string]any{}
		if servers, ok := dnsBlock["servers"].([]any); ok {
			if filtered := filterOutOurDNSServers(servers); len(filtered) > 0 {
				dnsSlot["servers"] = filtered
			}
		}
		if rulesArr, ok := dnsBlock["rules"].([]any); ok && len(rulesArr) > 0 {
			dnsSlot["rules"] = rulesArr
		}
		if len(dnsSlot) > 0 {
			raw["dns"] = dnsSlot
		}
	}

	slot := &Config{raw: raw}

	if err := slot.Save(target); err != nil {
		logConfigPatchWarn(log, "singbox config reconcile: write failed",
			"step", stepMigrateLegacyTunnels, "path", target, "err", err)
		return
	}
	_ = os.Remove(legacy)
}

// filterOutDirectPlaceholder drops the {type:"direct", tag:"direct"}
// outbound that v2.8.2 wrote into its skeleton. Modern config.d/00-base.json
// owns no placeholder direct, but the configmerge collision check rejects
// duplicate tags — so we strip it here. Other entries pass through verbatim.
func filterOutDirectPlaceholder(in []any) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		ob, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		typ, _ := ob["type"].(string)
		tag, _ := ob["tag"].(string)
		if typ == "direct" && tag == "direct" {
			continue
		}
		out = append(out, v)
	}
	return out
}

// filterOutDeviceProxyTags drops inbound/outbound entries whose "tag"
// field starts with "device-proxy". Those artefacts belong in the
// dedicated 30-deviceproxy.json slot; keeping them in 10-tunnels.json
// causes a tag-collision FATAL when deviceproxy.Service later writes its
// own slot.
func filterOutDeviceProxyTags(in []any) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		ob, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		tag, _ := ob["tag"].(string)
		if strings.HasPrefix(tag, "device-proxy") {
			continue
		}
		out = append(out, v)
	}
	return out
}

// filterOutDeviceProxyRouteRules drops route rules whose "inbound" or
// "outbound" field references a device-proxy tag. Both fields may be a
// plain string or an array of strings — either form is checked.
func filterOutDeviceProxyRouteRules(in []any) []any {
	mentionsDeviceProxy := func(v any) bool {
		switch s := v.(type) {
		case string:
			return strings.HasPrefix(s, "device-proxy")
		case []any:
			for _, item := range s {
				if str, ok := item.(string); ok && strings.HasPrefix(str, "device-proxy") {
					return true
				}
			}
		}
		return false
	}

	out := make([]any, 0, len(in))
	for _, v := range in {
		r, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		if mentionsDeviceProxy(r["inbound"]) || mentionsDeviceProxy(r["outbound"]) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// filterOutOurDNSServers removes dns.servers entries whose tag is one of
// the well-known tags 00-base.json owns ("dns-bootstrap", "dns-doh"). All
// other entries — user-added custom resolvers — pass through so they end
// up in 10-tunnels.json and survive the migration.
func filterOutOurDNSServers(in []any) []any {
	owned := map[string]bool{
		"dns-bootstrap": true,
		"dns-doh":       true,
	}
	out := make([]any, 0, len(in))
	for _, v := range in {
		s, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		tag, _ := s["tag"].(string)
		if owned[tag] {
			continue
		}
		out = append(out, v)
	}
	return out
}

// patchBaseLogLevel updates 00-base.json log.level to desired settings
// value and ensures log.timestamp exists.
func patchBaseLogLevel(basePath, desiredLevel string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepPatchBaseLogLevel, basePath, log)
	if !ok {
		return
	}
	logBlock, _ := m["log"].(map[string]any)
	if logBlock == nil {
		logBlock = map[string]any{}
		m["log"] = logBlock
	}
	desired := normalizeSingboxLogLevel(desiredLevel)
	current, _ := logBlock["level"].(string)
	changed := false
	if current != desired {
		logBlock["level"] = desired
		changed = true
	}
	if _, ok := logBlock["timestamp"]; !ok {
		logBlock["timestamp"] = true
		changed = true
	}
	if !changed {
		return
	}
	writeSlotJSON(stepPatchBaseLogLevel, basePath, m, log)
}

// patchBaseClashPort rewrites only the experimental.clash_api.external_controller
// field if it points anywhere other than clashAPIAddr. Other fields
// (user customizations: log level, DNS servers, etc.) are preserved
// verbatim. No-op when the file already has the correct port or has no
// experimental.clash_api block at all (latter case: the user removed
// clash_api on purpose; respect that).
func patchBaseClashPort(basePath string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepPatchBaseClashPort, basePath, log)
	if !ok {
		return
	}
	exp, _ := m["experimental"].(map[string]any)
	if exp == nil {
		return
	}
	clash, _ := exp["clash_api"].(map[string]any)
	if clash == nil {
		return
	}
	current, _ := clash["external_controller"].(string)
	if current == clashAPIAddr {
		return
	}
	clash["external_controller"] = clashAPIAddr
	writeSlotJSON(stepPatchBaseClashPort, basePath, m, log)
}

// setBootstrapServer ставит адрес серверу с тегом dns-bootstrap. Возвращает
// false, если менять нечего: записи нет или адрес уже такой.
func setBootstrapServer(m map[string]any, want string) bool {
	dns, _ := m["dns"].(map[string]any)
	if dns == nil {
		return false
	}
	servers, _ := dns["servers"].([]any)
	for _, v := range servers {
		s, _ := v.(map[string]any)
		if s == nil || s["tag"] != "dns-bootstrap" {
			continue
		}
		if s["server"] == want {
			return false
		}
		s["server"] = want
		return true
	}
	return false
}

// patchBaseBootstrapDNS приводит адрес сервера dns-bootstrap к заданному в
// настройках (issue #770). Пустая настройка — no-op: до её появления адрес
// правили руками прямо в 00-base.json, и такие правки обязаны выжить.
// Записи с тегом dns-bootstrap нет — тоже no-op: патчер правит адрес, а не
// проектирует структуру файла.
func patchBaseBootstrapDNS(basePath, want string, loggers ...*slog.Logger) {
	if want == "" {
		return
	}
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepPatchBaseBootstrapDNS, basePath, log)
	if !ok {
		return
	}
	if !setBootstrapServer(m, want) {
		return
	}
	if !writeSlotJSON(stepPatchBaseBootstrapDNS, basePath, m, log) {
		return
	}
	logConfigPatchInfo(log, "singbox base config reconciled",
		"patch", stepPatchBaseBootstrapDNS,
		"path", basePath,
		"newServer", want,
	)
}

// baseDefaultDomainResolver — тег резолвера, которым 00-base.json владеет,
// пока никакой routing-слот не задал свой.
const baseDefaultDomainResolver = "dns-bootstrap"

// reconcileBaseDomainResolverMap примиряет route.default_domain_resolver
// базового слота с владением routing-слотов.
//
// Ключ обязателен: sing-box 1.13+ падает на старте с «missing
// route.default_domain_resolver … in dial fields», и без него программа
// неработоспособна — поэтому при отсутствии владельца база материализует
// блок route и ставит dns-bootstrap.
//
// Но скаляры под `route` мержатся FIRST-FILE-WINS, а 00-base лексически
// первый: пока база несёт свой ключ, resolver роутера (fakeip-tun ставит
// {"server":"real"}) молча выбрасывается из merged-конфига. Ровно та же
// ловушка, что закрыта для route.final и dns.final. Поэтому routingOwns=true
// → база уступает ключ.
func reconcileBaseDomainResolverMap(m map[string]any, routingOwns bool) bool {
	route, _ := m["route"].(map[string]any)
	if route == nil {
		if routingOwns {
			return false
		}
		route = map[string]any{}
		m["route"] = route
	}
	_, has := route["default_domain_resolver"]
	switch {
	case routingOwns && has:
		delete(route, "default_domain_resolver")
		return true
	case !routingOwns && !has:
		route["default_domain_resolver"] = baseDefaultDomainResolver
		return true
	}
	return false
}

// routingSlotOwnsDomainResolver — задал ли routing-слот свой
// route.default_domain_resolver.
func routingSlotOwnsDomainResolver(configDir, step string, log *slog.Logger) bool {
	for _, name := range []string{"20-router.json", "21-fakeip.json"} {
		m, ok := readSlotJSON(step, filepath.Join(configDir, name), log)
		if !ok {
			continue
		}
		route, _ := m["route"].(map[string]any)
		if route == nil {
			continue
		}
		if _, has := route["default_domain_resolver"]; has {
			return true
		}
	}
	return false
}

// reconcileBaseDomainResolver — бут-шаг patch-base-domain-resolver.
func reconcileBaseDomainResolver(configDir string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	basePath := filepath.Join(configDir, "00-base.json")
	m, ok := readSlotJSON(stepPatchBaseDomainResolve, basePath, log)
	if !ok {
		return
	}
	if !reconcileBaseDomainResolverMap(m, routingSlotOwnsDomainResolver(configDir, stepPatchBaseDomainResolve, log)) {
		return
	}
	writeSlotJSON(stepPatchBaseDomainResolve, basePath, m, log)
}

// patchBaseDNSStrategy migrates the legacy 00-base.json default
// dns.strategy "ipv4_only" → "prefer_ipv4". The old default silently
// dropped all AAAA/IPv6 answers (issue #180); the new default returns IPv6
// when available. Only the exact legacy value is migrated — any other
// strategy (incl. a deliberately user-set one) is left untouched.
func patchBaseDNSStrategy(basePath string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepPatchBaseDNSStrategy, basePath, log)
	if !ok {
		return
	}
	dns, _ := m["dns"].(map[string]any)
	if dns == nil {
		return
	}
	if strategy, _ := dns["strategy"].(string); strategy != "ipv4_only" {
		return
	}
	dns["strategy"] = "prefer_ipv4"
	writeSlotJSON(stepPatchBaseDNSStrategy, basePath, m, log)
}

// patchBaseDirectOutbound self-heals legacy 00-base.json files that
// pre-date the canonical {type:"direct", tag:"direct"} outbound. With
// router.NewEmptyConfig now defaulting route.final to "direct"
// (commit 56bbab35), every merged config references that tag — but
// older base files written before freshBaseConfig included the entry
// never had it, so sing-box FATALs on start with
// "default outbound not found: direct".
//
// Behavior:
//   - If a direct-tagged outbound is missing, prepend canonical direct.
//   - If direct exists but is not first, move that exact outbound to index 0.
//
// Keeping direct first preserves the documented sing-box fallback
// behavior when route.final is absent ("first outbound is used"), so
// disabling router slot does not accidentally switch fallback to some
// other custom outbound on legacy/custom base files.
func patchBaseDirectOutbound(basePath string, log *slog.Logger) {
	m, ok := readSlotJSON(stepPatchBaseDirectOutbnd, basePath, log)
	if !ok {
		return
	}
	obs, _ := m["outbounds"].([]any)
	directIdx := -1
	for i, v := range obs {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if tag, _ := ob["tag"].(string); tag == "direct" {
			directIdx = i
			break
		}
	}
	action := ""
	switch {
	case directIdx == 0:
		return
	case directIdx > 0:
		action = "move-direct-first"
		direct := obs[directIdx]
		rest := make([]any, 0, len(obs)-1)
		rest = append(rest, obs[:directIdx]...)
		rest = append(rest, obs[directIdx+1:]...)
		m["outbounds"] = append([]any{direct}, rest...)
	default:
		action = "prepend-direct"
		m["outbounds"] = append([]any{map[string]any{"type": "direct", "tag": "direct"}}, obs...)
	}
	if err := writeJSONFile(basePath, m); err != nil {
		logConfigPatchWarn(log, "singbox base config self-heal failed",
			"patch", "direct-first",
			"action", action,
			"path", basePath,
			"err", err,
		)
		return
	}
	logConfigPatchInfo(log, "singbox base config self-healed",
		"patch", "direct-first",
		"action", action,
		"path", basePath,
	)
}

// removeFinalFromBase strips the legacy route.final key from
// 00-base.json. Pre-spec installs wrote {route:{final:"direct"}} in
// base; this could shadow the router-slot final in merged runtime
// configs. This patch lets 20-router.json own route.final exclusively.
//
// Sing-box behavior when route.final is absent: "The first outbound
// will be used if empty" (per upstream docs). 00-base.json's outbound
// list starts with {type:"direct", tag:"direct"} (also self-healed by
// patchBaseDirectOutbound), so the implicit fallback stays direct —
// same observable behavior as the old explicit "final":"direct".
//
// Idempotent: no-op when route.final is already absent. Silent skip on
// missing file / read error / malformed JSON / missing route section
// (matches patchBaseDirectOutbound and patchTunnelsSlotStripBaseDNS).
func removeFinalFromBase(basePath string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepRemoveRouteFinal, basePath, log)
	if !ok {
		return
	}
	route, _ := m["route"].(map[string]any)
	if route == nil {
		return
	}
	if _, has := route["final"]; !has {
		return
	}
	oldFinal, _ := route["final"]
	delete(route, "final")
	if !writeSlotJSON(stepRemoveRouteFinal, basePath, m, log) {
		return
	}
	logConfigPatchInfo(log, "singbox base config self-healed",
		"patch", "remove-route-final",
		"path", basePath,
		"oldFinal", oldFinal,
	)
}

// removeDNSFinalFromBase strips base-owned DNS globals from 00-base.json that
// would otherwise shadow the router slot's choices in the merged runtime
// config. Bug #445: sing-box resolves conflicting scalar sub-keys of `dns`
// FIRST-FILE-WINS across config.d (proven for route.final by
// router_final_merge_test.go), so 00-base.json's dns.final / dns.strategy
// always beat the user's 20-router.json values. This self-heal runs on every
// operator init (right after ensureBaseConfigWithLogLevel) so existing on-disk
// base files heal on reload. It is a boot self-heal, not a settings migration.
// Mirrors removeFinalFromBase, which did the same for route.final.
//
// dns.final — stripped UNCONDITIONALLY (safe). When final is absent sing-box
// falls back to the FIRST dns server; base's server list is [dns-bootstrap]
// and the router's servers concatenate AFTER base, so the merged first server
// stays dns-bootstrap when the router is disabled, and the router's dns.final
// (the only slot that then sets it) wins when enabled. Same observable
// behavior as the old explicit "dns-bootstrap".
//
// dns.strategy сюда не относится — ею занимается отдельный шаг
// reconcile-dns-strategy (reconcileBaseDNSStrategy): у strategy нет
// first-server fallback'а, поэтому её примирение симметрично (стрижка при
// владении routing-слотом / восстановление дефолта без владельца).
//
// Idempotent; silent skip on missing file / read error / malformed JSON /
// missing dns section (matches removeFinalFromBase).
func removeDNSFinalFromBase(basePath string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepRemoveDNSFinal, basePath, log)
	if !ok {
		return
	}
	dns, _ := m["dns"].(map[string]any)
	if dns == nil {
		return
	}
	oldFinal, hadFinal := dns["final"]
	changed := false
	if hadFinal {
		delete(dns, "final")
		changed = true
	}
	if !changed {
		return
	}
	if !writeSlotJSON(stepRemoveDNSFinal, basePath, m, log) {
		return
	}
	logConfigPatchInfo(log, "singbox base config self-healed",
		"patch", "remove-dns-final",
		"path", basePath,
		"oldFinal", oldFinal,
	)
}

// baseDefaultDNSStrategy — значение dns.strategy, которым 00-base.json владеет,
// пока никакой routing-слот его не задаёт. У strategy нет fallback'а на первый
// dns-сервер (в отличие от dns.final), поэтому её отсутствие в merged-конфиге
// — не «дефолт», а другое поведение.
const baseDefaultDNSStrategy = "prefer_ipv4"

// routingSlotOwnsDNSStrategy — непустую dns.strategy может задавать ЛЮБОЙ
// активный routing-слот: 20-router.json (tproxy/policy-tun) ИЛИ 21-fakeip.json
// (fakeip-tun). Файл, перепаркованный в disabled/, владельцем не считается —
// его нет в merged-конфиге. step/log принадлежат ВЫЗЫВАЮЩЕМУ шагу:
// подглядывание в чужой слот иначе остаётся анонимным в логе.
func routingSlotOwnsDNSStrategy(configDir, step string, log *slog.Logger) bool {
	for _, name := range []string{"20-router.json", "21-fakeip.json"} {
		m, ok := readSlotJSON(step, filepath.Join(configDir, name), log)
		if !ok {
			continue
		}
		dns, _ := m["dns"].(map[string]any)
		if dns == nil {
			continue
		}
		if s, _ := dns["strategy"].(string); strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

// reconcileBaseDNSStrategyMap примиряет dns.strategy базового слота с
// владением routing-слотов. routingOwns=true → strategy стрижётся из base
// (иначе мерж скаляров first-file-wins затеняет выбор пользователя, семейство
// бага #445); routingOwns=false → strategy восстанавливается в
// baseDefaultDNSStrategy, если dns-блок есть, а strategy нет. Без
// restore-направления рантайм-стрижка открыла бы дыру «поставил strategy →
// снял strategy → в merged нет strategy вовсе». Отсутствующий dns-блок —
// no-op: чужие секции не материализуем.
func reconcileBaseDNSStrategyMap(m map[string]any, routingOwns bool) bool {
	dns, _ := m["dns"].(map[string]any)
	if dns == nil {
		return false
	}
	_, has := dns["strategy"]
	switch {
	case routingOwns && has:
		delete(dns, "strategy")
		return true
	case !routingOwns && !has:
		dns["strategy"] = baseDefaultDNSStrategy
		return true
	}
	return false
}

// reconcileBaseDNSStrategy — бут-шаг reconcile-dns-strategy.
func reconcileBaseDNSStrategy(configDir string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	basePath := filepath.Join(configDir, "00-base.json")
	m, ok := readSlotJSON(stepReconcileDNSStrategy, basePath, log)
	if !ok {
		return
	}
	if !reconcileBaseDNSStrategyMap(m, routingSlotOwnsDNSStrategy(configDir, stepReconcileDNSStrategy, log)) {
		return
	}
	writeSlotJSON(stepReconcileDNSStrategy, basePath, m, log)
}

// stripStrayDirectPlaceholder removes the canonical
// {type:"direct", tag:"direct"} placeholder from every slot file in
// configDir EXCEPT 00-base.json. Sing-box rejects the merged config
// with "duplicate outbound/endpoint tag: direct" when the placeholder
// appears in more than one slot — the typical cause is a v2.8.x
// single-file config.json that migrated to 10-tunnels.json before
// commit 1186280b (2026-05-03) wired filterOutDirectPlaceholder into
// the migration path. patchBaseDirectOutbound then injects the
// placeholder into 00-base.json as well, creating the collision.
//
// User-customised direct outbounds that DO have additional fields
// (e.g. bind_interface) are also dropped — same semantics as
// filterOutDirectPlaceholder, used during the legacy migration. The
// canonical placeholder is owned by 00-base.json; if a user needs a
// per-WAN direct outbound, they should give it a distinct tag.
//
// Subdirectories (disabled/, pending/) are skipped — sing-box does not
// merge them. Idempotent: a clean slot tree is a no-op.
func stripStrayDirectPlaceholder(configDir string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	entries, err := os.ReadDir(configDir)
	if err != nil {
		if !os.IsNotExist(err) {
			logConfigPatchWarn(log, "singbox config reconcile: read failed",
				"step", stepStripStrayDirect, "path", configDir, "err", err)
		}
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "00-base.json" || filepath.Ext(name) != ".json" {
			continue
		}
		slotPath := filepath.Join(configDir, name)
		m, ok := readSlotJSON(stepStripStrayDirect, slotPath, log)
		if !ok {
			continue
		}
		before, _ := m["outbounds"].([]any)
		if len(before) == 0 {
			continue
		}
		after := filterOutDirectPlaceholder(before)
		if len(after) == len(before) {
			continue
		}
		m["outbounds"] = after
		writeSlotJSON(stepStripStrayDirect, slotPath, m, log)
	}
}

// legacyCacheFilePath is the hardcoded path some older sing-box docs/configs
// suggested. It lives under a read-only Entware mount so cache writes
// silently fail. We treat it as a known-bad migration target, not as a
// legitimate user customization.
const legacyCacheFilePath = "/opt/etc/sing-box/cache.db"

// patchBaseCacheFilePath ensures experimental.cache_file is present with a
// writable path. Three cases:
//
//  1. Block missing entirely — add it with enabled:true + defaultCacheDBPath.
//     Older installs predating our cache_file work didn't include the block;
//     adding it post-hoc gives them the same on-disk benefits as fresh installs.
//
//  2. Relative path ("cache.db") — sing-box resolves against CWD which is "/"
//     when the manager runs as a service on Entware. Replace with absolute.
//
//  3. Legacy absolute path /opt/etc/sing-box/cache.db — known-bad value from
//     older docs / pre-2.x installer drafts. Read-only on Entware. Replace
//     with defaultCacheDBPath.
//
// Any OTHER user-set absolute path is left untouched (legitimate
// customization).
func patchBaseCacheFilePath(basePath string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepPatchBaseCacheFile, basePath, log)
	if !ok {
		return
	}
	exp, ok := m["experimental"].(map[string]any)
	if !ok {
		// experimental block missing entirely — out of scope for cache_file
		// patcher. Other patches (clash_port etc.) handle their own gaps.
		return
	}

	cf, ok := exp["cache_file"].(map[string]any)
	if !ok {
		// Case 1: block missing — add it.
		exp["cache_file"] = map[string]any{
			"enabled": true,
			"path":    defaultCacheDBPath,
		}
		writeSlotJSON(stepPatchBaseCacheFile, basePath, m, log)
		return
	}

	path, _ := cf["path"].(string)
	switch {
	case path == "":
		// Empty/missing path — set to absolute default.
		cf["path"] = defaultCacheDBPath
	case !strings.HasPrefix(path, "/"):
		// Case 2: relative path — rewrite to absolute.
		cf["path"] = defaultCacheDBPath
	case path == legacyCacheFilePath:
		// Case 3: known-bad legacy absolute — replace.
		cf["path"] = defaultCacheDBPath
	default:
		// Any other absolute path — legitimate user customization, leave alone.
		return
	}
	writeSlotJSON(stepPatchBaseCacheFile, basePath, m, log)
}

// patchTunnelsSlotStripBaseOwnedBlocks self-heals 10-tunnels.json files polluted
// by a pre-fix bootstrap. Older NewConfig() emitted log/dns/experimental
// into the fresh skeleton — when AddTunnels (operator.go AddTunnels →
// loadOrInitConfig) created 10-tunnels.json for the first time, those
// base-owned blocks landed in the tunnels slot. The cross-slot validator
// then rejects every subsequent reload with "duplicate-dns: dns-bootstrap
// (also declared in [base])", blocking subscription saves and any other
// reload-triggering write.
//
// This patcher reads the slot file, runs dns.servers through
// filterOutOurDNSServers (drops dns-bootstrap / dns-doh, keeps custom
// user resolvers), and rewrites the file. The `dns` key is removed
// entirely when nothing user-relevant remains, restoring the canonical
// slot shape (no DNS in 10-tunnels.json).
//
// Idempotent: no-op when the file is missing, when there is no `dns`
// key, or when the dns block has no servers from the owned-set. Safe to
// run on every NewOperator. Also strips top-level `log` from the
// tunnels slot: log.level is base-owned (00-base.json), and leaving a
// stale log block in 10-tunnels.json can override user-selected base
// level during config merge.
func patchTunnelsSlotStripBaseOwnedBlocks(tunnelsPath string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepStripBaseOwnedBlocks, tunnelsPath, log)
	if !ok {
		return
	}
	changed := false
	if _, hasLog := m["log"]; hasLog {
		delete(m, "log")
		changed = true
	}
	dns, ok := m["dns"].(map[string]any)
	if !ok {
		if changed {
			writeSlotJSON(stepStripBaseOwnedBlocks, tunnelsPath, m, log)
		}
		return
	}
	servers, _ := dns["servers"].([]any)
	filtered := filterOutOurDNSServers(servers)

	// Detect whether anything user-relevant remains. The dns block can be
	// dropped entirely only when servers came back empty AND no user
	// rules/final/strategy were customized beyond what 00-base provides.
	rulesArr, _ := dns["rules"].([]any)
	hasUserRules := len(rulesArr) > 0
	if len(filtered) == 0 && !hasUserRules {
		delete(m, "dns")
		changed = true
	} else {
		if len(filtered) == 0 {
			if _, ok := dns["servers"]; ok {
				delete(dns, "servers")
				changed = true
			}
		} else {
			dns["servers"] = filtered
			changed = true
		}
		// Strip final/strategy keys that mirror 00-base defaults — they
		// would otherwise persist as zombie config noise after the
		// owned-set servers vanish.
		if final, _ := dns["final"].(string); final == "dns-doh" || final == "dns-bootstrap" {
			delete(dns, "final")
			changed = true
		}
		// Strip the strategy that mirrors the 00-base default ("prefer_ipv4"),
		// plus the legacy "ipv4_only" default from pre-prefer_ipv4 installs —
		// both are base-owned leakage in this slot, not user intent.
		if strategy, _ := dns["strategy"].(string); strategy == "prefer_ipv4" || strategy == "ipv4_only" {
			delete(dns, "strategy")
			changed = true
		}
		if len(dns) == 0 {
			delete(m, "dns")
			changed = true
		}
	}
	if changed {
		writeSlotJSON(stepStripBaseOwnedBlocks, tunnelsPath, m, log)
	}
}

// patchSlotOutboundCompat чинит уже лежащие на диске outbound'ы слота:
// naive без udp_over_tcp (UDP мёртв) и hysteria2 с TLS-опциями, которые не
// переживают включённый по умолчанию chrome-парротинг sing-box 1.14.0-beta.7.
// Без этого шага несовместимый туннель остался бы мёртвым до первой ручной
// правки. См. EnsureOutboundCompat.
func patchSlotOutboundCompat(slotPath string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepOutboundCompat, slotPath, log)
	if !ok {
		return
	}
	changed := false
	obs, _ := m["outbounds"].([]any)
	for _, v := range obs {
		if ob, ok := v.(map[string]any); ok && EnsureOutboundCompat(ob) {
			changed = true
		}
	}
	if changed {
		writeSlotJSON(stepOutboundCompat, slotPath, m, log)
	}
}

// defaultBootstrapDNS — исторический адрес bootstrap-резолвера. Остаётся
// дефолтом, когда пользователь не задал свой (issue #770: у части мобильных
// операторов 1.1.1.1 блокируется, и sing-box остаётся без резолвинга).
const defaultBootstrapDNS = "1.1.1.1"

// freshBaseConfig returns the canonical base sing-box config. Single source
// of truth for ensureBaseConfig (initial write + self-heal path). Empty
// bootstrapDNS falls back to defaultBootstrapDNS.
func freshBaseConfig(logLevel, bootstrapDNS string) map[string]any {
	if bootstrapDNS == "" {
		bootstrapDNS = defaultBootstrapDNS
	}
	return map[string]any{
		"log": map[string]any{"level": normalizeSingboxLogLevel(logLevel), "timestamp": true},
		"experimental": map[string]any{
			// MUST match clashAPIAddr — our ClashClient and LogForwarder
			// connect here. Hard-coding 9090 (sing-box default) used to
			// silently break log forwarding on existing installs.
			"clash_api": map[string]any{"external_controller": clashAPIAddr},
			// Absolute path to writable dir. Sing-box default resolves
			// relative path against CWD which is "/" (read-only on Entware) —
			// caused FATAL on user installs.
			"cache_file": map[string]any{
				"enabled": true,
				"path":    defaultCacheDBPath,
			},
		},
		"dns": map[string]any{
			// dns.strategy stays in base — base legitimately owns the
			// router-disabled default (prefer_ipv4). strategy is a genuine
			// scalar default with no first-server fallback, so it must be
			// present when the router slot is absent. The self-heal
			// (removeDNSFinalFromBase) strips it only when 20-router.json
			// owns strategy.
			"strategy": "prefer_ipv4",
			"servers": []any{
				map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": bootstrapDNS},
			},
			// dns.final intentionally omitted — owned by 20-router.json
			// (bug #445). sing-box resolves conflicting scalar sub-keys of
			// `dns` FIRST-FILE-WINS across config.d, so a base dns.final
			// would shadow the user's choice. With final absent sing-box
			// falls back to the FIRST dns server; base's list is
			// [dns-bootstrap] and router servers concatenate AFTER base, so
			// router-disabled keeps dns-bootstrap and router-enabled lets the
			// user's dns.final win (only one slot then sets it). Mirrors the
			// route.final omission below. See spec
			// 2026-05-21-route-final-router-owned-design.md.
		},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{
			// route.final intentionally omitted — owned by 20-router.json.
			// Sing-box uses first outbound (= direct, see outbounds above)
			// as fallback when final is absent. See spec
			// 2026-05-21-route-final-router-owned-design.md.
			"default_domain_resolver": "dns-bootstrap",
		},
	}
}

// ValidateConfigDir runs `sing-box check` over the entire config.d.
// Used by callers that just wrote a fragment and want to verify the
// merged config is valid before reload.
func (o *Operator) ValidateConfigDir(ctx context.Context) error {
	return o.validator.Validate(o.configPath)
}

// ApplyLogLevel updates 00-base.json log.level and ensures log.timestamp
// is present. When orchestrator is wired, writes through SlotBase so
// validate+reload lifecycle stays centralized.
func (o *Operator) ApplyLogLevel(level string) error {
	desired := normalizeSingboxLogLevel(level)
	basePath := filepath.Join(o.configPath, "00-base.json")

	var base map[string]any
	data, err := os.ReadFile(basePath)
	switch {
	case os.IsNotExist(err):
		base = freshBaseConfig(desired, "")
	case err != nil:
		return fmt.Errorf("read 00-base.json: %w", err)
	default:
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			return fmt.Errorf("parse 00-base.json: %w", err)
		}
		if parsed == nil {
			parsed = map[string]any{}
		}
		logBlock, _ := parsed["log"].(map[string]any)
		if logBlock == nil {
			logBlock = map[string]any{}
			parsed["log"] = logBlock
		}
		logBlock["level"] = desired
		if _, ok := logBlock["timestamp"]; !ok {
			logBlock["timestamp"] = true
		}
		base = parsed
	}

	raw, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal 00-base.json: %w", err)
	}

	if o.orch != nil {
		if err := o.orch.Save(orchestrator.SlotBase, raw); err != nil {
			return fmt.Errorf("save base slot: %w", err)
		}
		return nil
	}

	if err := writeJSONFile(basePath, base); err != nil {
		return fmt.Errorf("write base file: %w", err)
	}
	if running, _ := o.proc.IsRunning(); running {
		if err := o.proc.Reload(); err != nil {
			return fmt.Errorf("reload sing-box: %w", err)
		}
	}
	return nil
}

// ReconcileBaseDNSStrategy — рантайм-вариант шага reconcile-dns-strategy:
// зовётся роутер-сервисом после применения dns-globals, чтобы выбор strategy
// вступил в силу без перезапуска демона (мерж скаляров first-file-wins,
// 00-base затеняет 20/21). Запись через оркестратор — валидация +
// коалесцированный reload (как ApplyLogLevel).
func (o *Operator) ReconcileBaseDNSStrategy() error {
	basePath := filepath.Join(o.configPath, "00-base.json")
	data, err := os.ReadFile(basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read 00-base.json: %w", err)
	}
	var base map[string]any
	if err := json.Unmarshal(data, &base); err != nil {
		return fmt.Errorf("parse 00-base.json: %w", err)
	}
	owns := routingSlotOwnsDNSStrategy(o.configPath, stepReconcileDNSStrategy, o.log)
	if !reconcileBaseDNSStrategyMap(base, owns) {
		return nil
	}

	raw, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal 00-base.json: %w", err)
	}

	if o.orch != nil {
		if err := o.orch.Save(orchestrator.SlotBase, raw); err != nil {
			return fmt.Errorf("save base slot: %w", err)
		}
		return nil
	}

	if err := writeJSONFile(basePath, base); err != nil {
		return fmt.Errorf("write base file: %w", err)
	}
	if running, _ := o.proc.IsRunning(); running {
		if err := o.proc.Reload(); err != nil {
			return fmt.Errorf("reload sing-box: %w", err)
		}
	}
	return nil
}

// ApplyBootstrapDNS приводит адрес dns-bootstrap в 00-base.json к значению
// настройки без перезапуска демона (issue #770). Пустое значение — no-op:
// настройка снята, файл остаётся как есть. Запись через оркестратор —
// валидация + коалесцированный reload, как у ApplyLogLevel.
func (o *Operator) ApplyBootstrapDNS(server string) error {
	if server == "" {
		return nil
	}
	basePath := filepath.Join(o.configPath, "00-base.json")
	data, err := os.ReadFile(basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read 00-base.json: %w", err)
	}
	var base map[string]any
	if err := json.Unmarshal(data, &base); err != nil {
		return fmt.Errorf("parse 00-base.json: %w", err)
	}
	if !setBootstrapServer(base, server) {
		return nil
	}

	raw, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal 00-base.json: %w", err)
	}

	if o.orch != nil {
		if err := o.orch.Save(orchestrator.SlotBase, raw); err != nil {
			return fmt.Errorf("save base slot: %w", err)
		}
		return nil
	}

	if err := writeJSONFile(basePath, base); err != nil {
		return fmt.Errorf("write base file: %w", err)
	}
	if running, _ := o.proc.IsRunning(); running {
		if err := o.proc.Reload(); err != nil {
			return fmt.Errorf("reload sing-box: %w", err)
		}
	}
	return nil
}

// ReconcileBaseDomainResolver — рантайм-вариант шага
// patch-base-domain-resolver: зовётся роутер-сервисом после того, как
// содержимое routing-слота стало активным. Без него включение fakeip-tun
// оставляло бы в базе свой default_domain_resolver, который затеняет
// роутерный (скаляры route мержатся first-file-wins, 00-base первый).
func (o *Operator) ReconcileBaseDomainResolver() error {
	basePath := filepath.Join(o.configPath, "00-base.json")
	data, err := os.ReadFile(basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read 00-base.json: %w", err)
	}
	var base map[string]any
	if err := json.Unmarshal(data, &base); err != nil {
		return fmt.Errorf("parse 00-base.json: %w", err)
	}
	owns := routingSlotOwnsDomainResolver(o.configPath, stepPatchBaseDomainResolve, o.log)
	if !reconcileBaseDomainResolverMap(base, owns) {
		return nil
	}

	raw, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal 00-base.json: %w", err)
	}
	if o.orch != nil {
		if err := o.orch.Save(orchestrator.SlotBase, raw); err != nil {
			return fmt.Errorf("save base slot: %w", err)
		}
		return nil
	}
	if err := writeJSONFile(basePath, base); err != nil {
		return fmt.Errorf("write base file: %w", err)
	}
	if running, _ := o.proc.IsRunning(); running {
		if err := o.proc.Reload(); err != nil {
			return fmt.Errorf("reload sing-box: %w", err)
		}
	}
	return nil
}

// preflightConfigDir validates config.d/ before any action that would
// have sing-box parse it (cold start, post-write reload, etc.).
//
// Runs our local configmerge first: when two slot files contribute
// conflicting tags inside the same merged array, MergeDir returns a
// *configmerge.CollisionError naming BOTH offending files —
//
//	"tag collision: outbounds \"direct\" appears in both
//	 00-base.json and 10-tunnels.json"
//
// sing-box itself only reports the tag ("duplicate outbound/endpoint
// tag: direct"), so surfacing our message into LastError gives users
// an actionable diagnostic without needing SSH access to grep through
// config.d/. Then runs the outbound-feature gating step, and finally
// `sing-box check` for everything our merge doesn't cover (parse
// errors, schema violations, unknown option keys, etc.).
func (o *Operator) preflightConfigDir() error {
	if _, err := configmerge.MergeDir(o.configPath); err != nil {
		return err
	}
	if err := o.checkOutboundFeatures(); err != nil {
		return err
	}
	return o.validator.Validate(o.configPath)
}

// checkOutboundFeatures scans every config.d JSON fragment for outbounds
// whose declared "type" requires a build tag the installed sing-box
// binary does not declare, and returns a human-readable error BEFORE we
// hand control to `sing-box check`. Without this pre-check, sing-box
// reports only:
//
//	FATAL decode config …: outbounds[N]: unknown outbound type: naive
//
// which leaves the user guessing whether it's a typo, a bad sub, or an
// outdated binary. Our error names the specific missing build tag and
// recommends upgrading the pinned sing-box.
func (o *Operator) checkOutboundFeatures() error {
	ctx, cancel := context.WithTimeout(context.Background(), singboxVersionProbeTimeout)
	defer cancel()
	_, features := o.detectVersionAndFeaturesCached(ctx)
	// Пустой список тегов — это «не удалось определить», а не «фич нет».
	// Бинарь мог не отдать строку Tags вовсе; гейт на пути старта процесса
	// не имеет права резать конфиг по такой догадке.
	if len(features) == 0 {
		return nil
	}
	entries, err := os.ReadDir(o.configPath)
	if err != nil {
		return nil
	}
	var unsupported []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(o.configPath, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var cfg map[string]any
		if json.Unmarshal(data, &cfg) != nil {
			continue
		}
		obs, ok := cfg["outbounds"].([]any)
		if !ok {
			continue
		}
		for i, raw := range obs {
			ob, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := ob["type"].(string)
			tag, _ := ob["tag"].(string)
			required := OutboundTypeRequiresFeature(typ)
			if required == "" {
				continue
			}
			if OutboundSupportedByFeatures(features, typ) {
				continue
			}
			if tag == "" {
				tag = fmt.Sprintf("<no-tag index=%d>", i)
			}
			unsupported = append(unsupported, fmt.Sprintf(
				"%s outbounds[%d] %q (%s): missing sing-box build tag %q, update sing-box to version supporting %s",
				name, i, tag, typ, required, typ,
			))
		}
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("unsupported outbound type in config.d:\n  %s", strings.Join(unsupported, "\n  "))
	}
	return nil
}
