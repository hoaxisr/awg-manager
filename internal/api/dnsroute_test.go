package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/dnsroute"
)

type fakeDNSRouteService struct {
	createFn                  func(context.Context, dnsroute.DomainList) (*dnsroute.DomainList, error)
	getFn                     func(context.Context, string) (*dnsroute.DomainList, error)
	listFn                    func(context.Context) ([]dnsroute.DomainList, error)
	updateFn                  func(context.Context, dnsroute.DomainList) (*dnsroute.DomainList, error)
	deleteFn                  func(context.Context, string) error
	deleteBatchFn             func(context.Context, []string) (int, error)
	createBatchFn             func(context.Context, []dnsroute.DomainList) ([]*dnsroute.DomainList, error)
	setEnabledFn              func(context.Context, string, bool) error
	refreshSubscriptionsFn    func(context.Context, string) error
	refreshAllSubscriptionsFn func(context.Context) error

	created        []dnsroute.DomainList
	updated        []dnsroute.DomainList
	deletedIDs     []string
	deleteBatchIDs []string
	setEnabledID   string
	setEnabledVal  bool
	refreshID      string
	refreshAll     bool
}

func (f *fakeDNSRouteService) Create(ctx context.Context, list dnsroute.DomainList) (*dnsroute.DomainList, error) {
	if f.createFn != nil {
		return f.createFn(ctx, list)
	}
	f.created = append(f.created, list)
	out := list
	if out.ID == "" {
		out.ID = "created"
	}
	return &out, nil
}
func (f *fakeDNSRouteService) Get(ctx context.Context, id string) (*dnsroute.DomainList, error) {
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return &dnsroute.DomainList{ID: id, Name: "got"}, nil
}
func (f *fakeDNSRouteService) List(ctx context.Context) ([]dnsroute.DomainList, error) {
	if f.listFn != nil {
		return f.listFn(ctx)
	}
	return []dnsroute.DomainList{{ID: "dns1", Name: "One"}}, nil
}
func (f *fakeDNSRouteService) Update(ctx context.Context, list dnsroute.DomainList) (*dnsroute.DomainList, error) {
	if f.updateFn != nil {
		return f.updateFn(ctx, list)
	}
	f.updated = append(f.updated, list)
	return &list, nil
}
func (f *fakeDNSRouteService) Delete(ctx context.Context, id string) error {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, id)
	}
	f.deletedIDs = append(f.deletedIDs, id)
	return nil
}
func (f *fakeDNSRouteService) DeleteBatch(ctx context.Context, ids []string) (int, error) {
	if f.deleteBatchFn != nil {
		return f.deleteBatchFn(ctx, ids)
	}
	f.deleteBatchIDs = append(f.deleteBatchIDs, ids...)
	return len(ids), nil
}
func (f *fakeDNSRouteService) CreateBatch(ctx context.Context, lists []dnsroute.DomainList) ([]*dnsroute.DomainList, error) {
	if f.createBatchFn != nil {
		return f.createBatchFn(ctx, lists)
	}
	f.created = append(f.created, lists...)
	out := make([]*dnsroute.DomainList, 0, len(lists))
	for i := range lists {
		list := lists[i]
		if list.ID == "" {
			list.ID = "created"
		}
		out = append(out, &list)
	}
	return out, nil
}
func (f *fakeDNSRouteService) SetEnabled(ctx context.Context, id string, enabled bool) error {
	if f.setEnabledFn != nil {
		return f.setEnabledFn(ctx, id, enabled)
	}
	f.setEnabledID, f.setEnabledVal = id, enabled
	return nil
}
func (f *fakeDNSRouteService) RefreshSubscriptions(ctx context.Context, id string) error {
	if f.refreshSubscriptionsFn != nil {
		return f.refreshSubscriptionsFn(ctx, id)
	}
	f.refreshID = id
	return nil
}
func (f *fakeDNSRouteService) RefreshAllSubscriptions(ctx context.Context) error {
	if f.refreshAllSubscriptionsFn != nil {
		return f.refreshAllSubscriptionsFn(ctx)
	}
	f.refreshAll = true
	return nil
}

func perform(h http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func decodeJSONBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v body=%s", err, rr.Body.String())
	}
	return out
}

func sampleDNSList(id, name string) dnsroute.DomainList {
	manualText := "# comment\nexample.com"
	excludesText := "# bypass\n.local"
	return dnsroute.DomainList{ID: id, Name: name, ManualText: &manualText, ExcludesText: &excludesText, Backend: "ndms", Enabled: true}
}

func TestDNSRouteHandlerContracts(t *testing.T) {
	t.Run("List", func(t *testing.T) {
		h := NewDNSRouteHandler(&fakeDNSRouteService{}, nil)
		if rr := perform(h.List, http.MethodPost, "/dns-routes/list", ""); rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
		rr := perform(h.List, http.MethodGet, "/dns-routes/list", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d", rr.Code)
		}
		body := decodeJSONBody(t, rr)
		if body["success"] != true {
			t.Fatalf("success=%v", body["success"])
		}
		hErr := NewDNSRouteHandler(&fakeDNSRouteService{listFn: func(context.Context) ([]dnsroute.DomainList, error) { return nil, errors.New("boom") }}, nil)
		rr = perform(hErr.List, http.MethodGet, "/dns-routes/list", "")
		if decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_LIST_ERROR" {
			t.Fatal(rr.Body.String())
		}
	})

	t.Run("Get", func(t *testing.T) {
		fake := &fakeDNSRouteService{}
		h := NewDNSRouteHandler(fake, nil)
		if rr := perform(h.Get, http.MethodPost, "/dns-routes/get", ""); rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
		if rr := perform(h.Get, http.MethodGet, "/dns-routes/get", ""); decodeJSONBody(t, rr)["code"] != "MISSING_ID" {
			t.Fatal(rr.Body.String())
		}
		rr := perform(h.Get, http.MethodGet, "/dns-routes/get?id=dns1", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d", rr.Code)
		}
		hErr := NewDNSRouteHandler(&fakeDNSRouteService{getFn: func(context.Context, string) (*dnsroute.DomainList, error) { return nil, errors.New("boom") }}, nil)
		if rr := perform(hErr.Get, http.MethodGet, "/dns-routes/get?id=dns1", ""); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_GET_ERROR" {
			t.Fatal(rr.Body.String())
		}
		_ = fake
	})

	t.Run("CreateUpdate", func(t *testing.T) {
		fake := &fakeDNSRouteService{}
		h := NewDNSRouteHandler(fake, nil)
		if rr := perform(h.Create, http.MethodGet, "/dns-routes/create", "{}"); rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
		if rr := perform(h.Create, http.MethodPost, "/dns-routes/create", "{"); decodeJSONBody(t, rr)["code"] != "INVALID_JSON" {
			t.Fatal(rr.Body.String())
		}
		rr := perform(h.Create, http.MethodPost, "/dns-routes/create", `{"name":"Work","manualDomains":["corp.local"],"enabled":true}`)
		if rr.Code != http.StatusOK || len(fake.created) != 1 || fake.created[0].Name != "Work" {
			t.Fatalf("bad create: %s", rr.Body.String())
		}
		hCreateErr := NewDNSRouteHandler(&fakeDNSRouteService{createFn: func(context.Context, dnsroute.DomainList) (*dnsroute.DomainList, error) {
			return nil, errors.New("boom")
		}}, nil)
		if rr := perform(hCreateErr.Create, http.MethodPost, "/dns-routes/create", `{}`); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_CREATE_ERROR" {
			t.Fatal(rr.Body.String())
		}

		if rr := perform(h.Update, http.MethodGet, "/dns-routes/update?id=dns1", "{}"); rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
		if rr := perform(h.Update, http.MethodPost, "/dns-routes/update", `{"name":"New"}`); decodeJSONBody(t, rr)["code"] != "MISSING_ID" {
			t.Fatal(rr.Body.String())
		}
		if rr := perform(h.Update, http.MethodPost, "/dns-routes/update?id=dns1", "{"); decodeJSONBody(t, rr)["code"] != "INVALID_JSON" {
			t.Fatal(rr.Body.String())
		}
		rr = perform(h.Update, http.MethodPost, "/dns-routes/update?id=dns1", `{"name":"New"}`)
		if rr.Code != http.StatusOK || len(fake.updated) == 0 || fake.updated[0].ID != "dns1" {
			t.Fatalf("bad update: %s", rr.Body.String())
		}
		hUpdateErr := NewDNSRouteHandler(&fakeDNSRouteService{updateFn: func(context.Context, dnsroute.DomainList) (*dnsroute.DomainList, error) {
			return nil, errors.New("boom")
		}}, nil)
		if rr := perform(hUpdateErr.Update, http.MethodPost, "/dns-routes/update?id=dns1", `{}`); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_UPDATE_ERROR" {
			t.Fatal(rr.Body.String())
		}
	})

	t.Run("DeleteDeleteBatchSetEnabled", func(t *testing.T) {
		fake := &fakeDNSRouteService{}
		h := NewDNSRouteHandler(fake, nil)
		if rr := perform(h.Delete, http.MethodGet, "/dns-routes/delete?id=dns1", ""); rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
		if rr := perform(h.Delete, http.MethodPost, "/dns-routes/delete", ""); decodeJSONBody(t, rr)["code"] != "MISSING_ID" {
			t.Fatal(rr.Body.String())
		}
		if rr := perform(h.Delete, http.MethodPost, "/dns-routes/delete?id=dns1", ""); rr.Code != http.StatusOK || len(fake.deletedIDs) != 1 {
			t.Fatal(rr.Body.String())
		}
		hDelErr := NewDNSRouteHandler(&fakeDNSRouteService{deleteFn: func(context.Context, string) error { return errors.New("boom") }}, nil)
		if rr := perform(hDelErr.Delete, http.MethodPost, "/dns-routes/delete?id=dns1", ""); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_DELETE_ERROR" {
			t.Fatal(rr.Body.String())
		}
		hDelListErr := NewDNSRouteHandler(&fakeDNSRouteService{listFn: func(context.Context) ([]dnsroute.DomainList, error) { return nil, errors.New("boom") }}, nil)
		if rr := perform(hDelListErr.Delete, http.MethodPost, "/dns-routes/delete?id=dns1", ""); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_LIST_ERROR" {
			t.Fatal(rr.Body.String())
		}

		if rr := perform(h.DeleteBatch, http.MethodGet, "/dns-routes/delete-batch", ""); rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
		if rr := perform(h.DeleteBatch, http.MethodPost, "/dns-routes/delete-batch", `{"ids":[]}`); decodeJSONBody(t, rr)["code"] != "MISSING_IDS" {
			t.Fatal(rr.Body.String())
		}
		rr := perform(h.DeleteBatch, http.MethodPost, "/dns-routes/delete-batch", `{"ids":["a","b"]}`)
		if rr.Code != http.StatusOK || len(fake.deleteBatchIDs) != 2 {
			t.Fatal(rr.Body.String())
		}
		hDBErr := NewDNSRouteHandler(&fakeDNSRouteService{deleteBatchFn: func(context.Context, []string) (int, error) { return 0, errors.New("boom") }}, nil)
		if rr := perform(hDBErr.DeleteBatch, http.MethodPost, "/dns-routes/delete-batch", `{"ids":["a"]}`); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_DELETE_BATCH_ERROR" {
			t.Fatal(rr.Body.String())
		}
		hDBListErr := NewDNSRouteHandler(&fakeDNSRouteService{listFn: func(context.Context) ([]dnsroute.DomainList, error) { return nil, errors.New("boom") }}, nil)
		if rr := perform(hDBListErr.DeleteBatch, http.MethodPost, "/dns-routes/delete-batch", `{"ids":["a"]}`); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_LIST_ERROR" {
			t.Fatal(rr.Body.String())
		}

		if rr := perform(h.SetEnabled, http.MethodGet, "/dns-routes/set-enabled?id=dns1", `{"enabled":false}`); rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
		if rr := perform(h.SetEnabled, http.MethodPost, "/dns-routes/set-enabled", `{"enabled":false}`); decodeJSONBody(t, rr)["code"] != "MISSING_ID" {
			t.Fatal(rr.Body.String())
		}
		rr = perform(h.SetEnabled, http.MethodPost, "/dns-routes/set-enabled?id=dns1", `{"enabled":false}`)
		if rr.Code != http.StatusOK || fake.setEnabledID != "dns1" || fake.setEnabledVal != false {
			t.Fatal(rr.Body.String())
		}
		hSEErr := NewDNSRouteHandler(&fakeDNSRouteService{setEnabledFn: func(context.Context, string, bool) error { return errors.New("boom") }}, nil)
		if rr := perform(hSEErr.SetEnabled, http.MethodPost, "/dns-routes/set-enabled?id=dns1", `{"enabled":true}`); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_SET_ENABLED_ERROR" {
			t.Fatal(rr.Body.String())
		}
		hSEListErr := NewDNSRouteHandler(&fakeDNSRouteService{listFn: func(context.Context) ([]dnsroute.DomainList, error) { return nil, errors.New("boom") }}, nil)
		if rr := perform(hSEListErr.SetEnabled, http.MethodPost, "/dns-routes/set-enabled?id=dns1", `{"enabled":true}`); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_LIST_ERROR" {
			t.Fatal(rr.Body.String())
		}
	})

	t.Run("CreateBatchBulkBackendRefresh", func(t *testing.T) {
		fake := &fakeDNSRouteService{}
		h := NewDNSRouteHandler(fake, nil)
		if rr := perform(h.CreateBatch, http.MethodGet, "/dns-routes/create-batch", "[]"); rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
		if rr := perform(h.CreateBatch, http.MethodPost, "/dns-routes/create-batch", `[]`); decodeJSONBody(t, rr)["code"] != "MISSING_LISTS" {
			t.Fatal(rr.Body.String())
		}
		rr := perform(h.CreateBatch, http.MethodPost, "/dns-routes/create-batch", `[ {"name":"A"}, {"name":"B"} ]`)
		if rr.Code != http.StatusOK || len(fake.created) != 2 {
			t.Fatal(rr.Body.String())
		}
		hCBE := NewDNSRouteHandler(&fakeDNSRouteService{createBatchFn: func(context.Context, []dnsroute.DomainList) ([]*dnsroute.DomainList, error) {
			return nil, errors.New("boom")
		}}, nil)
		if rr := perform(hCBE.CreateBatch, http.MethodPost, "/dns-routes/create-batch", `[{}]`); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_CREATE_BATCH_ERROR" {
			t.Fatal(rr.Body.String())
		}

		if rr := perform(h.BulkBackend, http.MethodGet, "/dns-routes/bulk-backend", ""); rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
		if rr := perform(h.BulkBackend, http.MethodPost, "/dns-routes/bulk-backend", `{"listIDs":["a"],"backend":"bad"}`); decodeJSONBody(t, rr)["code"] != "INVALID_BACKEND" {
			t.Fatal(rr.Body.String())
		}
		if rr := perform(h.BulkBackend, http.MethodPost, "/dns-routes/bulk-backend", `{"listIDs":[],"backend":"ndms"}`); decodeJSONBody(t, rr)["code"] != "EMPTY_LIST" {
			t.Fatal(rr.Body.String())
		}
		calls := 0
		hB := NewDNSRouteHandler(&fakeDNSRouteService{
			getFn: func(_ context.Context, id string) (*dnsroute.DomainList, error) {
				if id == "bad" {
					return nil, errors.New("x")
				}
				l := sampleDNSList(id, id)
				return &l, nil
			},
			updateFn: func(_ context.Context, l dnsroute.DomainList) (*dnsroute.DomainList, error) {
				if l.ID == "uerr" {
					return nil, errors.New("uerr")
				}
				if l.Backend == "hydraroute" {
					calls++
				}
				return &l, nil
			},
		}, nil)
		rr = perform(hB.BulkBackend, http.MethodPost, "/dns-routes/bulk-backend", `{"listIDs":["good","bad","uerr"],"backend":"hydraroute"}`)
		if rr.Code != http.StatusOK || calls == 0 {
			t.Fatal(rr.Body.String())
		}
		hBL := NewDNSRouteHandler(&fakeDNSRouteService{listFn: func(context.Context) ([]dnsroute.DomainList, error) { return nil, errors.New("boom") }}, nil)
		if rr := perform(hBL.BulkBackend, http.MethodPost, "/dns-routes/bulk-backend", `{"listIDs":["a"],"backend":"ndms"}`); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_LIST_ERROR" {
			t.Fatal(rr.Body.String())
		}

		// Все элементы упали → отказ и ноль публикаций: раньше ручка отвечала 200 со
		// свежим списком и слала bulk-backend, как будто что-то поменялось.
		pZero := newBusProbe(t)
		hZero := NewDNSRouteHandler(&fakeDNSRouteService{
			getFn: func(context.Context, string) (*dnsroute.DomainList, error) { return nil, errors.New("x") },
		}, nil)
		hZero.SetEventBus(pZero.bus())
		rr = perform(hZero.BulkBackend, http.MethodPost, "/dns-routes/bulk-backend", `{"listIDs":["a","b"],"backend":"hydraroute"}`)
		if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "BULK_BACKEND_FAILED" {
			t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
		}
		if inv := pZero.invalidated(); len(inv) != 0 {
			t.Fatalf("публикации при нуле переехавших: %v", inv)
		}

		if rr := perform(h.Refresh, http.MethodGet, "/dns-routes/refresh", ""); rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
		rr = perform(h.Refresh, http.MethodPost, "/dns-routes/refresh?id=dns1", "")
		if rr.Code != http.StatusOK || fake.refreshID != "dns1" {
			t.Fatal(rr.Body.String())
		}
		fake.refreshID = ""
		rr = perform(h.Refresh, http.MethodPost, "/dns-routes/refresh", "")
		if rr.Code != http.StatusOK || !fake.refreshAll {
			t.Fatal(rr.Body.String())
		}
		hR1 := NewDNSRouteHandler(&fakeDNSRouteService{refreshSubscriptionsFn: func(context.Context, string) error { return errors.New("boom") }}, nil)
		if rr := perform(hR1.Refresh, http.MethodPost, "/dns-routes/refresh?id=a", ""); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_REFRESH_ERROR" {
			t.Fatal(rr.Body.String())
		}
		hR2 := NewDNSRouteHandler(&fakeDNSRouteService{refreshAllSubscriptionsFn: func(context.Context) error { return errors.New("boom") }}, nil)
		if rr := perform(hR2.Refresh, http.MethodPost, "/dns-routes/refresh", ""); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_REFRESH_ALL_ERROR" {
			t.Fatal(rr.Body.String())
		}
		hR3 := NewDNSRouteHandler(&fakeDNSRouteService{listFn: func(context.Context) ([]dnsroute.DomainList, error) { return nil, errors.New("boom") }}, nil)
		if rr := perform(hR3.Refresh, http.MethodPost, "/dns-routes/refresh", ""); decodeJSONBody(t, rr)["code"] != "DNS_ROUTE_LIST_ERROR" {
			t.Fatal(rr.Body.String())
		}
	})
}

// Каждая мутирующая ручка DNS-маршрутов на успехе публикует ровно одну
// инвалидацию dns-routes со своим reason; на отказе службы — ничего.
// До этого пина шина в api-тестах не читалась ни разу: снятый publish был зелёным.
// Все Delete/Refresh/BulkBackend — POST (гарды в dnsroute.go:268, :489, :432).
func TestDNSRouteHandler_MutationsPublishInvalidation(t *testing.T) {
	const res = "routing.dnsRoutes"
	boom := errors.New("boom")
	cases := []struct {
		name   string
		call   func(h *DNSRouteHandler) http.HandlerFunc
		method string
		target string
		body   string
		reason string
		fail   *fakeDNSRouteService // фейк, роняющий службу
	}{
		{"Create", func(h *DNSRouteHandler) http.HandlerFunc { return h.Create }, "POST", "/dns-routes/create", `{"name":"Work","manualDomains":["corp.local"],"enabled":true}`, "create",
			&fakeDNSRouteService{createFn: func(context.Context, dnsroute.DomainList) (*dnsroute.DomainList, error) { return nil, boom }}},
		{"Update", func(h *DNSRouteHandler) http.HandlerFunc { return h.Update }, "POST", "/dns-routes/update?id=dns1", `{"name":"Work2"}`, "update",
			&fakeDNSRouteService{updateFn: func(context.Context, dnsroute.DomainList) (*dnsroute.DomainList, error) { return nil, boom }}},
		{"Delete", func(h *DNSRouteHandler) http.HandlerFunc { return h.Delete }, "POST", "/dns-routes/delete?id=dns1", "", "delete",
			&fakeDNSRouteService{deleteFn: func(context.Context, string) error { return boom }}},
		{"DeleteBatch", func(h *DNSRouteHandler) http.HandlerFunc { return h.DeleteBatch }, "POST", "/dns-routes/delete-batch", `{"ids":["dns1","dns2"]}`, "delete-batch",
			&fakeDNSRouteService{deleteBatchFn: func(context.Context, []string) (int, error) { return 0, boom }}},
		{"CreateBatch", func(h *DNSRouteHandler) http.HandlerFunc { return h.CreateBatch }, "POST", "/dns-routes/create-batch", `[{"name":"A"},{"name":"B"}]`, "create-batch",
			&fakeDNSRouteService{createBatchFn: func(context.Context, []dnsroute.DomainList) ([]*dnsroute.DomainList, error) { return nil, boom }}},
		{"SetEnabled", func(h *DNSRouteHandler) http.HandlerFunc { return h.SetEnabled }, "POST", "/dns-routes/enabled?id=dns1", `{"enabled":false}`, "set-enabled",
			&fakeDNSRouteService{setEnabledFn: func(context.Context, string, bool) error { return boom }}},
		{"Refresh", func(h *DNSRouteHandler) http.HandlerFunc { return h.Refresh }, "POST", "/dns-routes/refresh?id=dns1", "", "refresh-subscriptions",
			&fakeDNSRouteService{refreshSubscriptionsFn: func(context.Context, string) error { return boom }}},
		// BulkBackend: отказ отдельного Get/Update по элементу — `continue` (200 +
		// публикация, пока переехал хотя бы один); ноль переехавших — 400
		// BULK_BACKEND_FAILED без публикации; отказ List — DNS_ROUTE_LIST_ERROR.
		{"BulkBackend", func(h *DNSRouteHandler) http.HandlerFunc { return h.BulkBackend }, "POST", "/dns-routes/bulk-backend", `{"listIDs":["dns1"],"backend":"hydraroute"}`, "bulk-backend",
			&fakeDNSRouteService{listFn: func(context.Context) ([]dnsroute.DomainList, error) { return nil, boom }}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newBusProbe(t)
			h := NewDNSRouteHandler(&fakeDNSRouteService{}, nil)
			h.SetEventBus(p.bus())
			rr := perform(tc.call(h), tc.method, tc.target, tc.body)
			if rr.Code != 200 {
				t.Fatalf("успех ожидался, code=%d body=%s", rr.Code, rr.Body.String())
			}
			if got, want := p.invalidated(), []string{res + "/" + tc.reason}; !reflect.DeepEqual(got, want) {
				t.Fatalf("публикации на успехе = %v, want %v", got, want)
			}

			pf := newBusProbe(t)
			hf := NewDNSRouteHandler(tc.fail, nil)
			hf.SetEventBus(pf.bus())
			rr = perform(tc.call(hf), tc.method, tc.target, tc.body)
			if rr.Code == 200 {
				t.Fatalf("отказ службы обязан быть не-200: %s", rr.Body.String())
			}
			if got := pf.invalidated(); len(got) != 0 {
				t.Fatalf("на отказе публикаций быть не должно: %v", got)
			}
		})
	}
}
