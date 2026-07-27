package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func newTestServiceWithGroups(t *testing.T) (*Service, *fakeMutator, *GroupStore) {
	t.Helper()
	svc, mut := newTestService(t)
	gs, err := NewGroupStore(filepath.Join(t.TempDir(), "groups.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetGroupStore(gs)
	return svc, mut, gs
}

func TestGroupStore_Roundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "groups.json")
	gs, err := NewGroupStore(path)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gs.Create(GroupCreateInput{
		Label:              "Европа",
		Mode:               ModeURLTest,
		UseSubscriptionIDs: []string{"sub-a", "sub-b"},
		FilterInclude:      "(?i)(DE|NL)",
		FilterExclude:      "BRIDGE",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(g.ID) != 24 {
		t.Errorf("ID=%q want 24 hex chars (12 rand bytes)", g.ID)
	}
	if g.Tag != "agg-"+g.ID[:8] || g.InboundTag != g.Tag+"-in" {
		t.Errorf("tags: Tag=%q InboundTag=%q", g.Tag, g.InboundTag)
	}
	if g.ProxyIndex != -1 {
		t.Errorf("ProxyIndex=%d want -1", g.ProxyIndex)
	}
	if err := gs.SetListenPort(g.ID, 11005); err != nil {
		t.Fatal(err)
	}

	// Повторная загрузка файла — все поля переживают roundtrip.
	reloaded, err := NewGroupStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := reloaded.Get(g.ID)
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if got.Label != "Европа" || got.FilterInclude != "(?i)(DE|NL)" || got.FilterExclude != "BRIDGE" ||
		got.ListenPort != 11005 || !got.Enabled || got.EffectiveMode() != ModeURLTest {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if len(got.UseSubscriptionIDs) != 2 || got.UseSubscriptionIDs[0] != "sub-a" {
		t.Errorf("UseSubscriptionIDs=%v", got.UseSubscriptionIDs)
	}
}

func TestGroupStore_RemoveSubscriptionRef(t *testing.T) {
	gs, err := NewGroupStore(filepath.Join(t.TempDir(), "groups.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, _ := gs.Create(GroupCreateInput{Label: "x", UseSubscriptionIDs: []string{"a", "b", "c"}})
	if err := gs.RemoveSubscriptionRef("b"); err != nil {
		t.Fatal(err)
	}
	got, _ := gs.Get(g.ID)
	if len(got.UseSubscriptionIDs) != 2 || got.UseSubscriptionIDs[0] != "a" || got.UseSubscriptionIDs[1] != "c" {
		t.Errorf("UseSubscriptionIDs=%v want [a c]", got.UseSubscriptionIDs)
	}
}

// groupOutboundBody парсит последний AddOutbound-body группы.
func groupOutboundBody(t *testing.T, mut *fakeMutator, tag string) map[string]any {
	t.Helper()
	raw, ok := mut.bodies[tag]
	if !ok {
		t.Fatalf("no outbound body staged for %s", tag)
	}
	var ob map[string]any
	if err := json.Unmarshal(raw, &ob); err != nil {
		t.Fatalf("parse group body: %v", err)
	}
	return ob
}

func groupMembers(t *testing.T, mut *fakeMutator, tag string) []string {
	t.Helper()
	ob := groupOutboundBody(t, mut, tag)
	raw, _ := ob["outbounds"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestService_CreateGroup_UnionDeterministicOrderWithFilter(t *testing.T) {
	svc, mut, _ := newTestServiceWithGroups(t)
	bodyA := namedLinks("DE-1", "RU-1")
	srvA := serveLinks(t, &bodyA)
	bodyB := namedLinks("NL-1", "NL-2")
	srvB := serveLinks(t, &bodyB)
	subA, err := svc.Create(context.Background(), CreateInput{Label: "a", URL: srvA.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	subB, err := svc.Create(context.Background(), CreateInput{Label: "b", URL: srvB.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "united",
		// Порядок подписок в группе — как задал пользователь: B, затем A.
		UseSubscriptionIDs: []string{subB.ID, subA.ID},
		FilterExclude:      "(?i)ru",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if g.ListenPort == 0 {
		t.Error("group must allocate a listen port")
	}
	if g.ProxyIndex < 0 {
		t.Error("group must allocate a proxy index (toggle on by default)")
	}
	// Дефолтный режим группы — urltest (основной кейс issue).
	ob := groupOutboundBody(t, mut, g.Tag)
	if ob["type"] != "urltest" {
		t.Errorf("group outbound type=%v want urltest", ob["type"])
	}
	got := groupMembers(t, mut, g.Tag)
	want := []string{subB.MemberTags[0], subB.MemberTags[1], subA.MemberTags[0]} // NL-1, NL-2, DE-1; RU-1 отфильтрован
	if len(got) != len(want) {
		t.Fatalf("group members=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("member order mismatch: got=%v want=%v", got, want)
		}
	}
	// Inbound + route rule подняты.
	if !containsTag(mut.addedInbounds, g.InboundTag) {
		t.Errorf("group inbound %s must be added", g.InboundTag)
	}
	if mut.addedRules == 0 {
		t.Error("group route rule must be added")
	}
}

func TestService_CreateGroup_Validation(t *testing.T) {
	svc, _, _ := newTestServiceWithGroups(t)
	if _, err := svc.CreateGroup(context.Background(), GroupCreateInput{Label: "  "}); err == nil {
		t.Error("empty label must be rejected")
	}
	if _, err := svc.CreateGroup(context.Background(), GroupCreateInput{Label: "x", FilterInclude: "(bad"}); err == nil {
		t.Error("invalid filter must be rejected")
	}
	if _, err := svc.CreateGroup(context.Background(), GroupCreateInput{Label: "x", UseSubscriptionIDs: []string{"nope"}}); err == nil {
		t.Error("unknown subscription id must be rejected")
	}
	if len(svc.ListGroups()) != 0 {
		t.Error("no group rows must persist after validation failures")
	}
}

// #572: кастомный tag при создании группы.
func TestGroupStore_Create_CustomTag(t *testing.T) {
	gs, err := NewGroupStore(filepath.Join(t.TempDir(), "groups.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gs.Create(GroupCreateInput{Label: "Европа", Tag: "eu-all", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g.Tag != "eu-all" || g.InboundTag != "eu-all-in" {
		t.Errorf("tags: Tag=%q InboundTag=%q", g.Tag, g.InboundTag)
	}
	if _, err := gs.Create(GroupCreateInput{Label: "Копия", Tag: "eu-all"}); err == nil {
		t.Error("duplicate tag must be rejected")
	}
	if _, err := gs.Create(GroupCreateInput{Label: "X", Tag: "eu-all-in"}); err == nil {
		t.Error("tag colliding with existing inbound tag must be rejected")
	}
}

func TestService_CreateGroup_CustomTagValidation(t *testing.T) {
	svc, _, _ := newTestServiceWithGroups(t)
	bad := []string{
		"с пробелом", "кириллица", "tag!", "-lead", ".lead",
		"sub-x", "direct", "block", "dns-out",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 33 символа
	}
	for _, tag := range bad {
		if _, err := svc.CreateGroup(context.Background(), GroupCreateInput{Label: "G", Tag: tag}); !errors.Is(err, ErrValidation) {
			t.Errorf("tag %q: want ErrValidation, got %v", tag, err)
		}
	}
	if len(svc.ListGroups()) != 0 {
		t.Error("no group rows must persist after tag validation failures")
	}
}

func TestService_CreateGroup_CustomTagMaterializes(t *testing.T) {
	svc, mut, _ := newTestServiceWithGroups(t)
	body := namedLinks("DE-1")
	srv := serveLinks(t, &body)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "a", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// Коллизия с selector-тегом существующей подписки — отклоняется.
	if _, err := svc.CreateGroup(context.Background(), GroupCreateInput{Label: "X", Tag: sub.SelectorTag}); !errors.Is(err, ErrValidation) {
		t.Errorf("tag colliding with subscription selector: want ErrValidation, got %v", err)
	}

	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label:              "Европа",
		Tag:                "eu.all_1",
		UseSubscriptionIDs: []string{sub.ID},
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if g.Tag != "eu.all_1" || g.InboundTag != "eu.all_1-in" {
		t.Fatalf("tags: Tag=%q InboundTag=%q", g.Tag, g.InboundTag)
	}
	if ob := groupOutboundBody(t, mut, "eu.all_1"); ob["type"] != "urltest" {
		t.Errorf("outbound под кастомным тегом не материализован: %v", ob)
	}
	if !containsTag(mut.addedInbounds, "eu.all_1-in") {
		t.Errorf("inbound под кастомным тегом не поднят")
	}
}

func TestService_CreateGroup_EmptyResolutionEmitsNoOutbound(t *testing.T) {
	svc, mut, _ := newTestServiceWithGroups(t)
	body := namedLinks("RU-1", "RU-2")
	srv := serveLinks(t, &body)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "a", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label:              "empty",
		UseSubscriptionIDs: []string{sub.ID},
		FilterExclude:      "RU", // скрывает всех — группа остаётся, outbound не эмитится
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, staged := mut.bodies[g.Tag]; staged {
		t.Errorf("empty group must not stage an outbound (sing-box rejects empty selector)")
	}
	if !mut.removedOutbound(g.Tag) {
		t.Error("stale group outbound must be removed")
	}
	// Группа-сущность жива, UI покажет «0 серверов».
	if len(svc.ListGroups()) != 1 {
		t.Error("group entity must survive empty resolution")
	}
}

func TestService_Refresh_UpdatesGroupInSameReload(t *testing.T) {
	svc, mut, _ := newTestServiceWithGroups(t)
	body := namedLinks("One")
	srv := serveLinks(t, &body)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "a", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "grp", UseSubscriptionIDs: []string{sub.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Фид меняется: появляется второй сервер. Один Refresh → группа
	// пересобрана в том же батче (один Reload).
	body = namedLinks("One", "Two")
	reloadsBefore := mut.reloads
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if mut.reloads != reloadsBefore+1 {
		t.Errorf("refresh must commit exactly one Reload, got %d", mut.reloads-reloadsBefore)
	}
	updated, _ := svc.Get(sub.ID)
	if len(updated.MemberTags) != 2 {
		t.Fatalf("want 2 members after refresh, got %d", len(updated.MemberTags))
	}
	got := groupMembers(t, mut, g.Tag)
	if len(got) != 2 {
		t.Fatalf("group must reference both members after refresh, got %v", got)
	}
	for i, tag := range updated.MemberTags {
		if got[i] != tag {
			t.Fatalf("group members=%v want %v", got, updated.MemberTags)
		}
	}
}

func TestService_DeleteSubscription_DropsGroupRef(t *testing.T) {
	svc, mut, gs := newTestServiceWithGroups(t)
	bodyA := namedLinks("A-1")
	srvA := serveLinks(t, &bodyA)
	bodyB := namedLinks("B-1")
	srvB := serveLinks(t, &bodyB)
	subA, _ := svc.Create(context.Background(), CreateInput{Label: "a", URL: srvA.URL, Enabled: true})
	subB, _ := svc.Create(context.Background(), CreateInput{Label: "b", URL: srvB.URL, Enabled: true})
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "grp", UseSubscriptionIDs: []string{subA.ID, subB.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(context.Background(), subA.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := gs.Get(g.ID)
	if err != nil {
		t.Fatalf("group must survive subscription delete: %v", err)
	}
	if len(got.UseSubscriptionIDs) != 1 || got.UseSubscriptionIDs[0] != subB.ID {
		t.Errorf("UseSubscriptionIDs=%v want [%s]", got.UseSubscriptionIDs, subB.ID)
	}
	// Группа пересобрана без членов удалённой подписки.
	members := groupMembers(t, mut, g.Tag)
	if len(members) != 1 || members[0] != subB.MemberTags[0] {
		t.Errorf("group members=%v want [%s]", members, subB.MemberTags[0])
	}
}

func TestService_DeleteGroup_RemovesEntities(t *testing.T) {
	svc, mut, gs := newTestServiceWithGroups(t)
	body := namedLinks("A-1")
	srv := serveLinks(t, &body)
	sub, _ := svc.Create(context.Background(), CreateInput{Label: "a", URL: srv.URL, Enabled: true})
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "grp", UseSubscriptionIDs: []string{sub.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxyIdx := g.ProxyIndex

	if err := svc.DeleteGroup(context.Background(), g.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if !mut.removedOutbound(g.Tag) {
		t.Error("group outbound must be removed")
	}
	if !containsTag(mut.removedInbounds, g.InboundTag) {
		t.Error("group inbound must be removed")
	}
	found := false
	for _, idx := range mut.removedProxies {
		if idx == proxyIdx {
			found = true
		}
	}
	if !found {
		t.Errorf("group proxy %d must be removed, removed=%v", proxyIdx, mut.removedProxies)
	}
	if _, err := gs.Get(g.ID); err == nil {
		t.Error("group row must be deleted from store")
	}
}

func TestService_UpdateGroup_PatchAndRestage(t *testing.T) {
	svc, mut, _ := newTestServiceWithGroups(t)
	body := namedLinks("DE-1", "RU-1")
	srv := serveLinks(t, &body)
	sub, _ := svc.Create(context.Background(), CreateInput{Label: "a", URL: srv.URL, Enabled: true})
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "grp", UseSubscriptionIDs: []string{sub.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := groupMembers(t, mut, g.Tag); len(got) != 2 {
		t.Fatalf("want 2 members before patch, got %v", got)
	}

	exclude := "RU"
	mode := ModeSelector
	updated, err := svc.UpdateGroup(context.Background(), g.ID, GroupUpdatePatch{
		FilterExclude: &exclude,
		Mode:          &mode,
	})
	if err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	if updated.EffectiveMode() != ModeSelector {
		t.Errorf("mode=%s want selector", updated.EffectiveMode())
	}
	ob := groupOutboundBody(t, mut, g.Tag)
	if ob["type"] != "selector" {
		t.Errorf("group outbound type=%v want selector after mode patch", ob["type"])
	}
	if got := groupMembers(t, mut, g.Tag); len(got) != 1 {
		t.Errorf("filter patch must drop RU member, got %v", got)
	}

	// Невалидный фильтр отклоняется и не сохраняется.
	bad := "(broken"
	if _, err := svc.UpdateGroup(context.Background(), g.ID, GroupUpdatePatch{FilterInclude: &bad}); err == nil {
		t.Error("invalid filter must be rejected")
	}
	stored, _ := svc.GetGroup(g.ID)
	if stored.FilterInclude != "" {
		t.Errorf("invalid filter must not persist, got %q", stored.FilterInclude)
	}
}

func TestService_UpdateGroup_DisableTearsDown(t *testing.T) {
	svc, mut, _ := newTestServiceWithGroups(t)
	body := namedLinks("A-1")
	srv := serveLinks(t, &body)
	sub, _ := svc.Create(context.Background(), CreateInput{Label: "a", URL: srv.URL, Enabled: true})
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "grp", UseSubscriptionIDs: []string{sub.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mut.reset()
	off := false
	if _, err := svc.UpdateGroup(context.Background(), g.ID, GroupUpdatePatch{Enabled: &off}); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	if !mut.removedOutbound(g.Tag) {
		t.Error("disabled group outbound must be removed")
	}
	if _, staged := mut.bodies[g.Tag]; staged {
		t.Error("disabled group must not stage a fresh outbound")
	}
}

// countTag — сколько раз tag встречается в срезе (reset не чистит
// added/removed inbounds, поэтому проверяем дельты по количеству).
func countTag(ss []string, t string) int {
	n := 0
	for _, s := range ss {
		if s == t {
			n++
		}
	}
	return n
}

// TestService_UpdateGroup_DisableKeepsInbound — выключенная группа обязана
// сохранить свой mixed inbound в слоте: он резервирует listen_port (иначе
// AllocListenPort, сканирующий inbounds слота, выдал бы порт другому, и
// повторное включение упёрлось бы в коллизию). Полный teardown inbound —
// только в DeleteGroup.
func TestService_UpdateGroup_DisableKeepsInbound(t *testing.T) {
	svc, mut, _ := newTestServiceWithGroups(t)
	body := namedLinks("A-1")
	srv := serveLinks(t, &body)
	sub, _ := svc.Create(context.Background(), CreateInput{Label: "a", URL: srv.URL, Enabled: true})
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "grp", UseSubscriptionIDs: []string{sub.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	addsBefore := countTag(mut.addedInbounds, g.InboundTag)
	off := false
	if _, err := svc.UpdateGroup(context.Background(), g.ID, GroupUpdatePatch{Enabled: &off}); err != nil {
		t.Fatalf("UpdateGroup(disable): %v", err)
	}
	if countTag(mut.removedInbounds, g.InboundTag) != 0 {
		t.Error("disabled group must KEEP its inbound (port reservation), but it was removed")
	}
	if countTag(mut.addedInbounds, g.InboundTag) <= addsBefore {
		t.Error("disabled group inbound must stay staged (idempotent AddInbound)")
	}

	// Повторное включение: outbound + route-правило поднимаются заново,
	// AddInbound идемпотентен — коллизии порта нет, ошибок нет.
	mut.reset()
	on := true
	if _, err := svc.UpdateGroup(context.Background(), g.ID, GroupUpdatePatch{Enabled: &on}); err != nil {
		t.Fatalf("UpdateGroup(re-enable): %v", err)
	}
	if members := groupMembers(t, mut, g.Tag); len(members) != 1 {
		t.Errorf("re-enabled group must stage its outbound with members, got %v", members)
	}
	if countTag(mut.removedInbounds, g.InboundTag) != 0 {
		t.Error("re-enable must not tear the inbound down")
	}
}

// TestService_Update_EnabledToggleRestagesGroups — resolveGroupTags пропускает
// выключенные подписки, поэтому смена enabled обязана пересобрать группы
// сразу, а не при следующем случайном reload.
func TestService_Update_EnabledToggleRestagesGroups(t *testing.T) {
	svc, mut, _ := newTestServiceWithGroups(t)
	bodyA := namedLinks("A-1")
	srvA := serveLinks(t, &bodyA)
	bodyB := namedLinks("B-1")
	srvB := serveLinks(t, &bodyB)
	subA, _ := svc.Create(context.Background(), CreateInput{Label: "a", URL: srvA.URL, Enabled: true})
	subB, _ := svc.Create(context.Background(), CreateInput{Label: "b", URL: srvB.URL, Enabled: true})
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "grp", UseSubscriptionIDs: []string{subA.ID, subB.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := groupMembers(t, mut, g.Tag); len(got) != 2 {
		t.Fatalf("want 2 members before toggle, got %v", got)
	}

	mut.reset()
	reloadsBefore := mut.reloads
	off := false
	if _, err := svc.Update(subA.ID, UpdatePatch{Enabled: &off}); err != nil {
		t.Fatalf("Update(disable): %v", err)
	}
	if mut.reloads != reloadsBefore+1 {
		t.Errorf("enabled toggle must commit exactly one reload, got %d", mut.reloads-reloadsBefore)
	}
	got := groupMembers(t, mut, g.Tag)
	if len(got) != 1 || got[0] != subB.MemberTags[0] {
		t.Errorf("group must be rebuilt without disabled sub's members, got %v want [%s]", got, subB.MemberTags[0])
	}

	// Обратное включение возвращает членов подписки в группу.
	mut.reset()
	on := true
	if _, err := svc.Update(subA.ID, UpdatePatch{Enabled: &on}); err != nil {
		t.Fatalf("Update(enable): %v", err)
	}
	got = groupMembers(t, mut, g.Tag)
	if len(got) != 2 {
		t.Errorf("re-enabled sub's members must return to the group, got %v", got)
	}
}

// TestService_DeleteGroup_FailedReloadRetryable — teardown-reload коммитится
// ДО удаления строки store: упавший reload оставляет строку (retry вместо
// 404), ProxyN не снимается до успешного коммита.
func TestService_DeleteGroup_FailedReloadRetryable(t *testing.T) {
	svc, mut, gs := newTestServiceWithGroups(t)
	body := namedLinks("A-1")
	srv := serveLinks(t, &body)
	sub, _ := svc.Create(context.Background(), CreateInput{Label: "a", URL: srv.URL, Enabled: true})
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "grp", UseSubscriptionIDs: []string{sub.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	mut.reloadErr = errors.New("boom")
	if err := svc.DeleteGroup(context.Background(), g.ID); err == nil {
		t.Fatal("DeleteGroup must fail when the teardown reload fails")
	}
	if _, err := gs.Get(g.ID); err != nil {
		t.Fatalf("group row must survive a failed teardown reload: %v", err)
	}
	for _, idx := range mut.removedProxies {
		if idx == g.ProxyIndex {
			t.Error("proxy must not be removed before the teardown reload commits")
		}
	}

	// Retry после устранения причины: строка ещё на месте → не 404.
	mut.reloadErr = nil
	mut.reset()
	if err := svc.DeleteGroup(context.Background(), g.ID); err != nil {
		t.Fatalf("DeleteGroup retry: %v", err)
	}
	if _, err := gs.Get(g.ID); err == nil {
		t.Error("group row must be deleted after a successful retry")
	}
	if !mut.removedOutbound(g.Tag) {
		t.Error("group outbound must be removed on retry")
	}
	// stageGroups не должен пересоздать сущности удаляемой группы в том же батче.
	if _, staged := mut.bodies[g.Tag]; staged {
		t.Error("teardown batch must not restage the deleted group's outbound")
	}
}

// TestGroupStore_List_Sorted — детерминированный порядок: Label без учёта
// регистра, при равенстве — ID.
func TestGroupStore_List_Sorted(t *testing.T) {
	gs, err := NewGroupStore(filepath.Join(t.TempDir(), "groups.json"))
	if err != nil {
		t.Fatal(err)
	}
	gb, _ := gs.Create(GroupCreateInput{Label: "bravo"})
	ga, _ := gs.Create(GroupCreateInput{Label: "Alpha"})
	gc1, _ := gs.Create(GroupCreateInput{Label: "same"})
	gc2, _ := gs.Create(GroupCreateInput{Label: "same"})

	list := gs.List()
	if len(list) != 4 {
		t.Fatalf("len=%d want 4", len(list))
	}
	if list[0].ID != ga.ID || list[1].ID != gb.ID {
		t.Errorf("order: got [%s %s ...] want [Alpha bravo ...]", list[0].Label, list[1].Label)
	}
	// Tie-break по ID.
	wantFirst, wantSecond := gc1.ID, gc2.ID
	if wantSecond < wantFirst {
		wantFirst, wantSecond = wantSecond, wantFirst
	}
	if list[2].ID != wantFirst || list[3].ID != wantSecond {
		t.Errorf("tie-break: got [%s %s] want [%s %s]", list[2].ID, list[3].ID, wantFirst, wantSecond)
	}
}

func TestService_SyncProxies_CoversGroups(t *testing.T) {
	svc, mut, gs := newTestServiceWithGroups(t)
	// Группа создана при выключенном тумблере → ProxyIndex=-1.
	enabled := false
	svc.SetNDMSProxyEnabled(func() bool { return enabled })

	body := namedLinks("A-1")
	srv := serveLinks(t, &body)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "a", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "grp", UseSubscriptionIDs: []string{sub.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.ProxyIndex != -1 {
		t.Fatalf("proxy must not be allocated while toggle is off, got %d", g.ProxyIndex)
	}

	// Тумблер включается → SyncProxies выделяет ProxyN и группе тоже.
	enabled = true
	if err := svc.SyncProxies(context.Background()); err != nil {
		t.Fatalf("SyncProxies: %v", err)
	}
	got, _ := gs.Get(g.ID)
	if got.ProxyIndex < 0 {
		t.Error("SyncProxies must allocate a proxy index for the group")
	}
	found := false
	for _, c := range mut.ensuredProxies {
		if c.idx == got.ProxyIndex && c.port == int(got.ListenPort) && c.description == "grp" {
			found = true
		}
	}
	if !found {
		t.Errorf("EnsureProxy(idx=%d, port=%d, %q) not observed: %v", got.ProxyIndex, got.ListenPort, "grp", mut.ensuredProxies)
	}
}

func TestService_ResolveGroupMembers_SkipsDisabledSub(t *testing.T) {
	svc, _, _ := newTestServiceWithGroups(t)
	body := namedLinks("A-1")
	srv := serveLinks(t, &body)
	sub, _ := svc.Create(context.Background(), CreateInput{Label: "a", URL: srv.URL, Enabled: true})
	g, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label: "grp", UseSubscriptionIDs: []string{sub.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	members, err := svc.ResolveGroupMembers(*g)
	if err != nil || len(members) != 1 {
		t.Fatalf("resolve=%v err=%v want 1 member", members, err)
	}
	off := false
	if _, err := svc.Update(sub.ID, UpdatePatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	members, err = svc.ResolveGroupMembers(*g)
	if err != nil || len(members) != 0 {
		t.Fatalf("disabled subscription must be excluded from group, got %v", members)
	}
}
