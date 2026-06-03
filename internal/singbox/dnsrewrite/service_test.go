package dnsrewrite

import (
	"encoding/json"
	"testing"
)

type fakeOrch struct {
	saved   map[string][]byte
	enabled map[string]bool
}

func newFakeOrch() *fakeOrch {
	return &fakeOrch{saved: map[string][]byte{}, enabled: map[string]bool{}}
}
func (f *fakeOrch) Save(slot string, data []byte) error   { f.saved[slot] = data; return nil }
func (f *fakeOrch) SetEnabled(slot string, on bool) error { f.enabled[slot] = on; return nil }

type fakeStore struct{ items []DNSRewrite }

func (s *fakeStore) List() ([]DNSRewrite, error)      { return s.items, nil }
func (s *fakeStore) Add(r DNSRewrite) error           { s.items = append(s.items, r); return nil }
func (s *fakeStore) Update(i int, r DNSRewrite) error { s.items[i] = r; return nil }
func (s *fakeStore) Delete(i int) error               { s.items = append(s.items[:i], s.items[i+1:]...); return nil }
func (s *fakeStore) Move(a, b int) error              { return nil }

func TestServiceAddFlushesCompiledRules(t *testing.T) {
	orch := newFakeOrch()
	svc := NewService(&fakeStore{}, orch, nil)

	if err := svc.Add(DNSRewrite{Pattern: "*.discord.media", IPs: []string{"104.25.158.178"}}); err != nil {
		t.Fatal(err)
	}

	data, ok := orch.saved[SlotName]
	if !ok {
		t.Fatal("slot not saved")
	}
	var slot struct {
		DNS struct {
			Rules []map[string]any `json:"rules"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(data, &slot); err != nil {
		t.Fatal(err)
	}
	// IPv4-only rewrite → A answer + AAAA NODATA suppression = 2 rules.
	if len(slot.DNS.Rules) != 2 {
		t.Fatalf("want 2 rules (answer + opposite-family NODATA), got %d", len(slot.DNS.Rules))
	}
	withAnswer := 0
	for _, r := range slot.DNS.Rules {
		if r["action"] != "predefined" {
			t.Errorf("rule not predefined: %#v", r)
		}
		if _, ok := r["answer"]; ok {
			withAnswer++
		}
	}
	if withAnswer != 1 {
		t.Errorf("want exactly 1 rule with an answer (the A family), got %d", withAnswer)
	}
	if !orch.enabled[SlotName] {
		t.Error("slot must be enabled after add")
	}
}

func TestServiceAddRejectsInvalidPattern(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, newFakeOrch(), nil)
	if err := svc.Add(DNSRewrite{Pattern: "finland10*", IPs: []string{"1.2.3.4"}}); err == nil {
		t.Error("invalid pattern must be rejected before store")
	}
	if len(store.items) != 0 {
		t.Error("invalid rewrite must not be stored")
	}
}

func TestServiceDeleteDisablesSlotWhenEmpty(t *testing.T) {
	orch := newFakeOrch()
	store := &fakeStore{items: []DNSRewrite{{Pattern: "a.lan", IPs: []string{"1.1.1.1"}}}}
	svc := NewService(store, orch, nil)
	if err := svc.Delete(0); err != nil {
		t.Fatal(err)
	}
	if orch.enabled[SlotName] {
		t.Error("slot must be disabled when no rewrites remain")
	}
}
