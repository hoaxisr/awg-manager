package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Getter is the subset of transport.Client that Query Stores use. Kept
// minimal so tests can mock without HTTP.
type Getter interface {
	Get(ctx context.Context, path string, dst any) error
	GetRaw(ctx context.Context, path string) ([]byte, error)
}

// Logger is the logging surface Stores use for warnings (e.g. serving
// stale cache on upstream error). Implemented by *logger.Logger; can be
// stubbed in tests.
type Logger interface {
	Warnf(format string, args ...any)
}

// nopLogger is a no-op logger used when a store is constructed without
// one (tests, or consumers that don't care).
type nopLogger struct{}

func (nopLogger) Warnf(string, ...any) {}

// NopLogger returns a Logger that drops everything. Use in tests.
func NopLogger() Logger { return nopLogger{} }

// ErrNotSupportedOnOS4 is returned by Stores that query endpoints absent
// on OS 4.x firmware (e.g. dns-proxy). Callers decide whether to surface
// or fall back to an alternative backend (e.g. HR Neo for DNS routing).
var ErrNotSupportedOnOS4 = errors.New("ndms query: feature not available on OS 4.x")

// decodeRCMap unmarshals a JSON response that is either a map of named
// entries (populated case) or an empty array [] (NDMS's quirky
// "no entries" representation on /show/rc/... endpoints). dst must be
// a pointer to a map. On empty array the map is left untouched (nil).
// Any other shape returns an error.
func decodeRCMap(raw []byte, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return fmt.Errorf("decode as array: %w", err)
		}
		if len(arr) != 0 {
			return fmt.Errorf("expected empty array or map, got populated array with %d items", len(arr))
		}
		return nil
	}
	return json.Unmarshal(trimmed, dst)
}

// ErrNotInitialized is returned by SystemInfoStore.Get before Init
// finishes. Consumers that read system info should normally call Init
// at boot; this error exists to surface wiring mistakes.
var ErrNotInitialized = errors.New("ndms query: store not initialized")

// ErrNoDefaultRoute is returned by RouteStore.GetDefaultGatewayInterface
// when the route table has no active IPv4 default route.
var ErrNoDefaultRoute = errors.New("ndms query: no default IPv4 route")

// --- Test helpers (used by every *_test.go in this package AND by
// external packages that need to script NDMS responses). ---

// FakeGetter records every call and lets tests script responses per path.
// Thread-safe.
type FakeGetter struct {
	mu         sync.Mutex
	calls      map[string]int    // path → call count
	jsonResp   map[string]string // path → JSON string to return
	rawResp    map[string][]byte // path → raw bytes to return
	errFor     map[string]error  // path → error to return
	defaultErr error             // returned when no specific entry set
}

// NewFakeGetter returns a FakeGetter ready to be scripted via SetJSON /
// SetRaw / SetError / SetDefaultError.
func NewFakeGetter() *FakeGetter {
	return &FakeGetter{
		calls:    make(map[string]int),
		jsonResp: make(map[string]string),
		rawResp:  make(map[string][]byte),
		errFor:   make(map[string]error),
	}
}

func (f *FakeGetter) SetJSON(path, body string) {
	f.mu.Lock()
	f.jsonResp[path] = body
	f.mu.Unlock()
}

func (f *FakeGetter) SetRaw(path string, body []byte) {
	f.mu.Lock()
	f.rawResp[path] = body
	f.mu.Unlock()
}

func (f *FakeGetter) SetError(path string, err error) {
	f.mu.Lock()
	if err == nil {
		delete(f.errFor, path)
	} else {
		f.errFor[path] = err
	}
	f.mu.Unlock()
}

func (f *FakeGetter) SetDefaultError(err error) {
	f.mu.Lock()
	f.defaultErr = err
	f.mu.Unlock()
}

func (f *FakeGetter) Calls(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *FakeGetter) Get(ctx context.Context, path string, dst any) error {
	f.mu.Lock()
	f.calls[path]++
	body, haveBody := f.jsonResp[path]
	err, haveErr := f.errFor[path]
	defaultErr := f.defaultErr
	f.mu.Unlock()

	if haveErr {
		return err
	}
	if !haveBody {
		if defaultErr != nil {
			return defaultErr
		}
		return errNoFakeResponse(path)
	}
	return json.Unmarshal([]byte(body), dst)
}

func (f *FakeGetter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	f.calls[path]++
	body, haveBody := f.rawResp[path]
	// Fall back to jsonResp so fixtures primed with SetJSON also satisfy
	// GetRaw callers (e.g. InterfaceStore.ResolveSystemName).
	if !haveBody {
		if jsonBody, haveJSON := f.jsonResp[path]; haveJSON {
			body = []byte(jsonBody)
			haveBody = true
		}
	}
	err, haveErr := f.errFor[path]
	defaultErr := f.defaultErr
	f.mu.Unlock()

	if haveErr {
		return nil, err
	}
	if !haveBody {
		if defaultErr != nil {
			return nil, defaultErr
		}
		return nil, errNoFakeResponse(path)
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	return cp, nil
}

// newFakeGetter is the internal alias kept so the in-package tests don't
// have to migrate to NewFakeGetter en masse — they're equivalent.
func newFakeGetter() *FakeGetter { return NewFakeGetter() }

type errNoFakeResp string

func (e errNoFakeResp) Error() string { return "FakeGetter: no response for path " + string(e) }

func errNoFakeResponse(path string) error { return errNoFakeResp(path) }
