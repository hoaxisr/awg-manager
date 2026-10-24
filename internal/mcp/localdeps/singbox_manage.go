package localdeps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/events"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// Subscription and proxy management for MCP. The service's error text is
// not passed on unless it is ours to begin with: a failed fetch or parse
// quotes the provider's list or the pasted links, a URL carries a token,
// and a share link carries credentials. A refused filter is the agent's
// own regular expression and is passed on; so is a refusal for references,
// which names only counts.

// ensureSingboxInstalled mirrors the REST handler's 412: without the
// binary the first fetch would fail with a reason the agent cannot act on.
func (l *Local) ensureSingboxInstalled(ctx context.Context) error {
	if l.c.Singbox == nil {
		return nil
	}
	if st := l.c.Singbox.GetStatus(ctx); !st.Installed {
		return fmt.Errorf("sing-box is not installed on this router; install it in the web interface first")
	}
	return nil
}

func (l *Local) CreateSingboxSubscription(ctx context.Context, in mcpsrv.SingboxSubscriptionInput) (mcpsrv.SingboxSubscription, error) {
	if l.c.Subscriptions == nil {
		return mcpsrv.SingboxSubscription{}, errUnavailable("sing-box subscriptions")
	}
	var mode subscription.SubscriptionMode
	switch in.Mode {
	case "", "selector":
		mode = subscription.ModeSelector
	case "urltest":
		mode = subscription.ModeURLTest
	case "fallback":
		mode = subscription.ModeFallback
	default:
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("mode must be selector, urltest or fallback")
	}
	if err := l.ensureSingboxInstalled(ctx); err != nil {
		return mcpsrv.SingboxSubscription{}, err
	}
	label := sanitizeLabel(in.Label)
	created, err := l.c.Subscriptions.Create(ctx, subscription.CreateInput{
		Label: in.Label, URL: in.URL, Inline: in.Links, RefreshHours: in.RefreshHours, Enabled: true, Mode: mode,
		FilterInclude: in.FilterInclude, FilterExclude: in.FilterExclude,
	})
	if err != nil {
		var filterErr *subscription.FilterError
		if errors.As(err, &filterErr) {
			return mcpsrv.SingboxSubscription{}, fmt.Errorf("the filter is not a valid regular expression: %s", sanitizeText(filterErr.Msg, maxMessageRunes))
		}
		if errors.Is(err, subscription.ErrAllMembersFiltered) {
			return mcpsrv.SingboxSubscription{}, fmt.Errorf("the subscription was not created: the filter hides every server it lists — loosen filterInclude or filterExclude")
		}
		l.subLog.Warn("subscription-create", label, "Failed to create subscription (MCP); the cause is not journalled here because it can quote the address or the links")
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("the subscription was not created and nothing was left behind: the list could not be fetched or held no server sing-box can use. Adding it in the web interface shows why")
	}
	if created == nil {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("subscription create returned no record")
	}
	l.subLog.Info("subscription-create", label, "Subscription created (MCP)")
	return singboxSubscription(created), nil
}

// subscriptionTags lists the outbound tags of a subscription that a rule,
// a group or the device proxy may point at: its group and its servers.
func subscriptionTags(sub *subscription.Subscription) []string {
	tags := make([]string, 0, 1+len(sub.MemberTags))
	if sub.SelectorTag != "" {
		tags = append(tags, sub.SelectorTag)
	}
	return append(tags, sub.MemberTags...)
}

// refusedForReferences runs the REST handlers' check
// (service.CheckOutboundTagsReferenced): deleting an outbound that a rule
// or the device proxy names would leave sing-box a configuration it
// refuses to start with.
func (l *Local) refusedForReferences(displayID string, tags []string) error {
	err := service.CheckOutboundTagsReferenced(displayID, tags, l.c.DeviceProxyRefs, l.c.RouterRefs)
	var refErr service.ErrTunnelReferenced
	if errors.As(err, &refErr) {
		return fmt.Errorf("%s — retarget those rules and groups first (list_singbox_rules, list_singbox_outbounds)", refErr.Error())
	}
	return err
}

func (l *Local) DeleteSingboxSubscription(ctx context.Context, id string) (mcpsrv.SingboxSubscription, error) {
	if l.c.Subscriptions == nil {
		return mcpsrv.SingboxSubscription{}, errUnavailable("sing-box subscriptions")
	}
	current, err := l.c.Subscriptions.Get(id)
	if err != nil || current == nil {
		return mcpsrv.SingboxSubscription{}, errSubscriptionNotFound(id)
	}
	label := sanitizeLabel(current.Label)
	if err := l.refusedForReferences(id, subscriptionTags(current)); err != nil {
		l.subLog.Info("subscription-delete", label, "Refused (MCP): still referenced")
		return mcpsrv.SingboxSubscription{}, err
	}
	if err := l.c.Subscriptions.Delete(ctx, id); err != nil {
		// The cause names tags and sing-box's check output, not the
		// address; the journal cuts any URL to its host all the same.
		l.subLog.Warn("subscription-delete", label, "Failed to delete subscription (MCP): "+err.Error())
		return mcpsrv.SingboxSubscription{}, fmt.Errorf(`the subscription could not be deleted; the cause is in the journal — get_logs with bucket "singbox"`)
	}
	l.subLog.Info("subscription-delete", label, "Subscription deleted (MCP)")
	return singboxSubscription(current), nil
}

func (l *Local) RefreshSingboxSubscription(ctx context.Context, id string) (mcpsrv.SingboxRefreshResult, error) {
	if l.c.Subscriptions == nil {
		return mcpsrv.SingboxRefreshResult{}, errUnavailable("sing-box subscriptions")
	}
	current, err := l.c.Subscriptions.Get(id)
	if err != nil || current == nil {
		return mcpsrv.SingboxRefreshResult{}, errSubscriptionNotFound(id)
	}
	label := sanitizeLabel(current.Label)
	res, err := l.c.Subscriptions.Refresh(ctx, id)
	if err != nil || res == nil {
		l.subLog.Warn("subscription-refresh", label, "Refresh failed (MCP); the service journalled the cause in bucket singbox")
		return mcpsrv.SingboxRefreshResult{}, fmt.Errorf(`the subscription could not be fetched; its servers are as they were. The cause is in the journal — get_logs with bucket "singbox" — and list_singbox_subscriptions shows the failure kind`)
	}
	out := mcpsrv.SingboxRefreshResult{
		SubscriptionID: id, Added: res.Added, Updated: res.Updated, Orphaned: res.Orphaned,
		Skipped: res.SkippedVmess + res.SkippedOther + res.SkippedDuplicate, ParseErrors: len(res.ParseErrors),
	}
	if after, err := l.c.Subscriptions.Get(id); err == nil && after != nil {
		out.MemberCount = len(after.MemberTags)
	}
	l.subLog.Info("subscription-refresh", label, fmt.Sprintf("Subscription refreshed (MCP): +%d ~%d -%d", res.Added, res.Updated, res.Orphaned))
	return out, nil
}

func (l *Local) UpdateSingboxSubscriptionSettings(_ context.Context, p mcpsrv.SingboxSubscriptionPatch) (mcpsrv.SingboxSubscription, error) {
	if l.c.Subscriptions == nil {
		return mcpsrv.SingboxSubscription{}, errUnavailable("sing-box subscriptions")
	}
	current, err := l.c.Subscriptions.Get(p.ID)
	if err != nil || current == nil {
		return mcpsrv.SingboxSubscription{}, errSubscriptionNotFound(p.ID)
	}
	label := sanitizeLabel(current.Label)
	updated, err := l.c.Subscriptions.Update(p.ID, subscription.UpdatePatch{
		Label: p.Label, RefreshHours: p.RefreshHours, FilterInclude: p.FilterInclude, FilterExclude: p.FilterExclude,
	})
	if err != nil {
		var filterErr *subscription.FilterError
		switch {
		case errors.As(err, &filterErr):
			return mcpsrv.SingboxSubscription{}, fmt.Errorf("the filter is not a valid regular expression: %s", sanitizeText(filterErr.Msg, maxMessageRunes))
		case errors.Is(err, subscription.ErrAllMembersFiltered):
			return mcpsrv.SingboxSubscription{}, fmt.Errorf("the filter would hide every server of the subscription; nothing was changed — loosen it")
		}
		l.subLog.Warn("subscription-update", label, "Failed to change subscription settings (MCP); the service journalled the cause in bucket singbox")
		return mcpsrv.SingboxSubscription{}, fmt.Errorf(`the settings could not be applied and were restored; the cause is in the journal — get_logs with bucket "singbox"`)
	}
	if updated == nil {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("subscription update returned no record")
	}
	l.subLog.Info("subscription-update", label, "Subscription settings changed (MCP)")
	return singboxSubscription(updated), nil
}

// memberEditError maps the exclude and restore refusals to the step that
// resolves them.
func memberEditError(err error, groupTag string) error {
	switch {
	case errors.Is(err, subscription.ErrExcludeOnInline):
		return fmt.Errorf("only a subscription fetched from a URL has servers to exclude; a pasted one is edited in the web interface")
	case errors.Is(err, subscription.ErrAllMembersExcluded):
		return fmt.Errorf("that would leave the group with no server; nothing was changed")
	case errors.Is(err, subscription.ErrMemberNotFound):
		return fmt.Errorf("a tag is not a server of this subscription (get_singbox_outbound with tag %q lists them)", groupTag)
	}
	return nil
}

func (l *Local) ExcludeSingboxSubscriptionServers(ctx context.Context, id string, tags []string) (mcpsrv.SingboxSubscription, error) {
	return l.editMembers(ctx, id, tags, "exclude", l.excludeMembers)
}

func (l *Local) RestoreSingboxSubscriptionServers(ctx context.Context, id string, tags []string) (mcpsrv.SingboxSubscription, error) {
	return l.editMembers(ctx, id, tags, "restore", l.restoreMembers)
}

func (l *Local) excludeMembers(ctx context.Context, id string, tags []string) (*subscription.Subscription, error) {
	return l.c.Subscriptions.ExcludeMembers(ctx, id, tags)
}

func (l *Local) restoreMembers(ctx context.Context, id string, tags []string) (*subscription.Subscription, error) {
	return l.c.Subscriptions.RestoreMembers(ctx, id, tags)
}

func (l *Local) editMembers(ctx context.Context, id string, tags []string, verb string,
	edit func(context.Context, string, []string) (*subscription.Subscription, error)) (mcpsrv.SingboxSubscription, error) {
	if l.c.Subscriptions == nil {
		return mcpsrv.SingboxSubscription{}, errUnavailable("sing-box subscriptions")
	}
	current, err := l.c.Subscriptions.Get(id)
	if err != nil || current == nil {
		return mcpsrv.SingboxSubscription{}, errSubscriptionNotFound(id)
	}
	label := sanitizeLabel(current.Label)
	updated, err := edit(ctx, id, tags)
	if err != nil {
		if mapped := memberEditError(err, current.SelectorTag); mapped != nil {
			return mcpsrv.SingboxSubscription{}, mapped
		}
		l.subLog.Warn("subscription-"+verb, label, "Failed to "+verb+" servers (MCP): "+err.Error())
		return mcpsrv.SingboxSubscription{}, fmt.Errorf(`the servers could not be changed; the cause is in the journal — get_logs with bucket "singbox"`)
	}
	if updated == nil {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("the subscription service returned no record")
	}
	l.subLog.Info("subscription-"+verb, label, fmt.Sprintf("%d servers %sd (MCP)", len(tags), verb))
	return singboxSubscription(updated), nil
}

// AddSingboxTunnel parses the link the way the REST handler does and
// refuses what that handler checks with dependencies MCP does not hold: a
// bind_interface (validated against NDMS there; a link may carry one in
// its query). A multi-address TrustTunnel config parses to one outbound per
// address, so the one-link check already refuses it.
//
// The operator's errors are judged as the REST handler does, by what was
// added: a proxy whose NDMS ProxyN setup failed stays in the configuration
// and comes back in added with a BatchError beside it — it is added, with
// a warning. A duplicate comes back as a BatchError and nothing added.
func (l *Local) AddSingboxTunnel(ctx context.Context, shareLink string) (mcpsrv.SingboxTunnel, []string, error) {
	if l.c.Singbox == nil {
		return mcpsrv.SingboxTunnel{}, nil, errUnavailable("sing-box")
	}
	parsed := singbox.ParseTunnelLinksInput(shareLink)
	if len(parsed.Outbounds) != 1 {
		return mcpsrv.SingboxTunnel{}, nil, fmt.Errorf("shareLink is not one proxy share link sing-box can use; nothing was added")
	}
	p := parsed.Outbounds[0]
	var peek struct {
		BindInterface string `json:"bind_interface"`
	}
	_ = json.Unmarshal(p.Outbound, &peek)
	if peek.BindInterface != "" {
		return mcpsrv.SingboxTunnel{}, nil, fmt.Errorf("this link binds the proxy to an interface, which MCP does not check; add it in the web interface")
	}
	added, errs, err := l.c.Singbox.AddTunnels(ctx, shareLink)
	if len(added) > 0 {
		t := added[0]
		var warnings []string
		if err != nil || len(errs) > 0 {
			l.singboxLog.Warn("single-add", t.Tag, "Proxy added, but part of its setup failed (MCP); the operator journalled the cause")
			warnings = append(warnings, "the proxy was added, but part of its setup failed (most likely its Keenetic ProxyN interface): check it in the web interface")
		}
		l.singboxLog.Info("single-add", t.Tag, "Proxy added (MCP)")
		l.publish(events.ResourceSingboxTunnels, "mcp-tunnel-added")
		return singboxTunnel(t), warnings, nil
	}
	if err == nil && slices.ContainsFunc(errs, func(e singbox.BatchError) bool {
		return e.Err != nil && strings.HasPrefix(e.Err.Error(), "duplicate of existing tunnel")
	}) {
		return mcpsrv.SingboxTunnel{}, nil, fmt.Errorf("this proxy is already configured (list_singbox_tunnels); nothing was added")
	}
	l.singboxLog.Warn("single-add", "", "Failed to add a proxy (MCP); the cause is not journalled here because it can quote the link")
	return mcpsrv.SingboxTunnel{}, nil, fmt.Errorf("the proxy was not added: sing-box cannot use this link or could not apply it (an unsupported protocol, a setting this build lacks, or a configuration sing-box refused)")
}

func (l *Local) DeleteSingboxTunnel(ctx context.Context, tag string) (mcpsrv.SingboxTunnel, error) {
	if l.c.Singbox == nil {
		return mcpsrv.SingboxTunnel{}, errUnavailable("sing-box")
	}
	list, err := l.c.Singbox.ListTunnels(ctx)
	if err != nil {
		return mcpsrv.SingboxTunnel{}, err
	}
	i := slices.IndexFunc(list, func(t singbox.TunnelInfo) bool { return t.Tag == tag })
	if i < 0 {
		return mcpsrv.SingboxTunnel{}, fmt.Errorf("sing-box proxy %q not found (use list_singbox_tunnels)", tag)
	}
	if err := l.refusedForReferences(tag, []string{tag}); err != nil {
		l.singboxLog.Info("single-remove", tag, "Refused (MCP): still referenced")
		return mcpsrv.SingboxTunnel{}, err
	}
	if err := l.c.Singbox.RemoveTunnel(ctx, tag); err != nil {
		l.singboxLog.Warn("single-remove", tag, "Failed to remove proxy (MCP): "+err.Error())
		return mcpsrv.SingboxTunnel{}, fmt.Errorf(`the proxy could not be removed; the cause is in the journal — get_logs with bucket "singbox"`)
	}
	l.singboxLog.Info("single-remove", tag, "Proxy removed (MCP)")
	l.publish(events.ResourceSingboxTunnels, "mcp-tunnel-removed")
	return singboxTunnel(list[i]), nil
}
