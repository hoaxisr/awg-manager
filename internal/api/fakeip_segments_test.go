package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type fakePoolLister struct {
	pools []query.DHCPPool
	err   error
}

func (f *fakePoolLister) List(context.Context) ([]query.DHCPPool, error) {
	return f.pools, f.err
}

// fakeSegmentMutator records SetPoolDNS / ClearPoolDNS calls so the toggle
// handler tests can assert the pool + the servers it was driven with.
type fakeSegmentMutator struct {
	setPool    string
	setServers []string
	clearPool  string
	setErr     error
	clearErr   error
}

func (f *fakeSegmentMutator) SetPoolDNS(_ context.Context, pool string, servers []string) error {
	f.setPool = pool
	f.setServers = servers
	return f.setErr
}

func (f *fakeSegmentMutator) ClearPoolDNS(_ context.Context, pool string) error {
	f.clearPool = pool
	return f.clearErr
}

func newToggleHandler(mut SegmentDNSMutator) *FakeIPSegmentsHandler {
	return NewFakeIPSegmentsHandler(&fakePoolLister{}, mut)
}

func TestFakeIPSegments_List(t *testing.T) {
	lister := &fakePoolLister{pools: []query.DHCPPool{
		{Name: "_WEBADMIN", Network: "192.168.0.1/24", DNSServer: "192.168.0.1"},
		{Name: "_WEBADMIN_GUEST_AP", Network: "172.18.0.0/24", DNSServer: "172.18.0.2"},
	}}
	h := NewFakeIPSegmentsHandler(lister, &fakeSegmentMutator{})

	rec := httptest.NewRecorder()
	h.ListSegments(rec, httptest.NewRequest(http.MethodGet, "/api/singbox/fakeip/segments", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool               `json:"success"`
		Data    []FakeIPSegmentDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success || len(resp.Data) != 2 {
		t.Fatalf("resp = %#v", resp)
	}
	// Sorted by pool.
	if resp.Data[0].Pool != "_WEBADMIN" || resp.Data[0].Subnet != "192.168.0.1/24" {
		t.Fatalf("seg0 = %#v", resp.Data[0])
	}
	if resp.Data[0].InFakeip {
		t.Fatalf("seg0 should NOT be inFakeip (dns 192.168.0.1)")
	}
	// Guest pool advertises the fakeip-tun DNS (172.18.0.2) → inFakeip true.
	if resp.Data[1].DNSServer != "172.18.0.2" || !resp.Data[1].InFakeip {
		t.Fatalf("seg1 should be inFakeip: %#v", resp.Data[1])
	}
}

func TestFakeIPSegments_EmptyDNSNotInFakeip(t *testing.T) {
	lister := &fakePoolLister{pools: []query.DHCPPool{
		{Name: "P", Network: "10.0.0.0/24", DNSServer: ""},
	}}
	h := NewFakeIPSegmentsHandler(lister, &fakeSegmentMutator{})
	rec := httptest.NewRecorder()
	h.ListSegments(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	var resp struct {
		Data []FakeIPSegmentDTO `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].InFakeip {
		t.Fatalf("empty dns must not be inFakeip: %#v", resp.Data)
	}
}

func TestFakeIPSegments_ListMethodNotAllowed(t *testing.T) {
	h := NewFakeIPSegmentsHandler(&fakePoolLister{}, &fakeSegmentMutator{})
	rec := httptest.NewRecorder()
	h.ListSegments(rec, httptest.NewRequest(http.MethodPut, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// Serve dispatches GET→ListSegments, POST→ToggleSegment on the same path.
func TestFakeIPSegments_ServeDispatch(t *testing.T) {
	mut := &fakeSegmentMutator{}
	h := NewFakeIPSegmentsHandler(&fakePoolLister{pools: []query.DHCPPool{}}, mut)

	getRec := httptest.NewRecorder()
	h.Serve(getRec, httptest.NewRequest(http.MethodGet, "/", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", getRec.Code)
	}

	postRec := httptest.NewRecorder()
	h.Serve(postRec, httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"pool":"_WEBADMIN","inFakeip":true}`)))
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, body=%s", postRec.Code, postRec.Body.String())
	}

	putRec := httptest.NewRecorder()
	h.Serve(putRec, httptest.NewRequest(http.MethodPut, "/", nil))
	if putRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", putRec.Code)
	}
}

func TestFakeIPSegments_ToggleSet(t *testing.T) {
	mut := &fakeSegmentMutator{}
	h := newToggleHandler(mut)

	rec := httptest.NewRecorder()
	h.ToggleSegment(rec, httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"pool":"_WEBADMIN","inFakeip":true}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if mut.setPool != "_WEBADMIN" {
		t.Fatalf("SetPoolDNS pool = %q, want _WEBADMIN", mut.setPool)
	}
	// Must drive the pool with the fakeip-tun DNS (.2 of the default /30).
	if len(mut.setServers) != 1 || mut.setServers[0] != "172.18.0.2" {
		t.Fatalf("SetPoolDNS servers = %#v, want [172.18.0.2]", mut.setServers)
	}
	if mut.clearPool != "" {
		t.Fatalf("ClearPoolDNS should not have been called: %q", mut.clearPool)
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Pool     string `json:"pool"`
			InFakeip bool   `json:"inFakeip"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success || resp.Data.Pool != "_WEBADMIN" || !resp.Data.InFakeip {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestFakeIPSegments_ToggleClear(t *testing.T) {
	mut := &fakeSegmentMutator{}
	h := newToggleHandler(mut)

	rec := httptest.NewRecorder()
	h.ToggleSegment(rec, httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"pool":"_WEBADMIN","inFakeip":false}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if mut.clearPool != "_WEBADMIN" {
		t.Fatalf("ClearPoolDNS pool = %q, want _WEBADMIN", mut.clearPool)
	}
	if mut.setPool != "" {
		t.Fatalf("SetPoolDNS should not have been called: %q", mut.setPool)
	}
}

func TestFakeIPSegments_ToggleEmptyPool400(t *testing.T) {
	mut := &fakeSegmentMutator{}
	h := newToggleHandler(mut)

	rec := httptest.NewRecorder()
	h.ToggleSegment(rec, httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"pool":"","inFakeip":true}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if mut.setPool != "" || mut.clearPool != "" {
		t.Fatalf("mutator must not be called on empty pool")
	}
}

func TestFakeIPSegments_ToggleMutatorError500(t *testing.T) {
	mut := &fakeSegmentMutator{setErr: errors.New("ndms boom")}
	h := newToggleHandler(mut)

	rec := httptest.NewRecorder()
	h.ToggleSegment(rec, httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"pool":"_WEBADMIN","inFakeip":true}`)))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	// The error must name the pool (FE-spec 2.2 "понятная ошибка").
	if !strings.Contains(rec.Body.String(), "_WEBADMIN") {
		t.Fatalf("error body should name the pool: %s", rec.Body.String())
	}
}

func TestFakeIPSegments_ToggleMethodNotAllowed(t *testing.T) {
	h := newToggleHandler(&fakeSegmentMutator{})
	rec := httptest.NewRecorder()
	h.ToggleSegment(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
