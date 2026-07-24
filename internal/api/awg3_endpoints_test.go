package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/awg3endpoint"
	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// fakeAwg3Service satisfies the handler's Sync interface and records how many
// times Sync ran (rollback assertions rely on the count).
type fakeAwg3Service struct {
	syncErr error
	syncCnt int
}

func (f *fakeAwg3Service) Sync() error {
	f.syncCnt++
	return f.syncErr
}

// fakeRuleLister returns a fixed rule set for the rename-conflict check.
// err, when set, simulates a transient router failure.
type fakeRuleLister struct {
	rules []router.Rule
	err   error
}

func (f *fakeRuleLister) ListRules(ctx context.Context) ([]router.Rule, error) {
	return f.rules, f.err
}

// fakeOutboundLister feeds the early tag-collision check with a fixed set of
// foreign outbound tags (subscription / 15-awg / composite).
type fakeOutboundLister struct {
	tags []string
	err  error
}

func (f *fakeOutboundLister) ListTags(ctx context.Context) ([]awgoutbounds.TagInfo, error) {
	out := make([]awgoutbounds.TagInfo, 0, len(f.tags))
	for _, t := range f.tags {
		out = append(out, awgoutbounds.TagInfo{Tag: t})
	}
	return out, f.err
}

// validEndpoint is a RouteBox envelope with S1-S4 ≥ 12 and a header_protection_key.
const validEndpoint = `{
  "success": true,
  "data": {
    "type": "awg",
    "private_key": "cGVlclByaXZhdGVLZXlCYXNlNjRFeGFtcGxlMDAwMDAwMD0=",
    "s1": 12, "s2": 12, "s3": 12, "s4": 12,
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

// badS endpoint: header_protection_key present but S1<12 → Parse rejects.
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
	svc := &fakeAwg3Service{}
	rules := &fakeRuleLister{}
	h := NewAwg3Handler(store, svc, rules, nil)
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

// awg3RecordToDTO must surface AWG3 device-timers when present, and omit them
// (leave empty → dropped by omitempty) when the config carries none.
func TestAwg3RecordToDTO_Timers(t *testing.T) {
	withTimers := awg3endpoint.Record{
		ID:  "awg3-abc",
		Tag: "amsterdam",
		Endpoint: json.RawMessage(`{"type":"awg","header_protection_key":"h",` +
			`"rekey_timeout":"5","reject_after_time":"180",` +
			`"keepalive_timeout":"25","max_handshake_attempts":"5",` +
			`"peers":[{"address":"vpn.example.com","port":51820}]}`),
	}
	dto := awg3RecordToDTO(withTimers)
	if dto.RekeyTimeout != "5" || dto.RejectAfterTime != "180" ||
		dto.KeepaliveTimeout != "25" || dto.MaxHandshakeAttempts != "5" {
		t.Fatalf("timers not surfaced: %+v", dto)
	}
	if b, _ := json.Marshal(dto); !strings.Contains(string(b), "rekeyTimeout") {
		t.Fatalf("timers must appear in JSON: %s", b)
	}

	noTimers := awg3endpoint.Record{
		ID:       "awg3-xyz",
		Tag:      "berlin",
		Endpoint: json.RawMessage(`{"type":"awg","peers":[{"address":"h","port":1}]}`),
	}
	dto2 := awg3RecordToDTO(noTimers)
	if dto2.RekeyTimeout != "" || dto2.RejectAfterTime != "" ||
		dto2.KeepaliveTimeout != "" || dto2.MaxHandshakeAttempts != "" {
		t.Fatalf("absent timers must stay empty: %+v", dto2)
	}
	if b, _ := json.Marshal(dto2); strings.Contains(string(b), "rekeyTimeout") {
		t.Fatalf("omitempty must drop absent timers from JSON: %s", b)
	}
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

// A referenced tag blocks delete with 409 before any store mutation or Sync.
func TestAwg3Handler_DeleteConflict(t *testing.T) {
	h, store, svc, rules := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))
	id := decodeAwg3List(t, rec.Body.Bytes())[0].ID

	// a routing rule references the tag → delete must 409
	rules.rules = []router.Rule{{Action: "route", Outbound: "amsterdam"}}
	svc.syncCnt = 0
	del := httptest.NewRecorder()
	h.Handle(del, httptest.NewRequest(http.MethodDelete, "/api/awg3-endpoints/"+id, nil))
	if del.Code != http.StatusConflict {
		t.Fatalf("DELETE conflict: code=%d body=%s", del.Code, del.Body.String())
	}
	if _, ok := store.Get(id); !ok {
		t.Errorf("record must survive a blocked delete")
	}
	if svc.syncCnt != 0 {
		t.Errorf("Sync must not run on blocked delete, got %d", svc.syncCnt)
	}
}

// A ListRules failure during delete surfaces as an honest 500, not a false 409.
func TestAwg3Handler_DeleteListRulesError(t *testing.T) {
	h, store, svc, rules := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))
	id := decodeAwg3List(t, rec.Body.Bytes())[0].ID

	rules.err = errors.New("router unreachable")
	svc.syncCnt = 0
	del := httptest.NewRecorder()
	h.Handle(del, httptest.NewRequest(http.MethodDelete, "/api/awg3-endpoints/"+id, nil))
	if del.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE ListRules-fail: code=%d body=%s", del.Code, del.Body.String())
	}
	if _, ok := store.Get(id); !ok {
		t.Errorf("record must survive when the reference check fails")
	}
	if svc.syncCnt != 0 {
		t.Errorf("Sync must not run when the reference check fails, got %d", svc.syncCnt)
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

// Sync-failure rollback: a rejected import is undone, store stays empty.
func TestAwg3Handler_ImportSyncRollback(t *testing.T) {
	h, store, svc, _ := newAwg3TestHandler(t)
	svc.syncErr = errors.New("sing-box check failed")

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST sync-fail: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.syncCnt != 1 {
		t.Errorf("expected Sync called once, got %d", svc.syncCnt)
	}
	if n := store.Len(); n != 0 {
		t.Errorf("store must be rolled back to empty, got %d", n)
	}
}

// Sync-failure rollback: a rejected delete restores the record.
func TestAwg3Handler_DeleteSyncRollback(t *testing.T) {
	h, store, svc, _ := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))
	id := decodeAwg3List(t, rec.Body.Bytes())[0].ID

	svc.syncErr = errors.New("sing-box check failed")
	del := httptest.NewRecorder()
	h.Handle(del, httptest.NewRequest(http.MethodDelete, "/api/awg3-endpoints/"+id, nil))

	if del.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE sync-fail: code=%d body=%s", del.Code, del.Body.String())
	}
	if _, ok := store.Get(id); !ok {
		t.Errorf("record must be restored after failed delete")
	}
	if n := store.Len(); n != 1 {
		t.Errorf("store must hold the restored record, got %d", n)
	}
}

// Sync-failure rollback: a rejected rename reverts the tag to its old value.
func TestAwg3Handler_RenameSyncRollback(t *testing.T) {
	h, store, svc, _ := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))
	id := decodeAwg3List(t, rec.Body.Bytes())[0].ID

	svc.syncErr = errors.New("sing-box check failed")
	patch := httptest.NewRecorder()
	h.Handle(patch, httptest.NewRequest(http.MethodPatch, "/api/awg3-endpoints/"+id,
		strings.NewReader(`{"tag":"berlin"}`)))

	if patch.Code != http.StatusInternalServerError {
		t.Fatalf("PATCH sync-fail: code=%d body=%s", patch.Code, patch.Body.String())
	}
	got, _ := store.Get(id)
	if got.Tag != "amsterdam" {
		t.Errorf("tag must be rolled back to amsterdam, got %q", got.Tag)
	}
}

// A ListRules failure must surface as an honest 500, not a false 409, and
// leave the tag unchanged.
func TestAwg3Handler_RenameListRulesError(t *testing.T) {
	h, store, svc, rules := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))
	id := decodeAwg3List(t, rec.Body.Bytes())[0].ID

	rules.err = errors.New("router unreachable")
	svc.syncCnt = 0
	patch := httptest.NewRecorder()
	h.Handle(patch, httptest.NewRequest(http.MethodPatch, "/api/awg3-endpoints/"+id,
		strings.NewReader(`{"tag":"berlin"}`)))

	if patch.Code != http.StatusInternalServerError {
		t.Fatalf("PATCH ListRules-fail: code=%d body=%s", patch.Code, patch.Body.String())
	}
	got, _ := store.Get(id)
	if got.Tag != "amsterdam" {
		t.Errorf("tag must be unchanged on ListRules error, got %q", got.Tag)
	}
	if svc.syncCnt != 0 {
		t.Errorf("Sync must not run when the reference check fails, got %d", svc.syncCnt)
	}
}

// A DELETE / PATCH on an unknown id is a 404, not a 400.
func TestAwg3Handler_NotFound(t *testing.T) {
	h, _, svc, _ := newAwg3TestHandler(t)

	del := httptest.NewRecorder()
	h.Handle(del, httptest.NewRequest(http.MethodDelete, "/api/awg3-endpoints/awg3-nope", nil))
	if del.Code != http.StatusNotFound {
		t.Fatalf("DELETE unknown: code=%d body=%s", del.Code, del.Body.String())
	}

	patch := httptest.NewRecorder()
	h.Handle(patch, httptest.NewRequest(http.MethodPatch, "/api/awg3-endpoints/awg3-nope",
		strings.NewReader(`{"tag":"berlin"}`)))
	if patch.Code != http.StatusNotFound {
		t.Fatalf("PATCH unknown: code=%d body=%s", patch.Code, patch.Body.String())
	}
	if svc.syncCnt != 0 {
		t.Errorf("Sync must not run for a not-found id, got %d", svc.syncCnt)
	}
}

// Import of a tag already owned by a foreign outbound (subscription / 15-awg /
// composite) is rejected early with a clear 400, before Sync.
func TestAwg3Handler_ImportOutboundTagCollision(t *testing.T) {
	h, store, svc, _ := newAwg3TestHandler(t)
	h.SetOutboundTagLister(&fakeOutboundLister{tags: []string{"amsterdam"}})

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("import outbound-collision: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.syncCnt != 0 {
		t.Errorf("Sync must not run when the tag collides, got %d", svc.syncCnt)
	}
	if n := store.Len(); n != 0 {
		t.Errorf("store must stay empty on collision, got %d", n)
	}
}

// Rename onto a foreign outbound tag is rejected early with a 400.
func TestAwg3Handler_RenameOutboundTagCollision(t *testing.T) {
	h, store, _, _ := newAwg3TestHandler(t)
	h.SetOutboundTagLister(&fakeOutboundLister{tags: []string{"berlin"}})

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))
	id := decodeAwg3List(t, rec.Body.Bytes())[0].ID

	patch := httptest.NewRecorder()
	h.Handle(patch, httptest.NewRequest(http.MethodPatch, "/api/awg3-endpoints/"+id,
		strings.NewReader(`{"tag":"berlin"}`)))
	if patch.Code != http.StatusBadRequest {
		t.Fatalf("rename outbound-collision: code=%d body=%s", patch.Code, patch.Body.String())
	}
	if got, _ := store.Get(id); got.Tag != "amsterdam" {
		t.Errorf("tag must be unchanged on collision, got %q", got.Tag)
	}
}

// Renaming a record to its own tag stays OK even though the outbound catalog
// reports that tag (the merged catalog includes the awg3 tags themselves).
func TestAwg3Handler_RenameToOwnTagOK(t *testing.T) {
	h, _, _, _ := newAwg3TestHandler(t)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))
	id := decodeAwg3List(t, rec.Body.Bytes())[0].ID

	// The catalog reports this record's own tag (merged catalog includes awg3
	// tags); rename to the same tag must still pass the collision guard.
	h.SetOutboundTagLister(&fakeOutboundLister{tags: []string{"amsterdam"}})
	patch := httptest.NewRecorder()
	h.Handle(patch, httptest.NewRequest(http.MethodPatch, "/api/awg3-endpoints/"+id,
		strings.NewReader(`{"tag":"amsterdam"}`)))
	if patch.Code != http.StatusOK {
		t.Fatalf("rename to own tag: code=%d body=%s", patch.Code, patch.Body.String())
	}
}

// sanity: store file is written to disk (real store, not mock).
func TestAwg3Handler_PersistsToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awg3.json")
	store := awg3endpoint.NewStore(path)
	h := NewAwg3Handler(store, &fakeAwg3Service{}, &fakeRuleLister{}, nil)

	body := `{"tag":"amsterdam","config":` + validEndpoint + `}`
	h.Handle(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/awg3-endpoints", strings.NewReader(body)))

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file not written: %v", err)
	}
}
