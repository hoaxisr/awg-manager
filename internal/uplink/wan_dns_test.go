package uplink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWANDNSIsolationPersistenceAndRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns.json")
	d, e := OpenWANDNS(path)
	if e != nil {
		t.Fatal(e)
	}
	calls := []string{}
	automatic := func(_ context.Context, device string) ([]string, error) {
		calls = append(calls, device)
		return nil, ErrNoExplicitProviderDNS
	}
	if _, e = d.Resolve(context.Background(), "ISP1", "wan1", automatic); !errors.Is(e, ErrNoExplicitProviderDNS) {
		t.Fatal(e)
	}
	v, e := d.put(WANDNSUpdate{Provider: "ISP1", Servers: []string{"1.1.1.1:53", "8.8.8.8:5353"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.put(WANDNSUpdate{Provider: "ISP1", Servers: []string{"9.9.9.9"}}); !errors.Is(e, ErrConflict) {
		t.Fatal("stale update accepted", e)
	}
	d, e = OpenWANDNS(path)
	if e != nil {
		t.Fatal(e)
	}
	got, e := d.Resolve(context.Background(), "ISP1", "wan1", automatic)
	if e != nil || !reflect.DeepEqual(got, []string{"1.1.1.1", "8.8.8.8:5353"}) {
		t.Fatal(got, e)
	}
	got[0] = "mutated"
	v.Overrides["ISP1"][0] = "mutated"
	if d.Snapshot().Overrides["ISP1"][0] != "1.1.1.1" {
		t.Fatal("mutable config escaped")
	}
	if _, e = d.Resolve(context.Background(), "ISP2", "wan2", automatic); !errors.Is(e, ErrNoExplicitProviderDNS) || !reflect.DeepEqual(calls, []string{"wan1", "wan2"}) {
		t.Fatal("cross-WAN substitution", calls, e)
	}
	if _, e = d.put(WANDNSUpdate{Provider: "ISP1", Revision: 1}); e != nil {
		t.Fatal(e)
	}
	if _, e = d.Resolve(context.Background(), "ISP1", "wan1", automatic); !errors.Is(e, ErrNoExplicitProviderDNS) {
		t.Fatal("automatic mode not restored", e)
	}
}
func TestWANDNSRejectsInvalidAndFailedWrites(t *testing.T) {
	d, _ := OpenWANDNS(filepath.Join(t.TempDir(), "dns.json"))
	for _, servers := range [][]string{{"dns.example"}, {"127.0.0.1"}, {"0.0.0.0"}, {"1.1.1.1:0"}, {"1.1.1.1", "1.1.1.1:53"}} {
		if _, e := d.put(WANDNSUpdate{Provider: "ISP", Servers: servers}); e == nil {
			t.Fatal(servers)
		}
	}
	if d.Snapshot().Revision != 0 {
		t.Fatal("invalid mutation")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0600)
	d.path = filepath.Join(blocker, "dns.json")
	if _, e := d.put(WANDNSUpdate{Provider: "ISP", Servers: []string{"1.1.1.1"}}); e == nil || d.Snapshot().Revision != 0 {
		t.Fatal("failed disk write published")
	}
}

type dnsConsumerAdapter struct{ *testAdapter }

func (dnsConsumerAdapter) UplinkCapabilities(Target) Capabilities {
	return Capabilities{ProviderDNS: true}
}
func TestWANDNSReappliesOnlyCurrentProxyAndPreservesConflict(t *testing.T) {
	c, a, _, _ := fixture(t)
	p := c.store.List()[0]
	delete(c.adapters, "test")
	p.Target.Engine = "firc"
	c.adapters["firc"] = dnsConsumerAdapter{a}
	// Replace only the test fixture's unpublished store identity.
	c.store.policies[p.ID] = p
	c.Tick(context.Background())
	a.activated = nil
	c.WANDNS, _ = OpenWANDNS(filepath.Join(t.TempDir(), "dns.json"))
	c.paused.Store(true)
	if _, e := c.UpdateWANDNS(context.Background(), WANDNSUpdate{Provider: "ISP1", Servers: []string{"1.1.1.1"}}); !errors.Is(e, ErrConflict) || c.WANDNS.Snapshot().Revision != 0 {
		t.Fatal("backup pause bypassed", e)
	}
	c.paused.Store(false)
	if _, e := c.UpdateWANDNS(context.Background(), WANDNSUpdate{Provider: "unknown", Servers: []string{"1.1.1.1"}}); e == nil {
		t.Fatal("foreign WAN accepted")
	}
	if _, e := c.UpdateWANDNS(context.Background(), WANDNSUpdate{Provider: "ISP2", Servers: []string{"1.1.1.1"}}); e != nil {
		t.Fatal(e)
	}
	c.Tick(context.Background())
	if len(a.activated) != 0 {
		t.Fatal("unrelated path restarted")
	}
	if _, e := c.UpdateWANDNS(context.Background(), WANDNSUpdate{Provider: "ISP1", Revision: 1, Servers: []string{"1.1.1.1"}}); e != nil {
		t.Fatal(e)
	}
	c.Tick(context.Background())
	if !reflect.DeepEqual(a.activated, []string{"wan1"}) || state(c).Phase != "healthy" {
		t.Fatal(a.activated, state(c))
	}
	a.activated = nil
	c.UpdateWANDNS(context.Background(), WANDNSUpdate{Provider: "ISP1", Revision: 2})
	a.device = "foreign"
	c.Tick(context.Background())
	if state(c).Phase != "conflict" || len(a.activated) != 0 {
		t.Fatal("external edit overwritten", state(c))
	}
}
