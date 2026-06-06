package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNATHandler_InvalidMode_Returns400 pins the review-commit guarantee:
// an unknown NAT mode is rejected at validation (BAD_REQUEST) BEFORE the
// service is invoked, not via the SetNATMode error path (NAT_FAILED). A zero
// handler with nil svc proves this: the handler returns before dereferencing
// h.svc, so reaching the service would nil-panic instead of producing 400.
func TestNATHandler_InvalidMode_Returns400(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown mode", `{"mode":"bogus"}`},
		{"empty body", `{}`},
		{"empty mode string", `{"mode":""}`},
	}
	h := &ManagedServerHandler{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/managed-servers/Wireguard0/nat", strings.NewReader(c.body))
			w := httptest.NewRecorder()
			h.NAT(w, r, "Wireguard0")
			if w.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400; body=%s", w.Code, w.Body.String())
			}
		})
	}
}
