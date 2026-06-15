package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestFakeIPSegments_List(t *testing.T) {
	lister := &fakePoolLister{pools: []query.DHCPPool{
		{Name: "_WEBADMIN", Network: "192.168.0.1/24", DNSServer: "192.168.0.1"},
		{Name: "_WEBADMIN_GUEST_AP", Network: "172.18.0.0/24", DNSServer: "172.18.0.2"},
	}}
	h := NewFakeIPSegmentsHandler(lister)

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
	h := NewFakeIPSegmentsHandler(lister)
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

func TestFakeIPSegments_MethodNotAllowed(t *testing.T) {
	h := NewFakeIPSegmentsHandler(&fakePoolLister{})
	rec := httptest.NewRecorder()
	h.ListSegments(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
