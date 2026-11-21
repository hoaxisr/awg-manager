package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ipinspect"
)

type stubInspectResolver struct {
	ptrs map[string][]string
	txts map[string][]string
}

func (s *stubInspectResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	return s.ptrs[addr], nil
}

func (s *stubInspectResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	return s.txts[name], nil
}

func TestIPInspectHandler_Get(t *testing.T) {
	svc := ipinspect.NewServiceWithResolver(&stubInspectResolver{
		ptrs: map[string][]string{"1.1.1.1": {"one.one.one.one."}},
		txts: map[string][]string{
			"1.1.1.1.origin.asn.cymru.com": {"13335 | 1.1.1.0/24 | AU | apnic | 2011-08-11"},
			"AS13335.asn.cymru.com":        {"13335 | US | arin | 2010-07-14 | CLOUDFLARENET, US"},
		},
	}, nil)
	h := NewIPInspectHandler(svc)

	t.Run("rejects non-GET with JSON envelope", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/diagnostics/ip-inspect?ip=1.1.1.1", nil)
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
		var env map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("expected JSON envelope on 405, got %q (%v)", rec.Body.String(), err)
		}
	})

	t.Run("rejects missing and invalid ip", func(t *testing.T) {
		for _, target := range []string{"/api/diagnostics/ip-inspect", "/api/diagnostics/ip-inspect?ip=invalid-host"} {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()
			h.Get(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s status = %d, want 400", target, rec.Code)
			}
		}
	})

	t.Run("returns inspected metadata", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/ip-inspect?ip=1.1.1.1", nil)
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var env struct {
			Success bool             `json:"success"`
			Data    ipinspect.Result `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !env.Success || env.Data.IP != "1.1.1.1" || env.Data.ASN != 13335 || env.Data.Prefix != "1.1.1.0/24" || env.Data.PTR != "one.one.one.one" {
			t.Fatalf("unexpected envelope: %+v", env)
		}
	})
}
