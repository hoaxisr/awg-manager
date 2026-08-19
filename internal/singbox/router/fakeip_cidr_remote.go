package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// ruleSetDecompileTimeout — потолок одного `sing-box rule-set decompile`.
// Щедрый: decompile .srs geosite-масштаба на 580МГц MIPS легально занимает
// минуты. Раньше команда шла через голый exec.Command без контекста и
// таймаута — зависший decompile переживал и stall guard пересборки, и её
// 2-часовой предохранитель (RebuildOwnedRun не возвращался, heavyop-гейт и
// флаг rebuilding залипали навсегда).
const ruleSetDecompileTimeout = 5 * time.Minute

// Exec/IO seams (mirror ruleSetMatchExec / inlineRuleSetCompileExec) so the
// network + binary integration is replaced in tests. Production paths call the
// default* implementations against the real cache + sing-box.
var ruleSetDownload = func(ctx context.Context, url, format string) (string, error) {
	return defaultRuleSetDownload(ctx, url, format)
}
var ruleSetDecompileExec = func(ctx context.Context, binary, srsPath string) ([]byte, error) {
	return defaultRuleSetDecompile(ctx, binary, srsPath)
}
var ruleSetDecompileToFile = func(ctx context.Context, binary, srsPath string) (string, error) {
	return defaultRuleSetDecompileToFile(ctx, binary, srsPath)
}

// remoteCIDRCache is a process-wide on-disk cache for the Tier-2 download path.
// It shares the same sha256-by-URL layout (and default $TMPDIR/awgm-router-rulesets
// dir) as the inspector's cache, so a .srs fetched by either path is reused by the
// other. Lazily constructed; newRuleSetCache itself touches no disk.
var (
	remoteCIDRCacheOnce sync.Once
	remoteCIDRCache     *ruleSetCache
)

func sharedRemoteCIDRCache() *ruleSetCache {
	remoteCIDRCacheOnce.Do(func() { remoteCIDRCache = newRuleSetCache("") })
	return remoteCIDRCache
}

// defaultRuleSetDownload fetches (and caches) a remote rule-set, returning the
// local file path. Reuses ruleSetCache.getOrDownload — the same machinery the
// inspector uses. format influences only the cache filename extension. emit is
// nil here (no Inspect progress channel during reconcile); tag is best-effort.
func defaultRuleSetDownload(_ context.Context, url, format string) (string, error) {
	if format == "" {
		format = inferFormat(url)
	}
	return sharedRemoteCIDRCache().getOrDownload(url, format, nil, "")
}

// defaultRuleSetDecompileToFile runs sing-box rule-set decompile into a temp JSON
// file and returns its path. The caller must remove the file when done.
// Команда подчинена ctx вызывающего (guard-контекст пересборки может её
// отменить) и ограничена собственным щедрым exec-таймаутом
// ruleSetDecompileTimeout; sysexec даёт WaitDelay и добивание process group.
func defaultRuleSetDecompileToFile(ctx context.Context, binary, srsPath string) (string, error) {
	if binary == "" {
		return "", fmt.Errorf("no sing-box binary for decompile")
	}
	tmp, err := os.CreateTemp("", "awgm-decompile-*.json")
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	jsonPath := tmp.Name()
	_ = tmp.Close()

	res, err := sysexec.RunWithOptions(ctx, binary,
		[]string{"rule-set", "decompile", "--output", jsonPath, srsPath},
		sysexec.Options{Timeout: ruleSetDecompileTimeout})
	if err != nil {
		_ = os.Remove(jsonPath)
		return "", sysexec.FormatError(res, fmt.Errorf("sing-box decompile: %w", err))
	}
	return jsonPath, nil
}

// defaultRuleSetDecompile runs `sing-box rule-set decompile --output <tmp.json>
// <srs>` (the genpresets form) and returns the decompiled source JSON bytes.
// sing-box writes to the --output file rather than stdout, so we point it at a
// temp file, read it back, and remove it. An empty binary (dev box without
// sing-box) yields an error the caller logs + skips.
func defaultRuleSetDecompile(ctx context.Context, binary, srsPath string) ([]byte, error) {
	jsonPath, err := defaultRuleSetDecompileToFile(ctx, binary, srsPath)
	if err != nil {
		return nil, err
	}
	defer os.Remove(jsonPath)
	return os.ReadFile(jsonPath)
}

// singboxBinary returns the configured sing-box binary path, or "" on a dev box
// without one wired (defaultRuleSetDecompile then errors and the set is skipped).
func (s *ServiceImpl) singboxBinary() string {
	if s.deps.Singbox != nil {
		return s.deps.Singbox.Binary()
	}
	return ""
}

// ruleSetFileFacts — всё, что Tier-2 берёт из СОДЕРЖИМОГО скачанного набора:
// пригоден ли он к merged matching и какие CIDR несёт. Обе величины следуют
// только из файла, поэтому запоминаются вместе с его mtime+размером.
type ruleSetFileFacts struct {
	mod       time.Time
	size      int64
	mergeable bool
	cidrs     []string
}

// ruleSetFactsByPath помнит разбор каждого скачанного набора. Без него каждый
// 30-секундный тик reconcile спавнил `sing-box rule-set decompile` на КАЖДЫЙ
// remote-набор (issue #756) — на 580МГц MIPS это секунды CPU в тик, вечно, при
// том что файл в кэше живёт час. Ключ — путь файла; запись самозаменяется на
// перекачке, так что карта не растёт сверх числа наборов.
var (
	ruleSetFactsMu     sync.Mutex
	ruleSetFactsByPath = map[string]ruleSetFileFacts{}
)

// factsForRuleSetFile разбирает скачанный набор, пропуская decompile, когда
// файл не менялся с прошлого раза. Провал stat (файла нет) отключает память
// для этого вызова — fail-open: разбираем как раньше.
func (s *ServiceImpl) factsForRuleSetFile(ctx context.Context, path, format string) (ruleSetFileFacts, error) {
	st, statErr := os.Stat(path)
	if statErr == nil {
		ruleSetFactsMu.Lock()
		f, ok := ruleSetFactsByPath[path]
		ruleSetFactsMu.Unlock()
		if ok && f.size == st.Size() && f.mod.Equal(st.ModTime()) {
			return f, nil
		}
	}

	var raw []byte
	var err error
	if strings.HasSuffix(path, ".json") || format == "source" {
		raw, err = os.ReadFile(path)
	} else {
		raw, err = ruleSetDecompileExec(ctx, s.singboxBinary(), path)
	}
	if err != nil {
		return ruleSetFileFacts{}, fmt.Errorf("read/decompile: %w", err)
	}
	var src inlineRuleSetSource
	if e := json.Unmarshal(raw, &src); e != nil {
		return ruleSetFileFacts{}, fmt.Errorf("parse: %w", e)
	}

	facts := ruleSetFileFacts{
		mergeable: mergeableRuleSetRule(RuleSet{Rules: src.Rules}) != nil,
		cidrs:     ruleSetCIDRs(RuleSet{Rules: src.Rules}),
	}
	if statErr == nil {
		facts.mod, facts.size = st.ModTime(), st.Size()
		ruleSetFactsMu.Lock()
		ruleSetFactsByPath[path] = facts
		ruleSetFactsMu.Unlock()
	}
	return facts, nil
}

// remoteTunCIDRs downloads + decompiles each remote rule-set referenced by a
// LOOP-SAFE proxy route-rule and returns the normalized v4/v6 ip_cidr it contains.
// Gating on loopSafeProxyRule (not just isProxyRoute) is the loop-safety contract:
// a remote set's IPs may only be routed to the tun if the referencing rule's only
// matchers are ip_cidr/rule_set, so a by-IP packet to those CIDRs is guaranteed to
// proxy and never fall through to route.final=direct (which would loop back to the
// tun). Best-effort: any per-set failure is skipped (logged, not fatal).
func (s *ServiceImpl) remoteTunCIDRs(ctx context.Context, cfg *RouterConfig) (v4 []string, v6 []string) {
	if cfg == nil {
		return nil, nil
	}
	byTag := make(map[string]RuleSet, len(cfg.Route.RuleSet))
	for _, rs := range cfg.Route.RuleSet {
		byTag[rs.Tag] = rs
	}
	want := map[string]RuleSet{}
	// standalone — на набор ссылается правило БЕЗ собственного ip_cidr. Только
	// тогда его CIDR безопасны независимо от мержимости набора (см. ниже).
	standalone := map[string]bool{}
	for _, r := range cfg.Route.Rules {
		flat, isOr := addressOrBranches(r)
		switch {
		case isOr:
			// Нормализованная форма: ветка rule_set совпадает сама по себе,
			// собственные адреса правила ей не условие → standalone.
			if !isProxyRoute(r) {
				continue
			}
		case loopSafeProxyRule(r):
			flat = r
		default:
			continue
		}
		for _, tag := range flat.RuleSet {
			if rs, ok := byTag[tag]; ok && rs.Type == "remote" && rs.URL != "" {
				want[tag] = rs
				if isOr || len(flat.IPCIDR) == 0 {
					standalone[tag] = true
				}
			}
		}
	}
	seen := map[string]bool{}
	add := func(c string) {
		norm, is4, ok := normalizeCIDR(c)
		if !ok || seen[norm] {
			return
		}
		seen[norm] = true
		if is4 {
			v4 = append(v4, norm)
		} else {
			v6 = append(v6, norm)
		}
	}
	for _, rs := range want {
		path, err := ruleSetDownload(ctx, rs.URL, rs.Format)
		if err != nil {
			s.appLog.Warn("fakeip-cidr-remote", rs.Tag, "download: "+err.Error())
			continue
		}
		facts, err := s.factsForRuleSetFile(ctx, path, rs.Format)
		if err != nil {
			s.appLog.Warn("fakeip-cidr-remote", rs.Tag, err.Error())
			continue
		}
		// Тот же beta.1-гейт merged matching, что в desiredTunCIDRs, но по
		// фактическому содержимому скачанного набора (в конфиге Rules пуст).
		// Если на набор ссылаются ТОЛЬКО правила с собственным ip_cidr, его
		// CIDR безопасны лишь когда набор mergeable — иначе внешнее правило по
		// такому пакету не совпадёт и он уйдёт в route.final=direct → петля.
		// Гейт зависит от КОНФИГА, поэтому решается здесь, а не в памяти
		// разбора: та помнит только то, что следует из самого файла.
		if !standalone[rs.Tag] && !facts.mergeable {
			continue
		}
		for _, c := range facts.cidrs {
			add(c)
		}
	}
	return v4, v6
}
