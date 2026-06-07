package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type restoreLiveGetter struct {
	live map[string]restoreLiveEntry
	asc  map[string]map[string]string
}

type restoreLiveEntry struct {
	Present   bool
	Address   string
	Mask      string
	PublicKey string
}

func (g *restoreLiveGetter) Get(ctx context.Context, path string, out any) error {
	if strings.HasPrefix(path, "/show/rc/interface/") && strings.HasSuffix(path, "/wireguard/asc") {
		iface := strings.TrimSuffix(strings.TrimPrefix(path, "/show/rc/interface/"), "/wireguard/asc")
		src, ok := g.asc[iface]
		if !ok {
			src = map[string]string{
				"jc": "0", "jmin": "0", "jmax": "0", "s1": "0", "s2": "0",
				"h1": "", "h2": "", "h3": "", "h4": "",
				"s3": "0", "s4": "0",
			}
		}
		b, _ := json.Marshal(src)
		return json.Unmarshal(b, out)
	}
	if path != "/show/interface/" {
		return errors.New("unsupported path: " + path)
	}
	m := map[string]json.RawMessage{}
	for name, ent := range g.live {
		if !ent.Present {
			continue
		}
		addr := ent.Address
		if addr == "" {
			addr = "10.0.0.1"
		}
		mask := ent.Mask
		if mask == "" {
			mask = "255.255.255.0"
		}
		entry := map[string]any{
			"id":             name,
			"interface-name": name,
			"type":           "Wireguard",
			"description":    ManagedServerDescription,
			"address":        addr,
			"mask":           mask,
		}
		raw, _ := json.Marshal(entry)
		m[name] = raw
	}
	b, _ := json.Marshal(m)
	return json.Unmarshal(b, out)
}

func (g *restoreLiveGetter) applyPost(payload map[string]interface{}) {
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
				continue
			}
		}
		ascMap, ok := ascRaw.(map[string]interface{})
		if !ok {
			// Fallback for map[any]any-like payloads.
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
		if _, ok := state["s3"]; !ok {
			state["s3"] = "0"
		}
		if _, ok := state["s4"]; !ok {
			state["s4"] = "0"
		}
		g.asc[ifaceName] = state
	}
}

func (g *restoreLiveGetter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if !strings.HasPrefix(path, "/show/interface/") {
		return nil, errors.New("unsupported path: " + path)
	}
	name := strings.TrimPrefix(path, "/show/interface/")
	ent, ok := g.live[name]
	if !ok || !ent.Present {
		return []byte{}, nil
	}
	addr := ent.Address
	if addr == "" {
		addr = "10.0.0.1"
	}
	mask := ent.Mask
	if mask == "" {
		mask = "255.255.255.0"
	}
	wire := map[string]any{
		"id":             name,
		"interface-name": name,
		"type":           "Wireguard",
		"description":    ManagedServerDescription,
		"address":        addr,
		"mask":           mask,
		"state":          "up",
		"link":           "up",
		"connected":      "yes",
		"wireguard": map[string]any{
			"public-key": ent.PublicKey,
			"peer":       []map[string]any{},
		},
	}
	return json.Marshal(wire)
}

func (g *restoreLiveGetter) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	top, ok := payload.(map[string]any)
	if !ok {
		return nil, errors.New("unsupported payload")
	}
	show, ok := top["show"].(map[string]any)
	if !ok {
		return nil, errors.New("unsupported payload.show")
	}
	ifaceReq, ok := show["interface"].(map[string]any)
	if !ok {
		return nil, errors.New("unsupported payload.show.interface")
	}
	name, _ := ifaceReq["name"].(string)
	if name == "" {
		return nil, errors.New("empty interface name")
	}
	ent, ok := g.live[name]
	if !ok || !ent.Present {
		return []byte(`{"show":{"interface":{}}}`), nil
	}
	addr := ent.Address
	if addr == "" {
		addr = "10.0.0.1"
	}
	mask := ent.Mask
	if mask == "" {
		mask = "255.255.255.0"
	}
	wire := map[string]any{
		"id":             name,
		"interface-name": name,
		"type":           "Wireguard",
		"description":    ManagedServerDescription,
		"address":        addr,
		"mask":           mask,
		"state":          "up",
		"link":           "up",
		"connected":      "yes",
		"wireguard": map[string]any{
			"public-key": ent.PublicKey,
			"peer":       []map[string]any{},
		},
	}
	resp := map[string]any{
		"show": map[string]any{
			"interface": wire,
		},
	}
	raw, _ := json.Marshal(resp)
	return raw, nil
}
func mustDerivePublicKey(t *testing.T, privateKey string) string {
	t.Helper()
	pub, err := derivePublicKeyFromPrivate(privateKey)
	if err != nil {
		t.Fatalf("derive pub key: %v", err)
	}
	return pub
}

func validPrivateKey(seed byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestRestore_CreatesNewServerHappyPath(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()

	// stateAwareGetter answers /show/interface/ from the live SettingsStore
	// so preflight's listUsedSubnets does not panic on nil queries.
	getter := &stateAwareGetter{store: store}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	outcomes := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Description:   "Home VPN",
		Address:       "10.99.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51900,
		PrivateKey:    validPrivateKey(1),
		NATEnabled:    true,
		Peers:         []storage.ManagedPeer{},
	}}, RestoreOptions{})

	if len(outcomes) != 1 || outcomes[0].Action != "created" {
		t.Fatalf("outcomes: %+v", outcomes)
	}
	// create + configure + set-key + set-NAT = at least 4 RCI calls.
	if len(poster.posts) < 4 {
		t.Errorf("expected at least 4 RCI calls (create, configure, set-key, set-NAT); got %d", len(poster.posts))
	}
	got, ok := store.GetManagedServerByID("Wireguard0")
	if !ok {
		t.Fatalf("server not persisted to storage")
	}
	if strings.TrimSpace(got.PrivateKey) == "" {
		t.Errorf("PrivateKey not persisted: got empty")
	}
}

func TestRestoreDrift_CreatesInterfaceWhenOnlyStorageExists(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Description:   "Drifted",
		Address:       "10.40.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51830,
		PrivateKey:    validPrivateKey(2),
		Policy:        "none",
		Peers:         []storage.ManagedPeer{},
	})

	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: false}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	drift, err := s.Drift(context.Background())
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	out := s.RestoreDrift(context.Background(), drift, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("outcomes: %+v", out)
	}
	if len(poster.posts) < 4 {
		t.Fatalf("expected create/configure/key/nat posts, got %d", len(poster.posts))
	}
}

func TestRestoreDrift_CreatesInterfaceAndPeers(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Description:   "Drifted",
		Address:       "10.41.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51831,
		PrivateKey:    validPrivateKey(3),
		Policy:        "none",
		Peers: []storage.ManagedPeer{
			{PublicKey: "PUBX", TunnelIP: "10.41.0.2/32", Enabled: true},
		},
	})

	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: false}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	drift, err := s.Drift(context.Background())
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	out := s.RestoreDrift(context.Background(), drift, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("outcomes: %+v", out)
	}
	foundPeerAdd := false
	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		wg0, ok := iface["Wireguard0"].(map[string]interface{})
		if !ok {
			continue
		}
		wg, ok := wg0["wireguard"].(map[string]interface{})
		if !ok {
			continue
		}
		if _, ok := wg["peer"]; ok {
			foundPeerAdd = true
			break
		}
	}
	if !foundPeerAdd {
		t.Fatalf("expected peer add payload during drift restore")
	}
}

func TestRestore_DoesNotCleanupWhenCreateFails(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()

	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: false}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{err: errors.New("already exists")}
	s := &Service{settings: store, transport: poster, queries: queries}

	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.50.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51840,
		PrivateKey:    validPrivateKey(4),
		Policy:        "none",
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "failed" {
		t.Fatalf("outcomes: %+v", out)
	}
	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		if row, ok := iface["Wireguard0"].(map[string]interface{}); ok {
			if no, ok := row["no"].(bool); ok && no {
				t.Fatalf("unexpected cleanup delete payload on failed create: %+v", post)
			}
		}
	}
}

func TestRestore_MergeRejectsDuplicatePeerPublicKey(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Address:       "10.60.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51850,
		PrivateKey:    validPrivateKey(5),
		Policy:        "none",
		Peers:         []storage.ManagedPeer{},
	})

	priv := validPrivateKey(5)
	pub := mustDerivePublicKey(t, priv)
	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {
		Present:   true,
		Address:   "10.60.0.1",
		Mask:      "255.255.255.0",
		PublicKey: pub,
	}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.60.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51850,
		PrivateKey:    priv,
		Policy:        "none",
		Peers: []storage.ManagedPeer{
			{PublicKey: "PUB1", TunnelIP: "10.60.0.2/32", Enabled: true},
			{PublicKey: "PUB1", TunnelIP: "10.60.0.3/32", Enabled: true},
		},
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "conflict" {
		t.Fatalf("outcomes: %+v", out)
	}
	if len(out[0].Conflicts) == 0 || !strings.Contains(out[0].Conflicts[0], "duplicate peer public key") {
		t.Fatalf("unexpected conflicts: %+v", out[0].Conflicts)
	}
	if len(poster.posts) != 0 {
		t.Fatalf("expected no RCI calls on merge preflight failure, got %d", len(poster.posts))
	}
}

func TestRestore_MergeAppliesASCAndPersistsIFields(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	priv := validPrivateKey(55)
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Address:       "10.61.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51851,
		PrivateKey:    priv,
		Policy:        "none",
		Peers:         []storage.ManagedPeer{},
	})

	pub := mustDerivePublicKey(t, priv)
	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {
		Present:   true,
		Address:   "10.61.0.1",
		Mask:      "255.255.255.0",
		PublicKey: pub,
	}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	asc := json.RawMessage(`{"jc":3,"jmin":77,"jmax":266,"s1":18,"s2":29,"h1":"103994526","h2":"1201929360","h3":"2403636727","h4":"3602647725","i1":"AA","i2":"BB","i3":"CC","i4":"DD","i5":"EE"}`)
	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.61.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51851,
		PrivateKey:    priv,
		Policy:        "none",
		ASC:           asc,
		Peers:         []storage.ManagedPeer{},
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "merged" {
		t.Fatalf("outcomes: %+v", out)
	}

	foundASC := false
	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		wg0, ok := iface["Wireguard0"].(map[string]interface{})
		if !ok {
			continue
		}
		wg, ok := wg0["wireguard"].(map[string]interface{})
		if !ok {
			continue
		}
		ascPayload, ok := wg["asc"].(map[string]interface{})
		if !ok {
			continue
		}
		if _, hasI1 := ascPayload["i1"]; hasI1 {
			t.Fatalf("i1 must be stripped before NDMS ASC apply payload")
		}
		if _, hasJC := ascPayload["jc"]; hasJC {
			foundASC = true
		}
	}
	if !foundASC {
		t.Fatalf("expected ASC payload in merge path")
	}

	got, ok := store.GetManagedServerByID("Wireguard0")
	if !ok {
		t.Fatalf("server not persisted")
	}
	if got.I1 != "AA" || got.I2 != "BB" || got.I3 != "CC" || got.I4 != "DD" || got.I5 != "EE" {
		t.Fatalf("expected I1..I5 persisted from merge ASC input, got: %+v", got)
	}
}

func TestRestore_PolicyNoneDoesNotEmitClearOnCreate(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: false}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}
	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.80.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51870,
		PrivateKey:    validPrivateKey(6),
		Policy:        "none",
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("outcomes: %+v", out)
	}
	for _, post := range poster.posts {
		ipObj, ok := post["ip"].(map[string]interface{})
		if !ok {
			continue
		}
		hotspot, ok := ipObj["hotspot"].(map[string]interface{})
		if !ok {
			continue
		}
		pol, ok := hotspot["policy"].(map[string]interface{})
		if !ok {
			continue
		}
		if no, ok := pol["no"].(bool); ok && no {
			t.Fatalf("unexpected clear policy payload on create: %+v", post)
		}
	}
}

func TestRestore_PolicyProfileEmitsSetOnCreate(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: false}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}
	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.81.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51871,
		PrivateKey:    validPrivateKey(7),
		Policy:        "Policy0",
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("outcomes: %+v", out)
	}
	foundSetPolicy := false
	for _, post := range poster.posts {
		ipObj, ok := post["ip"].(map[string]interface{})
		if !ok {
			continue
		}
		hotspot, ok := ipObj["hotspot"].(map[string]interface{})
		if !ok {
			continue
		}
		pol, ok := hotspot["policy"].(map[string]interface{})
		if !ok {
			continue
		}
		if access, ok := pol["policy"].(string); ok && access == "Policy0" {
			foundSetPolicy = true
			break
		}
	}
	if !foundSetPolicy {
		t.Fatalf("expected set policy payload on create")
	}
}

func TestRestore_BatchDuplicateNamesReturnsOneOutcomePerInput(t *testing.T) {
	s := &Service{}
	in := []ManagedServerExport{
		{InterfaceName: "Wireguard0", Address: "10.90.0.1", Mask: "255.255.255.0", ListenPort: 51820, PrivateKey: "K1"},
		{InterfaceName: "Wireguard0", Address: "10.91.0.1", Mask: "255.255.255.0", ListenPort: 51820, PrivateKey: "K2"},
	}
	out := s.restoreWithMode(context.Background(), in, RestoreOptions{}, false)
	if len(out) != len(in) {
		t.Fatalf("len(out)=%d want %d", len(out), len(in))
	}
	for _, o := range out {
		if o.Action != "conflict" {
			t.Fatalf("unexpected action: %+v", o)
		}
	}
}

func TestRestore_RenamePersistRollbackRestoresOldOnAddFailure(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Address:       "10.95.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51825,
		PrivateKey:    validPrivateKey(8),
		Policy:        "none",
	})
	// Occupy would-be target in storage so AddManagedServer(target) fails.
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard1",
		Address:       "10.96.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51826,
		PrivateKey:    "OTHER=",
		Policy:        "none",
	})

	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{
		"Wireguard0": {Present: true, Address: "10.95.0.1", Mask: "255.255.255.0", PublicKey: mustDerivePublicKey(t, validPrivateKey(99))},
		"Wireguard1": {Present: false},
	}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	// Drift-mode direct call to exercise rename persist path.
	out := s.restoreWithMode(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.97.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51835,
		PrivateKey:    validPrivateKey(8),
		Policy:        "none",
	}}, RestoreOptions{AllowRenumber: true}, true)
	if len(out) != 1 || out[0].Action != "failed" {
		t.Fatalf("outcomes: %+v", out)
	}
	if _, ok := store.GetManagedServerByID("Wireguard0"); !ok {
		t.Fatalf("old storage entry must remain after rename add failure")
	}
}

func TestRestore_InvalidPrivateKeyConflictsBeforeRCI(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: false}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.200.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    52000,
		PrivateKey:    "not-base64-private-key",
		Policy:        "none",
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "conflict" {
		t.Fatalf("outcomes: %+v", out)
	}
	if len(out[0].Conflicts) == 0 || !strings.Contains(out[0].Conflicts[0], "invalid server private key") {
		t.Fatalf("unexpected conflicts: %+v", out[0].Conflicts)
	}
	if len(poster.posts) != 0 {
		t.Fatalf("expected no RCI calls for invalid private key, got %d", len(poster.posts))
	}
}

func TestRestore_LiveSameAddressMaskButDifferentIdentityDoesNotMerge(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	privStored := validPrivateKey(10)
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Address:       "10.100.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51910,
		PrivateKey:    privStored,
		Policy:        "none",
	})

	foreignPriv := validPrivateKey(11)
	foreignPub := mustDerivePublicKey(t, foreignPriv)
	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{
		"Wireguard0": {
			Present:   true,
			Address:   "10.100.0.1",
			Mask:      "255.255.255.0",
			PublicKey: foreignPub, // same subnet, different identity
		},
	}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.100.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51910,
		PrivateKey:    privStored,
		Policy:        "none",
		Peers: []storage.ManagedPeer{
			{PublicKey: "PUB-NEW", TunnelIP: "10.100.0.2/32", Enabled: true},
		},
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "conflict" {
		t.Fatalf("outcomes: %+v", out)
	}
	if !strings.Contains(strings.Join(out[0].Conflicts, " "), "occupied by a different server") {
		t.Fatalf("unexpected conflicts: %+v", out[0].Conflicts)
	}
	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		if wg0, ok := iface["Wireguard0"].(map[string]interface{}); ok {
			if _, ok := wg0["wireguard"]; ok {
				t.Fatalf("must not merge/add peers into foreign live identity")
			}
		}
	}
}

func TestRestore_LiveInterfaceWithNilWGServersIsTreatedAsForeignOccupied(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	priv := validPrivateKey(22)
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Address:       "10.140.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51960,
		PrivateKey:    priv,
		Policy:        "none",
	})

	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{
		"Wireguard0": {
			Present: true,
			Address: "10.140.0.1",
			Mask:    "255.255.255.0",
		},
	}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		// WGServers intentionally nil to verify safe behavior:
		// live exists but identity cannot be proven -> treat as foreign occupied slot.
		WGServers: nil,
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.140.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51960,
		PrivateKey:    priv,
		Policy:        "none",
	}}, RestoreOptions{AllowRenumber: false})

	if len(out) != 1 || out[0].Action != "conflict" {
		t.Fatalf("outcomes: %+v", out)
	}
	if !strings.Contains(strings.Join(out[0].Conflicts, " "), "occupied by a different server") {
		t.Fatalf("unexpected conflicts: %+v", out[0].Conflicts)
	}
	if len(poster.posts) != 0 {
		t.Fatalf("expected no RCI calls for foreign occupied slot, got %d", len(poster.posts))
	}
}

func TestRestore_AllowRenumberWithStorageSameIdentityUsesRenamePersistMode(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	priv := validPrivateKey(12)
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Address:       "10.110.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51920,
		PrivateKey:    priv,
		Policy:        "none",
	})

	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{
		"Wireguard0": {Present: true, Address: "10.110.0.1", Mask: "255.255.255.0", PublicKey: mustDerivePublicKey(t, validPrivateKey(13))},
		"Wireguard1": {Present: false},
	}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.120.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51930,
		PrivateKey:    priv,
		Policy:        "none",
	}}, RestoreOptions{AllowRenumber: true})
	if len(out) != 1 || out[0].Action != "renamed" {
		t.Fatalf("outcomes: %+v", out)
	}
	if _, ok := store.GetManagedServerByID("Wireguard0"); ok {
		t.Fatalf("old storage entry must be renamed away")
	}
	if _, ok := store.GetManagedServerByID("Wireguard1"); !ok {
		t.Fatalf("renamed storage entry Wireguard1 not found")
	}
}

func TestRestore_RenumberDoesNotExcludeOldForeignSlotFromConflicts(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	priv := validPrivateKey(14)
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Address:       "10.130.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51940,
		PrivateKey:    priv,
		Policy:        "none",
	})

	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{
		"Wireguard0": {Present: true, Address: "10.130.0.1", Mask: "255.255.255.0", PublicKey: mustDerivePublicKey(t, validPrivateKey(15))},
		"Wireguard1": {Present: false},
	}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	s := &Service{settings: store, queries: queries}

	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.130.0.1", // overlaps foreign live slot on old name
		Mask:          "255.255.255.0",
		ListenPort:    51950,
		PrivateKey:    priv,
		Policy:        "none",
	}}, RestoreOptions{AllowRenumber: true})
	if len(out) != 1 || out[0].Action != "conflict" {
		t.Fatalf("outcomes: %+v", out)
	}
}

func TestRestore_AddPeerRespectsEnabledFlag(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: false}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.70.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51860,
		PrivateKey:    validPrivateKey(9),
		Policy:        "none",
		Peers: []storage.ManagedPeer{
			{PublicKey: "PUB2", TunnelIP: "10.70.0.2/32", Enabled: false},
		},
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("outcomes: %+v", out)
	}

	foundConnectFalse := false
	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		wg0, ok := iface["Wireguard0"].(map[string]interface{})
		if !ok {
			continue
		}
		wg, ok := wg0["wireguard"].(map[string]interface{})
		if !ok {
			continue
		}
		switch peers := wg["peer"].(type) {
		case []interface{}:
			if len(peers) == 0 {
				continue
			}
			first, ok := peers[0].(map[string]interface{})
			if !ok {
				continue
			}
			if c, ok := first["connect"].(bool); ok && !c {
				foundConnectFalse = true
				break
			}
		case []map[string]interface{}:
			if len(peers) == 0 {
				continue
			}
			if c, ok := peers[0]["connect"].(bool); ok && !c {
				foundConnectFalse = true
				break
			}
		}
	}
	if !foundConnectFalse {
		t.Fatalf("expected connect=false in peer add payload")
	}
}

func TestRestore_AppliesASCAndPersistsIFields(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: false}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	asc := json.RawMessage(`{"jc":3,"jmin":77,"jmax":266,"s1":18,"s2":29,"h1":"103994526","h2":"1201929360","h3":"2403636727","h4":"3602647725","i1":"AA","i2":"BB","i3":"CC","i4":"DD","i5":"EE"}`)
	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.170.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    52010,
		PrivateKey:    validPrivateKey(31),
		Policy:        "none",
		ASC:           asc,
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("outcomes: %+v", out)
	}

	foundASC := false
	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		wg0, ok := iface["Wireguard0"].(map[string]interface{})
		if !ok {
			continue
		}
		wg, ok := wg0["wireguard"].(map[string]interface{})
		if !ok {
			continue
		}
		ascPayload, ok := wg["asc"].(map[string]interface{})
		if !ok {
			continue
		}
		if _, hasI1 := ascPayload["i1"]; hasI1 {
			t.Fatalf("i1 must be stripped before NDMS ASC apply payload")
		}
		if _, hasJC := ascPayload["jc"]; hasJC {
			foundASC = true
		}
	}
	if !foundASC {
		t.Fatalf("expected ASC apply payload during restore")
	}

	got, ok := store.GetManagedServerByID("Wireguard0")
	if !ok {
		t.Fatalf("server not persisted")
	}
	if got.I1 != "AA" || got.I2 != "BB" || got.I3 != "CC" || got.I4 != "DD" || got.I5 != "EE" {
		t.Fatalf("expected I1..I5 persisted from ASC input, got: %+v", got)
	}
}

func TestRestore_WithoutASC_DoesNotAutoApplyASC(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()
	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: false}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.171.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    52011,
		PrivateKey:    validPrivateKey(41),
		Policy:        "none",
	}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("outcomes: %+v", out)
	}

	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		wg0, ok := iface["Wireguard0"].(map[string]interface{})
		if !ok {
			continue
		}
		wg, ok := wg0["wireguard"].(map[string]interface{})
		if !ok {
			continue
		}
		if _, hasASC := wg["asc"]; hasASC {
			t.Fatalf("ASC payload must not be auto-applied when backup ASC is missing")
		}
	}
}

func TestRestore_RejectsEmptyPrivateKey(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()

	s := &Service{settings: store}
	outcomes := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.0.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51820,
		// PrivateKey deliberately empty
	}}, RestoreOptions{})

	if len(outcomes) != 1 || outcomes[0].Action != "failed" {
		t.Fatalf("outcomes: %+v", outcomes)
	}
}

func TestRestore_PreflightDetectsInvalidAddress(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()

	s := &Service{settings: store}
	outcomes := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "not-an-ip",
		Mask:          "255.255.255.0",
		ListenPort:    51820,
		PrivateKey:    validPrivateKey(21),
	}}, RestoreOptions{})

	if outcomes[0].Action != "conflict" {
		t.Fatalf("action: %q, conflicts: %v", outcomes[0].Action, outcomes[0].Conflicts)
	}
	found := false
	for _, c := range outcomes[0].Conflicts {
		if strings.Contains(c, "invalid IP address") {
			found = true
		}
	}
	if !found {
		t.Errorf("conflicts: %v (expected invalid-IP reason)", outcomes[0].Conflicts)
	}
}

func TestRestore_InternetOnly_PersistsNATStaticWAN(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_, _ = store.Load()

	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: false}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}

	// Wire a Routes store that reports PPPoE0 as the default gateway.
	routeGetter := query.NewFakeGetter()
	routeGetter.SetJSON("/show/ip/route", `[{"destination":"0.0.0.0/0","gateway":"1.2.3.4","interface":"PPPoE0"}]`)
	queries.Routes = query.NewRouteStore(routeGetter, query.NopLogger())

	poster := &fakePoster{onPost: getter.applyPost}
	s := &Service{settings: store, transport: poster, queries: queries}

	out := s.Restore(context.Background(), []ManagedServerExport{{
		InterfaceName: "Wireguard0",
		Address:       "10.180.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    52020,
		PrivateKey:    validPrivateKey(51),
		NATMode:       "internet-only",
		NATStaticWAN:  "", // empty — must be filled in from live WAN
	}}, RestoreOptions{})

	if len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("outcomes: %+v", out)
	}

	got, ok := store.GetManagedServerByID("Wireguard0")
	if !ok {
		t.Fatalf("server not persisted to storage")
	}
	if got.NATMode != "internet-only" {
		t.Errorf("storage NATMode: got %q, want internet-only", got.NATMode)
	}
	if got.NATStaticWAN != "PPPoE0" {
		t.Errorf("storage NATStaticWAN: got %q, want PPPoE0", got.NATStaticWAN)
	}
}
