package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
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
	// created — интерфейсы, созданные POST-ом и ещё не снятые: настоящий NDMS
	// показывает их в списке сразу, до записи сервера в настройки (F546).
	created map[string]bool
	// wans — прочие интерфейсы списка (выходы static NAT): ссылки на них
	// подтверждаются списком (F546).
	wans []string
	// addr — адрес/маска, поставленные POST-ом configure: точечное чтение
	// после него видит их, как на роутере (П4).
	addr map[string][2]string
	// rcReads — чтения дерева /show/rc/interface/ (бюджет F581).
	rcReads atomic.Int64
	// rcHook — зовётся на каждом чтении дерева rc до ответа (вне mu);
	// ошибка — ответ чтения (сверка ASC, R44).
	rcHook func() error
}

func (g *stateAwareGetter) Get(ctx context.Context, path string, out any) error {
	if path != "/show/interface/" {
		// Дерево rc (одно чтение на все интерфейсы, F546): серверы из хранилища
		// и созданные POST-ом. Пиры — записанные в хранилище (роутер совпадает
		// с записью): правка по ключу проверяет наличие пира свежим чтением;
		// ASC — последний записанный POST-ом.
		if path == "/show/rc/interface/" {
			g.rcReads.Add(1)
			if g.rcHook != nil {
				if err := g.rcHook(); err != nil {
					return err
				}
			}
			names := map[string]bool{}
			for _, sv := range g.store.GetManagedServers() {
				names[sv.InterfaceName] = true
			}
			g.mu.Lock()
			for name := range g.created {
				names[name] = true
			}
			tree := map[string]any{}
			for name := range names {
				var peers []map[string]any
				if sv, ok := g.store.GetManagedServerByID(name); ok {
					for _, p := range sv.Peers {
						peers = append(peers, map[string]any{"key": p.PublicKey})
					}
				}
				src, ok := g.asc[name]
				if !ok {
					src = map[string]string{
						"jc": "0", "jmin": "0", "jmax": "0", "s1": "0", "s2": "0",
						"h1": "", "h2": "", "h3": "", "h4": "",
						"s3": "0", "s4": "0",
					}
				}
				tree[name] = map[string]any{"wireguard": map[string]any{"peer": peers, "asc": src}}
			}
			g.mu.Unlock()
			raw, err := json.Marshal(tree)
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, out)
		}
		return fmt.Errorf("stateAwareGetter: path not faked: %s", path)
	}
	m := map[string]json.RawMessage{}
	for _, sv := range g.store.GetManagedServers() {
		// Адрес — поставленный POST-ом, если был (правка, ответ на которую
		// потерялся), иначе из записи.
		addr, mask := sv.Address, sv.Mask
		g.mu.Lock()
		if a, ok := g.addr[sv.InterfaceName]; ok {
			addr, mask = a[0], a[1]
		}
		g.mu.Unlock()
		entry := map[string]any{
			"id":             sv.InterfaceName,
			"interface-name": sv.InterfaceName,
			"type":           "Wireguard",
			"description":    ManagedServerDescription,
			"address":        addr,
			"mask":           mask,
			// Runtime серверов читается из снимка списка (F546): публичный
			// ключ — в записи списка, как на роутере.
			"wireguard": map[string]any{"public-key": "SRV-" + sv.InterfaceName},
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		m[sv.InterfaceName] = raw
	}
	g.mu.Lock()
	brs := g.bridges
	for _, name := range g.wans {
		m[name] = json.RawMessage(`{"id":"` + name + `","interface-name":"` + name + `"}`)
	}
	for name := range g.created {
		if _, ok := m[name]; !ok {
			a := g.addr[name]
			m[name] = json.RawMessage(`{"id":"` + name + `","interface-name":"` + name + `","type":"Wireguard","address":"` + a[0] +
				`","mask":"` + a[1] + `","wireguard":{"public-key":"SRV-` + name + `"}}`)
		}
	}
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
		g.mu.Lock()
		if no, _ := cfg["no"].(bool); no {
			delete(g.created, ifaceName)
		} else if len(cfg) == 0 {
			if g.created == nil {
				g.created = map[string]bool{}
			}
			g.created[ifaceName] = true
		}
		if ip, ok := cfg["ip"].(map[string]interface{}); ok {
			set := func(a map[string]interface{}) {
				if g.addr == nil {
					g.addr = map[string][2]string{}
				}
				g.addr[ifaceName] = [2]string{fmt.Sprint(a["address"]), fmt.Sprint(a["mask"])}
			}
			switch a := ip["address"].(type) {
			case map[string]interface{}:
				set(a)
			case []map[string]interface{}: // правка: снятие старого + новый
				for _, e := range a {
					if no, _ := e["no"].(bool); !no {
						set(e)
					}
				}
			}
		}
		g.mu.Unlock()
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
	// Удаление пира сверяет маршруты с его меткой (#713) — статических нет.
	if path == "/show/rc/ip/route" {
		return []byte(`[]`), nil
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
	sn, ok := iface["system-name"].(map[string]any)
	if !ok {
		return nil, errors.New("stateAwareGetter: Post payload not recognised")
	}
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
	// respond — per-command тело ответа (nil — "{}"): отказы NDMS внутри
	// вложенного status, которые транспорт ошибкой не считает.
	respond func(map[string]interface{}) json.RawMessage
	// honorCtx — отменённый ctx отвергается без записи, как у настоящего транспорта.
	honorCtx bool
}

func (p *recordingPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	p.mu.Lock()
	if p.honorCtx && ctx.Err() != nil {
		p.mu.Unlock()
		return nil, ctx.Err()
	}
	var injected error
	var resp json.RawMessage
	if m, ok := payload.(map[string]interface{}); ok {
		if p.respond != nil {
			resp = p.respond(m)
		}
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
	if resp != nil {
		return resp, nil
	}
	return createdReply(payload), nil
}

// newCreateTestService wires a Service with the InterfaceStore and
// WGServerStore that Service.Create exercises. TTL is 0 so the
// ListStore caches always miss — necessary because Create #1 and
// Create #2 both call /show/interface/ and we want them to see
// different snapshots.
func newCreateTestService(t *testing.T) (*Service, *storage.SettingsStore, *stateAwareGetter) {
	t.Helper()
	tmpDir := t.TempDir()
	store := storage.NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	getter := &stateAwareGetter{store: store, asc: map[string]map[string]string{}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces:    ifaces,
		Policies:      query.NewPolicyStore(getter, query.NopLogger()),
		WGServers:     query.NewWGServerStore(getter, query.NopLogger(), ifaces),
		RunningConfig: query.NewRunningConfigStore(getter, query.NopLogger()),
		StaticRoutes:  query.NewStaticRouteStore(getter, query.NopLogger()),
	}
	poster := &recordingPoster{onPost: getter.applyPost}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// ACL-операции идут через command.Commands — строим его над тем же
	// recordingPoster, чтобы тесты видели parse-строки в том же журнале.
	// SaveCoordinator настоящий (Request не nil-safe — это осознанно: nil в
	// продакшене должен падать громко), debounce час — save в тестах не летит.
	sc := command.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil)
	cmds := command.NewCommands(command.Deps{Poster: poster, Save: sc, Queries: queries})
	svc := New(poster, nil, queries, cmds, store, log, nil)
	// Create now requires immediate private-key capture; tests should not
	// depend on host wg-tools availability.
	svc.wgRun = func(_ context.Context, _ string, _ ...string) (string, error) {
		return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", nil
	}
	// AddPeer иначе форкает /opt/bin/wg — на машине разработчика его нет.
	svc.keyGen = &fakeKeyGen{}
	return svc, store, getter
}

// fakeKeyGen выдаёт детерминированные ключи вместо wg genkey/pubkey/genpsk.
type fakeKeyGen struct{ n int }

func (f *fakeKeyGen) next() int {
	f.n++
	return f.n
}

func (f *fakeKeyGen) GenerateKeyPair(_ context.Context) (string, string, error) {
	n := f.next()
	return fmt.Sprintf("priv-%d", n), fmt.Sprintf("pub-%d", n), nil
}

func (f *fakeKeyGen) GeneratePresharedKey(_ context.Context) (string, error) {
	return fmt.Sprintf("psk-%d", f.next()), nil
}

func TestService_CreateMultipleServers(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
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
			svc, _, _ := newCreateTestService(t)
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

	svc, store, _ := newCreateTestService(t)

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
	svc, store, _ := newCreateTestService(t)

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
	svc, store, getter := newCreateTestService(t)
	getter.wans = []string{"PPPoE0", "Wireguard2", "OpkgTun0", "ISP", "WireguardX"}

	// Build a fake Getter that answers /show/ip/route with a default via PPPoE0.
	routeGetter := query.NewFakeGetter()
	routeGetter.SetJSON("/show/ip/route", `[{"destination":"0.0.0.0/0","gateway":"1.2.3.4","interface":"PPPoE0"}]`)
	svc.queries.Routes = query.NewRouteStore(routeGetter, query.NopLogger())

	// running-config с тремя `ip global`-выходами (порядок появления значим).
	rcGetter := query.NewFakeGetter()
	rcGetter.SetJSON("/show/running-config", `{"message":[
		"interface PPPoE0",
		"    ip global 32767",
		"interface Wireguard2",
		"    ip global auto",
		"interface OpkgTun0",
		"    ip global 100"
	]}`)
	svc.queries.RunningConfig = query.NewRunningConfigStore(rcGetter, query.NopLogger())

	poster, ok := svc.transport.(*recordingPoster)
	if !ok {
		t.Fatalf("unexpected transport type %T", svc.transport)
	}
	return svc, store, poster
}

func TestNatStaticTargets_AllGlobalExits(t *testing.T) {
	svc, _, _ := newNATModeTestService(t)
	got, err := svc.natStaticTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"PPPoE0", "Wireguard2", "OpkgTun0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNatStaticTargets_FallbackDefaultWAN(t *testing.T) {
	svc, _, _ := newNATModeTestService(t)
	svc.queries.RunningConfig = nil // running-config недоступен
	got, err := svc.natStaticTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"PPPoE0"} // Routes-фейк harness'а отвечает PPPoE0
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// staticPostTargets собирает to-interface из ip-static POST'ов интерфейса в
// порядке отправки. Формы разные (см. rciSetStaticNAT): add — map
// ({"ip":{"static":{...}}}), remove — slice ({"ip":{"static":[{"no":true,...}]}}).
func staticPostTargets(posts []map[string]interface{}, iface string, removed bool) []string {
	var out []string
	for _, p := range posts {
		ip, ok := p["ip"].(map[string]interface{})
		if !ok {
			continue
		}
		if !removed {
			static, ok := ip["static"].(map[string]interface{})
			if !ok || static["interface"] != iface {
				continue
			}
			if to, ok := static["to-interface"].(string); ok {
				out = append(out, to)
			}
			continue
		}
		arr, ok := ip["static"].([]map[string]interface{})
		if !ok {
			continue
		}
		for _, e := range arr {
			if e["no"] != true || e["interface"] != iface {
				continue
			}
			if to, ok := e["to-interface"].(string); ok {
				out = append(out, to)
			}
		}
	}
	return out
}

func snapshotPosts(p *recordingPoster) []map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	posts := make([]map[string]interface{}, len(p.posts))
	copy(posts, p.posts)
	return posts
}

func TestSetNATMode_InternetOnly_StaticOnAllExits(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATEnabled: true, NATMode: "full",
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "internet-only"); err != nil {
		t.Fatalf("SetNATMode: %v", err)
	}

	want := []string{"PPPoE0", "Wireguard2", "OpkgTun0"}
	if got := staticPostTargets(snapshotPosts(poster), ifaceName, false); !reflect.DeepEqual(got, want) {
		t.Errorf("static adds: got %v, want %v", got, want)
	}
	saved, _ := store.GetManagedServerByID(ifaceName)
	if saved.NATMode != "internet-only" {
		t.Errorf("storage NATMode: got %q, want internet-only", saved.NATMode)
	}
	if saved.NATEnabled {
		t.Errorf("storage NATEnabled: got true, want false for internet-only")
	}
	if !reflect.DeepEqual(saved.NATStaticWANs, want) {
		t.Errorf("NATStaticWANs: got %v, want %v", saved.NATStaticWANs, want)
	}
	if saved.NATStaticWAN != "" {
		t.Errorf("legacy NATStaticWAN must be cleared, got %q", saved.NATStaticWAN)
	}
}

func TestSetNATMode_InternetOnly_RollbackOnStaticFailure(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATEnabled: true, NATMode: "full",
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.failOn = func(p map[string]interface{}) error {
		if ip, ok := p["ip"].(map[string]interface{}); ok {
			if st, ok := ip["static"].(map[string]interface{}); ok {
				if _, remove := st["no"]; !remove && st["to-interface"] == "Wireguard2" {
					return fmt.Errorf("boom")
				}
			}
		}
		return nil
	}
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "internet-only"); err == nil {
		t.Fatal("expected error")
	}

	posts := snapshotPosts(poster)
	if got := staticPostTargets(posts, ifaceName, true); !reflect.DeepEqual(got, []string{"PPPoE0"}) {
		t.Errorf("rollback removes: got %v, want [PPPoE0]", got)
	}
	for _, p := range posts { // no ip nat не отправлялся
		if ip, ok := p["ip"].(map[string]interface{}); ok {
			if natSlice, ok := ip["nat"].([]map[string]interface{}); ok {
				for _, e := range natSlice {
					if e["no"] == true && e["interface"] == ifaceName {
						t.Fatal("no-ip-nat must not be sent on static failure")
					}
				}
			}
		}
	}
	saved, _ := store.GetManagedServerByID(ifaceName)
	if saved.NATMode != "full" {
		t.Errorf("storage must be untouched, NATMode=%q", saved.NATMode)
	}
}

// TestSetNATMode_ReapplyInternetOnly_RollbackKeepsPrevTargets: при повторном
// применении internet-only цели из prevWANs уже имеют static на роутере, а
// `ip nat` уже снят. Откат обязан вернуть состояние ДО вызова, а не пустое:
// снятие таких static оставило бы эти выходы вовсе без подмены источника,
// хотя storage (fail-closed, не тронут) продолжает их числить.
func TestSetNATMode_ReapplyInternetOnly_RollbackKeepsPrevTargets(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATMode: "internet-only",
		NATStaticWANs: []string{"PPPoE0"},
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.failOn = func(p map[string]interface{}) error {
		if ip, ok := p["ip"].(map[string]interface{}); ok {
			if st, ok := ip["static"].(map[string]interface{}); ok && st["to-interface"] == "Wireguard2" {
				return fmt.Errorf("boom")
			}
		}
		return nil
	}
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "internet-only"); err == nil {
		t.Fatal("expected error")
	}

	posts := snapshotPosts(poster)
	if got := staticPostTargets(posts, ifaceName, true); len(got) != 0 {
		t.Errorf("rollback must not remove pre-existing targets; removes: %v", got)
	}
	for _, p := range posts { // no ip nat не отправлялся
		if ip, ok := p["ip"].(map[string]interface{}); ok {
			if natSlice, ok := ip["nat"].([]map[string]interface{}); ok {
				for _, e := range natSlice {
					if e["no"] == true && e["interface"] == ifaceName {
						t.Fatal("no-ip-nat must not be sent on static failure")
					}
				}
			}
		}
	}
	saved, _ := store.GetManagedServerByID(ifaceName)
	if saved.NATMode != "internet-only" || !reflect.DeepEqual(saved.NATStaticWANs, []string{"PPPoE0"}) {
		t.Errorf("storage must be untouched: mode=%q wans=%v", saved.NATMode, saved.NATStaticWANs)
	}
}

func TestSetNATMode_FullAfterInternetOnly_RemovesAllStatics(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATMode: "internet-only",
		NATStaticWANs: []string{"PPPoE0", "Wireguard2"},
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "full"); err != nil {
		t.Fatalf("SetNATMode: %v", err)
	}
	got := staticPostTargets(snapshotPosts(poster), ifaceName, true)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"PPPoE0", "Wireguard2"}) {
		t.Errorf("static removes: got %v", got)
	}
	saved, _ := store.GetManagedServerByID(ifaceName)
	if len(saved.NATStaticWANs) != 0 || saved.NATStaticWAN != "" {
		t.Errorf("static fields must be cleared: %v %q", saved.NATStaticWANs, saved.NATStaticWAN)
	}
}

func TestSetNATMode_FullAfterLegacySingleWAN(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATMode: "internet-only", NATStaticWAN: "PPPoE0",
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "full"); err != nil {
		t.Fatalf("SetNATMode: %v", err)
	}
	if got := staticPostTargets(snapshotPosts(poster), ifaceName, true); !reflect.DeepEqual(got, []string{"PPPoE0"}) {
		t.Errorf("legacy static remove: got %v, want [PPPoE0]", got)
	}
}

func TestSetNATMode_InternetOnly_RemovesStaleTargets(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATMode: "internet-only",
		NATStaticWANs: []string{"PPPoE0", "WireguardX"},
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "internet-only"); err != nil {
		t.Fatalf("SetNATMode: %v", err)
	}
	posts := snapshotPosts(poster)
	if got := staticPostTargets(posts, ifaceName, false); !reflect.DeepEqual(got, []string{"PPPoE0", "Wireguard2", "OpkgTun0"}) {
		t.Errorf("static adds: got %v", got)
	}
	if got := staticPostTargets(posts, ifaceName, true); !reflect.DeepEqual(got, []string{"WireguardX"}) {
		t.Errorf("stale removes: got %v, want [WireguardX]", got)
	}
}

func TestService_Create_SkipsASCWhenDisabled(t *testing.T) {
	svc, store, _ := newCreateTestService(t)

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
		Interfaces:    ifaces,
		Policies:      query.NewPolicyStore(getter, query.NopLogger()),
		WGServers:     query.NewWGServerStore(getter, query.NopLogger(), ifaces),
		RunningConfig: query.NewRunningConfigStore(getter, query.NopLogger()),
		StaticRoutes:  query.NewStaticRouteStore(getter, query.NopLogger()),
	}
	poster := &recordingPoster{onPost: getter.applyPost}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// ACL-операции идут через command.Commands — строим его над тем же
	// recordingPoster, чтобы тесты видели parse-строки в том же журнале.
	// SaveCoordinator настоящий (Request не nil-safe — это осознанно: nil в
	// продакшене должен падать громко), debounce час — save в тестах не летит.
	sc := command.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil)
	cmds := command.NewCommands(command.Deps{Poster: poster, Save: sc, Queries: queries})
	svc := New(poster, nil, queries, cmds, store, log, nil)
	svc.wgRun = func(_ context.Context, _ string, _ ...string) (string, error) {
		return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", nil
	}
	return svc, store, poster
}

// withRunningConfig подменяет стор running-config фикстурой из строк (форма
// стенда: тело блока с отступом 4 пробела). Без него stateAwareGetter отвечает
// на /show/running-config ошибкой — путь «running-config недоступен».
func withRunningConfig(svc *Service, lines ...string) {
	fg := query.NewFakeGetter()
	b, _ := json.Marshal(map[string]any{"message": lines})
	fg.SetJSON("/show/running-config", string(b))
	svc.queries.RunningConfig = query.NewRunningConfigStore(fg, query.NopLogger())
}

// seedServer — managed-сервер 10.66.66.1/24 на iface (как в RebuildOrder).
func seedServer(t *testing.T, store *storage.SettingsStore, iface string) {
	t.Helper()
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: iface, Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820,
	}); err != nil {
		t.Fatalf("seed %s: %v", iface, err)
	}
}

func resetPosts(p *recordingPoster) {
	p.mu.Lock()
	p.posts = nil
	p.mu.Unlock()
}

// parseStrings — parse-строки RCI в порядке отправки.
func parseStrings(p *recordingPoster) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, m := range p.posts {
		if s, ok := m["parse"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// TestSetLANSegments_RebuildOrder verifies that SetLANSegments posts the five
// parse commands in the required order and persists LANSegments in storage.
// Empty-list variant verifies only unbind+remove are sent (no permit/bind).
// Подслучаи с остатком `_WEBADMIN_` пинят снятие чужого permit-all первым.
func TestSetLANSegments_RebuildOrder(t *testing.T) {
	const ifaceName = "Wireguard0"
	acl := "AWGM_" + ifaceName

	assertParses := func(t *testing.T, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("expected %d parse commands, got %d: %v", len(want), len(got), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("parse[%d]: got %q, want %q", i, got[i], want[i])
			}
		}
	}

	// Живой привязанный список — прежняя последовательность.
	ourACL := []string{"access-list AWGM_Wireguard0", "    permit ip 10.66.66.0 255.255.255.0 10.10.10.0 255.255.255.0", "    auto-delete", "!",
		"interface Wireguard0", "    security-level private", "    ip access-group AWGM_Wireguard0 in", "!"}

	t.Run("non-empty segments", func(t *testing.T) {
		svc, store, poster := newLANSegmentsTestService(t)
		ctx := context.Background()
		withRunningConfig(svc, ourACL...)
		seedServer(t, store, ifaceName)
		resetPosts(poster)

		if err := svc.SetLANSegments(ctx, ifaceName, []string{"Home"}); err != nil {
			t.Fatalf("SetLANSegments: %v", err)
		}

		// Expected order:
		// 1. no interface <iface> ip access-group <acl> in
		// 2. no access-list <acl>
		// 3. access-list <acl> permit ip <peerSub> <peerMask> <segSub> <segMask>
		// 4. interface <iface> ip access-group <acl> in
		// 5. access-list <acl> auto-delete
		assertParses(t, parseStrings(poster), []string{
			fmt.Sprintf("no interface %s ip access-group %s in", ifaceName, acl),
			"no access-list " + acl,
			fmt.Sprintf("access-list %s permit ip 10.66.66.0 255.255.255.0 10.10.10.0 255.255.255.0", acl),
			fmt.Sprintf("interface %s ip access-group %s in", ifaceName, acl),
			fmt.Sprintf("access-list %s auto-delete", acl),
		})

		// Storage must be updated.
		saved, ok := store.GetManagedServerByID(ifaceName)
		if !ok {
			t.Fatalf("server not found in storage")
		}
		if len(saved.LANSegments) != 1 || saved.LANSegments[0] != "Home" {
			t.Errorf("storage LANSegments: got %v, want [Home]", saved.LANSegments)
		}
	})

	// Стенд 28.09: первое включение — привязки и списка нет; unbind и
	// `no access-list` вслепую дали бы E «argument parse error» в журнале роутера.
	t.Run("first enable without list: no unbind/remove", func(t *testing.T) {
		svc, store, poster := newLANSegmentsTestService(t)
		withRunningConfig(svc, "interface Wireguard0", "    security-level private", "!")
		seedServer(t, store, ifaceName)
		resetPosts(poster)
		if err := svc.SetLANSegments(context.Background(), ifaceName, []string{"Home"}); err != nil {
			t.Fatalf("SetLANSegments: %v", err)
		}
		assertParses(t, parseStrings(poster), []string{
			fmt.Sprintf("access-list %s permit ip 10.66.66.0 255.255.255.0 10.10.10.0 255.255.255.0", acl),
			fmt.Sprintf("interface %s ip access-group %s in", ifaceName, acl),
			fmt.Sprintf("access-list %s auto-delete", acl),
		})
	})

	t.Run("empty segments unbinds and removes only", func(t *testing.T) {
		svc, store, poster := newLANSegmentsTestService(t)
		ctx := context.Background()
		withRunningConfig(svc, ourACL...)
		seedServer(t, store, ifaceName)
		resetPosts(poster)

		if err := svc.SetLANSegments(ctx, ifaceName, []string{}); err != nil {
			t.Fatalf("SetLANSegments(empty): %v", err)
		}

		assertParses(t, parseStrings(poster), []string{
			fmt.Sprintf("no interface %s ip access-group %s in", ifaceName, acl),
			"no access-list " + acl,
		})

		saved, ok := store.GetManagedServerByID(ifaceName)
		if !ok {
			t.Fatalf("server not found in storage")
		}
		if len(saved.LANSegments) != 0 {
			t.Errorf("storage LANSegments: got %v, want empty", saved.LANSegments)
		}
	})

	// Teardown-ветка тоже не трогает чужой `_WEBADMIN_` (#879): снятие
	// сегментов — не повод сносить правила межсетевого экрана пользователя.
	t.Run("empty segments leave foreign permit-all alone", func(t *testing.T) {
		svc, store, poster := newLANSegmentsTestService(t)
		withRunningConfig(svc,
			"interface Wireguard0",
			"    ip access-group _WEBADMIN_Wireguard0 in",
			"!",
		)
		seedServer(t, store, ifaceName)
		resetPosts(poster)

		if err := svc.SetLANSegments(context.Background(), ifaceName, []string{}); err != nil {
			t.Fatalf("SetLANSegments(empty): %v", err)
		}

		// Нашего списка нет — снимать нечего; чужой не тронут.
		assertParses(t, parseStrings(poster), nil)
	})

}

func TestResolveLANSegmentsPlan(t *testing.T) {
	bridges := []query.LANBridge{
		{Name: "Home", Address: "10.10.10.1", Mask: "255.255.255.0"},
		{Name: "Guest", Address: "10.10.20.1", Mask: "255.255.255.0"},
	}
	t.Run("valid segments → network-subnet permit rules", func(t *testing.T) {
		rules, err := resolveLANSegmentsPlan("10.66.66.1", "255.255.255.0", nil, []string{"Home", "Guest"}, bridges)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []permitRule{
			{srcSub: "10.66.66.0", srcMask: "255.255.255.0", dstSub: "10.10.10.0", dstMask: "255.255.255.0", seg: "Home"},
			{srcSub: "10.66.66.0", srcMask: "255.255.255.0", dstSub: "10.10.20.0", dstMask: "255.255.255.0", seg: "Guest"},
		}
		if len(rules) != len(want) {
			t.Fatalf("got %d rules, want %d: %+v", len(rules), len(want), rules)
		}
		for i := range want {
			if rules[i] != want[i] {
				t.Errorf("rule[%d] = %+v, want %+v", i, rules[i], want[i])
			}
		}
	})
	t.Run("unknown segment errors", func(t *testing.T) {
		if _, err := resolveLANSegmentsPlan("10.66.66.1", "255.255.255.0", nil, []string{"Ghost"}, bridges); err == nil {
			t.Fatal("expected error for unknown segment")
		}
	})
	t.Run("bad peer subnet errors", func(t *testing.T) {
		if _, err := resolveLANSegmentsPlan("not-an-ip", "255.255.255.0", nil, []string{"Home"}, bridges); err == nil {
			t.Fatal("expected error for bad peer subnet")
		}
	})
	t.Run("empty catalog with requested segments errors", func(t *testing.T) {
		if _, err := resolveLANSegmentsPlan("10.66.66.1", "255.255.255.0", nil, []string{"Home"}, nil); err == nil {
			t.Fatal("expected error when catalog empty")
		}
	})
}

func TestUpdate_SubnetChange_RebuildsLANACL(t *testing.T) {
	svc, store, poster := newLANSegmentsTestService(t) // bridge Home @ 10.10.10.0/24
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, LANSegments: []string{"Home"},
		Peers: []storage.ManagedPeer{{PublicKey: "PEER1", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.77.0/24"}}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	withRunningConfig(svc)
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.Update(ctx, ifaceName, UpdateServerRequest{
		Address: "10.77.77.1", Mask: "255.255.255.0", ListenPort: 51820,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()
	foundNew := false
	for _, p := range posts {
		cmd, ok := p["parse"].(string)
		if !ok {
			continue
		}
		if strings.Contains(cmd, "permit ip 10.77.77.0") {
			foundNew = true
		}
		if strings.Contains(cmd, "permit ip 10.66.66.0") {
			t.Errorf("permit still references OLD subnet: %q", cmd)
		}
	}
	if !foundNew {
		t.Errorf("expected permit with new source subnet 10.77.77.0; posts=%v", posts)
	}
	// Сети за клиентом пиров пересборка сохраняет (#713).
	if !slices.Contains(parseStrings(poster), "access-list AWGM_Wireguard0 permit ip 192.168.77.0 255.255.255.0 10.10.10.0 255.255.255.0") {
		t.Errorf("пересборка потеряла сеть за клиентом; posts=%v", parseStrings(poster))
	}
}

func TestSetNATMode_InternetOnly_StaticFails_KeepsNAT(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATEnabled: true, NATMode: "full",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.failOn = func(m map[string]interface{}) error {
		ip, ok := m["ip"].(map[string]interface{})
		if !ok {
			return nil
		}
		if _, ok := ip["static"].(map[string]interface{}); ok { // SET static (map-форма)
			return errors.New("static NAT boom")
		}
		return nil
	}
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "internet-only"); err == nil {
		t.Fatalf("expected error when static NAT fails")
	}
	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()
	for _, p := range posts {
		ip, ok := p["ip"].(map[string]interface{})
		if !ok {
			continue
		}
		if _, ok := ip["nat"].([]map[string]interface{}); ok {
			t.Errorf("normal NAT was disabled despite static NAT failure: %v", p)
		}
	}
	saved, _ := store.GetManagedServerByID(ifaceName)
	if saved.NATMode != "full" {
		t.Errorf("storage NATMode changed despite failure: %q", saved.NATMode)
	}
}

func TestSetNATMode_InternetOnly_DisableFails_RollsBackStatic(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATEnabled: true, NATMode: "full",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.failOn = func(m map[string]interface{}) error {
		ip, ok := m["ip"].(map[string]interface{})
		if !ok {
			return nil
		}
		if _, ok := ip["nat"].([]map[string]interface{}); ok { // disable NAT (slice no:true)
			return errors.New("disable NAT boom")
		}
		return nil
	}
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "internet-only"); err == nil {
		t.Fatalf("expected error when disable NAT fails")
	}
	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()

	staticAdd, natDisable, staticRemove := -1, -1, -1
	for i, p := range posts {
		ip, ok := p["ip"].(map[string]interface{})
		if !ok {
			continue
		}
		if st, ok := ip["static"].(map[string]interface{}); ok && st["interface"] == ifaceName {
			staticAdd = i
		}
		if _, ok := ip["nat"].([]map[string]interface{}); ok {
			natDisable = i
		}
		if arr, ok := ip["static"].([]map[string]interface{}); ok {
			for _, e := range arr {
				if e["no"] == true && e["interface"] == ifaceName {
					staticRemove = i
				}
			}
		}
	}
	if staticAdd == -1 || natDisable == -1 || staticAdd > natDisable {
		t.Fatalf("expected static-NAT add BEFORE nat-disable; staticAdd=%d natDisable=%d", staticAdd, natDisable)
	}
	if staticRemove == -1 || staticRemove <= natDisable {
		t.Fatalf("expected static-NAT rollback AFTER failed disable; staticRemove=%d natDisable=%d", staticRemove, natDisable)
	}
}

func TestSetNATMode_RemovesStaticOnStoredWAN(t *testing.T) {
	svc, store, poster := newNATModeTestService(t) // routes report PPPoE0 как текущий default
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATMode: "internet-only", NATEnabled: false,
		NATStaticWAN: "ISP", // создан на ДРУГОМ WAN, не на текущем default PPPoE0
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "none"); err != nil {
		t.Fatalf("SetNATMode none: %v", err)
	}
	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()
	foundISPremove := false
	for _, p := range posts {
		ip, ok := p["ip"].(map[string]interface{})
		if !ok {
			continue
		}
		if arr, ok := ip["static"].([]map[string]interface{}); ok {
			for _, e := range arr {
				if e["no"] == true && e["to-interface"] == "ISP" {
					foundISPremove = true
				}
				if e["to-interface"] == "PPPoE0" {
					t.Errorf("removed static NAT on CURRENT default WAN, not stored: %v", e)
				}
			}
		}
	}
	if !foundISPremove {
		t.Errorf("expected static-NAT remove on stored WAN ISP; posts=%v", posts)
	}
	saved, _ := store.GetManagedServerByID(ifaceName)
	if saved.NATStaticWAN != "" {
		t.Errorf("NATStaticWAN must be cleared after leaving internet-only; got %q", saved.NATStaticWAN)
	}
}

func TestListLANSegments_ReturnsNetworkCIDR(t *testing.T) {
	svc, _, _ := newLANSegmentsTestService(t) // bridge Home @ 10.10.10.1/24 (host address)
	segs, err := svc.ListLANSegments(context.Background())
	if err != nil {
		t.Fatalf("ListLANSegments: %v", err)
	}
	if len(segs) != 1 {
		t.Fatalf("want 1 segment, got %d", len(segs))
	}
	if segs[0].Subnet != "10.10.10.0/24" {
		t.Errorf("subnet: got %q, want 10.10.10.0/24 (network, not host)", segs[0].Subnet)
	}
}

func TestSetLANSegments_InvalidSegment_DoesNotDestroyACL(t *testing.T) {
	svc, store, poster := newLANSegmentsTestService(t) // bridge Home @ 10.10.10.0/24
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, LANSegments: []string{"Home"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.SetLANSegments(ctx, ifaceName, []string{"Ghost"}); err == nil {
		t.Fatalf("expected error for unknown segment")
	}

	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()
	for _, p := range posts {
		if cmd, ok := p["parse"].(string); ok {
			if strings.Contains(cmd, "no access-list") || strings.Contains(cmd, "no interface") {
				t.Errorf("destructive ACL command sent on invalid input: %q", cmd)
			}
		}
	}
	saved, _ := store.GetManagedServerByID(ifaceName)
	if len(saved.LANSegments) != 1 || saved.LANSegments[0] != "Home" {
		t.Errorf("storage LANSegments changed: %v", saved.LANSegments)
	}

	// Пин места strip: он идёт ПОСЛЕ preflight, поэтому чужой permit-all на
	// невалидном запросе остаётся на месте — роутер не тронут вообще.
	t.Run("с остатком _WEBADMIN_ и сегментом Ghost", func(t *testing.T) {
		svc, store, poster := newLANSegmentsTestService(t)
		withRunningConfig(svc, "interface Wireguard0", "    ip access-group _WEBADMIN_Wireguard0 in", "!")
		seedServer(t, store, ifaceName)
		resetPosts(poster)

		if err := svc.SetLANSegments(context.Background(), ifaceName, []string{"Ghost"}); err == nil {
			t.Fatalf("expected error for unknown segment")
		}
		if got := parseStrings(poster); len(got) != 0 {
			t.Errorf("роутер тронут на невалидном запросе: %v", got)
		}
	})
}

// hasStaticNATPost reports whether any RCI POST touches ip.static (set or remove).
func hasStaticNATPost(posts []map[string]interface{}) bool {
	for _, p := range posts {
		ip, ok := p["ip"].(map[string]interface{})
		if !ok {
			continue
		}
		if _, ok := ip["static"]; ok {
			return true
		}
	}
	return false
}

// TestSetNATMode_Full_NoSpeculativeStaticRemove: a server that was never in
// internet-only (empty NATStaticWAN) must NOT emit a speculative `no ip static`
// when switching to full — there is no static rule to remove, so the live-WAN
// lookup + remove is pure RCI noise.
func TestSetNATMode_Full_NoSpeculativeStaticRemove(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATMode: "none", NATEnabled: false, NATStaticWAN: "",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "full"); err != nil {
		t.Fatalf("SetNATMode full: %v", err)
	}
	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()
	if hasStaticNATPost(posts) {
		t.Errorf("full mode with empty NATStaticWAN must not touch ip.static; posts=%v", posts)
	}
}

// TestSetNATMode_None_NoSpeculativeStaticRemove: same guard for the none mode.
func TestSetNATMode_None_NoSpeculativeStaticRemove(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATMode: "full", NATEnabled: true, NATStaticWAN: "",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "none"); err != nil {
		t.Fatalf("SetNATMode none: %v", err)
	}
	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()
	if hasStaticNATPost(posts) {
		t.Errorf("none mode with empty NATStaticWAN must not touch ip.static; posts=%v", posts)
	}
}

// TestSetNATMode_InternetOnlyToFull_EnablesNATBeforeStaticRemove pins the
// teardown order for the reverse transition: full NAT must be re-enabled
// BEFORE the static rule is removed, so there is never a window without egress.
func TestSetNATMode_InternetOnlyToFull_EnablesNATBeforeStaticRemove(t *testing.T) {
	svc, store, poster := newNATModeTestService(t)
	ctx := context.Background()
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, NATMode: "internet-only", NATEnabled: false,
		NATStaticWAN: "PPPoE0",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.SetNATMode(ctx, ifaceName, "full"); err != nil {
		t.Fatalf("SetNATMode full: %v", err)
	}
	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()

	natEnableIdx, staticRemoveIdx := -1, -1
	for i, p := range posts {
		ip, ok := p["ip"].(map[string]interface{})
		if !ok {
			continue
		}
		if nat, ok := ip["nat"].(map[string]interface{}); ok {
			if nat["interface"] == ifaceName { // enable form is a map (no "no")
				natEnableIdx = i
			}
		}
		if arr, ok := ip["static"].([]map[string]interface{}); ok {
			for _, e := range arr {
				if e["no"] == true && e["interface"] == ifaceName {
					staticRemoveIdx = i
				}
			}
		}
	}
	if natEnableIdx == -1 {
		t.Fatalf("expected ip-nat enable POST; posts=%v", posts)
	}
	if staticRemoveIdx == -1 {
		t.Fatalf("expected static-NAT remove POST on stored WAN; posts=%v", posts)
	}
	if natEnableIdx > staticRemoveIdx {
		t.Errorf("NAT must be enabled (idx %d) BEFORE static removed (idx %d) — egress gap", natEnableIdx, staticRemoveIdx)
	}
}

// findInterfaceMTUPost scans posts for interface.<name>.ip.mtu and returns
// the last value seen.
func findInterfaceMTUPost(posts []map[string]interface{}, name string) (int, bool) {
	val, found := 0, false
	for _, post := range posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		row, ok := iface[name].(map[string]interface{})
		if !ok {
			continue
		}
		ip, ok := row["ip"].(map[string]interface{})
		if !ok {
			continue
		}
		if m, ok := ip["mtu"].(int); ok {
			val, found = m, true
		}
	}
	return val, found
}

func TestService_Create_SetsInterfaceMTU(t *testing.T) {
	svc, _, _ := newCreateTestService(t)

	srv, err := svc.Create(context.Background(), CreateServerRequest{
		Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820, MTU: 1400,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	poster := svc.transport.(*recordingPoster)
	got, found := findInterfaceMTUPost(poster.posts, srv.InterfaceName)
	if !found {
		t.Fatalf("expected ip.mtu in create transaction; posts=%v", poster.posts)
	}
	if got != 1400 {
		t.Errorf("interface ip.mtu = %d, want 1400", got)
	}
}

func TestService_Create_DefaultInterfaceMTU(t *testing.T) {
	svc, _, _ := newCreateTestService(t)

	srv, err := svc.Create(context.Background(), CreateServerRequest{
		Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	poster := svc.transport.(*recordingPoster)
	got, found := findInterfaceMTUPost(poster.posts, srv.InterfaceName)
	if !found {
		t.Fatalf("expected ip.mtu in create transaction; posts=%v", poster.posts)
	}
	if got != DefaultMTU {
		t.Errorf("interface ip.mtu = %d, want default %d", got, DefaultMTU)
	}
}

func TestUpdate_MTUSet_PostsInterfaceMTU(t *testing.T) {
	svc, store, poster := newLANSegmentsTestService(t)
	const ifaceName = "Wireguard0"
	// Legacy server: stored MTU=0, interface never had MTU applied.
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	mtu := 1400
	if err := svc.Update(context.Background(), ifaceName, UpdateServerRequest{
		Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820, MTU: &mtu,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()
	got, found := findInterfaceMTUPost(posts, ifaceName)
	if !found {
		t.Fatalf("expected ip.mtu POST on update; posts=%v", posts)
	}
	if got != 1400 {
		t.Errorf("interface ip.mtu = %d, want 1400", got)
	}
	saved, _ := store.GetManagedServerByID(ifaceName)
	if saved.MTU != 1400 {
		t.Errorf("storage MTU = %d, want 1400", saved.MTU)
	}
}

func TestUpdate_MTUCleared_PostsDefaultMTU(t *testing.T) {
	svc, store, poster := newLANSegmentsTestService(t)
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, MTU: 1400,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	zero := 0
	if err := svc.Update(context.Background(), ifaceName, UpdateServerRequest{
		Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820, MTU: &zero,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()
	got, found := findInterfaceMTUPost(posts, ifaceName)
	if !found {
		t.Fatalf("expected ip.mtu POST on cleared MTU; posts=%v", posts)
	}
	if got != DefaultMTU {
		t.Errorf("interface ip.mtu = %d, want default %d", got, DefaultMTU)
	}
}

func TestUpdate_MTUAbsent_NoInterfaceMTUPost(t *testing.T) {
	svc, store, poster := newLANSegmentsTestService(t)
	const ifaceName = "Wireguard0"
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: ifaceName, Address: "10.66.66.1", Mask: "255.255.255.0",
		ListenPort: 51820, MTU: 1400,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	poster.mu.Lock()
	poster.posts = nil
	poster.mu.Unlock()

	if err := svc.Update(context.Background(), ifaceName, UpdateServerRequest{
		Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	poster.mu.Lock()
	posts := append([]map[string]interface{}{}, poster.posts...)
	poster.mu.Unlock()
	if _, found := findInterfaceMTUPost(posts, ifaceName); found {
		t.Errorf("ip.mtu must not be posted when req.MTU is nil; posts=%v", posts)
	}
}

type recAppLog struct{ entries []string }

func (r *recAppLog) AppLog(level logging.Level, _, _, action, target, message string) {
	r.entries = append(r.entries, string(level)+"|"+action+"|"+target+"|"+message)
}

// Деградация static-NAT до одного WAN раньше была видна только в slog; теперь — в
// журнале приложения (/logs), обе ветки: running-config не читается и `ip global` нет.
func TestNatStaticTargets_DegradationIsInAppLog(t *testing.T) {
	cases := []struct {
		name    string
		rc      func(*query.FakeGetter)
		wantMsg string
	}{
		{"running-config недоступен", func(fg *query.FakeGetter) {}, // без SetJSON → ошибка чтения
			"running-config недоступен ("},
		{"нет ip global", func(fg *query.FakeGetter) {
			fg.SetJSON("/show/running-config", `{"message":["interface Bridge0","    ip address 192.168.1.1","!"]}`)
		}, "в running-config нет ни одного `ip global`: static NAT только на WAN по умолчанию"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := query.NewFakeGetter()
			tc.rc(fg)
			spy := &recAppLog{}
			q := &query.Queries{RunningConfig: query.NewRunningConfigStore(fg, query.NopLogger())}
			svc := New(&fakePoster{}, nil, q, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), spy)
			_, err := svc.natStaticTargets(context.Background())
			if err == nil {
				t.Fatal("без Routes-провайдера фолбэк обязан отказать — тут проверяем только журнал")
			}
			if len(spy.entries) != 1 || !strings.HasPrefix(spy.entries[0], "warn|nat|internet-only|"+tc.wantMsg) {
				t.Fatalf("журнал = %v", spy.entries)
			}
		})
	}
}

// gatePoster задерживает первый POST, чей объект интерфейса подходит под match
// (nil — создание: пустой объект под именем), до закрытия gate — вне замка
// recordingPoster: операция «в полёте» после проверки подсети, адреса на
// роутере ещё нет.
type gatePoster struct {
	rciPoster
	match   func(cfg map[string]interface{}) bool
	armed   atomic.Bool // не sync.Once: Do второго создания ждал бы первое
	entered chan struct{}
	gate    chan struct{}
}

func (g *gatePoster) matches(cfg map[string]interface{}) bool {
	if g.match == nil {
		return len(cfg) == 0
	}
	return g.match(cfg)
}

func (g *gatePoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if m, ok := payload.(map[string]interface{}); ok {
		if intf, ok := m["interface"].(map[string]interface{}); ok {
			for _, v := range intf {
				if cfg, ok := v.(map[string]interface{}); ok && g.matches(cfg) {
					if g.armed.CompareAndSwap(true, false) {
						close(g.entered)
						<-g.gate
					}
				}
			}
		}
	}
	return g.rciPoster.Post(ctx, payload)
}

// createInFlight запускает Create(first) и ждёт, пока он встанет на POST
// создания; finish отпускает его и возвращает результат.
func createInFlight(t *testing.T, svc *Service, first CreateServerRequest) (finish func() error) {
	t.Helper()
	return inFlight(t, svc, func() error {
		_, err := svc.Create(context.Background(), first)
		return err
	})
}

// inFlight — то же для любого создания run.
func inFlight(t *testing.T, svc *Service, run func() error) (finish func() error) {
	t.Helper()
	return inFlightOn(t, svc, nil, run)
}

// inFlightOn — inFlight с задержкой на POST, подходящем под match.
func inFlightOn(t *testing.T, svc *Service, match func(map[string]interface{}) bool, run func() error) (finish func() error) {
	t.Helper()
	gp := &gatePoster{rciPoster: svc.transport, match: match, entered: make(chan struct{}), gate: make(chan struct{})}
	gp.armed.Store(true)
	svc.transport = gp
	done := make(chan error, 1)
	go func() { done <- run() }()
	<-gp.entered
	return func() error {
		close(gp.gate)
		return <-done
	}
}

// F554: подсеть сервера, создание которого в полёте, занята — второе создание
// с той же подсетью отвергается, с другой проходит; оба в итоге в хранилище.
func TestService_Create_ParallelSameSubnetRejected(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	finish := createInFlight(t, svc, CreateServerRequest{Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820})
	_, err := svc.Create(context.Background(), CreateServerRequest{Address: "10.66.66.2", Mask: "255.255.255.0", ListenPort: 51821})
	if err == nil || !strings.Contains(err.Error(), "пересекается") {
		t.Errorf("same subnet while the first create is in flight: err=%v", err)
	}
	// Порт создания в полёте тоже занят (раунд 1, c).
	_, err = svc.Create(context.Background(), CreateServerRequest{Address: "10.88.88.1", Mask: "255.255.255.0", ListenPort: 51820})
	if err == nil || !strings.Contains(err.Error(), "listen-port") {
		t.Errorf("same port while the first create is in flight: err=%v", err)
	}
	if err := finish(); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Шов «резервация снята → список видит»: та же подсеть по-прежнему занята.
	_, err = svc.Create(context.Background(), CreateServerRequest{Address: "10.66.66.3", Mask: "255.255.255.0", ListenPort: 51823})
	if err == nil || !strings.Contains(err.Error(), "пересекается") {
		t.Errorf("same subnet after the first create finished: err=%v", err)
	}
	if _, err := svc.Create(context.Background(), CreateServerRequest{Address: "10.77.77.1", Mask: "255.255.255.0", ListenPort: 51822}); err != nil {
		t.Fatalf("other subnet: %v", err)
	}
	if got := len(store.GetManagedServers()); got != 2 {
		t.Fatalf("servers in storage: %d", got)
	}
}

// F554: OccupiedSubnets (сети за клиентом) видит подсеть создаваемого сервера.
func TestOccupiedSubnets_IncludesServerInFlight(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	finish := createInFlight(t, svc, CreateServerRequest{Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820})
	occ, err := svc.OccupiedSubnets(context.Background(), PeerRef{})
	if err := finish(); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range occ {
		if o.Net.String() == "10.66.66.0/24" {
			found = true
		}
	}
	if !found {
		t.Fatalf("in-flight server subnet not occupied: %+v", occ)
	}
}

// F554: резервация снимается при отказе создания — та же подсеть свободна.
func TestService_Create_FailureReleasesSubnet(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	poster := svc.transport.(*recordingPoster)
	poster.failOn = func(m map[string]interface{}) error {
		for _, v := range m["interface"].(map[string]interface{}) {
			if _, ok := v.(map[string]interface{})["description"]; ok {
				return errors.New("configure rejected")
			}
		}
		return nil
	}
	if _, err := svc.Create(context.Background(), CreateServerRequest{Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820}); err == nil {
		t.Fatal("first create must fail")
	}
	poster.failOn = nil
	if _, err := svc.Create(context.Background(), CreateServerRequest{Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820}); err != nil {
		t.Fatalf("subnet must be free after a failed create: %v", err)
	}
	if got := len(store.GetManagedServers()); got != 1 {
		t.Fatalf("servers in storage: %d", got)
	}
}

// F554: восстановление сервера резервирует подсеть так же, как Create.
func TestRestore_InFlightSubnetReserved(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	finish := inFlight(t, svc, func() error {
		out := svc.Restore(context.Background(), []ManagedServerExport{{
			InterfaceName: "Wireguard0",
			Address:       "10.66.66.1",
			Mask:          "255.255.255.0",
			ListenPort:    51900,
			PrivateKey:    validPrivateKey(1),
			Peers:         []storage.ManagedPeer{},
		}}, RestoreOptions{})
		if len(out) != 1 || out[0].Action != "created" {
			return fmt.Errorf("restore: %+v", out)
		}
		return nil
	})
	_, err := svc.Create(context.Background(), CreateServerRequest{Address: "10.66.66.2", Mask: "255.255.255.0", ListenPort: 51821})
	if err == nil || !strings.Contains(err.Error(), "пересекается") {
		t.Errorf("same subnet while restore is in flight: err=%v", err)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if got := len(store.GetManagedServers()); got != 1 {
		t.Fatalf("servers in storage: %d", got)
	}
}

// Раунд 1 (b, c): правка резервирует новые подсеть и порт так же, как Create.
func TestUpdate_InFlightSubnetAndPortReserved(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	ctx := context.Background()
	first, err := svc.Create(ctx, CreateServerRequest{Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820})
	if err != nil {
		t.Fatal(err)
	}
	finish := inFlightOn(t, svc, func(cfg map[string]interface{}) bool { _, ok := cfg["ip"]; return ok }, func() error {
		return svc.Update(ctx, first.InterfaceName, UpdateServerRequest{Address: "10.88.88.1", Mask: "255.255.255.0", ListenPort: 51830})
	})
	_, err = svc.Create(ctx, CreateServerRequest{Address: "10.88.88.2", Mask: "255.255.255.0", ListenPort: 51840})
	if err == nil || !strings.Contains(err.Error(), "пересекается") {
		t.Errorf("subnet of an update in flight: err=%v", err)
	}
	_, err = svc.Create(ctx, CreateServerRequest{Address: "10.99.99.1", Mask: "255.255.255.0", ListenPort: 51830})
	if err == nil || !strings.Contains(err.Error(), "listen-port") {
		t.Errorf("port of an update in flight: err=%v", err)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if got := len(store.GetManagedServers()); got != 1 {
		t.Fatalf("servers in storage: %d", got)
	}
}

// configureFailsDeleteFails: configure применяется на «роутере» и отвергается,
// снос отвергается и не применяется — интерфейс с адресом остаётся.
func configureFailsDeleteFails(svc *Service, getter *stateAwareGetter) *recordingPoster {
	poster := svc.transport.(*recordingPoster)
	isNo := func(m map[string]interface{}) bool {
		for _, v := range m["interface"].(map[string]interface{}) {
			if no, _ := v.(map[string]interface{})["no"].(bool); no {
				return true
			}
		}
		return false
	}
	poster.onPost = func(m map[string]interface{}) {
		if !isNo(m) {
			getter.applyPost(m)
		}
	}
	poster.failOn = func(m map[string]interface{}) error {
		if isNo(m) {
			return errors.New("delete rejected")
		}
		for _, v := range m["interface"].(map[string]interface{}) {
			if _, ok := v.(map[string]interface{})["description"]; ok {
				return errors.New("configure rejected")
			}
		}
		return nil
	}
	return poster
}

// Q1: configure применился частично, снос не прошёл — следующая проверка (в
// пределах SnapshotRecent) видит адрес: метка «грязно» до снятия резервации.
func TestCreate_ConfigureAndCleanupFail_SubnetStillSeen(t *testing.T) {
	svc, _, getter := newCreateTestService(t)
	poster := configureFailsDeleteFails(svc, getter)
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateServerRequest{Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820}); err == nil {
		t.Fatal("first create must fail")
	}
	poster.failOn = nil
	_, err := svc.Create(ctx, CreateServerRequest{Address: "10.66.66.2", Mask: "255.255.255.0", ListenPort: 51821})
	if err == nil || !strings.Contains(err.Error(), "пересекается") {
		t.Fatalf("leftover address must be seen: err=%v", err)
	}
}

// Q1 для restore: тот же откат в applyOne.
func TestRestore_ConfigureAndCleanupFail_SubnetStillSeen(t *testing.T) {
	svc, _, getter := newCreateTestService(t)
	poster := configureFailsDeleteFails(svc, getter)
	ctx := context.Background()
	out := svc.Restore(ctx, []ManagedServerExport{{
		InterfaceName: "Wireguard0", Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51900,
		PrivateKey: validPrivateKey(1), Peers: []storage.ManagedPeer{},
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "failed" {
		t.Fatalf("restore must fail: %+v", out)
	}
	poster.failOn = nil
	_, err := svc.Create(ctx, CreateServerRequest{Address: "10.66.66.2", Mask: "255.255.255.0", ListenPort: 51821})
	if err == nil || !strings.Contains(err.Error(), "пересекается") {
		t.Fatalf("leftover address must be seen: err=%v", err)
	}
}

// Раунд 2: отказ rciUpdateServer после применения (ответ потерян) — новая
// подсеть видна следующей проверке в пределах SnapshotRecent.
func TestUpdate_FailureAfterApply_NewSubnetStillSeen(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	ctx := context.Background()
	first, err := svc.Create(ctx, CreateServerRequest{Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820})
	if err != nil {
		t.Fatal(err)
	}
	poster := svc.transport.(*recordingPoster)
	poster.failOn = func(m map[string]interface{}) error {
		for _, v := range m["interface"].(map[string]interface{}) {
			if _, ok := v.(map[string]interface{})["ip"]; ok {
				return errors.New("reply lost")
			}
		}
		return nil
	}
	if err := svc.Update(ctx, first.InterfaceName, UpdateServerRequest{Address: "10.88.88.1", Mask: "255.255.255.0", ListenPort: 51820}); err == nil {
		t.Fatal("update must fail")
	}
	poster.failOn = nil
	_, err = svc.Create(ctx, CreateServerRequest{Address: "10.88.88.2", Mask: "255.255.255.0", ListenPort: 51821})
	if err == nil || !strings.Contains(err.Error(), "пересекается") {
		t.Fatalf("applied subnet of a failed update must be seen: err=%v", err)
	}
}

// Раунд 2: reserve/release и чтение резерваций параллельно — под -race гонка
// на картах резерваций (release без лока) видна сразу.
func TestReservations_ConcurrentReserveRelease(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				release, err := svc.reserveServerSubnet(ctx, fmt.Sprintf("10.%d.%d.1", 100+i, j), "255.255.255.0", 40000+i*100+j, "")
				if err != nil {
					t.Error(err)
					return
				}
				release()
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				svc.reservations()
			}
		}()
	}
	wg.Wait()
	if subs, ports := svc.reservations(); len(subs) != 0 || len(ports) != 0 {
		t.Fatalf("leaked reservations: %v %v", subs, ports)
	}
}
