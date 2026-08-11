package storage

import (
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
)

func TestDNSRewriteStoreCRUD(t *testing.T) {
	s := NewDNSRewriteStore(filepath.Join(t.TempDir(), "dns-rewrites.json"))

	if err := s.Add(dnsrewrite.DNSRewrite{Pattern: "a.lan", IPs: []string{"1.1.1.1"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(dnsrewrite.DNSRewrite{Pattern: "*.b.com", IPs: []string{"2.2.2.2"}}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 2 || list[0].Pattern != "a.lan" {
		t.Fatalf("list: %#v", list)
	}
	if err := s.Update(0, dnsrewrite.DNSRewrite{Pattern: "a2.lan", IPs: []string{"1.1.1.2"}}); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List()
	if list[0].Pattern != "a2.lan" {
		t.Errorf("update failed: %#v", list[0])
	}
	if err := s.Move(1, 0); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List()
	if list[0].Pattern != "*.b.com" {
		t.Errorf("move failed: %#v", list)
	}
	if err := s.Delete(0); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List()
	if len(list) != 1 || list[0].Pattern != "a2.lan" {
		t.Errorf("delete failed: %#v", list)
	}
	// out-of-range guards
	if err := s.Update(99, dnsrewrite.DNSRewrite{Pattern: "z"}); err == nil {
		t.Error("update out of range must fail")
	}
	if err := s.Delete(99); err == nil {
		t.Error("delete out of range must fail")
	}
}

func TestDNSRewriteStoreMoveForward(t *testing.T) {
	s := NewDNSRewriteStore(filepath.Join(t.TempDir(), "dns-rewrites.json"))
	_ = s.Add(dnsrewrite.DNSRewrite{Pattern: "a", IPs: []string{"1.1.1.1"}})
	_ = s.Add(dnsrewrite.DNSRewrite{Pattern: "b", IPs: []string{"2.2.2.2"}})
	_ = s.Add(dnsrewrite.DNSRewrite{Pattern: "c", IPs: []string{"3.3.3.3"}})

	// from < to: Move(0,2) on [a,b,c] => [b,c,a]
	if err := s.Move(0, 2); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	got := []string{list[0].Pattern, list[1].Pattern, list[2].Pattern}
	if got[0] != "b" || got[1] != "c" || got[2] != "a" {
		t.Errorf("Move(0,2) => %v, want [b c a]", got)
	}

	// Move out-of-range must fail
	if err := s.Move(0, 99); err == nil {
		t.Error("Move out of range must fail")
	}
}

func TestDNSRewriteStoreReplaceManaged(t *testing.T) {
	s := NewDNSRewriteStore(filepath.Join(t.TempDir(), "dns-rewrites.json"))
	_ = s.Add(dnsrewrite.DNSRewrite{Pattern: "nas.lan", IPs: []string{"10.0.0.5"}})
	if err := s.ReplaceManaged(dnsrewrite.ManagedKeenDNS, []dnsrewrite.DNSRewrite{
		{Pattern: "home.netcraze.pro", IPs: []string{"192.168.1.1"}},
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	// Managed лежат в хвосте: индексы пользовательских записей не должны
	// ехать от появления/снятия managed-набора.
	if len(list) != 2 || list[1].Managed != dnsrewrite.ManagedKeenDNS ||
		list[0].Pattern != "nas.lan" || list[1].Pattern != "home.netcraze.pro" {
		t.Fatalf("after upsert: %+v", list)
	}
	if err := s.ReplaceManaged(dnsrewrite.ManagedKeenDNS, nil); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List()
	if len(list) != 1 || list[0].Pattern != "nas.lan" {
		t.Fatalf("after clear: %+v", list)
	}
}

func TestDNSRewriteStorePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns-rewrites.json")
	s1 := NewDNSRewriteStore(path)
	_ = s1.Add(dnsrewrite.DNSRewrite{Pattern: "x.lan", IPs: []string{"9.9.9.9"}})

	s2 := NewDNSRewriteStore(path)
	list, _ := s2.List()
	if len(list) != 1 || list[0].Pattern != "x.lan" {
		t.Errorf("not persisted: %#v", list)
	}
}
