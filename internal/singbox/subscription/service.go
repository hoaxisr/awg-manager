package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

// ConfigMutator is the narrow contract for committing subscription state to
// sing-box config. The real implementation lives in singbox.Operator.
type ConfigMutator interface {
	AllocListenPort() (uint16, error)
	AllocProxyIndex(ctx context.Context) (int, error)
	AddOutbound(tag string, jsonBody []byte) error
	UpdateOutbound(tag string, jsonBody []byte) error
	RemoveOutbound(tag string) error
	AddInbound(tag string, jsonBody []byte) error
	RemoveInbound(tag string) error
	AddRouteRule(jsonBody []byte) error
	RemoveRouteRule(inboundTag, outboundTag string) error
	EnsureProxy(ctx context.Context, idx, port int, description string) error
	RemoveProxy(ctx context.Context, idx int) error
	// Reload commits the batch of mutations since the last commit with a
	// single validate+save+reload (#331). Mutations (Add*/Remove*/Update*)
	// only accumulate in memory; nothing reaches sing-box until Reload.
	Reload(ctx context.Context) error
	// Rollback discards an uncommitted batch (mutations since the last
	// commit), restoring the last committed config. Used on a failed Create
	// so a partial materialisation can't linger in the in-memory slot.
	Rollback()
	// SelectClashProxy hits the running sing-box Clash API to switch the
	// selector's active member at runtime, without rewriting config or
	// triggering a reload. The config slot's stored selector.default
	// should be updated separately for restart persistence.
	SelectClashProxy(selectorTag, memberTag string) error
	// GetClashSelectorActive reads the currently-active member of a
	// selector/urltest outbound from the running sing-box Clash API.
	// Returns ("", nil) when Clash is unreachable — callers treat this
	// as "no live data" rather than as an error.
	GetClashSelectorActive(selectorTag string) (string, error)
	// DeclaredOutboundTags returns the outbound tags currently present in
	// the committed subscriptions slot (after the last flush). The Service
	// reconciles stored MemberTags against this so servers that flush
	// dropped (sing-box rejected) don't linger and get re-introduced as
	// dangling group members on later rebuilds. Empty slice = "can't
	// report" (caller must treat as no-op, not as "drop everything").
	DeclaredOutboundTags() []string
}

// Service is the subscription business-logic facade.
type Service struct {
	store   *Store
	mutator ConfigMutator
	muById  sync.Map // map[string]*sync.Mutex
	// createMu serializes Create across all subscriptions. Allocation
	// (AllocListenPort / AllocProxyIndex) scans the slot without reserving,
	// so two concurrent Creates would hand out the same listen_port / ProxyN
	// — the documented "subscriptions are created one at a time" invariant
	// (issue #287). This lock enforces it.
	createMu  sync.Mutex
	fetchOpts FetchOpts
	log       *logging.ScopedLogger // nil-safe; sing-box journal (runtime)
	appLog    *logging.ScopedLogger // nil-safe; app journal (subscription partition)
	// ndmsProxyEnabled mirrors Settings.CreateNDMSProxyForSingbox. When the
	// closure is nil the service behaves as if enabled (back-compat for
	// tests / legacy bootstrap), mirroring OperatorDeps.IsNDMSProxyEnabled.
	ndmsProxyEnabled func() bool
}

func NewService(store *Store, mutator ConfigMutator) *Service {
	return &Service{store: store, mutator: mutator}
}

// SetNDMSProxyEnabled wires the global "Create NDMS Proxy for sing-box"
// toggle. When off, Create/Update do not register a ProxyN interface for
// the subscription (ProxyIndex stays -1); SyncProxies (re-)creates them
// when the toggle is turned back on.
func (s *Service) SetNDMSProxyEnabled(fn func() bool) { s.ndmsProxyEnabled = fn }

func (s *Service) proxyEnabled() bool {
	if s.ndmsProxyEnabled == nil {
		return true
	}
	return s.ndmsProxyEnabled()
}

// SyncProxies ensures every subscription has its NDMS ProxyN interface when
// the global toggle is on. It is the toggle-ON counterpart to the gated
// Create: subscriptions created while the toggle was off carry ProxyIndex=-1
// and get a freshly allocated proxy here; subscriptions that already had one
// (index retained across a toggle-off) are re-registered idempotently.
//
// Allocation is serialized (createMu) against Create. AllocProxyIndex scans
// the live router without a reserved set, so two passes are required: first
// re-register every subscription that already holds an index (occupying its
// router slot), then allocate fresh indices for the proxy-less ones. Without
// pass 1 a fresh allocation could land on a slot a retained subscription still
// owns in the store but hasn't re-registered yet — store.List() order is
// non-deterministic, so the proxy-less subscription is not reliably processed
// last. Within pass 2 each proxy is committed via EnsureProxy before the next
// index is allocated, for the same reason.
func (s *Service) SyncProxies(ctx context.Context) error {
	if !s.proxyEnabled() {
		return nil
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()

	subs := s.store.List()
	// Pass 1: re-register retained indices so their router slots are occupied
	// before pass 2 allocates.
	for _, sub := range subs {
		if sub.ListenPort == 0 || sub.ProxyIndex < 0 {
			continue
		}
		mu := s.lockSub(sub.ID)
		err := func() error {
			mu.Lock()
			defer mu.Unlock()
			return s.mutator.EnsureProxy(ctx, sub.ProxyIndex, int(sub.ListenPort), sub.Label)
		}()
		if err != nil {
			return fmt.Errorf("subscription %s: ensure proxy: %w", sub.ID, err)
		}
	}
	// Pass 2: allocate a fresh ProxyN for subscriptions created while the
	// toggle was off (ProxyIndex=-1). The live-router scan now sees the pass-1
	// slots as taken, so no collision with a retained index.
	for _, sub := range subs {
		if sub.ListenPort == 0 || sub.ProxyIndex >= 0 {
			continue
		}
		mu := s.lockSub(sub.ID)
		err := func() error {
			mu.Lock()
			defer mu.Unlock()
			idx, err := s.mutator.AllocProxyIndex(ctx)
			if err != nil {
				return fmt.Errorf("alloc proxy index: %w", err)
			}
			if err := s.mutator.EnsureProxy(ctx, idx, int(sub.ListenPort), sub.Label); err != nil {
				return fmt.Errorf("ensure proxy: %w", err)
			}
			if err := s.store.SetProxyIndex(sub.ID, idx); err != nil {
				return fmt.Errorf("persist proxy index: %w", err)
			}
			return nil
		}()
		if err != nil {
			return fmt.Errorf("subscription %s: %w", sub.ID, err)
		}
	}
	return nil
}

// SetAppLogger wires UI-visible logging for events outside of the
// success/error envelope returned to the caller — currently used for
// "URL rewritten from web-view to raw" notices that would otherwise
// be invisible to the user.
func (s *Service) SetAppLogger(app logging.AppLogger) {
	s.log = logging.NewScopedLogger(app, logging.GroupSingbox, logging.SubSBRuntime)
	s.appLog = logging.NewScopedLogger(app, logging.GroupRouting, logging.SubSubscription)
}

func (s *Service) logInfo(action, target, msg string) {
	if s.log != nil {
		s.log.Info(action, target, msg)
	}
}

func (s *Service) logWarn(action, target, msg string) {
	if s.log != nil {
		s.log.Warn(action, target, msg)
	}
}

func (s *Service) logPartitionResult(subID string, parts partitionResult) {
	if s.appLog == nil {
		return
	}
	for _, it := range parts.Info {
		s.appLog.Debug("subscription-info", subID,
			fmt.Sprintf("provider banner → info: %q (id=%s)", it.Label, it.ID))
	}
	for _, r := range parts.Rejected {
		if strings.Contains(r.Reason, "info slot full") {
			s.appLog.Debug("subscription-info-full", subID,
				fmt.Sprintf("banner overflow → rejected: %q (%s)", r.Label, r.Reason))
		}
	}
}

func (s *Service) lockSub(id string) *sync.Mutex {
	if v, ok := s.muById.Load(id); ok {
		return v.(*sync.Mutex)
	}
	m := &sync.Mutex{}
	actual, _ := s.muById.LoadOrStore(id, m)
	return actual.(*sync.Mutex)
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Subscription, error) {
	source := "url"
	if in.Inline != "" {
		source = "inline"
	}
	s.logInfo("subscription-create", in.Label, fmt.Sprintf("start source=%s refresh_hours=%d enabled=%v", source, in.RefreshHours, in.Enabled))
	switch {
	case in.URL == "" && in.Inline == "":
		return nil, errors.New("subscription: either URL or inline content is required")
	case in.URL != "" && in.Inline != "":
		return nil, errors.New("subscription: URL and inline content are mutually exclusive")
	}
	// Serialize the whole Create: allocation scans-without-reserve, so two
	// concurrent Creates must not interleave between allocate and commit
	// (issue #287). Per-subscription locks below don't help — each Create has
	// a fresh random ID, hence a distinct lock.
	s.createMu.Lock()
	defer s.createMu.Unlock()

	sub, err := s.store.Create(in)
	if err != nil {
		s.logWarn("subscription-create", in.Label, "failed to create store row: "+err.Error())
		return nil, err
	}
	mu := s.lockSub(sub.ID)
	mu.Lock()
	defer mu.Unlock()

	port, err := s.mutator.AllocListenPort()
	if err != nil {
		s.store.Delete(sub.ID)
		s.logWarn("subscription-create", sub.ID, "failed to allocate listen port: "+err.Error())
		return nil, fmt.Errorf("subscription: alloc listen port: %w", err)
	}
	if err := s.store.SetListenPort(sub.ID, port); err != nil {
		s.store.Delete(sub.ID)
		return nil, err
	}

	// Исключение по ключу из превью импорта: ключи — identity-суффиксы, не
	// зависящие от subID. Здесь subID уже выделен → достраиваем полный
	// стабильный тег и пишем в ExcludedTags ДО первичного refreshLocked,
	// чтобы исключённые серверы вообще не материализовались при создании.
	if len(in.ExcludedKeys) > 0 {
		subShort := sub.ID
		if len(subShort) > 8 {
			subShort = subShort[:8]
		}
		tags := make([]string, 0, len(in.ExcludedKeys))
		for _, k := range in.ExcludedKeys {
			tags = append(tags, "sub-"+subShort+"-"+k)
		}
		if err := s.store.SetExcludedTags(sub.ID, tags, nil); err != nil {
			s.store.Delete(sub.ID)
			return nil, err
		}
	}

	// NDMS Proxy is only created when the global toggle is on. When off, the
	// subscription stays proxy-less (ProxyIndex=-1) and routes via its mixed
	// inbound + selector through the internal sing-box router; SyncProxies
	// allocates the ProxyN later if the toggle is turned back on.
	proxyIdx := -1
	if s.proxyEnabled() {
		idx, err := s.mutator.AllocProxyIndex(ctx)
		if err != nil {
			s.store.Delete(sub.ID)
			s.logWarn("subscription-create", sub.ID, "failed to allocate proxy index: "+err.Error())
			return nil, fmt.Errorf("subscription: alloc proxy index: %w", err)
		}
		if err := s.store.SetProxyIndex(sub.ID, idx); err != nil {
			s.store.Delete(sub.ID)
			return nil, err
		}
		proxyIdx = idx
		if err := s.mutator.EnsureProxy(ctx, idx, int(port), sub.Label); err != nil {
			// Best-effort cleanup: EnsureProxy may have partially registered
			// the interface before failing. RemoveProxy is idempotent.
			_ = s.mutator.RemoveProxy(ctx, idx)
			s.store.Delete(sub.ID)
			s.logWarn("subscription-create", sub.ID, "failed to ensure NDMS proxy: "+err.Error())
			return nil, fmt.Errorf("subscription: register NDMS proxy: %w", err)
		}
	}

	if _, err := s.refreshLocked(ctx, sub.ID); err != nil {
		// refreshLocked accumulates member outbounds / selector / inbound /
		// route into the in-memory slot but commits only at Reload (#331).
		// On a mid-failure nothing was flushed to 40-subscriptions.json, so
		// discard the uncommitted batch — otherwise the partial would linger
		// in memory and get committed by the next operation (issue #287).
		s.mutator.Rollback()
		// EnsureProxy succeeded above — the NDMS Proxy interface is now
		// live in the router. We must roll it back before dropping the
		// storage row; otherwise every failed Create leaks a ProxyN that
		// only the startup cleanup sweep would eventually reap. Swallow
		// the RemoveProxy error: the storage row is going away regardless,
		// and a stranded ProxyN is recoverable via Settings → cleanup.
		if proxyIdx >= 0 {
			_ = s.mutator.RemoveProxy(ctx, proxyIdx)
		}
		s.store.Delete(sub.ID)
		s.logWarn("subscription-create", sub.ID, "initial refresh failed: "+err.Error())
		return nil, fmt.Errorf("subscription: initial fetch failed: %w", err)
	}

	final, _ := s.store.Get(sub.ID)
	s.logInfo("subscription-create", sub.ID, fmt.Sprintf("created mode=%s members=%d listen_port=%d proxy_index=%d", final.EffectiveMode(), len(final.MemberTags), final.ListenPort, final.ProxyIndex))
	return final, nil
}

func (s *Service) Refresh(ctx context.Context, id string) (*RefreshResult, error) {
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()
	return s.refreshLocked(ctx, id)
}

func (s *Service) refreshLocked(ctx context.Context, id string) (*RefreshResult, error) {
	s.logInfo("subscription-refresh", id, "start")
	sub, err := s.store.Get(id)
	if err != nil {
		s.logWarn("subscription-refresh", id, "load failed: "+err.Error())
		return nil, err
	}
	var body []byte
	var ct string
	if sub.IsInline() {
		// Inline subscription: paste content is the body. No HTTP, no
		// MaskURL on errors (there's no URL to mask). The same
		// downstream parser (Clash YAML / sing-box JSON / share-links)
		// handles whatever the user pasted.
		//
		// After the initial Create-time parse populates MemberTags,
		// the source of truth for an inline subscription becomes the
		// stored Members slice — manual Add/Remove member CRUD
		// directly mutates that slice, and re-parsing sub.Inline would
		// clobber those edits (Inline is preserved as the original
		// seed only). So skip re-parsing on subsequent Refresh calls.
		if len(sub.MemberTags) > 0 {
			res := &RefreshResult{When: time.Now()}
			if err := s.store.UpdateState(id, *res); err != nil {
				return nil, err
			}
			return res, nil
		}
		body = []byte(sub.Inline)
		ct = "text/plain; charset=utf-8"
	} else {
		// Rewrite well-known git-hosting web-view URLs (github blob /
		// gitlab /-/blob/ / gitea src/branch/) to the raw-content URL.
		// Skipping this leg downloads an HTML page whose embedded React
		// payload JSON-escapes the share-links beyond what extractFromHTML
		// can safely recover — at best a hot path of recovery, at worst
		// silently garbled outbounds (see PR adding RewriteForRaw for the
		// HardVPN-bypass-WhiteLists/good_keys.txt regression).
		fetchURL, rewrote := RewriteForRaw(sub.URL)
		if rewrote {
			s.logWarn("subscription-refresh", id,
				"rewrote web-view URL to raw URL")
		}
		fetched, fetchedCT, fetchErr := FetchWithContext(ctx, fetchURL, sub.Headers, s.fetchOpts)
		if fetchErr != nil {
			masked := fmt.Errorf("%s", MaskURL(fetchErr.Error(), sub.URL))
			s.store.UpdateState(id, RefreshResult{When: time.Now(), Err: masked})
			s.logWarn("subscription-refresh", id, "fetch failed: "+masked.Error())
			return nil, masked
		}
		body = fetched
		ct = fetchedCT
	}
	isClash := vlink.IsClashYAML(body)
	isSbJSON := !isClash && vlink.IsSingboxJSON(body)
	// Body that's valid JSON but not a recognised sing-box subscription
	// (no outbounds key in the right place) gets a precise error rather
	// than a fall-through into share-link parsing — otherwise the user
	// sees "ни одной валидной ссылки" with a meaningless prefix from
	// scanning JSON bytes for "://".
	if !isClash && !isSbJSON && vlink.LooksLikeJSON(body) {
		err := errors.New("subscription: тело подписки выглядит как JSON, но не похоже на sing-box config (нет outbounds). Поддерживаются: sing-box JSON config (одиночный, массив конфигов, или массив outbounds), Clash / mihomo YAML, base64 share-links, plain text vless://, trojan://, ss://, hysteria2://, mieru://, mierus://.")
		s.store.UpdateState(id, RefreshResult{When: time.Now(), Err: err})
		s.logWarn("subscription-refresh", id, err.Error())
		return nil, err
	}
	var parseRes vlink.BatchResult
	switch {
	case isClash:
		parseRes = vlink.ParseClashBody(body)
	case isSbJSON:
		parseRes = vlink.ParseSingboxBody(body)
	default:
		lines := NormalizeBody(body, ct)
		parseRes = vlink.ParseBatch(lines)
	}

	parts := partitionParsedOutbounds(id, parseRes.Outbounds)
	s.logPartitionResult(id, parts)
	if len(parts.Valid) == 0 {
		emptyClean := len(parseRes.Errors) == 0 && parseRes.SkippedVmess == 0 && parseRes.SkippedUnsupp == 0 &&
			len(parts.Info) == 0 && len(parts.Rejected) == 0
		var errMsg string
		switch {
		case isClash && emptyClean:
			errMsg = "subscription: подписка пуста (proxies: []). Возможно, истекла или ещё не активирована — проверь на стороне провайдера."
		case isSbJSON && emptyClean:
			errMsg = "subscription: подписка пуста (outbounds: []). Возможно, истекла или ещё не активирована — проверь на стороне провайдера."
		case len(parseRes.Errors) > 0:
			hint := "ни одной валидной ссылки. Поддерживаются: base64-encoded share-links, HTML с share-link якорями, plain text со ссылками vless://, trojan://, ss://, hysteria2://, mieru://, mierus://, Clash YAML / mihomo, а также sing-box JSON (одиночный, массив конфигов, или массив outbounds; типы vless, trojan, ss, hysteria2, mieru). Записи vmess пропускаются."
			errMsg = fmt.Sprintf("subscription: %s Первая ошибка парсера: %s", hint, parseRes.Errors[0].Error())
		default:
			hint := "ни одной валидной ссылки. Поддерживаются: base64-encoded share-links, HTML с share-link якорями, plain text со ссылками vless://, trojan://, ss://, hysteria2://, mieru://, mierus://, Clash YAML / mihomo, а также sing-box JSON (одиночный, массив конфигов, или массив outbounds; типы vless, trojan, ss, hysteria2, mieru). Записи vmess пропускаются."
			if len(parts.Info) > 0 {
				hint += fmt.Sprintf(" (инфо-строк провайдера: %d — не являются серверами)", len(parts.Info))
			}
			errMsg = fmt.Sprintf("subscription: %s", hint)
		}
		err := errors.New(errMsg)
		s.store.UpdateState(id, RefreshResult{When: time.Now(), Err: err})
		s.logWarn("subscription-refresh", id, err.Error())
		return nil, err
	}

	diff := ApplyDiff(id, sub.MemberTags, parts.Valid)

	if err := s.applyDiff(ctx, sub, diff); err != nil {
		s.store.UpdateState(id, RefreshResult{When: time.Now(), Err: err})
		s.logWarn("subscription-refresh", id, "apply failed: "+err.Error())
		return nil, err
	}

	excluded := make(map[string]bool, len(sub.ExcludedTags))
	for _, t := range sub.ExcludedTags {
		excluded[t] = true
	}
	newMembers := make([]MemberInfo, 0, len(diff.New)+len(diff.Existing))
	excludedMembers := make([]MemberInfo, 0, len(sub.ExcludedTags))
	for _, n := range diff.New {
		mi := toMemberInfo(n.Tag, n.Out)
		if excluded[n.Tag] {
			excludedMembers = append(excludedMembers, mi)
		} else {
			newMembers = append(newMembers, mi)
		}
	}
	for _, e := range diff.Existing {
		mi := toMemberInfo(e.Tag, e.Out)
		if excluded[e.Tag] {
			excludedMembers = append(excludedMembers, mi)
		} else {
			newMembers = append(newMembers, mi)
		}
	}
	// Reconcile against what actually materialized: flush() may have dropped
	// servers sing-box rejected. Keeping them in MemberTags would re-create
	// dangling group members on the next rebuild (cross-slot unknown-outbound).
	declared := make(map[string]bool)
	for _, t := range s.mutator.DeclaredOutboundTags() {
		declared[t] = true
	}
	newMembers, prunedTags := filterDeclaredMembers(newMembers, declared)
	rejected := appendRejectedUnique(parts.Rejected, rejectedFromPrunedTags(sub, prunedTags)...)
	info := mergeInfoItems(sub.InfoItems, filterDismissedInfo(parts.Info, sub.DismissedInfoIDs))
	if len(prunedTags) > 0 {
		s.logWarn("subscription-refresh", id, fmt.Sprintf("pruned %d member(s) not materialized (dropped by validation): %s", len(prunedTags), strings.Join(prunedTags, ", ")))
	}
	if err := s.store.SetMembersExtras(id, newMembers, diff.Orphan, rejected, info, excludedMembers); err != nil {
		return nil, err
	}

	res := &RefreshResult{
		When:             time.Now(),
		Added:            len(diff.New),
		Updated:          len(diff.Existing),
		Orphaned:         len(diff.Orphan),
		SkippedVmess:     parseRes.SkippedVmess,
		SkippedOther:     parseRes.SkippedUnsupp,
		SkippedDuplicate: diff.SkippedDuplicate,
	}
	for _, e := range parseRes.Errors {
		res.ParseErrors = append(res.ParseErrors, e.Error())
	}
	if err := s.store.UpdateState(id, *res); err != nil {
		s.logWarn("subscription-refresh", id, "failed to update refresh state: "+err.Error())
		return nil, err
	}
	s.logInfo("subscription-refresh", id, fmt.Sprintf("done added=%d updated=%d orphaned=%d skipped_dup=%d skipped_vmess=%d skipped_other=%d parse_errors=%d", res.Added, res.Updated, res.Orphaned, res.SkippedDuplicate, res.SkippedVmess, res.SkippedOther, len(res.ParseErrors)))
	return res, nil
}

// filterDeclaredMembers keeps only members whose outbound tag is actually
// declared in the emitted slot, returning the kept members and the dropped
// tags. flush() may silently drop servers sing-box rejects; without this the
// stored MemberTags would keep referencing them and later group rebuilds
// (SetActiveMember / add / replace / re-enable) would re-introduce dangling
// members — the cross-slot unknown-outbound failure. When declared is empty
// (mutator can't report) members pass through unchanged: never nuke
// everything on missing data.
func filterDeclaredMembers(members []MemberInfo, declared map[string]bool) (kept []MemberInfo, dropped []string) {
	if len(declared) == 0 {
		return members, nil
	}
	for _, m := range members {
		if declared[m.Tag] {
			kept = append(kept, m)
		} else {
			dropped = append(dropped, m.Tag)
		}
	}
	return kept, dropped
}

// applyDiff commits the diff to sing-box config. Selector + mixed inbound +
// route rule are recreated each refresh (they may not exist yet on first
// run). Per-member outbounds are added/updated/left alone — orphans are NOT
// removed (the UI offers explicit deletion).
func (s *Service) applyDiff(ctx context.Context, sub *Subscription, diff DiffResult) error {
	excluded := make(map[string]bool, len(sub.ExcludedTags))
	for _, t := range sub.ExcludedTags {
		excluded[t] = true
	}
	for _, n := range diff.New {
		if excluded[n.Tag] {
			continue // исключённый сервер не материализуем
		}
		jsonWithTag := replaceTag(n.Out.Outbound, n.Tag)
		if err := s.mutator.AddOutbound(n.Tag, jsonWithTag); err != nil {
			return err
		}
	}
	for _, e := range diff.Existing {
		if excluded[e.Tag] {
			s.mutator.RemoveOutbound(e.Tag) // на случай, если ранее был активен
			continue
		}
		jsonWithTag := replaceTag(e.Out.Outbound, e.Tag)
		if err := s.mutator.UpdateOutbound(e.Tag, jsonWithTag); err != nil {
			return err
		}
	}

	memberTags := make([]string, 0, len(diff.New)+len(diff.Existing))
	for _, n := range diff.New {
		if !excluded[n.Tag] {
			memberTags = append(memberTags, n.Tag)
		}
	}
	for _, e := range diff.Existing {
		if !excluded[e.Tag] {
			memberTags = append(memberTags, e.Tag)
		}
	}

	// Selector / urltest — remove old (idempotent) then add fresh.
	// BuildGroupOutbound dispatches by sub.Mode.
	s.mutator.RemoveOutbound(sub.SelectorTag)
	if err := s.mutator.AddOutbound(sub.SelectorTag, BuildGroupOutbound(*sub, memberTags, "")); err != nil {
		return err
	}

	// Mixed inbound — add only if not present (first time).
	if sub.ListenPort != 0 {
		s.mutator.AddInbound(sub.InboundTag, BuildMixedInbound(sub.InboundTag, sub.ListenPort))
		s.mutator.AddRouteRule(BuildRouteRule(sub.InboundTag, sub.SelectorTag))
	}

	return s.mutator.Reload(ctx)
}

// Delete tears down a subscription unconditionally: the NDMS ProxyN, mixed
// inbound, selector outbound, route rule, and per-member outbounds are all
// removed. ConfigMutator errors are non-blocking — sing-box config may have
// drifted; the subscription row still gets removed from storage so the user
// is not stuck with an undeletable entry.
func (s *Service) Delete(ctx context.Context, id string) error {
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()
	return s.deleteLocked(ctx, id)
}

// deleteLocked is the lock-free body of Delete. Callers MUST already hold
// the per-subscription mutex. Used by RemoveMember to drop a subscription
// when its last member is taken out (caller already holds the lock).
func (s *Service) deleteLocked(ctx context.Context, id string) error {
	sub, err := s.store.Get(id)
	if err != nil {
		return err
	}

	s.mutator.RemoveRouteRule(sub.InboundTag, sub.SelectorTag)
	s.mutator.RemoveInbound(sub.InboundTag)
	s.mutator.RemoveOutbound(sub.SelectorTag)
	for _, m := range sub.MemberTags {
		s.mutator.RemoveOutbound(m)
	}
	if sub.ProxyIndex >= 0 {
		if err := s.mutator.RemoveProxy(ctx, sub.ProxyIndex); err != nil {
			s.store.UpdateState(id, RefreshResult{When: time.Now(), Err: err})
		}
	}
	if err := s.mutator.Reload(ctx); err != nil {
		return fmt.Errorf("subscription: delete reload: %w", err)
	}
	return s.store.Delete(id)
}

// replaceTag patches the "tag" field of a JSON-encoded outbound to match the
// stable tag we're committing under.
func replaceTag(raw []byte, tag string) []byte {
	var ob map[string]any
	_ = json.Unmarshal(raw, &ob)
	ob["tag"] = tag
	out, _ := json.Marshal(ob)
	return out
}

// toMemberInfo extracts user-facing metadata from a parsed outbound so the
// UI can render protocol, server:port, transport, and security badges without
// re-parsing the raw JSON on every render.
func toMemberInfo(tag string, p vlink.ParsedOutbound) MemberInfo {
	mi := MemberInfo{
		Tag:      tag,
		Label:    p.Label,
		Protocol: p.Protocol,
		Server:   p.Server,
		Port:     p.Port,
	}
	var ob map[string]any
	if json.Unmarshal(p.Outbound, &ob) != nil {
		return mi
	}
	if tr, ok := ob["transport"].(map[string]any); ok {
		if t, ok := tr["type"].(string); ok {
			mi.Transport = t
		}
	}
	if tls, ok := ob["tls"].(map[string]any); ok {
		if serverName, ok := tls["server_name"].(string); ok {
			mi.SNI = strings.TrimSpace(serverName)
		}
		if _, hasReality := tls["reality"]; hasReality {
			mi.Security = "reality"
		} else if enabled, _ := tls["enabled"].(bool); enabled {
			mi.Security = "tls"
		}
	}
	return mi
}

// ListActiveMemberTags returns the active member tag of every enabled
// subscription whose ActiveMember is set. Used by DelayChecker so the
// active outbound of each subscription gets the same periodic latency
// probe as regular sing-box tunnels.
func (s *Service) ListActiveMemberTags() []string {
	subs := s.store.List()
	out := make([]string, 0, len(subs))
	for _, sub := range subs {
		if !sub.Enabled || sub.ActiveMember == "" {
			continue
		}
		out = append(out, sub.ActiveMember)
	}
	return out
}

// === Helpers used by REST handlers (B-Task 5) ===

func (s *Service) List() []Subscription                 { return s.store.List() }
func (s *Service) Get(id string) (*Subscription, error) { return s.store.Get(id) }
func (s *Service) Update(id string, patch UpdatePatch) (*Subscription, error) {
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()
	// Source-type guard: URL-backed and inline subscriptions stay on
	// their original source for life. Reject patches that would clear
	// a URL (would make a URL-backed sub source-less) or that would
	// add a URL to an inline sub (would dual-source it). Inline body
	// is not in UpdatePatch at all, so the reverse direction is
	// unreachable from API.
	if patch.URL != nil {
		newURL := *patch.URL
		current, err := s.store.Get(id)
		if err != nil {
			return nil, err
		}
		if current.IsInline() {
			return nil, errors.New("subscription: cannot add URL to an inline subscription")
		}
		if newURL == "" {
			return nil, errors.New("subscription: cannot clear URL after creation")
		}
	}
	sub, err := s.store.Update(id, patch)
	if err != nil {
		return nil, err
	}
	// Mode / urltest config changes require a fresh group outbound in
	// sing-box config and a SIGHUP so the new wrapper takes effect.
	// URL / headers / refresh-cadence / enabled only mutate metadata.
	// Label changes mutate store AND must propagate to NDMS Proxy
	// description so the rename is visible in the router UI.
	if patch.Mode != nil || patch.URLTest != nil {
		s.mutator.RemoveOutbound(sub.SelectorTag)
		if err := s.mutator.AddOutbound(sub.SelectorTag, BuildGroupOutbound(*sub, sub.MemberTags, sub.ActiveMember)); err != nil {
			return sub, fmt.Errorf("rebuild group outbound: %w", err)
		}
		if err := s.mutator.Reload(context.Background()); err != nil {
			return sub, fmt.Errorf("reload after mode change: %w", err)
		}
	}
	if patch.Label != nil && s.proxyEnabled() && sub.ProxyIndex >= 0 {
		// EnsureProxy is idempotent — re-running with new description updates
		// NDMS Proxy.description in place. Best-effort: on failure the store
		// already has the new label, the proxy description stays stale until
		// next refresh; we surface the error so the UI can show a warning.
		// Skipped when the toggle is off or the subscription has no ProxyN
		// (created while off): there is no interface to relabel.
		if err := s.mutator.EnsureProxy(context.Background(), sub.ProxyIndex, int(sub.ListenPort), sub.Label); err != nil {
			return sub, fmt.Errorf("sync proxy description: %w", err)
		}
	}
	return sub, nil
}

// ErrActiveMemberOnURLTest is returned by SetActiveMember when the
// caller tries to pin a member on a urltest-mode subscription. Sing-box's
// Clash-compat API only exposes member-selection on `selector` outbounds —
// urltest groups are auto-managed and reject the switch with a runtime
// error. Callers (HTTP handlers) should map this to 409 Conflict so the
// UI can hide the picker rather than spam errors.
var ErrActiveMemberOnURLTest = errors.New("subscription: SetActiveMember not supported in urltest mode")

// SetActiveMember updates the selector's "default" pointer to memberTag.
// It updates the config slot for restart persistence and persists the active
// member in the store, then hits the Clash API for an instant runtime switch
// — no SIGHUP, no connection drop.
func (s *Service) SetActiveMember(ctx context.Context, id, memberTag string) error {
	s.logInfo("subscription-active-member", id, "set requested: "+memberTag)
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()

	sub, err := s.store.Get(id)
	if err != nil {
		return err
	}
	if sub.EffectiveMode() == ModeURLTest {
		return ErrActiveMemberOnURLTest
	}
	found := false
	for _, m := range sub.MemberTags {
		if m == memberTag {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("member %q not in subscription", memberTag)
	}

	// Persist the choice in the store — the source of truth for the active
	// member. The config slot is deliberately NOT rewritten: selector.default
	// is rebuilt as first-member on every refresh, so persisting it here buys
	// nothing, and a slot write without Reload would only leave an uncommitted
	// batch open in the adapter.
	if err := s.store.SetActiveMember(id, memberTag); err != nil {
		return err
	}

	// Switch the live selector via the Clash API — instant, no SIGHUP, no
	// connection drop. If clash is unreachable (sing-box not running) we return
	// the error, but the store already holds the new active member.
	if err := s.mutator.SelectClashProxy(sub.SelectorTag, memberTag); err != nil {
		s.logWarn("subscription-active-member", id, "clash switch failed: "+err.Error())
		return fmt.Errorf("subscription: clash select: %w", err)
	}

	s.logInfo("subscription-active-member", id, "set to "+memberTag)
	return nil
}

// ErrManualMemberOnURLSub is returned by AddManualMember / RemoveMember
// when called on a URL-backed (non-inline) subscription. Manual member
// CRUD is currently scoped to inline subscriptions because the URL diff
// pipeline owns the truth of which members exist; mixing manual entries
// in would race the next refresh.
var ErrManualMemberOnURLSub = errors.New("subscription: member CRUD is only allowed on inline subscriptions")

// ErrExcludeOnInline is returned by ExcludeMembers when called on an inline
// subscription. Exclusion survives refresh by keeping the member in the URL
// diff truth but skipping materialization — inline subs have no refresh truth,
// so inline removal uses RemoveMember (destructive) instead.
var ErrExcludeOnInline = errors.New("subscription: exclude is only allowed on URL subscriptions")

// ErrAllMembersExcluded is returned by ExcludeMembers when excluding the given
// tags would leave the subscription with no active members. At least one member
// must remain so the selector has something to route to.
var ErrAllMembersExcluded = errors.New("subscription: cannot exclude all members; at least one must remain active")

// ErrValidation wraps subscription-save failures produced by the Pass-2
// `sing-box check` gate when the merged config is rejected. Callers can
// use errors.Is to surface a 422 instead of 500 — the user's payload is
// the problem, not the daemon.
var ErrValidation = errors.New("subscription: validation failed")

// ErrShareLinkInvalid is returned when AddManualMember could not parse
// the supplied share-link into exactly one outbound.
var ErrShareLinkInvalid = errors.New("subscription: share-link did not parse to a single outbound")

// ErrMemberDuplicate is returned when AddManualMember would create a
// member with a tag that already exists for this subscription. Tags are
// derived from the server identity (StableTag), so the same vless://
// twice produces the same tag.
var ErrMemberDuplicate = errors.New("subscription: member already exists")

// ErrMemberNotFound is returned by RemoveMember when the supplied tag
// does not match any of the subscription's current members.
var ErrMemberNotFound = errors.New("subscription: member not found")

// AddManualMember parses a single share-link, validates it, and adds it
// to an inline subscription as a new member. Re-uses the same StableTag
// derivation as the URL refresh path so adding the same server twice
// short-circuits with ErrMemberDuplicate.
//
// On selector-mode subs the new member becomes available for picking;
// on urltest-mode subs it joins the auto-test pool. Active member is
// preserved (caller chooses whether to switch).
func (s *Service) AddManualMember(ctx context.Context, id, shareLink string) (*Subscription, error) {
	s.logInfo("subscription-member-add", id, "add requested")
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()

	sub, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	if !sub.IsInline() {
		return nil, ErrManualMemberOnURLSub
	}

	parsed := vlink.ParseBatch([]string{shareLink})
	if len(parsed.Outbounds) != 1 {
		return nil, ErrShareLinkInvalid
	}
	parts := partitionParsedOutbounds(sub.ID, parsed.Outbounds)
	s.logPartitionResult(sub.ID, parts)
	if len(parts.Valid) == 0 {
		rejected := appendRejectedUnique(sub.RejectedMembers, parts.Rejected...)
		info := mergeInfoItems(sub.InfoItems, filterDismissedInfo(parts.Info, sub.DismissedInfoIDs))
		if len(rejected) == len(sub.RejectedMembers) && len(info) == len(sub.InfoItems) {
			if len(parts.Rejected) > 0 {
				return nil, fmt.Errorf("subscription: %s", parts.Rejected[0].Reason)
			}
			if len(parts.Info) > 0 {
				return nil, errors.New("subscription: info banner (not a server); use «Перенести в info» after refresh or add a valid share-link")
			}
			return nil, ErrShareLinkInvalid
		}
		if err := s.store.SetRejectedAndInfo(id, rejected, info); err != nil {
			return nil, err
		}
		updated, err := s.store.Get(id)
		if err != nil {
			return nil, err
		}
		s.logInfo("subscription-member-add", id, fmt.Sprintf("stored extras rejected=%d info=%d (no valid member)", len(parts.Rejected), len(parts.Info)))
		return updated, nil
	}
	out := parts.Valid[0]
	// Набор для chooseKeys недоступен (credential существующих членов не
	// хранится), поэтому тег нового члена считаем по расширенному ключу, а
	// точный повтор ловим по метаданным MemberInfo (server+port+protocol+SNI).
	// ponytail: short_id/uuid в MemberInfo нет — два сервера на одном
	// server:port:SNI с разным short_id/uuid дадут ложный отказ; редкий край.
	tag := stableTagFromKey(sub.ID, extendedKey(out))
	mi := toMemberInfo(tag, out)
	for _, existing := range sub.Members {
		if existing.Server == mi.Server && existing.Port == mi.Port &&
			existing.Protocol == mi.Protocol && existing.SNI == mi.SNI {
			return nil, ErrMemberDuplicate
		}
	}

	// Fail-closed write order: mutate sing-box config (idempotent
	// upserts) BEFORE persisting to storage. If either AddOutbound
	// call fails, roll back any partial config change so storage and
	// sing-box stay aligned.
	newTags := append([]string{}, sub.MemberTags...)
	newTags = append(newTags, tag)
	subForBuild := *sub
	subForBuild.MemberTags = newTags
	groupBody := BuildGroupOutbound(subForBuild, newTags, sub.ActiveMember)

	if err := s.mutator.AddOutbound(tag, replaceTag(out.Outbound, tag)); err != nil {
		s.logWarn("subscription-member-add", id, "failed to add outbound: "+err.Error())
		return nil, fmt.Errorf("add outbound: %w", err)
	}
	if err := s.mutator.AddOutbound(sub.SelectorTag, groupBody); err != nil {
		// Rollback the partial member add. If rollback itself fails
		// the config slot now contains an unreferenced member outbound
		// (sing-box runs fine, but no code path will reap it). Surface
		// that explicitly so the caller can advise a full subscription
		// refresh/delete to clean the slot.
		if rbErr := s.mutator.RemoveOutbound(tag); rbErr != nil {
			s.logWarn("subscription-member-add", id, "failed to rebuild selector and rollback member outbound")
			return nil, fmt.Errorf("rebuild group outbound: %w (rollback also failed, leaving orphan outbound %q in sing-box config: %v)", err, tag, rbErr)
		}
		s.logWarn("subscription-member-add", id, "failed to rebuild selector outbound: "+err.Error())
		return nil, fmt.Errorf("rebuild group outbound: %w", err)
	}

	newMembers := append([]MemberInfo{}, sub.Members...)
	newMembers = append(newMembers, toMemberInfo(tag, out))
	rejected := appendRejectedUnique(sub.RejectedMembers, parts.Rejected...)
	info := mergeInfoItems(sub.InfoItems, filterDismissedInfo(parts.Info, sub.DismissedInfoIDs))
	if err := s.store.SetMembersExtras(id, newMembers, sub.OrphanTags, rejected, info, nil); err != nil {
		return nil, err
	}

	if err := s.mutator.Reload(ctx); err != nil {
		s.logWarn("subscription-member-add", id, "reload failed: "+err.Error())
		return nil, fmt.Errorf("reload: %w", err)
	}
	updated, err := s.store.Get(id)
	if err != nil {
		s.logWarn("subscription-member-add", id, "post-add read failed: "+err.Error())
		return nil, err
	}
	s.logInfo("subscription-member-add", id, "member added: "+tag)
	return updated, nil
}

// RemoveMember drops a single member from an inline subscription. When
// the removed member is the last one, the entire subscription tears
// down (proxy/inbound/selector teardown) — there is no meaningful empty
// subscription. When the removed member was the active selector member,
// the active selector is auto-bumped to the next remaining member and a
// Clash API switch is issued so traffic does not stall on a dangling
// reference.
//
// Returns (nil, nil) when the subscription was deleted (last-member case);
// returns (updatedSub, nil) otherwise.
func (s *Service) RemoveMember(ctx context.Context, id, memberTag string) (*Subscription, error) {
	s.logInfo("subscription-member-remove", id, "remove requested: "+memberTag)
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()

	sub, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	if !sub.IsInline() {
		return nil, ErrManualMemberOnURLSub
	}

	idx := -1
	for i, m := range sub.Members {
		if m.Tag == memberTag {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, ErrMemberNotFound
	}

	// Last member → full subscription teardown.
	if len(sub.Members) == 1 {
		if err := s.deleteLocked(ctx, id); err != nil {
			s.logWarn("subscription-member-remove", id, "last-member delete failed: "+err.Error())
			return nil, err
		}
		s.logInfo("subscription-member-remove", id, "removed last member, subscription deleted")
		return nil, nil
	}

	// Fail-closed: rebuild the selector pointing at the SHRUNK member
	// list FIRST. If that AddOutbound fails, the original member outbound
	// is still in config and the original selector is unchanged — no
	// orphan, no storage write happened, caller can retry safely.
	newTags := append([]string{}, sub.MemberTags[:idx]...)
	newTags = append(newTags, sub.MemberTags[idx+1:]...)
	newActive := sub.ActiveMember
	if newActive == memberTag {
		newActive = newTags[0] // SetMembers will mirror this; pre-compute for the rebuild
	}
	groupBody := BuildGroupOutbound(*sub, newTags, newActive)
	if err := s.mutator.AddOutbound(sub.SelectorTag, groupBody); err != nil {
		s.logWarn("subscription-member-remove", id, "failed to rebuild selector outbound: "+err.Error())
		return nil, fmt.Errorf("rebuild group outbound: %w", err)
	}

	// Selector now references newTags only; safe to drop the old member
	// outbound. RemoveOutbound is idempotent.
	s.mutator.RemoveOutbound(memberTag)

	newMembers := append([]MemberInfo{}, sub.Members[:idx]...)
	newMembers = append(newMembers, sub.Members[idx+1:]...)
	if err := s.store.SetMembers(id, newMembers, sub.OrphanTags); err != nil {
		return nil, err
	}

	updated, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}

	// SetMembers auto-bumps ActiveMember to the first remaining tag if
	// the prior active was removed. Mirror that to the live Clash API
	// for selector mode so connections don't stall on a missing tag.
	// urltest mode auto-routes by latency — Clash API is not used.
	if sub.ActiveMember == memberTag && updated.EffectiveMode() == ModeSelector && updated.ActiveMember != "" {
		_ = s.mutator.SelectClashProxy(updated.SelectorTag, updated.ActiveMember)
	}

	if err := s.mutator.Reload(ctx); err != nil {
		s.logWarn("subscription-member-remove", id, "reload failed: "+err.Error())
		return updated, fmt.Errorf("reload: %w", err)
	}
	s.logInfo("subscription-member-remove", id, "member removed: "+memberTag)
	return updated, nil
}

// ExcludeMembers помечает теги исключёнными: пересобирает селектор без них,
// снимает их outbounds, переносит в ExcludedTags/ExcludedMembers. Обратимо
// через RestoreMembers. Только для URL-подписок.
func (s *Service) ExcludeMembers(ctx context.Context, id string, tags []string) (*Subscription, error) {
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()

	sub, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	if sub.IsInline() {
		return nil, ErrExcludeOnInline
	}
	exSet := make(map[string]bool, len(tags))
	for _, t := range tags {
		exSet[t] = true
	}

	keep := make([]MemberInfo, 0, len(sub.Members))
	moved := make([]MemberInfo, 0, len(tags))
	keepTags := make([]string, 0, len(sub.Members))
	for _, m := range sub.Members {
		if exSet[m.Tag] {
			moved = append(moved, m)
		} else {
			keep = append(keep, m)
			keepTags = append(keepTags, m.Tag)
		}
	}
	if len(moved) == 0 {
		return nil, ErrMemberNotFound
	}
	if len(keep) == 0 {
		return nil, ErrAllMembersExcluded
	}

	newActive := sub.ActiveMember
	if exSet[newActive] {
		newActive = keepTags[0]
	}

	// (1) fail-closed: пересобрать селектор на сокращённый набор ПЕРВЫМ.
	if err := s.mutator.AddOutbound(sub.SelectorTag, BuildGroupOutbound(*sub, keepTags, newActive)); err != nil {
		return nil, fmt.Errorf("rebuild group outbound: %w", err)
	}
	// (2) снять outbounds исключённых (идемпотентно).
	for _, m := range moved {
		s.mutator.RemoveOutbound(m.Tag)
	}
	// (3) union ExcludedTags + ExcludedMembers, atomic store-write.
	excludedTags := append(append([]string{}, sub.ExcludedTags...), tags...)
	excludedMembers := append(append([]MemberInfo{}, sub.ExcludedMembers...), moved...)
	if err := s.store.MoveToExcluded(id, keep, excludedTags, excludedMembers); err != nil {
		return nil, err
	}
	updated, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	// (4) синхр. Clash для selector-режима, если active сдвинулся.
	if sub.ActiveMember != updated.ActiveMember && updated.EffectiveMode() == ModeSelector && updated.ActiveMember != "" {
		_ = s.mutator.SelectClashProxy(updated.SelectorTag, updated.ActiveMember)
	}
	if err := s.mutator.Reload(ctx); err != nil {
		return updated, fmt.Errorf("reload: %w", err)
	}
	return updated, nil
}

// RestoreMembers убирает теги из ExcludedTags и запускает refresh — вернувшиеся
// серверы материализуются. Стоимость = один обычный refresh (редкая операция).
func (s *Service) RestoreMembers(ctx context.Context, id string, tags []string) (*Subscription, error) {
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()

	sub, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	rm := make(map[string]bool, len(tags))
	for _, t := range tags {
		rm[t] = true
	}
	newExcludedTags := make([]string, 0, len(sub.ExcludedTags))
	for _, t := range sub.ExcludedTags {
		if !rm[t] {
			newExcludedTags = append(newExcludedTags, t)
		}
	}
	newExcludedMembers := make([]MemberInfo, 0, len(sub.ExcludedMembers))
	for _, m := range sub.ExcludedMembers {
		if !rm[m.Tag] {
			newExcludedMembers = append(newExcludedMembers, m)
		}
	}
	if err := s.store.SetExcludedTags(id, newExcludedTags, newExcludedMembers); err != nil {
		return nil, err
	}
	if _, err := s.refreshLocked(ctx, id); err != nil {
		return nil, err
	}
	return s.store.Get(id)
}

// PreviewMember — display-метаданные одного сервера из read-only превью URL-подписки.
// Key — subID-независимый суффикс тега (узкий, либо расширенный при коллизии маскировки); по нему исключают при создании.
type PreviewMember struct {
	Key       string `json:"key"`
	Label     string `json:"label,omitempty"`
	Protocol  string `json:"protocol"`
	Server    string `json:"server"`
	Port      uint16 `json:"port"`
	SNI       string `json:"sni,omitempty"`
	Transport string `json:"transport,omitempty"`
	Security  string `json:"security,omitempty"`
}

// PreviewURL качает и парсит URL-подписку БЕЗ создания/записи — для шага превью
// при импорте. Key — subID-независимый суффикс тега (узкий, либо расширенный при коллизии маскировки); по нему исключают при создании.
// ponytail: small read-only dup of fetch+detect — safer than refactoring tested refreshLocked.
func (s *Service) PreviewURL(ctx context.Context, url string, headers []Header) ([]PreviewMember, error) {
	if url == "" {
		return nil, errors.New("subscription: preview requires a URL")
	}
	fetchURL, _ := RewriteForRaw(url)
	body, ct, err := FetchWithContext(ctx, fetchURL, headers, s.fetchOpts)
	if err != nil {
		return nil, fmt.Errorf("%s", MaskURL(err.Error(), url))
	}
	var parseRes vlink.BatchResult
	switch {
	case vlink.IsClashYAML(body):
		parseRes = vlink.ParseClashBody(body)
	case vlink.IsSingboxJSON(body):
		parseRes = vlink.ParseSingboxBody(body)
	default:
		parseRes = vlink.ParseBatch(NormalizeBody(body, ct))
	}
	parts := partitionParsedOutbounds("preview", parseRes.Outbounds)
	out := make([]PreviewMember, 0, len(parts.Valid))
	keys := chooseKeys(parts.Valid)
	for i, p := range parts.Valid {
		mi := toMemberInfo(StableTag("preview00", p), p) // tag игнорируется, берём поля
		out = append(out, PreviewMember{
			Key: suffixOf(keys[i]), Label: mi.Label, Protocol: mi.Protocol,
			Server: mi.Server, Port: mi.Port, SNI: mi.SNI,
			Transport: mi.Transport, Security: mi.Security,
		})
	}
	return out, nil
}

// GetActiveNow returns the currently-active member tag as reported by the
// running sing-box Clash API. For urltest-mode subscriptions this reflects
// the auto-selected fastest member, which can drift from the persisted
// ActiveMember. Returns ("", nil) when Clash is unreachable so callers can
// fall back to stored ActiveMember.
func (s *Service) GetActiveNow(_ context.Context, id string) (string, error) {
	sub, err := s.store.Get(id)
	if err != nil {
		s.logWarn("subscription-active-now", id, "load failed: "+err.Error())
		return "", err
	}
	now, err := s.mutator.GetClashSelectorActive(sub.SelectorTag)
	if err != nil {
		s.logWarn("subscription-active-now", id, "clash query failed: "+err.Error())
		return "", err
	}
	if now == "" {
		s.logInfo("subscription-active-now", id, "no live active member (clash unavailable or not selected yet)")
	} else {
		s.logInfo("subscription-active-now", id, "live active member: "+now)
	}
	return now, nil
}

// ErrInfoItemsFull is returned when MoveRejectedToInfo would exceed MaxSubscriptionInfoItems.
var ErrInfoItemsFull = errors.New("subscription: info block is full (max 4 items)")

// ErrRejectedMemberNotFound is returned when the tag is not in RejectedMembers.
var ErrRejectedMemberNotFound = errors.New("subscription: rejected member not found")

// ErrInfoItemNotFound is returned when RemoveInfoItem cannot find the id.
var ErrInfoItemNotFound = errors.New("subscription: info item not found")

// MoveRejectedToInfo promotes one rejected entry to the pinned info block (user source).
func (s *Service) MoveRejectedToInfo(ctx context.Context, id, memberTag string) (*Subscription, error) {
	s.logInfo("subscription-rejected-to-info", id, "move requested: "+memberTag)
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()
	sub, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	idx := findRejected(sub.RejectedMembers, memberTag)
	if idx < 0 {
		return nil, ErrRejectedMemberNotFound
	}
	if len(sub.InfoItems) >= MaxSubscriptionInfoItems {
		return nil, ErrInfoItemsFull
	}
	r := sub.RejectedMembers[idx]
	label := strings.TrimSpace(r.Label)
	if label == "" {
		label = r.Tag
	}
	item := SubscriptionInfoItem{
		ID:     infoItemID(r.Tag, label),
		Label:  label,
		Tag:    r.Tag,
		Source: "auto",
	}
	for _, existing := range sub.InfoItems {
		if existing.ID == item.ID {
			return nil, fmt.Errorf("subscription: info item %q already present", item.ID)
		}
	}
	rejected := append(sub.RejectedMembers[:idx], sub.RejectedMembers[idx+1:]...)
	info := append(sub.InfoItems, item)
	if err := s.store.SetRejectedAndInfo(id, rejected, info); err != nil {
		return nil, err
	}
	if item.ID != "" {
		_ = s.store.UnmarkDismissedInfoID(id, item.ID)
	}
	return s.store.Get(id)
}

// RemoveInfoItem moves a provider info line to rejectedMembers; refresh won't re-add it to info.
func (s *Service) RemoveInfoItem(ctx context.Context, id, itemID string) (*Subscription, error) {
	s.logInfo("subscription-info-remove", id, "remove requested: "+itemID)
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()
	if err := s.store.RemoveInfoItem(id, itemID); err != nil {
		return nil, err
	}
	return s.store.Get(id)
}

// DeleteOrphans removes orphan-flagged outbounds from sing-box config and
// clears the OrphanTags slice in the store.
func (s *Service) DeleteOrphans(ctx context.Context, id string) error {
	s.logInfo("subscription-orphans-delete", id, "delete requested")
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()
	sub, err := s.store.Get(id)
	if err != nil {
		return err
	}
	for _, t := range sub.OrphanTags {
		s.mutator.RemoveOutbound(t)
	}
	if err := s.store.SetMembership(id, sub.MemberTags, nil); err != nil {
		s.logWarn("subscription-orphans-delete", id, "failed to clear orphan list in store: "+err.Error())
		return err
	}
	if err := s.mutator.Reload(ctx); err != nil {
		s.logWarn("subscription-orphans-delete", id, "reload failed: "+err.Error())
		return err
	}
	s.logInfo("subscription-orphans-delete", id, fmt.Sprintf("deleted %d orphan outbounds", len(sub.OrphanTags)))
	return nil
}
