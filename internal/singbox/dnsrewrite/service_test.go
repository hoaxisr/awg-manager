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

type fakeStore struct {
	items        []DNSRewrite
	replaceCalls int
}

func (s *fakeStore) List() ([]DNSRewrite, error)      { return s.items, nil }
func (s *fakeStore) Add(r DNSRewrite) error           { s.items = append(s.items, r); return nil }
func (s *fakeStore) Update(i int, r DNSRewrite) error { s.items[i] = r; return nil }
func (s *fakeStore) Delete(i int) error               { s.items = append(s.items[:i], s.items[i+1:]...); return nil }
func (s *fakeStore) Move(a, b int) error              { return nil }
func (s *fakeStore) ReplaceManaged(id string, items []DNSRewrite) error {
	s.replaceCalls++
	out := make([]DNSRewrite, 0, len(s.items)+len(items))
	for _, r := range items {
		r.Managed = id
		out = append(out, r)
	}
	for _, r := range s.items {
		if r.Managed == id {
			continue
		}
		out = append(out, r)
	}
	s.items = out
	return nil
}

func TestSyncManagedKeenDNS_UpsertsAndClears(t *testing.T) {
	orch := newFakeOrch()
	store := &fakeStore{items: []DNSRewrite{{Pattern: "nas.lan", IPs: []string{"10.0.0.5"}}}}
	svc := NewService(store, orch, nil)

	if err := svc.SyncManagedKeenDNS("home.netcraze.pro", "192.168.1.1"); err != nil {
		t.Fatal(err)
	}
	// Managed идут ПЕРВЫМИ: sing-box берёт первое совпавшее DNS-правило.
	want := []string{"home.netcraze.pro", "*.home.netcraze.pro", "my.keenetic.net", "my.netcraze.net", "nas.lan"}
	if len(store.items) != len(want) {
		t.Fatalf("items = %d, want %d: %+v", len(store.items), len(want), store.items)
	}
	for i, p := range want {
		if store.items[i].Pattern != p {
			t.Errorf("items[%d].Pattern = %q, want %q", i, store.items[i].Pattern, p)
		}
	}
	for i := range want[:4] {
		if store.items[i].Managed != ManagedKeenDNS {
			t.Errorf("items[%d] not stamped managed: %+v", i, store.items[i])
		}
	}
	if store.items[4].Managed != "" {
		t.Errorf("user rewrite got stamped: %+v", store.items[4])
	}
	if !orch.enabled[SlotName] {
		t.Error("slot should stay enabled")
	}

	if err := svc.SyncManagedKeenDNS("", ""); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 || store.items[0].Pattern != "nas.lan" {
		t.Fatalf("after clear: %+v", store.items)
	}
}

// Reconcile зовёт sync каждые 30с. Повтор с теми же аргументами не должен
// трогать стор: каждая запись — writeAtomic на флеш + пересборка слота +
// SSE-инвалидация у фронта.
func TestSyncManagedKeenDNS_IdempotentNoWrite(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, newFakeOrch(), nil)

	if err := svc.SyncManagedKeenDNS("home.netcraze.pro", "192.168.1.1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := svc.SyncManagedKeenDNS("home.netcraze.pro", "192.168.1.1"); err != nil {
			t.Fatal(err)
		}
	}
	if store.replaceCalls != 1 {
		t.Fatalf("ReplaceManaged calls = %d, want 1 (repeats must be no-ops)", store.replaceCalls)
	}

	// Смена LAN IP — реальное изменение, запись обязана произойти.
	if err := svc.SyncManagedKeenDNS("home.netcraze.pro", "192.168.2.1"); err != nil {
		t.Fatal(err)
	}
	if store.replaceCalls != 2 {
		t.Fatalf("ReplaceManaged calls = %d, want 2 after LAN IP change", store.replaceCalls)
	}
	// И снос пустого набора при уже пустом сторе тоже не должен писать.
	if err := svc.SyncManagedKeenDNS("", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncManagedKeenDNS("", ""); err != nil {
		t.Fatal(err)
	}
	if store.replaceCalls != 3 {
		t.Fatalf("ReplaceManaged calls = %d, want 3 (second clear is a no-op)", store.replaceCalls)
	}
}

func TestServiceMoveRejectsManaged(t *testing.T) {
	store := &fakeStore{items: []DNSRewrite{
		{Pattern: "a.lan", IPs: []string{"1.1.1.1"}},
		{Pattern: "home.netcraze.pro", IPs: []string{"192.168.1.1"}, Managed: ManagedKeenDNS},
	}}
	svc := NewService(store, newFakeOrch(), nil)
	if err := svc.Move(1, 0); err == nil {
		t.Fatal("Move of managed rewrite must fail")
	}
	if err := svc.Move(0, 1); err == nil {
		t.Fatal("Move onto managed rewrite must fail")
	}
	if store.items[0].Pattern != "a.lan" || store.items[1].Managed != ManagedKeenDNS {
		t.Fatalf("store mutated on rejected move: %+v", store.items)
	}
}

func TestSyncManagedKeenDNS_NoDomainClears(t *testing.T) {
	store := &fakeStore{items: []DNSRewrite{
		{Pattern: "x.lan", IPs: []string{"1.1.1.1"}, Managed: ManagedKeenDNS},
	}}
	svc := NewService(store, newFakeOrch(), nil)
	if err := svc.SyncManagedKeenDNS("", "192.168.1.1"); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 0 {
		t.Fatalf("empty domain must clear managed, got %+v", store.items)
	}
}

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
