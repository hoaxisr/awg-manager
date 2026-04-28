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

// stateAwareGetter answers /show/interface/ from the live SettingsStore so
// FindFreeIndex and listUsedSubnets see the latest set of managed servers
// across multiple Create calls. Other paths are unsupported (this fake
// covers exactly the surface Service.Create touches).
type stateAwareGetter struct {
	store *storage.SettingsStore
}

func (g *stateAwareGetter) Get(ctx context.Context, path string, out any) error {
	if path != "/show/interface/" {
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
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (g *stateAwareGetter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	return nil, errors.New("stateAwareGetter: GetRaw not faked: " + path)
}

// recordingPoster is a thread-safe variant of fakePoster — Create uses three
// POSTs per server and a parallel test would race the slice. Fresh instance
// per test keeps this simple.
type recordingPoster struct {
	mu    sync.Mutex
	posts []map[string]interface{}
	err   error
}

func (p *recordingPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	p.mu.Lock()
	if m, ok := payload.(map[string]interface{}); ok {
		p.posts = append(p.posts, m)
	}
	err := p.err
	p.mu.Unlock()
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
	getter := &stateAwareGetter{store: store}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{
		Interfaces: ifaces,
		Policies:   query.NewPolicyStore(getter, query.NopLogger()),
		WGServers:  query.NewWGServerStore(getter, query.NopLogger(), ifaces),
	}
	poster := &recordingPoster{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(poster, nil, queries, nil, store, log, nil)
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
		name        string
		req         CreateServerRequest
		wantErr     bool
		wantErrSub  string // substring expected in error message; empty = any error matches
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
