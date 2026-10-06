package localdeps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

// singboxSubscription maps a subscription field by field. Nothing that
// can carry the provider's token is read here: not the URL beyond its
// host, not the headers, not the pasted body, not the file path.
func singboxSubscription(s *subscription.Subscription) mcpsrv.SingboxSubscription {
	out := mcpsrv.SingboxSubscription{
		ID:              s.ID,
		Label:           sanitizeLabel(s.Label),
		SourceType:      "url",
		Host:            hostOf(s.URL),
		Enabled:         s.Enabled,
		Mode:            string(s.EffectiveMode()),
		GroupTag:        s.SelectorTag,
		MemberCount:     len(s.MemberTags),
		ExcludedCount:   len(s.ExcludedTags),
		OrphanCount:     len(s.OrphanTags),
		RefreshHours:    s.RefreshHours,
		LastFetchFailed: s.LastError != "",
		LastErrorKind:   lastErrorKind(s),
	}
	switch {
	case s.IsInline():
		out.SourceType = "inline"
	case s.IsFile():
		out.SourceType = "file"
	}
	if !s.LastFetched.IsZero() {
		out.LastFetched = s.LastFetched.UTC().Format(time.RFC3339)
	}
	return out
}

// ListSingboxSubscriptions returns every subscription in the store's own
// order (label, then id), so that an offset is stable between calls.
func (l *Local) ListSingboxSubscriptions(context.Context) ([]mcpsrv.SingboxSubscription, error) {
	if l.c.Subscriptions == nil {
		return nil, errUnavailable("sing-box subscriptions")
	}
	list := l.c.Subscriptions.List()
	out := make([]mcpsrv.SingboxSubscription, 0, len(list))
	for i := range list {
		out = append(out, singboxSubscription(&list[i]))
	}
	return out, nil
}

// lastErrorKind reduces the stored error to one word. The text itself
// never crosses the boundary, and masking it would not be enough: it is
// built from arbitrary errors, so it can quote the subscription URL, the
// path of a file subscription, and — through the parser — fragments of
// the list itself, a server's uuid included (a failed url.Parse embeds
// the whole share link).
//
// Only what is known is claimed. The two markers are the daemon's own
// wording in subscription.Service (refreshLockedOpts, "len(parts.Valid)
// == 0") and are true when present. Everything else is other: the same
// field records a failed download, an unreadable file, a body that is not
// a server list, a failed apply or reload, an undecryptable link and a bad
// filter, and the text cannot be told apart reliably. If the daemon's
// wording changes, the answer degrades to other; it never leaks.
func lastErrorKind(s *subscription.Subscription) string {
	switch {
	case s.LastError == "":
		return ""
	case strings.Contains(s.LastError, "подписка пуста"):
		return "empty"
	case strings.Contains(s.LastError, "ни одной валидной ссылки"):
		return "parse"
	}
	return "other"
}

// sbIndex is one consistent read of what sing-box is configured with,
// keyed the way the group tools look things up.
type sbIndex struct {
	groups      map[string]router.CompositeOutboundView
	order       []string          // group tags, in configuration order
	subByGroup  map[string]string // a subscription's own group tag → its id
	subLabel    map[string]string // subscription id → its sanitised label
	aggByTag    map[string]subscription.AggregateGroup
	member      map[string]subscription.MemberInfo // subscription server tag → what it is
	isMember    map[string]bool                    // tags that are outbounds of a subscription
	notOutbound map[string]string                  // tag → why it cannot be used
	proxies     map[string]singbox.TunnelInfo      // hand-configured proxies; see withProxies
}

// singboxIndex reads the router and the subscriptions. Each is optional:
// a daemon without subscriptions still lists its groups, and the fields
// that would link them stay empty. The hand-configured proxies are not
// read here — see withProxies.
//
// A router that cannot be read (a draft that does not parse) does not
// stop the rest: the error is returned beside an index whose groups are
// empty, so a caller that only needs a proxy or a subscription's server
// can still proceed, and one that needs a group reports the router's
// error rather than "not found".
func (l *Local) singboxIndex(ctx context.Context) (sbIndex, error) {
	idx := sbIndex{
		groups:      map[string]router.CompositeOutboundView{},
		subByGroup:  map[string]string{},
		subLabel:    map[string]string{},
		aggByTag:    map[string]subscription.AggregateGroup{},
		member:      map[string]subscription.MemberInfo{},
		isMember:    map[string]bool{},
		notOutbound: map[string]string{},
		proxies:     map[string]singbox.TunnelInfo{},
	}
	var routerErr error
	if l.c.Router != nil {
		list, err := l.c.Router.ListCompositeOutbounds(ctx)
		routerErr = err
		for _, o := range list {
			idx.groups[o.Tag] = o
			idx.order = append(idx.order, o.Tag)
		}
	}
	if l.c.Subscriptions != nil {
		for _, s := range l.c.Subscriptions.List() {
			name := sanitizeLabel(s.Label)
			idx.subByGroup[s.SelectorTag] = s.ID
			idx.subLabel[s.ID] = name
			// A server orphaned by the last refresh stays an outbound until
			// the user deletes it (subscription.Service.DeleteOrphans).
			for _, tag := range s.MemberTags {
				idx.isMember[tag] = true
			}
			for _, tag := range s.OrphanTags {
				idx.isMember[tag] = true
			}
			for _, m := range s.Members {
				idx.member[m.Tag] = m
			}
			for _, tag := range s.ExcludedTags {
				idx.notOutbound[tag] = fmt.Sprintf("it is excluded from subscription %q by the user, so it is not an outbound", name)
			}
			for _, m := range s.FilteredMembers {
				idx.notOutbound[m.Tag] = fmt.Sprintf("it is hidden by the filter of subscription %q, so it is not an outbound", name)
			}
		}
		for _, g := range l.c.Subscriptions.ListGroups() {
			idx.aggByTag[g.Tag] = g
		}
	}
	return idx, routerErr
}

// withProxies adds the hand-configured proxies to the index. It is a
// separate step because Operator.ListTunnels asks the engine about every
// proxy (operator_tunnels.go: HasOutbound, one Clash request each, 5 s
// timeout) — a price only the lookups that classify a tag need to pay,
// and one list_singbox_outbounds never does. A failure is returned, not
// swallowed: with the proxies unknown a real tag would be classified as
// nothing and reported as not found, sending the agent to hunt a typo
// instead of the configuration error.
func (l *Local) withProxies(ctx context.Context, idx *sbIndex) error {
	if l.c.Singbox == nil {
		return nil
	}
	list, err := l.c.Singbox.ListTunnels(ctx)
	if err != nil {
		return fmt.Errorf("sing-box proxies could not be read, so the tag was not looked up: %w", err)
	}
	for _, t := range list {
		idx.proxies[t.Tag] = t
	}
	return nil
}

// whyNoGroup explains a tag that is not a group in the configuration but
// was not made up either. It is checked after kindOf found nothing.
// routerErr comes first: with the router unread, every group is missing,
// and a subscription's tag would be wrongly called unbuilt.
func (idx sbIndex) whyNoGroup(tag string, routerErr error) error {
	if routerErr != nil {
		return fmt.Errorf("the sing-box router configuration could not be read, so %q could not be looked up: %w", tag, routerErr)
	}
	if id, ok := idx.subByGroup[tag]; ok {
		// The service builds a subscription's group only from its
		// servers: a failed first fetch, or a filter that hides them
		// all, leaves no group (subscription.Service, ErrAllMembersFiltered).
		return fmt.Errorf("%q is the group of subscription %q, but sing-box has no such group yet: the subscription has no servers in the configuration. It may be disabled, its last fetch may have failed, or its filter may hide every server; list_singbox_subscriptions shows its state", tag, idx.subLabel[id])
	}
	return nil
}

// kindOf classifies a tag from configuration, never from the engine, so
// that a mistyped tag gets a precise answer while sing-box is down. The
// first match wins: group, member, proxy.
func (idx sbIndex) kindOf(tag string) string {
	switch {
	case hasKey(idx.groups, tag):
		return "group"
	case idx.isMember[tag]:
		return "member"
	case hasKey(idx.proxies, tag):
		return "proxy"
	}
	return ""
}

func hasKey[V any](m map[string]V, k string) bool { _, ok := m[k]; return ok }

// clashProxies reads the engine once. Any failure means the present is
// unknown; it is never turned into an empty answer.
func (l *Local) clashProxies() (map[string]singbox.ClashProxy, bool) {
	if l.c.Clash == nil {
		return nil, false
	}
	proxies, err := l.c.Clash.GetProxies()
	if err != nil {
		return nil, false
	}
	return proxies, true
}

// outbound assembles one group's summary.
//
// runtimeKnown is per group: the engine answered AND has an entry for this
// group. The router lists groups from the draft when one exists
// (orchestrator.LoadEffective) and from the disabled copy when the sing-box
// router is switched off, so a group can be configured and unknown to the
// engine; OutOfSync marks that, and a member list that differs from the
// engine's. It names no cause: the engine's view cannot tell them apart.
func (idx sbIndex) outbound(o router.CompositeOutboundView, proxies map[string]singbox.ClashProxy, engineAnswered bool) mcpsrv.SingboxOutbound {
	gp, inEngine := proxies[o.Tag]
	runtimeKnown := engineAnswered && inEngine
	out := mcpsrv.SingboxOutbound{
		Tag: o.Tag, Type: o.Type, Source: o.Source,
		MemberCount:  len(o.Outbounds),
		RuntimeKnown: runtimeKnown,
		OutOfSync:    engineAnswered && (!inEngine || !sameTags(gp.All, o.Outbounds)),
	}
	if id, ok := idx.subByGroup[o.Tag]; ok {
		out.SubscriptionID = id
	}
	if g, ok := idx.aggByTag[o.Tag]; ok {
		out.AggregateOf = append([]string(nil), g.UseSubscriptionIDs...)
	}
	if runtimeKnown {
		out.ActiveMember = gp.Now
		out.ActiveMemberLabel = sanitizeLabel(idx.member[out.ActiveMember].Label)
	}
	return out
}

// sameTags compares two tag lists as sets: the order of members is not a
// difference.
func sameTags(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, t := range a {
		set[t] = struct{}{}
	}
	other := make(map[string]struct{}, len(b))
	for _, t := range b {
		other[t] = struct{}{}
	}
	if len(set) != len(other) {
		return false
	}
	for t := range other {
		if _, ok := set[t]; !ok {
			return false
		}
	}
	return true
}

// ListSingboxOutbounds lists every group. The engine is read once for
// the whole list.
func (l *Local) ListSingboxOutbounds(ctx context.Context) ([]mcpsrv.SingboxOutbound, bool, error) {
	if l.c.Router == nil {
		return nil, false, errUnavailable("sing-box router")
	}
	idx, err := l.singboxIndex(ctx)
	if err != nil {
		return nil, false, err
	}
	proxies, known := l.clashProxies()
	out := make([]mcpsrv.SingboxOutbound, 0, len(idx.order))
	for _, tag := range idx.order {
		out = append(out, idx.outbound(idx.groups[tag], proxies, known))
	}
	return out, l.c.Router.StagingStatus(ctx).HasDraft, nil
}

// groupMember describes one member of a group.
func (idx sbIndex) groupMember(tag, active string, proxies map[string]singbox.ClashProxy, runtimeKnown bool) mcpsrv.SingboxGroupMember {
	m := mcpsrv.SingboxGroupMember{Tag: tag, Kind: idx.kindOf(tag)}
	switch m.Kind {
	case "member":
		// Every field here is the provider's text: for a sing-box JSON or
		// Clash YAML subscription the daemon checks only that the server
		// is not empty, and transport is transport.type verbatim.
		info := idx.member[tag]
		m.Label = sanitizeLabel(info.Label)
		m.Protocol, m.Server, m.Port = token(info.Protocol), hostShaped(info.Server), int(info.Port)
		m.Transport, m.Security = token(info.Transport), token(info.Security)
	case "proxy":
		t := idx.proxies[tag]
		m.Protocol, m.Server, m.Port = token(t.Protocol), hostShaped(t.Server), t.Port
		m.Transport, m.Security = token(t.Transport), token(t.Security)
	case "":
		// In the group's configuration and nowhere else we can see: a
		// built-in such as direct, or an AWG outbound. Calling it a proxy
		// would send the agent to list_singbox_tunnels, where it is not.
		m.Kind = "other"
	}
	if !runtimeKnown {
		return m
	}
	isActive := tag == active
	m.Active = &isActive
	// The engine appends to history; the last entry is the latest test.
	// A delay of 0 there is a test that got no answer.
	if h := proxies[tag].History; len(h) > 0 {
		m.DelayKnown = true
		if d := h[len(h)-1].Delay; d > 0 {
			m.LastDelayMs = &d
		}
	}
	return m
}

// GetSingboxOutbound returns one group with all its members.
func (l *Local) GetSingboxOutbound(ctx context.Context, tag string) (mcpsrv.SingboxOutboundDetail, error) {
	if l.c.Router == nil {
		return mcpsrv.SingboxOutboundDetail{}, errUnavailable("sing-box router")
	}
	idx, routerErr := l.singboxIndex(ctx)
	// A group's members may be hand-configured proxies, and a tag that is
	// not a group is told apart from a typo by the proxies too.
	if err := l.withProxies(ctx, &idx); err != nil {
		return mcpsrv.SingboxOutboundDetail{}, err
	}
	o, ok := idx.groups[tag]
	if !ok {
		switch idx.kindOf(tag) {
		case "member":
			return mcpsrv.SingboxOutboundDetail{}, fmt.Errorf("%q is a single server, not a group (groups are in list_singbox_outbounds)", tag)
		case "proxy":
			return mcpsrv.SingboxOutboundDetail{}, fmt.Errorf("%q is a single proxy, not a group (groups are in list_singbox_outbounds)", tag)
		}
		if why, ok := idx.notOutbound[tag]; ok {
			return mcpsrv.SingboxOutboundDetail{}, fmt.Errorf("%q is not a group: %s (groups are in list_singbox_outbounds)", tag, why)
		}
		if err := idx.whyNoGroup(tag, routerErr); err != nil {
			return mcpsrv.SingboxOutboundDetail{}, err
		}
		return mcpsrv.SingboxOutboundDetail{}, fmt.Errorf("sing-box group %q not found (use list_singbox_outbounds)", tag)
	}
	proxies, known := l.clashProxies()
	out := mcpsrv.SingboxOutboundDetail{
		SingboxOutbound: idx.outbound(o, proxies, known),
		Members:         make([]mcpsrv.SingboxGroupMember, 0, len(o.Outbounds)),
		HasDraft:        l.c.Router.StagingStatus(ctx).HasDraft,
	}
	for _, memberTag := range o.Outbounds {
		out.Members = append(out.Members, idx.groupMember(memberTag, out.ActiveMember, proxies, out.RuntimeKnown))
	}
	return out, nil
}

// CheckSingboxDelay probes one outbound. The tag is classified from
// configuration first: the delay test itself answers "no response" for a
// tag that does not exist, so a typo would otherwise be reported as an
// outbound that is down.
func (l *Local) CheckSingboxDelay(ctx context.Context, tag string) (mcpsrv.SingboxDelay, error) {
	if l.c.Singbox == nil {
		return mcpsrv.SingboxDelay{}, errUnavailable("sing-box")
	}
	// A router that cannot be read blocks only the tags it would have
	// classified: a proxy or a subscription's server is known without it.
	idx, routerErr := l.singboxIndex(ctx)
	if err := l.withProxies(ctx, &idx); err != nil {
		return mcpsrv.SingboxDelay{}, err
	}
	kind := idx.kindOf(tag)
	if kind == "" {
		if why, ok := idx.notOutbound[tag]; ok {
			return mcpsrv.SingboxDelay{}, fmt.Errorf("%q cannot be probed: %s", tag, why)
		}
		if err := idx.whyNoGroup(tag, routerErr); err != nil {
			return mcpsrv.SingboxDelay{}, err
		}
		return mcpsrv.SingboxDelay{}, fmt.Errorf("sing-box outbound %q not found (proxies are in list_singbox_tunnels, groups in list_singbox_outbounds, a group's servers in get_singbox_outbound)", tag)
	}
	// The prober answers 0 whenever it learns nothing: DelayChecker.Probe
	// swallows every transport error, and it goes through the same Clash
	// API. So the engine is asked first, and nothing is probed unless it
	// answers and runs the tag. Configuration may be an unapplied draft or
	// the disabled copy of a router that is switched off
	// (orchestrator.LoadEffective), so a configured tag can be missing
	// from the engine; probing it would read as "down". When no engine is
	// wired at all there is nothing to ask, and the probe runs as it is.
	if l.c.Clash != nil {
		proxies, answered := l.clashProxies()
		if !answered {
			return mcpsrv.SingboxDelay{}, fmt.Errorf("sing-box did not answer, so nothing was measured; this says nothing about whether %q works. sing-box may be stopped or reloading — check get_system_status and try again", tag)
		}
		if _, running := proxies[tag]; !running {
			return mcpsrv.SingboxDelay{}, fmt.Errorf("%q is configured but sing-box is not running it. Either it comes from a draft that is not applied yet (get_singbox_staging says whether one exists), or the sing-box router is switched off, or sing-box is still reloading. Nothing was measured, and this says nothing about whether it works", tag)
		}
	}
	ms, err := l.c.Singbox.CheckDelay(ctx, tag)
	if errors.Is(err, singbox.ErrProbeInFlight) {
		// The periodic sweep shares the prober and holds a slow outbound's
		// tag for several seconds; nothing was measured here, so neither
		// verdict applies.
		return mcpsrv.SingboxDelay{Tag: tag, Kind: kind, Busy: true}, nil
	}
	if err != nil {
		return mcpsrv.SingboxDelay{}, err
	}
	// CheckOne normalises a timeout to 0 ms, so 0 means silence — not a
	// round trip that took no time.
	out := mcpsrv.SingboxDelay{Tag: tag, Kind: kind, Reachable: ms > 0, DelayMs: ms}
	if kind == "group" {
		// Read after the probe: a urltest group may have just re-chosen.
		if proxies, answered := l.clashProxies(); answered {
			out.Via = proxies[tag].Now
		}
	}
	return out, nil
}

// SetSingboxSubscriptionEnabled switches one subscription. Only Enabled
// is sent: subscription.Service.Update reads a nil field as "not sent"
// and keeps the stored value, so a sparse patch is what leaves the
// filters, the mode and the URL alone.
//
// No invalidation event is published, because the REST handler publishes
// none: there is no subscription resource in internal/events, and an
// open web tab sees the change on its next fetch either way.
func (l *Local) SetSingboxSubscriptionEnabled(_ context.Context, id string, enabled bool) (mcpsrv.SingboxSubscription, []string, error) {
	if l.c.Subscriptions == nil {
		return mcpsrv.SingboxSubscription{}, nil, errUnavailable("sing-box subscriptions")
	}
	// Read first: the subscription may have been deleted since the agent
	// listed it, and the error should send it back to the list.
	current, err := l.c.Subscriptions.Get(id)
	if err != nil || current == nil {
		return mcpsrv.SingboxSubscription{}, nil, fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
	}
	// Read outside the service's per-subscription lock: Update reads the
	// value again under it and does not return what it saw. If another
	// client switches the subscription between the two reads, the
	// warnings below describe a change that did not happen, or miss one
	// that did. The record returned is right either way.
	changed := current.Enabled != enabled
	label := sanitizeLabel(current.Label)

	updated, err := l.c.Subscriptions.Update(id, subscription.UpdatePatch{Enabled: &enabled})
	if err != nil {
		// The cause goes neither to the model nor into this line: it is
		// free text that can quote the subscription's address or its
		// content. The service journals it itself, under singbox/runtime
		// (subscription.Service.SetAppLogger); this line only marks the
		// attempt as MCP's.
		l.subLog.Warn("subscription-update", label, "Failed to switch subscription "+onOff(enabled)+" (MCP); the service journalled the cause in bucket singbox")
		return mcpsrv.SingboxSubscription{}, nil, l.switchFailure(id, current.Enabled, enabled)
	}
	if updated == nil {
		return mcpsrv.SingboxSubscription{}, nil, fmt.Errorf("subscription update returned no record")
	}
	l.subLog.Info("subscription-update", label, "Subscription switched "+onOff(enabled)+" (MCP)")

	var warnings []string
	if changed {
		// resolveGroupTags skips a disabled subscription, so every enabled
		// aggregate group that lists this one just changed its members; one
		// left with no member has no outbound at all (subscription/groups.go),
		// so the warning points at the list, not at the group.
		for _, g := range l.c.Subscriptions.ListGroups() {
			if g.Enabled && slices.Contains(g.UseSubscriptionIDs, id) {
				warnings = append(warnings, fmt.Sprintf("aggregate group %q (%s) lists this subscription, so its set of servers may have changed. If no enabled subscription is left in it, the group is gone from sing-box — list_singbox_outbounds shows what is there now", sanitizeLabel(g.Label), g.Tag))
			}
		}
	}
	return singboxSubscription(updated), warnings, nil
}

// switchFailure says what state a failed switch left behind. It reads the
// subscription again rather than assume: the service restores the previous
// value when applying fails, but that restore can fail too
// (subscription.Service.Update, "rollback"), and the subscription can be
// deleted between our read and the write.
func (l *Local) switchFailure(id string, before, wanted bool) error {
	const where = `The cause is in the journal — get_logs with bucket "singbox"`
	after, err := l.c.Subscriptions.Get(id)
	switch {
	case err != nil || after == nil:
		return fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
	case after.Enabled != before:
		return fmt.Errorf("the subscription is now STORED as %s, but applying that to sing-box failed and the previous value could not be restored. The stored setting and the running engine disagree, and the stored one takes effect when sing-box next reloads. Tell the user. %s", onOff(after.Enabled), where)
	}
	return fmt.Errorf("the subscription could not be switched %s and is unchanged: it is still %s. %s", onOff(wanted), onOff(before), where)
}

// SetSingboxSubscriptionMode switches a subscription's group between
// selector and urltest. Only Mode is sent, for the reason
// SetSingboxSubscriptionEnabled gives; the urltest tuning stays as stored,
// and a subscription that never had any gets the service's defaults
// (Subscription.EffectiveURLTest).
//
// A call that changes nothing sends no patch at all: the service treats any
// patch carrying Mode as a mode change and rebuilds the group with a reload
// (subscription.Service.Update, "modeChanged"), which would interrupt
// connections for nothing.
//
// This is the one place the mode is validated: the tool only trims and
// lowercases it.
//
// No invalidation event is published, for the reason
// SetSingboxSubscriptionEnabled gives: the REST handler publishes none.
func (l *Local) SetSingboxSubscriptionMode(_ context.Context, id, mode string) (mcpsrv.SingboxSubscription, error) {
	if l.c.Subscriptions == nil {
		return mcpsrv.SingboxSubscription{}, errUnavailable("sing-box subscriptions")
	}
	var want subscription.SubscriptionMode
	switch mode {
	case "selector":
		want = subscription.ModeSelector
	case "urltest":
		want = subscription.ModeURLTest
	default:
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("mode must be selector or urltest")
	}
	current, err := l.c.Subscriptions.Get(id)
	if err != nil || current == nil {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
	}
	if current.EffectiveMode() == want {
		return singboxSubscription(current), nil
	}
	label := sanitizeLabel(current.Label)

	updated, err := l.c.Subscriptions.Update(id, subscription.UpdatePatch{Mode: &want})
	if err != nil {
		// The cause stays behind for the reason SetSingboxSubscriptionEnabled
		// gives: the service journals it under singbox/runtime.
		l.subLog.Warn("subscription-update", label, "Failed to switch subscription mode to "+mode+" (MCP); the service journalled the cause in bucket singbox")
		return mcpsrv.SingboxSubscription{}, l.modeFailure(id, current.EffectiveMode(), want)
	}
	if updated == nil {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("subscription update returned no record")
	}
	l.subLog.Info("subscription-update", label, "Subscription mode switched to "+mode+" (MCP)")
	return singboxSubscription(updated), nil
}

// modeFailure says what state a failed mode switch left behind, reading the
// subscription again for the reasons switchFailure gives.
func (l *Local) modeFailure(id string, before, wanted subscription.SubscriptionMode) error {
	const where = `The cause is in the journal — get_logs with bucket "singbox"`
	after, err := l.c.Subscriptions.Get(id)
	switch {
	case err != nil || after == nil:
		return fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
	case after.EffectiveMode() != before:
		return fmt.Errorf("the subscription is now STORED in %s mode, but applying that to sing-box failed and the previous mode could not be restored. The stored setting and the running engine disagree, and the stored one takes effect when sing-box next reloads. Tell the user. %s", after.EffectiveMode(), where)
	}
	return fmt.Errorf("the subscription could not be switched to %s mode and is unchanged: it is still in %s mode. %s", wanted, before, where)
}

// SetSingboxSubscriptionActiveMember points a selector-mode subscription at
// one of its servers. The service checks the mode and the membership too,
// under its lock; they are checked here first so that a refusal names the
// tool that gets the agent out of it instead of quoting the service.
//
// The service writes the store first and then switches the running group
// through the Clash API (subscription.Service.SetActiveMember), so a failure
// can leave the choice stored and not applied; activeMemberFailure tells the
// two apart.
//
// The record returned is the one read before the switch. SingboxSubscription
// carries no active server (see its doc), so the switch changes none of its
// fields, and a second read would only add a way to fail after the switch
// succeeded.
//
// No invalidation event is published, for the reason
// SetSingboxSubscriptionEnabled gives: the REST handler publishes none.
func (l *Local) SetSingboxSubscriptionActiveMember(ctx context.Context, id, memberTag string) (mcpsrv.SingboxSubscription, error) {
	if l.c.Subscriptions == nil {
		return mcpsrv.SingboxSubscription{}, errUnavailable("sing-box subscriptions")
	}
	current, err := l.c.Subscriptions.Get(id)
	if err != nil || current == nil {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
	}
	if current.EffectiveMode() == subscription.ModeURLTest {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("the subscription is in urltest mode: sing-box picks its server itself and a server cannot be chosen by hand. Switch it to selector with set_singbox_subscription_mode first")
	}
	// MemberTags holds exactly the servers of the group: the ones the user
	// excluded and the ones a filter hides are not written there
	// (subscription.Service, applyDiff).
	if !slices.Contains(current.MemberTags, memberTag) {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("%q is not a server of this subscription's group (get_singbox_outbound with tag %q lists them)", memberTag, current.SelectorTag)
	}
	label := sanitizeLabel(current.Label)

	if err := l.c.Subscriptions.SetActiveMember(ctx, id, memberTag); err != nil {
		return mcpsrv.SingboxSubscription{}, l.activeMemberFailure(id, label, memberTag, current.ActiveMember)
	}
	l.subLog.Info("subscription-active-member", label, "Active server chosen (MCP)")
	return singboxSubscription(current), nil
}

// activeMemberFailure says what a failed choice left behind, and journals
// the attempt. The service stores the choice before it switches the running
// group and journals a failed switch itself; anything that fails before the
// store write leaves the subscription as it was and is journalled by no one
// but this line.
//
// A stored value equal to the request proves a write only when the request
// was not the active server already (before): otherwise a refusal before the
// write would read as a write.
func (l *Local) activeMemberFailure(id, label, memberTag, before string) error {
	after, err := l.c.Subscriptions.Get(id)
	stored := err == nil && after != nil && after.ActiveMember == memberTag && before != memberTag
	if stored {
		l.subLog.Warn("subscription-active-member", label, "Failed to switch the running group to the chosen server (MCP); the choice is stored; the service journalled the cause in bucket singbox")
	} else {
		l.subLog.Warn("subscription-active-member", label, "Failed to choose the active server (MCP)")
	}
	switch {
	case err != nil || after == nil:
		return fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
	case stored:
		return fmt.Errorf("the server is STORED as the active one, but switching the running sing-box failed, so the group may still route through the previous server. The stored choice is applied when the subscription's group is next rebuilt, for example by a refresh; a sing-box restart before that starts from the previous server. Tell the user. The cause is in the journal — get_logs with bucket \"singbox\"")
	}
	return fmt.Errorf("the server could not be chosen and the subscription is unchanged. A refresh or another client may have changed the subscription meanwhile: check it with list_singbox_subscriptions and get_singbox_outbound, then try again")
}
