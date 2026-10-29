package subscription

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveLinks поднимает httptest-фид с mutable-телом (для проверки refresh
// после смены фида).
func serveLinks(t *testing.T, body *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(*body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// namedLinks строит фид из vless share-link'ов с заданными именами (фрагмент).
func namedLinks(labels ...string) string {
	var b strings.Builder
	for i, label := range labels {
		fmt.Fprintf(&b, "vless://3a3b1c2e-9999-4321-aaaa-12345678%02d%02d@h%d.example:443?security=tls&sni=h#%s\n", i, i, i, label)
	}
	return b.String()
}

func memberLabels(members []MemberInfo) []string {
	out := make([]string, len(members))
	for i, m := range members {
		out[i] = m.Label
	}
	return out
}

func TestService_Create_FilterExcludeHidesMembers(t *testing.T) {
	svc, mut := newTestService(t)
	body := namedLinks("DE-Berlin", "RU-Moscow", "NL-Amsterdam")
	srv := serveLinks(t, &body)

	sub, err := svc.Create(context.Background(), CreateInput{
		Label: "x", URL: srv.URL, Enabled: true,
		FilterExclude: "(?i)ru",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.Members) != 2 {
		t.Fatalf("Members=%v want 2 (RU hidden)", memberLabels(sub.Members))
	}
	for _, m := range sub.Members {
		if strings.Contains(m.Label, "RU") {
			t.Errorf("filtered member %q must not be in Members", m.Label)
		}
	}
	if len(sub.FilteredMembers) != 1 || sub.FilteredMembers[0].Label != "RU-Moscow" {
		t.Fatalf("FilteredMembers=%v want [RU-Moscow]", memberLabels(sub.FilteredMembers))
	}
	filteredTag := sub.FilteredMembers[0].Tag
	if containsTag(sub.MemberTags, filteredTag) {
		t.Errorf("filtered tag %s must not be in MemberTags", filteredTag)
	}
	// Selector не должен ссылаться на скрытый сервер, а его outbound —
	// не материализоваться.
	if containsTag(selectorMembers(t, mut, sub.SelectorTag), filteredTag) {
		t.Errorf("selector must not reference filtered tag %s", filteredTag)
	}
	if mut.addedOutbound(filteredTag) {
		t.Errorf("filtered member outbound %s must not be added", filteredTag)
	}
}

func TestService_Create_InvalidFilterRejected(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Create(context.Background(), CreateInput{
		Label: "x", Inline: "vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@a.example:443#A",
		FilterInclude: "(broken",
	})
	if err == nil {
		t.Fatal("Create with invalid filter must fail")
	}
	if len(svc.List()) != 0 {
		t.Error("no subscription row must be created on validation failure")
	}
}

func TestService_Update_FilterChange_RemovesStoppedMatching(t *testing.T) {
	svc, mut := newTestService(t)
	mut.slot = map[string]bool{} // состав слота — чистка сирот снимает по нему (F761)
	body := namedLinks("DE-Berlin", "RU-Moscow")
	srv := serveLinks(t, &body)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "x", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.Members) != 2 {
		t.Fatalf("want 2 members, got %d", len(sub.Members))
	}
	var ruTag string
	for _, m := range sub.Members {
		if m.Label == "RU-Moscow" {
			ruTag = m.Tag
		}
	}
	if ruTag == "" {
		t.Fatal("RU-Moscow member not found")
	}

	mut.reset()
	exclude := "(?i)ru"
	updated, err := svc.Update(sub.ID, UpdatePatch{FilterExclude: &exclude})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// URL-подписка: смена фильтра триггерит refresh → сервер, попавший под
	// фильтр, снимается из конфига и переезжает в FilteredMembers.
	if !mut.removedOutbound(ruTag) {
		t.Errorf("outbound %s must be removed after filter update", ruTag)
	}
	if containsTag(updated.MemberTags, ruTag) {
		t.Errorf("MemberTags must not contain %s", ruTag)
	}
	if len(updated.FilteredMembers) != 1 || updated.FilteredMembers[0].Tag != ruTag {
		t.Fatalf("FilteredMembers=%v want [%s]", updated.FilteredMembers, ruTag)
	}
	if containsTag(selectorMembers(t, mut, sub.SelectorTag), ruTag) {
		t.Errorf("selector must not reference %s", ruTag)
	}

	// Обратная сторона: снятие фильтра возвращает сервер.
	mut.reset()
	empty := ""
	restored, err := svc.Update(sub.ID, UpdatePatch{FilterExclude: &empty})
	if err != nil {
		t.Fatalf("Update(clear): %v", err)
	}
	if !containsTag(restored.MemberTags, ruTag) {
		t.Errorf("clearing the filter must restore %s, MemberTags=%v", ruTag, restored.MemberTags)
	}
	if len(restored.FilteredMembers) != 0 {
		t.Errorf("FilteredMembers must be empty after clearing, got %v", restored.FilteredMembers)
	}
}

func TestService_Update_InvalidFilterRejected(t *testing.T) {
	svc, _ := newTestService(t)
	sub := createInlineSubWithTwoMembers(t, svc)

	bad := `(?i)^(?!.*(RU|Russia)).*$` // lookahead из issue — RE2 не умеет
	_, err := svc.Update(sub.ID, UpdatePatch{FilterInclude: &bad})
	if err == nil {
		t.Fatal("Update with lookahead filter must fail")
	}
	if !strings.Contains(err.Error(), "lookahead/lookbehind") {
		t.Errorf("error must be the targeted lookaround message, got: %v", err)
	}
	stored, _ := svc.Get(sub.ID)
	if stored.FilterInclude != "" {
		t.Errorf("invalid filter must not be persisted, got %q", stored.FilterInclude)
	}
}

func TestService_InlineSub_FilterChange_ReparsesInline(t *testing.T) {
	svc, mut := newTestService(t)
	mut.slot = map[string]bool{} // состав слота — чистка сирот снимает по нему (F761)
	sub, err := svc.Create(context.Background(), CreateInput{
		Label:  "x",
		Inline: "vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@a.example:443?security=tls&sni=a#Keep\nvless://3a3b1c2e-9999-4321-aaaa-1234567890a2@b.example:443?security=tls&sni=b#Hide-me",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.Members) != 2 {
		t.Fatalf("want 2 members, got %d", len(sub.Members))
	}
	var hideTag string
	for _, m := range sub.Members {
		if m.Label == "Hide-me" {
			hideTag = m.Tag
		}
	}

	mut.reset()
	exclude := "Hide"
	updated, err := svc.Update(sub.ID, UpdatePatch{FilterExclude: &exclude})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// Inline short-circuit обойдён: paste-тело перечитано, фильтр применён.
	if len(updated.Members) != 1 || updated.Members[0].Label != "Keep" {
		t.Fatalf("Members=%v want [Keep]", memberLabels(updated.Members))
	}
	if len(updated.FilteredMembers) != 1 || updated.FilteredMembers[0].Tag != hideTag {
		t.Fatalf("FilteredMembers=%v want tag %s", updated.FilteredMembers, hideTag)
	}
	if !mut.removedOutbound(hideTag) {
		t.Errorf("outbound %s must be removed", hideTag)
	}
}

func TestService_Refresh_FilterComposesWithExcludedTags(t *testing.T) {
	svc, mut := newTestService(t)
	body := namedLinks("Alpha", "Bravo", "Charlie")
	srv := serveLinks(t, &body)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "x", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var alphaTag string
	for _, m := range sub.Members {
		if m.Label == "Alpha" {
			alphaTag = m.Tag
		}
	}
	// Ручное исключение Alpha по тегу...
	if _, err := svc.ExcludeMembers(context.Background(), sub.ID, []string{alphaTag}); err != nil {
		t.Fatalf("ExcludeMembers: %v", err)
	}
	// ...плюс regex-фильтр на Bravo — механизмы композируются.
	mut.reset()
	exclude := "Bravo"
	updated, err := svc.Update(sub.ID, UpdatePatch{FilterExclude: &exclude})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.Members) != 1 || updated.Members[0].Label != "Charlie" {
		t.Fatalf("Members=%v want [Charlie]", memberLabels(updated.Members))
	}
	if len(updated.ExcludedMembers) != 1 || updated.ExcludedMembers[0].Label != "Alpha" {
		t.Fatalf("ExcludedMembers=%v want [Alpha]", memberLabels(updated.ExcludedMembers))
	}
	if len(updated.FilteredMembers) != 1 || updated.FilteredMembers[0].Label != "Bravo" {
		t.Fatalf("FilteredMembers=%v want [Bravo]", memberLabels(updated.FilteredMembers))
	}
	sel := selectorMembers(t, mut, sub.SelectorTag)
	if len(sel) != 1 {
		t.Fatalf("selector members=%v want exactly [Charlie tag]", sel)
	}
}

func TestService_Refresh_FilterHidingAllFails(t *testing.T) {
	svc, _ := newTestService(t)
	body := namedLinks("RU-1", "RU-2")
	srv := serveLinks(t, &body)

	_, err := svc.Create(context.Background(), CreateInput{
		Label: "x", URL: srv.URL, Enabled: true,
		FilterExclude: "RU",
	})
	if err == nil {
		t.Fatal("Create must fail when the filter hides every server")
	}
	if !errors.Is(err, ErrAllMembersFiltered) {
		t.Errorf("error must wrap ErrAllMembersFiltered, got: %v", err)
	}
	if !strings.Contains(err.Error(), "скрывают все серверы") {
		t.Errorf("expected the hide-all error, got: %v", err)
	}
}

// TestService_Refresh_AllFiltered_NoStagedMutations — ошибка «фильтр скрывает
// всё» обязана детектироваться ДО первой мутации: staged RemoveOutbound'ы в
// общем незакоммиченном батче унёс бы в конфиг следующий несвязанный Reload
// (тихая де-материализация подписки). Плюс defense-in-depth: refresh-путь
// сбрасывает батч Rollback'ом при любой ошибке applyDiff.
func TestService_Refresh_AllFiltered_NoStagedMutations(t *testing.T) {
	svc, mut := newTestService(t)
	body := namedLinks("DE-1", "RU-1")
	srv := serveLinks(t, &body)

	sub, err := svc.Create(context.Background(), CreateInput{
		Label: "x", URL: srv.URL, Enabled: true,
		FilterExclude: "RU",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Фид меняется: остаются только RU-серверы — фильтр скрывает всё.
	body = namedLinks("RU-1", "RU-2")
	mut.reset()
	updatedBefore := len(mut.updatedOutbounds)
	inboundsBefore := len(mut.addedInbounds)
	rollbacksBefore := mut.rollbacks

	_, err = svc.Refresh(context.Background(), sub.ID)
	if !errors.Is(err, ErrAllMembersFiltered) {
		t.Fatalf("Refresh err=%v want ErrAllMembersFiltered", err)
	}
	// Ни одной staged-мутации: батч остался чистым.
	if len(mut.addedOutbounds) != 0 {
		t.Errorf("no outbounds must be staged, added=%v", mut.addedOutbounds)
	}
	if len(mut.removedOutbounds) != 0 {
		t.Errorf("no removals must be staged, removed=%v", mut.removedOutbounds)
	}
	if len(mut.updatedOutbounds) != updatedBefore {
		t.Errorf("no updates must be staged, updated=%v", mut.updatedOutbounds[updatedBefore:])
	}
	if len(mut.addedInbounds) != inboundsBefore {
		t.Errorf("no inbounds must be staged, added=%v", mut.addedInbounds[inboundsBefore:])
	}
	// Rollback вызван (идемпотентен при пустом батче — но страхует пути,
	// где applyDiff падает после части мутаций).
	if mut.rollbacks <= rollbacksBefore {
		t.Error("refresh failure must roll the pending batch back")
	}
	// Прежний состав подписки не тронут.
	stored, _ := svc.Get(sub.ID)
	if len(stored.Members) != 1 || stored.Members[0].Label != "DE-1" {
		t.Errorf("Members=%v want [DE-1] (untouched)", memberLabels(stored.Members))
	}
}

// TestService_Update_FilterCompensationOnFailedRefresh — упавшая после
// записи фильтра ре-материализация не должна оставить новый фильтр на диске:
// каждый плановый refresh падал бы той же ошибкой до ручного вмешательства.
func TestService_Update_FilterCompensationOnFailedRefresh(t *testing.T) {
	svc, _ := newTestService(t)
	body := namedLinks("RU-1", "RU-2")
	srv := serveLinks(t, &body)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "x", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	exclude := "RU" // скрывает все серверы → refresh падает
	_, err = svc.Update(sub.ID, UpdatePatch{FilterExclude: &exclude})
	if !errors.Is(err, ErrAllMembersFiltered) {
		t.Fatalf("Update err=%v want ErrAllMembersFiltered", err)
	}
	stored, _ := svc.Get(sub.ID)
	if stored.FilterExclude != "" {
		t.Errorf("failed filter must be rolled back in store, got %q", stored.FilterExclude)
	}
	// Компенсация вернула рабочее состояние: плановый refresh снова проходит.
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Errorf("refresh after compensation must succeed, got: %v", err)
	}
}
