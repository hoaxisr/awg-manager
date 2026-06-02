package nwg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// fakeNDMS is a stateful NDMS stub that models Wireguard interface
// create / delete / list / show. Unlike captureServer it tracks WHICH
// interfaces exist — exactly the state issue #255 hinges on: nextFreeIndex
// reads the InterfaceStore cache, and the cache must reflect a just-created
// (or just-deleted) interface for back-to-back allocations to be correct.
type fakeNDMS struct {
	srv   *httptest.Server
	mu    sync.Mutex
	known map[string]bool // interface id -> exists
}

func newFakeNDMS(t *testing.T, seed ...string) *fakeNDMS {
	t.Helper()
	f := &fakeNDMS{known: map[string]bool{}}
	for _, s := range seed {
		f.known[s] = true
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNDMS) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	// fetchListMap: GET /show/interface/ -> { id: {iface}, ... }
	if r.Method == http.MethodGet {
		f.mu.Lock()
		out := map[string]any{}
		for id := range f.known {
			out[id] = map[string]any{"id": id, "type": "Wireguard"}
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(out)
		return
	}

	trimmed := strings.TrimLeft(string(body), " \t\r\n")

	// RCI batch (create path): register interfaces, reply length-matched.
	if strings.HasPrefix(trimmed, "[") {
		var arr []map[string]any
		_ = json.Unmarshal(body, &arr)
		f.mu.Lock()
		for _, cmd := range arr {
			f.applyInterfaceCmd(cmd)
		}
		f.mu.Unlock()
		out := make([]map[string]any, len(arr))
		for i := range out {
			out[i] = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}

	// Single POST: either a fetchOne show query, or CmdInterfaceDelete.
	var single map[string]any
	_ = json.Unmarshal(body, &single)

	if showRaw, ok := single["show"]; ok {
		show, _ := showRaw.(map[string]any)
		ifaceQ, _ := show["interface"].(map[string]any)
		name, _ := ifaceQ["name"].(string)
		f.mu.Lock()
		exists := f.known[name]
		f.mu.Unlock()
		if !exists {
			_, _ = w.Write([]byte(`{}`)) // NDMS-side absence
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"show": map[string]any{
				"interface": map[string]any{"id": name, "type": "Wireguard"},
			},
		})
		return
	}

	f.mu.Lock()
	f.applyInterfaceCmd(single)
	f.mu.Unlock()
	_, _ = w.Write([]byte(`{}`))
}

// applyInterfaceCmd mutates known-interface state from an {"interface":{...}}
// command. Caller holds f.mu.
func (f *fakeNDMS) applyInterfaceCmd(cmd map[string]any) {
	iface, ok := cmd["interface"].(map[string]any)
	if !ok {
		return
	}
	name, _ := iface["name"].(string)
	if name == "" {
		return
	}
	if no, _ := iface["no"].(bool); no {
		delete(f.known, name)
		return
	}
	f.known[name] = true
}

func newCreateTestOperator(t *testing.T, f *fakeNDMS) *OperatorNativeWG {
	t.Helper()
	ndmsinfo.Reset() // Get()==nil -> Supports{HRanges,WireguardASC}() == false
	sem := transport.NewSemaphore(4)
	tr := transport.NewWithURL(f.srv.URL, sem)
	return &OperatorNativeWG{
		queries:     &query.Queries{Interfaces: query.NewInterfaceStore(tr, nil)},
		transport:   tr,
		kmod:        NewKmodManager(nil),
		appLog:      logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps),
		resolveFn:   func(string) (string, int, error) { return "203.0.113.10", 51820, nil },
		supportsASC: func() bool { return false },
	}
}

func testTunnel(id, name string) *storage.AWGTunnel {
	return &storage.AWGTunnel{
		ID:   id,
		Name: name,
		Interface: storage.AWGInterface{
			PrivateKey: "QFakePrivateKeyBase64EncodedValueAAAAAAAAAAA=",
			Address:    "10.8.0.2/32",
			MTU:        1280,
		},
		Peer: storage.AWGPeer{
			PublicKey:  "QFakePeerPublicKeyBase64EncodedValueAAAAAAA=",
			Endpoint:   "vpn.example.com:51820",
			AllowedIPs: []string{"0.0.0.0/0"},
		},
	}
}

// Symptom 1 (issue #255): two imports without a Start in between must NOT
// land on the same Wireguard index. Before the fix the InterfaceStore cache
// is never refreshed after Create, so the second nextFreeIndex re-reads the
// stale map and hands out the same slot.
func TestCreateViaBatch_BackToBack_DistinctIndices(t *testing.T) {
	f := newFakeNDMS(t, "Wireguard1") // a pre-existing system tunnel
	op := newCreateTestOperator(t, f)
	ctx := context.Background()

	idxA, err := op.Create(ctx, testTunnel("a", "CH"))
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	idxB, err := op.Create(ctx, testTunnel("b", "NL"))
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}

	if idxA == idxB {
		t.Fatalf("two back-to-back creates collided on Wireguard%d (want distinct indices)", idxA)
	}
	if idxA != 0 || idxB != 2 {
		t.Fatalf("got indices A=%d B=%d, want A=0 B=2 (lowest-free over {1, A})", idxA, idxB)
	}
}

// Symptom 2 (issue #255): deleting a tunnel must free its index for reuse
// without an AWGM restart. Before the fix Delete never tells the cache the
// interface is gone, so the freed slot is skipped and allocation walks up.
func TestCreateViaBatch_DeleteFreesIndexForReuse(t *testing.T) {
	f := newFakeNDMS(t, "Wireguard1")
	op := newCreateTestOperator(t, f)
	ctx := context.Background()

	a := testTunnel("a", "CH")
	idxA, err := op.Create(ctx, a)
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	a.NWGIndex = idxA // caller persists the assigned index

	idxB, err := op.Create(ctx, testTunnel("b", "NL"))
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	if idxB == idxA {
		t.Fatalf("precondition: A and B collided on Wireguard%d", idxA)
	}

	if err := op.Delete(ctx, a); err != nil {
		t.Fatalf("Delete A: %v", err)
	}

	idxC, err := op.Create(ctx, testTunnel("c", "DE"))
	if err != nil {
		t.Fatalf("Create C: %v", err)
	}
	if idxC != idxA {
		t.Fatalf("freed index Wireguard%d not reused: new tunnel got Wireguard%d", idxA, idxC)
	}
}
