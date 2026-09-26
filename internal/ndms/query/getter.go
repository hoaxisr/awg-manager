package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Getter is the subset of transport.Client that Query Stores use. Kept
// minimal so tests can mock without HTTP.
//
// Post is used for the JSON-payload form of RCI commands (preferred over
// GET when the target identifier may contain slashes — see
// internal/ndms/transport/payload.go for the rationale and helpers).
type Getter interface {
	Get(ctx context.Context, path string, dst any) error
	GetRaw(ctx context.Context, path string) ([]byte, error)
	Post(ctx context.Context, payload any) (json.RawMessage, error)
}

// Logger is the logging surface Stores use for warnings (e.g. serving
// stale cache on upstream error) and debug diagnostics. Implemented by
// *logger.Logger; can be stubbed in tests.
type Logger interface {
	Warnf(format string, args ...any)
	Debugf(format string, args ...any)
}

// nopLogger is a no-op logger used when a store is constructed without
// one (tests, or consumers that don't care).
type nopLogger struct{}

func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Debugf(string, ...any) {}

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

	// POST-side scripting. POST payloads are not paths, so we key by the
	// interface name extracted from {"show":{"interface":{"name":...}}}
	// payloads — the only POST shape currently used in this package.
	// Non-interface POSTs fall through to postHandler.
	postIfaceResp  map[string][]byte
	postIfaceErr   map[string]error
	postIfaceCalls map[string]int

	// system-name resolver POSTs.
	postSystemName      map[string][]byte
	postSystemNameErr   map[string]error
	postSystemNameCalls map[string]int

	postHandler func(payload any) (json.RawMessage, error)
}

// NewFakeGetter returns a FakeGetter ready to be scripted via SetJSON /
// SetRaw / SetError / SetDefaultError.
func NewFakeGetter() *FakeGetter {
	return &FakeGetter{
		calls:          make(map[string]int),
		jsonResp:       make(map[string]string),
		rawResp:        make(map[string][]byte),
		errFor:         make(map[string]error),
		postIfaceResp:  make(map[string][]byte),
		postIfaceErr:   make(map[string]error),
		postIfaceCalls: make(map[string]int),
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
		body, haveBody = f.interfaceFromList(path)
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

// SetPostInterface scripts the Post response for a ShowInterface(name, …)
// payload. body must include the full {"show":{"interface":{…}}} wrapper,
// matching what NDMS actually returns over HTTP. The store unwraps it.
func (f *FakeGetter) SetPostInterface(name, body string) {
	f.mu.Lock()
	f.postIfaceResp[name] = []byte(body)
	f.mu.Unlock()
}

// SetPostInterfaceError scripts an error for a Post call against the given
// interface name. nil clears the entry.
func (f *FakeGetter) SetPostInterfaceError(name string, err error) {
	f.mu.Lock()
	if err == nil {
		delete(f.postIfaceErr, name)
	} else {
		f.postIfaceErr[name] = err
	}
	f.mu.Unlock()
}

// SetPostHandler installs a catch-all handler for POST payloads that
// aren't ShowInterface-shaped (e.g. listings or future commands). Pass
// nil to clear.
func (f *FakeGetter) SetPostHandler(fn func(payload any) (json.RawMessage, error)) {
	f.mu.Lock()
	f.postHandler = fn
	f.mu.Unlock()
}

// SetPostSystemName scripts the POST response for the system-name
// resolver — payload shape {"show":{"interface":{"system-name":{"name":X}}}}.
// body is the inner system-name value as NDMS returns it: a bare string
// ("nwg0"), a {"result":"..."} object, or any raw JSON that the resolver
// parser should be able to digest. The fake automatically wraps it back
// into {"show":{"interface":{"system-name":<body>}}} so the call shape
// matches what the live server emits.
func (f *FakeGetter) SetPostSystemName(name, body string) {
	f.mu.Lock()
	if f.postSystemName == nil {
		f.postSystemName = make(map[string][]byte)
	}
	f.postSystemName[name] = []byte(body)
	f.mu.Unlock()
}

// SetPostSystemNameError scripts an error for a system-name POST against
// the given NDMS id. nil clears the entry.
func (f *FakeGetter) SetPostSystemNameError(name string, err error) {
	f.mu.Lock()
	if err == nil {
		delete(f.postSystemNameErr, name)
	} else {
		if f.postSystemNameErr == nil {
			f.postSystemNameErr = make(map[string]error)
		}
		f.postSystemNameErr[name] = err
	}
	f.mu.Unlock()
}

// PostSystemNameCalls returns how many Post calls hit the system-name
// resolver with the given NDMS id.
func (f *FakeGetter) PostSystemNameCalls(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.postSystemNameCalls[name]
}

// extractShowSystemName walks a {"show":{"interface":{"system-name":{"name":X}}}}
// payload and returns X. Returns "" for any other shape.
func extractShowSystemName(payload any) string {
	top, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	show, ok := top["show"].(map[string]any)
	if !ok {
		return ""
	}
	iface, ok := show["interface"].(map[string]any)
	if !ok {
		return ""
	}
	sn, ok := iface["system-name"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := sn["name"].(string)
	return name
}

// PostInterfaceCalls returns how many Post calls targeted the given
// interface name via a ShowInterface payload.
func (f *FakeGetter) PostInterfaceCalls(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.postIfaceCalls[name]
}

// Post implements Getter.Post. ShowInterface-shaped payloads dispatch on
// the embedded "name"; system-name resolver payloads dispatch on the
// embedded id; everything else falls through to postHandler.
func (f *FakeGetter) Post(_ context.Context, payload any) (json.RawMessage, error) {
	if name := extractShowSystemName(payload); name != "" {
		f.mu.Lock()
		if f.postSystemNameCalls == nil {
			f.postSystemNameCalls = make(map[string]int)
		}
		f.postSystemNameCalls[name]++
		body, haveBody := f.postSystemName[name]
		err, haveErr := f.postSystemNameErr[name]
		defaultErr := f.defaultErr
		f.mu.Unlock()

		if haveErr {
			return nil, err
		}
		if !haveBody {
			if defaultErr != nil {
				return nil, defaultErr
			}
			return nil, errNoFakeResponse("POST show.interface.system-name name=" + name)
		}
		// Wrap inner body back into the show.interface.system-name envelope
		// the real NDMS emits.
		wrapped := []byte(`{"show":{"interface":{"system-name":` + string(body) + `}}}`)
		return wrapped, nil
	}

	if name := extractShowInterfaceName(payload); name != "" {
		f.mu.Lock()
		f.postIfaceCalls[name]++
		body, haveBody := f.postIfaceResp[name]
		err, haveErr := f.postIfaceErr[name]
		defaultErr := f.defaultErr
		f.mu.Unlock()

		if haveErr {
			return nil, err
		}
		if !haveBody {
			if defaultErr != nil {
				return nil, defaultErr
			}
			return nil, errNoFakeResponse("POST show.interface name=" + name)
		}
		out := make([]byte, len(body))
		copy(out, body)
		return out, nil
	}

	f.mu.Lock()
	handler := f.postHandler
	defaultErr := f.defaultErr
	f.mu.Unlock()
	if handler != nil {
		return handler(payload)
	}
	if defaultErr != nil {
		return nil, defaultErr
	}
	return nil, errNoFakeResponse("POST (unscripted payload)")
}

// extractShowInterfaceName walks a {"show":{"interface":{"name":X,…}}}
// payload — the only POST shape this fake recognises by name. Returns ""
// for any other shape so callers can fall back to the catch-all handler.
func extractShowInterfaceName(payload any) string {
	top, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	show, ok := top["show"].(map[string]any)
	if !ok {
		return ""
	}
	iface, ok := show["interface"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := iface["name"].(string)
	return name
}

// interfaceFromList отвечает на `/show/interface/<name>` записью из заданного
// полного списка, как настоящий NDMS (ответы побайтно совпадают, стенд
// 5.02.A.11). Явный SetJSON на точечный путь имеет приоритет.
func (f *FakeGetter) interfaceFromList(path string) (string, bool) {
	name, ok := strings.CutPrefix(path, "/show/interface/")
	if !ok || name == "" || strings.ContainsAny(name, "/?") {
		return "", false
	}
	f.mu.Lock()
	list, ok := f.jsonResp["/show/interface/"]
	f.mu.Unlock()
	if !ok {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(list), &m) != nil {
		return "", false
	}
	entry, ok := m[name]
	return string(entry), ok
}
