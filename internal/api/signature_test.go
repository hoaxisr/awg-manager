package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSignatureGenerate_ProfileAndNoMTU(t *testing.T) {
	h := NewSignatureHandler()
	body := strings.NewReader(`{"protocol":"dns"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/signature/generate", body)
	rec := httptest.NewRecorder()
	h.Generate(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	var env SignatureGenerateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Protocol != "dns" || env.Data.Packets.I1 == "" || env.Data.ByteSize == 0 {
		t.Fatalf("%+v", env.Data)
	}

	rec = httptest.NewRecorder()
	h.Generate(rec, httptest.NewRequest(http.MethodPost, "/api/signature/generate", strings.NewReader(`{"protocol":"tls"}`)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "UNKNOWN_PROTOCOL") {
		t.Fatalf("tls: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.Generate(rec, httptest.NewRequest(http.MethodPost, "/api/signature/generate", strings.NewReader(`{"protocol":"dns","mtu":1280}`)))
	if rec.Code != 400 {
		t.Fatalf("mtu is gone, unknown field must be rejected: %d", rec.Code)
	}
}
