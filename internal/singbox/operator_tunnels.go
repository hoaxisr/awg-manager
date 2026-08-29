package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
	"github.com/hoaxisr/awg-manager/internal/sys/perftrace"
)

// tunnelsFile is the canonical path for the tunnels.json fragment
// (config.d/10-tunnels.json). Used by loadConfig + HasUserTunnels.
func (o *Operator) tunnelsFile() string {
	return filepath.Join(o.configPath, "10-tunnels.json")
}

// ListTunnels returns the current tunnels from config.json enriched with
// per-tunnel runtime state (Running = process-alive && TUN exists).
func (o *Operator) ListTunnels(ctx context.Context) ([]TunnelInfo, error) {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "ListTunnels", "total", time.Now())
	cfg, err := o.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return []TunnelInfo{}, nil
		}
		return nil, err
	}
	tunnels := cfg.Tunnels()
	procAlive, _ := o.proc.IsRunning()
	ndmsEnabled := o.isNDMSProxyEnabled()
	for i := range tunnels {
		t := &tunnels[i]
		if ndmsEnabled && t.KernelInterface != "" {
			t.Running = procAlive && kernelInterfaceExists(t.KernelInterface)
			continue
		}
		// NDMS Proxy off → нет t2sN в ядре, проверяем outbound через Clash.
		// Полей ProxyInterface/KernelInterface не должно быть видно наверх:
		// Tunnels() парсер derives их из listenPort всегда, здесь чистим.
		t.Running = procAlive && o.clash.HasOutbound(t.Tag)
		t.ProxyInterface = ""
		t.KernelInterface = ""
	}
	return tunnels, nil
}

// kernelInterfaceExists probes /sys/class/net/<name> to confirm the TUN
// created by sing-box is currently present in the kernel. Empty name (the
// tunnel has no kernelInterface hint) always returns false — we cannot
// assert running state without a concrete interface to check.
func kernelInterfaceExists(name string) bool {
	if name == "" {
		return false
	}
	_, err := os.Stat("/sys/class/net/" + name)
	return err == nil
}

// GetTunnel returns the full outbound JSON for one tag.
func (o *Operator) GetTunnel(ctx context.Context, tag string) (json.RawMessage, error) {
	cfg, err := o.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %q", ErrTunnelNotFound, tag)
		}
		return nil, err
	}
	return cfg.GetOutbound(tag)
}

// tunnelTagsInUse returns outbound tags already present in cfg.
func tunnelTagsInUse(cfg *Config) map[string]bool {
	used := make(map[string]bool)
	for _, t := range cfg.Tunnels() {
		used[t.Tag] = true
	}
	return used
}

// allocUniqueTunnelTag returns base if unused; otherwise base-2, base-3, …
// (Share links often reuse the same URI fragment for different nodes — sing-box
// tags must stay unique.)
func allocUniqueTunnelTag(used map[string]bool, base string) string {
	if base == "" {
		base = "tunnel"
	}
	candidate := base
	if !used[candidate] {
		return candidate
	}
	for n := 2; ; n++ {
		candidate = fmt.Sprintf("%s-%d", base, n)
		if !used[candidate] {
			return candidate
		}
	}
}

// outboundFingerprint извлекает identity-поля outbound для проверки дублей
// при импорте: совпавший fingerprint = тот же VPN-аккаунт. Возвращает ""
// для нераспознанных типов — такие пропускаются (не считаем дублём).
//
// Используется только для prevention двойного добавления через AddTunnels:
// двойной POST от nginx-proxy retry, открытие приложения в двух вкладках,
// и т.п.
func outboundFingerprint(ob map[string]any) string {
	typ, _ := ob["type"].(string)
	server, _ := ob["server"].(string)
	port, _ := toInt(ob["server_port"])
	if server == "" || port == 0 {
		return ""
	}
	var secret string
	switch typ {
	case "vless", "vmess":
		secret, _ = ob["uuid"].(string)
	case "trojan", "hysteria2", "shadowsocks":
		secret, _ = ob["password"].(string)
	case "naive":
		u, _ := ob["username"].(string)
		p, _ := ob["password"].(string)
		secret = u + ":" + p
	default:
		return ""
	}
	if secret == "" {
		return ""
	}
	return fmt.Sprintf("%s|%s|%d|%s", typ, server, port, secret)
}

// existingOutboundFingerprints собирает fingerprints из текущей config.json.
// Используется AddTunnels для prevention дублей.
func existingOutboundFingerprints(cfg *Config) map[string]string {
	out := make(map[string]string)
	for _, ob := range cfg.userOutbounds() {
		fp := outboundFingerprint(ob)
		if fp == "" {
			continue
		}
		tag, _ := ob["tag"].(string)
		out[fp] = tag
	}
	return out
}

func outboundJSONWithTag(raw json.RawMessage, tag string) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("outbound json: %w", err)
	}
	m["tag"] = tag
	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// nextFreeListenPortSlot picks the lowest unused listen-port slot
// (relative to firstPort) among existing tunnels and reserved
// (slots handed out earlier in the same batch). NDMS-free counterpart
// to proxyMgr.NextFreeIndex used when the NDMS Proxy toggle is off.
func nextFreeListenPortSlot(cfg *Config, reserved map[int]bool) int {
	used := make(map[int]bool, len(reserved))
	for k := range reserved {
		used[k] = true
	}
	for _, t := range cfg.Tunnels() {
		slot := t.ListenPort - firstPort
		if slot >= 0 {
			used[slot] = true
		}
	}
	for i := 0; i < maxProxySlots; i++ {
		if !used[i] {
			return i
		}
	}
	return 0
}

// ParseTunnelLinksInput разбирает пользовательский ввод AddTunnels. Обычный
// путь — построчный ParseBatch по share-link'ам, но канонический JSON-конфиг
// клиента mieru (экспорт панелей, формат mieru apply config) — это единый
// многострочный документ: line-split его убивает, поэтому сначала проверяем
// тело целиком. Экспортирована ради валидации bind_interface в API-хендлере
// до вызова AddTunnels (#709).
func ParseTunnelLinksInput(linksText string) vlink.BatchResult {
	if body := []byte(linksText); vlink.IsMieruClientJSON(body) {
		return vlink.ParseMieruClientJSON(body)
	}
	return vlink.ParseBatch(strings.Split(linksText, "\n"))
}

// AddTunnels parses one or more links and atomically adds them.
// Returns successfully-added tunnels and parse errors.
func (o *Operator) AddTunnels(ctx context.Context, linksText string) ([]TunnelInfo, []BatchError, error) {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "AddTunnels", "total", time.Now())
	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()

	// Snapshot the flag once for the whole operation — a flip mid-AddTunnels
	// would split the tunnel's NDMS state from its config.json.
	ndmsProxyEnabled := o.isNDMSProxyEnabled()

	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-add", "", "start add tunnels batch")
	}
	batchResult := ParseTunnelLinksInput(linksText)
	var parseErrs []BatchError
	for _, pe := range batchResult.Errors {
		parseErrs = append(parseErrs, BatchError{Line: pe.LineIdx + 1, Input: pe.Scheme, Err: fmt.Errorf("%s", pe.Message)})
	}
	if len(batchResult.Outbounds) == 0 {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("single-add", "", "no valid outbounds parsed from input")
		}
		return nil, parseErrs, nil
	}

	cfg, err := o.loadOrInitConfig()
	if err != nil {
		return nil, parseErrs, err
	}
	tagOccupied := tunnelTagsInUse(cfg)
	// fingerprints существующих туннелей — для отбраковки дублей при двойном
	// POST (nginx proxy_next_upstream, две вкладки UI, и т.п.). См.
	// outboundFingerprint выше — сравнение по (protocol, server, port, secret).
	existingFps := existingOutboundFingerprints(cfg)
	// reserved tracks indices we've handed out in this batch so the slot
	// allocator doesn't reuse the same slot twice before the batch is
	// committed (NDMS path) or written to config (port-only path).
	reserved := make(map[int]bool)
	var addedTags []string
	// Pre-gate by sing-box build features BEFORE we mutate cfg or allocate
	// NDMS proxy slots. Outbounds that require a missing sing-box build tag
	// (e.g. naive needs with_naive_outbound) are rejected with a
	// user-visible BatchError: adding them would just fail later at the
	// checkTunnelFeatures step inside ApplyConfig, but stopping early here
	// keeps the rest of the batch intact and avoids partial side-effects.
	//
	// Пустой список = проба не удалась (бинарь ещё не установлен), а не
	// «фич нет»: гейт в этом случае пропускает всё, решение остаётся за
	// самим sing-box.
	features := o.singboxFeaturesCached()
	for _, p := range batchResult.Outbounds {
		if len(features) > 0 && !OutboundSupportedByFeatures(features, p.Protocol) {
			required := OutboundTypeRequiresFeature(p.Protocol)
			if required == "" {
				required = "<unknown required tag>"
			}
			parseErrs = append(parseErrs, BatchError{
				Input: p.Label,
				Err:   fmt.Errorf("%s: missing sing-box build tag %q, update sing-box to a version supporting %s", p.Protocol, required, p.Protocol),
			})
			continue
		}
		// Idempotency: пропускаем если такой же outbound уже есть в config.
		var obParsed map[string]any
		if err := json.Unmarshal(p.Outbound, &obParsed); err == nil {
			if fp := outboundFingerprint(obParsed); fp != "" {
				if existingTag, dup := existingFps[fp]; dup {
					parseErrs = append(parseErrs, BatchError{
						Input: p.Tag,
						Err:   fmt.Errorf("duplicate of existing tunnel %q", existingTag),
					})
					continue
				}
				existingFps[fp] = p.Tag // защита от повтора внутри одного batch
			}
		}
		alloc := func() (int, error) {
			if ndmsProxyEnabled {
				return o.proxyMgr.NextFreeIndex(ctx, reserved)
			}
			return nextFreeListenPortSlot(cfg, reserved), nil
		}
		freeIdx, listenPort, allocErr := allocBindableSlot(reserved, alloc)
		if allocErr != nil {
			parseErrs = append(parseErrs, BatchError{Input: p.Tag, Err: fmt.Errorf("allocate listen port: %w", allocErr)})
			continue
		}
		tag := allocUniqueTunnelTag(tagOccupied, p.Tag)
		outbound, jerr := outboundJSONWithTag(p.Outbound, tag)
		if jerr != nil {
			parseErrs = append(parseErrs, BatchError{Input: p.Tag, Err: jerr})
			continue
		}
		if err := cfg.AddTunnelWithListenPort(tag, p.Protocol, p.Server, int(p.Port), listenPort, outbound); err != nil {
			parseErrs = append(parseErrs, BatchError{Input: p.Tag, Err: err})
			continue
		}
		tagOccupied[tag] = true
		reserved[freeIdx] = true
		addedTags = append(addedTags, tag)
	}
	if len(addedTags) == 0 {
		return nil, parseErrs, nil
	}

	if err := o.ApplyConfig(ctx, cfg); err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-add", "", "apply config failed: "+err.Error())
		}
		return nil, parseErrs, fmt.Errorf("apply: %w", err)
	}

	all := cfg.Tunnels()

	// Create NDMS Proxy interfaces for new tunnels (skipped when toggle is off).
	if ndmsProxyEnabled {
		for _, t := range all {
			for _, newTag := range addedTags {
				if t.Tag != newTag {
					continue
				}
				idx, err := parseProxyIdx(t.ProxyInterface)
				if err != nil {
					o.log.Error("malformed proxy interface post-add", "tag", t.Tag, "iface", t.ProxyInterface, "err", err)
					parseErrs = append(parseErrs, BatchError{Input: t.Tag, Err: fmt.Errorf("ndms proxy setup: %w", err)})
					continue
				}
				if err := o.proxyMgr.EnsureProxy(ctx, idx, t.ListenPort, t.Tag); err != nil {
					o.log.Warn("create proxy failed", "tag", t.Tag, "err", err)
					parseErrs = append(parseErrs, BatchError{Input: t.Tag, Err: fmt.Errorf("ndms proxy setup for %s: %w", t.Tag, err)})
				}
			}
		}
	}

	added := make([]TunnelInfo, 0, len(addedTags))
	for _, t := range all {
		for _, newTag := range addedTags {
			if t.Tag == newTag {
				// When NDMS Proxy is disabled the ProxyInterface/KernelInterface
				// fields are meaningless — clear them so callers don't act on
				// stale interface names.
				if !ndmsProxyEnabled {
					t.ProxyInterface = ""
					t.KernelInterface = ""
				}
				added = append(added, t)
			}
		}
	}
	if o.bus != nil {
		o.bus.Publish("singbox:tunnels-changed", nil)
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-add", "", fmt.Sprintf("done added=%d parse_errors=%d", len(added), len(parseErrs)))
	}
	return added, parseErrs, nil
}

// RemoveTunnel removes outbound+inbound+route+Proxy for a tag.
func (o *Operator) RemoveTunnel(ctx context.Context, tag string) error {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "RemoveTunnel", "total", time.Now())
	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-remove", tag, "start")
	}
	cfg, err := o.loadConfig()
	if err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-remove", tag, "load config failed: "+err.Error())
		}
		return err
	}
	proxyIdx := -1
	for _, t := range cfg.Tunnels() {
		if t.Tag == tag {
			idx, err := parseProxyIdx(t.ProxyInterface)
			if err != nil {
				return fmt.Errorf("tunnel %q has malformed proxy interface %q: %w", tag, t.ProxyInterface, err)
			}
			proxyIdx = idx
			break
		}
	}
	if err := cfg.RemoveTunnel(tag); err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("single-remove", tag, "remove from config failed: "+err.Error())
		}
		return err
	}

	// Commit config/process state BEFORE NDMS teardown so a mid-failure leaves
	// a consistent recoverable state (sing-box config matches on-disk reality).
	//
	// Пишем опустевший слот, а не удаляем файл: гасить ли демона, решает
	// reload оркестратора по hasActiveWork — SlotTunnels считается работой
	// только через HasContent, а пустой конфиг её не даёт. Прямые proc.Stop +
	// os.Remove дублировали это решение, рвали чужие транзакции и оставляли
	// applied-state протухшим.
	//
	// Опустевший слот идёт БЕЗ merged-валидации: она и есть само удаление.
	// Отказать здесь значило бы сделать туннель неудаляемым из-за ЧУЖОЙ
	// поломки — соседнего селектора, для которого он был единственным членом,
	// или отсутствующего бинаря sing-box. Прежний код удалял файл вообще без
	// проверок, так что это паритет, а не послабление.
	if err := o.writeTunnelsSlot(cfg, len(cfg.Tunnels()) > 0); err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-remove", tag, "apply config failed: "+err.Error())
		}
		return err
	}

	// NDMS teardown last — if it fails, Reconcile/retry can clean up later.
	if proxyIdx >= 0 {
		if err := o.proxyMgr.RemoveProxy(ctx, proxyIdx); err != nil {
			o.log.Warn("remove proxy failed", "tag", tag, "err", err)
		}
	}
	if o.bus != nil {
		o.bus.Publish("singbox:tunnels-changed", nil)
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-remove", tag, "done")
	}
	return nil
}

// UpdateTunnel replaces outbound JSON, reloads.
func (o *Operator) UpdateTunnel(ctx context.Context, tag string, outbound json.RawMessage) error {
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-update", tag, "start")
	}
	cfg, err := o.loadConfig()
	if err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-update", tag, "load config failed: "+err.Error())
		}
		return err
	}
	if err := cfg.UpdateTunnel(tag, outbound); err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("single-update", tag, "update outbound failed: "+err.Error())
		}
		return err
	}
	if err := o.ApplyConfig(ctx, cfg); err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-update", tag, "apply config failed: "+err.Error())
		}
		return err
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-update", tag, "done")
	}
	return nil
}

var reservedOutboundTags = map[string]struct{}{
	"direct": {},
	"block":  {},
	"dns":    {},
}

// RenameTunnel changes a single sing-box tunnel tag and rewrites every
// singbox-router reference that points at that outbound.
func (o *Operator) RenameTunnel(ctx context.Context, oldTag, newTag string) error {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "RenameTunnel", "total", time.Now())
	oldTag = strings.TrimSpace(oldTag)
	newTag = strings.TrimSpace(newTag)
	if oldTag == "" || newTag == "" {
		return ErrInvalidTunnelTag
	}
	if _, reserved := reservedOutboundTags[newTag]; reserved {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidTunnelTag, newTag)
	}

	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-rename", oldTag, "start new="+newTag)
	}

	cfg, err := o.loadConfig()
	if err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-rename", oldTag, "load config failed: "+err.Error())
		}
		return err
	}
	var renamed TunnelInfo
	found := false
	for _, t := range cfg.Tunnels() {
		if t.Tag == oldTag {
			renamed = t
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrTunnelNotFound, oldTag)
	}
	if oldTag == newTag {
		return nil
	}
	for _, t := range cfg.Tunnels() {
		if t.Tag == newTag {
			return fmt.Errorf("%w: %q", ErrTunnelTagConflict, newTag)
		}
	}
	for _, v := range cfg.outbounds() {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ob["tag"].(string); t == newTag {
			return fmt.Errorf("%w: %q", ErrTunnelTagConflict, newTag)
		}
	}
	if o.outboundRefs != nil && o.outboundRefs.IsOutboundTagInUse(ctx, newTag) {
		return fmt.Errorf("%w: %q", ErrTunnelTagConflict, newTag)
	}

	if err := cfg.RenameTunnel(oldTag, newTag); err != nil {
		return err
	}
	refsRenamed := false
	if o.outboundRefs != nil {
		if err := o.outboundRefs.RenameExternalOutboundTag(ctx, oldTag, newTag); err != nil {
			return err
		}
		refsRenamed = true
	}
	if err := o.ApplyConfig(ctx, cfg); err != nil {
		if refsRenamed {
			_ = o.outboundRefs.RenameExternalOutboundTag(context.Background(), newTag, oldTag)
		}
		return err
	}

	if o.isNDMSProxyEnabled() && renamed.ProxyInterface != "" {
		if idx, err := parseProxyIdx(renamed.ProxyInterface); err == nil && idx >= 0 {
			if err := o.proxyMgr.EnsureProxy(ctx, idx, renamed.ListenPort, newTag); err != nil {
				o.log.Warn("rename proxy description failed", "old", oldTag, "new", newTag, "err", err)
			}
		}
	}
	if o.bus != nil {
		o.bus.Publish("singbox:tunnels-changed", nil)
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-rename", oldTag, "done new="+newTag)
	}
	return nil
}

func (o *Operator) loadConfig() (*Config, error) {
	return LoadConfig(o.tunnelsFile())
}

// HasUserTunnels reports whether 10-tunnels.json defines at least one
// user-managed sing-box tunnel. Wired into orchestrator.SlotTunnels
// HasContent so an empty tunnels file does not, by itself, keep the
// daemon running.
func (o *Operator) HasUserTunnels() bool {
	cfg, err := o.loadConfig()
	if err != nil {
		return false
	}
	return len(cfg.Tunnels()) > 0
}

// ApplyConfig — рантайм-запись 10-tunnels.json с полной проверкой. Гейт по
// build-фичам бинаря идёт до неё: sing-box на неподдерживаемом протоколе
// говорит лишь «unknown outbound type», не называя недостающий тег сборки.
// Прежде этот гейт давал preflightConfigDir, снятый вместе с ручным
// протоколом; здесь он честнее — смотрит в КАНДИДАТА, а не в каталог.
func (o *Operator) ApplyConfig(ctx context.Context, cfg *Config) error {
	if err := o.checkTunnelFeatures(cfg); err != nil {
		return err
	}
	return o.writeTunnelsSlot(cfg, true)
}

// checkTunnelFeatures — тот же гейт, что в AddTunnels, но по готовому
// конфигу: он покрывает и UpdateTunnel/RenameTunnel, у которых своего нет.
func (o *Operator) checkTunnelFeatures(cfg *Config) error {
	// Пустой список = проба не удалась (бинарь ещё не установлен), а не
	// «фич нет»: резать конфиг по такой догадке нельзя.
	features := o.singboxFeaturesCached()
	if len(features) == 0 {
		return nil
	}
	for _, t := range cfg.Tunnels() {
		if t.Protocol == "" || OutboundSupportedByFeatures(features, t.Protocol) {
			continue
		}
		required := OutboundTypeRequiresFeature(t.Protocol)
		if required == "" {
			required = "<unknown required tag>"
		}
		return fmt.Errorf("туннель %q: протокол %q не поддержан установленным sing-box (нужен тег сборки %s)", t.Tag, t.Protocol, required)
	}
	return nil
}

// writeTunnelsSlot — единственный рантайм-писатель слота. Оркестратор
// валидирует merged-конфиг с этими байтами, НЕ трогая активный файл, пишет
// атомарно и взводит debounced reload.
//
// validate=false остаётся ровно для одного случая — опустевшего слота, см.
// RemoveTunnel. Прежде рядом жил приватный applyConfig со своим протоколом
// (rename в .bak, preflight по каталогу, restore на провале): он существовал
// ради синхронной валидации, которой не даёт orch.Save, а SaveAndValidate
// даёт её же без окна, когда битый файл уже лежит на диске.
//
// Оркестратор обязателен: в проде SetOrch вызывается до старта HTTP, тесты
// поднимают его сами (см. newOrchedOperator).
func (o *Operator) writeTunnelsSlot(cfg *Config, validate bool) error {
	if o.orch == nil {
		return fmt.Errorf("apply tunnels config: orchestrator not wired")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tunnels config: %w", err)
	}
	if !validate {
		return o.orch.Save(orchestrator.SlotTunnels, data)
	}
	res, err := o.orch.SaveAndValidate(orchestrator.SlotTunnels, data)
	if err != nil {
		return err
	}
	if !res.Ok() {
		return fmt.Errorf("validate: %s", res.Error())
	}
	return nil
}

// LoadCurrentConfig reads the on-disk config.json that sing-box is
// running from. Returns a fresh NewConfig() if the file is missing
// (first ever apply / tunnels never configured).
func (o *Operator) LoadCurrentConfig() (*Config, error) {
	cfg, err := o.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return NewConfig(), nil
		}
		return nil, err
	}
	return cfg, nil
}

// SetSelectorDefault switches a selector's active member live via
// Clash API. Returns ErrSingboxNotRunning if the daemon is not alive —
// callers decide whether to treat that as fatal.
func (o *Operator) SetSelectorDefault(ctx context.Context, selectorTag, memberTag string) error {
	if running, _ := o.proc.IsRunning(); !running {
		return ErrSingboxNotRunning
	}
	return o.clash.SetSelector(selectorTag, memberTag)
}

// GetSelectorActive returns the currently-active member of a
// selector. Returns ErrSingboxNotRunning if the daemon is down, so
// callers can cheaply distinguish "no live state" from transport
// errors.
func (o *Operator) GetSelectorActive(ctx context.Context, selectorTag string) (string, error) {
	if running, _ := o.proc.IsRunning(); !running {
		return "", ErrSingboxNotRunning
	}
	return o.clash.SelectorActive(selectorTag)
}

func (o *Operator) loadOrInitConfig() (*Config, error) {
	cfg, err := LoadConfig(o.tunnelsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return NewConfig(), nil
		}
		return nil, err
	}
	return cfg, nil
}
