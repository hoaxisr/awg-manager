package dnsrewrite

import (
	"encoding/json"
	"errors"
	"testing"
)

type fakeOrch struct {
	saved     map[string][]byte
	enabled   map[string]bool
	saveErr   error
	saveCalls int
}

func newFakeOrch() *fakeOrch {
	return &fakeOrch{saved: map[string][]byte{}, enabled: map[string]bool{}}
}
func (f *fakeOrch) Save(slot string, data []byte) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved[slot] = data
	f.saveCalls++
	return nil
}
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
	for _, r := range s.items {
		if r.Managed == id {
			continue
		}
		out = append(out, r)
	}
	for _, r := range items {
		r.Managed = id
		out = append(out, r)
	}
	s.items = out
	return nil
}

func TestServiceMoveRejectsManaged(t *testing.T) {
	store := &fakeStore{items: []DNSRewrite{
		{Pattern: "a.lan", IPs: []string{"1.1.1.1"}},
		{Pattern: "home.netcraze.pro", IPs: []string{"192.168.1.1"}, Managed: ManagedKeenDNS},
	}}
	svc := NewService(store, newFakeOrch())
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

func TestServiceAddFlushesCompiledRules(t *testing.T) {
	orch := newFakeOrch()
	svc := NewService(&fakeStore{}, orch)

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
	svc := NewService(store, newFakeOrch())
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
	svc := NewService(store, orch)
	if err := svc.Delete(0); err != nil {
		t.Fatal(err)
	}
	if orch.enabled[SlotName] {
		t.Error("slot must be disabled when no rewrites remain")
	}
}

// slotOf распаковывает сохранённый слот в удобный для проверок вид.
func slotOf(t *testing.T, orch *fakeOrch) struct {
	DNS struct {
		Servers []map[string]any `json:"servers"`
		Rules   []map[string]any `json:"rules"`
	} `json:"dns"`
} {
	t.Helper()
	var slot struct {
		DNS struct {
			Servers []map[string]any `json:"servers"`
			Rules   []map[string]any `json:"rules"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(orch.saved[SlotName], &slot); err != nil {
		t.Fatal(err)
	}
	return slot
}

// Пресет отправляет имена KeenDNS резолверу самого роутера. Блок обязан идти
// ПЕРВЫМ правилом слота: sing-box берёт первое совпавшее, и широкий
// пользовательский паттерн (*.pro) иначе перехватил бы имя роутера.
func TestSetKeenDNSEnabled_AddsBlockFirstAndClears(t *testing.T) {
	orch := newFakeOrch()
	store := &fakeStore{items: []DNSRewrite{{Pattern: "*.pro", IPs: []string{"10.0.0.5"}}}}
	svc := NewService(store, orch)

	if err := svc.SetKeenDNSEnabled(true, "impod.netcraze.pro"); err != nil {
		t.Fatal(err)
	}
	slot := slotOf(t, orch)
	if len(slot.DNS.Servers) != 1 || slot.DNS.Servers[0]["tag"] != keenDNSServerTag ||
		slot.DNS.Servers[0]["server"] != "127.0.0.1" || slot.DNS.Servers[0]["type"] != "udp" {
		t.Fatalf("сервер пресета = %#v", slot.DNS.Servers)
	}
	first := slot.DNS.Rules[0]
	if first["server"] != keenDNSServerTag {
		t.Fatalf("первым правилом должен быть блок пресета, got %#v", first)
	}
	if len(store.items) != 1 || store.items[0].Managed != "" {
		t.Fatalf("пресет не должен писать в стор: %+v", store.items)
	}
	if !orch.enabled[SlotName] {
		t.Error("слот должен быть включён")
	}

	if err := svc.SetKeenDNSEnabled(false, ""); err != nil {
		t.Fatal(err)
	}
	slot = slotOf(t, orch)
	if len(slot.DNS.Servers) != 0 {
		t.Fatalf("после снятия пресета сервера быть не должно: %#v", slot.DNS.Servers)
	}
	if slot.DNS.Rules[0]["server"] == keenDNSServerTag {
		t.Fatalf("после снятия пресета правило осталось: %#v", slot.DNS.Rules[0])
	}
}

// FQDN роутера добавляется в правило, только если он не покрыт известными
// зонами — иначе это дубль.
func TestSetKeenDNSEnabled_ExtraDomain(t *testing.T) {
	orch := newFakeOrch()
	svc := NewService(&fakeStore{}, orch)

	if err := svc.SetKeenDNSEnabled(true, "Impod.Netcraze.Pro."); err != nil {
		t.Fatal(err)
	}
	domains := slotOf(t, orch).DNS.Rules[0]["domain"].([]any)
	if len(domains) != len(KeenDNSHosts()) {
		t.Fatalf("имя из известной зоны не должно попадать в domain: %#v", domains)
	}

	if err := svc.SetKeenDNSEnabled(true, "router.keenetic.center"); err != nil {
		t.Fatal(err)
	}
	domains = slotOf(t, orch).DNS.Rules[0]["domain"].([]any)
	if domains[len(domains)-1] != "router.keenetic.center" {
		t.Fatalf("имя вне известных зон обязано попасть в domain: %#v", domains)
	}
}

// Reconcile зовёт синк каждые 30с. Повтор с теми же аргументами не должен
// пересобирать слот: каждая пересборка — запись файла и SIGHUP sing-box.
func TestSetKeenDNSEnabled_IdempotentNoWrite(t *testing.T) {
	orch := newFakeOrch()
	svc := NewService(&fakeStore{}, orch)

	if err := svc.SetKeenDNSEnabled(true, ""); err != nil {
		t.Fatal(err)
	}
	calls := orch.saveCalls
	for i := 0; i < 3; i++ {
		if err := svc.SetKeenDNSEnabled(true, ""); err != nil {
			t.Fatal(err)
		}
	}
	if orch.saveCalls != calls {
		t.Fatalf("Save calls = %d, want %d (повторы обязаны быть no-op)", orch.saveCalls, calls)
	}
	if err := svc.SetKeenDNSEnabled(false, ""); err != nil {
		t.Fatal(err)
	}
	if orch.saveCalls != calls+1 {
		t.Fatalf("снятие пресета обязано пересобрать слот (calls %d → %d)", calls, orch.saveCalls)
	}
}

// Разовая уборка: до 2.18.0 пресет клал в стор перезаписи своего FQDN на
// LAN-адрес роутера — они и ломали доступ к его морде (issue #729).
func TestSetKeenDNSEnabled_DropsLegacyManaged(t *testing.T) {
	orch := newFakeOrch()
	store := &fakeStore{items: []DNSRewrite{
		{Pattern: "nas.lan", IPs: []string{"10.0.0.5"}},
		{Pattern: "impod.crazedns.ru", IPs: []string{"192.168.0.1"}, Managed: ManagedKeenDNS},
		{Pattern: "my.keenetic.net", IPs: []string{"192.168.0.1"}, Managed: ManagedKeenDNS},
	}}
	svc := NewService(store, orch)

	if err := svc.SetKeenDNSEnabled(false, ""); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 || store.items[0].Pattern != "nas.lan" {
		t.Fatalf("остались managed-записи: %+v", store.items)
	}
	if store.replaceCalls != 1 {
		t.Fatalf("ReplaceManaged calls = %d, want 1", store.replaceCalls)
	}
	// Повтор уже ничего не сносит и слот не трогает.
	calls := orch.saveCalls
	if err := svc.SetKeenDNSEnabled(false, ""); err != nil {
		t.Fatal(err)
	}
	if store.replaceCalls != 1 || orch.saveCalls != calls {
		t.Fatalf("повторная уборка не должна писать: replace=%d save=%d", store.replaceCalls, orch.saveCalls)
	}
}

// Если слот пересобрать не удалось, идемпотентный гвард не должен замкнуть
// накоротко: иначе слот остаётся протухшим навсегда.
func TestSetKeenDNSEnabled_RetriesAfterFailedFlush(t *testing.T) {
	orch := newFakeOrch()
	orch.saveErr = errors.New("no space left on device")
	svc := NewService(&fakeStore{}, orch)

	if err := svc.SetKeenDNSEnabled(true, ""); err == nil {
		t.Fatal("сбой флеша обязан всплыть")
	}
	orch.saveErr = nil
	if err := svc.SetKeenDNSEnabled(true, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := orch.saved[SlotName]; !ok {
		t.Fatal("слот обязан пересобраться на следующем синке после сбоя")
	}
}
