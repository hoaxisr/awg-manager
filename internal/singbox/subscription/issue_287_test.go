package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// raceMutator models the REAL adapter's scan-without-reserve allocation:
// AllocListenPort returns the lowest port not yet committed via AddInbound.
// Two Creates that interleave between allocate and commit would otherwise both
// get the same port — issue #287 Defect 1.
type raceMutator struct {
	fakeMutator
	mu        sync.Mutex
	committed map[uint16]bool
	nextProxy int
}

func (m *raceMutator) AllocListenPort() (uint16, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := uint16(11000)
	for m.committed[p] {
		p++
	}
	return p, nil
}

func (m *raceMutator) AllocProxyIndex(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextProxy++
	return m.nextProxy, nil
}

func (m *raceMutator) AddInbound(tag string, body []byte) error {
	m.mu.Lock()
	var ib map[string]any
	_ = json.Unmarshal(body, &ib)
	if p, ok := toAnyInt(ib["listen_port"]); ok {
		if m.committed == nil {
			m.committed = map[uint16]bool{}
		}
		m.committed[uint16(p)] = true
	}
	m.mu.Unlock()
	return nil
}

// Issue #287 Defect 1 — concurrent Create must not hand out the same
// listen_port. createMu serializes Create so the first commits its inbound
// (reserving the port) before the second allocates.
func TestIssue287_ConcurrentCreate_DistinctListenPorts(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, &raceMutator{})

	const inline = "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h\n"
	var wg sync.WaitGroup
	ports := make([]uint16, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sub, cerr := svc.Create(context.Background(), CreateInput{Label: "x", Inline: inline})
			if cerr != nil {
				t.Errorf("Create %d: %v", idx, cerr)
				return
			}
			ports[idx] = sub.ListenPort
		}(i)
	}
	wg.Wait()

	if ports[0] == 0 || ports[1] == 0 {
		t.Fatalf("a Create failed to allocate a port: %v", ports)
	}
	if ports[0] == ports[1] {
		t.Fatalf("BUG #287: two concurrent Creates shared listen_port %d", ports[0])
	}
}

// Issue #287 defense-in-depth — AddInbound rejects a second inbound on a
// listen_port already taken by a different tag.
func TestIssue287_AddInbound_RejectsDuplicateListenPort(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	a := NewOperatorAdapter(orch, nil, nil)

	// A slot with an inbound but zero outbounds fails Pass-1 validation, so
	// seed one valid outbound first.
	validVless := []byte(`{"type":"vless","server":"ex.com","server_port":443,"uuid":"3a3b1c2e-9999-4321-aaaa-1234567890ab","tls":{"enabled":true,"server_name":"h"}}`)
	if err := a.AddOutbound("sub-aaaa-x", validVless); err != nil {
		t.Fatalf("seed outbound: %v", err)
	}

	if err := a.AddInbound("sub-aaaa-in", BuildMixedInbound("sub-aaaa-in", 11000)); err != nil {
		t.Fatalf("first AddInbound: %v", err)
	}
	if err := a.AddInbound("sub-bbbb-in", BuildMixedInbound("sub-bbbb-in", 11000)); err == nil {
		t.Fatal("BUG #287: AddInbound accepted a duplicate listen_port 11000")
	}

	count := 0
	for _, v := range a.cfg.Inbounds {
		if ib, ok := v.(map[string]any); ok {
			if p, ok := toAnyInt(ib["listen_port"]); ok && p == 11000 {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("want exactly 1 inbound on port 11000, got %d", count)
	}
}

// reloadFailMutator is a fakeMutator whose Reload (the batch commit) fails —
// modelling a Create that errors at commit (slow link, or a duplicate
// listen_port the daemon rejects). DeclaredOutboundTags mirrors the live slot
// (added minus removed); Rollback discards the uncommitted batch.
type reloadFailMutator struct {
	fakeMutator
}

func (m *reloadFailMutator) Reload(context.Context) error {
	return errors.New("simulated reload failure")
}

// Rollback models the real adapter discarding the uncommitted batch (#331):
// this fake never commits (Reload always fails), so everything added since
// start is uncommitted and is discarded here. The Create-failure path now
// calls this instead of the old per-tag purge; if it were not wired, the
// orphan check would still see the added outbound.
func (m *reloadFailMutator) Rollback() {
	m.addedOutbounds = nil
	m.addedInbounds = nil
	m.addedRules = 0
}

func (m *reloadFailMutator) DeclaredOutboundTags() []string {
	removed := map[string]bool{}
	for _, t := range m.removedOutbounds {
		removed[t] = true
	}
	var live []string
	for _, t := range m.addedOutbounds {
		if !removed[t] {
			live = append(live, t)
		}
	}
	return live
}

// Issue #287 Defect 2 — a failed Create must not leave orphan outbounds in the
// subscription slot.
func TestIssue287_FailedCreate_LeavesNoOrphans(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	if err != nil {
		t.Fatal(err)
	}
	mut := &reloadFailMutator{}
	svc := NewService(store, mut)

	_, createErr := svc.Create(context.Background(), CreateInput{
		Label:  "mifa",
		Inline: "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h\n",
	})
	if createErr == nil {
		t.Fatal("expected Create to fail (Reload errors), got nil")
	}
	if len(store.List()) != 0 {
		t.Errorf("store should be empty after failed Create, has %d", len(store.List()))
	}

	removed := map[string]bool{}
	for _, tag := range mut.removedOutbounds {
		removed[tag] = true
	}
	var orphans []string
	for _, tag := range mut.addedOutbounds {
		if !removed[tag] {
			orphans = append(orphans, tag)
		}
	}
	if len(orphans) > 0 {
		t.Fatalf("BUG #287: failed Create left %d orphan outbound(s): %v", len(orphans), orphans)
	}
}
