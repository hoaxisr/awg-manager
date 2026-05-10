package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

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
	Reload(ctx context.Context) error
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
}

// Service is the subscription business-logic facade.
type Service struct {
	store     *Store
	mutator   ConfigMutator
	muById    sync.Map // map[string]*sync.Mutex
	fetchOpts FetchOpts
}

func NewService(store *Store, mutator ConfigMutator) *Service {
	return &Service{store: store, mutator: mutator}
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
	switch {
	case in.URL == "" && in.Inline == "":
		return nil, errors.New("subscription: either URL or inline content is required")
	case in.URL != "" && in.Inline != "":
		return nil, errors.New("subscription: URL and inline content are mutually exclusive")
	}
	sub, err := s.store.Create(in)
	if err != nil {
		return nil, err
	}
	mu := s.lockSub(sub.ID)
	mu.Lock()
	defer mu.Unlock()

	port, err := s.mutator.AllocListenPort()
	if err != nil {
		s.store.Delete(sub.ID)
		return nil, fmt.Errorf("subscription: alloc listen port: %w", err)
	}
	if err := s.store.SetListenPort(sub.ID, port); err != nil {
		s.store.Delete(sub.ID)
		return nil, err
	}

	proxyIdx, err := s.mutator.AllocProxyIndex(ctx)
	if err != nil {
		s.store.Delete(sub.ID)
		return nil, fmt.Errorf("subscription: alloc proxy index: %w", err)
	}
	if err := s.store.SetProxyIndex(sub.ID, proxyIdx); err != nil {
		s.store.Delete(sub.ID)
		return nil, err
	}
	if err := s.mutator.EnsureProxy(ctx, proxyIdx, int(port), sub.Label); err != nil {
		// Best-effort cleanup: EnsureProxy may have partially registered
		// the interface before failing. RemoveProxy is idempotent.
		_ = s.mutator.RemoveProxy(ctx, proxyIdx)
		s.store.Delete(sub.ID)
		return nil, fmt.Errorf("subscription: register NDMS proxy: %w", err)
	}

	if _, err := s.refreshLocked(ctx, sub.ID); err != nil {
		// EnsureProxy succeeded above — the NDMS Proxy interface is now
		// live in the router. We must roll it back before dropping the
		// storage row; otherwise every failed Create leaks a ProxyN that
		// only the startup cleanup sweep would eventually reap. Swallow
		// the RemoveProxy error: the storage row is going away regardless,
		// and a stranded ProxyN is recoverable via Settings → cleanup.
		_ = s.mutator.RemoveProxy(ctx, proxyIdx)
		s.store.Delete(sub.ID)
		return nil, fmt.Errorf("subscription: initial fetch failed: %w", err)
	}

	final, _ := s.store.Get(sub.ID)
	return final, nil
}

func (s *Service) Refresh(ctx context.Context, id string) (*RefreshResult, error) {
	mu := s.lockSub(id)
	mu.Lock()
	defer mu.Unlock()
	return s.refreshLocked(ctx, id)
}

func (s *Service) refreshLocked(ctx context.Context, id string) (*RefreshResult, error) {
	sub, err := s.store.Get(id)
	if err != nil {
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
		fetched, fetchedCT, fetchErr := Fetch(sub.URL, sub.Headers, s.fetchOpts)
		if fetchErr != nil {
			masked := fmt.Errorf("%s", MaskURL(fetchErr.Error(), sub.URL))
			s.store.UpdateState(id, RefreshResult{When: time.Now(), Err: masked})
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
		err := errors.New("subscription: тело подписки выглядит как JSON, но не похоже на sing-box config (нет outbounds). Поддерживаются: sing-box JSON config (одиночный, массив конфигов, или массив outbounds), Clash / mihomo YAML, base64 share-links, plain text vless://, trojan://, ss://, hysteria2://.")
		s.store.UpdateState(id, RefreshResult{When: time.Now(), Err: err})
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

	if len(parseRes.Outbounds) == 0 {
		// Distinguish failure shapes:
		//  1. Clash YAML parsed cleanly with proxies: []  → subscription empty
		//  2. sing-box JSON parsed cleanly with outbounds: [] → empty
		//  3. Clash YAML / sing-box JSON / share-link parsed with errors → bad entries.
		//  4. Body in unsupported format → 0 entries, 0 errors, not structured.
		emptyClean := len(parseRes.Errors) == 0 && parseRes.SkippedVmess == 0 && parseRes.SkippedUnsupp == 0
		var errMsg string
		switch {
		case isClash && emptyClean:
			errMsg = "subscription: подписка пуста (proxies: []). Возможно, истекла или ещё не активирована — проверь на стороне провайдера."
		case isSbJSON && emptyClean:
			errMsg = "subscription: подписка пуста (outbounds: []). Возможно, истекла или ещё не активирована — проверь на стороне провайдера."
		case len(parseRes.Errors) > 0:
			hint := "ни одной валидной ссылки. Поддерживаются: base64-encoded share-links, HTML с share-link якорями, plain text со ссылками vless://, trojan://, ss://, hysteria2://, Clash YAML / mihomo, а также sing-box JSON (одиночный, массив конфигов, или массив outbounds; типы vless, trojan, ss, hysteria2). Записи vmess пропускаются."
			errMsg = fmt.Sprintf("subscription: %s Первая ошибка парсера: %s", hint, parseRes.Errors[0].Error())
		default:
			hint := "ни одной валидной ссылки. Поддерживаются: base64-encoded share-links, HTML с share-link якорями, plain text со ссылками vless://, trojan://, ss://, hysteria2://, Clash YAML / mihomo, а также sing-box JSON (одиночный, массив конфигов, или массив outbounds; типы vless, trojan, ss, hysteria2). Записи vmess пропускаются."
			errMsg = fmt.Sprintf("subscription: %s", hint)
		}
		err := errors.New(errMsg)
		s.store.UpdateState(id, RefreshResult{When: time.Now(), Err: err})
		return nil, err
	}

	diff := ApplyDiff(id, sub.MemberTags, parseRes.Outbounds)

	if err := s.applyDiff(ctx, sub, diff); err != nil {
		s.store.UpdateState(id, RefreshResult{When: time.Now(), Err: err})
		return nil, err
	}

	newMembers := make([]MemberInfo, 0, len(diff.New)+len(diff.Existing))
	for _, n := range diff.New {
		newMembers = append(newMembers, toMemberInfo(n.Tag, n.Out))
	}
	for _, e := range diff.Existing {
		newMembers = append(newMembers, toMemberInfo(e.Tag, e.Out))
	}
	if err := s.store.SetMembers(id, newMembers, diff.Orphan); err != nil {
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
		return nil, err
	}
	return res, nil
}

// applyDiff commits the diff to sing-box config. Selector + mixed inbound +
// route rule are recreated each refresh (they may not exist yet on first
// run). Per-member outbounds are added/updated/left alone — orphans are NOT
// removed (the UI offers explicit deletion).
func (s *Service) applyDiff(ctx context.Context, sub *Subscription, diff DiffResult) error {
	for _, n := range diff.New {
		jsonWithTag := replaceTag(n.Out.Outbound, n.Tag)
		if err := s.mutator.AddOutbound(n.Tag, jsonWithTag); err != nil {
			return err
		}
	}
	for _, e := range diff.Existing {
		jsonWithTag := replaceTag(e.Out.Outbound, e.Tag)
		if err := s.mutator.UpdateOutbound(e.Tag, jsonWithTag); err != nil {
			return err
		}
	}

	memberTags := make([]string, 0, len(diff.New)+len(diff.Existing))
	for _, n := range diff.New {
		memberTags = append(memberTags, n.Tag)
	}
	for _, e := range diff.Existing {
		memberTags = append(memberTags, e.Tag)
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
	// Other patch fields (label / URL / headers / refresh-cadence /
	// enabled) only mutate metadata and don't need a config rewrite.
	if patch.Mode != nil || patch.URLTest != nil {
		s.mutator.RemoveOutbound(sub.SelectorTag)
		if err := s.mutator.AddOutbound(sub.SelectorTag, BuildGroupOutbound(*sub, sub.MemberTags, sub.ActiveMember)); err != nil {
			return sub, fmt.Errorf("rebuild group outbound: %w", err)
		}
		if err := s.mutator.Reload(context.Background()); err != nil {
			return sub, fmt.Errorf("reload after mode change: %w", err)
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

	// 1. Update config slot's selector.default for restart persistence.
	//    NOT followed by Reload — clash API handles the runtime switch.
	//    For urltest mode: BuildURLTest ignores defaultTag (urltest has
	//    no `default` field). The Clash API call below still pins the
	//    chosen member as a temporary override until sing-box's next
	//    auto-test interval, matching mihomo/sing-box semantics.
	s.mutator.RemoveOutbound(sub.SelectorTag)
	if err := s.mutator.AddOutbound(sub.SelectorTag, BuildGroupOutbound(*sub, sub.MemberTags, memberTag)); err != nil {
		return err
	}

	// 2. Persist active member in store.
	if err := s.store.SetActiveMember(id, memberTag); err != nil {
		return err
	}

	// 3. Hit Clash API for instant runtime switch — no SIGHUP, no connection
	//    drop. If clash is unreachable (sing-box not running), return error but
	//    config is already updated so a future restart will use the new default.
	if err := s.mutator.SelectClashProxy(sub.SelectorTag, memberTag); err != nil {
		return fmt.Errorf("subscription: clash select: %w", err)
	}

	return nil
}

// ErrManualMemberOnURLSub is returned by AddManualMember / RemoveMember
// when called on a URL-backed (non-inline) subscription. Manual member
// CRUD is currently scoped to inline subscriptions because the URL diff
// pipeline owns the truth of which members exist; mixing manual entries
// in would race the next refresh.
var ErrManualMemberOnURLSub = errors.New("subscription: member CRUD is only allowed on inline subscriptions")

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
	out := parsed.Outbounds[0]
	tag := StableTag(sub.ID, out)

	for _, existing := range sub.MemberTags {
		if existing == tag {
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
		return nil, fmt.Errorf("add outbound: %w", err)
	}
	if err := s.mutator.AddOutbound(sub.SelectorTag, groupBody); err != nil {
		// Rollback the partial member add. If rollback itself fails
		// the config slot now contains an unreferenced member outbound
		// (sing-box runs fine, but no code path will reap it). Surface
		// that explicitly so the caller can advise a full subscription
		// refresh/delete to clean the slot.
		if rbErr := s.mutator.RemoveOutbound(tag); rbErr != nil {
			return nil, fmt.Errorf("rebuild group outbound: %w (rollback also failed, leaving orphan outbound %q in sing-box config: %v)", err, tag, rbErr)
		}
		return nil, fmt.Errorf("rebuild group outbound: %w", err)
	}

	newMembers := append([]MemberInfo{}, sub.Members...)
	newMembers = append(newMembers, toMemberInfo(tag, out))
	if err := s.store.SetMembers(id, newMembers, sub.OrphanTags); err != nil {
		return nil, err
	}

	if err := s.mutator.Reload(ctx); err != nil {
		return nil, fmt.Errorf("reload: %w", err)
	}
	updated, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
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
			return nil, err
		}
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
		return updated, fmt.Errorf("reload: %w", err)
	}
	return updated, nil
}

// GetActiveNow returns the currently-active member tag as reported by the
// running sing-box Clash API. For urltest-mode subscriptions this reflects
// the auto-selected fastest member, which can drift from the persisted
// ActiveMember. Returns ("", nil) when Clash is unreachable so callers can
// fall back to stored ActiveMember.
func (s *Service) GetActiveNow(_ context.Context, id string) (string, error) {
	sub, err := s.store.Get(id)
	if err != nil {
		return "", err
	}
	return s.mutator.GetClashSelectorActive(sub.SelectorTag)
}

// DeleteOrphans removes orphan-flagged outbounds from sing-box config and
// clears the OrphanTags slice in the store.
func (s *Service) DeleteOrphans(ctx context.Context, id string) error {
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
		return err
	}
	return s.mutator.Reload(ctx)
}
