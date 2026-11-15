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

func TestSignatureGenerate_DomainAndPreset(t *testing.T) {
	h := NewSignatureHandler()

	rec := httptest.NewRecorder()
	h.Generate(rec, httptest.NewRequest(http.MethodPost, "/api/signature/generate", strings.NewReader(`{"protocol":"quic_initial","domain":"gosuslugi.ru"}`)))
	if rec.Code != 200 {
		t.Fatalf("custom domain code %d: %s", rec.Code, rec.Body)
	}
	var env SignatureGenerateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Domain != "gosuslugi.ru" || env.Data.Packets.I1 == "" {
		t.Fatalf("unexpected custom domain response: %+v", env.Data)
	}

	rec = httptest.NewRecorder()
	h.Generate(rec, httptest.NewRequest(http.MethodPost, "/api/signature/generate", strings.NewReader(`{"protocol":"dtls","hostPreset":"ru_whitelist"}`)))
	if rec.Code != 200 {
		t.Fatalf("ru_whitelist code %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Domain == "" || env.Data.Packets.I1 == "" {
		t.Fatalf("unexpected preset response: %+v", env.Data)
	}

	rec = httptest.NewRecorder()
	h.Generate(rec, httptest.NewRequest(http.MethodPost, "/api/signature/generate", strings.NewReader(`{"protocol":"dns","domain":"bad"}`)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "INVALID_DOMAIN") {
		t.Fatalf("bad domain: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.Generate(rec, httptest.NewRequest(http.MethodPost, "/api/signature/generate", strings.NewReader(`{"protocol":"dns","hostPreset":"mars"}`)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "INVALID_HOST_PRESET") {
		t.Fatalf("bad preset: %d %s", rec.Code, rec.Body)
	}
}
