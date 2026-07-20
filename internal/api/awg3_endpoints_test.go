package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/awg3endpoint"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// fakeAwg3Service satisfies the handler's Sync/ListTags interface and records
// how many times Sync ran (rollback assertions rely on the count).
type fakeAwg3Service struct {
	store   *awg3endpoint.Store
	syncErr error
	syncCnt int
}

func (f *fakeAwg3Service) Sync() error {
	f.syncCnt++
	return f.syncErr
}

func (f *fakeAwg3Service) ListTags() []awg3endpoint.TagInfo {
	list, _ := f.store.List()
	out := make([]awg3endpoint.TagInfo, 0, len(list))
	for _, r := range list {
		out = append(out, awg3endpoint.TagInfo{Tag: r.Tag, Kind: "awg3"})
	}
	return out
}

// fakeRuleLister returns a fixed rule set for the rename-conflict check.
type fakeRuleLister struct {
	rules []router.Rule
}

func (f *fakeRuleLister) ListRules(ctx context.Context) ([]router.Rule, error) {
	return f.rules, nil
}

// validEndpoint is a RouteBox envelope with S1-S4 ≥ 8 and a header_protection_key.
const validEndpoint = `{
  "success": true,
  "data": {
    "type": "awg",
    "private_key": "cGVlclByaXZhdGVLZXlCYXNlNjRFeGFtcGxlMDAwMDAwMD0=",
    "s1": 8, "s2": 8, "s3": 8, "s4": 8,
    "header_protection_key": "aGVhZGVyUHJvdGVjdGlvbktleUJhc2U2NEV4YW1wbGU9",
    "peers": [
      {
        "public_key": "c2VydmVyUHVibGljS2V5QmFzZTY0RXhhbXBsZTAwMD0=",
        "address": "vpn.example.com",
        "port": 51820
      }
    ]
  }
}`

// badS endpoint: header_protection_key present but S1<8 → Parse rejects.
const badSEndpoint = `{
  "type": "awg",
  "private_key": "cGVlclByaXZhdGVLZXlCYXNlNjRFeGFtcGxlMDAwMDAwMD0=",
  "s1": 4, "s2": 8, "s3": 8, "s4": 8,
  "header_protection_key": "aGVhZGVyUHJvdGVjdGlvbktleUJhc2U2NEV4YW1wbGU9",
  "peers": [
    {"public_key": "c2VydmVyUHVibGljS2V5QmFzZTY0RXhhbXBsZTAwMD0=", "address": "h", "port": 1}
  ]
}`

func newAwg3TestHandler(t *testing.T) (*Awg3Handler, *awg3endpoint.Store, *fakeAwg3Service, *fakeRuleLister) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "awg3.json")
	store := awg3endpoint.NewStore(path)
	svc := &fakeAwg3Service{store: store}
	rules := &fakeRuleLister{}
	h := NewAwg3Handler(store, svc, rules)
	return h, store, svc, rules
}

func decodeAwg3List(t *testing.T, body []byte) []Awg3TunnelDTO {
	t.Helper()
	var env struct {
		Success bool            `json:"success"`
		Data    []Awg3TunnelDTO `json:"data"`
		Error   bool            `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, body)
	}
	return env.Data
}

func TestAwg3Handler_ImportValid(t *testing.T) {
	h, _, svc, _ := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Handle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST valid: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.syncCnt != 1 {
		t.Fatalf("expected Sync called once, got %d", svc.syncCnt)
	}
	list := decodeAwg3List(t, rec.Body.Bytes())
	if len(list) != 1 {
		t.Fatalf("expected 1 record, got %d", len(list))
	}
	dto := list[0]
	if dto.Tag != "amsterdam" {
		t.Errorf("tag = %q, want amsterdam", dto.Tag)
	}
	if dto.Host != "vpn.example.com:51820" {
		t.Errorf("host = %q, want vpn.example.com:51820", dto.Host)
	}
	if !dto.HeaderProtection {
		t.Errorf("headerProtection = false, want true")
	}
	if dto.ID == "" || !strings.HasPrefix(dto.ID, "awg3-") {
		t.Errorf("id = %q, want awg3- prefix", dto.ID)
	}
	// DTO must not leak the raw private key.
	if strings.Contains(rec.Body.String(), "private_key") {
		t.Errorf("response leaks private_key: %s", rec.Body.String())
	}
}

func TestAwg3Handler_ImportBadSchema(t *testing.T) {
	h, store, svc, _ := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + badSEndpoint + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Handle(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST bad-S: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.syncCnt != 0 {
		t.Errorf("Sync must not run on parse failure, got %d", svc.syncCnt)
	}
	if n := store.Len(); n != 0 {
		t.Errorf("store must stay empty, got %d", n)
	}
}

func TestAwg3Handler_ImportDuplicateTag(t *testing.T) {
	h, _, _, _ := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body))
	h.Handle(httptest.NewRecorder(), req)

	// second import with same tag
	req2 := httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body))
	rec2 := httptest.NewRecorder()
	h.Handle(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("duplicate tag: code=%d body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestAwg3Handler_Delete(t *testing.T) {
	h, store, _, _ := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))
	id := decodeAwg3List(t, rec.Body.Bytes())[0].ID

	del := httptest.NewRecorder()
	h.Handle(del, httptest.NewRequest(http.MethodDelete, "/api/awg3-endpoints/"+id, nil))
	if del.Code != http.StatusOK {
		t.Fatalf("DELETE: code=%d body=%s", del.Code, del.Body.String())
	}
	if n := store.Len(); n != 0 {
		t.Errorf("store must be empty after delete, got %d", n)
	}
	if len(decodeAwg3List(t, del.Body.Bytes())) != 0 {
		t.Errorf("delete response list not empty")
	}
}

func TestAwg3Handler_Rename(t *testing.T) {
	h, store, _, _ := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))
	id := decodeAwg3List(t, rec.Body.Bytes())[0].ID

	patch := httptest.NewRecorder()
	h.Handle(patch, httptest.NewRequest(http.MethodPatch, "/api/awg3-endpoints/"+id,
		strings.NewReader(`{"tag":"berlin"}`)))
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH rename: code=%d body=%s", patch.Code, patch.Body.String())
	}
	rec2, _ := store.Get(id)
	if rec2.Tag != "berlin" {
		t.Errorf("tag after rename = %q, want berlin", rec2.Tag)
	}
}

func TestAwg3Handler_RenameConflict(t *testing.T) {
	h, store, _, rules := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))
	id := decodeAwg3List(t, rec.Body.Bytes())[0].ID

	// a routing rule references the old tag → rename must 409
	rules.rules = []router.Rule{{Action: "route", Outbound: "amsterdam"}}
	patch := httptest.NewRecorder()
	h.Handle(patch, httptest.NewRequest(http.MethodPatch, "/api/awg3-endpoints/"+id,
		strings.NewReader(`{"tag":"berlin"}`)))
	if patch.Code != http.StatusConflict {
		t.Fatalf("PATCH rename conflict: code=%d body=%s", patch.Code, patch.Body.String())
	}
	got, _ := store.Get(id)
	if got.Tag != "amsterdam" {
		t.Errorf("tag must be unchanged on conflict, got %q", got.Tag)
	}
}

// sanity: store file is written to disk (real store, not mock).
func TestAwg3Handler_PersistsToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awg3.json")
	store := awg3endpoint.NewStore(path)
	h := NewAwg3Handler(store, &fakeAwg3Service{store: store}, &fakeRuleLister{})

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	h.Handle(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file not written: %v", err)
	}
}
