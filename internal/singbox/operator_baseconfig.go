package singbox

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// defaultCacheDBPath is the absolute path for sing-box's experimental.cache_file.
// Must live in a writable directory — sing-box resolves relative paths against
// CWD ("/" when the manager runs as a service on Entware), which is read-only.
// var (not const) because filepath.Join requires runtime evaluation; tests can
// override defaultDir to redirect this too.
var defaultCacheDBPath = filepath.Join(defaultDir, "cache.db")

// tempCacheDBPath — cache.db в RAM (tmpfs): бережёт NAND-флеш роутера от
// постоянных записей sing-box в кэш (fakeip-карта, выбор outbound в Clash,
// rule-set) (issue #842).
// Переменная, как defaultCacheDBPath: тесты перенаправляют её в t.TempDir().
var tempCacheDBPath = "/tmp/singbox-cache.db"

// legacyCacheFilePath — путь из старых доков sing-box; лежит на read-only
// монтировании Entware, записи кэша молча падают. Заведомо негодный, а не
// осознанная правка пользователя.
const legacyCacheFilePath = "/opt/etc/sing-box/cache.db"

// cacheDBPathFor — эффективный путь cache.db (issue #842). Настройка задана
// (flash | tmp) — путём владеет она. Настройка пуста — путём владеет
// пользователь: абсолютный путь из base (рукописный, до появления настройки
// его правили прямо в 00-base.json) остаётся, а заведомо негодный —
// относительный (sing-box резолвит от CWD, под службой Entware это "/"),
// legacy или отсутствующий — заменяется дефолтом на флеше. Единственный
// источник пути для базы, overlay 21-fakeip.json (через Operator.CacheDBPath)
// и статуса, поэтому рукописный путь действует во всех режимах.
func cacheDBPathFor(location string, base map[string]any) string {
	switch location {
	case storage.CacheFileLocationTmp:
		return tempCacheDBPath
	case storage.CacheFileLocationFlash:
		return defaultCacheDBPath
	}
	if p := currentCacheFilePath(base); filepath.IsAbs(p) && p != legacyCacheFilePath {
		return p
	}
	return defaultCacheDBPath
}

// ensureBaseConfig writes a minimal 00-base.json if config.d is
// empty, so sing-box starts standalone (direct outbound + bootstrap DNS) before
// any tunnels are added. Also surgically self-heals an older base config
// that hard-coded the wrong Clash API port (9090 instead of ours), which
// silently broke our LogForwarder / DelayChecker on existing installs.
func ensureBaseConfig(configDir, desiredLogLevel, desiredBootstrapDNS string, desiredClashPort int, desiredCacheLocation string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	basePath := filepath.Join(configDir, "00-base.json")
	if _, err := os.Stat(basePath); err == nil {
		patchBaseClashPort(basePath, desiredClashPort, log)
		patchBaseLogLevel(basePath, desiredLogLevel, log)
		patchBaseDirectOutbound(basePath, log)
		patchBaseCacheFilePath(basePath, desiredCacheLocation, log)
		patchBaseBootstrapDNS(basePath, desiredBootstrapDNS, log)
		return
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		logConfigPatchWarn(log, "singbox config reconcile: mkdir failed",
			"step", stepEnsureBaseConfig, "path", configDir, "err", err)
		return
	}
	writeSlotJSON(stepEnsureBaseConfig, basePath, freshBaseConfig(desiredLogLevel, desiredBootstrapDNS, desiredClashPort, cacheDBPathFor(desiredCacheLocation, nil)), log)
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
	stepEnsureBaseConfig      = "ensure-base-config"
	stepPatchBaseClashPort    = "patch-base-clash-port"
	stepPatchBaseLogLevel     = "patch-base-log-level"
	stepPatchBaseDirectOutbnd = "patch-base-direct-outbound"
	stepPatchBaseCacheFile    = "patch-base-cache-file"
	stepPatchBaseBootstrapDNS = "patch-base-bootstrap-dns"
	stepMigrateLegacyTunnels  = "migrate-legacy-tunnels"
	stepStripBaseOwnedBlocks  = "strip-base-owned-blocks"
	stepOutboundCompat        = "outbound-compat"
	stepStripStrayDirect      = "strip-stray-direct"
	stepRemoveRouteFinal      = "remove-route-final"
	stepRemoveDNSFinal        = "remove-dns-final"
	stepDerivedDefaults       = "reconcile-derived-defaults"
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
func reconcileConfigSteps(dir, configPath, desiredLogLevel, desiredBootstrapDNS string, desiredClashPort int, desiredCacheLocation string, log *slog.Logger) []reconcileStep {
	base := filepath.Join(configPath, "00-base.json")
	tunnels := filepath.Join(configPath, "10-tunnels.json")
	return []reconcileStep{
		{stepEnsureBaseConfig, func() {
			ensureBaseConfig(configPath, desiredLogLevel, desiredBootstrapDNS, desiredClashPort, desiredCacheLocation, log)
		}},
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
		{stepDerivedDefaults, func() { reconcileDerivedDefaults(configPath, log) }},
	}
}

// derivedDefaultsSlot — содержимое 99-defaults.json. Константа: слот несёт
// ровно те скаляры, у которых нет собственного «хозяина по умолчанию», и
// перекрывается любым слотом выше по first-file-wins.
func derivedDefaultsSlot() map[string]any {
	return map[string]any{
		// optimistic (sing-box 1.14): протухший ответ отдаётся сразу, обновление
		// в фоне (окно 3 суток по умолчанию). Здесь, а не в базе, чтобы
		// 90-user.json мог перебить по first-file-wins.
		"dns": map[string]any{"strategy": baseDefaultDNSStrategy, "optimistic": true},
		// Резолвер — ОБЪЕКТОМ, а не строкой, хотя оба варианта sing-box
		// принимает: режимные слоты пишут его как {"server": …}, а слить
		// объект со строкой merge не умеет — «cannot merge json object into
		// string», FATAL на старте (стенд 2026-08-24, поймано именно так).
		// Пока дефолт жил в 00-base, типы не сталкивались: примирение убирало
		// ключ из базы, как только слот брал владение. Теперь оба источника
		// присутствуют ВСЕГДА, и совпадение типов — обязательное условие.
		"route": map[string]any{
			"default_domain_resolver": map[string]any{"server": baseDefaultDomainResolver},
		},
	}
}

// reconcileDerivedDefaults переносит дефолты условных скаляров из 00-base в
// 99-defaults.json — то есть из ВЫИГРЫВАЮЩЕЙ позиции merge в проигрывающую.
//
// Скаляры dns.strategy и route.default_domain_resolver принадлежат тому слоту
// режима, который сейчас активен, а базовое значение — лишь дефолт «когда
// хозяина нет». Пока дефолт лежал в 00-base (лексически ПЕРВОМ, а merge —
// first-file-wins), уступать приходилось активно: код читал чужие слот-файлы,
// вычислял владение и переписывал базу на каждом промежуточном шаге
// транзакции. За один переход режима база менялась дважды в противоположные
// стороны, и каждая запись планировала reload — при живом tun это полный
// Stop+Start движка (стенд 2026-08-24). Из 99 дефолт проигрывает ПАССИВНО:
// владение не вычисляет никто, его решает сам merge в момент применения.
//
// Порядок внутри шага обязателен: сперва создать 99, потом стричь базу. Иначе
// падение между двумя записями оставило бы конфиг без резолвера вовсе —
// sing-box отвергает такой merged (FATAL «missing route.default_domain_resolver
// … in dial fields»). Оба действия в ОДНОМ шаге по той же причине и ради
// попарной коммутативности набора (TestReconcileConfigSteps_CommuteReversed).
//
// Из базы снимаются ТОЛЬКО наши значения: чужое (в том числе легаси
// "ipv4_only") пользователь поставил осознанно, оно остаётся и продолжает
// затенять — та же философия, что была у резолвера.
func reconcileDerivedDefaults(configDir string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	defaultsPath := filepath.Join(configDir, "99-defaults.json")
	want := derivedDefaultsSlot()
	if cur, ok := readSlotJSON(stepDerivedDefaults, defaultsPath, log); !ok || !sameJSON(cur, want) {
		// Стрижка базы ТОЛЬКО после подтверждённой записи 99: провал (ENOSPC,
		// права) иначе оставил бы конфиг вообще без резолвера — sing-box
		// отвергает такой merged и не стартует до следующего удачного бута.
		if !writeSlotJSON(stepDerivedDefaults, defaultsPath, want, log) {
			return
		}
	}

	basePath := filepath.Join(configDir, "00-base.json")
	base, ok := readSlotJSON(stepDerivedDefaults, basePath, log)
	if !ok {
		return
	}
	if !stripOurDerivedDefaults(base) {
		return
	}
	writeSlotJSON(stepDerivedDefaults, basePath, base, log)
	logConfigPatchInfo(log, "singbox config reconcile: дефолты скаляров переехали в 99-defaults.json",
		"step", stepDerivedDefaults, "path", basePath)
}

// stripOurDerivedDefaults убирает из базы наши дефолтные значения обоих
// скаляров; чужие оставляет. Возвращает, изменилась ли карта.
func stripOurDerivedDefaults(base map[string]any) bool {
	changed := false
	if dns, _ := base["dns"].(map[string]any); dns != nil {
		if v, _ := dns["strategy"].(string); v == baseDefaultDNSStrategy || v == "ipv4_only" {
			delete(dns, "strategy")
			changed = true
		}
	}
	if route, _ := base["route"].(map[string]any); route != nil {
		// Историческая база несёт резолвер строкой, но объектную форму тоже
		// снимаем: она наша, если внутри наш сервер.
		switch v := route["default_domain_resolver"].(type) {
		case string:
			if v == baseDefaultDomainResolver {
				delete(route, "default_domain_resolver")
				changed = true
			}
		case map[string]any:
			if srv, _ := v["server"].(string); srv == baseDefaultDomainResolver && len(v) == 1 {
				delete(route, "default_domain_resolver")
				changed = true
			}
		}
		// Чужое значение остаётся, но СТРОКУ приводим к объектной форме:
		// 99-defaults несёт объект, а слить объект со строкой merge sing-box
		// не умеет («cannot merge json object into string», FATAL). Раньше
		// коллизии не было — база была единственным источником, когда владельца
		// нет. Значение при этом не меняется, меняется только форма записи.
		if v, ok := route["default_domain_resolver"].(string); ok && v != "" {
			route["default_domain_resolver"] = map[string]any{"server": v}
			changed = true
		}
	}
	return changed
}

// sameJSON сравнивает две карты по сериализации — дешевле рефлексии и ровно
// та же семантика, что у побайтового гейта записи слота.
func sameJSON(a, b map[string]any) bool {
	ja, ea := json.Marshal(a)
	jb, eb := json.Marshal(b)
	return ea == nil && eb == nil && bytes.Equal(ja, jb)
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

// filterOutOurDNSServers removes dns.servers entries tagged with our two
// historical tags, "dns-bootstrap" and "dns-doh". "dns-doh" is a legacy
// phantom: the current 00-base.json (freshBaseConfig) does not emit it —
// only pre-fix installs and old config.json snapshots still carry it — but
// this owned-set exists precisely to strip that historical pollution out of
// 10-tunnels.json, so the tag stays listed here on purpose (see F43,
// docs/plans/2026-08-29-base-config-defects.md). All other entries —
// user-added custom resolvers — pass through so they end up in
// 10-tunnels.json and survive the migration.
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
	if !setLogLevel(m, desiredLevel) {
		return
	}
	writeSlotJSON(stepPatchBaseLogLevel, basePath, m, log)
}

// patchBaseClashPort приводит experimental.clash_api.external_controller к
// нашему адресу (порт из настроек, хост всегда 127.0.0.1). Остальные поля —
// пользовательские правки уровня лога, DNS-серверов и прочего — сохраняются
// дословно. No-op, только когда адрес уже правильный.
//
// Отсутствующий блок clash_api ВОССТАНАВЛИВАЕТСЯ, а не считается намерением
// пользователя: Clash API — служебный канал управления awg-manager, и его
// значением и наличием владеем мы (ADR 0001). Без блока молча слепнут
// LogForwarder, DelayChecker, /connections и селекторы подписок, причём
// sing-box при этом стартует нормально и в журнале не будет ни строчки.
func patchBaseClashPort(basePath string, port int, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepPatchBaseClashPort, basePath, log)
	if !ok {
		return
	}
	if !setClashController(m, ClashAddr(port)) {
		return
	}
	writeSlotJSON(stepPatchBaseClashPort, basePath, m, log)
}

// setLogLevel ставит log.level в нормализованный desired и добивает
// log.timestamp, создавая недостающие уровни. Возвращает false, если менять
// нечего. Общий для стартовой сверки (patchBaseLogLevel) и ApplyLogLevel —
// иначе два места по-разному понимали бы «изменилось».
func setLogLevel(m map[string]any, desiredLevel string) bool {
	logBlock, _ := m["log"].(map[string]any)
	if logBlock == nil {
		logBlock = map[string]any{}
		m["log"] = logBlock
	}
	desired := normalizeSingboxLogLevel(desiredLevel)
	changed := false
	if current, _ := logBlock["level"].(string); current != desired {
		logBlock["level"] = desired
		changed = true
	}
	if _, ok := logBlock["timestamp"]; !ok {
		logBlock["timestamp"] = true
		changed = true
	}
	return changed
}

// setClashController ставит experimental.clash_api.external_controller в want,
// создавая недостающие уровни. Возвращает false, если адрес уже такой.
func setClashController(m map[string]any, want string) bool {
	exp, _ := m["experimental"].(map[string]any)
	if exp == nil {
		exp = map[string]any{}
		m["experimental"] = exp
	}
	clash, _ := exp["clash_api"].(map[string]any)
	if clash == nil {
		clash = map[string]any{}
		exp["clash_api"] = clash
	}
	if current, _ := clash["external_controller"].(string); current == want {
		return false
	}
	clash["external_controller"] = want
	return true
}

// setBootstrapServer ставит адрес серверу с тегом dns-bootstrap. Возвращает
// false, если менять нечего: записи нет или адрес уже такой. Не создаёт
// запись — этим занимается reconcileBootstrapServer, который решает, что
// делать при отсутствующей записи.
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

// reconcileBootstrapServer владеет ФАКТОМ наличия записи dns-bootstrap
// (F44): 99-defaults.json ссылается на этот тег безусловно
// (reconcileDerivedDefaults), а наш же кросс-слотовый валидатор даёт
// блокирующий unknown-dns-server на висячую ссылку — без записи ни один
// reload/cold start не проходит. Адресом при непустой настройке продолжает
// владеть менеджер, при пустой — пользователь (issue #770): до появления
// настройки адрес правили руками прямо в 00-base.json, такие правки обязаны
// выжить.
//
//   - записи нет → создаём {type:udp, tag:dns-bootstrap, server: want, а при
//     пустой настройке — defaultBootstrapDNS}, достраивая недостающие уровни
//     dns/servers;
//   - запись есть, want == "" → не трогаем;
//   - запись есть, want != "" → правим адрес через setBootstrapServer.
//
// Осознанная потеря: пользователь, перенёсший объявление тега dns-bootstrap
// в свой слот (90-user.json) и удаливший запись из базы, получит после
// самолечения блокирующий duplicate-dns с именами обоих файлов — явную
// ошибку вместо тихого «ни один reload не проходит» (см. ADR-0002).
//
// Возвращает true, если base изменена (нужна запись файла).
func reconcileBootstrapServer(base map[string]any, want string) bool {
	// Ключ есть, но это не объект (строка, число, массив) — чужое содержимое
	// неизвестной формы. Такой 00-base.json движок не загрузит в любом случае,
	// а подмена его нашей картой стёрла бы то, что пользователь туда положил:
	// молча выходим, как выходил прежний setBootstrapServer. То же и для
	// dns.servers не-массивом. Достраиваем только ОТСУТСТВУЮЩИЕ уровни;
	// литеральный null считается отсутствием, а не чужим содержимым.
	raw, has := base["dns"]
	dns, _ := raw.(map[string]any)
	if has && raw != nil && dns == nil {
		return false
	}
	if dns == nil {
		dns = map[string]any{}
		base["dns"] = dns
	}
	rawServers, hasServers := dns["servers"]
	servers, _ := rawServers.([]any)
	if hasServers && rawServers != nil && servers == nil {
		return false
	}
	for _, v := range servers {
		s, _ := v.(map[string]any)
		if s == nil || s["tag"] != "dns-bootstrap" {
			continue
		}
		if want == "" {
			return false
		}
		return setBootstrapServer(base, want)
	}
	dns["servers"] = append(servers, map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": bootstrapAddr(want)})
	return true
}

// bootstrapAddr — адрес, который получит СОЗДАВАЕМАЯ запись dns-bootstrap:
// настройка, а при пустой — исторический дефолт. Общий с логом
// patchBaseBootstrapDNS: два вычисления «какой адрес мы применили»
// разъехались бы, и лог начал бы врать.
func bootstrapAddr(want string) string {
	if want == "" {
		return defaultBootstrapDNS
	}
	return want
}

// patchBaseBootstrapDNS приводит запись dns-bootstrap в 00-base.json к
// желаемому состоянию через reconcileBootstrapServer: наличием записи
// владеет менеджер, адресом при непустой настройке — тоже менеджер, при
// пустой — пользователь.
func patchBaseBootstrapDNS(basePath, want string, loggers ...*slog.Logger) {
	want = sanitizeBootstrapDNS(want)
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepPatchBaseBootstrapDNS, basePath, log)
	if !ok {
		return
	}
	if !reconcileBootstrapServer(m, want) {
		return
	}
	if !writeSlotJSON(stepPatchBaseBootstrapDNS, basePath, m, log) {
		return
	}
	logConfigPatchInfo(log, "singbox base config reconciled",
		"patch", stepPatchBaseBootstrapDNS,
		"path", basePath,
		"newServer", bootstrapAddr(want),
	)
}

// baseDefaultDomainResolver — тег резолвера по умолчанию. Живёт в
// 99-defaults.json и действует, пока никакой слот выше не задал свой.
const baseDefaultDomainResolver = "dns-bootstrap"

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
// operator init (right after ensureBaseConfig) so existing on-disk
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
// dns.strategy сюда не относится — ею занимается reconcileDerivedDefaults:
// у strategy нет
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

// baseDefaultDNSStrategy — значение dns.strategy по умолчанию. Живёт в
// 99-defaults.json и действует, пока никакой слот выше его не задаёт. У
// strategy нет fallback'а на первый dns-сервер (в отличие от dns.final),
// поэтому её отсутствие в merged-конфиге — не «дефолт», а другое поведение;
// именно поэтому дефолт обязан лежать в слоте, а не отсутствовать вовсе.
const baseDefaultDNSStrategy = "prefer_ipv4"

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

// reconcileCacheFile приводит experimental.cache_file в 00-base.json к пути
// по cacheDBPathFor. Общий мутатор стартового примирения
// (patchBaseCacheFilePath) и живого применения (ApplyCacheFileLocation);
// хвост правки — лог и снос устаревшего кэша — у них тоже общий
// (finishCacheFileChange). Отсутствующие блоки experimental/cache_file
// достраиваем. Возвращает эффективный путь и признак «base изменена».
func reconcileCacheFile(base map[string]any, location string) (want string, changed bool) {
	want = cacheDBPathFor(location, base)
	exp, _ := base["experimental"].(map[string]any)
	if exp == nil {
		exp = map[string]any{}
		base["experimental"] = exp
	}
	storeDNS := router.StoreDNSForCachePath(want)
	cf, _ := exp["cache_file"].(map[string]any)
	if cf == nil {
		cf = map[string]any{"enabled": true, "path": want}
		if storeDNS {
			cf["store_dns"] = true
		}
		exp["cache_file"] = cf
		return want, true
	}
	sd, _ := cf["store_dns"].(bool)
	if cf["path"] == want && sd == storeDNS {
		return want, false
	}
	cf["path"] = want
	if storeDNS {
		cf["store_dns"] = true
	} else {
		delete(cf, "store_dns")
	}
	return want, true
}

// finishCacheFileChange — хвост состоявшейся правки пути: лог со старым и
// новым путём (иначе рукописный путь исчез бы молча) и снос кэша по
// покинутому пути, если он один из наших двух — иначе откат поднял бы
// протухшую fakeip-карту. Рукописный путь при переезде не трогаем: файл не
// наш (сброс кэша при смене пула — другая история, там путь эффективный).
// Открытый файл sing-box доживает до reload, снос его не рвёт.
func finishCacheFileChange(log *slog.Logger, basePath, was, want string) {
	logConfigPatchInfo(log, "singbox base config reconciled",
		"patch", stepPatchBaseCacheFile,
		"path", basePath,
		"oldCachePath", loggedCachePath(was),
		"newCachePath", want,
	)
	if was != defaultCacheDBPath && was != tempCacheDBPath {
		return
	}
	if was == want {
		// путь не менялся — файл живой, сносить нечего (changed мог стать
		// true из-за store_dns, а не переезда).
		return
	}
	switch err := os.Remove(was); {
	case err == nil:
		logConfigPatchInfo(log, "singbox stale cache.db removed", "patch", stepPatchBaseCacheFile, "cachePath", was)
	case !os.IsNotExist(err):
		logConfigPatchWarn(log, "singbox stale cache.db not removed", "patch", stepPatchBaseCacheFile, "cachePath", was, "err", err)
	}
}

// currentCacheFilePath — путь cache_file из base как есть; "" если блока нет.
func currentCacheFilePath(base map[string]any) string {
	exp, _ := base["experimental"].(map[string]any)
	cf, _ := exp["cache_file"].(map[string]any)
	path, _ := cf["path"].(string)
	return path
}

// loggedCachePath — старый путь для лога: пустой значит «блока не было», а
// не «путь был пуст».
func loggedCachePath(was string) string { return cmp.Or(was, "<none>") }

// patchBaseCacheFilePath — стартовый шаг примирения cache_file (см.
// reconcileCacheFile, finishCacheFileChange).
func patchBaseCacheFilePath(basePath, location string, loggers ...*slog.Logger) {
	log := firstLogger(loggers)
	m, ok := readSlotJSON(stepPatchBaseCacheFile, basePath, log)
	if !ok {
		return
	}
	was := currentCacheFilePath(m)
	want, changed := reconcileCacheFile(m, location)
	if !changed || !writeSlotJSON(stepPatchBaseCacheFile, basePath, m, log) {
		return
	}
	finishCacheFileChange(log, basePath, was, want)
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

// sanitizeBootstrapDNS отсеивает всё, что не является литеральным IP.
// Настройка может прийти невалидной из settings.json: API валидирует только
// присланное в патче (иначе одно протухшее значение заперло бы пользователя
// вне всех настроек), поэтому последний рубеж здесь. Домен или host:port в
// dns-bootstrap роняет движок на старте: «missing domain resolver for domain
// server address» — резолвить это имя нечем, оно и есть первый резолвер.
func sanitizeBootstrapDNS(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || net.ParseIP(v) == nil {
		return ""
	}
	return v
}

// defaultBootstrapDNS — исторический адрес bootstrap-резолвера. Остаётся
// дефолтом, когда пользователь не задал свой (issue #770: у части мобильных
// операторов 1.1.1.1 блокируется, и sing-box остаётся без резолвинга).
const defaultBootstrapDNS = "1.1.1.1"

// freshBaseConfig returns the canonical base sing-box config. Single source
// of truth for ensureBaseConfig (initial write + self-heal path). Empty
// bootstrapDNS falls back to defaultBootstrapDNS.
func freshBaseConfig(logLevel, bootstrapDNS string, clashPort int, cachePath string) map[string]any {
	bootstrapDNS = sanitizeBootstrapDNS(bootstrapDNS)
	if bootstrapDNS == "" {
		bootstrapDNS = defaultBootstrapDNS
	}
	return map[string]any{
		"log": map[string]any{"level": normalizeSingboxLogLevel(logLevel), "timestamp": true},
		"experimental": map[string]any{
			// MUST match ClashClient.Address() — our ClashClient and
			// LogForwarder connect here. Hard-coding 9090 (sing-box default)
			// used to silently break log forwarding on existing installs.
			"clash_api": map[string]any{"external_controller": ClashAddr(clashPort)},
			// Absolute path to writable dir. Sing-box default resolves
			// relative path against CWD which is "/" (read-only on Entware) —
			// caused FATAL on user installs.
			"cache_file": map[string]any{
				"enabled": true,
				"path":    cachePath,
			},
		},
		"dns": map[string]any{
			// dns.strategy НЕ здесь — дефолт живёт в 99-defaults.json, в
			// проигрывающей позиции merge (см. reconcileDerivedDefaults).
			// В базе он был бы в выигрывающей и затенял бы выбор слота.
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
			// default_domain_resolver тоже не здесь — см. dns.strategy выше.
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
	return o.mutateBase(func(base map[string]any) bool {
		return setLogLevel(base, desired)
	})
}

// desiredBootstrapDNS — текущая настройка адреса bootstrap-резолвера;
// пусто, когда замыкание не подключено (тестовые wiring'и) или адрес не задан.
func (o *Operator) desiredBootstrapDNS() string {
	if o.bootstrapDNS == nil {
		return ""
	}
	return sanitizeBootstrapDNS(o.bootstrapDNS())
}

// desiredSingboxLogLevel — уровень журнала sing-box из настроек; без
// замыкания (тестовые wiring'и) — исторический дефолт.
func (o *Operator) desiredSingboxLogLevel() string {
	if o.singboxLogLevel == nil {
		return normalizeSingboxLogLevel("")
	}
	return normalizeSingboxLogLevel(o.singboxLogLevel())
}

// desiredClashPort — порт Clash API из настроек; 0 означает DefaultClashPort.
func (o *Operator) desiredClashPort() int {
	if o.clashPort == nil {
		return 0
	}
	return o.clashPort()
}

// desiredCacheLocation — настройка места хранения cache.db (issue #842);
// "" — не задана.
func (o *Operator) desiredCacheLocation() string {
	if o.cacheFileLocation == nil {
		return ""
	}
	return o.cacheFileLocation()
}

// CacheDBPath — эффективный путь cache.db (см. cacheDBPathFor). Роутер берёт
// его для overlay 21-fakeip.json и статуса: overlay перекрывает базу в merge,
// и второй источник пути разводил бы режимы. База читается только при пустой
// настройке и с nil-логгером (метка шага при нём инертна): битый 00-base.json
// — не событие этого чтения, о нём говорят шаги примирения и валидатор.
func (o *Operator) CacheDBPath() string {
	location := o.desiredCacheLocation()
	var base map[string]any
	if location == "" {
		base, _ = readSlotJSON(stepPatchBaseCacheFile, filepath.Join(o.configPath, "00-base.json"), nil)
	}
	return cacheDBPathFor(location, base)
}

// ApplyClashPort приводит experimental.clash_api.external_controller в
// 00-base.json к новому порту и перенаправляет туда же наш ClashClient
// (issue #788). Перезапуск демона не нужен: SIGHUP в форке — это Close +
// пересоздание инстанса, так что слушающий сокет Clash API переезжает сам.
//
// Применение АСИНХРОННОЕ: при подключённом оркестраторе запись лишь взводит
// debounced reload (250 мс), и валидация merged-конфига произойдёт уже после
// возврата отсюда. Отсюда окно в сотни миллисекунд, когда клиент смотрит на
// порт, который ещё никто не слушает, — его закрывают ретраи LogForwarder и
// TrafficAggregator. Если же merged-конфиг сломан чем-то ПОСТОРОННИМ, reload
// откажет валидацией и демон останется на старом порту, а клиент уедет на
// новый; сам порт валидацию сломать не может.
//
// ClashClient переставляется ПОСЛЕ успешной записи: провалившаяся запись
// оставила бы клиент смотреть в порт, которого в конфиге нет.
func (o *Operator) ApplyClashPort(port int) error {
	addr := ClashAddr(port)
	if err := o.mutateBase(func(base map[string]any) bool {
		return setClashController(base, addr)
	}); err != nil {
		return err
	}
	o.clash.SetAddress(addr)
	return nil
}

// ApplyBootstrapDNS приводит запись dns-bootstrap в 00-base.json к значению
// настройки без перезапуска демона (issue #770). Наличием записи владеет
// менеджер (F44): отсутствующая запись самолечится даже при пустой
// настройке — иначе unknown-dns-server валит следующий reload вместо
// тихого no-op. Пустое значение при СУЩЕСТВУЮЩЕЙ записи — no-op (ручные
// правки адреса обязаны выжить).
func (o *Operator) ApplyBootstrapDNS(server string) error {
	server = sanitizeBootstrapDNS(server)
	return o.mutateBase(func(base map[string]any) bool {
		return reconcileBootstrapServer(base, server)
	})
}

// ApplyCacheFileLocation приводит experimental.cache_file.path в 00-base.json
// к месту хранения из настройки (flash | tmp, issue #842). Применяется через
// reload оркестратора: при tun это Stop+Start, без tun — SIGHUP, который в
// форке тоже Close + пересоздание, так что соединения рвутся в любом режиме.
// Хвост правки общий со стартовым шагом (finishCacheFileChange).
func (o *Operator) ApplyCacheFileLocation(location string) error {
	was, want, changed := "", "", false
	err := o.mutateBase(func(base map[string]any) bool {
		was = currentCacheFilePath(base)
		want, changed = reconcileCacheFile(base, location)
		return changed
	})
	// Хвост только по факту правки: без config.d оркестратор выходит до
	// мутатора, а при совпадении путей мутатор говорит «менять нечего».
	if err == nil && changed {
		finishCacheFileChange(o.log, filepath.Join(o.configPath, "00-base.json"), was, want)
	}
	return err
}

// mutateBase — общий транспорт правки 00-base.json: прочитать, дать мутатору
// решить, менять ли (false — выходим без записи), записать через
// оркестратор — там валидация merged-конфига и коалесцированный reload.
//
// Чтение и запись идут ОДНОЙ транзакцией оркестратора (Mutate): читать файл
// самим, а потом звать Save, значило бы работать со снимком, взятым вне лока,
// — параллельная правка другого скаляра терялась (дефект F41).
//
// Оркестратор обязателен: в проде SetOrch вызывается до старта HTTP
// (wiring_singbox.go), «раннего бута» без него не существует — все четыре
// вызывающих достижимы только из HTTP: ApplyLogLevel/ApplyClashPort/
// ApplyBootstrapDNS через хуки server_routes.go, ApplyCacheFileLocation —
// через router.Deps из UpdateSettings (wiring_server.go). Тесты поднимают
// оркестратор сами (см. newOrchedOperator).
func (o *Operator) mutateBase(mutate func(map[string]any) bool) error {
	if o.orch == nil {
		return fmt.Errorf("mutate base config: orchestrator not wired")
	}
	// Кандидат на восстановление считается ДО взятия лока оркестратора:
	// desired* ходят в SettingsStore, а под чужим локом блокирующей работе
	// делать нечего. Нужен он только в ветке «файла нет».
	fresh := freshBaseConfig(o.desiredSingboxLogLevel(), o.desiredBootstrapDNS(), o.desiredClashPort(), cacheDBPathFor(o.desiredCacheLocation(), nil))
	return o.orch.Mutate(orchestrator.SlotBase, func(data []byte, exists bool) ([]byte, error) {
		var base map[string]any
		restored := false
		if exists {
			if err := json.Unmarshal(data, &base); err != nil {
				return nil, fmt.Errorf("parse 00-base.json: %w", err)
			}
			// Файл с литеральным null — валидный JSON, дающий nil-карту;
			// запись в неё паникует. Гард был у прежнего ApplyLogLevel и
			// потерялся при сведении к общему транспорту.
			if base == nil {
				base = map[string]any{}
			}
		} else {
			// Пропал ТОЛЬКО файл, каталог на месте — ВОССТАНАВЛИВАЕМ, а не
			// считаем намерением пользователя, симметрично трактовке
			// отсутствующего блока clash_api в ADR-0001. Молчаливый выход
			// оставлял настройку сохранённой в settings.json, но не доехавшей
			// до базы до следующего бута.
			//
			// Нет самого config.d — движок удалён вместе с каталогом, и
			// воскрешать его правкой настройки нельзя: молчим, как молчали.
			// Прежде этим различием не владел никто — ApplyLogLevel
			// восстанавливал базу даже поверх снесённого каталога, а
			// ApplyClashPort/ApplyBootstrapDNS не восстанавливали никогда.
			if _, statErr := os.Stat(o.configPath); os.IsNotExist(statErr) {
				return nil, nil
			} else if statErr != nil {
				return nil, fmt.Errorf("stat config.d: %w", statErr)
			}
			base = fresh
			restored = true
		}
		// Свежая база уже несёт желаемые значения, так что мутатор на ней
		// обычно говорит «менять нечего»; писать всё равно надо — файла-то нет.
		if !mutate(base) && !restored {
			return nil, nil
		}
		raw, err := json.MarshalIndent(base, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal 00-base.json: %w", err)
		}
		return raw, nil
	})
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
