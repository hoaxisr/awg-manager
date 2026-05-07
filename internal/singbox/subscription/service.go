package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if in.URL == "" {
		return nil, errors.New("subscription: URL is required")
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
	body, ct, err := Fetch(sub.URL, sub.Headers, s.fetchOpts)
	if err != nil {
		masked := fmt.Errorf("%s", MaskURL(err.Error(), sub.URL))
		s.store.UpdateState(id, RefreshResult{When: time.Now(), Err: masked})
		return nil, masked
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
		When:         time.Now(),
		Added:        len(diff.New),
		Updated:      len(diff.Existing),
		Orphaned:     len(diff.Orphan),
		SkippedVmess: parseRes.SkippedVmess,
		SkippedOther: parseRes.SkippedUnsupp,
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

	// Selector — remove old (idempotent) then add fresh.
	s.mutator.RemoveOutbound(sub.SelectorTag)
	if err := s.mutator.AddOutbound(sub.SelectorTag, BuildSelector(sub.SelectorTag, memberTags, "")); err != nil {
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
	return s.store.Update(id, patch)
}

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
	s.mutator.RemoveOutbound(sub.SelectorTag)
	if err := s.mutator.AddOutbound(sub.SelectorTag, BuildSelector(sub.SelectorTag, sub.MemberTags, memberTag)); err != nil {
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
