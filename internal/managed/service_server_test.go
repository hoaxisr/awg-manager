package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakeBridge holds a static bridge entry emitted by stateAwareGetter.
type fakeBridge struct {
	id      string // NDMS interface ID (= LANBridge.Name)
	address string
	mask    string
}

// stateAwareGetter answers /show/interface/ from the live SettingsStore so
// FindFreeIndex and listUsedSubnets see the latest set of managed servers
// across multiple Create calls. Other paths are unsupported (this fake
// covers exactly the surface Service.Create touches).
type stateAwareGetter struct {
	store   *storage.SettingsStore
	mu      sync.Mutex
	asc     map[string]map[string]string
	bridges []fakeBridge // static bridge entries injected by tests
}

func (g *stateAwareGetter) Get(ctx context.Context, path string, out any) error {
	if path != "/show/interface/" {
		if strings.HasPrefix(path, "/show/rc/interface/") && strings.HasSuffix(path, "/wireguard/asc") {
			iface := strings.TrimSuffix(strings.TrimPrefix(path, "/show/rc/interface/"), "/wireguard/asc")
			g.mu.Lock()
			src, ok := g.asc[iface]
			g.mu.Unlock()
			if !ok {
				src = map[string]string{
					"jc": "0", "jmin": "0", "jmax": "0", "s1": "0", "s2": "0",
					"h1": "", "h2": "", "h3": "", "h4": "",
					"s3": "0", "s4": "0",
				}
			}
			raw, err := json.Marshal(src)
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, out)
		}
		return fmt.Errorf("stateAwareGetter: path not faked: %s", path)
	}
	m := map[string]json.RawMessage{}
	for _, sv := range g.store.GetManagedServers() {
		entry := map[string]any{
			"id":             sv.InterfaceName,
			"interface-name": sv.InterfaceName,
			"type":           "Wireguard",
			"description":    ManagedServerDescription,
			"address":        sv.Address,
			"mask":           sv.Mask,
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		m[sv.InterfaceName] = raw
	}
	g.mu.Lock()
	brs := g.bridges
	g.mu.Unlock()
	for _, br := range brs {
		entry := map[string]any{
			"id":      br.id,
			"type":    "Bridge",
			"address": br.address,
			"mask":    br.mask,
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		m[br.id] = raw
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (g *stateAwareGetter) applyPost(payload map[string]interface{}) {
	intf, ok := payload["interface"].(map[string]interface{})
	if !ok {
		return
	}
	for ifaceName, raw := range intf {
		cfg, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		wg, ok := cfg["wireguard"].(map[string]interface{})
		if !ok {
			continue
		}
		ascRaw, has := wg["asc"]
		if !has {
			continue
		}
		g.mu.Lock()
		if g.asc == nil {
			g.asc = map[string]map[string]string{}
		}
		if clear, ok := ascRaw.(map[string]interface{}); ok {
			if no, ok := clear["no"].(bool); ok && no {
				g.asc[ifaceName] = map[string]string{
					"jc": "0", "jmin": "0", "jmax": "0", "s1": "0", "s2": "0",
					"h1": "", "h2": "", "h3": "", "h4": "",
					"s3": "0", "s4": "0",
				}
				g.mu.Unlock()
				continue
			}
		}
		ascMap, ok := ascRaw.(map[string]interface{})
		if !ok {
			b, _ := json.Marshal(ascRaw)
			_ = json.Unmarshal(b, &ascMap)
		}
		state := map[string]string{}
		for k, v := range ascMap {
			switch val := v.(type) {
			case string:
				state[k] = val
			default:
				state[k] = strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%v", val), ".0"), ".")
			}
		}
		// Preserve optional fields as zeros if omitted by set payload.
		for _, k := range []string{"s3", "s4"} {
			if _, ok := state[k]; !ok {
				state[k] = "0"
			}
		}
		g.asc[ifaceName] = state
		g.mu.Unlock()
	}
}

// GetRaw handles /show/interface/system-name?name=<ndmsName> by mapping
// NDMS Wireguard<N> names to their kernel equivalents (nwg<N>). Other
// paths return an error so unexpected callers surface immediately.
func (g *stateAwareGetter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	const prefix = "/show/interface/system-name?name="
	if strings.HasPrefix(path, prefix) {
		ndmsName := strings.TrimPrefix(path, prefix)
		// Map "WireguardN" → "nwgN".
		if strings.HasPrefix(ndmsName, "Wireguard") {
			suffix := strings.TrimPrefix(ndmsName, "Wireguard")
			kernelName := "nwg" + suffix
			// Return as a bare JSON string, matching NDMS response format.
			b, _ := json.Marshal(kernelName)
			return b, nil
		}
		return []byte(`""`), nil
	}
	return nil, errors.New("stateAwareGetter: GetRaw not faked: " + path)
}

// Post handles the system-name resolver payload (used by
// InterfaceStore.fetchSystemName for slash-safe lookups via POST).
// Payload shape: {"show":{"interface":{"system-name":{"name":<NDMS-id>}}}}.
// Maps "WireguardN" → "nwgN" and wraps the response back into the same
// show.interface.system-name envelope NDMS emits. Other POST shapes
// are intentionally unsupported — managed-server writes go through the
// Poster, not Getter.Post.
func (g *stateAwareGetter) Post(_ context.Context, payload any) (json.RawMessage, error) {
	top, _ := payload.(map[string]any)
	show, _ := top["show"].(map[string]any)
	iface, _ := show["interface"].(map[string]any)
	sn, _ := iface["system-name"].(map[string]any)
	name, _ := sn["name"].(string)
	if name == "" {
		return nil, errors.New("stateAwareGetter: Post payload not recognised")
	}
	kernel := ""
	if strings.HasPrefix(name, "Wireguard") {
		kernel = "nwg" + strings.TrimPrefix(name, "Wireguard")
	}
	// Bare-string response wrapped in show.interface.system-name envelope.
	inner, _ := json.Marshal(kernel)
	return []byte(`{"show":{"interface":{"system-name":` + string(inner) + `}}}`), nil
}

// recordingPoster is a thread-safe variant of fakePoster — Create uses three
// POSTs per server and a parallel test would race the slice. Fresh instance
// per test keeps this simple.
type recordingPoster struct {
	mu     sync.Mutex
	posts  []map[string]interface{}
	err    error
	onPost func(map[string]interface{})
	failOn func(map[string]interface{}) error // per-command инъекция ошибки
}

func (p *recordingPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	p.mu.Lock()
	var injected error
	if m, ok := payload.(map[string]interface{}); ok {
		p.posts = append(p.posts, m) // запись ДО проверки failOn: тест видит, что было попытано
		if p.onPost != nil {
			p.onPost(m)
		}
		if p.failOn != nil {
			injected = p.failOn(m)
		}
	}
	err := p.err
	p.mu.Unlock()
	if injected != nil {
		return nil, injected
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage("{}"), nil
}

// newCreateTestService wires a Service with the InterfaceStore and
// WGServerStore that Service.Create exercises. TTL is 0 so the
// ListStore caches always miss — necessary because Create #1 and
// Create #2 both call /show/interface/ and we want them to see
// different snapshots.
func newCreateTestService(t *testing.T) (*Service, *storage.SettingsStore) {
	t.Helper()
	tmpDir := t.TempDir()
	store := storage.NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	getter := &stateAwareGetter{store: store, asc: map[string]map[string]string{}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		Policies:   query.NewPolicyStore(getter, query.NopLogger()),
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &recordingPoster{onPost: getter.applyPost}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(poster, nil, queries, nil, store, log, nil)
	// Create now requires immediate private-key capture; tests should not
	// depend on host wg-tools availability.
	svc.wgRun = func(_ context.Context, _ string, _ ...string) (string, error) {
		return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", nil
	}
	return svc, store
}

func TestService_CreateMultipleServers(t *testing.T) {
	svc, store := newCreateTestService(t)
	ctx := context.Background()

	first, err := svc.Create(ctx, CreateServerRequest{
		Address:    "10.66.66.1",
		Mask:       "255.255.255.0",
		ListenPort: 51820,
	})
	if err != nil {
		t.Fatalf("create #1: %v", err)
	}

	second, err := svc.Create(ctx, CreateServerRequest{
		Address:    "10.77.77.1",
		Mask:       "255.255.255.0",
		ListenPort: 51821,
	})
	if err != nil {
		t.Fatalf("create #2: %v", err)
	}

	if first.InterfaceName == second.InterfaceName {
		t.Errorf("expected distinct interface names, both = %s", first.InterfaceName)
	}

	all := svc.List()
	if len(all) != 2 {
		t.Errorf("expected 2 servers in list, got %d", len(all))
	}

	// Sanity-check the storage agrees with svc.List().
	if got := len(store.GetManagedServers()); got != 2 {
		t.Errorf("expected 2 servers in storage, got %d", got)
	}

	// Both servers must be retrievable by id.
	if _, err := svc.Get(first.InterfaceName); err != nil {
		t.Errorf("Get(%s): %v", first.InterfaceName, err)
	}
	if _, err := svc.Get(second.InterfaceName); err != nil {
		t.Errorf("Get(%s): %v", second.InterfaceName, err)
	}
}

// TestService_CreateRejectsConflicts is a table-driven battery for
// validateServerParams. The first server is always created at
// 10.66.66.1/24 listen-port 51820; each row then attempts a second
// Create and asserts whether it should succeed or be rejected with a
// specific error substring.
func TestService_CreateRejectsConflicts(t *testing.T) {
	cases := []struct {
		name       string
		req        CreateServerRequest
		wantErr    bool
		wantErrSub string // substring expected in error message; empty = any error matches
	}{
		{
			name:    "different subnet, different port — accepted",
			req:     CreateServerRequest{Address: "10.77.77.1", Mask: "255.255.255.0", ListenPort: 51821},
			wantErr: false,
		},
		{
			name:       "exact subnet match — rejected",
			req:        CreateServerRequest{Address: "10.66.66.5", Mask: "255.255.255.0", ListenPort: 51821},
			wantErr:    true,
			wantErrSub: "пересекается",
		},
		{
			name:       "smaller subnet inside larger — rejected",
			req:        CreateServerRequest{Address: "10.66.66.129", Mask: "255.255.255.128", ListenPort: 51821},
			wantErr:    true,
			wantErrSub: "пересекается",
		},
		{
			name:       "larger subnet over smaller — rejected",
			req:        CreateServerRequest{Address: "10.66.0.1", Mask: "255.255.0.0", ListenPort: 51821},
			wantErr:    true,
			wantErrSub: "пересекается",
		},
		{
			name:    "sibling subnet — accepted",
			req:     CreateServerRequest{Address: "10.66.67.1", Mask: "255.255.255.0", ListenPort: 51821},
			wantErr: false,
		},
		{
			name:       "port collision — rejected",
			req:        CreateServerRequest{Address: "10.77.77.1", Mask: "255.255.255.0", ListenPort: 51820},
			wantErr:    true,
			wantErrSub: "listen-port",
		},
		{
			name:       "subnet ok + port collision — rejected on port (port checked first)",
			req:        CreateServerRequest{Address: "10.88.88.1", Mask: "255.255.255.0", ListenPort: 51820},
			wantErr:    true,
			wantErrSub: "listen-port",
		},
		{
			name:       "subnet conflict + port ok — rejected on subnet",
			req:        CreateServerRequest{Address: "10.66.66.50", Mask: "255.255.255.0", ListenPort: 51999},
			wantErr:    true,
			wantErrSub: "пересекается",
		},
		{
			name:       "invalid port — rejected by range check",
			req:        CreateServerRequest{Address: "10.99.99.1", Mask: "255.255.255.0", ListenPort: 70000},
			wantErr:    true,
			wantErrSub: "invalid port",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newCreateTestService(t)
			ctx := context.Background()
			if _, err := svc.Create(ctx, CreateServerRequest{Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820}); err != nil {
				t.Fatalf("seed first server: %v", err)
			}
			_, err := svc.Create(ctx, tc.req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected success, got: %v", err)
			}
		})
	}
}

// TestService_Create_CapturesPrivateKey exercises the happy path introduced in
// Task 4: after rciSetNAT, Create resolves the kernel interface name and reads
// the private key via wg-tools. The test injects a stub runner that returns a
// known key and verifies both the returned server value and the persisted
// storage entry carry that key.
//
// The stateAwareGetter.GetRaw implementation handles
// /show/interface/system-name?name=WireguardN → "nwgN" so that
// InterfaceStore.ResolveSystemName falls through to the fetchSystemName
// fallback (the cached SystemName from the list response is "Wireguard0"
// which equals the NDMS id, so it is treated as garbage and the resolver
// probe is triggered).
func TestService_Create_CapturesPrivateKey(t *testing.T) {
	const wantKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	svc, store := newCreateTestService(t)

	// Stub wg-tools: return a known key regardless of the interface name.
	svc.wgRun = func(_ context.Context, _ string, _ ...string) (string, error) {
		return wantKey + "\n", nil
	}

	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.20.30.1",
		Mask:       "255.255.255.0",
		ListenPort: 51920,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if server.PrivateKey != wantKey {
		t.Errorf("returned PrivateKey: got %q, want %q", server.PrivateKey, wantKey)
	}

	// Also verify the key reached persistent storage.
	saved, ok := store.GetManagedServerByID(server.InterfaceName)
	if !ok {
		t.Fatalf("server %q not found in storage after Create", server.InterfaceName)
	}
	if saved.PrivateKey != wantKey {
		t.Errorf("storage PrivateKey: got %q, want %q", saved.PrivateKey, wantKey)
	}

	// Create must apply generated ASC params during the creation transaction.
	poster, ok := svc.transport.(*recordingPoster)
	if !ok {
		t.Fatalf("unexpected transport type %T", svc.transport)
	}
	foundASC := false
	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		row, ok := iface[server.InterfaceName].(map[string]interface{})
		if !ok {
			continue
		}
		wg, ok := row["wireguard"].(map[string]interface{})
		if !ok {
			continue
		}
		if asc, ok := wg["asc"].(map[string]interface{}); ok {
			if _, ok := asc["jc"]; ok {
				foundASC = true
				break
			}
		}
	}
	if !foundASC {
		t.Fatalf("expected ASC payload in create transaction")
	}
}

func TestService_Create_FailsWhenPrivateKeyUnavailable(t *testing.T) {
	svc, store := newCreateTestService(t)

	svc.wgRun = func(_ context.Context, _ string, _ ...string) (string, error) {
		return "", errors.New("wg unavailable")
	}

	_, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.20.40.1",
		Mask:       "255.255.255.0",
		ListenPort: 51921,
	})
	if err == nil {
		t.Fatalf("expected Create to fail when private key cannot be read")
	}
	if !strings.Contains(err.Error(), "read private key") {
		t.Fatalf("expected read private key error, got: %v", err)
	}

	if got := len(store.GetManagedServers()); got != 0 {
		t.Fatalf("server must not be persisted on private-key failure, got %d entries", got)
	}
}

// newNATModeTestService wires a Service like newCreateTestService but with an
// injected Routes store that reports PPPoE0 as the default-gateway interface.
// This is required for TestSetNATMode_InternetOnly_SetsStaticToWAN.
func newNATModeTestService(t *testing.T) (*Service, *storage.SettingsStore, *recordingPoster) {
	t.Helper()
	svc, store := newCreateTestService(t)

	// Build a fake Getter that answers /show/ip/route with a default via PPPoE0.
	routeGetter := query.NewFakeGetter()
	routeGetter.SetJSON("/show/ip/route", `[{"destination":"0.0.0.0/0","gateway":"1.2.3.4","interface":"PPPoE0"}]`)
	svc.queries.Routes = query.NewRouteStore(routeGetter, query.NopLogger())

	poster, ok := svc.transport.(*recordingPoster)
	if !ok {
		t.Fatalf("unexpected transport type %T", svc.transport)
	}
	return svc, store, poster
}

func TestSetNATMode_InternetOnly_SetsStaticToWAN(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()

	// Seed a server in storage so SetNATMode can look it up.
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName,
		Address:       "10.66.66.1",
		Mask:          "255.255.255.0",
		ListenPort:    51820,
		NATEnabled:    true,
		NATMode:       "full",
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	poster.mu.Lock()
	poster.posts = nil // reset posts from any prior calls
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "internet-only"); err != nil {
		t.Fatalf("SetNATMode: %v", err)
	}

	// Verify storage was updated.
	saved, ok := store.GetManagedServerByID(ifaceName)
	if !ok {
		t.Fatalf("server not found in storage after SetNATMode")
	}
	if saved.NATMode != "internet-only" {
		t.Errorf("storage NATMode: got %q, want %q", saved.NATMode, "internet-only")
	}
	if saved.NATEnabled {
		t.Errorf("storage NATEnabled: got true, want false for internet-only")
	}

	// Inspect the RCI POSTs.
	poster.mu.Lock()
	posts := make([]map[string]interface{}, len(poster.posts))
	copy(posts, poster.posts)
	poster.mu.Unlock()

	foundNoIPNat := false
	foundStaticNAT := false
	for _, p := range posts {
		ip, ok := p["ip"].(map[string]interface{})
		if !ok {
			continue
		}
		// no-ip-nat: {"ip":{"nat":[{"no":true,"interface":"Wireguard0"}]}}
		if natSlice, ok := ip["nat"].([]map[string]interface{}); ok {
			for _, entry := range natSlice {
				if entry["no"] == true && entry["interface"] == ifaceName {
					foundNoIPNat = true
				}
			}
		}
		// ip-static: {"ip":{"static":{"interface":"Wireguard0","to-interface":"PPPoE0"}}}
		if static, ok := ip["static"].(map[string]interface{}); ok {
			if static["interface"] == ifaceName && static["to-interface"] == "PPPoE0" {
				foundStaticNAT = true
			}
		}
	}
	if !foundNoIPNat {
		t.Errorf("expected no-ip-nat POST for %s; posts = %v", ifaceName, posts)
	}
	if !foundStaticNAT {
		t.Errorf("expected ip-static POST for %s to PPPoE0; posts = %v", ifaceName, posts)
	}
}

func TestService_Create_SkipsASCWhenDisabled(t *testing.T) {
	svc, store := newCreateTestService(t)

	generate := false
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:     "10.20.50.1",
		Mask:        "255.255.255.0",
		ListenPort:  51922,
		GenerateASC: &generate,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got := len(store.GetManagedServers()); got != 1 {
		t.Fatalf("server must be persisted, got %d", got)
	}

	poster, ok := svc.transport.(*recordingPoster)
	if !ok {
		t.Fatalf("unexpected transport type %T", svc.transport)
	}
	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		row, ok := iface[server.InterfaceName].(map[string]interface{})
		if !ok {
			continue
		}
		wg, ok := row["wireguard"].(map[string]interface{})
		if !ok {
			continue
		}
		if _, ok := wg["asc"]; ok {
			t.Fatalf("ASC payload must not be sent when GenerateASC=false")
		}
	}
}

// newLANSegmentsTestService builds a Service wired with a fake bridge "Home"
// (10.10.10.1/24) available in the InterfaceStore. Returns svc, store, and
// the recording poster for RCI inspection.
func newLANSegmentsTestService(t *testing.T) (*Service, *storage.SettingsStore, *recordingPoster) {
	t.Helper()
	tmpDir := t.TempDir()
	store := storage.NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	getter := &stateAwareGetter{
		store: store,
		asc:   map[string]map[string]string{},
		bridges: []fakeBridge{
			{id: "Home", address: "10.10.10.1", mask: "255.255.255.0"},
		},
	}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		Policies:   query.NewPolicyStore(getter, query.NopLogger()),
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &recordingPoster{onPost: getter.applyPost}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(poster, nil, queries, nil, store, log, nil)
	svc.wgRun = func(_ context.Context, _ string, _ ...string) (string, error) {
		return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", nil
	}
	return svc, store, poster
}

// TestSetLANSegments_RebuildOrder verifies that SetLANSegments posts the four
// parse commands in the required order and persists LANSegments in storage.
// Empty-list variant verifies only unbind+remove are sent (no permit/bind).
func TestSetLANSegments_RebuildOrder(t *testing.T) {
	const ifaceName = "Wireguard0"

	t.Run("non-empty segments", func(t *testing.T) {
		svc, store, poster := newLANSegmentsTestService(t)
		ctx := context.Background()

		if err := store.AddManagedServer(storage.ManagedServer{
			InterfaceName: ifaceName,
			Address:       "10.66.66.1",
			Mask:          "255.255.255.0",
			ListenPort:    51820,
		}); err != nil {
			t.Fatalf("seed server: %v", err)
		}

		poster.mu.Lock()
		poster.posts = nil
		poster.mu.Unlock()

		if err := svc.SetLANSegments(ctx, ifaceName, []string{"Home"}); err != nil {
			t.Fatalf("SetLANSegments: %v", err)
		}

		// Collect parse strings in order.
		poster.mu.Lock()
		posts := make([]map[string]interface{}, len(poster.posts))
		copy(posts, poster.posts)
		poster.mu.Unlock()

		var parseStrings []string
		for _, p := range posts {
			if s, ok := p["parse"].(string); ok {
				parseStrings = append(parseStrings, s)
			}
		}

		acl := "AWGM_" + ifaceName
		// Expected order:
		// 1. no interface <iface> ip access-group <acl> in
		// 2. no access-list <acl>
		// 3. access-list <acl> permit ip <peerSub> <peerMask> <segSub> <segMask>
		// 4. interface <iface> ip access-group <acl> in
		wantParses := []string{
			fmt.Sprintf("no interface %s ip access-group %s in", ifaceName, acl),
			"no access-list " + acl,
			fmt.Sprintf("access-list %s permit ip 10.66.66.0 255.255.255.0 10.10.10.0 255.255.255.0", acl),
			fmt.Sprintf("interface %s ip access-group %s in", ifaceName, acl),
		}

		if len(parseStrings) != len(wantParses) {
			t.Fatalf("expected %d parse commands, got %d: %v", len(wantParses), len(parseStrings), parseStrings)
		}
		for i, want := range wantParses {
			if parseStrings[i] != want {
				t.Errorf("parse[%d]: got %q, want %q", i, parseStrings[i], want)
			}
		}

		// Storage must be updated.
		saved, ok := store.GetManagedServerByID(ifaceName)
		if !ok {
			t.Fatalf("server not found in storage")
		}
		if len(saved.LANSegments) != 1 || saved.LANSegments[0] != "Home" {
			t.Errorf("storage LANSegments: got %v, want [Home]", saved.LANSegments)
		}
	})

	t.Run("empty segments unbinds and removes only", func(t *testing.T) {
		svc, store, poster := newLANSegmentsTestService(t)
		ctx := context.Background()

		if err := store.AddManagedServer(storage.ManagedServer{
			InterfaceName: ifaceName,
			Address:       "10.66.66.1",
			Mask:          "255.255.255.0",
			ListenPort:    51820,
		}); err != nil {
			t.Fatalf("seed server: %v", err)
		}

		poster.mu.Lock()
		poster.posts = nil
		poster.mu.Unlock()

		if err := svc.SetLANSegments(ctx, ifaceName, []string{}); err != nil {
			t.Fatalf("SetLANSegments(empty): %v", err)
		}

		poster.mu.Lock()
		posts := make([]map[string]interface{}, len(poster.posts))
		copy(posts, poster.posts)
		poster.mu.Unlock()

		var parseStrings []string
		for _, p := range posts {
			if s, ok := p["parse"].(string); ok {
				parseStrings = append(parseStrings, s)
			}
		}

		acl := "AWGM_" + ifaceName
		wantParses := []string{
			fmt.Sprintf("no interface %s ip access-group %s in", ifaceName, acl),
			"no access-list " + acl,
		}

		if len(parseStrings) != len(wantParses) {
			t.Fatalf("expected %d parse commands, got %d: %v", len(wantParses), len(parseStrings), parseStrings)
		}
		for i, want := range wantParses {
			if parseStrings[i] != want {
				t.Errorf("parse[%d]: got %q, want %q", i, parseStrings[i], want)
			}
		}

		saved, ok := store.GetManagedServerByID(ifaceName)
		if !ok {
			t.Fatalf("server not found in storage")
		}
		if len(saved.LANSegments) != 0 {
			t.Errorf("storage LANSegments: got %v, want empty", saved.LANSegments)
		}
	})
}
