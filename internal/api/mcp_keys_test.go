package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func newMcpKeysHandler(t *testing.T) (*McpKeysHandler, *storage.McpKeyStore) {
	t.Helper()
	store := storage.NewMcpKeyStore(t.TempDir())
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	return NewMcpKeysHandler(store, nil), store
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestMcpKeys_CreateListRevoke(t *testing.T) {
	h, store := newMcpKeysHandler(t)

	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/keys/create", strings.NewReader(`{"name":"laptop"}`)))
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	data := decode(t, rec)["data"].(map[string]any)
	key, _ := data["key"].(string)
	id, _ := data["id"].(string)
	if !strings.HasPrefix(key, storage.McpKeyPrefix) || id == "" || data["name"] != "laptop" {
		t.Fatalf("data = %v", data)
	}
	if _, ok := store.Verify(key); !ok {
		t.Fatal("created key does not verify")
	}

	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/keys", nil))
	keys := decode(t, rec)["data"].(map[string]any)["keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("keys = %v", keys)
	}
	if k := keys[0].(map[string]any); k["hash"] != nil || k["key"] != nil {
		t.Fatalf("list leaks secret material: %v", k)
	}

	rec = httptest.NewRecorder()
	h.Revoke(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/keys/revoke", strings.NewReader(`{"id":"`+id+`"}`)))
	if rec.Code != 200 {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := store.Verify(key); ok {
		t.Fatal("revoked key still verifies")
	}

	rec = httptest.NewRecorder()
	h.Revoke(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/keys/revoke", strings.NewReader(`{"id":"`+id+`"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoke twice: %d", rec.Code)
	}
}

func TestMcpKeys_Validation(t *testing.T) {
	h, _ := newMcpKeysHandler(t)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/keys/create", strings.NewReader(`{"name":"  "}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty name: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/keys", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("list POST: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/keys/create", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("create GET: %d", rec.Code)
	}
}
