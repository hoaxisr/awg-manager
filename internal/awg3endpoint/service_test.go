package awg3endpoint

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	obox "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// recordingLogger captures AppLog calls so a test can assert a skip was traced.
type recordingLogger struct{ entries []string }

func (r *recordingLogger) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	r.entries = append(r.entries, action+" "+target+" "+message)
}

type fakeOrch struct {
	saved map[obox.Slot][]byte
	res   obox.ValidationResult // нулевой = Ok()==true (нет Errors)
}

func (f *fakeOrch) SaveAndValidate(slot obox.Slot, data []byte) (obox.ValidationResult, error) {
	if f.saved == nil {
		f.saved = map[obox.Slot][]byte{}
	}
	f.saved[slot] = data
	return f.res, nil
}

// newTestStore возвращает *Store в tempdir с 1 записью tag="AMS", endpoint
// которой содержит "tag":"peer-1" — чтобы тест доказал перезапись тега.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "awg3.json"))
	if err := store.Add(Record{
		ID:       "id-1",
		Tag:      "AMS",
		Endpoint: json.RawMessage(`{"type":"awg","tag":"peer-1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestService_Sync_MaterializesEndpointsSlot(t *testing.T) {
	store := newTestStore(t) // helper: Awg3Store с 1 записью tag="AMS", endpoint содержит "tag":"peer-1"
	orch := &fakeOrch{}
	svc := NewService(store, orch, nil)
	if err := svc.Sync(); err != nil {
		t.Fatal(err)
	}
	var slot struct {
		Endpoints []map[string]any `json:"endpoints"`
	}
	if err := json.Unmarshal(orch.saved[obox.SlotAwg3], &slot); err != nil {
		t.Fatal(err)
	}
	if len(slot.Endpoints) != 1 {
		t.Fatalf("endpoints = %v", slot.Endpoints)
	}
	// tag перезаписан на Record.Tag (человекочитаемый), не исходный "peer-1"
	if slot.Endpoints[0]["tag"] != "AMS" {
		t.Fatalf("tag not overwritten: %v", slot.Endpoints[0]["tag"])
	}
}

// A corrupt endpoint record is skipped (Sync still succeeds), only the valid
// record reaches the slot, and the skip is logged at warn level.
func TestService_Sync_SkipsCorruptRecordWithLog(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "awg3.json"))
	// Valid JSON (so the store persists it) but not an object → the Sync
	// unmarshal into map[string]json.RawMessage fails and the record is skipped.
	if err := store.Add(Record{ID: "bad", Tag: "BROKEN", Endpoint: json.RawMessage(`[1,2,3]`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Record{ID: "ok", Tag: "AMS", Endpoint: json.RawMessage(`{"type":"awg"}`)}); err != nil {
		t.Fatal(err)
	}
	orch := &fakeOrch{}
	log := &recordingLogger{}
	svc := NewService(store, orch, log)
	if err := svc.Sync(); err != nil {
		t.Fatalf("Sync must succeed despite a corrupt record: %v", err)
	}
	var slot struct {
		Endpoints []map[string]any `json:"endpoints"`
	}
	if err := json.Unmarshal(orch.saved[obox.SlotAwg3], &slot); err != nil {
		t.Fatal(err)
	}
	if len(slot.Endpoints) != 1 || slot.Endpoints[0]["tag"] != "AMS" {
		t.Fatalf("slot must contain only the valid record, got %v", slot.Endpoints)
	}
	if len(log.entries) != 1 {
		t.Fatalf("expected 1 warn for the skipped record, got %v", log.entries)
	}
}

// SaveAndValidate возвращает (res, nil) даже когда конфиг отвергнут (res не
// Ok). Sync обязан превратить это в ошибку с текстом валидации, иначе handler
// не откатит только что добавленную запись.
func TestService_Sync_RejectedConfigReturnsError(t *testing.T) {
	store := newTestStore(t)
	orch := &fakeOrch{res: obox.ValidationResult{Errors: []obox.ValidationError{{
		Slot:    obox.SlotAwg3,
		Kind:    "duplicate-outbound",
		Tag:     "AMS",
		Message: "also declared in [15-awg]",
	}}}}
	err := NewService(store, orch, nil).Sync()
	if err == nil {
		t.Fatal("Sync must return an error when the config is rejected")
	}
	if want := orch.res.Error(); !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, must contain validation text %q", err, want)
	}
}

func TestService_ListTags(t *testing.T) {
	svc := NewService(newTestStore(t), &fakeOrch{}, nil)
	tags := svc.ListTags()
	if len(tags) != 1 || tags[0].Tag != "AMS" || tags[0].Kind != "awg3" {
		t.Fatalf("tags = %v", tags)
	}
}
