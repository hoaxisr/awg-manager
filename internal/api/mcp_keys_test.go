package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

// TestMcpKeys_StorageErrorsSurfaceAs500 forces a persistence failure (an
// unwritable data dir) to confirm the handler tells it apart from a
// validation failure: an invalid name still 400s as MCP_KEY_INVALID_NAME,
// but an infrastructure failure (bad name aside) must 500, not 400 — and a
// revoke of an unknown id must still 404 even under the same unwritable dir,
// since the not-found check happens before the failing save.
func TestMcpKeys_StorageErrorsSurfaceAs500(t *testing.T) {
	if os.Geteuid() == 0 {
		// The handler cannot reach the store's directory to break writes
		// any other way, so this one runs only on non-root CI (GitHub
		// Actions); the local Docker run skips it. The store-level
		// rollback tests in internal/storage do run as root.
		t.Skip("root ignores directory permission bits")
	}
	dataDir := t.TempDir()
	store := storage.NewMcpKeyStore(dataDir)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	h := NewMcpKeysHandler(store, nil)

	// Create a key while the dir is still writable, so Revoke below has a
	// real id to target once the dir becomes unwritable.
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/keys/create", strings.NewReader(`{"name":"laptop"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup create: %d %s", rec.Code, rec.Body.String())
	}
	id, _ := decode(t, rec)["data"].(map[string]any)["id"].(string)
	if id == "" {
		t.Fatal("setup create: no id")
	}

	if err := os.Chmod(dataDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dataDir, 0o700) })

	// Invalid name still 400s, even though the dir is unwritable — name
	// validation runs before the store ever tries to persist.
	rec = httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/keys/create", strings.NewReader(`{"name":"  "}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid name under unwritable dir: %d %s", rec.Code, rec.Body.String())
	}
	if code, _ := decode(t, rec)["code"].(string); code != "MCP_KEY_INVALID_NAME" {
		t.Fatalf("invalid name code = %q, want MCP_KEY_INVALID_NAME", code)
	}

	// Valid name, unwritable dir: 500, not 400/MCP_KEY_INVALID_NAME.
	rec = httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/keys/create", strings.NewReader(`{"name":"phone"}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("create with unwritable dir: %d %s", rec.Code, rec.Body.String())
	}
	if code, _ := decode(t, rec)["code"].(string); code != "MCP_KEY_CREATE_ERROR" {
		t.Fatalf("create error code = %q, want MCP_KEY_CREATE_ERROR", code)
	}

	// Revoke of an existing key under an unwritable dir: 500, not 400/404.
	rec = httptest.NewRecorder()
	h.Revoke(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/keys/revoke", strings.NewReader(`{"id":"`+id+`"}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("revoke with unwritable dir: %d %s", rec.Code, rec.Body.String())
	}
	if code, _ := decode(t, rec)["code"].(string); code != "MCP_KEY_REVOKE_ERROR" {
		t.Fatalf("revoke error code = %q, want MCP_KEY_REVOKE_ERROR", code)
	}

	// Revoke of an unknown id still 404s under the same unwritable dir.
	rec = httptest.NewRecorder()
	h.Revoke(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/keys/revoke", strings.NewReader(`{"id":"does-not-exist"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown id under unwritable dir: %d %s", rec.Code, rec.Body.String())
	}
}
