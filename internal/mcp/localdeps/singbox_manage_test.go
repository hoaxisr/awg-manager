package localdeps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

// fakeRouterRefs reports tags a rule still targets, as router.Service does.
type fakeRouterRefs map[string][]int

func (f fakeRouterRefs) RulesReferencing(tag string) []int          { return f[tag] }
func (f fakeRouterRefs) OutboundReferenceLocations(string) []string { return nil }

// TestLocal_CreateSingboxSubscription — подписка создаётся включённой, с
// режимом по умолчанию selector; текст сервиса о неудачном скачивании не
// уходит агенту — он может цитировать адрес с токеном.
func TestLocal_CreateSingboxSubscription(t *testing.T) {
	subs := subsHarness()
	journal := &recLog{}
	l := New(Config{Subscriptions: subs, Singbox: singboxHarness(), AppLog: journal})
	ctx := context.Background()

	got, err := l.CreateSingboxSubscription(ctx, mcpsrv.SingboxSubscriptionInput{Label: "New", URL: "https://sub.example.net/u/TOKEN", RefreshHours: 6})
	if err != nil {
		t.Fatal(err)
	}
	if in := subs.createdIn; !in.Enabled || in.Mode != subscription.ModeSelector || in.URL != "https://sub.example.net/u/TOKEN" || in.RefreshHours != 6 {
		t.Fatalf("create input = %+v", in)
	}
	if got.Host != "sub.example.net" || strings.Contains(fmt.Sprint(got), "TOKEN") {
		t.Fatalf("only the host may come back: %+v", got)
	}

	subs.mgmtErr = errors.New(`initial fetch failed: Get "https://sub.example.net/u/TOKEN": refused`)
	_, err = l.CreateSingboxSubscription(ctx, mcpsrv.SingboxSubscriptionInput{Label: "x", URL: "https://sub.example.net/u/TOKEN"})
	if err == nil || strings.Contains(err.Error(), "TOKEN") || strings.Contains(strings.Join(journal.lines, "\n"), "TOKEN") {
		t.Fatalf("the service's text must reach neither the agent nor the journal: %v / %v", err, journal.lines)
	}
	subs.mgmtErr = &subscription.FilterError{Msg: "error parsing regexp: missing closing ): `(a`"}
	if _, err := l.CreateSingboxSubscription(ctx, mcpsrv.SingboxSubscriptionInput{Label: "x", URL: "https://a.example/l", FilterInclude: "(a"}); err == nil || !strings.Contains(err.Error(), "missing closing") {
		t.Fatalf("a bad filter is the agent's own text and is passed on: %v", err)
	}
	subs.mgmtErr = fmt.Errorf("apply: %w", subscription.ErrAllMembersFiltered)
	if _, err := l.CreateSingboxSubscription(ctx, mcpsrv.SingboxSubscriptionInput{Label: "x", URL: "https://a.example/l", FilterExclude: "."}); err == nil || !strings.Contains(err.Error(), "filter hides every server") {
		t.Fatalf("a filter hiding every server is the agent's to loosen: %v", err)
	}
	if _, err := l.CreateSingboxSubscription(ctx, mcpsrv.SingboxSubscriptionInput{Label: "x", URL: "https://a.example/l", Mode: "loadbalance"}); err == nil {
		t.Fatal("an unknown mode must be refused")
	}

	op := singboxHarness()
	op.notInstalled = true
	subs.mgmtErr, subs.createdIn = nil, nil
	l = New(Config{Subscriptions: subs, Singbox: op, AppLog: journal})
	if _, err := l.CreateSingboxSubscription(ctx, mcpsrv.SingboxSubscriptionInput{Label: "x", URL: "https://a.example/l"}); err == nil || subs.createdIn != nil {
		t.Fatalf("without sing-box the service must not be asked: %v", err)
	}
}

// TestLocal_DeleteSingboxSubscriptionChecksReferences — как в REST: группу
// или сервер подписки ещё использует правило — удаление отклоняется, и
// сервис не вызывается.
func TestLocal_DeleteSingboxSubscriptionChecksReferences(t *testing.T) {
	subs := subsHarness()
	refs := fakeRouterRefs{"sub-706dcf33-b2": {4}}
	journal := &recLog{}
	l := New(Config{Subscriptions: subs, RouterRefs: refs, AppLog: journal})
	ctx := context.Background()

	_, err := l.DeleteSingboxSubscription(ctx, subAutoID)
	if err == nil || !strings.Contains(err.Error(), "1 router rule") || subs.deletedID != "" {
		t.Fatalf("a referenced subscription must be refused before the service: %v, deleted=%q", err, subs.deletedID)
	}
	deleted, err := l.DeleteSingboxSubscription(ctx, subPasteID)
	if err != nil || subs.deletedID != subPasteID || deleted.ID != subPasteID {
		t.Fatalf("an unused subscription must be deleted and returned: %+v, %v", deleted, err)
	}
	if _, err := l.DeleteSingboxSubscription(ctx, "deadbeefdeadbeefdeadbeef"); err == nil || !strings.Contains(err.Error(), "list_singbox_subscriptions") {
		t.Fatalf("err = %v", err)
	}
	subs.mgmtErr = errors.New("subscription: delete reload: sing-box check failed")
	if _, err := l.DeleteSingboxSubscription(ctx, subPasteID); err == nil || !strings.Contains(err.Error(), "get_logs") ||
		!strings.Contains(strings.Join(journal.lines, "\n"), "delete reload") {
		t.Fatalf("a failed delete sends the agent to the journal, and the cause is there: %v / %v", err, journal.lines)
	}
}

// TestLocal_RefreshSingboxSubscription — счётчики доходят, тексты ошибок
// разбора — нет: они цитируют список провайдера.
func TestLocal_RefreshSingboxSubscription(t *testing.T) {
	subs := subsHarness()
	l := New(Config{Subscriptions: subs, AppLog: &recLog{}})

	got, err := l.RefreshSingboxSubscription(context.Background(), subAutoID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Added != 2 || got.Updated != 1 || got.Orphaned != 1 || got.Skipped != 3 || got.ParseErrors != 1 || got.MemberCount != 3 {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(fmt.Sprint(got), "SECRET") {
		t.Fatal("parse messages must not be returned")
	}
	subs.mgmtErr = errors.New("fetch https://sub.example.net/api/v1/TOKEN123: 500")
	if _, err := l.RefreshSingboxSubscription(context.Background(), subAutoID); err == nil || strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestLocal_UpdateSingboxSubscriptionSettingsSendsOnlyWhatChanged(t *testing.T) {
	subs := subsHarness()
	l := New(Config{Subscriptions: subs, AppLog: &recLog{}})
	hours := 24
	if _, err := l.UpdateSingboxSubscriptionSettings(context.Background(), mcpsrv.SingboxSubscriptionPatch{ID: subAutoID, RefreshHours: &hours}); err != nil {
		t.Fatal(err)
	}
	if p := subs.patched; p.RefreshHours == nil || *p.RefreshHours != 24 || p.Enabled != nil || p.Mode != nil || p.URL != nil || p.FilterInclude != nil || p.Label != nil {
		t.Fatalf("only the field sent may be in the patch: %+v", p)
	}
}

func TestLocal_ExcludeRestoreSingboxSubscriptionServers(t *testing.T) {
	subs := subsHarness()
	l := New(Config{Subscriptions: subs, AppLog: &recLog{}})
	ctx := context.Background()

	if _, err := l.ExcludeSingboxSubscriptionServers(ctx, subAutoID, []string{"sub-706dcf33-a1"}); err != nil || len(subs.excluded) != 1 {
		t.Fatalf("exclude: %v, %v", subs.excluded, err)
	}
	if _, err := l.RestoreSingboxSubscriptionServers(ctx, subAutoID, []string{"sub-706dcf33-x9"}); err != nil || len(subs.restored) != 1 {
		t.Fatalf("restore: %v, %v", subs.restored, err)
	}
	journal := &recLog{}
	l = New(Config{Subscriptions: subs, AppLog: journal})
	subs.mgmtErr = errors.New("rebuild group outbound: sing-box check failed")
	if _, err := l.ExcludeSingboxSubscriptionServers(ctx, subAutoID, []string{"x"}); err == nil || !strings.Contains(strings.Join(journal.lines, "\n"), "rebuild group outbound") {
		t.Fatalf("an unmapped failure is journalled with its cause: %v / %v", err, journal.lines)
	}
	for svcErr, want := range map[error]string{
		subscription.ErrExcludeOnInline:    "fetched from a URL",
		subscription.ErrAllMembersExcluded: "no server",
		subscription.ErrMemberNotFound:     "get_singbox_outbound",
	} {
		subs.mgmtErr = svcErr
		if _, err := l.ExcludeSingboxSubscriptionServers(ctx, subAutoID, []string{"x"}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v → %v, want it to mention %q", svcErr, err, want)
		}
	}
}

// TestLocal_AddSingboxTunnel — ссылка разбирается тем же парсером, что в
// REST; ссылка не попадает ни в ответ, ни в отказ, ни в журнал.
func TestLocal_AddSingboxTunnel(t *testing.T) {
	op := singboxHarness()
	journal := &recLog{}
	l := New(Config{Singbox: op, AppLog: journal})
	ctx := context.Background()
	const link = "vless://11111111-2222-3333-4444-555555555555@n.example.net:443?security=tls&sni=n.example.net&type=tcp#SECRETNAME"

	got, warnings, err := l.AddSingboxTunnel(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "new-proxy" || len(op.addedLinks) != 1 || op.addedLinks[0] != link || len(warnings) != 0 {
		t.Fatalf("got %+v, links %v, warnings %v", got, op.addedLinks, warnings)
	}
	op.addProxyErr = true
	if got, warnings, err = l.AddSingboxTunnel(ctx, link); err != nil || got.Tag != "new-proxy" || len(warnings) != 1 {
		t.Fatalf("a proxy added with its ProxyN setup failed is added, with a warning: %+v, %v, %v", got, warnings, err)
	}
	op.addProxyErr, op.addErrs = false, true
	if _, _, err := l.AddSingboxTunnel(ctx, link); err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(strings.Join(journal.lines, "\n"), "SECRET") {
		t.Fatalf("the link must not leak: %v / %v", err, journal.lines)
	}
	op.addErrs, op.addDup = false, true
	if _, _, err := l.AddSingboxTunnel(ctx, link); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("err = %v", err)
	}
	calls := len(op.addedLinks)
	if _, _, err := l.AddSingboxTunnel(ctx, "not a link at all"); err == nil || len(op.addedLinks) != calls {
		t.Fatalf("an unparsable link must be refused before the operator: %v", err)
	}
	bound := "vless://11111111-2222-3333-4444-555555555555@n.example.net:443?security=tls&sni=n.example.net&type=tcp&bind_interface=ppp0#x"
	if _, _, err := l.AddSingboxTunnel(ctx, bound); err == nil || !strings.Contains(err.Error(), "interface") || len(op.addedLinks) != calls {
		t.Fatalf("an interface binding must be refused before the operator: %v", err)
	}
}

func TestLocal_DeleteSingboxTunnelChecksReferences(t *testing.T) {
	op := singboxHarness()
	l := New(Config{Singbox: op, RouterRefs: fakeRouterRefs{"vless-nl": {0, 2}}, AppLog: &recLog{}})
	ctx := context.Background()

	if _, err := l.DeleteSingboxTunnel(ctx, "vless-nl"); err == nil || !strings.Contains(err.Error(), "2 router rule") || len(op.removed) != 0 {
		t.Fatalf("a referenced proxy must be refused: %v, removed %v", err, op.removed)
	}
	deleted, err := l.DeleteSingboxTunnel(ctx, "hy2-de")
	if err != nil || deleted.Tag != "hy2-de" || len(op.removed) != 1 {
		t.Fatalf("deleted = %+v, %v", deleted, err)
	}
	if _, err := l.DeleteSingboxTunnel(ctx, "ghost"); err == nil || !strings.Contains(err.Error(), "list_singbox_tunnels") {
		t.Fatalf("err = %v", err)
	}
}
