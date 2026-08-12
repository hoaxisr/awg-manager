package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

// seedSubscriptionWithGroups — как seedSubscription, но с подключённым
// GroupStore, чтобы Group-CRUD работал.
func seedSubscriptionWithGroups(t *testing.T, n int) (*subscription.Service, string) {
	t.Helper()
	store, err := subscription.NewStore(filepath.Join(t.TempDir(), "sub.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	gs, err := subscription.NewGroupStore(filepath.Join(t.TempDir(), "groups.json"))
	if err != nil {
		t.Fatalf("NewGroupStore: %v", err)
	}
	svc := subscription.NewService(store, noopMutator{})
	svc.SetGroupStore(gs)

	links := make([]string, n)
	for i := 0; i < n; i++ {
		links[i] = "vless://aaaaaaaa-bbbb-cccc-dddd-" + leftPad(i+1, 12) +
			"@h" + leftPad(i+1, 1) + ".example:443?security=tls#member-" + leftPad(i+1, 1)
	}
	sub, err := svc.Create(context.Background(), subscription.CreateInput{
		Label:   "test",
		Inline:  strings.Join(links, "\n"),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return svc, sub.ID
}

func postGroupCreate(t *testing.T, h *SubscriptionHandler, req CreateSubscriptionGroupRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/api/singbox/subscriptions/groups/create", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.CreateGroup(rr, r)
	return rr
}

func TestSubscriptionGroupHandler_Create_HappyPath(t *testing.T) {
	svc, subID := seedSubscriptionWithGroups(t, 3)
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	rr := postGroupCreate(t, h, CreateSubscriptionGroupRequest{
		Label:              "Все серверы",
		UseSubscriptionIDs: []string{subID},
		FilterExclude:      "member-3",
		Enabled:            true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp SubscriptionGroupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	d := resp.Data
	if d.Label != "Все серверы" || !d.Enabled {
		t.Errorf("DTO label/enabled mismatch: %+v", d)
	}
	if d.Mode != "urltest" {
		t.Errorf("default mode=%q want urltest", d.Mode)
	}
	if d.URLTest == nil || d.URLTest.IntervalSec != 60 {
		t.Errorf("urlTest defaults must be surfaced: %+v", d.URLTest)
	}
	if !strings.HasPrefix(d.Tag, "agg-") || d.InboundTag != d.Tag+"-in" {
		t.Errorf("tags: %q / %q", d.Tag, d.InboundTag)
	}
	if d.ListenPort == 0 {
		t.Error("listenPort must be allocated")
	}
	if d.ProxyIndex < 0 {
		t.Error("proxyIndex must be allocated (toggle defaults to on)")
	}
	// member-3 скрыт фильтром → 2 из 3.
	if d.MemberCount != 2 || len(d.Members) != 2 {
		t.Errorf("memberCount=%d members=%v want 2", d.MemberCount, d.Members)
	}
	if len(d.UseSubscriptionIDs) != 1 || d.UseSubscriptionIDs[0] != subID {
		t.Errorf("useSubscriptionIds=%v", d.UseSubscriptionIDs)
	}
	if d.FilterExclude != "member-3" {
		t.Errorf("filterExclude=%q", d.FilterExclude)
	}

	// Список отдаёт созданную группу.
	lr := httptest.NewRequest(http.MethodGet, "/api/singbox/subscriptions/groups", nil)
	lrr := httptest.NewRecorder()
	h.ListGroups(lrr, lr)
	if lrr.Code != http.StatusOK {
		t.Fatalf("list status=%d", lrr.Code)
	}
	var list SubscriptionGroupListResponse
	if err := json.Unmarshal(lrr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != d.ID {
		t.Errorf("list=%v want the created group", list.Data)
	}
}

func TestSubscriptionGroupHandler_Create_InvalidRegex400(t *testing.T) {
	svc, subID := seedSubscriptionWithGroups(t, 1)
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	rr := postGroupCreate(t, h, CreateSubscriptionGroupRequest{
		Label:              "x",
		UseSubscriptionIDs: []string{subID},
		FilterInclude:      `(?i)^(?!.*(RU|Russia)).*$`, // lookahead — RE2 не умеет
		Enabled:            true,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "lookahead/lookbehind") {
		t.Errorf("body must carry the targeted lookaround message: %s", rr.Body.String())
	}
}

func TestSubscriptionGroupHandler_Create_UnknownSub400(t *testing.T) {
	svc, _ := seedSubscriptionWithGroups(t, 1)
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	rr := postGroupCreate(t, h, CreateSubscriptionGroupRequest{
		Label:              "x",
		UseSubscriptionIDs: []string{"does-not-exist"},
		Enabled:            true,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rr.Code, rr.Body.String())
	}
}

func TestSubscriptionGroupHandler_UpdateAndDelete(t *testing.T) {
	svc, subID := seedSubscriptionWithGroups(t, 2)
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	rr := postGroupCreate(t, h, CreateSubscriptionGroupRequest{
		Label: "x", UseSubscriptionIDs: []string{subID}, Enabled: true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d", rr.Code)
	}
	var created SubscriptionGroupResponse
	json.Unmarshal(rr.Body.Bytes(), &created)

	// Update: смена label + mode.
	label := "renamed"
	mode := "selector"
	upBody, _ := json.Marshal(UpdateSubscriptionGroupRequest{Label: &label, Mode: &mode})
	ur := httptest.NewRequest(http.MethodPut, "/api/singbox/subscriptions/groups/update?id="+created.Data.ID, strings.NewReader(string(upBody)))
	urr := httptest.NewRecorder()
	h.UpdateGroup(urr, ur)
	if urr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", urr.Code, urr.Body.String())
	}
	var updated SubscriptionGroupResponse
	json.Unmarshal(urr.Body.Bytes(), &updated)
	if updated.Data.Label != "renamed" || updated.Data.Mode != "selector" {
		t.Errorf("update DTO: %+v", updated.Data)
	}

	// Delete.
	delBody, _ := json.Marshal(DeleteSubscriptionGroupRequest{ID: created.Data.ID})
	dr := httptest.NewRequest(http.MethodPost, "/api/singbox/subscriptions/groups/delete", strings.NewReader(string(delBody)))
	drr := httptest.NewRecorder()
	h.DeleteGroup(drr, dr)
	if drr.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", drr.Code, drr.Body.String())
	}
	if len(svc.ListGroups()) != 0 {
		t.Error("group must be gone after delete")
	}

	// Delete неизвестного id → 404 (через sentinel ErrGroupNotFound).
	delBody2, _ := json.Marshal(DeleteSubscriptionGroupRequest{ID: "nope"})
	dr2 := httptest.NewRequest(http.MethodPost, "/api/singbox/subscriptions/groups/delete", strings.NewReader(string(delBody2)))
	drr2 := httptest.NewRecorder()
	h.DeleteGroup(drr2, dr2)
	if drr2.Code != http.StatusNotFound {
		t.Errorf("delete unknown id status=%d want 404", drr2.Code)
	}
	if !strings.Contains(drr2.Body.String(), "NOT_FOUND") {
		t.Errorf("delete unknown id body must carry NOT_FOUND: %s", drr2.Body.String())
	}

	// Update неизвестного id → 404 тем же sentinel-путём.
	ur2 := httptest.NewRequest(http.MethodPut, "/api/singbox/subscriptions/groups/update?id=nope", strings.NewReader(`{"label":"x"}`))
	urr2 := httptest.NewRecorder()
	h.UpdateGroup(urr2, ur2)
	if urr2.Code != http.StatusNotFound {
		t.Errorf("update unknown id status=%d want 404, body=%s", urr2.Code, urr2.Body.String())
	}
}

// TestSubscriptionHandler_Update_AllFiltered409 — фильтр, скрывающий все
// серверы, отклоняется 409 ALL_MEMBERS_FILTERED (sentinel через errors.Is),
// а прежний фильтр остаётся в store (компенсация в Service.Update).
func TestSubscriptionHandler_Update_AllFiltered409(t *testing.T) {
	svc, subID := seedSubscription(t, 2)
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	body := `{"filterExclude":"member"}` // члены называются member-N → скрыты все
	r := httptest.NewRequest(http.MethodPut, "/api/singbox/subscriptions/update?id="+subID, strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Update(rr, r)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ALL_MEMBERS_FILTERED") {
		t.Errorf("body must carry ALL_MEMBERS_FILTERED: %s", rr.Body.String())
	}
	sub, err := svc.Get(subID)
	if err != nil {
		t.Fatal(err)
	}
	if sub.FilterExclude != "" {
		t.Errorf("rejected filter must not persist, got %q", sub.FilterExclude)
	}
	if len(sub.Members) != 2 {
		t.Errorf("members must be untouched, got %d", len(sub.Members))
	}
}

// TestSubscriptionHandler_GetStream_CarriesFilterFields — SSE-стрим (основной
// путь загрузки детальной страницы) обязан нести фильтры в meta и скрытых
// фильтром членов в done, иначе UI-блоки «Фильтр серверов» / «Скрыто
// фильтром» останутся пустыми.
func TestSubscriptionHandler_GetStream_CarriesFilterFields(t *testing.T) {
	store, err := subscription.NewStore(filepath.Join(t.TempDir(), "sub.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	svc := subscription.NewService(store, noopMutator{})
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	sub, err := svc.Create(context.Background(), subscription.CreateInput{
		Label:         "x",
		Inline:        "vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@a.example:443?security=tls&sni=a#Keep\nvless://3a3b1c2e-9999-4321-aaaa-1234567890a2@b.example:443?security=tls&sni=b#Hidden",
		Enabled:       true,
		FilterExclude: "Hidden",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rr := httptest.NewRecorder()
	h.GetStream(rr, httptest.NewRequest(http.MethodGet, "/get-stream?id="+sub.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()

	_, metaData, ok := strings.Cut(body, "event: meta\ndata: ")
	if !ok {
		t.Fatalf("no meta event: %s", body)
	}
	metaData, _, _ = strings.Cut(metaData, "\n")
	var meta SubscriptionMetaDTO
	if err := json.Unmarshal([]byte(metaData), &meta); err != nil {
		t.Fatalf("unmarshal meta: %v (%s)", err, metaData)
	}
	if meta.FilterExclude != "Hidden" {
		t.Errorf("meta.filterExclude=%q want %q", meta.FilterExclude, "Hidden")
	}

	_, doneData, ok := strings.Cut(body, "event: done\ndata: ")
	if !ok {
		t.Fatalf("no done event: %s", body)
	}
	doneData, _, _ = strings.Cut(doneData, "\n")
	var done SubscriptionStreamDoneDTO
	if err := json.Unmarshal([]byte(doneData), &done); err != nil {
		t.Fatalf("unmarshal done: %v (%s)", err, doneData)
	}
	if len(done.FilteredMembers) != 1 || done.FilteredMembers[0].Label != "Hidden" {
		t.Errorf("done.filteredMembers=%v want the Hidden member", done.FilteredMembers)
	}
}

func TestSubscriptionHandler_GetStream_CarriesBindInterface(t *testing.T) {
	store, err := subscription.NewStore(filepath.Join(t.TempDir(), "sub.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	svc := subscription.NewService(store, noopMutator{})
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	sub, err := svc.Create(context.Background(), subscription.CreateInput{
		Label:         "x",
		Inline:        "vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@a.example:443?security=tls&sni=a#Keep",
		Enabled:       true,
		BindInterface: "eth3",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rr := httptest.NewRecorder()
	h.GetStream(rr, httptest.NewRequest(http.MethodGet, "/get-stream?id="+sub.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()

	_, metaData, ok := strings.Cut(body, "event: meta\ndata: ")
	if !ok {
		t.Fatalf("no meta event: %s", body)
	}
	metaData, _, _ = strings.Cut(metaData, "\n")
	var meta SubscriptionMetaDTO
	if err := json.Unmarshal([]byte(metaData), &meta); err != nil {
		t.Fatalf("unmarshal meta: %v (%s)", err, metaData)
	}
	if meta.BindInterface != "eth3" {
		t.Errorf("meta.bindInterface=%q want eth3", meta.BindInterface)
	}
}

// TestSubscriptionDTO_FilterFields — filterInclude/filterExclude/filteredMembers
// доезжают до DTO подписки.
func TestSubscriptionDTO_FilterFields(t *testing.T) {
	s := subscription.Subscription{
		ID:            "sub-x",
		FilterInclude: "(?i)de",
		FilterExclude: "(?i)ru",
		FilteredMembers: []subscription.MemberInfo{
			{Tag: "sub-x-aaaa", Label: "🇷🇺 Moscow", Protocol: "vless", Server: "m.example", Port: 443},
		},
	}
	dto := toSubscriptionDTO(s, true)
	if dto.FilterInclude != "(?i)de" || dto.FilterExclude != "(?i)ru" {
		t.Errorf("filter fields: %+v", dto)
	}
	if len(dto.FilteredMembers) != 1 || dto.FilteredMembers[0].Label != "🇷🇺 Moscow" {
		t.Errorf("filteredMembers: %+v", dto.FilteredMembers)
	}
}
