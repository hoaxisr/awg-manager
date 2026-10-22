package zapret

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Форков много, и имя каталога установки не выводится из названия пакета:
// zapret2 ставится в /opt/zapret2, z2k и KZM2 — тоже в /opt/zapret2, но
// бывают и другие (встречается /opt/zapret22). На реальном роутере
// одновременно лежали все три, и активным был /opt/zapret22, а
// /opt/zapret2 — протухшая копия. Поэтому каталоги не перечисляются, а
// находятся поиском: берём всё, что похоже на zapret и содержит config.
//
// Пути заданы переменными ради тестов — тот же приём, что в
// internal/hydraroute/detect.go.
var (
	optRoot = "/opt"
	// binaryRelCandidates — где внутри установки лежит демон. Раскладка
	// отличается между zapret (nfq/) и zapret2 (nfq2/).
	binaryRelCandidates = []string{
		filepath.Join("nfq2", "nfqws2"),
		filepath.Join("nfq", "nfqws"),
		"nfqws2",
		"nfqws",
	}
)

// initDirs — каталоги init-скриптов. z2k кладёт свой S99zapret2 обычным
// файлом (cp, не симлинк) в /opt/etc/init.d, KZM2 —
// симлинк S90-zapret2 на штатный init.d/sysv/zapret2, «чистый» zapret2
// кладёт sysv/zapret2 внутрь себя. Вместо угадывания имени перебираем
// каталоги и ищем файл, в имени которого есть «zapret».
var initDirs = []string{
	"/opt/etc/init.d",
	"/opt/zapret2/init.d/sysv",
	"/opt/zapret/init.d/sysv",
	"/opt/zapret22/init.d/sysv",
}

// layout — разобранное содержимое установленного zapret: где лежит база,
// какой файл конфига и в какой переменной живут опции демона.
type layout struct {
	base   string
	config string
	binary string
	optVar string // "NFQWS2_OPT" (zapret2) или "NFQWS_OPT" (zapret)
	ipset  string // каталог со списками доменов
	init   string // полный путь к init-скрипту, "" если не найден
}

// baseOfExe выделяет корень установки из пути к исполняемому файлу:
// /opt/zapret22/nfq2/nfqws2 → /opt/zapret22
func baseOfExe(exe string) string {
	if exe == "" {
		return ""
	}
	clean := strings.TrimSuffix(exe, " (deleted)")
	clean = filepath.Clean(clean)
	// Отрезаем известные подкаталоги с демоном.
	for _, rel := range binaryRelCandidates {
		if filepath.Base(filepath.Dir(clean)) == filepath.Dir(rel) {
			return filepath.Dir(filepath.Dir(clean))
		}
	}
	// Фолбэк: первый сегмент после корня, если он похож на zapret.
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	for _, p := range parts {
		if strings.Contains(strings.ToLower(p), "zapret") {
			acc := ""
			for _, q := range parts {
				acc += "/" + q
				if q == p {
					return acc
				}
			}
		}
	}
	return ""
}

// canonicalBase resolves installation aliases so a process launched through
// a symlink and an installation discovered through /opt compare as one base.
func canonicalBase(base string) string {
	base = filepath.Clean(strings.TrimSuffix(base, " (deleted)"))
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		return filepath.Clean(resolved)
	}
	return base
}

// discoverBasesRaw finds direct installations under optRoot. Stat follows
// directory symlinks, while canonical paths deduplicate aliases. Discovery
// order has no selection meaning; the service resolver chooses the live or
// last-running installation.
func discoverBasesRaw() []string {
	entries, err := os.ReadDir(optRoot)
	if err != nil {
		return nil
	}
	var bases []string
	seen := make(map[string]bool)
	for _, e := range entries {
		if !strings.Contains(strings.ToLower(e.Name()), "zapret") {
			continue
		}
		base := filepath.Join(optRoot, e.Name())
		baseInfo, err := os.Stat(base)
		if err != nil || !baseInfo.IsDir() {
			continue
		}
		identity := canonicalBase(base)
		configInfo, err := os.Stat(filepath.Join(base, "config"))
		if err != nil || configInfo.IsDir() || seen[identity] {
			continue
		}
		seen[identity] = true
		bases = append(bases, base)
	}
	return bases
}

// discoverLayouts returns candidates with a supported config format without
// selecting one by modification time. The service resolver owns selection.
func discoverLayouts() []layout {
	bases := discoverBasesRaw()
	out := make([]layout, 0, len(bases))
	for _, base := range bases {
		l := layout{
			base:   base,
			config: filepath.Join(base, "config"),
			ipset:  filepath.Join(base, "ipset"),
		}
		if l.optVar = detectOptVar(l.config); l.optVar == "" {
			continue
		}
		l.binary = findBinary(base)
		l.init = findInitScript(base)
		out = append(out, l)
	}
	return out
}

// findBinary ищет демон внутри базы. Путь строится от базы, а не
// захардкожен: у разных форков он разный, а каталог мы уже нашли.
func findBinary(base string) string {
	for _, rel := range binaryRelCandidates {
		candidate := filepath.Join(base, rel)
		if isExecutable(candidate) {
			return candidate
		}
	}
	return ""
}

// detectOptVar определяет переменную конфига, в которой лежат параметры
// демона.
//
// zapret2 всегда NFQWS2_OPT. У zapret v1 вариантов три — NFQWS_OPT,
// TPWS_OPT и TPWS_SOCKS_OPT, — и какая из них рабочая, задаёт MODE.
// Поэтому сначала смотрим MODE и только потом падаем на первую
// непустую: иначе у v1 в режиме tpws мы бы показывали и правили не тот
// блок, а демон читал бы другой.
func detectOptVar(configPath string) string {
	body, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	text := string(body)

	if findAssignment(text, optVarV2) != "" {
		return optVarV2
	}
	if mode := findAssignment(text, "MODE"); mode != "" {
		if v, ok := modeOptVars[strings.ToLower(strings.TrimSpace(mode))]; ok {
			if findAssignment(text, v) != "" {
				return v
			}
		}
	}
	for _, v := range []string{optVarV1, optVarTPWS, optVarTPWSSocks} {
		if findAssignment(text, v) != "" {
			return v
		}
	}
	return ""
}

const (
	optVarV2        = "NFQWS2_OPT"
	optVarV1        = "NFQWS_OPT"
	optVarTPWS      = "TPWS_OPT"
	optVarTPWSSocks = "TPWS_SOCKS_OPT"
)

// modeOptVars — соответствие MODE zapret v1 переменной с опциями
// (значения из config.default и install_easy.sh upstream).
var modeOptVars = map[string]string{
	"nfqws":      optVarV1,
	"tpws":       optVarTPWS,
	"tpws-socks": optVarTPWSSocks,
}

// flavorFor различает поколения по раскладке, а не по имени каталога:
// zapret2 лежит в nfq2/ и использует NFQWS2_OPT, zapret v1 — в nfq/ и
// NFQWS_OPT. Имя каталога может быть любым (встречается /opt/zapret22).
func flavorFor(base, optVar string) string {
	if optVar == optVarV2 {
		return "zapret2"
	}
	if optVar != "" {
		return "zapret"
	}
	if _, err := os.Stat(filepath.Join(base, "nfq2")); err == nil {
		return "zapret2"
	}
	return "zapret"
}

func isExecutable(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode().Perm()&0111 != 0
}

// findInitScript перебирает каталоги init-скриптов в поисках файла, в
// имени которого есть «zapret». Точное имя не фиксируем намеренно: у нас
// оно S99zapret2 (z2k), S90-zapret2 (KZM2), zapret2 (stock sysv),
// S99zapret (zapret v1) — список разъезжается с каждым форком, а
// совпадение по подстроке переживает все четыре.
func findInitScript(base string) string {
	base = filepath.Clean(base)
	identity := canonicalBase(base)
	// A global init entry is usable only when its resolved target belongs to
	// this installation or the script pins ZAPRET_BASE to it (z2k copies its
	// S99zapret2 as a plain file). Then search the selected base's own init
	// directory.
	dirs := append([]string{}, initDirs...)
	if base != "" {
		dirs = append(dirs, filepath.Join(base, "init.d", "sysv"))
	}

	seen := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		if seen[d] {
			continue
		}
		seen[d] = true
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.Contains(strings.ToLower(e.Name()), "zapret") {
				continue
			}
			full := filepath.Join(d, e.Name())
			if !isExecutable(full) {
				continue
			}
			resolved, err := filepath.EvalSymlinks(full)
			if err != nil || (!pathWithin(identity, resolved) && !initPinsBase(resolved, identity)) {
				continue
			}
			return full
		}
	}
	return ""
}

// initPinsBase reports whether an init script outside the installation
// drives it: the script has a start verb and assigns ZAPRET_BASE to it
// literally. Other zapret-named helpers (GUI, schedulers) may name the base
// too, but they are not init scripts and must not receive start/stop. The
// script is read, never sourced: a value with expansions is ignored.
func initPinsBase(script, identity string) bool {
	body, err := os.ReadFile(script)
	if err != nil || !slices.Contains(caseLabels(body), "start") {
		return false
	}
	// Unquoted assignments may carry a trailing comment or separator.
	value := strings.Trim(findAssignment(string(body), "ZAPRET_BASE"), "\"'")
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return false
	}
	base := strings.TrimSuffix(fields[0], ";")
	if !filepath.IsAbs(base) || strings.ContainsAny(base, "$`") {
		return false
	}
	return canonicalBase(base) == identity
}

var caseLabelRe = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_|-]+)\s*\)`)

// caseLabels returns the alternatives of every `label)` line in a shell
// case statement, as an init script spells its verbs.
func caseLabels(body []byte) []string {
	var out []string
	for _, m := range caseLabelRe.FindAllSubmatch(body, -1) {
		out = append(out, strings.Split(string(m[1]), "|")...)
	}
	return out
}

func pathWithin(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	root = canonicalBase(root)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
