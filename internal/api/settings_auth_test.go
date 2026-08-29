package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newSettingsHandlerFromRaw seeds settings.json with raw JSON before the store
// first loads it, so tests can exercise Load-time self-heal (e.g. a stored
// sessionTtlHours of 0 left by a downgrade). Returns a handler + the healed
// store.
func newSettingsHandlerFromRaw(t *testing.T, raw string) (*SettingsHandler, *storage.SettingsStore) {
	t.Helper()
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "settings.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}
	store := storage.NewSettingsStore(tmp)
	if _, err := store.Load(); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	return NewSettingsHandler(store, nil), store
}

func postSettingsUpdate(t *testing.T, h *SettingsHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/settings/update", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	return rec
}

func TestUpdate_SessionTtlOutOfRangeRejected(t *testing.T) {
	for _, body := range []string{
		`{"sessionTtlHours":0}`,
		`{"sessionTtlHours":-5}`,
		`{"sessionTtlHours":721}`,
	} {
		t.Run(body, func(t *testing.T) {
			h, store := newSettingsHandlerForTest(t)
			rec := postSettingsUpdate(t, h, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "INVALID_SESSION_TTL") {
				t.Fatalf("missing INVALID_SESSION_TTL, body=%s", rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "время жизни сессии должно быть от 1 до 720 часов") {
				t.Fatalf("missing Russian validation message, body=%s", rec.Body.String())
			}
			got, _ := store.Get()
			if got.SessionTtlHours != 24 {
				t.Fatalf("stored sessionTtlHours = %d, want untouched 24", got.SessionTtlHours)
			}
		})
	}
}

func TestUpdate_SessionTtlBoundaryValuesAccepted(t *testing.T) {
	for _, ttl := range []int{1, 720} {
		h, store := newSettingsHandlerForTest(t)
		body := `{"sessionTtlHours":` + strconv.Itoa(ttl) + `}`
		rec := postSettingsUpdate(t, h, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("ttl=%d: status = %d, want 200, body=%s", ttl, rec.Code, rec.Body.String())
		}
		got, _ := store.Get()
		if got.SessionTtlHours != ttl {
			t.Fatalf("stored sessionTtlHours = %d, want %d", got.SessionTtlHours, ttl)
		}
	}
}

func TestUpdate_SessionTtlOmittedPreserved(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	seed, _ := store.Get()
	cp := *seed
	cp.SessionTtlHours = 100
	if err := store.Update(func(cur *storage.Settings) error { *cur = cp; return nil }); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := postSettingsUpdate(t, h, `{"disableMemorySaving":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ := store.Get()
	if got.SessionTtlHours != 100 {
		t.Fatalf("sessionTtlHours = %d, want 100 (patch semantics preserve omitted fields)", got.SessionTtlHours)
	}
}

const rawV29ZeroTTL = `{"schemaVersion":29,"authEnabled":true,"sessionTtlHours":0,"usageLevel":"expert","server":{"port":2222,"interface":"br0"},"pingCheck":{"defaults":{"target":"8.8.8.8"}},"logging":{"singboxLogLevel":"info"},"updates":{},"connectivityCheckUrl":"http://connectivitycheck.gstatic.com/generate_204"}`

// R1: a save whose patch does NOT touch sessionTtlHours must never fail on a
// pre-existing stored 0 (rollback→re-upgrade). Load self-heals the stored 0 to
// the default, and the validation only fires when the patch sends the field,
// so an unrelated save succeeds and the value converges on 24.
func TestUpdate_StoredZeroTTL_UnrelatedSaveHealsTo24(t *testing.T) {
	h, store := newSettingsHandlerFromRaw(t, rawV29ZeroTTL)

	rec := postSettingsUpdate(t, h, `{"disableMemorySaving":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (unrelated save must not fail on stored 0), body=%s", rec.Code, rec.Body.String())
	}
	got, _ := store.Get()
	if got.SessionTtlHours != 24 {
		t.Fatalf("sessionTtlHours = %d, want 24 (self-healed)", got.SessionTtlHours)
	}
}

// R1: even with a stored 0 still cached (bypassing the Load heal), a patch that
// omits sessionTtlHours must not trip the range validation — the guard keys on
// patch.SessionTtlHours != nil, not on the merged value.
func TestUpdate_CachedZeroTTL_OmittedPatchNotRejected(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	seed, _ := store.Get()
	cp := *seed
	cp.SessionTtlHours = 0
	if err := store.Update(func(cur *storage.Settings) error { *cur = cp; return nil }); err != nil { // Save persists 0 without healing
		t.Fatalf("seed zero: %v", err)
	}
	rec := postSettingsUpdate(t, h, `{"authEnabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (omitted TTL must not validate a cached 0), body=%s", rec.Code, rec.Body.String())
	}
}

// R1: a stored 0 with a valid explicit TTL patch saves the new value.
func TestUpdate_StoredZeroTTL_ExplicitPatchSaves48(t *testing.T) {
	h, store := newSettingsHandlerFromRaw(t, rawV29ZeroTTL)

	rec := postSettingsUpdate(t, h, `{"sessionTtlHours":48}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	got, _ := store.Get()
	if got.SessionTtlHours != 48 {
		t.Fatalf("sessionTtlHours = %d, want 48", got.SessionTtlHours)
	}
}

// R1: an explicit out-of-range TTL patch is still rejected (0 and 5000).
func TestUpdate_ExplicitTTLOutOfRangeStillRejected(t *testing.T) {
	for _, body := range []string{`{"sessionTtlHours":0}`, `{"sessionTtlHours":5000}`} {
		t.Run(body, func(t *testing.T) {
			h, _ := newSettingsHandlerForTest(t)
			rec := postSettingsUpdate(t, h, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "INVALID_SESSION_TTL") {
				t.Fatalf("missing INVALID_SESSION_TTL, body=%s", rec.Body.String())
			}
		})
	}
}

func TestUpdate_EntwareAuthEnabledPersisted(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	rec := postSettingsUpdate(t, h, `{"entwareAuthEnabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ := store.Get()
	if !got.EntwareAuthEnabled {
		t.Fatal("entwareAuthEnabled not persisted")
	}

	rec = postSettingsUpdate(t, h, `{"entwareAuthEnabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ = store.Get()
	if got.EntwareAuthEnabled {
		t.Fatal("entwareAuthEnabled=false not persisted")
	}
}
